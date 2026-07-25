package main

import (
	"github.com/spf13/cobra"
)

// newFeishuCmd 创建 `claw feishu` 子命令。
func newFeishuCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "feishu",
		Short: "启动飞书机器人",
		Long:  "连接飞书开放平台，启动 AI Agent 机器人服务。",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig("claw.yaml")
			if err != nil {
				return err
			}
			return runFeishuMode(cfg)
		},
		SilenceUsage: true,
	}
	return cmd
}

// runFeishuMode 启动飞书机器人模式。
// 从 main.go 的 runFeishu 迁移而来。
func runFeishuMode(cfg *AppConfig) error {
	b, err := Bootstrap(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if b.CancelFunc != nil {
			b.CancelFunc()
		}
	}()
	runFeishu(b.Engine, b.BillingSession, cfg)
	return nil
}
