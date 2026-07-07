package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// Plan Mode 测试套件
//
// 验证 PromptComposer 的计划模式功能：
//   1. 默认关闭 — Build() 不注入计划模式指令
//   2. 构造函数传参开启 — NewPromptComposer(dir, true)
//   3. SetPlanMode(true) — Build() 注入完整的计划模式指令
//   4. SetPlanMode(false) — 可动态关闭
//   5. 计划模式指令内容完整性校验
//   6. 与 AGENTS.md 共存
// ============================================================================

// TestPlanMode_DefaultDisabled 验证默认状态下不注入计划模式指令。
func TestPlanMode_DefaultDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	composer := NewPromptComposer(tmpDir)

	msg := composer.Build()

	if strings.Contains(msg.Content, "Plan Mode: ON") {
		t.Fatal("❌ 默认状态下不应包含计划模式指令，但发现了 'Plan Mode: ON'")
	}
	if strings.Contains(msg.Content, "PLAN.md") {
		t.Fatal("❌ 默认状态下不应包含 PLAN.md 引用")
	}
	if strings.Contains(msg.Content, "TODO.md") {
		t.Fatal("❌ 默认状态下不应包含 TODO.md 引用")
	}

	t.Log("✅ 默认状态 (planMode=false)：Build() 未注入计划模式指令")
}

// TestPlanMode_ConstructorWithPlanMode 验证构造函数传参开启计划模式。
func TestPlanMode_ConstructorWithPlanMode(t *testing.T) {
	tmpDir := t.TempDir()

	// 传 true
	composer := NewPromptComposer(tmpDir, true)
	msg := composer.Build()
	if !strings.Contains(msg.Content, "Plan Mode: ON") {
		t.Fatal("❌ NewPromptComposer(dir, true) 应开启计划模式")
	}

	// 传 false（显式）
	composer2 := NewPromptComposer(tmpDir, false)
	msg2 := composer2.Build()
	if strings.Contains(msg2.Content, "Plan Mode: ON") {
		t.Fatal("❌ NewPromptComposer(dir, false) 不应开启计划模式")
	}

	// 不传（向后兼容）
	composer3 := NewPromptComposer(tmpDir)
	msg3 := composer3.Build()
	if strings.Contains(msg3.Content, "Plan Mode: ON") {
		t.Fatal("❌ NewPromptComposer(dir) 不应开启计划模式（向后兼容）")
	}

	t.Log("✅ 构造函数传参：NewPromptComposer(dir, true/false/不传) 行为正确")
}

// TestPlanMode_SetPlanModeTrue 验证开启计划模式后注入完整指令。
func TestPlanMode_SetPlanModeTrue(t *testing.T) {
	tmpDir := t.TempDir()
	composer := NewPromptComposer(tmpDir)

	composer.SetPlanMode(true)
	msg := composer.Build()

	// 关键指令片段校验
	requiredFragments := []struct {
		name    string
		feature string
	}{
		{"Plan Mode 标记", "Plan Mode: ON"},
		{"最高优先级", "最高优先级"},
		{"强制环境嗅探", "STEP 1"},
		{"强制前置步骤", "强制前置步骤"},
		{"严重违规", "严重违规"},
		{"PLAN.md 引用", "PLAN.md"},
		{"TODO.md 引用", "TODO.md"},
		{"分支 A（全新任务）", "分支 A"},
		{"分支 B（断点续传）", "分支 B"},
		{"单步执行", "STEP 2"},
		{"实时打勾", "- [x]"},
		{"未完成标记", "- [ ]"},
		{"迷失自救", "STEP 3"},
		{"write_file 指令", "write_file"},
		{"read_file 指令", "read_file"},
		{"edit_file 指令", "edit_file"},
	}

	for _, frag := range requiredFragments {
		if !strings.Contains(msg.Content, frag.feature) {
			t.Errorf("❌ 计划模式缺少关键指令 [%s]: 未找到 %q", frag.name, frag.feature)
		}
	}

	t.Log("✅ 计划模式 (planMode=true)：Build() 正确注入了全部关键指令")
}

// TestPlanMode_SetPlanModeToggle 验证动态开关切换。
func TestPlanMode_SetPlanModeToggle(t *testing.T) {
	tmpDir := t.TempDir()
	composer := NewPromptComposer(tmpDir)

	// 开启
	composer.SetPlanMode(true)
	msg1 := composer.Build()
	if !strings.Contains(msg1.Content, "Plan Mode: ON") {
		t.Fatal("❌ SetPlanMode(true) 后 Build() 应包含计划模式指令")
	}

	// 关闭
	composer.SetPlanMode(false)
	msg2 := composer.Build()
	if strings.Contains(msg2.Content, "Plan Mode: ON") {
		t.Fatal("❌ SetPlanMode(false) 后 Build() 不应包含计划模式指令")
	}

	// 再次开启
	composer.SetPlanMode(true)
	msg3 := composer.Build()
	if !strings.Contains(msg3.Content, "Plan Mode: ON") {
		t.Fatal("❌ 再次 SetPlanMode(true) 后 Build() 应包含计划模式指令")
	}

	t.Log("✅ 动态开关切换：planMode 可正确切换 true → false → true")
}

