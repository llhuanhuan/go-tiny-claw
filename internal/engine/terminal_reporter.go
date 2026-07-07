package engine

import (
	"context"
	"fmt"
	"strings"
)

// TerminalReporter 将引擎的运行状态实时输出到终端的 Reporter 实现。
// 适用于 CLI 场景：用户直接在命令行与 Agent 交互。
type TerminalReporter struct{}

// NewTerminalReporter 创建一个终端输出报告器。
func NewTerminalReporter() *TerminalReporter {
	return &TerminalReporter{}
}

func (r *TerminalReporter) OnThinking(ctx context.Context) {
	fmt.Print("🧠 [内部思考]: ")
}

func (r *TerminalReporter) OnStreamDelta(ctx context.Context, delta string, isThinking bool) {
	// 流式增量直接打印到终端，实现实时逐字输出
	fmt.Print(delta)
}

func (r *TerminalReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	fmt.Printf("[🛠️ 调用工具] %s\n", toolName)

	// 将参数中的换行符压缩，保持单行输出整洁
	displayArgs := strings.ReplaceAll(args, "\n", "\\n")
	displayArgs = strings.ReplaceAll(displayArgs, "\r", "\\r")

	if len(displayArgs) > 150 {
		displayArgs = displayArgs[:150] + "... (已截断)"
	}
	fmt.Printf("   参数: %s\n", displayArgs)
}

func (r *TerminalReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		fmt.Printf("  ⚠️  [%s] 执行出错: %s\n", toolName, result)
		if result != "" {
			fmt.Printf("      错误信息: %s\n", result)
		}
	} else {
		// 截断过长输出，终端刷屏体验很差
		display := result
		if len(display) > 300 {
			display = display[:300] + "... (已截断)"
		}
		fmt.Printf("  ✅ [%s] 执行成功 (%d 字节)\n", toolName, len(result))
		if display != "" {
			fmt.Printf("      输出: %s\n", display)
		}
	}
}

func (r *TerminalReporter) OnMessage(ctx context.Context, content string) {
	// 流式模式下文本已通过 OnStreamDelta 实时输出，这里只打印结尾换行
	fmt.Printf("\n")
}

// 编译时类型检查：确保 TerminalReporter 实现了 Reporter 接口
var _ Reporter = (*TerminalReporter)(nil)
