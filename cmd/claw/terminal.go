package main

import (
	"bufio"
	"context"
	"fmt"
	"os"

	"github.com/lhuan/go-tiny-claw/internal/engine"
)

// ANSI 颜色常量 — 直接使用裸转义序列，不引入第三方 color 包
const (
	ansiReset     = "\033[0m"
	ansiBold      = "\033[1m"
	ansiDim       = "\033[2m"
	ansiGreen     = "\033[32m"
	ansiYellow    = "\033[33m"
	ansiBlue      = "\033[34m"
	ansiMagenta   = "\033[35m"
	ansiCyan      = "\033[36m"
	ansiRed       = "\033[31m"
	ansiBgBlue    = "\033[44m"
)

// isTerminal 检测 stdout 是否连接到真实的终端设备。
// 在管道或重定向场景下返回 false，用于禁用 ANSI 颜色和交互式输入。
func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// colorize 仅在终端模式下为文本添加颜色。
// 管道模式下原样返回，避免 ANSI 转义序列污染脚本输出。
func colorize(color, text string) string {
	if !isTerminal() {
		return text
	}
	return color + text + ansiReset
}

// newScanner 创建一个 bufio.Scanner，支持最大 1MB 的单行。
func newScanner(f *os.File) *bufio.Scanner {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	return scanner
}

// CLITerminalReporter 实现 engine.Reporter 接口，将 Agent 状态输出到终端。
// 相比 engine.TerminalReporter，增加了管道模式支持：
//   - stdout 仅输出流式内容和最终消息（管道安全）
//   - stderr 输出装饰性信息（工具调用、思考状态等）
type CLITerminalReporter struct {
	isTTY bool
}

func NewCLITerminalReporter() *CLITerminalReporter {
	return &CLITerminalReporter{isTTY: isTerminal()}
}

func (r *CLITerminalReporter) OnThinking(_ context.Context) {
	if r.isTTY {
		fmt.Fprintf(os.Stderr, "\n%s\n", colorize(ansiCyan, "🧠 思考中..."))
	}
}

func (r *CLITerminalReporter) OnStreamDelta(_ context.Context, delta string, isThinking bool) {
	if isThinking {
		if r.isTTY {
			fmt.Fprintf(os.Stderr, "%s%s%s", ansiDim, delta, ansiReset)
		}
	} else {
		// 流式内容直接输出到 stdout — 管道模式下也能正常接收
		fmt.Print(delta)
	}
}

func (r *CLITerminalReporter) OnToolCall(_ context.Context, toolName string, args string) {
	if r.isTTY {
		preview := args
		if len(preview) > 150 {
			preview = preview[:150] + "..."
		}
		fmt.Fprintf(os.Stderr, "\n  %s %s %s\n",
			colorize(ansiBgBlue, " TOOL "),
			colorize(ansiBold, toolName),
			colorize(ansiDim, preview))
	}
}

func (r *CLITerminalReporter) OnToolResult(_ context.Context, toolName string, result string, isError bool) {
	if r.isTTY {
		status := colorize(ansiGreen, "✅")
		if isError {
			status = colorize(ansiRed, "❌")
		}
		preview := result
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		fmt.Fprintf(os.Stderr, "  %s %s\n", status, colorize(ansiDim, preview))
	}
}

func (r *CLITerminalReporter) OnMessage(_ context.Context, content string) {
	if r.isTTY {
		fmt.Println()
	}
}

// 编译时类型检查
var _ engine.Reporter = (*CLITerminalReporter)(nil)
