package memory

import (
	"context"
	"testing"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// mockProvider 是一个简单的 mock LLM Provider，用于测试。
type mockProvider struct {
	responses []*schema.Message
	callIndex int
}

func (m *mockProvider) Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	if m.callIndex >= len(m.responses) {
		return &schema.Message{Role: schema.RoleAssistant, Content: "mock response"}, nil
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return resp, nil
}

// mockSession 实现 HistoryReader 接口。
type mockSession struct {
	messages []schema.Message
}

func (m *mockSession) GetAllMessages() []schema.Message {
	return m.messages
}

func TestManager_GetSummary_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	provider := &mockProvider{}
	config := DefaultConfig()

	mgr := NewManager(tmpDir, "test:session", provider, config)
	summary := mgr.GetSummary()
	if summary != "" {
		t.Errorf("期望空摘要, 得到: %q", summary)
	}
}

func TestManager_GetLongTermFacts_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	provider := &mockProvider{}
	config := DefaultConfig()

	mgr := NewManager(tmpDir, "test:session", provider, config)
	facts := mgr.GetLongTermFacts()
	if facts != "" {
		t.Errorf("期望空事实, 得到: %q", facts)
	}
}

func TestManager_IncrementTurn_TriggersSummarize(t *testing.T) {
	tmpDir := t.TempDir()
	config := MemoryConfig{
		Enabled:         true,
		SummarizeEveryN: 3, // 每3轮触发摘要
		ExtractEveryN:   5,
	}

	// Mock provider 返回摘要
	provider := &mockProvider{
		responses: []*schema.Message{
			{Role: schema.RoleAssistant, Content: "这是对话摘要"},
			{Role: schema.RoleAssistant, Content: `[{"type":"user_preference","content":"用户偏好中文","confidence":0.9}]`},
		},
	}

	session := &mockSession{
		messages: []schema.Message{
			{Role: schema.RoleUser, Content: "你好"},
			{Role: schema.RoleAssistant, Content: "你好！有什么可以帮你的？"},
			{Role: schema.RoleUser, Content: "帮我写代码"},
			{Role: schema.RoleAssistant, Content: "好的，我来帮你"},
		},
	}

	mgr := NewManager(tmpDir, "test:session", provider, config)

	ctx := context.Background()

	// 触发3轮，应触发摘要
	for i := 0; i < 3; i++ {
		mgr.IncrementTurn(ctx, session)
	}

	// 等待异步操作完成
	time.Sleep(100 * time.Millisecond)

	// 检查摘要是否生成
	summary := mgr.GetSummary()
	if summary == "" {
		t.Error("期望生成摘要, 但为空")
	}
}

func TestManager_IncrementTurn_Disabled(t *testing.T) {
	tmpDir := t.TempDir()
	config := MemoryConfig{
		Enabled: false,
	}
	provider := &mockProvider{}
	session := &mockSession{
		messages: []schema.Message{
			{Role: schema.RoleUser, Content: "你好"},
		},
	}

	mgr := NewManager(tmpDir, "test:session", provider, config)
	ctx := context.Background()

	// 触发多轮，不应触发摘要
	for i := 0; i < 20; i++ {
		mgr.IncrementTurn(ctx, session)
	}

	// 等待异步操作完成
	time.Sleep(100 * time.Millisecond)

	// 检查摘要是否为空
	summary := mgr.GetSummary()
	if summary != "" {
		t.Errorf("未启用时不应生成摘要, 得到: %q", summary)
	}
}

func TestManager_GetTurnCount(t *testing.T) {
	tmpDir := t.TempDir()
	config := DefaultConfig()
	provider := &mockProvider{}
	session := &mockSession{}

	mgr := NewManager(tmpDir, "test:session", provider, config)
	ctx := context.Background()

	if mgr.GetTurnCount() != 0 {
		t.Errorf("初始轮次应为 0, 得到: %d", mgr.GetTurnCount())
	}

	for i := 0; i < 5; i++ {
		mgr.IncrementTurn(ctx, session)
	}

	if mgr.GetTurnCount() != 5 {
		t.Errorf("轮次应为 5, 得到: %d", mgr.GetTurnCount())
	}
}
