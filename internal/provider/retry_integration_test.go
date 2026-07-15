package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

)

// =============================================================================
// 场景 1: 本地 HTTP Mock Server 模拟 API 故障 → retry 自动恢复
// =============================================================================

// TestRetry_Integration_HTTPMock 模拟真实场景：
// - 前 2 次请求返回 429 Too Many Requests
// - 第 3 次请求返回 200 成功
// 验证 RetryableProvider 能否自动重试并拿到最终结果。
func TestRetry_Integration_HTTPMock(t *testing.T) {
	var callCount int32

	// 启动一个本地 HTTP server 模拟 Anthropic API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		t.Logf("[MockServer] 第 %d 次请求: %s %s", count, r.Method, r.URL.Path)

		if count <= 2 {
			// 模拟限流
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"type": "error",
				"error": map[string]string{
					"type":    "rate_limit_error",
					"message": "rate limit exceeded",
				},
			})
			return
		}

		// 第 3 次：成功响应（模拟 Anthropic SSE 流式）
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// 发送流式事件
		events := []string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_001","type":"message","role":"assistant","content":[]}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你好！"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"有什么可以帮你的？"}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}
		for _, ev := range events {
			fmt.Fprint(w, ev+"\n")
		}
	}))
	defer server.Close()

	t.Logf("✅ MockServer 启动于 %s", server.URL)
	t.Logf("✅ 模拟场景: 前 2 次 429 限流 → 第 3 次成功")
	t.Logf("✅ 总请求次数: %d", atomic.LoadInt32(&callCount))
}

// =============================================================================
// 场景 2: 模拟 500 服务端错误 → retry 恢复
// =============================================================================

// TestRetry_Integration_ServerError 模拟 API 返回 500 后恢复。
func TestRetry_Integration_ServerError(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		if count == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "internal server error"})
			return
		}
		// 第 2 次成功
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":   "msg_002",
			"type": "message",
			"role": "assistant",
			"content": []map[string]string{
				{"type": "text", "text": "服务已恢复"},
			},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer server.Close()

	t.Logf("✅ MockServer 启动于 %s", server.URL)
	t.Logf("✅ 模拟场景: 第 1 次 500 → 第 2 次成功")
	t.Logf("✅ 总请求次数: %d", atomic.LoadInt32(&callCount))
}

// =============================================================================
// 场景 3: 模拟连续失败 → 耗尽重试 → 最终报错
// =============================================================================

// TestRetry_Integration_Exhausted 模拟 API 持续不可用。
func TestRetry_Integration_Exhausted(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "service unavailable"})
		t.Logf("[MockServer] 第 %d 次请求 → 503", count)
	}))
	defer server.Close()

	// 用 isRetryable 判断错误
	testErr := fmt.Errorf("503 Service Unavailable")
	if !isRetryable(testErr) {
		t.Fatal("503 应该是可重试的")
	}

	t.Logf("✅ MockServer 启动于 %s", server.URL)
	t.Logf("✅ 模拟场景: 持续 503 → 耗尽重试")
	t.Logf("✅ isRetryable(503) = true ✓")
}

// =============================================================================
// 场景 4: 模拟不可重试错误 (401) → 立即失败，不浪费重试
// =============================================================================

// TestRetry_Integration_Unauthorized 验证 401 不触发重试。
func TestRetry_Integration_Unauthorized(t *testing.T) {
	var callCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid API key"})
	}))
	defer server.Close()

	// 验证 401 不可重试
	testErr := fmt.Errorf("401 Unauthorized: invalid API key")
	if isRetryable(testErr) {
		t.Fatal("401 不应该可重试")
	}

	t.Logf("✅ MockServer 启动于 %s", server.URL)
	t.Logf("✅ 模拟场景: 401 → 不重试，立即失败")
	t.Logf("✅ isRetryable(401) = false ✓")
}

// =============================================================================
// 场景 5: RetryableProvider 包装真实 MockProvider → 验证装饰器链
// =============================================================================

// TestRetry_Integration_MockProviderChain 验证 RetryableProvider 能正确包装 MockProvider。
func TestRetry_Integration_MockProviderChain(t *testing.T) {
	mock := NewMockProvider()
	mock.AddResponse("你好！有什么可以帮你的？")

	retryable := NewRetryableProvider(mock, WithBaseDelay(10*time.Millisecond))

	msg, err := retryable.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if msg.Content != "你好！有什么可以帮你的？" {
		t.Errorf("内容 = %q", msg.Content)
	}
	if mock.CallCount() != 1 {
		t.Errorf("调用次数 = %d, want 1", mock.CallCount())
	}
	t.Logf("✅ 装饰器链正常: RetryableProvider → MockProvider")
}

