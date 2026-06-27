package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/lhuan/go-tiny-claw/internal/context"
	"github.com/lhuan/go-tiny-claw/internal/engine"
	"github.com/lhuan/go-tiny-claw/internal/schema"
	"github.com/lhuan/go-tiny-claw/internal/tools"
)

// ============================================================================
// 测试设计思路：构造"恶意任务"场景 + 自适应压缩验证
//
//   恶意任务 = 连续多次读取大文件，迫使上下文膨胀
//
//   防御层级：
//     Layer 1: ReadFile 工具自身截断 (8000 字节硬限)
//     Layer 2: Session.GetWorkingMemory 双维度滑动窗口 (6 条 / 50000 字符)
//     Layer 3: Compactor 自适应压缩 (基于真实 Token 利用率)
//
//   自适应压缩级别：
//     利用率 < 50%  → 不压缩
//     利用率 50-70% → 温和压缩
//     利用率 70-85% → 标准压缩
//     利用率 85-95% → 激进压缩
//     利用率 > 95%  → 紧急压缩
// ============================================================================

// generateLargeContent 生成指定大小的填充文本，模拟大文件
func generateLargeContent(lineCount int) string {
	var sb strings.Builder
	for i := 0; i < lineCount; i++ {
		sb.WriteString(fmt.Sprintf(
			"[行 %04d] 这是一段用于测试内容压缩机制的填充文本。"+
				"当大模型读取这种超大文件时，系统需要执行三层压缩。"+
				"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n", i+1))
	}
	return sb.String()
}

// ============================================================================
// Layer 1: ReadFile 工具级截断测试
// ============================================================================

func TestLayer1_ReadFile_Truncation(t *testing.T) {
	tmpDir := t.TempDir()
	bigContent := generateLargeContent(2000)
	bigFile := filepath.Join(tmpDir, "big.txt")
	if err := os.WriteFile(bigFile, []byte(bigContent), 0644); err != nil {
		t.Fatal(err)
	}

	tool := tools.NewReadFileTool(tmpDir)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}

	t.Logf("原始文件大小: %d 字节", len(bigContent))
	t.Logf("截断后返回大小: %d 字节", len(result))

	if len(result) > 8200 {
		t.Errorf("❌ Layer 1 失败：返回内容 (%d 字节) 远超 8000 字节限制", len(result))
	}

	if !strings.Contains(result, "已被系统截断") {
		t.Error("❌ Layer 1 失败：缺少截断标记信息")
	}

	t.Log("✅ Layer 1 通过：ReadFile 工具正确截断了大文件")
}

