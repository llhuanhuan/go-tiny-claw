// cmd/trace-demo/main.go
//
// Tracing 演示程序：同时输出到 Jaeger（甘特图）+ 终端（文本树）+ 文件（JSON）
//
// 用法：
//
//	go run cmd/trace-demo/main.go
//	然后打开浏览器 http://localhost:16686 → 选择服务 "go-tiny-claw-demo" → 查看甘特图
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/observability"
)

func main() {
	log.Println("🚀 Tracing 演示程序启动...")
	log.Println("📊 Jaeger UI: http://localhost:16686")
	log.Println("")

	workDir, _ := os.Getwd()
	ctx := context.Background()

	// ═══════════════════════════════════════════════════
	// 1. 创建 TraceProvider，注册三个 Exporter
	// ═══════════════════════════════════════════════════
	otelExp, err := observability.NewOTelExporter(ctx, "localhost:4317", "go-tiny-claw-demo")
	if err != nil {
		log.Fatalf("创建 OTel Exporter 失败: %v", err)
	}

	traceProvider := observability.NewTraceProvider(
		observability.NewFileExporter(workDir, "demo-session"),
		observability.NewLogExporter(),
		otelExp,
	)
	defer func() { _ = traceProvider.Shutdown(ctx) }()

	// ═══════════════════════════════════════════════════
	// 2. 模拟一次完整的 Agent Run（带多轮 Turn）
	// ═══════════════════════════════════════════════════
	ctx, rootSpan := observability.StartSpan(ctx, "Agent.Run")
	rootSpan.AddAttribute("SessionID", "demo-session")
	rootSpan.AddAttribute("WorkDir", workDir)
	rootSpan.AddAttribute("Prompt", "帮我分析项目结构并生成报告")

	// ---- Turn-1: LLM 思考 + 调用工具 ----
	simulateTurn(ctx, 1, []toolCall{
		{name: "read_file", args: `{"path": "go.mod"}`, output: "module github.com/lhuan/go-tiny-claw\ngo 1.21", duration: 15},
		{name: "bash", args: `{"command": "find . -name '*.go' | wc -l"}`, output: "42", duration: 120},
	})

	// ---- Turn-2: LLM 继续推理 + 写文件 ----
	simulateTurn(ctx, 2, []toolCall{
		{name: "write_file", args: `{"path": "REPORT.md", "content": "# 项目分析报告\n..."}`, output: "成功写入 REPORT.md", duration: 8},
	})

	// ---- Turn-3: LLM 最终回复（无工具调用）----
	simulateTurn(ctx, 3, nil)

	rootSpan.EndSpan()

	// ═══════════════════════════════════════════════════
	// 3. 触发所有 Exporter
	// ═══════════════════════════════════════════════════
	log.Println("")
	if err := traceProvider.Export(ctx, rootSpan); err != nil {
		log.Printf("⚠️ 部分 Exporter 导出失败: %v", err)
	}

	log.Println("")
	log.Println("✅ 演示完成！")
	log.Println("")
	log.Println("📊 查看方式：")
	log.Println("  1. 甘特图: 浏览器打开 http://localhost:16686 → 服务选 'go-tiny-claw-demo' → Find Traces")
	log.Println("  2. 文本树: 查看上方终端输出")
	log.Printf("  3. JSON:   查看 %s/.claw/traces/ 目录\n", workDir)
}

// toolCall 模拟一次工具调用
type toolCall struct {
	name     string
	args     string
	output   string
	duration time.Duration
}

// simulateTurn 模拟一轮 ReAct Turn
func simulateTurn(ctx context.Context, turnNum int, tools []toolCall) {
	turnCtx, turnSpan := observability.StartSpan(ctx, fmt.Sprintf("Turn-%d", turnNum))
	turnSpan.AddAttribute("context_message_count", 2+turnNum*2)

	// 模拟 LLM 推理（Thinking）
	if turnNum <= 2 { // 前两轮有 Thinking
		_, thinkSpan := observability.StartSpan(turnCtx, "LLM.Thinking")
		time.Sleep(time.Duration(500+rand.Intn(1000)) * time.Millisecond)
		thinkSpan.EndSpan()
	}

	// 模拟 LLM 推理（Action）
	_, actSpan := observability.StartSpan(turnCtx, "LLM.Action")
	time.Sleep(time.Duration(800+rand.Intn(1500)) * time.Millisecond)
	actSpan.EndSpan()

	// 模拟工具执行
	for _, tc := range tools {
		_, toolSpan := observability.StartSpan(turnCtx, "Tool.Execute")
		toolSpan.AddAttribute("tool_name", tc.name)
		toolSpan.AddAttribute("arguments", tc.args)
		toolSpan.AddAttribute("output_preview", truncate(tc.output, 100))
		toolSpan.AddAttribute("is_error", false)
		time.Sleep(tc.duration * time.Millisecond)
		toolSpan.EndSpan()
	}

	turnSpan.EndSpan()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
