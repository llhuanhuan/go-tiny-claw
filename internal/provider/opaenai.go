// internal/provider/openai.go
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/lhuan/go-tiny-claw/internal/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

type OpenAIProvider struct {
	client openai.Client
	model  string
}

func NewDeepSeekOpenAIProvider(model string) *OpenAIProvider {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		panic("请设置 DEEPSEEK_API_KEY 环境变量")
	}
	baseURL := "https://api.deepseek.com/"

	return &OpenAIProvider{
		client: openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL)),
		model:  model,
	}
}

func NewZhipuOpenAIProvider(model string) *OpenAIProvider {
	apiKey := os.Getenv("ZHIPU_API_KEY")
	if apiKey == "" {
		panic("请设置 ZHIPU_API_KEY 环境变量")
	}
	baseURL := "https://open.bigmodel.cn/api/paas/v4/"

	return &OpenAIProvider{
		client: openai.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL)),
		model:  model,
	}
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
		}
	}

	var openaiTools []openai.ChatCompletionToolUnionParam
	for _, toolDef := range availableTools {
		var params shared.FunctionParameters

		if m, ok := toolDef.InputSchema.(map[string]interface{}); ok {
			params = shared.FunctionParameters(m)
		} else {
			b, _ := json.Marshal(toolDef.InputSchema)
			_ = json.Unmarshal(b, &params)
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
//
// 通过 OpenAI SDK 的 NewStreaming 订阅 SSE 事件流，
// 将 ChatCompletionChunk 转换为统一的 StreamEvent。
func (p *OpenAIProvider) StreamGenerate(
	ctx context.Context,
	msgs []schema.Message,
	availableTools []schema.ToolDefinition,
) (<-chan StreamEvent, error) {
	params, err := p.buildOpenAIParams(msgs, availableTools)
	if err != nil {
		return nil, err
	}

	stream := p.client.Chat.Completions.NewStreaming(ctx, params)

	ch := make(chan StreamEvent, 16)

	go func() {
		defer close(ch)

		// 追踪已见过的工具调用索引，用于判断 ToolCallBegin vs ArgsDelta
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
				continue
			}

			delta := chunk.Choices[0].Delta

			// 文本增量 —— 直接投递
			if delta.Content != "" {
				ch <- StreamEvent{
					Type:  StreamEventTextDelta,
					Delta: delta.Content,
				}
			}

			// 工具调用增量
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

			// 结束判断：任一 choice 的 finish_reason 非空表示流结束
			if chunk.Choices[0].FinishReason != "" {
				ch <- StreamEvent{Type: StreamEventDone}
				return
			}
		}

		if err := stream.Err(); err != nil {
			ch <- StreamEvent{Type: StreamEventError, Error: fmt.Errorf("OpenAI 流式响应中断: %w", err)}
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
