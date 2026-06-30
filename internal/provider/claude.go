// internal/provider/claude.go
package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/lhuan/go-tiny-claw/internal/schema"
)

const defaultClaudeModel = "claude-sonnet-4-20250514"

// ClaudeProvider 通过 Anthropic SDK 与 Claude 系列模型通信。
type ClaudeProvider struct {
	client anthropic.Client
	model  string
}

// NewClaudeProvider 创建 Claude Provider，支持 Functional Options。
//
// envKeys 支持逗号分隔的多个环境变量名，按顺序尝试读取 API Key。
// 典型用法：
//
//	p, err := NewClaudeProvider("ANTHROPIC_AUTH_TOKEN,ANTHROPIC_API_KEY",
//	    WithBaseURL(os.Getenv("ANTHROPIC_BASE_URL")),
//	    WithModel(os.Getenv("ANTHROPIC_MODEL")),
//	)
//
// 也可直接传入 API Key：
//
//	p, err := NewClaudeProvider("",
//	    WithAPIKey("sk-ant-xxx"),
//	    WithModel("claude-sonnet-4-20250514"),
//	)
func NewClaudeProvider(envKeys string, opts ...ProviderOption) (*ClaudeProvider, error) {
	cfg, err := loadConfig(envKeys, opts, ProviderConfig{
		Model: defaultClaudeModel,
	})
	if err != nil {
		return nil, fmt.Errorf("create Claude provider: %w", err)
	}

	clientOpts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(cfg.BaseURL))
	}

	return &ClaudeProvider{
		client: anthropic.NewClient(clientOpts...),
		model:  cfg.Model,
	}, nil
}

// NewAnthropicProvider 创建连接 Anthropic 官方 API 的 Provider。
// 自动从环境变量 ANTHROPIC_AUTH_TOKEN / ANTHROPIC_API_KEY 读取凭据。
func NewAnthropicProvider(model string) (*ClaudeProvider, error) {
	return NewClaudeProvider("ANTHROPIC_AUTH_TOKEN,ANTHROPIC_API_KEY",
		WithModel(model),
	)
}

// buildAnthropicParams 将内部消息和工具定义翻译为 Anthropic SDK 的请求参数。
func (p *ClaudeProvider) buildAnthropicParams(msgs []schema.Message, availableTools []schema.ToolDefinition) (anthropic.MessageNewParams, error) {
	var anthropicMsgs []anthropic.MessageParam
	var systemPrompt string

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
				if err := json.Unmarshal(tc.Arguments, &inputMap); err != nil {
					return anthropic.MessageNewParams{}, fmt.Errorf("unmarshal tool args for %s: %w", tc.Name, err)
				}
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
		default:
			return anthropic.MessageNewParams{}, fmt.Errorf("unsupported message role: %s", msg.Role)
		}
	}

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
func (p *ClaudeProvider) StreamGenerate(
	ctx context.Context,
	msgs []schema.Message,
	availableTools []schema.ToolDefinition,
) (<-chan StreamEvent, error) {
	params, err := p.buildAnthropicParams(msgs, availableTools)
	if err != nil {
		return nil, fmt.Errorf("build params: %w", err)
	}

	stream := p.client.Messages.NewStreaming(ctx, params)

	ch := make(chan StreamEvent, 16)

	go func() {
		defer close(ch)

		toolUseMeta := make(map[int64]struct {
			ID   string
			Name string
		})

		for stream.Next() {
			select {
			case <-ctx.Done():
				ch <- StreamEvent{Type: StreamEventError, Error: ctx.Err()}
				return
			default:
			}

			event := stream.Current()

			switch ev := event.AsAny().(type) {
			case anthropic.MessageStartEvent:
				if ev.Message.Usage.InputTokens > 0 {
					ch <- StreamEvent{
						Type: StreamEventUsage,
						Usage: &schema.TokenUsage{
							PromptTokens:     int(ev.Message.Usage.InputTokens),
							CompletionTokens: int(ev.Message.Usage.OutputTokens),
							TotalTokens:      int(ev.Message.Usage.InputTokens + ev.Message.Usage.OutputTokens),
						},
					}
				}

			case anthropic.ContentBlockStartEvent:
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
				}

			case anthropic.MessageDeltaEvent:
				if ev.Usage.OutputTokens > 0 {
					ch <- StreamEvent{
						Type: StreamEventUsage,
						Usage: &schema.TokenUsage{
							CompletionTokens: int(ev.Usage.OutputTokens),
							TotalTokens:      int(ev.Usage.OutputTokens),
						},
					}
				}

			case anthropic.MessageStopEvent:
				ch <- StreamEvent{Type: StreamEventDone}
				return
			}
		}

		if err := stream.Err(); err != nil {
			ch <- StreamEvent{Type: StreamEventError, Error: fmt.Errorf("Claude stream interrupted: %w", err)}
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
