// internal/tools/write_file.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// WriteFileTool 实现了创建或覆盖写入文件的工具
type WriteFileTool struct {
	workDir string // 工作区约束
}

func NewWriteFileTool(workDir string) *WriteFileTool {
	return &WriteFileTool{workDir: workDir}
}

func (t *WriteFileTool) Name() string {
	return "write_file"
}

func (t *WriteFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "创建或覆盖写入一个文件。如果目录不存在会自动创建。请提供相对于工作区的相对路径。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "要写入的文件路径，如 src/main.go",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "要写入的完整文件内容",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *WriteFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input writeFileArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", NewToolError(ErrParamParseFailed, "参数解析失败", err)
	}

	// 【安全防线】：限制在 WorkDir 下执行，防止大模型修改系统级文件
	fullPath := filepath.Join(t.workDir, input.Path)

	// 自动创建缺失的父级目录
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return "", NewToolError(ErrDirCreateFailed, "创建父目录失败", err)
	}

	// 写入文件内容，权限设为 0644
	err := os.WriteFile(fullPath, []byte(input.Content), 0644)
	if err != nil {
		if os.IsPermission(err) {
			return "", NewToolError(ErrPermissionDenied, fmt.Sprintf("无权限写入: %s", input.Path), err)
		}
		return "", NewToolError(ErrFileWriteFailed, "写入文件失败", err)
	}

	return fmt.Sprintf("成功将内容写入到文件: %s", input.Path), nil
}
