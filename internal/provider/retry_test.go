package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// =============================================================================
// 辅助 Mock：可控失败的 Provider
// =============================================================================

// failThenSucceedProvider 前 N 次调用返回错误，之后返回成功。
type failThenSucceedProvider struct {
	failCount int
	calls     int
	lastMsg   *schema.Message
	errMsg    string
}

func (p *failThenSucceedProvider) Generate(ctx context.Context, messages []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	p.calls++
	if p.calls <= p.failCount {
		return nil, fmt.Errorf("%s", p.errMsg)
	}
	return p.lastMsg, nil
}

func (p *failThenSucceedProvider) StreamGenerate(ctx context.Context, messages []schema.Message, tools []schema.ToolDefinition) (<-chan StreamEvent, error) {
	p.calls++
	if p.calls <= p.failCount {
		return nil, fmt.Errorf("%s", p.errMsg)
	}
	ch := make(chan StreamEvent, 2)
	go func() {
		defer close(ch)
		ch <- StreamEvent{Type: StreamEventTextDelta, Delta: p.lastMsg.Content}
		ch <- StreamEvent{Type: StreamEventDone}
	}()
	return ch, nil
}

// alwaysFailProvider 总是返回指定错误。
type alwaysFailProvider struct {
	errMsg string
	calls  int
}

func (p *alwaysFailProvider) Generate(ctx context.Context, messages []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	p.calls++
	return nil, fmt.Errorf("%s", p.errMsg)
}

func (p *alwaysFailProvider) StreamGenerate(ctx context.Context, messages []schema.Message, tools []schema.ToolDefinition) (<-chan StreamEvent, error) {
	p.calls++
	return nil, fmt.Errorf("%s", p.errMsg)
}

// =============================================================================
// isRetryable 测试
// =============================================================================

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name     string
		errMsg   string
		expected bool
	}{
		// 可重试的错误
		{"429 Too Many Requests", "429 Too Many Requests", true},
		{"rate_limit", "rate_limit exceeded", true},
		{"500 Internal Server Error", "500 Internal Server Error", true},
		{"internal server error", "internal server error occurred", true},
		{"502 Bad Gateway", "502 Bad Gateway", true},
		{"bad gateway", "bad gateway error", true},
		{"503 Service Unavailable", "503 Service Unavailable", true},
		{"service unavailable", "service unavailable temporarily", true},
		{"529 overloaded", "529 overloaded", true},
		{"overloaded", "server overloaded", true},
		{"connection reset", "connection reset by peer", true},
		{"timeout", "request timeout", true},

		// 不可重试的错误
		{"400 Bad Request", "400 Bad Request", false},
		{"401 Unauthorized", "401 Unauthorized: invalid API key", false},
		{"403 Forbidden", "403 Forbidden", false},
		{"404 Not Found", "404 Not Found", false},
		{"业务错误", "参数格式不正确", false},
		{"nil error", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.errMsg != "" {
				err = fmt.Errorf("%s", tt.errMsg)
			}
			got := isRetryable(err)
			if got != tt.expected {
				t.Errorf("isRetryable(%q) = %v, want %v", tt.errMsg, got, tt.expected)
			}
		})
	}
}

func TestIsRetryable_NilError(t *testing.T) {
	if isRetryable(nil) {
		t.Error("isRetryable(nil) 应返回 false")
	}
}

// =============================================================================
// calculateDelay 测试
// =============================================================================

func TestCalculateDelay_ExponentialBackoff(t *testing.T) {
	r := &RetryableProvider{baseDelay: 1 * time.Second}

	// attempt=1: base = 1s * 2^0 = 1s, jitter ∈ [0, 0.5s)
	// attempt=2: base = 1s * 2^1 = 2s, jitter ∈ [0, 1s)
	// attempt=3: base = 1s * 2^2 = 4s, jitter ∈ [0, 2s)

	for attempt := 1; attempt <= 3; attempt++ {
		delay := r.calculateDelay(attempt)
		expectedBase := time.Duration(1<<uint(attempt-1)) * time.Second

		// delay 应在 [base, base + base/2) 范围内
		if delay < expectedBase {
			t.Errorf("attempt=%d: delay %v < base %v", attempt, delay, expectedBase)
		}
		maxDelay := expectedBase + expectedBase/2
		if delay >= maxDelay {
			t.Errorf("attempt=%d: delay %v >= max %v", attempt, delay, maxDelay)
		}
		t.Logf("✅ attempt=%d: delay=%v (base=%v)", attempt, delay, expectedBase)
	}
}

