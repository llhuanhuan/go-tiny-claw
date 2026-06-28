package observability

import (
	"context"
	"sync/atomic"
	"testing"

	ctxpkg "github.com/lhuan/go-tiny-claw/internal/context"
	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// ============================================================
// Mock Provider: 模拟大模型返回带 Usage 的响应
// ============================================================

type mockProvider struct {
	response *schema.Message
	err      error
	callCount int32
}

func (m *mockProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	atomic.AddInt32(&m.callCount, 1)
	return m.response, m.err
}

func (m *mockProvider) StreamGenerate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (<-chan provider.StreamEvent, error) {
	// 不用于测试，返回已关闭的 channel
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}

// ============================================================
// 场景 1: 正常调用 —— 计费数据正确记录
// ============================================================

// TestCostTracker_NormalCall_TracksUsage 模拟一次正常 API 调用，
// 验证 CostTracker 正确解析 Usage 并累加到 Session。
func TestCostTracker_NormalCall_TracksUsage(t *testing.T) {
	mock := &mockProvider{
		response: &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "你好！",
			Usage: &schema.TokenUsage{
				PromptTokens:     1000,
				CompletionTokens: 200,
				TotalTokens:      1200,
			},
		},
	}

	session := ctxpkg.NewSession("test-normal")
	tracker := NewCostTracker(mock, "glm-4.5-air", session)

	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: "你好"},
	}

	resp, err := tracker.Generate(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}

	// 验证响应内容透传
	if resp.Content != "你好！" {
		t.Fatalf("期望响应内容='你好！', 得到 %q", resp.Content)
	}

	// 验证 Session 计费数据
	if session.TotalPromptTokens != 1000 {
		t.Fatalf("期望 PromptTokens=1000, 得到 %d", session.TotalPromptTokens)
	}
	if session.TotalCompletionTokens != 200 {
		t.Fatalf("期望 CompletionTokens=200, 得到 %d", session.TotalCompletionTokens)
	}

	// 验证成本计算: (1000*0.15 + 200*0.15) / 1000000 = 0.00018
	expectedCost := (1000.0*0.15 + 200.0*0.15) / 1000000.0
	diff := session.TotalCostCNY - expectedCost
	if diff < 0 {
		diff = -diff
	}
	if diff > 1e-9 {
		t.Fatalf("期望 Cost≈%f, 得到 %f", expectedCost, session.TotalCostCNY)
	}
}

// ============================================================
// 场景 2: API 报错 —— 不计费
// ============================================================

// TestCostTracker_APIError_NoCostTracking 模拟 API 调用失败，
// 验证不会计费，只记录耗时。
func TestCostTracker_APIError_NoCostTracking(t *testing.T) {
	mock := &mockProvider{
		err: context.DeadlineExceeded,
	}

	session := ctxpkg.NewSession("test-error")
	tracker := NewCostTracker(mock, "glm-4.5-air", session)

	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: "测试"},
	}

	_, err := tracker.Generate(context.Background(), msgs, nil)
	if err == nil {
		t.Fatal("期望返回错误")
	}

	// 验证 Session 计费数据仍为零
	if session.TotalPromptTokens != 0 {
		t.Fatalf("API 报错后不应计费: PromptTokens=%d", session.TotalPromptTokens)
	}
	if session.TotalCostCNY != 0.0 {
		t.Fatalf("API 报错后不应计费: CostCNY=%f", session.TotalCostCNY)
	}
}

// ============================================================
// 场景 3: 无 Usage 数据 —— 安全降级
// ============================================================

// TestCostTracker_NoUsageData_GracefulDegradation 模拟 API 返回响应但无 Usage 数据，
// 验证 CostTracker 安全降级，不 panic，不计费。
func TestCostTracker_NoUsageData_GracefulDegradation(t *testing.T) {
	mock := &mockProvider{
		response: &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "回复内容",
			Usage:   nil, // 无 Usage 数据
		},
	}

	session := ctxpkg.NewSession("test-no-usage")
	tracker := NewCostTracker(mock, "glm-4.5-air", session)

	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: "测试"},
	}

	resp, err := tracker.Generate(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}
	if resp.Content != "回复内容" {
		t.Fatalf("响应内容应正确透传")
	}

	// 无 Usage 时不应计费
	if session.TotalPromptTokens != 0 {
		t.Fatalf("无 Usage 时不应计费: PromptTokens=%d", session.TotalPromptTokens)
	}
}

