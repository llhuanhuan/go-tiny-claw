package observability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ============================================================
// 测试基础设施
// ============================================================

// buildTestSpanTree 构建一棵测试用的 Span 树。
//
//	Root (Agent.Run)
//	├── Turn-1
//	│   ├── LLM.Action
//	│   └── Tool.Execute
//	└── Turn-2
//	    └── LLM.Action
func buildTestSpanTree() *Span {
	root := &Span{
		Name:       "Agent.Run",
		StartTime:  time.Now().Add(-5 * time.Second),
		EndTime:    time.Now(),
		DurationMs: 5000,
		Attributes: map[string]interface{}{
			"SessionID": "test-session",
			"WorkDir":   "/tmp/test",
		},
	}

	turn1 := &Span{
		Name:       "Turn-1",
		StartTime:  root.StartTime,
		EndTime:    root.StartTime.Add(3 * time.Second),
		DurationMs: 3000,
		Attributes: map[string]interface{}{
			"context_message_count": 2,
		},
	}

	action1 := &Span{
		Name:       "LLM.Action",
		StartTime:  turn1.StartTime,
		EndTime:    turn1.StartTime.Add(2 * time.Second),
		DurationMs: 2000,
	}

	toolExec := &Span{
		Name:       "Tool.Execute",
		StartTime:  action1.EndTime,
		EndTime:    action1.EndTime.Add(50*time.Millisecond),
		DurationMs: 50,
		Attributes: map[string]interface{}{
			"tool_name":     "read_file",
			"arguments":     `{"path": "test.txt"}`,
			"output_preview": "hello world",
			"is_error":      false,
		},
	}

	turn2 := &Span{
		Name:       "Turn-2",
		StartTime:  turn1.EndTime,
		EndTime:    root.EndTime,
		DurationMs: 2000,
		Attributes: map[string]interface{}{
			"context_message_count": 4,
		},
	}

	action2 := &Span{
		Name:       "LLM.Action",
		StartTime:  turn2.StartTime,
		EndTime:    turn2.EndTime,
		DurationMs: 2000,
	}

	// 构建父子关系
	turn1.Children = []*Span{action1, toolExec}
	turn2.Children = []*Span{action2}
	root.Children = []*Span{turn1, turn2}

	return root
}

// ============================================================
// FileExporter 测试
// ============================================================

// TestFileExporter_Export 验证文件导出器正确写出 JSON 文件。
func TestFileExporter_Export(t *testing.T) {
	workDir := t.TempDir()
	sessionID := "file_export_test"
	exp := NewFileExporter(workDir, sessionID)

	root := buildTestSpanTree()
	err := exp.Export(context.Background(), root)
	if err != nil {
		t.Fatalf("Export 失败: %v", err)
	}

	// 检查文件是否存在
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

		// 验证文件名格式
		if !strings.HasPrefix(e.Name(), "trace_") {
			t.Errorf("文件名应以 'trace_' 开头: %s", e.Name())
		}
		if !strings.Contains(e.Name(), sessionID) {
			t.Errorf("文件名应包含 sessionID: %s", e.Name())
		}

		// 验证 JSON 可解析
		data, err := os.ReadFile(filepath.Join(traceDir, e.Name()))
		if err != nil {
			t.Errorf("读取失败: %v", err)
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Errorf("非法 JSON: %v", err)
			continue
		}

		// 验证 Root Span 名称
		if parsed["name"] != "Agent.Run" {
			t.Errorf("期望 name=Agent.Run, 得到 %v", parsed["name"])
		}

		t.Logf("  ✅ %s (%d bytes)", e.Name(), len(data))
	}

	if jsonCount == 0 {
		t.Fatal("未生成任何 JSON 文件")
	}
}

// TestFileExporter_Shutdown 验证 Shutdown 是空操作。
func TestFileExporter_Shutdown(t *testing.T) {
	exp := NewFileExporter(t.TempDir(), "test")
	if err := exp.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown 应返回 nil: %v", err)
	}
}

// ============================================================
// LogExporter 测试
// ============================================================

// TestLogExporter_Export 验证日志导出器不 panic。
func TestLogExporter_Export(t *testing.T) {
	exp := NewLogExporter()
	root := buildTestSpanTree()

	// 只要不 panic 就算通过
	err := exp.Export(context.Background(), root)
	if err != nil {
		t.Fatalf("Export 失败: %v", err)
	}
	t.Log("  ✅ LogExporter 输出到终端（请目视检查上方日志）")
}

// TestLogExporter_Shutdown 验证 Shutdown 是空操作。
func TestLogExporter_Shutdown(t *testing.T) {
	exp := NewLogExporter()
	if err := exp.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown 应返回 nil: %v", err)
	}
}

