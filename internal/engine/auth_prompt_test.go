package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuthPrompt_AutoTriage 测试自动分诊与纠偏体系
// 验证引擎能否正确处理修改 auth.go 的 prompt
func TestAuthPrompt_AutoTriage(t *testing.T) {
	skipIfNoAPIKey(t)

	eng, workDir := newTestEngine(t)
	defer os.RemoveAll(workDir)

	// 创建初始 auth.go 文件
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

	// 构造 prompt
	prompt := `我当前目录下有一个 auth.go 文件。
请修改 auth.go 中的 login 函数。
请直接使用 edit_file 工具替换下面的代码块，将判断条件改为同时允许"admin"、"root"和"guest"三种用户登录：

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
	session := NewSession("test_auth_prompt", workDir)

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
	if !strings.Contains(modifiedContent, `"admin"`) {
		t.Error("修改后的代码缺少 admin 用户判断")
	}
	if !strings.Contains(modifiedContent, `"root"`) {
		t.Error("修改后的代码缺少 root 用户判断")
	}
	if !strings.Contains(modifiedContent, `"guest"`) {
		t.Error("修改后的代码缺少 guest 用户判断")
	}

	// 验证是否使用了 edit_file 工工具
	editFileUsed := false
	for _, tool := range reporter.tools {
		if tool == "edit_file" {
			editFileUsed = true
			break
		}
	}
	if !editFileUsed {
		t.Error("引擎未使用 edit_file 工具，自动分诊机制可能失效")
	}

	t.Logf("✅ 自动分诊与纠偏体系验证通过")
	t.Logf("使用的工具: %v", reporter.tools)
}
