// internal/context/recovery.go
package context

import (
	"fmt"
	"strings"
)

// RecoveryManager 负责在工具执行失败时，根据标准化错误码注入恢复建议
type RecoveryManager struct{}

func NewRecoveryManager() *RecoveryManager {
	return &RecoveryManager{}
}

// AnalyzeAndInject 接收原始报错，提取错误码，返回增强后的报错信息
func (rm *RecoveryManager) AnalyzeAndInject(toolName string, rawError string) string {
	// 第一步：从错误文本中提取标准化错误码
	code := extractErrorCode(rawError)

	// 第二步：基于错误码的精确 switch-case 匹配
	var hint string
	switch code {
	case "ERR_EDIT_MATCH_NOT_FOUND":
		hint = "old_string 与文件内容不一致。请先使用 `read_file` 重新读取文件，获取最新内容后再编辑。"
	case "ERR_EDIT_MULTIPLE_MATCH":
		hint = "old_string 匹配到了多处。请增加上下相邻的几行代码，确保替换的唯一性。"
	case "ERR_EDIT_OLD_EMPTY":
		hint = "old_string 不能为空，请提供待替换的文本片段。"
	case "ERR_FILE_NOT_FOUND":
		hint = "文件路径不正确。请使用 `bash` 执行 `ls` 或 `find` 命令确认正确的目录结构。"
	case "ERR_PERMISSION_DENIED":
		hint = "权限不足。请检查文件权限，或考虑是否需要修改其他文件。"
	case "ERR_FILE_READ_FAILED":
		hint = "文件读取失败。请检查文件是否被占用或磁盘空间是否充足。"
	case "ERR_FILE_WRITE_FAILED":
		hint = "文件写入失败。请检查磁盘空间和写入权限。"
	case "ERR_DIR_CREATE_FAILED":
		hint = "目录创建失败。请检查父路径权限或磁盘空间。"
	case "ERR_PARAM_PARSE_FAILED":
		hint = "参数格式错误。请检查 JSON 格式是否正确，必填字段是否遗漏。"
	case "ERR_BASH_TIMEOUT":
		hint = "命令执行超时。如果是常驻服务，请使用 `run_in_background: true` 转入后台。"
	}

	// 第三步：无匹配错误码时，尝试基于工具名的通用兜底
	if hint == "" {
		hint = rm.fallbackByToolName(toolName, rawError)
	}

	if hint == "" {
		return rawError
	}

	return fmt.Sprintf("%s\n\n[系统救援指南]: %s", rawError, hint)
}

// fallbackByToolName 当错误码未命中时，按工具名做最后一层兜底
func (rm *RecoveryManager) fallbackByToolName(toolName, rawError string) string {
	lower := strings.ToLower(rawError)

	switch toolName {
	case "bash":
		if strings.Contains(lower, "command not found") {
			return "系统中未安装该命令。请考虑是否有替代命令，或需要先安装依赖。"
		}
	case "read_file", "write_file":
		if strings.Contains(lower, "no such file or directory") {
			return "文件路径似乎不正确。请先使用 `bash` 执行 `ls` 确认目录结构。"
		}
	}

	return ""
}

// extractErrorCode 从错误文本中提取 [ERR_XXX] 格式的错误码
func extractErrorCode(errMsg string) string {
	start := strings.Index(errMsg, "[")
	end := strings.Index(errMsg, "]")
	if start == -1 || end == -1 || end <= start+1 {
		return ""
	}
	code := errMsg[start+1 : end]
	// 验证是否为合法的错误码格式
	if strings.HasPrefix(code, "ERR_") {
		return code
	}
	return ""
}
