// internal/provider/claude.go
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/lhuan/go-tiny-claw/internal/schema"
)

type ClaudeProvider struct {
	client anthropic.Client
	model  string
}

func NewDeepSeekClaudeProvider(model string) *ClaudeProvider {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		panic("请设置 DEEPSEEK_API_KEY 环境变量")
	}
	baseURL := "https://api.deepseek.com/"
	return &ClaudeProvider{
		client: anthropic.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL)),
		model:  model,
	}
}

func NewZhipuClaudeProvider(model string) *ClaudeProvider {
	apiKey := os.Getenv("ZHIPU_API_KEY")
	if apiKey == "" {
		panic("请设置 ZHIPU_API_KEY 环境变量")
	}
	baseURL := "https://open.bigmodel.cn/api/paas/v4/"
	return &ClaudeProvider{
		client: anthropic.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL)),
		model:  model,
	}
}

// buildAnthropicParams 将内部消息和工具定义翻译为 Anthropic SDK 的请求参数。
// StreamGenerate 和 Generate 共享此翻译逻辑。
func (p *ClaudeProvider) buildAnthropicParams(msgs []schema.Message, availableTools []schema.ToolDefinition) (anthropic.MessageNewParams, error) {
	var anthropicMsgs []anthropic.MessageParam
	var systemPrompt string

	// 1. 消息翻译
	for _, msg := range msgs {
		switch msg.Role {
		case schema.RoleSystem:
			systemPrompt = msg.Content
		case schema.RoleUser:
			if msg.ToolCallID != "" {
				anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(
					anthropic.NewToolResultBlock(msg.ToolCallID, msg.Content, false),
				))
			} else {
				anthropicMsgs = append(anthropicMsgs, anthropic.NewUserMessage(
					anthropic.NewTextBlock(msg.Content),
				))
			}
		case schema.RoleAssistant:
			var blocks []anthropic.ContentBlockParamUnion
			if msg.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
			}
			for _, tc := range msg.ToolCalls {
				var inputMap map[string]interface{}
				_ = json.Unmarshal(tc.Arguments, &inputMap)
				blocks = append(blocks, anthropic.ContentBlockParamUnion{
					OfToolUse: &anthropic.ToolUseBlockParam{
						ID:    tc.ID,
						Name:  tc.Name,
						Input: inputMap,
					},
				})
			}
			if len(blocks) > 0 {
				anthropicMsgs = append(anthropicMsgs, anthropic.NewAssistantMessage(blocks...))
			}
		}
	}

	// 2. 工具 Schema 翻译
	var anthropicTools []anthropic.ToolUnionParam
	for _, toolDef := range availableTools {
		var properties map[string]any
		var required []string

		if m, ok := toolDef.InputSchema.(map[string]interface{}); ok {
			if p, ok := m["properties"].(map[string]interface{}); ok {
				properties = p
			}
			if r, ok := m["required"].([]string); ok {
				required = r
			}
		}

		tp := anthropic.ToolParam{
			Name:        toolDef.Name,
			Description: anthropic.String(toolDef.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: properties,
				Required:   required,
			},
		}
		anthropicTools = append(anthropicTools, anthropic.ToolUnionParam{OfTool: &tp})
	}

	// 3. 构建请求参数
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: 4096,
		Messages:  anthropicMsgs,
	}

	if systemPrompt != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: systemPrompt},
		}
	}

	if len(anthropicTools) > 0 {
		params.Tools = anthropicTools
	}

	return params, nil
}

// StreamGenerate 实现 LLMProvider 接口的流式推理。
//
// 内部通过 Anthropic SDK 的 NewStreaming API 订阅 SSE 事件流，
// 在 goroutine 中将 SDK 事件转换为统一的 StreamEvent 并通过 channel 投递。
func (p *ClaudeProvider) StreamGenerate(
	ctx context.Context,
	msgs []schema.Message,
	availableTools []schema.ToolDefinition,
) (<-chan StreamEvent, error) {
	params, err := p.buildAnthropicParams(msgs, availableTools)
	if err != nil {
		return nil, err
	}

	// 启动 SDK 流
	stream := p.client.Messages.NewStreaming(ctx, params)

	ch := make(chan StreamEvent, 16)

	go func() {
		defer close(ch)

		// 使用 SDK 内置的 Accumulate 机制来跟踪工具调用 ID 信息。
		// 当看到 ContentBlockDeltaEvent 且类型为 input_json_delta 时，
		// 需要知道它对应哪个工具调用。我们通过维护一个 index → {id, name} 的映射来解决。
		toolUseMeta := make(map[int64]struct {
			ID   string
			Name string
		})

		for stream.Next() {
			// 支持 ctx 取消
			select {
			case <-ctx.Done():
				ch <- StreamEvent{Type: StreamEventError, Error: ctx.Err()}
				return
			default:
			}

			event := stream.Current()

			switch ev := event.AsAny().(type) {

			case anthropic.ContentBlockStartEvent:
				// 当内容块开始时，记录其类型信息。
				// tool_use 块会在后续 delta 事件中逐步接收 JSON 片段。
				if ev.ContentBlock.Type == "tool_use" {
					toolUseMeta[ev.Index] = struct {
						ID   string
						Name string
					}{
						ID:   ev.ContentBlock.ID,
						Name: ev.ContentBlock.Name,
					}
					ch <- StreamEvent{
						Type:          StreamEventToolCallBegin,
						ToolCallIndex: int(ev.Index),
						ToolCallID:    ev.ContentBlock.ID,
						ToolCallName:  ev.ContentBlock.Name,
					}
				}

			case anthropic.ContentBlockDeltaEvent:
				switch ev.Delta.Type {
				case "text_delta":
					ch <- StreamEvent{
						Type:  StreamEventTextDelta,
						Delta: ev.Delta.Text,
					}

				case "input_json_delta":
					ch <- StreamEvent{
						Type:          StreamEventToolCallArgsDelta,
						ToolCallIndex: int(ev.Index),
						Delta:         ev.Delta.PartialJSON,
					}

				case "thinking_delta":
					ch <- StreamEvent{
						Type:  StreamEventThinkingDelta,
						Delta: ev.Delta.Thinking,
					}

				// signature_delta / citations_delta 是协议内部细节，忽略
				}

			case anthropic.MessageStopEvent:
				// 消息结束，标记完成
				ch <- StreamEvent{Type: StreamEventDone}
				return
			}
		}

		// 流被中断（Next 返回 false 但没有 MessageStopEvent）
		if err := stream.Err(); err != nil {
			ch <- StreamEvent{Type: StreamEventError, Error: fmt.Errorf("Claude 流式响应中断: %w", err)}
		} else {
			ch <- StreamEvent{Type: StreamEventDone}
		}
	}()

	return ch, nil
}

// Generate 阻塞式推理 —— 委托给 StreamGenerate 并收集所有事件。
func (p *ClaudeProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	return GenerateBlocking(ctx, p.StreamGenerate, msgs, availableTools)
}
