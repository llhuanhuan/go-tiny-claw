package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuthPrompt_RecoveryGuide 测试救援指南注入机制
// 故意构造一个 edit_file 会失败的场景，验证引擎能否自愈
func TestAuthPrompt_RecoveryGuide(t *testing.T) {
	skipIfNoAPIKey(t)

	eng, workDir := newTestEngine(t)
	defer os.RemoveAll(workDir)

	// 创建 auth.go，但内容与 prompt 中的 old_text 不完全匹配
	// 故意缺少 "// 鉴权入口函数" 注释，触发 edit_file 失败
	authContent := `package main

func login(user string) bool {
    // 检查用户名
    if user == "admin" {
        return true
    }
    return false
}
`
	authPath := filepath.Join(workDir, "auth.go")
	if err := os.WriteFile(authPath, []byte(authContent), 0644); err != nil {
		t.Fatalf("创建 auth.go 失败: %v", err)
	}

	// 构造一个会触发 edit_file 失败的 prompt
	// prompt 中的 old_text 包含 "// 鉴权入口函数"，但文件中没有
	prompt := `我当前目录下有一个 auth.go 文件。
请修改 auth.go 中的 login 函数。
请直接使用 edit_file 工具替换下面的代码块，将判断条件改为同时允许"admin"、"root"和"guest"三种用户登录：

// 鉴权入口函数
func login(user string) bool {
    // 检查用户名
    if user == "admin" {
        return true
    }
    return false
}`

	// 执行引擎
	reporter := &captureReporter{}
	ctx := context.Background()
	session := NewSession("test_recovery_001", workDir)

	err := eng.Run(ctx, session, prompt, reporter)
	if err != nil {
		t.Fatalf("引擎执行失败: %v", err)
	}

	// 验证结果
	content, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("读取修改后的 auth.go 失败: %v", err)
	}

	modifiedContent := string(content)

	// 验证是否包含三种用户
	adminFound := strings.Contains(modifiedContent, `"admin"`)
	rootFound := strings.Contains(modifiedContent, `"root"`)
	guestFound := strings.Contains(modifiedContent, `"guest"`)

	if !adminFound || !rootFound || !guestFound {
		t.Errorf("修改后的代码缺少用户判断: admin=%v, root=%v, guest=%v", adminFound, rootFound, guestFound)
	}

	// 验证是否经历了失败-自愈过程
	editFileFailed := false
	editFileSucceeded := false
	readFileCalled := false

	for _, result := range reporter.toolResults {
		if strings.Contains(result, "edit_file") {
			if strings.Contains(result, "FAIL") || strings.Contains(result, "Error") {
				editFileFailed = true
			}
			if strings.Contains(result, "OK") {
				editFileSucceeded = true
			}
		}
		if strings.Contains(result, "read_file") {
			readFileCalled = true
		}
	}

	t.Logf("=== 救援指南测试结果 ===")
	t.Logf("edit_file 失败过: %v", editFileFailed)
	t.Logf("edit_file 最终成功: %v", editFileSucceeded)
	t.Logf("read_file 被调用: %v", readFileCalled)
	t.Logf("使用的工具: %v", reporter.tools)

	// 验证自愈过程
	if editFileFailed && editFileSucceeded {
		t.Log("✅ 救援指南机制生效！引擎经历了 失败→救援→自愈 的完整流程")
	} else if editFileSucceeded {
		t.Log("⚠️ edit_file 一次成功（可能模型自行修正了 old_text），未触发救援指南")
	}
}
