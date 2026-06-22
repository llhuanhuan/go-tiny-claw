package main

import (
	"context"
	"log"
	"os"

	"github.com/lhuan/go-tiny-claw/internal/engine"
	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/tools"
)

func main() {
	// 确保已设置 DEEPSEEK_API_KEY
	if os.Getenv("DEEPSEEK_API_KEY") == "" {
		log.Fatal("请先设置 DEEPSEEK_API_KEY 环境变量")
	}

	// 设置：set DEEPSEEK_API_KEY=sk-3xxxxxxxxxxx
	// 验证:echo $env:DEEPSEEK_API_KEY

	// 1. 获取工作区物理边界
	workDir, _ := os.Getwd()

	// 2. 初始化真实的 Provider 大脑 (指向 DeepSeek)
	// 这里你可以任意切换 NewDeepSeekClaudeProvider 或 NewDeepSeekOpenAIProvider，效果完全一致！
	// llmProvider := provider.NewDeepSeekClaudeProvider("deepseek-v4-pro")
	llmProvider := provider.NewDeepSeekOpenAIProvider("deepseek-v4-pro")

	// 3. 初始化真实的 Tool Registry
	registry := tools.NewRegistry()

	// 4. 挂载工具集 —— 四件套: 文件读写 + 编辑 + Bash + 后台进程管理
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))
	registry.Register(tools.NewTaskOutputTool()) // 查看后台进程日志
	registry.Register(tools.NewTaskStopTool())   // 终止 / 列出后台进程

	// 5. 实例化核心引擎，由于任务简单，我们关闭思考阶段 (EnableThinking = false) 以加快速度
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, false)

	// 6. 【缩进陷阱】下发一个 edit_file 任务。
	// config.go 中有一行深埋于 switch/case 内的日志，带两层 Tab 缩进。
	// 我们故意用自然语言描述目标，诱使大模型写出缺少缩进的 old_text，
	// 以此验证 edit_file 的 L4 滑动窗口逐行去缩进匹配能否兜底成功。
	prompt := `请用 edit_file 工具，把 cmd/claw/config.go 中那句 "星宿老仙，法力无边！" 的日志消息，替换为 "千秋万载，一统江湖！"。整行代码替换，保持其他部分不变。`
	err := eng.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}
