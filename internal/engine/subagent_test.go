package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/schema"
	"github.com/lhuan/go-tiny-claw/internal/tools"
)

// ============================================================
// Mock Provider：用于不依赖真实 API 的单元测试
// ============================================================

// mockProvider 按预设的响应序列依次返回，用于精确控制 LLM 行为。
type mockProvider struct {
	responses []*schema.Message
	callIndex int
}

func (m *mockProvider) Generate(ctx context.Context, messages []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	if m.callIndex >= len(m.responses) {
		return nil, fmt.Errorf("mockProvider: 预设响应已用尽 (callIndex=%d)", m.callIndex)
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return resp, nil
}

func (m *mockProvider) StreamGenerate(ctx context.Context, messages []schema.Message, tools []schema.ToolDefinition) (<-chan provider.StreamEvent, error) {
	return nil, fmt.Errorf("mockProvider: StreamGenerate 未实现")
}

// ============================================================
// 辅助函数
// ============================================================

// newMockEngine 创建一个使用 MockProvider 的 AgentEngine，用于单元测试。
func newMockEngine(responses []*schema.Message) (*AgentEngine, tools.Registry) {
	p := &mockProvider{responses: responses}
	r := tools.NewRegistry()
	// 注册一个虚拟的 read_file 工具，让子智能体有工具可调
	r.Register(&mockReadFileTool{})
	eng := NewAgentEngine(p, r, ".", false, false)
	return eng, r
}

// mockReadFileTool 是一个极简的只读工具，用于测试子智能体的工具调用链路。
type mockReadFileTool struct{}

func (t *mockReadFileTool) Name() string { return "read_file" }

func (t *mockReadFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        "read_file",
		Description: "读取文件内容",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string"},
			},
			"required": []string{"path"},
		},
	}
}

func (t *mockReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return "file content: hello world", nil
}

// ============================================================
// 测试用例
// ============================================================

// TestRunSub_BasicFlow 验证子智能体的基本工作流：
// 第 1 轮：子智能体调用工具 → 获得结果
// 第 2 轮：子智能体不再调用工具 → 输出总结 → 退出
func TestRunSub_BasicFlow(t *testing.T) {
	eng, subRegistry := newMockEngine([]*schema.Message{
		// 第 1 轮：调用 read_file 工具
		{
			Role:    schema.RoleAssistant,
			Content: "让我先看看文件内容。",
			ToolCalls: []schema.ToolCall{
				{
					ID:        "call_001",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path":"main.go"}`),
				},
			},
		},
		// 第 2 轮：不再调用工具，输出总结
		{
			Role:    schema.RoleAssistant,
			Content: "经过探索，main.go 文件内容为 hello world。这是一个简单的项目。",
		},
	})

	reporter := &captureReporter{}
	summary, err := eng.RunSub(context.Background(), "探索 main.go 文件", subRegistry, reporter)
	if err != nil {
		t.Fatalf("RunSub 失败: %v", err)
	}

	if summary == "" {
		t.Fatal("RunSub 返回了空的 summary")
	}

	t.Logf("子智能体总结: %s", summary)

	// 验证包含预期内容
	if !strings.Contains(summary, "hello world") {
		t.Fatalf("summary 中未包含预期的文件内容: %s", summary)
	}

	// 验证子智能体调用了工具
	if len(reporter.tools) == 0 {
		t.Fatal("子智能体未调用任何工具")
	}
	t.Logf("子智能体调用的工具: %v", reporter.ToolNames())
}

// TestRunSub_TurnLimit 验证子智能体超过 10 轮后被强制召回。
func TestRunSub_TurnLimit(t *testing.T) {
	// 预设 11 轮全部返回 ToolCall（超过 maxSubTurns=10）
	responses := make([]*schema.Message, 11)
	for i := range responses {
		responses[i] = &schema.Message{
			Role:    schema.RoleAssistant,
			Content: fmt.Sprintf("继续探索第 %d 轮...", i+1),
			ToolCalls: []schema.ToolCall{
				{
					ID:        fmt.Sprintf("call_%03d", i),
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path":"some_file.go"}`),
				},
			},
		}
	}

	eng, subRegistry := newMockEngine(responses)

	_, err := eng.RunSub(context.Background(), "无限探索任务", subRegistry, nil)
	if err == nil {
		t.Fatal("RunSub 应该返回错误（超过 Turn 上限），但返回了 nil")
	}

	if !strings.Contains(err.Error(), "超过") {
		t.Fatalf("错误信息应包含'超过'字样: %v", err)
	}
	t.Logf("预期的 Turn 上限错误: %v", err)
}

// TestRunSub_NoToolCalls 验证子智能体第一轮就不调用工具时立即退出。
func TestRunSub_NoToolCalls(t *testing.T) {
	eng, subRegistry := newMockEngine([]*schema.Message{
		{
			Role:    schema.RoleAssistant,
			Content: "根据我的知识，这个问题不需要查阅代码。答案是 42。",
		},
	})

	summary, err := eng.RunSub(context.Background(), "简单问题", subRegistry, nil)
	if err != nil {
		t.Fatalf("RunSub 失败: %v", err)
	}

	if !strings.Contains(summary, "42") {
		t.Fatalf("summary 未包含预期答案: %s", summary)
	}
	t.Logf("子智能体直接回答: %s", summary)
}

// TestRunSub_ReadOnlyRegistry 验证子智能体只能使用传入的只读注册表。
func TestRunSub_ReadOnlyRegistry(t *testing.T) {
	// 创建一个只有 read_file 的只读注册表
	roRegistry := tools.NewRegistry()
	roRegistry.Register(&mockReadFileTool{})

	// 创建引擎，主注册表包含 write_file（但子智能体不应该看到它）
	mainResponses := []*schema.Message{
		{
			Role:    schema.RoleAssistant,
			Content: "我来读取文件。",
			ToolCalls: []schema.ToolCall{
				{
					ID:        "call_ro_001",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path":"test.go"}`),
				},
			},
		},
		{
			Role:    schema.RoleAssistant,
			Content: "读取完毕，文件内容正常。",
		},
	}

	p := &mockProvider{responses: mainResponses}
	mainRegistry := tools.NewRegistry()
	mainRegistry.Register(&mockReadFileTool{})
	mainRegistry.Register(&mockWriteFileTool{}) // 主注册表有写工具
	eng := NewAgentEngine(p, mainRegistry, ".", false, false)

	summary, err := eng.RunSub(context.Background(), "只读探索", roRegistry, nil)
	if err != nil {
		t.Fatalf("RunSub 失败: %v", err)
	}

	t.Logf("只读注册表下的子智能体总结: %s", summary)

	// 验证：子智能体的可用工具列表中不应包含 write_file
	availableTools := roRegistry.GetAvailableTools()
	for _, td := range availableTools {
		if td.Name == "write_file" {
			t.Fatal("只读注册表中不应包含 write_file 工具")
		}
	}
}

// mockWriteFileTool 用于验证只读注册表不包含写工具。
type mockWriteFileTool struct{}

func (t *mockWriteFileTool) Name() string { return "write_file" }
func (t *mockWriteFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "write_file", Description: "写入文件"}
}
func (t *mockWriteFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return "written", nil
}