// ============================================================
// 场景 4: 多次调用累加
// ============================================================

// TestCostTracker_MultipleCalls_AccumulateCost 验证多次调用的成本正确累加。
func TestCostTracker_MultipleCalls_AccumulateCost(t *testing.T) {
	mock := &mockProvider{
		response: &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "回复",
			Usage: &schema.TokenUsage{
				PromptTokens:     500,
				CompletionTokens: 100,
				TotalTokens:      600,
			},
		},
	}

	session := ctxpkg.NewSession("test-multi")
	tracker := NewCostTracker(mock, "glm-4.5-air", session)

	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: "测试"},
	}

	// 调用 3 次
	for i := 0; i < 3; i++ {
		_, err := tracker.Generate(context.Background(), msgs, nil)
		if err != nil {
			t.Fatalf("第 %d 次调用失败: %v", i+1, err)
		}
	}

	if session.TotalPromptTokens != 1500 {
		t.Fatalf("期望 PromptTokens=1500, 得到 %d", session.TotalPromptTokens)
	}
	if session.TotalCompletionTokens != 300 {
		t.Fatalf("期望 CompletionTokens=300, 得到 %d", session.TotalCompletionTokens)
	}
}

// ============================================================
// 场景 5: 未知模型 —— 成本为 0 但 Token 仍记录
// ============================================================

// TestCostTracker_UnknownModel_ZeroCost 验证未知模型名不会 panic，
// 成本为 0 但 Token 数据仍正确记录。
func TestCostTracker_UnknownModel_ZeroCost(t *testing.T) {
	mock := &mockProvider{
		response: &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "回复",
			Usage: &schema.TokenUsage{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
			},
		},
	}

	session := ctxpkg.NewSession("test-unknown-model")
	tracker := NewCostTracker(mock, "gpt-99-turbo", session) // 不存在的模型

	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: "测试"},
	}

	_, err := tracker.Generate(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}

	// Token 数据应正确记录
	if session.TotalPromptTokens != 1000 {
		t.Fatalf("期望 PromptTokens=1000, 得到 %d", session.TotalPromptTokens)
	}
	if session.TotalCompletionTokens != 500 {
		t.Fatalf("期望 CompletionTokens=500, 得到 %d", session.TotalCompletionTokens)
	}

	// 未知模型成本应为 0
	if session.TotalCostCNY != 0.0 {
		t.Fatalf("未知模型成本应为 0, 得到 %f", session.TotalCostCNY)
	}
}

// ============================================================
// 场景 6: StreamGenerate 直接透传
// ============================================================

// TestCostTracker_StreamGenerate_Passthrough 验证 StreamGenerate 直接委托给底层 Provider。
func TestCostTracker_StreamGenerate_Passthrough(t *testing.T) {
	mock := &mockProvider{
		response: &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "流式回复",
		},
	}

	session := ctxpkg.NewSession("test-stream")
	tracker := NewCostTracker(mock, "glm-4.5-air", session)

	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: "测试"},
	}

	ch, err := tracker.StreamGenerate(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("StreamGenerate 失败: %v", err)
	}

	// channel 应该已关闭（mock 直接 close）
	_, ok := <-ch
	if ok {
		t.Fatal("StreamGenerate 返回的 channel 应该已关闭")
	}

	// StreamGenerate 不应触发计费
	if session.TotalPromptTokens != 0 {
		t.Fatal("StreamGenerate 不应触发计费")
	}
}

// ============================================================
// 场景 7: 底层 Provider 被正确调用
// ============================================================

// TestCostTracker_DelegatesToUnderlyingProvider 验证 CostTracker 确实调用了底层 Provider。
func TestCostTracker_DelegatesToUnderlyingProvider(t *testing.T) {
	mock := &mockProvider{
		response: &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "回复",
			Usage:   &schema.TokenUsage{PromptTokens: 100, CompletionTokens: 50},
		},
	}

	session := ctxpkg.NewSession("test-delegate")
	tracker := NewCostTracker(mock, "glm-4.5-air", session)

	msgs := []schema.Message{
		{Role: schema.RoleUser, Content: "你好"},
	}

	tracker.Generate(context.Background(), msgs, nil)

	if atomic.LoadInt32(&mock.callCount) != 1 {
		t.Fatalf("底层 Provider 应被调用 1 次, 实际 %d 次", mock.callCount)
	}
}
