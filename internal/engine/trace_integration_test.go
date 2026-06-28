package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/lhuan/go-tiny-claw/internal/context"
	"github.com/lhuan/go-tiny-claw/internal/observability"
	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/tools"
)

// ============================================================
// Tracing 集成测试：验证 Span 树结构的真实性
//
// 测试策略：
//   - 使用真实 LLM API 调用（无 API Key 自动跳过）
//   - Run() 结束后从 .claw/traces/ 读取导出的 JSON
//   - 逐层断言 Span 树结构：Root → Turn → Leaf
// ============================================================

// traceSpan 是用于反序列化 trace JSON 的辅助结构
type traceSpan struct {
	Name       string                 `json:"name"`
	StartTime  string                 `json:"start_time"`
	EndTime    string                 `json:"end_time"`
	DurationMs int64                  `json:"duration_ms"`
	Attributes map[string]interface{} `json:"attributes,omitempty"`
	Children   []*traceSpan           `json:"children,omitempty"`
}

// loadLatestTrace 从 .claw/traces/ 目录加载最新的 trace JSON 文件。
func loadLatestTrace(t *testing.T, workDir string) *traceSpan {
	t.Helper()

	traceDir := filepath.Join(workDir, ".claw", "traces")
	entries, err := os.ReadDir(traceDir)
	if err != nil {
		t.Fatalf("读取 traces 目录失败: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("traces 目录为空，Run() 未导出任何 Trace 文件")
	}

	var latest os.DirEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if latest == nil {
			latest = e
			continue
		}
		infoA, _ := latest.Info()
		infoB, _ := e.Info()
		if infoB.ModTime().After(infoA.ModTime()) {
			latest = e
		}
	}

	data, err := os.ReadFile(filepath.Join(traceDir, latest.Name()))
	if err != nil {
		t.Fatalf("读取 trace 文件 %s 失败: %v", latest.Name(), err)
	}

	var span traceSpan
	if err := json.Unmarshal(data, &span); err != nil {
		t.Fatalf("解析 trace JSON 失败: %v", err)
	}

	t.Logf("📂 已加载 Trace 文件: %s (%d bytes)", latest.Name(), len(data))
	return &span
}

// findChildrenByName 递归查找所有匹配 name 的子 Span
func findChildrenByName(span *traceSpan, name string) []*traceSpan {
	var result []*traceSpan
	for _, child := range span.Children {
		if child.Name == name {
			result = append(result, child)
		}
		result = append(result, findChildrenByName(child, name)...)
	}
	return result
}

// findDirectChildrenNames 返回直接子节点名称列表
func findDirectChildrenNames(span *traceSpan) []string {
	names := make([]string, len(span.Children))
	for i, c := range span.Children {
		names[i] = c.Name
	}
	return names
}

// printTraceTree 递归打印 Span 树结构（带缩进）
func printTraceTree(t *testing.T, span *traceSpan, depth int) {
	t.Helper()
	indent := strings.Repeat("  ", depth)
	prefix := "├──"
	if depth == 0 {
		prefix = "●"
	}

	attrSummary := ""
	if len(span.Attributes) > 0 {
		var parts []string
		for k, v := range span.Attributes {
			val := fmt.Sprintf("%v", v)
			if len(val) > 50 {
				val = val[:50] + "..."
			}
			parts = append(parts, fmt.Sprintf("%s=%s", k, val))
		}
		attrSummary = " | " + strings.Join(parts, ", ")
	}

	t.Logf("%s%s %s (%dms)%s", indent, prefix, span.Name, span.DurationMs, attrSummary)

	for _, child := range span.Children {
		printTraceTree(t, child, depth+1)
	}
}

// newTraceTestEngine 创建带 Tracing 的测试引擎（复用真实 Provider）
func newTraceTestEngine(t *testing.T, enableThinking bool) (*AgentEngine, string) {
	t.Helper()

	rawProvider := provider.NewAnthropicProvider("")
	workDir := t.TempDir()

	// 包装 CostTracker（与 main.go 同逻辑）
	llmProvider := observability.NewCostTracker(rawProvider, "glm-4.5-air", ctxpkg.NewSession("trace-test"))

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))

	eng := NewAgentEngine(llmProvider, registry, workDir, enableThinking, false)
	return eng, workDir
}

// ============================================================
// 场景 1: 单轮简单问答 —— 验证完整的 Span 树骨架
// ============================================================