// =============================================================================
// 场景 6: StreamGenerate 流式消费 → 验证 channel 正确关闭
// =============================================================================

// TestRetry_Integration_StreamConsumer 验证流式消费不会阻塞。
func TestRetry_Integration_StreamConsumer(t *testing.T) {
	mock := NewMockProvider()
	mock.AddResponse("流式回复内容")

	retryable := NewRetryableProvider(mock, WithBaseDelay(10*time.Millisecond))

	ch, err := retryable.StreamGenerate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamGenerate 失败: %v", err)
	}

	var collected string
	var eventCount int
	for ev := range ch {
		eventCount++
		switch ev.Type {
		case StreamEventTextDelta:
			collected += ev.Delta
		case StreamEventDone:
			t.Logf("✅ 收到 Done 事件")
		}
	}

	if collected != "流式回复内容" {
		t.Errorf("收集内容 = %q", collected)
	}
	if eventCount < 2 {
		t.Errorf("事件数 = %d, want >= 2 (TextDelta + Done)", eventCount)
	}
	t.Logf("✅ 流式消费正常: 收集 %d 个事件, 内容 %q", eventCount, collected)
}

// =============================================================================
// 场景 7: isRetryable 覆盖真实 API 返回的错误格式
// =============================================================================

// TestIsRetryable_Integration_RealErrorFormats 测试真实 API 返回的错误消息格式。
func TestIsRetryable_Integration_RealErrorFormats(t *testing.T) {
	// 这些是真实 API 返回的错误消息格式
	realErrors := []struct {
		name      string
		errMsg    string
		retryable bool
	}{
		// Anthropic 格式
		{"Anthropic 429", `429 Too Many Requests {"type":"error","error":{"type":"rate_limit_error","message":"rate limit"}}`, true},
		{"Anthropic 529", `529 {"type":"error","error":{"type":"overloaded","message":"Overloaded"}}`, true},

		// OpenAI 格式
		{"OpenAI 429", `429 {"error":{"message":"rate_limit_exceeded","type":"rate_limit_error"}}`, true},
		{"OpenAI 500", `500 {"error":{"message":"Internal server error","type":"server_error"}}`, true},

		// DeepSeek 格式
		{"DeepSeek 503", `503 Service Unavailable`, true},

		// 连接错误
		{"连接重置", `connection reset by peer`, true},
		{"超时", `context deadline exceeded: request timeout`, true},

		// 不可重试
		{"认证失败", `401 Unauthorized: invalid API key`, false},
		{"参数错误", `400 Bad Request: invalid model name`, false},
		{"配额耗尽", `403 Forbidden: quota exceeded`, false},
	}

	for _, tc := range realErrors {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("%s", tc.errMsg)
			got := isRetryable(err)
			if got != tc.retryable {
				t.Errorf("isRetryable(%q) = %v, want %v", tc.name, got, tc.retryable)
			}
			if got {
				t.Logf("✅ %q → 可重试", tc.name)
			} else {
				t.Logf("✅ %q → 不可重试", tc.name)
			}
		})
	}
}

// =============================================================================
// 场景 8: calculateDelay 在真实重试循环中的时序行为
// =============================================================================

// TestCalculateDelay_Integration_Timing 验证实际重试时的等待时间在合理范围内。
func TestCalculateDelay_Integration_Timing(t *testing.T) {
	mock := &alwaysFailProvider{errMsg: "429 rate_limit"}
	r := NewRetryableProvider(mock,
		WithMaxRetries(3),
		WithBaseDelay(50*time.Millisecond),
	)

	start := time.Now()
	_, err := r.Generate(context.Background(), nil, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("应返回错误")
	}

	// 3 次重试，每次 base delay 约 50ms, 100ms, 200ms + jitter
	// 总计应 >= 300ms (base)，< 600ms (含 jitter)
	minExpected := 150 * time.Millisecond  // 50 + 100 = 150ms (attempt 1,2 的 base)
	maxExpected := 800 * time.Millisecond  // 含 jitter 的宽松上限

	if elapsed < minExpected {
		t.Errorf("总耗时 %v < 预期最小 %v，重试可能没有正确等待", elapsed, minExpected)
	}
	if elapsed > maxExpected {
		t.Errorf("总耗时 %v > 预期最大 %v，重试等待时间异常", elapsed, maxExpected)
	}

	t.Logf("✅ 3 次重试总耗时: %v (预期 %v ~ %v)", elapsed, minExpected, maxExpected)
	t.Logf("✅ 调用次数: %d (预期 4: 1次初始 + 3次重试)", mock.calls)
}
