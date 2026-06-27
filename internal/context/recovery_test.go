package context

import (
	"strings"
	"testing"
)

// TestRecoveryManager_EditMatchNotFound 测试 edit_file 匹配失败救援
func TestRecoveryManager_EditMatchNotFound(t *testing.T) {
	rm := NewRecoveryManager()

	rawError := "[ERR_EDIT_MATCH_NOT_FOUND] 编辑文件 auth.go 失败（Level 4）: 在文件中未找到 old_text"
	result := rm.AnalyzeAndInject("edit_file", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("未注入救援指南")
	}
	if !strings.Contains(result, "read_file") {
		t.Error("救援指南未提示使用 read_file")
	}

	t.Logf("✅ ERR_EDIT_MATCH_NOT_FOUND 救援:\n%s", result)
}

// TestRecoveryManager_EditMultipleMatch 测试 edit_file 多处匹配救援
func TestRecoveryManager_EditMultipleMatch(t *testing.T) {
	rm := NewRecoveryManager()

	rawError := "[ERR_EDIT_MULTIPLE_MATCH] 编辑文件 auth.go 失败（Level 1）: 精确匹配到了 3 处"
	result := rm.AnalyzeAndInject("edit_file", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("未注入救援指南")
	}
	if !strings.Contains(result, "上下") {
		t.Error("救援指南未提示增加上下文")
	}

	t.Logf("✅ ERR_EDIT_MULTIPLE_MATCH 救援:\n%s", result)
}

// TestRecoveryManager_FileNotFound 测试文件不存在救援
func TestRecoveryManager_FileNotFound(t *testing.T) {
	rm := NewRecoveryManager()

	rawError := "[ERR_FILE_NOT_FOUND] 文件不存在: auth.go"
	result := rm.AnalyzeAndInject("read_file", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("未注入救援指南")
	}
	if !strings.Contains(result, "ls") || !strings.Contains(result, "find") {
		t.Error("救援指南未提示使用 ls/find 命令")
	}

	t.Logf("✅ ERR_FILE_NOT_FOUND 救援:\n%s", result)
}

// TestRecoveryManager_PermissionDenied 测试权限拒绝救援
func TestRecoveryManager_PermissionDenied(t *testing.T) {
	rm := NewRecoveryManager()

	rawError := "[ERR_PERMISSION_DENIED] 无权限读取: /etc/shadow"
	result := rm.AnalyzeAndInject("read_file", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("未注入救援指南")
	}
	if !strings.Contains(result, "权限") {
		t.Error("救援指南未提示权限问题")
	}

	t.Logf("✅ ERR_PERMISSION_DENIED 救援:\n%s", result)
}

// TestRecoveryManager_BashTimeout 测试 bash 超时救援
func TestRecoveryManager_BashTimeout(t *testing.T) {
	rm := NewRecoveryManager()

	rawError := "[ERR_BASH_TIMEOUT] 命令执行超时"
	result := rm.AnalyzeAndInject("bash", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("未注入救援指南")
	}
	if !strings.Contains(result, "后台") {
		t.Error("救援指南未提示后台执行")
	}

	t.Logf("✅ ERR_BASH_TIMEOUT 救援:\n%s", result)
}

// TestRecoveryManager_UnknownErrorCode 测试未知错误码走兜底逻辑
func TestRecoveryManager_UnknownErrorCode(t *testing.T) {
	rm := NewRecoveryManager()

	// 有错误码但不在 switch 中
	rawError := "[ERR_SOMETHING_ELSE] 未知错误"
	result := rm.AnalyzeAndInject("bash", rawError)

	// 无匹配错误码，且 bash 兜底也不匹配，应原样返回
	if strings.Contains(result, "[系统救援指南]") {
		t.Error("未知错误码不应注入救援指南")
	}

	t.Logf("✅ 未知错误码正确跳过: %s", result)
}

// TestRecoveryManager_NoErrorCode 测试无错误码的原始报错走兜底
func TestRecoveryManager_NoErrorCode(t *testing.T) {
	rm := NewRecoveryManager()

	// bash: command not found（无错误码，但命中 bash 兜底）
	rawError := "bash: xyz: command not found"
	result := rm.AnalyzeAndInject("bash", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("bash command not found 应触发兜底救援")
	}

	t.Logf("✅ 无错误码兜底救援:\n%s", result)
}

// TestRecoveryManager_PosixFallback 测试 POSIX 标准错误的兜底
func TestRecoveryManager_PosixFallback(t *testing.T) {
	rm := NewRecoveryManager()

	// 无错误码，但包含 POSIX 标准错误
	rawError := "open /tmp/auth.go: no such file or directory"
	result := rm.AnalyzeAndInject("read_file", rawError)

	if !strings.Contains(result, "[系统救援指南]") {
		t.Error("POSIX 错误应触发兜底救援")
	}

	t.Logf("✅ POSIX 兜底救援:\n%s", result)
}
