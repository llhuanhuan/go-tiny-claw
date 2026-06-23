package engine

import "context"

// Reporter 定义了 Agent 引擎向外界输出信息的规范。
// 这使得引擎可以无缝切换终端 (CLI)、飞书、钉钉、微信甚至 WebUI 等不同的展现层。
//
// 实现者只需关心"如何展示"，而无需触碰引擎的核心逻辑。
type Reporter interface {
	// OnThinking 当模型开始进行慢思考 (Reasoning) 时调用。
	// 引擎在剥夺工具访问权、强制模型进入规划阶段前触发此回调。
	OnThinking(ctx context.Context)

	// OnToolCall 当模型决定调用工具时调用。
	// toolName: 被调用的工具名称 (如 "read_file", "bash")
	// args: 序列化后的 JSON 参数字符串
	OnToolCall(ctx context.Context, toolName string, args string)

	// OnToolResult 当工具在底层执行完毕并返回结果时调用。
	// toolName: 工具名称
	// result: 工具执行输出（可能已被截断，完整数据仍会传递给大模型）
	// isError: 标记此次执行是否以失败告终
	OnToolResult(ctx context.Context, toolName string, result string, isError bool)

	// OnMessage 当模型宣告任务完成，向用户输出最终纯文本回答时调用。
	// 对于流式输出，此方法在流完全结束后被调用，content 为完整文本。
	OnMessage(ctx context.Context, content string)
}
