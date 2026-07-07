package provider

import (
	"context"
	"encoding/json"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// MockProvider 是一个用于单元测试的 LLM Provider 实现。
// 它返回预设的响应序列，不需要真实的 API Key。
//
// 使用示例：
//
//	mock := NewMockProvider()
//	mock.AddResponse(&schema.Message{Content: "你好"})
//	mock.AddToolCallResponse("read_file", `{"path":"test.txt"}`, "文件内容")
//	msg, err := mock.Generate(ctx, msgs, tools)
type MockProvider struct {
	responses []*schema.Message
	callIndex int
}

// NewMockProvider 创建一个空的 Mock Provider。
func NewMockProvider() *MockProvider {
	return &MockProvider{
		responses: make([]*schema.Message, 0),
	}
}

// AddResponse 添加一个预设的纯文本响应。
func (m *MockProvider) AddResponse(content string) {
	m.responses = append(m.responses, &schema.Message{
		Role:    schema.RoleAssistant,
		Content: content,
	})
}

// AddToolCallResponse 添加一个包含工具调用的预设响应。
func (m *MockProvider) AddToolCallResponse(toolName string, toolArgs string, toolCallID string) {
	m.responses = append(m.responses, &schema.Message{
		Role:    schema.RoleAssistant,
		Content: "",
		ToolCalls: []schema.ToolCall{
			{
				ID:        toolCallID,
				Name:      toolName,
				Arguments: json.RawMessage(toolArgs),
			},
		},
	})
}

// Generate 返回下一个预设响应。超出预设列表时返回空消息。
func (m *MockProvider) Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	if m.callIndex >= len(m.responses) {
		return &schema.Message{
			Role:    schema.RoleAssistant,
			Content: "[MockProvider] 无更多预设响应",
		}, nil
	}
	msg := m.responses[m.callIndex]
	m.callIndex++
	return msg, nil
}

// StreamGenerate 实现流式接口（Mock 模式下直接返回完整消息）。
func (m *MockProvider) StreamGenerate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 2)

	go func() {
		defer close(ch)

		msg, _ := m.Generate(ctx, messages, availableTools)

		if msg.Content != "" {
			ch <- StreamEvent{
				Type:  StreamEventTextDelta,
				Delta: msg.Content,
			}
		}

		for _, tc := range msg.ToolCalls {
			ch <- StreamEvent{
				Type:           StreamEventToolCallBegin,
				ToolCallIndex:  0,
				ToolCallID:     tc.ID,
				ToolCallName:   tc.Name,
			}
			ch <- StreamEvent{
				Type:  StreamEventToolCallArgsDelta,
				Delta: string(tc.Arguments),
			}
		}

		ch <- StreamEvent{Type: StreamEventDone}
	}()

	return ch, nil
}

// Reset 重置调用计数器（用于复用 Mock）
func (m *MockProvider) Reset() {
	m.callIndex = 0
}

// CallCount 返回已调用的次数
func (m *MockProvider) CallCount() int {
	return m.callIndex
}
