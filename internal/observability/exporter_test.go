package observability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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
// OTelExporter 测试（使用 InMemoryExporter，无需真实 Jaeger）
// ============================================================

// newTestOTelExporter 创建一个使用 InMemoryExporter 的 OTelExporter，用于单元测试。
func newTestOTelExporter() (*OTelExporter, *tracetest.InMemoryExporter) {
	memExporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(memExporter), // 同步写入，方便测试
	)
	return &OTelExporter{
		tracerProvider: tp,
		tracer:         tp.Tracer("test/engine"),
	}, memExporter
}

// TestOTelExporter_Export_SpanTree 验证 Span 树正确转换为 OTel Span。
func TestOTelExporter_Export_SpanTree(t *testing.T) {
	exp, memExporter := newTestOTelExporter()

	root := buildTestSpanTree()
	err := exp.Export(context.Background(), root)
	if err != nil {
		t.Fatalf("Export 失败: %v", err)
	}

	spans := memExporter.GetSpans()
	// 6 个 Span: Agent.Run + Turn-1 + Turn-2 + LLM.Action + Tool.Execute + LLM.Action
	if len(spans) != 6 {
		t.Fatalf("期望 6 个 OTel Span, 实际 %d", len(spans))
	}

	// 验证根 Span 无 parent
	for _, sp := range spans {
		if sp.Name == "Agent.Run" {
			if sp.Parent.IsValid() {
				t.Error("Root Span 不应有 parent")
			}
		}
		if sp.Name == "Turn-1" || sp.Name == "Turn-2" {
			if !sp.Parent.IsValid() {
				t.Errorf("%s 应有 parent", sp.Name)
			}
		}
	}

	// 验证 Attributes 传递
	for _, sp := range spans {
		if sp.Name == "Agent.Run" {
			found := false
			for _, attr := range sp.Attributes {
				if string(attr.Key) == "SessionID" && attr.Value.AsString() == "test-session" {
					found = true
				}
			}
			if !found {
				t.Error("Agent.Run 应包含 SessionID=test-session 属性")
			}
		}
	}

	t.Logf("  ✅ OTelExporter 正确转换 %d 个 Span（含父子关系）", len(spans))
}

// TestOTelExporter_Export_NilRootSpan 验证 Export(nil) 返回错误而非 panic。
func TestOTelExporter_Export_NilRootSpan(t *testing.T) {
	exp, _ := newTestOTelExporter()

	err := exp.Export(context.Background(), nil)
	if err == nil {
		t.Fatal("Export(nil) 应返回非 nil 错误")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("错误信息应包含 'nil': %v", err)
	}
	t.Logf("  ✅ Export(nil) 正确返回错误: %v", err)
}

// TestOTelExporter_Export_ZeroEndTime 验证 EndTime 为零值时不产生 53 年 Duration。
func TestOTelExporter_Export_ZeroEndTime(t *testing.T) {
	exp, memExporter := newTestOTelExporter()

	zeroSpan := &Span{
		Name:       "ZeroEnd",
		StartTime:  time.Now(),
		EndTime:    time.Time{}, // 零值
		DurationMs: 0,
	}

	err := exp.Export(context.Background(), zeroSpan)
	if err != nil {
		t.Fatalf("Export 失败: %v", err)
	}

	spans := memExporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("期望 1 个 Span, 实际 %d", len(spans))
	}

	// EndTime 应该被修正为接近 StartTime（而非零值导致 53 年）
	duration := spans[0].EndTime.Sub(spans[0].StartTime)
	if duration > 1*time.Second {
		t.Errorf("Duration 应 < 1s（EndTime 零值已被修正），实际 %v", duration)
	}
	t.Logf("  ✅ EndTime 零值已修正，Duration = %v", duration)
}

// TestOTelExporter_Shutdown 验证 Shutdown 正确关闭 TracerProvider。
func TestOTelExporter_Shutdown(t *testing.T) {
	exp, _ := newTestOTelExporter()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := exp.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown 失败: %v", err)
	}
	t.Log("  ✅ OTelExporter Shutdown 成功")
}

// TestOTelExporter_TracerProvider 验证 TracerProvider() 访问器。
func TestOTelExporter_TracerProvider(t *testing.T) {
	exp, _ := newTestOTelExporter()

	tp := exp.TracerProvider()
	if tp == nil {
		t.Fatal("TracerProvider() 不应返回 nil")
	}
	t.Log("  ✅ TracerProvider() 访问器正常")
}

// ============================================================
// TraceProvider 测试
// ============================================================

// mockExporter 是一个记录调用次数的 mock Exporter。
type mockExporter struct {
	exportCount   int
	shutdownCount int
	exportedSpan  *Span
	exportErr     error // 可注入的 Export 错误
	shutdownErr   error // 可注入的 Shutdown 错误
}

func (m *mockExporter) Export(_ context.Context, rootSpan *Span) error {
	m.exportCount++
	m.exportedSpan = rootSpan
	return m.exportErr
}

func (m *mockExporter) Shutdown(_ context.Context) error {
	m.shutdownCount++
	return m.shutdownErr
}

// TestTraceProvider_Export 验证 TraceProvider 并行调用所有 Exporter。
func TestTraceProvider_Export(t *testing.T) {
	mock1 := &mockExporter{}
	mock2 := &mockExporter{}
	tp := NewTraceProvider(mock1, mock2)

	root := buildTestSpanTree()
	if err := tp.Export(context.Background(), root); err != nil {
		t.Fatalf("Export 不应返回错误: %v", err)
	}

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

	if err := tp.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown 不应返回错误: %v", err)
	}

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
	_ = tp.Export(context.Background(), root)   // 应不 panic
	_ = tp.Shutdown(context.Background())       // 应不 panic

	t.Log("  ✅ 空 TraceProvider 安全运行")
}

// TestTraceProvider_Export_PartialFailure 验证部分 Exporter 失败时，其他 Exporter 仍被调用。
func TestTraceProvider_Export_PartialFailure(t *testing.T) {
	good := &mockExporter{}
	bad := &mockExporter{exportErr: context.DeadlineExceeded}
	tp := NewTraceProvider(bad, good)

	root := buildTestSpanTree()
	err := tp.Export(context.Background(), root)

	// 应返回聚合错误
	if err == nil {
		t.Fatal("部分 Exporter 失败时应返回非 nil 错误")
	}
	if !strings.Contains(err.Error(), "deadline exceeded") {
		t.Errorf("错误信息应包含 'deadline exceeded': %v", err)
	}

	// 好的 Exporter 仍应被调用
	if good.exportCount != 1 {
		t.Errorf("好的 Exporter 应被调用 1 次, 实际 %d 次", good.exportCount)
	}

	t.Log("  ✅ 部分失败时其他 Exporter 仍正常执行")
}

// TestTraceProvider_Shutdown_PartialFailure 验证部分 Exporter Shutdown 失败时聚合错误。
func TestTraceProvider_Shutdown_PartialFailure(t *testing.T) {
	good := &mockExporter{}
	bad := &mockExporter{shutdownErr: context.DeadlineExceeded}
	tp := NewTraceProvider(bad, good)

	err := tp.Shutdown(context.Background())
	if err == nil {
		t.Fatal("部分 Shutdown 失败时应返回非 nil 错误")
	}

	// 好的 Exporter 仍应被调用
	if good.shutdownCount != 1 {
		t.Errorf("好的 Exporter Shutdown 应被调用 1 次, 实际 %d 次", good.shutdownCount)
	}

	t.Log("  ✅ Shutdown 部分失败时聚合错误")
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
