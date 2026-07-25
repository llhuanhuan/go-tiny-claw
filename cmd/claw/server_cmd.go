package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"

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
			cfg, err := LoadConfig("claw.yaml")
			if err != nil {
				return err
			}
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
	defer func() {
		if b.CancelFunc != nil {
			b.CancelFunc()
		}
	}()

	mux := http.NewServeMux()

	// 健康检查端点
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// 如果配置了企业微信，注册 webhook 端点
	if cfg.Wechat.WebhookURL != "" {
		runWechat(b.Engine, cfg)
		return nil // runWechat 内部会启动 HTTP 服务
	}

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{Addr: addr, Handler: mux}

	// 优雅退出
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Println("[Server] 收到退出信号，正在关闭...")
		srv.Close()
	}()

	log.Printf("🚀 go-tiny-claw HTTP 服务器已启动，监听 %s\n", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("服务器启动失败: %w", err)
	}
	return nil
}
