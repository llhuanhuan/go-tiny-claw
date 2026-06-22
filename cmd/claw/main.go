package main

import (
	"context"
	"log"
	"os"

	"github.com/lhuan/go-tiny-claw/internal/engine"
	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/tools"
)

// 伪造的工具注册表 (用于测试 Provider 的工具提取能力)
type mockRegistry struct{}

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

	// 4   挂载极简工具集
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))

	// 5. 实例化核心引擎，由于任务简单，我们关闭思考阶段 (EnableThinking = false) 以加快速度
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, false)

	// 6. 下发一个必须通过真实工具才能完成的任务
	prompt := `    请帮我执行以下操作：   
	 1. 用 bash 查看一下我当前电脑的 Go 版本。    
	 2. 帮我写一个简单的 helloworld.go 文件，输出 "Hello, go-tiny-claw!"。    
	 3. 用 bash 编译并运行这个 go 文件，确认它能正常工作。    `
	err := eng.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}
