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

func (r *TerminalReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	// 将参数中的换行符压缩，保持单行输出整洁
	compactArgs := strings.ReplaceAll(args, "\n", "")
	fmt.Printf("\n🛠️  [工具调用] %s(%s)\n", toolName, compactArgs)
}

func (r *TerminalReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		fmt.Printf("  ⚠️  [%s] 执行出错: %s\n", toolName, result)
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
	// 模型回复直接打印（流式在 consumeStream 中已实时输出，
	// 这里作为完整性保障 —— 若流未输出文本则补打）
	if content != "" {
		fmt.Print(content)
	}
}

// 编译时类型检查：确保 TerminalReporter 实现了 Reporter 接口
var _ Reporter = (*TerminalReporter)(nil)
