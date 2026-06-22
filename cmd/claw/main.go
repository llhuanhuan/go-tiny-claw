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
	// 4. 将真实的 ReadFile 工具挂载到注册表中
	readFileTool := tools.NewReadFileTool(workDir)
	registry.Register(readFileTool)
	// 5. 实例化核心引擎，由于任务简单，我们关闭思考阶段 (EnableThinking = false) 以加快速度
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, false)

	// 6. 下发一个必须通过真实工具才能完成的任务
	prompt := "请调用工具读取一下当前工作区目录下 hello.txt 文件的内容，并用一句话向我总结它说了什么。"
	err := eng.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}
