package main

import (
	"github.com/spf13/cobra"
)

// newILinkCmd 创建 `claw ilink` 子命令。
func newILinkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ilink",
		Short: "启动 iLink Bot（个人微信机器人）",
		Long:  "通过 HTTP 长轮询接入 iLink Bot，连接个人微信接收消息。",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig("claw.yaml")
			if err != nil {
				return err
			}
			return runILinkMode(cfg)
		},
		SilenceUsage: true,
	}
	return cmd
}

// runILinkMode 启动 iLink Bot 模式。
func runILinkMode(cfg *AppConfig) error {
	b, err := Bootstrap(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if b.CancelFunc != nil {
			b.CancelFunc()
		}
	}()
	runILink(b.Engine, b.BillingSession, cfg)
	return nil
}
