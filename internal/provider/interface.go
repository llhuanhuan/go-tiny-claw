package provider

import (
	"context"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// 架构数据流全景
//
// 本包是 Engine（消费者）与各 LLM SDK（生产者）之间的抽象层。
// 核心思想：用 Go channel 将 SDK 的 push 式 SSE 流转换为 Engine 的 pull 式 for-range 循环。
//
//	┌──────────────────────────────────────────────────────────────────────┐
//	│                        Provider (goroutine)                          │
//	│                                                                      │
//	│  SDK Streaming API                                                   │
//	│    stream.Next() ──▶ type-switch event                               │
//	│                         │                                            │
//	│     ┌───────────────────┼───────────────────┐                        │
//	│     ▼                   ▼                   ▼                        │
//	│  TextDelta         InputJSONDelta      ThinkingDelta                 │
//	│     │                   │                   │                        │
//	│     ▼                   ▼                   ▼                        │
//	│  StreamEvent{        StreamEvent{        StreamEvent{                │
//	│    TextDelta,          ToolCallArgsDelta,  ThinkingDelta,            │
//	│    Delta:"你好"         Delta:"{\"city\":   Delta:"让我想一下..."     │
//	│  }                     \"北京\"}"                                  │
//	│     │                   │                   │                        │
//	│     └───────────────────┼───────────────────┘                        │
//	│                         ▼                                            │
//	│              chan StreamEvent (cap=16)                               │
//	│                         │                                            │
//	│              defer close(ch)  ◀── 流结束自动关闭                      │
//	└─────────────────────────┬────────────────────────────────────────────┘
//	                          │
//	   ═══════════════════════╪═══════════════════════ 进程边界 ════════════
//	                          │
//	┌─────────────────────────▼────────────────────────────────────────────┐
//	│                      Engine (main goroutine)                         │
//	│                                                                      │
//	│  ch, _ := provider.StreamGenerate(ctx, msgs, tools)                  │
//	│                          │                                           │
//	│  for ev := range ch {    │                                           │
//	│    switch ev.Type {      │                                           │
//	│    case TextDelta:       ▼                                           │
//	│      fmt.Print(ev.Delta)  ──▶ 终端实时逐字打印，不再等完整响应         │
//	│      acc.Ingest(ev)      ──▶ strings.Builder 拼接完整文本            │
//	│                                                                      │
//	│    case ThinkingDelta:                                               │
//	│      fmt.Print(ev.Delta)  ──▶ 思考过程对用户可见                      │
//	│      acc.Ingest(ev)                                                  │
//	│                                                                      │
//	│    case ToolCallBegin:                                               │
//	│      acc.Ingest(ev)      ──▶ 建立 index → {ID, Name} 映射            │
//	│                                                                      │
//	│    case ToolCallArgsDelta:                                           │
//	│      acc.Ingest(ev)      ──▶ 按 index 拼接 JSON 片段到 ArgsBuf          │
//	│                                                                      │
//	│    case Done:                                                        │
//	│      return acc.Finalize() ──▶ strings.Builder → string              │
//	│                                ArgsBuf → json.RawMessage             │
//	│                                sort by index → []ToolCall            │
//	│                                → *schema.Message                     │
//	│    }                                                                 │
//	│  }                                                                   │
//	└──────────────────────────────────────────────────────────────────────┘
//
// 两种消费模式：
//
//	StreamGenerate: Engine 直接消费 channel，实时打印（生产环境推荐）
//	Generate:       通过 GenerateBlocking 静默收集，丢弃中间事件（测试/批处理用）

// LLMProvider 定义了与大模型通信的统一契约。
//
// 每个 Provider 同时支持两种调用模式：
//   - StreamGenerate: 流式（推荐）—— 通过 channel 实时推送生成内容，
//     引擎可以边接收边打印，不再阻塞等待完整响应。
//   - Generate: 阻塞式便利方法 —— 内部委托给 StreamGenerate 并收集所有事件。
type LLMProvider interface {
	// StreamGenerate 发起流式推理，返回一个只读事件通道。
	//
	// 契约：
	//   1. 调用方得到 channel 后应立即 for-range 消费，否则生产者 goroutine 会阻塞
	//   2. ctx 取消时，生产者应尽快退出并关闭 channel
	//   3. 生产者保证在退出前发送一个且仅一个 Done 或 Error 事件，然后 close(channel)
	//   4. 当 availableTools 为 nil 或长度为 0 时，代表引擎正在强制模型进入慢思考阶段
	//
	// 返回的 error 仅代表"启动流失败"（网络不通、参数错误等）；
	// 流中发生的错误通过 StreamEventError 事件投递。
	StreamGenerate(
		ctx context.Context,
		messages []schema.Message,
		availableTools []schema.ToolDefinition,
	) (<-chan StreamEvent, error)

	// Generate 接收当前的上下文历史、可用工具列表，并发起一次大模型推理。
	// 这是阻塞式的便利方法 —— 如果你需要边接收边打印或实时处理，请使用 StreamGenerate。
	//
	// 默认实现通过 GenerateBlocking 委托给 StreamGenerate。
	// 注意：当 availableTools 为 nil 或长度为 0 时，代表引擎正在强制模型进入慢思考阶段。
	Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error)
}

// GenerateBlocking 是 StreamGenerate 的阻塞式便利包装。
//
// 它启动一个流（通过 streamFn），收集所有中间事件，丢弃实时反馈，
// 最终返回组装好的 *schema.Message。适用于测试、批处理、或任何不需要
// 实时打印的场景。
//
// 如果 Provider 没有覆盖 Generate 方法，可以在 Generate 中直接调用此函数：
//
//	func (p *MyProvider) Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
//	    return GenerateBlocking(ctx, p.StreamGenerate, msgs, tools)
//	}
func GenerateBlocking(
	ctx context.Context,
	streamFn func(context.Context, []schema.Message, []schema.ToolDefinition) (<-chan StreamEvent, error),
	messages []schema.Message,
	tools []schema.ToolDefinition,
) (*schema.Message, error) {
	ch, err := streamFn(ctx, messages, tools)
	if err != nil {
		return nil, err
	}

	acc := NewStreamAccumulator()
	for ev := range ch {
		switch ev.Type {
		case StreamEventError:
			return nil, ev.Error
		case StreamEventDone:
			return acc.Finalize(), nil
		default:
			acc.Ingest(ev)
		}
	}
	return acc.Finalize(), nil
}
