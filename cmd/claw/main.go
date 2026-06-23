package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"

	"github.com/lhuan/go-tiny-claw/internal/engine"
	"github.com/lhuan/go-tiny-claw/internal/feishu"
	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/tools"
	"github.com/lhuan/go-tiny-claw/internal/wechat"
)

func main() {
	workDir, _ := os.Getwd()

	// 1. 初始化 LLM Provider
	llmProvider := detectProvider()

	// 2. 初始化 Tool Registry
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewTaskOutputTool())
	registry.Register(tools.NewTaskStopTool())

	// 3. 实例化引擎
	enableThinking := os.Getenv("ENABLE_THINKING") == "true"
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, enableThinking)

	// 4. 检测运行模式
	hasFeishu := os.Getenv("FEISHU_APP_ID") != ""
	hasWechat := os.Getenv("WECHAT_WEBHOOK_URL") != ""

	switch {
	case hasFeishu:
		// ================================================================
		// 飞书模式：WebSocket 长连接（无需公网 IP/域名）
		// ================================================================
		runFeishu(eng)

	case hasWechat:
		// ================================================================
		// 微信模式：HTTP Webhook 回调
		// ================================================================
		runWechat(eng)

	default:
		// ================================================================
		// 终端模式：交互式 CLI
		// ================================================================
		runTerminal(eng)
	}
}

// detectProvider 自动选择合适的 LLM Provider。
func detectProvider() provider.LLMProvider {
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		log.Println("[Bootstrap] 检测到 DEEPSEEK_API_KEY，使用 DeepSeek Provider")
		return provider.NewDeepSeekOpenAIProvider("deepseek-v4-pro")
	}
	if key := os.Getenv("ZHIPU_API_KEY"); key != "" {
		log.Println("[Bootstrap] 检测到 ZHIPU_API_KEY，使用 智谱 GLM-4.5 Provider")
		return provider.NewZhipuOpenAIProvider("glm-4.5-air")
	}
	log.Fatal("请设置 DEEPSEEK_API_KEY 或 ZHIPU_API_KEY 环境变量")
	return nil
}

// runFeishu 通过 WebSocket 长连接接入飞书。
func runFeishu(eng *engine.AgentEngine) {
	bot := feishu.NewFeishuBot(eng)

	// 监听 Ctrl+C 优雅退出
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	log.Println("🚀 go-tiny-claw 飞书长连接模式启动中...")
	if err := bot.Start(ctx); err != nil {
		log.Fatalf("飞书长连接失败: %v", err)
	}
	log.Println("[Bootstrap] 飞书长连接已断开，程序退出。")
}

// runWechat 通过 HTTP Webhook 接入企业微信。
func runWechat(eng *engine.AgentEngine) {
	bot := wechat.NewWeChatBot(eng)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/wechat", bot.ServeHTTP)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = ":48080"
	} else if port[0] != ':' {
		port = ":" + port
	}

	log.Printf("🚀 go-tiny-claw 微信服务端已启动，监听 %s/webhook/wechat\n", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}

// runTerminal 终端 CLI 模式。
func runTerminal(eng *engine.AgentEngine) {
	reporter := engine.NewTerminalReporter()

	prompt := "Hello, go-tiny-claw! 请用一句话介绍你自己。"
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	log.Printf("[Bootstrap] 终端模式启动，Prompt: %s\n", prompt)
	if err := eng.Run(context.Background(), prompt, reporter); err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
	log.Println("[Bootstrap] Agent 任务执行完毕。")
}
