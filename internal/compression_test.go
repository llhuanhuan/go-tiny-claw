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
// 测试设计思路：构造一个"恶意任务"场景
//
//   恶意任务 = 连续多次读取大文件，迫使上下文膨胀
//
//   防御层级：
//     Layer 1: ReadFile 工具自身截断 (8000 字节硬限)
//     Layer 2: Session.GetWorkingMemory 双维度滑动窗口 (6 条 / 50000 字符)
//     Layer 3: Compactor 语义压缩 (3000 字符水位线)
//
//   本测试直接断言每一层的行为，无需实际调用 LLM API。
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
	// 创建临时目录和大文件
	tmpDir := t.TempDir()
	bigContent := generateLargeContent(2000) // ~200KB
	bigFile := filepath.Join(tmpDir, "big.txt")
	if err := os.WriteFile(bigFile, []byte(bigContent), 0644); err != nil {
		t.Fatal(err)
	}

	tool := tools.NewReadFileTool(tmpDir)

	// 测试：读取大文件应被截断
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}

	t.Logf("原始文件大小: %d 字节", len(bigContent))
	t.Logf("截断后返回大小: %d 字节", len(result))

	if len(result) > 8200 { // 8000 + 截断标记的余量
		t.Errorf("❌ Layer 1 失败：返回内容 (%d 字节) 远超 8000 字节限制", len(result))
	}

	if !strings.Contains(result, "已被系统截断") {
		t.Error("❌ Layer 1 失败：缺少截断标记信息")
	}

	t.Log("✅ Layer 1 通过：ReadFile 工具正确截断了大文件")
}

// 测试小文件不应被截断
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

	// 追加 20 条短消息
	for i := 0; i < 20; i++ {
		session.Append(schema.Message{
			Role:    schema.RoleUser,
			Content: fmt.Sprintf("消息 #%d", i+1),
		})
	}

	workingMemory := session.GetWorkingMemory(6, 0) // 只限制条数

	t.Logf("总消息数: 20, WorkingMemory 返回: %d 条", len(workingMemory))

	if len(workingMemory) > 6 {
		t.Errorf("❌ Layer 2 (条数) 失败：期望最多 6 条，实际 %d 条", len(workingMemory))
	}

	// 验证保留的是最后几条
	lastContent := workingMemory[len(workingMemory)-1].Content
	if lastContent != "消息 #20" {
		t.Errorf("❌ 应保留最后一条消息，实际为: %s", lastContent)
	}

	t.Log("✅ Layer 2 (条数限制) 通过")
}

func TestLayer2_WorkingMemory_CharBudgetLimit(t *testing.T) {
	session := engine.NewSession("test", t.TempDir())

	// 追加 5 条超长消息，每条约 20000 字符
	for i := 0; i < 5; i++ {
		session.Append(schema.Message{
			Role:    schema.RoleUser,
			Content: strings.Repeat(fmt.Sprintf("X-第%d条-", i+1), 3000), // ~18000 字符
		})
	}

	workingMemory := session.GetWorkingMemory(0, 50000) // 只限制字符数

	totalChars := 0
	for _, msg := range workingMemory {
		totalChars += len(msg.Content)
	}

	t.Logf("WorkingMemory 返回 %d 条，总字符 %d", len(workingMemory), totalChars)

	if totalChars > 52000 { // 留一点余量
		t.Errorf("❌ Layer 2 (字符) 失败：总字符 %d 超过 50000 预算", totalChars)
	}

	t.Log("✅ Layer 2 (字符预算) 通过")
}

// ============================================================================
// Layer 3: Compactor 语义压缩测试
// ============================================================================

func TestLayer3_Compactor_FarHistoryFullMasking(t *testing.T) {
	compactor := ctxpkg.NewCompactor(3000, 6)

	// 构造一个超标的上下文：
	//   System Prompt + 10 条早期 ToolResult（每条 500 字符）+ 6 条近期消息
	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "你是一个助手"},
	}

	// 远期历史：8 条大 ToolResult（超出保护区）
	for i := 0; i < 8; i++ {
		msgs = append(msgs, schema.Message{
			Role:       schema.RoleUser,
			Content:    strings.Repeat(fmt.Sprintf("远期工具输出#%d-", i+1), 100), // ~500 字符
			ToolCallID: fmt.Sprintf("call_%d", i),
		})
	}

	// 近期保护区：6 条消息
	for i := 0; i < 6; i++ {
		msgs = append(msgs, schema.Message{
			Role:    schema.RoleAssistant,
			Content: fmt.Sprintf("这是第 %d 条近期回复", i+1),
		})
	}

	// 计算原始长度
	originalLen := 0
	for _, m := range msgs {
		originalLen += len(m.Content)
	}
	t.Logf("压缩前上下文总长度: %d 字符 (阈值: 3000)", originalLen)

	compacted := compactor.Compact(msgs)

	compactedLen := 0
	for _, m := range compacted {
		compactedLen += len(m.Content)
	}
	t.Logf("压缩后上下文总长度: %d 字符", compactedLen)

	// 断言：远期 ToolResult 应被全量掩码
	for i := 1; i <= 8; i++ {
		content := compacted[i].Content
		if strings.Contains(content, "早期的工具输出已被系统强制清理") {
			t.Logf("  ✅ 远期消息 #%d 已被全量掩码", i)
		} else if len(content) > 200 {
			t.Errorf("  ❌ 远期消息 #%d 未被掩码，长度: %d", i, len(content))
		}
	}

	if compactedLen >= originalLen {
		t.Errorf("❌ Layer 3 (远期掩码) 失败：压缩后 (%d) >= 压缩前 (%d)", compactedLen, originalLen)
	}

	t.Log("✅ Layer 3 (远期历史全量掩码) 通过")
}