func TestCalculateDelay_CustomBaseDelay(t *testing.T) {
	r := &RetryableProvider{baseDelay: 500 * time.Millisecond}

	delay := r.calculateDelay(1) // base = 500ms * 2^0 = 500ms
	if delay < 500*time.Millisecond {
		t.Errorf("delay %v < 500ms", delay)
	}
	t.Logf("✅ 自定义 baseDelay: delay=%v", delay)
}

// =============================================================================
// NewRetryableProvider 选项测试
// =============================================================================

func TestNewRetryableProvider_Defaults(t *testing.T) {
	mock := &failThenSucceedProvider{failCount: 0, lastMsg: &schema.Message{Content: "ok"}}
	r := NewRetryableProvider(mock)

	if r.maxRetries != 3 {
		t.Errorf("默认 maxRetries = %d, want 3", r.maxRetries)
	}
	if r.baseDelay != 1*time.Second {
		t.Errorf("默认 baseDelay = %v, want 1s", r.baseDelay)
	}
	t.Log("✅ 默认配置正确")
}

func TestNewRetryableProvider_WithOptions(t *testing.T) {
	mock := &failThenSucceedProvider{failCount: 0, lastMsg: &schema.Message{Content: "ok"}}
	r := NewRetryableProvider(mock,
		WithMaxRetries(5),
		WithBaseDelay(2*time.Second),
	)

	if r.maxRetries != 5 {
		t.Errorf("maxRetries = %d, want 5", r.maxRetries)
	}
	if r.baseDelay != 2*time.Second {
		t.Errorf("baseDelay = %v, want 2s", r.baseDelay)
	}
	t.Log("✅ 自定义选项生效")
}

func TestNewRetryableProvider_IgnoreInvalidOptions(t *testing.T) {
	mock := &failThenSucceedProvider{failCount: 0, lastMsg: &schema.Message{Content: "ok"}}
	r := NewRetryableProvider(mock,
		WithMaxRetries(-1),  // 无效，应忽略
		WithBaseDelay(0),    // 无效，应忽略
	)

	if r.maxRetries != 3 {
		t.Errorf("maxRetries = %d, want 3 (忽略负数)", r.maxRetries)
	}
	if r.baseDelay != 1*time.Second {
		t.Errorf("baseDelay = %v, want 1s (忽略零值)", r.baseDelay)
	}
	t.Log("✅ 无效选项被忽略")
}

// =============================================================================
// Generate 重试测试
// =============================================================================

func TestRetryableProvider_Generate_Success(t *testing.T) {
	mock := &failThenSucceedProvider{
		failCount: 0,
		lastMsg:   &schema.Message{Content: "你好"},
	}
	r := NewRetryableProvider(mock, WithBaseDelay(10*time.Millisecond))

	msg, err := r.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("首次成功不应报错: %v", err)
	}
	if msg.Content != "你好" {
		t.Errorf("内容 = %q, want %q", msg.Content, "你好")
	}
	if mock.calls != 1 {
		t.Errorf("调用次数 = %d, want 1", mock.calls)
	}
	t.Log("✅ 首次成功，无重试")
}

func TestRetryableProvider_Generate_RetryThenSucceed(t *testing.T) {
	mock := &failThenSucceedProvider{
		failCount: 2,
		lastMsg:   &schema.Message{Content: "成功"},
		errMsg:    "429 Too Many Requests",
	}
	r := NewRetryableProvider(mock, WithMaxRetries(3), WithBaseDelay(10*time.Millisecond))

	msg, err := r.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("重试后成功不应报错: %v", err)
	}
	if msg.Content != "成功" {
		t.Errorf("内容 = %q, want %q", msg.Content, "成功")
	}
	if mock.calls != 3 {
		t.Errorf("调用次数 = %d, want 3 (2次失败+1次成功)", mock.calls)
	}
	t.Logf("✅ 重试 %d 次后成功", mock.calls-1)
}

func TestRetryableProvider_Generate_ExhaustRetries(t *testing.T) {
	mock := &alwaysFailProvider{
		errMsg: "503 Service Unavailable",
	}
	r := NewRetryableProvider(mock, WithMaxRetries(2), WithBaseDelay(10*time.Millisecond))

	_, err := r.Generate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("耗尽重试后应报错")
	}
	if !strings.Contains(err.Error(), "重试 2 次后仍然失败") {
		t.Errorf("错误信息 = %q, 应包含'重试 2 次后仍然失败'", err.Error())
	}
	if mock.calls != 3 {
		t.Errorf("调用次数 = %d, want 3 (maxRetries+1)", mock.calls)
	}
	t.Logf("✅ 耗尽重试: %v", err)
}

