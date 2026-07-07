package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// SearchFilesTool 在工作区内搜索文件内容（类似 ripgrep）。
// 返回匹配的文件路径、行号和内容摘要。
type SearchFilesTool struct {
	workDir string
}

func NewSearchFilesTool(workDir string) *SearchFilesTool {
	return &SearchFilesTool{workDir: workDir}
}

func (t *SearchFilesTool) Name() string { return "search_files" }

func (t *SearchFilesTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: "search_files",
		Description: "在工作区内搜索文件内容（正则匹配）。返回匹配的文件路径、行号和内容摘要。\n" +
			"适用于快速定位代码位置、查找函数定义、搜索错误信息等场景。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{
					"type":        "string",
					"description": "正则表达式搜索模式（Go regexp 语法），例如 \"func.*Handler\" 或 \"TODO\"",
				},
				"path": map[string]interface{}{
					"type":        "string",
					"description": "搜索的目录路径（相对于工作区），默认为 \".\"（整个工作区）",
				},
				"file_glob": map[string]interface{}{
					"type":        "string",
					"description": "文件名通配符过滤，例如 \"*.go\" 或 \"*.md\"，留空则搜索所有文件",
				},
				"max_results": map[string]interface{}{
					"type":        "integer",
					"description": "最大返回结果数，默认 30",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

type searchFilesArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	FileGlob   string `json:"file_glob"`
	MaxResults int    `json:"max_results"`
}

type searchResult struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

func (t *SearchFilesTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input searchFilesArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", NewToolError(ErrParamParseFailed, "参数解析失败", err)
	}

	if input.Pattern == "" {
		return "", NewToolError(ErrParamParseFailed, "搜索模式不能为空", nil)
	}

	searchDir := t.workDir
	if input.Path != "" {
		searchDir = filepath.Join(t.workDir, filepath.FromSlash(input.Path))
	}

	maxResults := input.MaxResults
	if maxResults <= 0 {
		maxResults = 30
	}

	regex, err := regexp.Compile(input.Pattern)
	if err != nil {
		return "", NewToolError(ErrParamParseFailed, fmt.Sprintf("正则表达式编译失败: %v", err), err)
	}

	var results []searchResult
	filesSearched := 0

	err = filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过无法访问的文件
		}

		// 跳过隐藏目录和常见非源码目录
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}

		// 文件名通配符过滤
		if input.FileGlob != "" {
			matched, _ := filepath.Match(input.FileGlob, info.Name())
			if !matched {
				return nil
			}
		}

		// 跳过二进制文件（简单判断：大小 > 1MB 或扩展名）
		if info.Size() > 1024*1024 {
			return nil
		}

		filesSearched++

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		relPath, _ := filepath.Rel(t.workDir, path)

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if regex.MatchString(line) {
				results = append(results, searchResult{
					File:    filepath.ToSlash(relPath),
					Line:    lineNum,
					Content: truncateString(line, 200),
				})
				if len(results) >= maxResults {
					return filepath.SkipDir // 提前终止（实际上只终止当前目录遍历）
				}
			}
		}

		if len(results) >= maxResults {
			return filepath.SkipDir
		}

		return nil
	})

	if err != nil && err != filepath.SkipDir {
		return fmt.Sprintf("搜索过程中出现警告: %v", err), nil
	}

	if len(results) == 0 {
		return fmt.Sprintf("未找到匹配结果（搜索了 %d 个文件，模式: %s）", filesSearched, input.Pattern), nil
	}

	// 格式化输出
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("找到 %d 个匹配（搜索了 %d 个文件）:\n\n", len(results), filesSearched))
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("  %s:%d: %s\n", r.File, r.Line, r.Content))
	}

	return sb.String(), nil
}

func truncateString(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
