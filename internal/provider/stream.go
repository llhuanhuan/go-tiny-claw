package provider

import "github.com/lhuan/go-tiny-claw/internal/schema"

// StreamEvent 事件生命周期
//
// 一次完整的流式推理按时间线投递以下事件序列：
//
//	┌─ Thinking 阶段（tools=nil，仅当 EnableThinking 开启）─────────────────┐
//	│  StreamEventThinkingDelta  × N   (逐字推理文本)                        │
//	│  StreamEventDone                (思考结束)                              │
//	└────────────────────────────────────────────────────────────────────────┘
//
//	┌─ Action 阶段（tools 正常挂载）──────────────────────────────────────────┐
//	│                                                                         │
//	│  ┌─ 情况 A：纯文本回复 ──────────────────────────────────────────┐      │
//	│  │  StreamEventTextDelta  × N                                    │      │
//	│  │  StreamEventDone                                              │      │
//	│  └───────────────────────────────────────────────────────────────┘      │
//	│                                                                         │
//	│  ┌─ 情况 B：文本 + 工具调用（可能并行多个工具） ─────────────────┐      │
//	│  │  StreamEventTextDelta  × N        (先说话)                    │      │
//	│  │  StreamEventToolCallBegin         (#0: read_file, id=abc)     │      │
//	│  │  StreamEventToolCallArgsDelta × N (#0: {"path":...)           │      │
//	│  │  StreamEventToolCallBegin         (#1: bash, id=def)          │      │
//	│  │  StreamEventToolCallArgsDelta × N (#1: {"cmd":...)            │      │
//	│  │  // 文本和工具调用的 delta 可能交错（模型边说话边调工具）         │      │
//	│  │  StreamEventDone                                              │      │
//	│  └───────────────────────────────────────────────────────────────┘      │
//	│                                                                         │
//	│  ┌─ 错误情况 ───────────────────────────────────────────────────┐      │
//	│  │  StreamEventError{Error: ...}                                 │      │
//	│  └───────────────────────────────────────────────────────────────┘      │
//	└────────────────────────────────────────────────────────────────────────┘

// StreamEventType 区分流式响应的不同阶段
type StreamEventType int

const (
	// StreamEventThinkingDelta 代表模型的内部推理/思考过程增量（Phase 1 慢思考）
	// 引擎应实时打印这些内容，同时可选择性地将其存入上下文
	StreamEventThinkingDelta StreamEventType = iota

	// StreamEventTextDelta 代表模型对用户的纯文本回复增量
	StreamEventTextDelta

	// StreamEventToolCallBegin 工具调用初始化 —— Index + ID + Name 在此时确定
	// 后续该工具调用的 JSON 参数片段通过 StreamEventToolCallArgsDelta 投递
	StreamEventToolCallBegin

	// StreamEventToolCallArgsDelta 工具调用 JSON 参数的一个片段
	// 调用方按顺序拼接所有同 Index 的 Delta 即可得到完整的 JSON 参数字符串
	StreamEventToolCallArgsDelta

	// StreamEventDone 流已正常结束，底层连接关闭
	// 在此事件之后，生产者必须 close(channel)
	StreamEventDone

	// StreamEventError 流中发生不可恢复的错误
	StreamEventError

	// StreamEventUsage 携带本次 API 调用的 Token 消耗统计
	// 用于自适应压缩决策：Compactor 根据真实 PromptTokens 与模型窗口的比值调整压缩策略
	StreamEventUsage
)

// StreamEvent 是流式响应的最小传输单元
//
// 生产者契约（Provider 侧）：
//  1. 必须发送一个且仅一个 Done 或 Error 事件作为流的终点
//  2. 发送终点事件后必须 close(channel)
//  3. ToolCallBegin 必须在同 Index 的 ToolCallArgsDelta 之前发送
//  4. 事件发送顺序与模型生成顺序一致
//
// 消费者契约（Engine 侧）：
//  1. 必须持续消费 channel 直到其关闭
//  2. 不消费会导致生产者 goroutine 泄漏
type StreamEvent struct {
	Type StreamEventType

	// Delta 承载增量文本：
	//   - ThinkingDelta / TextDelta: 模型的逐字输出
	//   - ToolCallArgsDelta: 工具调用的 JSON 片段
	Delta string

	// 工具调用定位字段（ToolCallBegin 时全部填充，ArgsDelta 时只需 Index + Delta）
	ToolCallIndex int
	ToolCallID    string
	ToolCallName  string

	// Error 仅在 StreamEventError 类型时有效
	Error error

	// Usage 仅在 StreamEventUsage 类型时有效，携带本次 API 调用的 Token 消耗
	Usage *schema.TokenUsage
}