// ============================================================
// TraceProvider 测试
// ============================================================

// mockExporter 是一个记录调用次数的 mock Exporter。
type mockExporter struct {
	exportCount  int
	shutdownCount int
	exportedSpan *Span
}

func (m *mockExporter) Export(_ context.Context, rootSpan *Span) error {
	m.exportCount++
	m.exportedSpan = rootSpan
	return nil
}

func (m *mockExporter) Shutdown(_ context.Context) error {
	m.shutdownCount++
	return nil
}

// TestTraceProvider_Export 验证 TraceProvider 并行调用所有 Exporter。
func TestTraceProvider_Export(t *testing.T) {
	mock1 := &mockExporter{}
	mock2 := &mockExporter{}
	tp := NewTraceProvider(mock1, mock2)

	root := buildTestSpanTree()
	tp.Export(context.Background(), root)

	if mock1.exportCount != 1 {
		t.Errorf("Exporter1 应被调用 1 次, 实际 %d 次", mock1.exportCount)
	}
	if mock2.exportCount != 1 {
		t.Errorf("Exporter2 应被调用 1 次, 实际 %d 次", mock2.exportCount)
	}

	// 验证传入的 Span 树是正确的
	if mock1.exportedSpan.Name != "Agent.Run" {
		t.Errorf("期望 Agent.Run, 得到 %s", mock1.exportedSpan.Name)
	}
	if len(mock1.exportedSpan.Children) != 2 {
		t.Errorf("期望 2 个子 Span, 得到 %d", len(mock1.exportedSpan.Children))
	}

	t.Log("  ✅ TraceProvider 并行导出到多个 Exporter")
}

// TestTraceProvider_Shutdown 验证 Shutdown 遍历所有 Exporter。
func TestTraceProvider_Shutdown(t *testing.T) {
	mock1 := &mockExporter{}
	mock2 := &mockExporter{}
	tp := NewTraceProvider(mock1, mock2)

	tp.Shutdown(context.Background())

	if mock1.shutdownCount != 1 {
		t.Errorf("Exporter1 Shutdown 应被调用 1 次, 实际 %d 次", mock1.shutdownCount)
	}
	if mock2.shutdownCount != 1 {
		t.Errorf("Exporter2 Shutdown 应被调用 1 次, 实际 %d 次", mock2.shutdownCount)
	}
}

// TestTraceProvider_NoExporters 验证无 Exporter 时不 panic。
func TestTraceProvider_NoExporters(t *testing.T) {
	tp := NewTraceProvider()

	root := buildTestSpanTree()
	tp.Export(context.Background(), root) // 应不 panic
	tp.Shutdown(context.Background())     // 应不 panic

	t.Log("  ✅ 空 TraceProvider 安全运行")
}

// ============================================================
// Span 树结构验证
// ============================================================

// TestSpanTree_Structure 验证构建的测试 Span 树结构正确。
func TestSpanTree_Structure(t *testing.T) {
	root := buildTestSpanTree()

	if root.Name != "Agent.Run" {
		t.Fatalf("root.Name = %s", root.Name)
	}
	if len(root.Children) != 2 {
		t.Fatalf("期望 2 个 Turn, 得到 %d", len(root.Children))
	}
	if root.Children[0].Name != "Turn-1" {
		t.Fatalf("Children[0].Name = %s", root.Children[0].Name)
	}
	if root.Children[1].Name != "Turn-2" {
		t.Fatalf("Children[1].Name = %s", root.Children[1].Name)
	}

	// Turn-1 应有 2 个子 Span
	turn1 := root.Children[0]
	if len(turn1.Children) != 2 {
		t.Fatalf("Turn-1 应有 2 个子 Span, 得到 %d", len(turn1.Children))
	}
	if turn1.Children[0].Name != "LLM.Action" {
		t.Fatalf("Turn-1.Children[0] = %s", turn1.Children[0].Name)
	}
	if turn1.Children[1].Name != "Tool.Execute" {
		t.Fatalf("Turn-1.Children[1] = %s", turn1.Children[1].Name)
	}

	// Turn-2 应有 1 个子 Span
	turn2 := root.Children[1]
	if len(turn2.Children) != 1 {
		t.Fatalf("Turn-2 应有 1 个子 Span, 得到 %d", len(turn2.Children))
	}

	// 验证 Attributes
	if turn1.Attributes["context_message_count"] != 2 {
		t.Fatalf("Turn-1 context_message_count = %v", turn1.Attributes["context_message_count"])
	}

	t.Log("  ✅ Span 树结构验证通过")
}