func TestTrace_SingleTurn_Structure(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, workDir := newTraceTestEngine(t, false) // Thinking OFF

	session := GlobalSessionMgr.GetOrCreate("trace_single_turn", workDir)
	reporter := &captureReporter{}

	err := eng.Run(context.Background(), session, "回复两个字：你好", reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	root := loadLatestTrace(t, workDir)

	// 1. Root Span 必须是 "Agent.Run"
	if root.Name != "Agent.Run" {
		t.Fatalf("期望 Root Span='Agent.Run'，得到 '%s'", root.Name)
	}

	// 2. Root 必须携带 SessionID / WorkDir 属性
	if root.Attributes["SessionID"] == nil {
		t.Fatal("Root Span 缺少 SessionID 属性")
	}
	if root.Attributes["WorkDir"] == nil {
		t.Fatal("Root Span 缺少 WorkDir 属性")
	}
	t.Logf("  ✅ Root: %s | SessionID=%v | Duration=%dms",
		root.Name, root.Attributes["SessionID"], root.DurationMs)

	// 3. Root 下必须有 Turn-1
	turns := findChildrenByName(root, "Turn-1")
	if len(turns) != 1 {
		t.Fatalf("期望 1 个 Turn-1，实际 %d 个", len(turns))
	}
	turn := turns[0]
	t.Logf("  ✅ Turn-1: Duration=%dms, Children=%d", turn.DurationMs, len(turn.Children))

	// 4. Turn-1 必须有 context_message_count 属性
	if turn.Attributes["context_message_count"] == nil {
		t.Fatal("Turn-1 缺少 context_message_count 属性")
	}
	t.Logf("  ✅ context_message_count=%v", turn.Attributes["context_message_count"])

	// 5. Turn-1 下必须有 LLM.Action
	actionSpans := findChildrenByName(turn, "LLM.Action")
	if len(actionSpans) < 1 {
		t.Fatal("Turn-1 下缺少 LLM.Action Span")
	}
	t.Logf("  ✅ LLM.Action: %dms", actionSpans[0].DurationMs)

	// 6. 不应有 LLM.Thinking（已关闭）
	thinkSpans := findChildrenByName(root, "LLM.Thinking")
	if len(thinkSpans) != 0 {
		t.Fatalf("Thinking 已关闭，不应有 LLM.Thinking Span，实际 %d 个", len(thinkSpans))
	}

	// 7. 耗时合理性校验
	if root.DurationMs <= 0 {
		t.Fatalf("Root Duration 应 > 0，得到 %d", root.DurationMs)
	}
	if turn.DurationMs > root.DurationMs {
		t.Fatalf("Turn Duration (%dms) 不应超过 Root (%dms)", turn.DurationMs, root.DurationMs)
	}

	// 打印完整树
	t.Logf("\n📊 Trace 树结构:")
	printTraceTree(t, root, 0)
}

// ============================================================
// 场景 2: 带工具调用 —— 验证 Tool.Execute 叶子 Span
// ============================================================

func TestTrace_WithToolCall(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, workDir := newTraceTestEngine(t, false)

	// 写入测试文件
	os.WriteFile(filepath.Join(workDir, "test.txt"), []byte("hello-trace"), 0644)

	session := GlobalSessionMgr.GetOrCreate("trace_tool_call", workDir)
	reporter := &captureReporter{}

	err := eng.Run(context.Background(), session, "读取 test.txt 文件，告诉我里面写了什么。", reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	root := loadLatestTrace(t, workDir)

	// 找到 Turn-1
	turns := findChildrenByName(root, "Turn-1")
	if len(turns) < 1 {
		t.Fatal("缺少 Turn-1 Span")
	}
	turn := turns[0]

	// Turn 下应有 Tool.Execute
	toolSpans := findChildrenByName(turn, "Tool.Execute")
	if len(toolSpans) < 1 {
		t.Fatalf("Turn 下缺少 Tool.Execute Span（模型未调用工具）")
	}
	toolSpan := toolSpans[0]

	// 验证 Tool.Execute 的属性
	if toolSpan.Attributes["tool_name"] == nil {
		t.Fatal("Tool.Execute 缺少 tool_name 属性")
	}
	if toolSpan.Attributes["arguments"] == nil {
		t.Fatal("Tool.Execute 缺少 arguments 属性")
	}
	if toolSpan.Attributes["output_preview"] == nil {
		t.Fatal("Tool.Execute 缺少 output_preview 属性")
	}
	t.Logf("  ✅ Tool.Execute: tool_name=%v | %dms", toolSpan.Attributes["tool_name"], toolSpan.DurationMs)
	t.Logf("  ✅ output_preview: %v", toolSpan.Attributes["output_preview"])

	t.Logf("\n📊 Trace 树结构:")
	printTraceTree(t, root, 0)
}

// ============================================================
// 场景 3: 带 Thinking —— 验证 Compaction + Thinking + Action 三层叶子
// ============================================================

func TestTrace_WithThinking(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, workDir := newTraceTestEngine(t, true) // Thinking ON

	session := GlobalSessionMgr.GetOrCreate("trace_thinking", workDir)
	reporter := &captureReporter{}

	err := eng.Run(context.Background(), session, "回复两个字：收到", reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	root := loadLatestTrace(t, workDir)

	turns := findChildrenByName(root, "Turn-1")
	if len(turns) < 1 {
		t.Fatal("缺少 Turn-1 Span")
	}
	turn := turns[0]

	childNames := findDirectChildrenNames(turn)
	t.Logf("  Turn-1 子节点: %v", childNames)

	// 验证 context_message_count 属性
	if turn.Attributes["context_message_count"] == nil {
		t.Fatal("Turn-1 缺少 context_message_count 属性")
	}
	t.Logf("  ✅ context_message_count=%v", turn.Attributes["context_message_count"])

	// 验证 LLM.Thinking 和 LLM.Action 都存在
	thinkSpans := findChildrenByName(turn, "LLM.Thinking")
	if len(thinkSpans) < 1 {
		t.Fatal("缺少 LLM.Thinking Span（Thinking 已开启）")
	}
	t.Logf("  ✅ LLM.Thinking: %dms", thinkSpans[0].DurationMs)

	actionSpans := findChildrenByName(turn, "LLM.Action")
	if len(actionSpans) < 1 {
		t.Fatal("缺少 LLM.Action Span")
	}
	t.Logf("  ✅ LLM.Action: %dms", actionSpans[0].DurationMs)

	t.Logf("\n📊 Trace 树结构:")
	printTraceTree(t, root, 0)
}

// ============================================================
// 场景 4: 多轮 ReAct 循环 —— 验证多个 Turn Span 编号连续
// ============================================================

func TestTrace_MultipleTurns(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, workDir := newTraceTestEngine(t, false)

	os.WriteFile(filepath.Join(workDir, "data.txt"), []byte("SCORE=98"), 0644)

	session := GlobalSessionMgr.GetOrCreate("trace_multi_turn", workDir)
	reporter := &captureReporter{}

	// 大概率需要 Turn-1 读文件 → Turn-2 回复
	err := eng.Run(context.Background(), session,
		"读取 data.txt，找到 SCORE 的值，然后告诉我分数是多少。", reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	root := loadLatestTrace(t, workDir)

	// 统计 Turn 数量
	var turnCount int
	for _, child := range root.Children {
		if strings.HasPrefix(child.Name, "Turn-") {
			turnCount++
		}
	}
	t.Logf("  共 %d 个 Turn Span", turnCount)

	if turnCount < 1 {
		t.Fatal("至少应有 1 个 Turn Span")
	}

	// 验证每个 Turn 都有 context_message_count 属性
	for _, child := range root.Children {
		if !strings.HasPrefix(child.Name, "Turn-") {
			continue
		}
		if child.Attributes["context_message_count"] == nil {
			t.Errorf("%s 缺少 context_message_count 属性", child.Name)
		}
		t.Logf("  ✅ %s: %dms, context_messages=%v, %d children",
			child.Name, child.DurationMs, child.Attributes["context_message_count"], len(child.Children))
	}

	// 验证 Turn 编号连续
	for i := 1; i <= turnCount; i++ {
		expected := fmt.Sprintf("Turn-%d", i)
		found := false
		for _, child := range root.Children {
			if child.Name == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("缺少 %s Span", expected)
		}
	}

	t.Logf("\n📊 Trace 树结构:")
	printTraceTree(t, root, 0)
}

// ============================================================
// 场景 5: Trace 文件物理存在且 JSON 合法
// ============================================================

func TestTrace_FileExported(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, workDir := newTraceTestEngine(t, false)

	session := GlobalSessionMgr.GetOrCreate("trace_file_export", workDir)
	reporter := &captureReporter{}

	err := eng.Run(context.Background(), session, "回复 OK", reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	traceDir := filepath.Join(workDir, ".claw", "traces")
	entries, err := os.ReadDir(traceDir)
	if err != nil {
		t.Fatalf("traces 目录不存在: %v", err)
	}

	jsonCount := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		jsonCount++

		if !strings.HasPrefix(e.Name(), "trace_") {
			t.Errorf("文件名应以 'trace_' 开头: %s", e.Name())
		}

		data, err := os.ReadFile(filepath.Join(traceDir, e.Name()))
		if err != nil {
			t.Errorf("读取 %s 失败: %v", e.Name(), err)
			continue
		}
		var raw json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Errorf("%s 不是合法 JSON: %v", e.Name(), err)
			continue
		}
		t.Logf("  ✅ %s (%d bytes, 合法 JSON)", e.Name(), len(data))
	}

	if jsonCount == 0 {
		t.Fatal("未找到任何 Trace JSON 文件")
	}
}