// TestPlanMode_CoreIdentityPreserved 验证计划模式不影响核心身份指令。
func TestPlanMode_CoreIdentityPreserved(t *testing.T) {
	tmpDir := t.TempDir()
	composer := NewPromptComposer(tmpDir)
	composer.SetPlanMode(true)

	msg := composer.Build()

	// 核心身份必须保留
	coreFragments := []string{
		"核心身份",
		"ai-tiny",
		"核心纪律",
		"write_file",
		"中文回复",
	}

	for _, frag := range coreFragments {
		if !strings.Contains(msg.Content, frag) {
			t.Errorf("❌ 计划模式下丢失了核心指令: 未找到 %q", frag)
		}
	}

	t.Log("✅ 计划模式下核心身份与纪律指令完整保留")
}

// TestPlanMode_WithAgentsMD 验证计划模式与 AGENTS.md 共存。
func TestPlanMode_WithAgentsMD(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建 AGENTS.md
	agentsContent := "# 项目规范\n请使用 Go 1.21+ 编写代码。"
	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(agentsContent), 0644); err != nil {
		t.Fatal(err)
	}

	composer := NewPromptComposer(tmpDir)
	composer.SetPlanMode(true)

	msg := composer.Build()

	// 计划模式指令存在
	if !strings.Contains(msg.Content, "Plan Mode: ON") {
		t.Fatal("❌ 计划模式指令丢失")
	}

	// AGENTS.md 内容也存在
	if !strings.Contains(msg.Content, "项目规范") {
		t.Fatal("❌ AGENTS.md 内容丢失")
	}
	if !strings.Contains(msg.Content, "Go 1.21+") {
		t.Fatal("❌ AGENTS.md 具体内容丢失")
	}

	t.Log("✅ 计划模式与 AGENTS.md 正确共存")
}

// TestPlanMode_WithoutAgentsMD 验证无 AGENTS.md 时计划模式正常工作。
func TestPlanMode_WithoutAgentsMD(t *testing.T) {
	tmpDir := t.TempDir()
	// 不创建 AGENTS.md

	composer := NewPromptComposer(tmpDir)
	composer.SetPlanMode(true)

	msg := composer.Build()

	if !strings.Contains(msg.Content, "Plan Mode: ON") {
		t.Fatal("❌ 计划模式指令丢失")
	}

	// 不应包含 AGENTS.md 相关内容
	if strings.Contains(msg.Content, "项目专属指南") {
		t.Fatal("❌ 无 AGENTS.md 时不应出现项目专属指南段落")
	}

	t.Log("✅ 无 AGENTS.md 时计划模式正常工作")
}

// TestPlanMode_PromptStructureOrder 验证 System Prompt 的段落顺序：
// 核心身份 → 计划模式 → AGENTS.md → 技能索引
func TestPlanMode_PromptStructureOrder(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建 AGENTS.md
	os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("# 规范"), 0644)

	composer := NewPromptComposer(tmpDir)
	composer.SetPlanMode(true)

	msg := composer.Build()

	// 验证顺序：核心身份 < 计划模式 < AGENTS.md
	idxCore := strings.Index(msg.Content, "核心身份")
	idxPlan := strings.Index(msg.Content, "Plan Mode: ON")
	idxAgents := strings.Index(msg.Content, "项目专属指南")

	if idxCore < 0 || idxPlan < 0 || idxAgents < 0 {
		t.Fatalf("❌ 段落缺失: core=%d, plan=%d, agents=%d", idxCore, idxPlan, idxAgents)
	}

	if idxCore > idxPlan {
		t.Fatal("❌ 段落顺序错误：核心身份应在计划模式之前")
	}
	if idxPlan > idxAgents {
		t.Fatal("❌ 段落顺序错误：计划模式应在 AGENTS.md 之前")
	}

	t.Logf("✅ 段落顺序正确: 核心身份(%d) → 计划模式(%d) → AGENTS.md(%d)", idxCore, idxPlan, idxAgents)
}

// TestPlanMode_PromptLength 验证计划模式增加了合理的 Prompt 长度。
func TestPlanMode_PromptLength(t *testing.T) {
	tmpDir := t.TempDir()

	composerOff := NewPromptComposer(tmpDir)
	msgOff := composerOff.Build()

	composerOn := NewPromptComposer(tmpDir)
	composerOn.SetPlanMode(true)
	msgOn := composerOn.Build()

	addedChars := len(msgOn.Content) - len(msgOff.Content)

	t.Logf("📊 无计划模式: %d 字符", len(msgOff.Content))
	t.Logf("📊 有计划模式: %d 字符", len(msgOn.Content))
	t.Logf("📊 计划模式增量: %d 字符", addedChars)

	// 计划模式指令不应为空
	if addedChars <= 0 {
		t.Fatal("❌ 计划模式未增加任何内容")
	}

	// 增量不应过大（合理范围：500-3000 字符）
	if addedChars < 200 {
		t.Errorf("❌ 计划模式增量过小 (%d 字符)，可能指令不完整", addedChars)
	}
	if addedChars > 5000 {
		t.Errorf("❌ 计划模式增量过大 (%d 字符)，可能有冗余内容", addedChars)
	}

	t.Log("✅ 计划模式增量长度合理")
}

// TestPlanMode_MessageRole 验证 Build() 返回的消息角色始终是 RoleSystem。
func TestPlanMode_MessageRole(t *testing.T) {
	tmpDir := t.TempDir()

	composer := NewPromptComposer(tmpDir)
	composer.SetPlanMode(true)

	msg := composer.Build()

	if msg.Role != "system" {
		t.Errorf("❌ 期望 role=system, 得到 role=%s", msg.Role)
	}

	t.Log("✅ Build() 返回消息角色正确 (system)")
}
