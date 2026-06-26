package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"

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
	// enableThinking := os.Getenv("ENABLE_THINKING") == "true"
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, true)

	// 3.5 注册渐进式暴露技能工具 (read_skill)
	// 引擎内部已持有 PromptComposer → SkillLoader 引用链，
	// 此处取出 SkillLoader 注入 ReadSkillTool，实现"元数据在 System Prompt、
	// 正文按需加载"的渐进式暴露架构。
	skillLoader := eng.SkillLoader()
	registry.Register(tools.NewReadSkillTool(skillLoader))

	// 4. 检测运行模式
	hasFeishu := os.Getenv("FEISHU_APP_ID") != ""
	hasWechat := os.Getenv("WECHAT_WEBHOOK_URL") != ""

	// 【注入新实现的终端输出器】
	// reporter := engine.NewTerminalReporter()

	// TODO: 以下为调试用 PromptComposer 端到端测试代码，生产环境请注释
	// prompt := `    我需要在当前目录下新建一个 ping.go，提供一个简单的 http ping 接口。    写完之后，帮我把代码用 git 提交一下。    `
	// err := eng.Run(context.Background(), prompt, reporter)
	// if err != nil {
	// 	log.Fatalf("引擎运行崩溃: %v", err)
	// }
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
	session := engine.GlobalSessionMgr.GetOrCreate("terminal", eng.WorkDir)
	if err := eng.Run(context.Background(), session, prompt, reporter); err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
	log.Println("[Bootstrap] Agent 任务执行完毕。")
}
