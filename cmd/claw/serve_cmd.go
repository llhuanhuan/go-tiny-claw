package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"

	"github.com/spf13/cobra"
)

// newServeCmd 创建 `claw serve` 子命令，支持同时启动多个消息渠道。
func newServeCmd() *cobra.Command {
	var (
		enableFeishu bool
		enableILink  bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动多个消息渠道（飞书、个人微信等）",
		Long: `同时启动多个消息渠道，共享同一个 AI Agent 引擎。

示例：
  claw serve --feishu --ilink    # 同时启动飞书和个人微信
  claw serve --feishu            # 仅启动飞书
  claw serve --ilink             # 仅启动个人微信`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !enableFeishu && !enableILink {
				return fmt.Errorf("至少启用一个渠道：使用 --feishu 或 --ilink")
			}

			cfg, err := LoadConfig("claw.yaml")
			if err != nil {
				return err
			}
			return runServe(cfg, enableFeishu, enableILink)
		},
		SilenceUsage: true,
	}

	cmd.Flags().BoolVar(&enableFeishu, "feishu", false, "启用飞书机器人")
	cmd.Flags().BoolVar(&enableILink, "ilink", false, "启用 iLink Bot（个人微信）")

	return cmd
}

// runServe 同时启动多个消息渠道。
// 使用 goroutine 并发运行，共享同一个 Bootstrap 实例。
// 任意一个渠道退出或收到中断信号时，所有渠道优雅关闭。
func runServe(cfg *AppConfig, enableFeishu, enableILink bool) error {
	b, err := Bootstrap(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if b.CancelFunc != nil {
			b.CancelFunc()
		}
	}()

	// 监听中断信号
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 2) // 缓冲区大小 = 最大渠道数

	// 启动飞书
	if enableFeishu {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runFeishu(b.Engine, b.BillingSession, cfg)
			errCh <- nil // 飞书退出
		}()
		logInfo("飞书渠道已启动")
	}

	// 启动 iLink Bot
	if enableILink {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runILink(b.Engine, b.BillingSession, cfg)
			errCh <- nil // iLink 退出
		}()
		logInfo("iLink 渠道已启动")
	}

	// 等待任意一个渠道退出或中断信号
	select {
	case <-ctx.Done():
		logInfo("收到中断信号，正在关闭所有渠道...")
	case err := <-errCh:
		if err != nil {
			logInfo(fmt.Sprintf("渠道退出: %v，正在关闭其他渠道...", err))
		}
	}

	// 等待所有 goroutine 完成
	wg.Wait()
	logInfo("所有渠道已关闭")
	return nil
}

// logInfo 输出带前缀的日志。
func logInfo(msg string) {
	fmt.Printf("[Serve] %s\n", msg)
}
