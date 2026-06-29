package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	ctxpkg "github.com/lhuan/go-tiny-claw/internal/context"
	"github.com/lhuan/go-tiny-claw/internal/engine"
	"github.com/lhuan/go-tiny-claw/internal/feishu"
	"github.com/lhuan/go-tiny-claw/internal/observability"
	"github.com/lhuan/go-tiny-claw/internal/permissions"
	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/tools"
	"github.com/lhuan/go-tiny-claw/internal/wechat"
)

func main() {
	workDir, _ := os.Getwd()

	// 0. 加载配置（config.yaml + 环境变量覆盖）
	cfg, err := LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 1. 初始化 LLM Provider
	rawProvider := detectProvider()

	// 1.5 创建计费追踪器：包装原始 Provider，自动记录每次 API 调用的 Token 消耗和成本
	billingSession := ctxpkg.NewSession("global-billing")
	llmProvider := observability.NewCostTracker(rawProvider, cfg.Model.Name, billingSession)

	// 2. 初始化动态权限引擎
	permConfigPath := filepath.Join(workDir, ".claw", "permissions.yaml")
	permEngine := permissions.NewEngine(permConfigPath)
	if err := permEngine.Load(); err != nil {
		log.Printf("[Bootstrap] ⚠️ 权限配置加载失败，使用默认策略: %v", err)
	} else {
		// 启动热更新监听
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go permEngine.StartHotReload(ctx)
		log.Printf("[Bootstrap] ✅ 动态权限引擎已启动")
	}

	// 3. 初始化 Tool Registry
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewBashToolWithPermissions(workDir, permEngine))
	registry.Register(tools.NewTaskOutputTool())
	registry.Register(tools.NewTaskStopTool())

	// 4. 实例化引擎
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, true, cfg.Model.PlanMode)
	eng.SetMaxContextWindow(cfg.Model.MaxContextWindow) // 自适应压缩：设置模型上下文窗口

	// 4.5 注册渐进式暴露技能工具 (read_skill)
	skillLoader := eng.SkillLoader()
	registry.Register(tools.NewReadSkillTool(skillLoader))

	// 4.6 注册子智能体工具 (spawn_subagent)
	// 为子智能体创建独立的只读注册表，仅暴露安全工具
	subRegistry := tools.NewRegistry()
	subRegistry.Register(tools.NewReadFileTool(workDir))
	subRegistry.Register(tools.NewBashToolWithPermissions(workDir, permEngine))
	subRegistry.Register(tools.NewReadSkillTool(skillLoader))
	registry.Register(tools.NewSubagentTool(eng, subRegistry, nil))
	registry.Register(tools.NewCheckSubagentTool())

	// 4.7 挂载工具执行计时中间件：记录每个工具的真实物理执行耗时
	registry.UseToolMiddleware(tools.ExecutionTimer())

	// 4.8 注入计费 Session 到引擎，启用试错成本指标
	eng.SetBillingSession(billingSession)

	// 5. 检测运行模式（优先飞书 > 微信 > 终端）
	switch {
	case cfg.Feishu.AppID != "":
		runFeishu(eng, cfg)

	case cfg.Wechat.WebhookURL != "":
		runWechat(eng, cfg)

	default:
		runTerminal(eng, billingSession)
	}
}

// detectProvider 直接读取 Claude Code (~/.claude/settings.json) 的 env 配置，
// 注入当前进程环境变量后创建 Anthropic Provider。
// 无需手动设置任何环境变量，完全复用 Claude Code 已配置的模型和密钥。
func detectProvider() provider.LLMProvider {
	loadClaudeCodeEnv()
	return provider.NewAnthropicProvider("")
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
func runFeishu(eng *engine.AgentEngine, cfg *AppConfig) {
	bot := feishu.NewFeishuBot(eng, cfg.Feishu.AppID, cfg.Feishu.AppSecret)

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

// runTerminal 终端 CLI 模式，支持 -prompt / -dir / -session 命令行参数。
func runTerminal(eng *engine.AgentEngine, billingSession *ctxpkg.Session) {
	promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述")
	workDirPtr := flag.String("dir", ".", "Agent 运行的工作区目录路径 (默认为当前目录)")
	sessionPtr := flag.String("session", "cli_default_session", "指定会话 ID，支持断点续传")
	flag.Parse()

	if *promptPtr == "" {
		fmt.Println("用法: go-tiny-claw -prompt \"你的任务描述\" [-dir /path/to/workdir] [-session session_id]")
		os.Exit(1)
	}

	workDir, err := filepath.Abs(*workDirPtr)
	if err != nil {
		log.Fatalf("解析工作区路径失败: %v", err)
	}

	fmt.Println("==================================================")
	fmt.Printf("🚀 启动 go-tiny-claw CLI 引擎...\n")
	fmt.Printf("📁 锁定工作区: %s\n", workDir)
	fmt.Println("==================================================")

	reporter := engine.NewTerminalReporter()
	session := engine.GlobalSessionMgr.GetOrCreate(*sessionPtr, workDir)

	log.Printf("[Bootstrap] 终端模式启动，Prompt: %s\n", *promptPtr)
	startTime := time.Now()

	if err := eng.Run(context.Background(), session, *promptPtr, reporter); err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}

	elapsed := time.Since(startTime)
	fmt.Println("\n==================================================")
	fmt.Printf("✨ 任务圆满结束。总耗时: %v\n", elapsed)
	fmt.Printf("💰 Session 累计消耗: $%.6f | Token: Input %d, Output %d\n",
		billingSession.TotalCostCNY, billingSession.TotalPromptTokens, billingSession.TotalCompletionTokens)
	fmt.Println("==================================================")
}
