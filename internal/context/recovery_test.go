package context

import (
	"strings"
	"testing"
)

// TestRecoveryManager_EditFileNotFound 测试 edit_file 的 "old_text 未找到" 救援
func TestRecoveryManager_EditFileNotFound(t *testing.T) {
	rm := NewRecoveryManager()

	rawError := "Error executing edit_file: 在文件中未找到 old_text，请检查内容和缩进"
	result := rm.AnalyzeAndInject("edit_file", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("未注入救援指南")
	}
	if !strings.Contains(result, "read_file") {
		t.Error("救援指南未提示使用 read_file")
	}

	t.Logf("✅ edit_file 未找到救援:\n%s", result)
}

// TestRecoveryManager_EditFileMultipleMatches 测试 edit_file 多处匹配救援
func TestRecoveryManager_EditFileMultipleMatches(t *testing.T) {
	rm := NewRecoveryManager()

	rawError := "匹配到了多处，请提供更多上下文"
	result := rm.AnalyzeAndInject("edit_file", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("未注入救援指南")
	}
	if !strings.Contains(result, "上下") {
		t.Error("救援指南未提示增加上下文")
	}

	t.Logf("✅ edit_file 多处匹配救援:\n%s", result)
}

// TestRecoveryManager_ReadFileNotFound 测试 read_file 文件不存在救援
func TestRecoveryManager_ReadFileNotFound(t *testing.T) {
	rm := NewRecoveryManager()

	rawError := "open /tmp/auth.go: no such file or directory"
	result := rm.AnalyzeAndInject("read_file", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("未注入救援指南")
	}
	if !strings.Contains(result, "ls") || !strings.Contains(result, "find") {
		t.Error("救援指南未提示使用 ls/find 命令")
	}

	t.Logf("✅ read_file 文件不存在救援:\n%s", result)
}

// TestRecoveryManager_BashCommandNotFound 测试 bash 命令不存在救援
func TestRecoveryManager_BashCommandNotFound(t *testing.T) {
	rm := NewRecoveryManager()

	rawError := "bash: xyz: command not found"
	result := rm.AnalyzeAndInject("bash", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("未注入救援指南")
	}

	t.Logf("✅ bash 命令不存在救援:\n%s", result)
}

// TestRecoveryManager_BashTimeout 测试 bash 超时救援
func TestRecoveryManager_BashTimeout(t *testing.T) {
	rm := NewRecoveryManager()

	rawError := "命令执行超时，已强制终止"
	result := rm.AnalyzeAndInject("bash", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("未注入救援指南")
	}
	if !strings.Contains(result, "后台") {
		t.Error("救援指南未提示后台执行")
	}

	t.Logf("✅ bash 超时救援:\n%s", result)
}

// TestRecoveryManager_UnknownError 测试未知错误不注入救援
func TestRecoveryManager_UnknownError(t *testing.T) {
	rm := NewRecoveryManager()

	rawError := "some random error"
	result := rm.AnalyzeAndInject("edit_file", rawError)

	if strings.Contains(result, "[系统救援指南]") {
		t.Error("未知错误不应注入救援指南")
	}

	t.Logf("✅ 未知错误正确跳过: %s", result)
}
