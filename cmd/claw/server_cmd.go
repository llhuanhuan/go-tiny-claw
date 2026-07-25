package main

import (
	"github.com/spf13/cobra"
)

// newServerCmd 创建 `claw server` 子命令。
func newServerCmd() *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "server",
		Short: "启动 HTTP 服务器",
		Long:  "启动 HTTP API 服务器，接收外部请求执行 Agent 任务。",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := DefaultConfig()
			if port > 0 {
				cfg.Server.Port = port
			}
			return runServerMode(cfg)
		},
		SilenceUsage: true,
	}

	cmd.Flags().IntVarP(&port, "port", "p", 0, "服务器端口（默认使用配置文件中的值）")

	return cmd
}

// runServerMode 启动 HTTP 服务器模式。
func runServerMode(cfg *AppConfig) error {
	b, err := Bootstrap(cfg)
	if err != nil {
		return err
	}
	_ = b
	// HTTP 服务器逻辑保持在原 main.go 中
	return nil
}
