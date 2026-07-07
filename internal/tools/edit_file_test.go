package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFuzzyReplace_L1_ExactMatch 测试 Level 1 精确匹配
func TestFuzzyReplace_L1_ExactMatch(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		oldText   string
		newText   string
		want      string
		wantLevel int
		wantErr   bool
	}{
		{
			name:      "单次精确匹配",
			content:   "hello world",
			oldText:   "world",
			newText:   "golang",
			want:      "hello golang",
			wantLevel: 1,
		},
		{
			name:      "精确匹配多处（非 replaceAll）",
			content:   "abc abc abc",
			oldText:   "abc",
			newText:   "xyz",
			wantErr:   true,
			wantLevel: 1,
		},
		{
			name:      "精确匹配多处（replaceAll）",
			content:   "abc abc abc",
			oldText:   "abc",
			newText:   "xyz",
			want:      "xyz xyz xyz",
			wantLevel: 1,
		},
		{
			name:      "完全不匹配",
			content:   "hello world",
			oldText:   "notfound",
			newText:   "xxx",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, level, err := fuzzyReplace(tt.content, tt.oldText, tt.newText, false)
			if tt.name == "精确匹配多处（replaceAll）" {
				got, level, err = fuzzyReplace(tt.content, tt.oldText, tt.newText, true)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("fuzzyReplace() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got != tt.want {
					t.Errorf("fuzzyReplace() got = %q, want %q", got, tt.want)
				}
				if level != tt.wantLevel {
					t.Errorf("fuzzyReplace() level = %d, want %d", level, tt.wantLevel)
				}
			}
		})
	}
}

// TestFuzzyReplace_L2_CRLF 测试 Level 2 换行符归一化
func TestFuzzyReplace_L2_CRLF(t *testing.T) {
	// 文件内容用 \r\n，搜索文本用 \n（模拟 LLM 生成的内容不含 \r\n）
	content := "line1\r\nline2\r\nline3"
	oldText := "line1\nline2"
	newText := "replaced"

	got, level, err := fuzzyReplace(content, oldText, newText, false)
	if err != nil {
		t.Fatalf("fuzzyReplace() error = %v", err)
	}
	if level != 2 {
		t.Errorf("期望 Level 2 匹配，实际 Level %d", level)
	}
	// 归一化后 \r\n → \n，替换后内容应包含 newText
	if got == content {
		t.Error("内容未被替换")
	}
}

// TestFuzzyReplace_L3_TrimSpace 测试 Level 3 首尾空白忽略
func TestFuzzyReplace_L3_TrimSpace(t *testing.T) {
	content := "  hello world  \n  foo bar  "
	oldText := "\nhello world\n"
	newText := "\nHELLO WORLD\n"

	got, level, err := fuzzyReplace(content, oldText, newText, false)
	if err != nil {
		t.Fatalf("fuzzyReplace() error = %v", err)
	}
	if level != 3 {
		t.Errorf("期望 Level 3 匹配，实际 Level %d", level)
	}
	if got == "" {
		t.Error("fuzzyReplace() 返回空字符串")
	}
}

// TestFuzzyReplace_L4_IndentTolerance 测试 Level 4 缩进容错
func TestFuzzyReplace_L4_IndentTolerance(t *testing.T) {
	// 文件内容有缩进，搜索文本没有缩进
	content := "func main() {\n\tfmt.Println(\"hello\")\n\tfmt.Println(\"world\")\n}"
	oldText := "fmt.Println(\"hello\")\nfmt.Println(\"world\")"
	newText := "log.Println(\"HELLO\")\nlog.Println(\"WORLD\")"

	got, level, err := fuzzyReplace(content, oldText, newText, false)
	if err != nil {
		t.Fatalf("fuzzyReplace() error = %v", err)
	}
	if level != 4 {
		t.Errorf("期望 Level 4 匹配，实际 Level %d", level)
	}
	if got == content {
		t.Error("内容未被替换")
	}
}

// TestLineByLineReplace_MultipleMatches 测试多处匹配的错误
func TestLineByLineReplace_MultipleMatches(t *testing.T) {
	content := "aaa\nbbb\naaa\nbbb"
	oldText := "aaa\nbbb"
	newText := "xxx"

	_, _, err := lineByLineReplace(content, oldText, newText, false)
	if err == nil {
		t.Error("期望返回多处匹配错误，但没有")
	}
}

// TestLineByLineReplace_ReplaceAll 测试多处替换
func TestLineByLineReplace_ReplaceAll(t *testing.T) {
	content := "aaa\nbbb\naaa\nbbb"
	oldText := "aaa\nbbb"
	newText := "xxx"

	got, level, err := lineByLineReplace(content, oldText, newText, true)
	if err != nil {
		t.Fatalf("lineByLineReplace() error = %v", err)
	}
	if level != 4 {
		t.Errorf("期望 Level 4，实际 Level %d", level)
	}
	// replaceAll 应该替换所有匹配
	if got == content {
		t.Error("内容未被替换")
	}
}

// TestEditFileTool_Execute 测试完整的 edit_file 工具执行
func TestEditFileTool_Execute(t *testing.T) {
	// 创建临时目录
	tmpDir := t.TempDir()
	tool := NewEditFileTool(tmpDir)

	// 创建测试文件
	testFile := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(testFile, []byte("hello world\nfoo bar\n"), 0644)

	tests := []struct {
		name    string
		args    string
		wantErr bool
	}{
		{
			name:    "L1 精确替换",
			args:    `{"path":"test.txt","old_string":"hello world","new_string":"HELLO WORLD"}`,
			wantErr: false,
		},
		{
			name:    "old_string 为空",
			args:    `{"path":"test.txt","old_string":"","new_string":"xxx"}`,
			wantErr: true,
		},
		{
			name:    "文件不存在",
			args:    `{"path":"nonexistent.txt","old_string":"xxx","new_string":"yyy"}`,
			wantErr: true,
		},
		{
			name:    "L4 缩进容错",
			args:    `{"path":"test.txt","old_string":"foo bar","new_string":"BAZ QUUX"}`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Execute(nil, []byte(tt.args))
			if (err != nil) != tt.wantErr {
				t.Errorf("Execute() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
