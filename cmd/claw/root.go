package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// 语义化版本号 — 构建时注入
var (
	version = "0.2.0-dev"
	commit  = "unknown"
)

// buildLegacyArgs 从 os.Args 中捕获旧格式 `-prompt=xxx` 参数，
// 兼容无 cobra 时代的调用方式。
// 返回捕获到的 prompt 和移除后的 args。
func buildLegacyArgs() (string, []string) {
	args := os.Args[1:]
	var prompt string
	var cleaned []string

	for _, arg := range args {
		if strings.HasPrefix(arg, "-prompt=") {
			prompt = strings.TrimPrefix(arg, "-prompt=")
		} else if strings.HasPrefix(arg, "-dir=") {
			// 保留 -dir 给 cobra 处理（run 子命令会解析）
			cleaned = append(cleaned, arg)
		} else if arg == "-prompt" {
			// 不带等号的形式，跳过（下一个 arg 当作 prompt 值）
			// 这种情况比较少见，暂不处理
			continue
		} else {
			cleaned = append(cleaned, arg)
		}
	}

	return prompt, cleaned
}

// newRootCmd 创建 cobra 根命令。
// 当检测到旧格式 `-prompt=xxx` 时，自动路由到 run 子命令。
func newRootCmd() *cobra.Command {
	// 先捕获旧格式参数
	legacyPrompt, cleanedArgs := buildLegacyArgs()

	rootCmd := &cobra.Command{
		Use:   "claw",
		Short: "go-tiny-claw — 轻量级自托管 AI Agent 引擎",
		Long: `go-tiny-claw 是一个轻量级、可自托管的 AI Agent 引擎。
支持 ReAct 循环、文件系统操作、Shell 命令执行、子智能体等能力。

用法:
  claw run "你的任务描述"        # 单次任务
  claw run -f task.txt          # 从文件读取任务
  claw                         # 进入 REPL 交互模式
  claw feishu                  # 启动飞书机器人
  claw server                  # 启动 HTTP 服务器`,
		// 无子命令时进入 REPL（交互模式）或兼容旧格式
		RunE: func(cmd *cobra.Command, args []string) error {
			// 兼容旧格式：检测到 -prompt 自动路由到 run
			if legacyPrompt != "" {
				dir, _ := cmd.Flags().GetString("dir")
				if dir != "" {
					if err := os.Chdir(dir); err != nil {
						return fmt.Errorf("切换工作目录失败: %w", err)
					}
				}
				sessionID, _ := cmd.Flags().GetString("session")
				return runRun(legacyPrompt, sessionID)
			}

			// 无参数且 stdout 是终端 → 进入 REPL
			if isStdoutTTY() {
				return runREPL("")
			}

			// stdin 是管道 → 读取管道内容作为 prompt（pipe mode）
			scanner := bufio.NewScanner(os.Stdin)
			var lines []string
			for scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("读取 stdin 失败: %w", err)
			}
			if len(lines) == 0 {
				return cmd.Help()
			}

			dir, _ := cmd.Flags().GetString("dir")
			if dir != "" {
				if err := os.Chdir(dir); err != nil {
					return fmt.Errorf("切换工作目录失败: %w", err)
				}
			}
			sessionID, _ := cmd.Flags().GetString("session")
			return runRun(strings.Join(lines, "\n"), sessionID)
		},
		SilenceUsage: true,
	}

	// 保留旧格式兼容的 flag
	rootCmd.PersistentFlags().String("dir", "", "工作目录 (兼容旧格式 -dir)")
	rootCmd.PersistentFlags().String("session", "", "会话 ID (兼容旧格式 -session)")

	// 如果有旧格式参数，覆盖 os.Args 让 cobra 正确解析
	if legacyPrompt != "" {
		os.Args = append([]string{os.Args[0]}, cleanedArgs...)
	}

	// 注册子命令
	rootCmd.AddCommand(
		newRunCmd(),
		newReplCmd(),
		newFeishuCmd(),
		newServerCmd(),
		newSessionCmd(),
		newConfigCmd(),
		newVersionCmd(),
	)

	return rootCmd
}