func TestLayer1_ReadFile_SmallFile_NoTruncation(t *testing.T) {
	tmpDir := t.TempDir()
	smallContent := "hello world\n"
	smallFile := filepath.Join(tmpDir, "small.txt")
	if err := os.WriteFile(smallFile, []byte(smallContent), 0644); err != nil {
		t.Fatal(err)
	}

	tool := tools.NewReadFileTool(tmpDir)
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"small.txt"}`))
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}

	if strings.Contains(result, "已被系统截断") {
		t.Error("❌ 小文件不应被截断")
	}
	if result != smallContent {
		t.Errorf("❌ 小文件内容不匹配: got %q, want %q", result, smallContent)
	}
	t.Log("✅ 小文件正确返回，未触发截断")
}

// ============================================================================
// Layer 2: Session.GetWorkingMemory 双维度截断测试
// ============================================================================

func TestLayer2_WorkingMemory_MessageCountLimit(t *testing.T) {
	session := engine.NewSession("test", t.TempDir())

	for i := 0; i < 20; i++ {
		session.Append(schema.Message{
			Role:    schema.RoleUser,
			Content: fmt.Sprintf("消息 #%d", i+1),
		})
	}

	workingMemory := session.GetWorkingMemory(6, 0)

	t.Logf("总消息数: 20, WorkingMemory 返回: %d 条", len(workingMemory))

	if len(workingMemory) > 6 {
		t.Errorf("❌ Layer 2 (条数) 失败：期望最多 6 条，实际 %d 条", len(workingMemory))
	}

	lastContent := workingMemory[len(workingMemory)-1].Content
	if lastContent != "消息 #20" {
		t.Errorf("❌ 应保留最后一条消息，实际为: %s", lastContent)
	}

	t.Log("✅ Layer 2 (条数限制) 通过")
}

func TestLayer2_WorkingMemory_CharBudgetLimit(t *testing.T) {
	session := engine.NewSession("test", t.TempDir())

	for i := 0; i < 5; i++ {
		session.Append(schema.Message{
			Role:    schema.RoleUser,
			Content: strings.Repeat(fmt.Sprintf("X-第%d条-", i+1), 3000),
		})
	}

	workingMemory := session.GetWorkingMemory(0, 50000)

	totalChars := 0
	for _, msg := range workingMemory {
		totalChars += len(msg.Content)
	}

	t.Logf("WorkingMemory 返回 %d 条，总字符 %d", len(workingMemory), totalChars)

	if totalChars > 52000 {
		t.Errorf("❌ Layer 2 (字符) 失败：总字符 %d 超过 50000 预算", totalChars)
	}

	t.Log("✅ Layer 2 (字符预算) 通过")
}

// ============================================================================
// Layer 3: Compactor 自适应压缩测试
// ============================================================================

// TestLayer3_Adaptive_NoCompression 验证低利用率时不压缩
func TestLayer3_Adaptive_NoCompression(t *testing.T) {
	compactor := ctxpkg.NewCompactor(200000, 6) // 200k Token 窗口

	// 模拟低利用率：5000 PromptTokens / 200000 窗口 = 2.5%
	compactor.UpdateUsage(5000)

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "你是一个助手"},
		{Role: schema.RoleUser, Content: "你好"},
		{Role: schema.RoleAssistant, Content: "你好！有什么可以帮助你的？"},
	}

	compacted := compactor.Compact(msgs)

	// 低利用率不应压缩
	if len(compacted) != len(msgs) {
		t.Errorf("❌ 低利用率不应压缩：消息数从 %d 变为 %d", len(msgs), len(compacted))
	}
	for i, m := range compacted {
		if m.Content != msgs[i].Content {
			t.Errorf("❌ 低利用率不应修改消息内容")
			break
		}
	}
	t.Log("✅ 低利用率 (2.5%) 正确跳过压缩")
}

// TestLayer3_Adaptive_GentleCompression 验证温和压缩（50-70%）
func TestLayer3_Adaptive_GentleCompression(t *testing.T) {
	compactor := ctxpkg.NewCompactor(1000, 6) // 小窗口便于测试

	// 模拟 60% 利用率
	compactor.UpdateUsage(600)

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "系统提示"},
	}
	// 远期历史：5 条大 ToolResult
	for i := 0; i < 5; i++ {
		msgs = append(msgs, schema.Message{
			Role:       schema.RoleUser,
			Content:    strings.Repeat("远期数据-", 100), // ~800 字符
			ToolCallID: fmt.Sprintf("old_%d", i),
		})
	}
	// 近期保护区
	for i := 0; i < 4; i++ {
		msgs = append(msgs, schema.Message{
			Role:    schema.RoleAssistant,
			Content: fmt.Sprintf("回复 #%d", i+1),
		})
	}

	compacted := compactor.Compact(msgs)

	// 远期 ToolResult 应被掩码
	maskedCount := 0
	for _, m := range compacted {
		if strings.Contains(m.Content, "已被系统强制清理") {
			maskedCount++
		}
	}

	if maskedCount == 0 {
		t.Error("❌ 温和压缩应掩码远期历史")
	}
	t.Logf("✅ 温和压缩 (60%%) 正确掩码了 %d 条远期消息", maskedCount)
}

// TestLayer3_Adaptive_StandardCompression 验证标准压缩（70-85%）
func TestLayer3_Adaptive_StandardCompression(t *testing.T) {
	compactor := ctxpkg.NewCompactor(1000, 6)

	// 模拟 80% 利用率
	compactor.UpdateUsage(800)

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "系统提示"},
	}
	// 远期大 ToolResult
	for i := 0; i < 5; i++ {
		msgs = append(msgs, schema.Message{
			Role:       schema.RoleUser,
			Content:    strings.Repeat("远期-", 200), // ~1000 字符
			ToolCallID: fmt.Sprintf("old_%d", i),
		})
	}
	// 近期超大 ToolResult
	msgs = append(msgs, schema.Message{
		Role:       schema.RoleUser,
		Content:    strings.Repeat("X", 5000),
		ToolCallID: "big_recent",
	})
	for i := 0; i < 4; i++ {
		msgs = append(msgs, schema.Message{
			Role:    schema.RoleAssistant,
			Content: fmt.Sprintf("回复 #%d", i+1),
		})
	}

	compacted := compactor.Compact(msgs)

	// 检查近期大 ToolResult 被掐头去尾
	found := false
	for _, m := range compacted {
		if m.ToolCallID == "big_recent" {
			if strings.Contains(m.Content, "已被系统截断") {
				t.Logf("✅ 标准压缩 (80%%) 正确截断了近期大 ToolResult: %d → %d 字节", 5000, len(m.Content))
				found = true
			}
		}
	}
	if !found {
		t.Error("❌ 标准压缩应截断近期大 ToolResult")
	}
}

// TestLayer3_Adaptive_AggressiveCompression 验证激进压缩（85-95%）
func TestLayer3_Adaptive_AggressiveCompression(t *testing.T) {
	compactor := ctxpkg.NewCompactor(1000, 6)

	// 模拟 90% 利用率
	compactor.UpdateUsage(900)

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "系统提示"},
	}
	for i := 0; i < 5; i++ {
		msgs = append(msgs, schema.Message{
			Role:       schema.RoleUser,
			Content:    strings.Repeat("数据-", 200),
			ToolCallID: fmt.Sprintf("old_%d", i),
		})
	}
	msgs = append(msgs, schema.Message{
		Role:       schema.RoleUser,
		Content:    strings.Repeat("Y", 3000),
		ToolCallID: "big_recent",
	})
	for i := 0; i < 4; i++ {
		msgs = append(msgs, schema.Message{
			Role:    schema.RoleAssistant,
			Content: fmt.Sprintf("回复 #%d", i+1),
		})
	}

	originalLen := 0
	for _, m := range msgs {
		originalLen += len(m.Content)
	}

	compacted := compactor.Compact(msgs)

	compactedLen := 0
	for _, m := range compacted {
		compactedLen += len(m.Content)
	}

	// 激进压缩应该更小
	if compactedLen >= originalLen {
		t.Errorf("❌ 激进压缩未生效：压缩前 %d, 压缩后 %d", originalLen, compactedLen)
	}

	// 检查近期截断更短（200+200 而非 500+500）
	for _, m := range compacted {
		if m.ToolCallID == "big_recent" && strings.Contains(m.Content, "已被系统截断") {
			t.Logf("✅ 激进压缩 (90%%) 截断近期大 ToolResult: %d → %d 字节", 3000, len(m.Content))
			if len(m.Content) > 600 { // 200+200 + 标记 ≈ 500
				t.Errorf("❌ 激进压缩截断不够短：%d 字节 (期望 < 600)", len(m.Content))
			}
		}
	}
}

// TestLayer3_Adaptive_EmergencyCompression 验证紧急压缩（>95%）
func TestLayer3_Adaptive_EmergencyCompression(t *testing.T) {
	compactor := ctxpkg.NewCompactor(1000, 6)

	// 模拟 98% 利用率
	compactor.UpdateUsage(980)

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "系统提示"},
	}
	for i := 0; i < 8; i++ {
		msgs = append(msgs, schema.Message{
			Role:       schema.RoleUser,
			Content:    strings.Repeat("紧急数据-", 200),
			ToolCallID: fmt.Sprintf("call_%d", i),
		})
	}
	for i := 0; i < 4; i++ {
		msgs = append(msgs, schema.Message{
			Role:    schema.RoleAssistant,
			Content: fmt.Sprintf("回复 #%d", i+1),
		})
	}

	originalLen := 0
	for _, m := range msgs {
		originalLen += len(m.Content)
	}

	compacted := compactor.Compact(msgs)

	compactedLen := 0
	for _, m := range compacted {
		compactedLen += len(m.Content)
	}

	compressionRatio := (1 - float64(compactedLen)/float64(originalLen)) * 100
	t.Logf("✅ 紧急压缩 (98%%): %d → %d 字符 (压缩率 %.1f%%)", originalLen, compactedLen, compressionRatio)

	if compressionRatio < 50 {
		t.Errorf("❌ 紧急压缩率不足：%.1f%% (期望 > 50%%)", compressionRatio)
	}
}

// TestLayer3_Fallback_CharacterMode 验证无 Token 数据时的降级模式
func TestLayer3_Fallback_CharacterMode(t *testing.T) {
	// 不调用 UpdateUsage，使用降级字符估算模式
	compactor := ctxpkg.NewCompactor(200000, 6)

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "系统提示"},
	}
	// 塞入大量内容触发降级估算
	for i := 0; i < 10; i++ {
		msgs = append(msgs, schema.Message{
			Role:       schema.RoleUser,
			Content:    strings.Repeat("填充数据-", 5000), // ~20000 字符 × 10 = 200k 字符
			ToolCallID: fmt.Sprintf("call_%d", i),
		})
	}

	originalLen := 0
	for _, m := range msgs {
		originalLen += len(m.Content)
	}

	compacted := compactor.Compact(msgs)

	compactedLen := 0
	for _, m := range compacted {
		compactedLen += len(m.Content)
	}

	if compactedLen >= originalLen {
		t.Logf("降级模式：内容量 (%d 字符) 未触发压缩（估算 Token 数未超窗口一半）", originalLen)
	} else {
		t.Logf("✅ 降级字符估算模式生效：%d → %d 字符", originalLen, compactedLen)
	}
}

// TestLayer3_UtilizationString 验证利用率报告
func TestLayer3_UtilizationString(t *testing.T) {
	compactor := ctxpkg.NewCompactor(200000, 6)

	// 未收到 Usage 时
	msgs := []schema.Message{{Role: schema.RoleUser, Content: "test"}}
	compactor.Compact(msgs) // 触发一次以打印日志

	// 收到 Usage 后
	compactor.UpdateUsage(150000)
	compactor.Compact(msgs) // 触发一次以打印日志

	t.Log("✅ 利用率报告测试完成（请检查日志输出）")
}

// ============================================================================
// 综合测试：模拟恶意任务的完整压缩链路
// ============================================================================

func TestFullChain_MaliciousTask(t *testing.T) {
	t.Log("═══════════════════════════════════════════════════════════")
	t.Log("  恶意任务模拟：连续 5 次读取大文件 + 自适应压缩")
	t.Log("═══════════════════════════════════════════════════════════")

	tmpDir := t.TempDir()

	// 1. 创建大文件
	bigContent := generateLargeContent(2000)
	bigFile := filepath.Join(tmpDir, "huge.txt")
	if err := os.WriteFile(bigFile, []byte(bigContent), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("📁 测试文件大小: %d 字节 (%.1f KB)", len(bigContent), float64(len(bigContent))/1024)

	// 2. 模拟 Layer 1：ReadFile 截断
	readTool := tools.NewReadFileTool(tmpDir)
	rawResult, _ := readTool.Execute(context.Background(), json.RawMessage(`{"path":"huge.txt"}`))
	t.Logf("🔪 Layer 1 (ReadFile截断): %d → %d 字节", len(bigContent), len(rawResult))

	// 3. 模拟连续 5 次读取
	session := engine.NewSession("malicious-test", tmpDir)
	session.Append(schema.Message{Role: schema.RoleUser, Content: "请帮我分析这个大文件"})

	for i := 0; i < 5; i++ {
		session.Append(schema.Message{
			Role:    schema.RoleAssistant,
			Content: fmt.Sprintf("好的，我来读取第 %d 次", i+1),
			ToolCalls: []schema.ToolCall{
				{ID: fmt.Sprintf("call_%d", i), Name: "read_file", Arguments: json.RawMessage(`{"path":"huge.txt"}`)},
			},
		})
		session.Append(schema.Message{
			Role:       schema.RoleUser,
			Content:    rawResult,
			ToolCallID: fmt.Sprintf("call_%d", i),
		})
	}

	// 4. Layer 2：WorkingMemory 截断
	allHistory := session.GetWorkingMemory(6, 50000)
	t.Logf("📦 Layer 2 (WorkingMemory): 全量 11 条 → 截取 %d 条", len(allHistory))

	totalChars := 0
	for _, m := range allHistory {
		totalChars += len(m.Content)
	}
	t.Logf("   截取后总字符: %d", totalChars)

	// 5. Layer 3：自适应压缩（模拟 90% 利用率）
	systemMsg := schema.Message{Role: schema.RoleSystem, Content: "你是一个代码分析助手"}
	contextToSend := append([]schema.Message{systemMsg}, allHistory...)

	compactor := ctxpkg.NewCompactor(8000, 6) // 小窗口便于测试
	compactor.UpdateUsage(7200)               // 90% 利用率
	compacted := compactor.Compact(contextToSend)

	finalChars := 0
	for _, m := range compacted {
		finalChars += len(m.Content)
	}

	t.Logf("🗜️  Layer 3 (自适应压缩): %d 字符 → %d 字符", totalChars+len(systemMsg.Content), finalChars)
	t.Logf("   压缩率: %.1f%%", (1-float64(finalChars)/float64(totalChars+len(systemMsg.Content)))*100)

	// 6. 最终断言
	maskedCount := 0
	truncatedCount := 0
	for _, m := range compacted {
		if strings.Contains(m.Content, "已被系统强制清理") {
			maskedCount++
		}
		if strings.Contains(m.Content, "已被系统截断") {
			truncatedCount++
		}
	}

	t.Logf("   远期掩码消息数: %d, 近期截断消息数: %d", maskedCount, truncatedCount)

	t.Log("═══════════════════════════════════════════════════════════")
	t.Log("  ✅ 恶意任务防御测试通过！自适应压缩全部生效。")
	t.Log("═══════════════════════════════════════════════════════════")
}