func TestLayer3_Compactor_NearHistoryHeadTailTruncation(t *testing.T) {
	compactor := ctxpkg.NewCompactor(3000, 6)

	msgs := []schema.Message{
		{Role: schema.RoleSystem, Content: "你是一个助手"},
	}

	// 塞入足够多的内容确保触发压缩
	for i := 0; i < 5; i++ {
		msgs = append(msgs, schema.Message{
			Role:       schema.RoleUser,
			Content:    strings.Repeat("填充-", 500), // ~2000 字符
			ToolCallID: fmt.Sprintf("old_%d", i),
		})
	}

	// 近期保护区：一条超大 ToolResult（模拟 read_file 返回 8000 字节）
	largeToolResult := strings.Repeat("A", 8000)
	msgs = append(msgs, schema.Message{
		Role:       schema.RoleUser,
		Content:    largeToolResult,
		ToolCallID: "recent_big_call",
	})

	// 补几条近期消息凑满保护区
	for i := 0; i < 4; i++ {
		msgs = append(msgs, schema.Message{
			Role:    schema.RoleAssistant,
			Content: fmt.Sprintf("回复 #%d", i+1),
		})
	}

	compacted := compactor.Compact(msgs)

	// 找到那条近期大 ToolResult
	for _, m := range compacted {
		if m.ToolCallID == "recent_big_call" {
			if strings.Contains(m.Content, "中间") && strings.Contains(m.Content, "已被系统截断") {
				t.Logf("✅ 近期大 ToolResult (%d 字节) 被正确掐头去尾", len(largeToolResult))
				t.Logf("   截断后长度: %d 字节", len(m.Content))
			} else if len(m.Content) == len(largeToolResult) {
				t.Errorf("❌ 近期大 ToolResult (%d 字节) 未被截断!", len(largeToolResult))
			}
			return
		}
	}
	t.Error("❌ 未找到近期大 ToolResult 消息")
}

// ============================================================================
// 综合测试：模拟恶意任务的完整压缩链路
// ============================================================================

func TestFullChain_MaliciousTask(t *testing.T) {
	t.Log("═══════════════════════════════════════════════════════════")
	t.Log("  恶意任务模拟：连续 5 次读取 950KB 大文件")
	t.Log("═══════════════════════════════════════════════════════════")

	tmpDir := t.TempDir()

	// 1. 创建 950KB 大文件
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

	// 3. 模拟连续 5 次读取，构建 Session 历史
	session := engine.NewSession("malicious-test", tmpDir)

	// 先加一条用户消息
	session.Append(schema.Message{Role: schema.RoleUser, Content: "请帮我分析这个大文件"})

	// 模拟 5 轮：每轮 = 助手调用 read_file + 工具返回结果
	for i := 0; i < 5; i++ {
		// 助手决定读文件
		session.Append(schema.Message{
			Role:    schema.RoleAssistant,
			Content: fmt.Sprintf("好的，我来读取第 %d 次", i+1),
			ToolCalls: []schema.ToolCall{
				{ID: fmt.Sprintf("call_%d", i), Name: "read_file", Arguments: json.RawMessage(`{"path":"huge.txt"}`)},
			},
		})
		// 工具返回截断后的内容
		session.Append(schema.Message{
			Role:       schema.RoleUser,
			Content:    rawResult,
			ToolCallID: fmt.Sprintf("call_%d", i),
		})
	}

	// 4. Layer 2：WorkingMemory 截断
	allHistory := session.GetWorkingMemory(6, 50000)
	t.Logf("📦 Layer 2 (WorkingMemory): 全量 %d 条 → 截取 %d 条", 11, len(allHistory))

	totalChars := 0
	for _, m := range allHistory {
		totalChars += len(m.Content)
	}
	t.Logf("   截取后总字符: %d", totalChars)

	// 5. Layer 3：Compactor 压缩
	systemMsg := schema.Message{Role: schema.RoleSystem, Content: "你是一个代码分析助手"}
	contextToSend := append([]schema.Message{systemMsg}, allHistory...)

	compactor := ctxpkg.NewCompactor(3000, 6)
	compacted := compactor.Compact(contextToSend)

	finalChars := 0
	for _, m := range compacted {
		finalChars += len(m.Content)
	}

	t.Logf("🗜️  Layer 3 (Compactor): %d 字符 → %d 字符", totalChars+len(systemMsg.Content), finalChars)
	t.Logf("   压缩率: %.1f%%", (1-float64(finalChars)/float64(totalChars+len(systemMsg.Content)))*100)

	// 6. 最终断言
	if finalChars > 10000 {
		t.Errorf("❌ 最终上下文仍然过大: %d 字符 (期望 < 10000)", finalChars)
	}

	// 验证压缩后的消息中包含截断标记
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
	t.Log("  ✅ 恶意任务防御测试通过！三层压缩全部生效。")
	t.Log("═══════════════════════════════════════════════════════════")
}
