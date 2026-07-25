package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/lhuan/go-tiny-claw/internal/engine"
	"github.com/peterh/liner"
	"github.com/spf13/cobra"
)

// REPL 状态机
const (
	stateIdle       = iota // 等待用户输入
	stateRunning           // Agent 正在执行
	stateCancelling        // 正在取消
)

// newReplCmd 创建 `claw repl` 子命令。
func newReplCmd() *cobra.Command {
	var workDir string
	var sessionID string

	cmd := &cobra.Command{
		Use:   "repl",
		Short: "进入交互式 REPL 模式",
		Long:  "启动交互式对话循环。支持多轮对话、上下文保持、Ctrl+C 取消。",
		RunE: func(cmd *cobra.Command, args []string) error {
			if workDir != "" {
				if err := os.Chdir(workDir); err != nil {
					return fmt.Errorf("切换工作目录失败: %w", err)
				}
			}
			return runREPL(sessionID)
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVarP(&workDir, "dir", "d", "", "工作目录")
	cmd.Flags().StringVarP(&sessionID, "session", "s", "default", "会话 ID")

	return cmd
}

// replState 封装 REPL 的运行时状态。
type replState struct {
	state       atomic.Int32
	cancelFunc  atomic.Pointer[context.CancelFunc]
	engine      *engine.AgentEngine
	session     *engine.Session
	billingSess interface{ TotalTokens() int }
}

// runREPL 启动交互式 REPL 循环。
func runREPL(sessionID string) error {
	if !isStdoutTTY() {
		return fmt.Errorf("REPL 模式需要终端环境（stdout 必须是 TTY）")
	}

	cfg, err := LoadConfig("claw.yaml")
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}

	b, err := Bootstrap(cfg)
	if err != nil {
		return fmt.Errorf("引擎初始化失败: %w", err)
	}
	defer func() {
		if b.CancelFunc != nil {
			b.CancelFunc()
		}
	}()

	if sessionID == "" {
		sessionID = "repl"
	}
	session := engine.GlobalSessionMgr.GetOrCreate(sessionID, b.WorkDir)

	state := &replState{
		engine:      b.Engine,
		session:     session,
		billingSess: b.BillingSession,
	}
	state.state.Store(stateIdle)

	// 初始化 liner
	line := liner.NewLiner()
	defer line.Close()

	line.SetCtrlCAborts(true)
	promptStr := colorize(ansiGreen, "🦞 ")

	// 设置命令历史（存放在用户主目录）
	homeDir, _ := os.UserHomeDir()
	historyFile := ".claw_history"
	if homeDir != "" {
		historyFile = homeDir + string(os.PathSeparator) + ".claw_history"
	}
	loadHistory(line, historyFile)
	defer saveHistory(line, historyFile)

	// 信号处理：Ctrl+C 在 Agent 执行中取消，空闲时退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	quitCh := make(chan struct{})
	go func() {
		for range sigCh {
			switch state.state.Load() {
			case stateRunning:
				if fn := state.cancelFunc.Load(); fn != nil {
					state.state.Store(stateCancelling)
					(*fn)()
				}
			case stateIdle:
				fmt.Println("\n👋 再见！")
				signal.Stop(sigCh)
				close(quitCh)
				return
			}
		}
	}()

	fmt.Println(colorize(ansiBold, "🦞 go-tiny-claw REPL"))
	fmt.Println(colorize(ansiDim, "输入任务描述开始对话，Ctrl+C 取消当前任务或退出"))
	fmt.Println(colorize(ansiDim, "输入 /exit 退出，/reset 清空历史"))
	fmt.Println()

	for {
		// 检查是否收到退出信号
		select {
		case <-quitCh:
			return nil
		default:
		}

		input, err := line.Prompt(promptStr)
		if err != nil {
			if err == liner.ErrPromptAborted {
				fmt.Println("\n👋 再见！")
				return nil
			}
			return fmt.Errorf("输入读取失败: %w", err)
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		// 内置命令
		switch input {
		case "/exit", "/quit":
			fmt.Println("👋 再见！")
			return nil
		case "/reset":
			session.ClearHistory()
			fmt.Println(colorize(ansiYellow, "🧹 会话历史已清空"))
			continue
		case "/history":
			printHistory(session)
			continue
		}

		// 保存到 liner 历史
		line.AppendHistory(input)

		// 执行 Agent
		if err := executeREPLTurn(state, input); err != nil {
			if state.state.Load() == stateCancelling {
				fmt.Fprintf(os.Stderr, "\n%s\n", colorize(ansiYellow, "⚠️ 任务已取消"))
			} else {
				fmt.Fprintf(os.Stderr, "\n%s %v\n", colorize(ansiRed, "❌ 错误:"), err)
			}
		}

		state.state.Store(stateIdle)
		fmt.Println()
	}
}

// executeREPLTurn 执行一轮 REPL 对话。
func executeREPLTurn(state *replState, prompt string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	state.cancelFunc.Store(&cancel)
	state.state.Store(stateRunning)

	reporter := NewCLITerminalReporter()
	return state.engine.Run(ctx, state.session, prompt, reporter)
}

// printHistory 打印会话历史摘要。
func printHistory(session *engine.Session) {
	// session.history 是私有的，通过 WorkingMemory 获取最近的消息
	msgs := session.GetWorkingMemory(10, 0)
	if len(msgs) == 0 {
		fmt.Println(colorize(ansiDim, "(会话历史为空)"))
		return
	}

	fmt.Println(colorize(ansiBold, "📜 最近消息:"))
	for _, msg := range msgs {
		role := msg.Role
		content := msg.Content
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		switch role {
		case "user":
			if msg.ToolCallID != "" {
				fmt.Printf("  %s %s\n", colorize(ansiDim, "[工具结果]"), content)
			} else {
				fmt.Printf("  %s %s\n", colorize(ansiGreen, "[用户]"), content)
			}
		case "assistant":
			fmt.Printf("  %s %s\n", colorize(ansiBlue, "[助手]"), content)
		}
	}
}

// loadHistory 从文件加载命令历史。
func loadHistory(line *liner.State, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	line.ReadHistory(f)
}

// saveHistory 保存命令历史到文件。
func saveHistory(line *liner.State, path string) {
	f, err := os.Create(path)
	if err != nil {
		log.Printf("[REPL] 保存历史失败: %v\n", err)
		return
	}
	defer f.Close()
	line.WriteHistory(f)
}
