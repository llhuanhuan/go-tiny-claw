package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/lhuan/go-tiny-claw/internal/engine"
	"github.com/spf13/cobra"
)

// newRunCmd 创建 `claw run` 子命令。
func newRunCmd() *cobra.Command {
	var promptFile string
	var workDir string
	var sessionID string

	cmd := &cobra.Command{
		Use:   "run [prompt]",
		Short: "执行单次任务",
		Long: `执行一次性的 AI Agent 任务。支持直接传入 prompt 或从文件读取。

示例:
  claw run "帮我写一个 hello world"
  claw run -f task.txt
  claw run -f task.txt -d /path/to/project
  echo "你的任务" | claw run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 切换工作目录
			if workDir != "" {
				if err := os.Chdir(workDir); err != nil {
					return fmt.Errorf("切换工作目录失败: %w", err)
				}
			}

			var prompt string

			if promptFile != "" {
				data, err := os.ReadFile(promptFile)
				if err != nil {
					return fmt.Errorf("读取 prompt 文件失败: %w", err)
				}
				prompt = strings.TrimSpace(string(data))
			} else if len(args) > 0 {
				prompt = strings.Join(args, " ")
			} else if !isTerminal() {
				scanner := newScanner(os.Stdin)
				var lines []string
				for scanner.Scan() {
					lines = append(lines, scanner.Text())
				}
				prompt = strings.TrimSpace(strings.Join(lines, "\n"))
			}

			if prompt == "" {
				return fmt.Errorf("请提供 prompt 内容（作为参数、-f 文件或 stdin 管道）")
			}

			return runRun(prompt, sessionID)
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVarP(&promptFile, "file", "f", "", "从文件读取 prompt")
	cmd.Flags().StringVarP(&workDir, "dir", "d", "", "工作目录（默认当前目录）")
	cmd.Flags().StringVarP(&sessionID, "session", "s", "", "会话 ID（留空自动生成）")

	return cmd
}

// runRun 执行单次任务的核心逻辑。
func runRun(prompt, sessionID string) error {
	cfg := DefaultConfig()

	b, err := Bootstrap(cfg)
	if err != nil {
		return fmt.Errorf("引擎初始化失败: %w", err)
	}

	if sessionID == "" {
		sessionID = "default"
	}

	ctx := context.Background()
	session := engine.GlobalSessionMgr.GetOrCreate(sessionID, b.WorkDir)
	reporter := NewCLITerminalReporter()

	log.Printf("[CLI] 启动单次任务: session=%s, workDir=%s\n", sessionID, b.WorkDir)
	return b.Engine.Run(ctx, session, prompt, reporter)
}
