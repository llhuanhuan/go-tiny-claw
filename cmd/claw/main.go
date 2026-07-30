package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"

	ctxpkg "github.com/lhuan/go-tiny-claw/internal/context"
	"github.com/lhuan/go-tiny-claw/internal/engine"
	"github.com/lhuan/go-tiny-claw/internal/feishu"
	"github.com/lhuan/go-tiny-claw/internal/ilink"
	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/wechat"
)

func main() {
	rootCmd := newRootCmd()
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// detectProvider 自动检测环境变量，返回对应的 Provider。
//   - ANTHROPIC_BASE_URL 有值 → Claude SDK
//   - OPENAI_BASE_URL 有值 → OpenAI SDK
func detectProvider() (provider.LLMProvider, error) {
	loadClaudeCodeEnv()

	switch {
	case os.Getenv("ANTHROPIC_BASE_URL") != "":
		log.Println("[Bootstrap] 检测到 ANTHROPIC_BASE_URL，使用 Claude SDK")
		p, err := provider.NewClaudeProvider("ANTHROPIC_AUTH_TOKEN,ANTHROPIC_API_KEY",
			provider.WithBaseURL(os.Getenv("ANTHROPIC_BASE_URL")),
			provider.WithModel(os.Getenv("ANTHROPIC_MODEL")),
		)
		if err != nil {
			return nil, fmt.Errorf("创建 Claude Provider 失败: %w", err)
		}
		return p, nil

	case os.Getenv("OPENAI_BASE_URL") != "":
		log.Println("[Bootstrap] 检测到 OPENAI_BASE_URL，使用 OpenAI SDK")
		p, err := provider.NewOpenAIProvider("OPENAI_API_KEY",
			provider.WithBaseURL(os.Getenv("OPENAI_BASE_URL")),
			provider.WithModel(os.Getenv("OPENAI_MODEL")),
		)
		if err != nil {
			return nil, fmt.Errorf("创建 OpenAI Provider 失败: %w", err)
		}
		return p, nil

	default:
		return nil, fmt.Errorf("请设置 ANTHROPIC_BASE_URL 或 OPENAI_BASE_URL（可通过 Claude Code settings.json 配置）")
	}
}

// claudeCodeSettings 是 ~/.claude/settings.json 的部分结构。
type claudeCodeSettings struct {
	Env map[string]string `json:"env"`
}

// loadClaudeCodeEnv 读取 Claude Code 的 settings.json，将其中的 env 字段
// 注入到当前进程的环境变量中（不覆盖已存在的变量）。
func loadClaudeCodeEnv() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("[Bootstrap] 无法获取用户主目录: %v", err)
		return
	}

	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		log.Printf("[Bootstrap] 无法读取 Claude Code 配置 %s: %v", settingsPath, err)
		return
	}

	var settings claudeCodeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		log.Printf("[Bootstrap] 解析 Claude Code 配置失败: %v", err)
		return
	}

	injected := 0
	for key, val := range settings.Env {
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
			injected++
		}
	}
	log.Printf("[Bootstrap] 已从 Claude Code 配置注入 %d 个环境变量", injected)
}

// runFeishu 通过 WebSocket 长连接接入飞书。
// 使用工厂模式：每个飞书会话动态创建独立的引擎实例，实现 per-session 计费隔离。
func runFeishu(eng *engine.AgentEngine, billingSession *ctxpkg.Session, cfg *AppConfig) {
	workDir, _ := os.Getwd()

	// 工厂闭包：捕获共享依赖，为每个 Session 创建独立引擎
	factory := feishu.AgentEngineFactory(func(sess *engine.Session) *engine.AgentEngine {
		e := engine.NewAgentEngine(eng.Provider(), eng.Registry(), workDir, true, cfg.Model.PlanMode)
		e.SetMaxContextWindow(cfg.Model.MaxContextWindow)
		// 为每个会话创建独立的计费 Session，实现 per-session 计费隔离
		billing := ctxpkg.NewSession("feishu:" + sess.ID)
		e.SetBillingSession(billing)
		// 初始化分层记忆系统
		InitMemoryManager(e, cfg, sess.ID)
		return e
	})

	bot := feishu.NewFeishuBot(factory, workDir, cfg.Feishu.AppID, cfg.Feishu.AppSecret)

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
func runWechat(eng *engine.AgentEngine, cfg *AppConfig) {
	bot := wechat.NewWeChatBot(eng, wechat.WechatBotConfig{
		WebhookURL:     cfg.Wechat.WebhookURL,
		Token:          cfg.Wechat.Token,
		EncodingAESKey: cfg.Wechat.EncodingAESKey,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/wechat", bot.ServeHTTP)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	port := fmt.Sprintf(":%d", cfg.Server.Port)

	log.Printf("🚀 go-tiny-claw 微信服务端已启动，监听 %s/webhook/wechat\n", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}

// runILink 通过 HTTP 长轮询接入 iLink Bot（个人微信）。
// 使用工厂模式：每个用户会话动态创建独立的引擎实例，实现 per-session 计费隔离。
func runILink(eng *engine.AgentEngine, billingSession *ctxpkg.Session, cfg *AppConfig) {
	workDir, _ := os.Getwd()

	// 工厂闭包：捕获共享依赖，为每个 Session 创建独立引擎
	factory := ilink.AgentEngineFactory(func(sess *engine.Session) *engine.AgentEngine {
		e := engine.NewAgentEngine(eng.Provider(), eng.Registry(), workDir, true, cfg.Model.PlanMode)
		e.SetMaxContextWindow(cfg.Model.MaxContextWindow)
		// 为每个会话创建独立的计费 Session，实现 per-session 计费隔离
		billing := ctxpkg.NewSession("ilink:" + sess.ID)
		e.SetBillingSession(billing)
		// 初始化分层记忆系统
		InitMemoryManager(e, cfg, sess.ID)
		return e
	})

	bot := ilink.NewILinkBot(factory, workDir, cfg.ILink.Token, cfg.ILink.BaseURL)

	// 监听 Ctrl+C 优雅退出
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	log.Println("🚀 go-tiny-claw iLink Bot（个人微信）模式启动中...")
	if err := bot.Start(ctx); err != nil {
		log.Fatalf("iLink Bot 启动失败: %v", err)
	}
	log.Println("[Bootstrap] iLink Bot 长轮询已停止，程序退出。")
}