func TestRetryableProvider_Generate_NonRetryableError(t *testing.T) {
	mock := &alwaysFailProvider{
		errMsg: "401 Unauthorized: invalid API key",
	}
	r := NewRetryableProvider(mock, WithMaxRetries(3), WithBaseDelay(10*time.Millisecond))

	_, err := r.Generate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("不可重试错误应立即返回")
	}
	if mock.calls != 1 {
		t.Errorf("调用次数 = %d, want 1 (不可重试，立即失败)", mock.calls)
	}
	t.Logf("✅ 不可重试错误立即返回: %v", err)
}

func TestRetryableProvider_Generate_ContextCancelled(t *testing.T) {
	mock := &alwaysFailProvider{
		errMsg: "429 rate_limit",
	}
	r := NewRetryableProvider(mock, WithMaxRetries(5), WithBaseDelay(1*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	// 第一次调用后取消 context
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := r.Generate(ctx, nil, nil)
	if err == nil {
		t.Fatal("context 取消应返回错误")
	}
	if err != context.Canceled {
		t.Errorf("错误 = %v, want context.Canceled", err)
	}
	t.Logf("✅ context 取消正确传播: %v", err)
}

// =============================================================================
// StreamGenerate 重试测试
// =============================================================================

func TestRetryableProvider_StreamGenerate_Success(t *testing.T) {
	mock := &failThenSucceedProvider{
		failCount: 0,
		lastMsg:   &schema.Message{Content: "流式内容"},
	}
	r := NewRetryableProvider(mock, WithBaseDelay(10*time.Millisecond))

	ch, err := r.StreamGenerate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("首次成功不应报错: %v", err)
	}

	var content string
	for ev := range ch {
		if ev.Type == StreamEventTextDelta {
			content += ev.Delta
		}
	}
	if content != "流式内容" {
		t.Errorf("内容 = %q, want %q", content, "流式内容")
	}
	t.Log("✅ 流式首次成功")
}

func TestRetryableProvider_StreamGenerate_RetryThenSucceed(t *testing.T) {
	mock := &failThenSucceedProvider{
		failCount: 1,
		lastMsg:   &schema.Message{Content: "重试成功"},
		errMsg:    "502 bad gateway",
	}
	r := NewRetryableProvider(mock, WithMaxRetries(2), WithBaseDelay(10*time.Millisecond))

	ch, err := r.StreamGenerate(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("重试后成功不应报错: %v", err)
	}

	var content string
	for ev := range ch {
		if ev.Type == StreamEventTextDelta {
			content += ev.Delta
		}
	}
	if content != "重试成功" {
		t.Errorf("内容 = %q, want %q", content, "重试成功")
	}
	if mock.calls != 2 {
		t.Errorf("调用次数 = %d, want 2", mock.calls)
	}
	t.Logf("✅ 流式重试 %d 次后成功", mock.calls-1)
}

func TestRetryableProvider_StreamGenerate_ExhaustRetries(t *testing.T) {
	mock := &alwaysFailProvider{
		errMsg: "529 overloaded",
	}
	r := NewRetryableProvider(mock, WithMaxRetries(1), WithBaseDelay(10*time.Millisecond))

	_, err := r.StreamGenerate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("耗尽重试后应报错")
	}
	if !strings.Contains(err.Error(), "流式重试 1 次后仍然失败") {
		t.Errorf("错误信息 = %q", err.Error())
	}
	t.Logf("✅ 流式耗尽重试: %v", err)
}

func TestRetryableProvider_StreamGenerate_NonRetryableError(t *testing.T) {
	mock := &alwaysFailProvider{
		errMsg: "400 Bad Request: invalid model",
	}
	r := NewRetryableProvider(mock, WithMaxRetries(3), WithBaseDelay(10*time.Millisecond))

	_, err := r.StreamGenerate(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("不可重试错误应立即返回")
	}
	if mock.calls != 1 {
		t.Errorf("调用次数 = %d, want 1", mock.calls)
	}
	t.Logf("✅ 流式不可重试错误立即返回: %v", err)
}
