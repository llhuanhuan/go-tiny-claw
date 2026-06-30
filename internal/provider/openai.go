// internal/provider/openai.go
package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lhuan/go-tiny-claw/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// OpenAIProvider 通过 OpenAI SDK 与兼容 API 通信。
type OpenAIProvider struct {
	client openai.Client
	model  string
}

// NewOpenAIProvider 创建 OpenAI Provider，支持 Functional Options。
//
// 典型用法：
//
//	p, err := NewOpenAIProvider("OPENAI_API_KEY",
//	    WithBaseURL(os.Getenv("OPENAI_BASE_URL")),
//	    WithModel(os.Getenv("OPENAI_MODEL")),
//	)
//
// 也可直接传入 API Key：
//
//	p, err := NewOpenAIProvider("",
//	    WithAPIKey("sk-xxx"),
//	    WithBaseURL("https://api.deepseek.com/"),
//	    WithModel("deepseek-chat"),
//	)
func NewOpenAIProvider(envKeys string, opts ...ProviderOption) (*OpenAIProvider, error) {
	cfg, err := loadConfig(envKeys, opts, ProviderConfig{})
	if err != nil {
		return nil, fmt.Errorf("create OpenAI provider: %w", err)
	}

	clientOpts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(cfg.BaseURL))
	}

	return &OpenAIProvider{
		client: openai.NewClient(clientOpts...),
		model:  cfg.Model,
	}, nil
}

// buildOpenAIParams 将内部消息和工具定义翻译为 OpenAI SDK 的请求参数。
func (p *OpenAIProvider) buildOpenAIParams(msgs []schema.Message, availableTools []schema.ToolDefinition) (openai.ChatCompletionNewParams, error) {
	var openaiMsgs []openai.ChatCompletionMessageParamUnion

	for _, msg := range msgs {
		switch msg.Role {
		case schema.RoleSystem:
			openaiMsgs = append(openaiMsgs, openai.SystemMessage(msg.Content))
		case schema.RoleUser:
			if msg.ToolCallID != "" {
				openaiMsgs = append(openaiMsgs, openai.ToolMessage(msg.Content, msg.ToolCallID))
			} else {
				openaiMsgs = append(openaiMsgs, openai.UserMessage(msg.Content))
			}
		case schema.RoleAssistant:
			astParam := openai.ChatCompletionAssistantMessageParam{}
			if msg.Content != "" {
				astParam.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(msg.Content),
				}
			}
			if len(msg.ToolCalls) > 0 {
				var toolCalls []openai.ChatCompletionMessageToolCallUnionParam
				for _, tc := range msg.ToolCalls {
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID:   tc.ID,
							Type: "function",
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      tc.Name,
								Arguments: string(tc.Arguments),
							},
						},
					})
				}
				astParam.ToolCalls = toolCalls
			}
			openaiMsgs = append(openaiMsgs, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &astParam,
			})
		default:
			return openai.ChatCompletionNewParams{}, fmt.Errorf("unsupported message role: %s", msg.Role)
		}
	}

	var openaiTools []openai.ChatCompletionToolUnionParam
	for _, toolDef := range availableTools {
		var params shared.FunctionParameters
		if m, ok := toolDef.InputSchema.(map[string]interface{}); ok {
			params = shared.FunctionParameters(m)
		} else {
			b, err := json.Marshal(toolDef.InputSchema)
			if err != nil {
				return openai.ChatCompletionNewParams{}, fmt.Errorf("marshal tool schema for %s: %w", toolDef.Name, err)
			}
			if err := json.Unmarshal(b, &params); err != nil {
				return openai.ChatCompletionNewParams{}, fmt.Errorf("unmarshal tool schema for %s: %w", toolDef.Name, err)
			}
		}

		openaiTools = append(openaiTools, openai.ChatCompletionFunctionTool(
			shared.FunctionDefinitionParam{
				Name:        toolDef.Name,
				Description: openai.String(toolDef.Description),
				Parameters:  params,
			},
		))
	}

	reqParams := openai.ChatCompletionNewParams{
		Model:    p.model,
		Messages: openaiMsgs,
	}

	if len(openaiTools) > 0 {
		reqParams.Tools = openaiTools
	}

	return reqParams, nil
}

// StreamGenerate 实现 LLMProvider 接口的流式推理（OpenAI 协议兼容）。
func (p *OpenAIProvider) StreamGenerate(
	ctx context.Context,
	msgs []schema.Message,
	availableTools []schema.ToolDefinition,
) (<-chan StreamEvent, error) {
	params, err := p.buildOpenAIParams(msgs, availableTools)
	if err != nil {
		return nil, fmt.Errorf("build params: %w", err)
	}

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	ch := make(chan StreamEvent, 16)

	go func() {
		defer close(ch)

		seenToolIndices := make(map[int64]bool)

		for stream.Next() {
			select {
			case <-ctx.Done():
				ch <- StreamEvent{Type: StreamEventError, Error: ctx.Err()}
				return
			default:
			}

			chunk := stream.Current()

			if len(chunk.Choices) == 0 {
				if chunk.Usage.PromptTokens > 0 {
					ch <- StreamEvent{
						Type: StreamEventUsage,
						Usage: &schema.TokenUsage{
							PromptTokens:     int(chunk.Usage.PromptTokens),
							CompletionTokens: int(chunk.Usage.CompletionTokens),
							TotalTokens:      int(chunk.Usage.TotalTokens),
						},
					}
				}
				continue
			}

			delta := chunk.Choices[0].Delta

			if delta.Content != "" {
				ch <- StreamEvent{
					Type:  StreamEventTextDelta,
					Delta: delta.Content,
				}
			}

			for _, tc := range delta.ToolCalls {
				if !seenToolIndices[tc.Index] {
					seenToolIndices[tc.Index] = true
					ch <- StreamEvent{
						Type:          StreamEventToolCallBegin,
						ToolCallIndex: int(tc.Index),
						ToolCallID:    tc.ID,
						ToolCallName:  tc.Function.Name,
					}
				}

				if tc.Function.Arguments != "" {
					ch <- StreamEvent{
						Type:          StreamEventToolCallArgsDelta,
						ToolCallIndex: int(tc.Index),
						Delta:         tc.Function.Arguments,
					}
				}
			}

			if chunk.Choices[0].FinishReason != "" {
				ch <- StreamEvent{Type: StreamEventDone}
				return
			}
		}

		if err := stream.Err(); err != nil {
			ch <- StreamEvent{Type: StreamEventError, Error: fmt.Errorf("OpenAI stream interrupted: %w", err)}
		} else {
			ch <- StreamEvent{Type: StreamEventDone}
		}
	}()

	return ch, nil
}

// Generate 阻塞式推理 —— 委托给 StreamGenerate 并收集所有事件。
func (p *OpenAIProvider) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	return GenerateBlocking(ctx, p.StreamGenerate, msgs, availableTools)
}
