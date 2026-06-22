package main

import (
	"context"
	"log"
	"os"

	"github.com/lhuan/go-tiny-claw/internal/engine"
	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// 伪造的工具注册表 (用于测试 Provider 的工具提取能力)
type mockRegistry struct{}

func (m *mockRegistry) GetAvailableTools() []schema.ToolDefinition {
	return []schema.ToolDefinition{
		{
			Name:        "get_weather",
			Description: "获取指定城市的当前天气情况。",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"city": map[string]interface{}{
						"type": "string",
					},
				},
				"required": []string{"city"},
			},
		},
	}
}
func (m *mockRegistry) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	log.Printf("  -> [Mock 工具执行] 获取 %s 的天气中...\n", call.Name)
	return schema.ToolResult{
		ToolCallID: call.ID,
		Output:     "API 返回：今天是晴天，气温 25 度。",
		IsError:    false,
	}
}
func main() {
	// 确保已设置 DEEPSEEK_API_KEY
	if os.Getenv("DEEPSEEK_API_KEY") == "" {
		log.Fatal("请先设置 DEEPSEEK_API_KEY 环境变量")
	}

	// 设置：set DEEPSEEK_API_KEY=sk-3xxxxxxxxxxx
	// 验证:echo $env:DEEPSEEK_API_KEY
	workDir, _ := os.Getwd()
	// 1. 初始化真实的 Provider 大脑 (指向 DeepSeek)
	// 这里你可以任意切换 NewDeepSeekClaudeProvider 或 NewDeepSeekOpenAIProvider，效果完全一致！
	// llmProvider := provider.NewDeepSeekClaudeProvider("deepseek-v4-pro")
	llmProvider := provider.NewDeepSeekOpenAIProvider("deepseek-v4-pro")
	// 2. 注入伪造的工具注册表
	registry := &mockRegistry{}
	// 3. 实例化并运行引擎，开启 EnableThinking = true (开启慢思考阶段！)
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, false)
	// 设定测试任务
	prompt := "我想去北京跑步，帮我查查天气适合吗？"
	err := eng.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
}
