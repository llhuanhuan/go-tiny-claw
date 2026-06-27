package tools

import (
	"fmt"
	"strings"
)

// ToolErrorCode 领域错误码，用于 RecoveryManager 精确匹配
// 采用全大写 + 下划线命名，与语言无关，极其稳定
type ToolErrorCode string

const (
	// 文件操作通用
	ErrFileNotFound     ToolErrorCode = "ERR_FILE_NOT_FOUND"
	ErrPermissionDenied ToolErrorCode = "ERR_PERMISSION_DENIED"
	ErrFileReadFailed   ToolErrorCode = "ERR_FILE_READ_FAILED"
	ErrFileWriteFailed  ToolErrorCode = "ERR_FILE_WRITE_FAILED"
	ErrDirCreateFailed  ToolErrorCode = "ERR_DIR_CREATE_FAILED"

	// edit_file 专属
	ErrEditMatchNotFound ToolErrorCode = "ERR_EDIT_MATCH_NOT_FOUND"
	ErrEditMultipleMatch ToolErrorCode = "ERR_EDIT_MULTIPLE_MATCH"
	ErrEditOldEmpty      ToolErrorCode = "ERR_EDIT_OLD_EMPTY"

	// bash 专属
	ErrBashTimeout ToolErrorCode = "ERR_BASH_TIMEOUT"

	// 通用
	ErrParamParseFailed ToolErrorCode = "ERR_PARAM_PARSE_FAILED"
)

// ToolError 带标准化错误码的结构化错误
type ToolError struct {
	Code    ToolErrorCode
	Message string
	Cause   error
}

func (e *ToolError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *ToolError) Unwrap() error {
	return e.Cause
}

// NewToolError 创建带错误码的工具错误
func NewToolError(code ToolErrorCode, msg string, cause error) *ToolError {
	return &ToolError{Code: code, Message: msg, Cause: cause}
}

// ExtractErrorCode 从错误文本中提取错误码
// 格式: "[ERR_XXX] 详细信息..."
func ExtractErrorCode(errMsg string) ToolErrorCode {
	start := strings.Index(errMsg, "[")
	end := strings.Index(errMsg, "]")
	if start == -1 || end == -1 || end <= start+1 {
		return ""
	}
	code := errMsg[start+1 : end]
	return ToolErrorCode(code)
}
