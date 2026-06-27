// internal/tools/edit_file.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// EditFileTool 实现了精确字符串替换的文件编辑工具。
//
// 容错哲学：把容错做在底层工具里，吸收大模型的误差。
// 不再要求精确匹配，而是实现一条四级模糊匹配链：
//
//	Level 1: 精确匹配 —— 最快最安全
//	Level 2: 统一换行符 (Windows \r\n → Unix \n)
//	Level 3: TrimSpace 匹配 —— 忽略首尾空白（含空行）
//	Level 4: 滑动窗口逐行去缩进匹配 —— 消除大模型遗漏缩进的幻觉
//
// 唯一性校验贯穿每一级：若匹配到多处相似片段，
// 工具拒绝盲目替换，要求模型提供更多上下行以精确定位。
//
// 宁可格式微瑕，也绝不阻断执行流。
type EditFileTool struct {
	workDir string
}

func NewEditFileTool(workDir string) *EditFileTool {
	return &EditFileTool{workDir: workDir}
}

func (t *EditFileTool) Name() string {
	return "edit_file"
}

func (t *EditFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "对文件执行精确字符串替换。提供 old_string 定位待替换文本，new_string 为替换后的内容。\n\n" +
			"匹配策略（四级模糊容错）：工具内置降级匹配管道，无需模型产出完美格式。\n" +
			"唯一性约束：若匹配到多处相似片段，工具拒绝执行，请提供更多上下行以精确锁定目标。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "要编辑的文件路径，如 cmd/claw/main.go",
				},
				"old_string": map[string]interface{}{
					"type":        "string",
					"description": "文件中原有的待替换文本片段。不需要100%精确——工具内置多级模糊匹配。",
				},
				"new_string": map[string]interface{}{
					"type":        "string",
					"description": "替换后的新文本内容",
				},
				"replace_all": map[string]interface{}{
					"type":        "boolean",
					"description": "设为 true 时替换文件中所有匹配位置，默认为 false（仅替换第一处）",
				},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
	}
}

type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (t *EditFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input editFileArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", NewToolError(ErrParamParseFailed, "参数解析失败", err)
	}

	if input.OldString == "" {
		return "", NewToolError(ErrEditOldEmpty, "old_string 不能为空", nil)
	}

	fullPath := filepath.Join(t.workDir, input.Path)

	raw, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", NewToolError(ErrFileNotFound, fmt.Sprintf("文件不存在: %s", input.Path), err)
		}
		return "", NewToolError(ErrFileReadFailed, "读取文件失败", err)
	}

	originalContent := string(raw)

	// =========================================================================
	// 多级模糊匹配 + 替换 (大道至简：不做 rebase，直接降级替换)
	// =========================================================================

	var result string
	var level int

	result, level, err = fuzzyReplace(originalContent, input.OldString, input.NewString, input.ReplaceAll)
	if err != nil {
		// 匹配失败：输出文件预览帮助模型定位
		preview := originalContent
		if len(preview) > 1200 {
			preview = originalContent[:1200] + "\n...[已截断]..."
		}
		// 根据错误类型分派错误码
		code := ErrEditMatchNotFound
		if strings.Contains(err.Error(), "匹配到了") {
			code = ErrEditMultipleMatch
		}
		return "", NewToolError(code,
			fmt.Sprintf("编辑文件 %s 失败（Level %d）: %s\n文件预览:\n---\n%s\n---",
				input.Path, level, err.Error(), preview), nil)
	}

	if err := os.WriteFile(fullPath, []byte(result), 0644); err != nil {
		return "", NewToolError(ErrFileWriteFailed, "写入文件失败", err)
	}

	return fmt.Sprintf("成功编辑文件 %s（匹配级别: Level %d）。", input.Path, level), nil
}

// =========================================================================
// fuzzyReplace 四级容错降级替换算法
// =========================================================================
func fuzzyReplace(originalContent, oldText, newText string, replaceAll bool) (string, int, error) {

	// L1: 精确匹配
	count := strings.Count(originalContent, oldText)
	if count == 1 {
		return strings.Replace(originalContent, oldText, newText, 1), 1, nil
	}
	if count > 1 && !replaceAll {
		return "", 1, fmt.Errorf("old_text 精确匹配到了 %d 处，请提供更多的上下文代码以确保唯一性", count)
	}
	if count > 1 && replaceAll {
		return strings.ReplaceAll(originalContent, oldText, newText), 1, nil
	}

	// L2: 换行符归一化 (统一将 \r\n 转换为 \n)
	// 宁可接受 \r\n → \n 的格式微瑕，也绝不阻断执行流
	normalizedContent := strings.ReplaceAll(originalContent, "\r\n", "\n")
	normalizedOld := strings.ReplaceAll(oldText, "\r\n", "\n")

	count = strings.Count(normalizedContent, normalizedOld)
	if count == 1 {
		return strings.Replace(normalizedContent, normalizedOld, newText, 1), 2, nil
	}
	if count > 1 && !replaceAll {
		return "", 2, fmt.Errorf("归一化匹配到了 %d 处，请提供更多的上下文代码以确保唯一性", count)
	}
	if count > 1 && replaceAll {
		return strings.ReplaceAll(normalizedContent, normalizedOld, newText), 2, nil
	}

	// L3: TrimSpace 匹配 (忽略首尾空白和空行)
	trimmedOld := strings.TrimSpace(normalizedOld)
	if trimmedOld != "" {
		count = strings.Count(normalizedContent, trimmedOld)
		if count == 1 {
			// 注意：newText 可能不带原始缩进，替换后格式可能不完美。
			// 但这总比直接报错让 Agent 死循环要好。
			return strings.Replace(normalizedContent, trimmedOld, newText, 1), 3, nil
		}
		if count > 1 && !replaceAll {
			return "", 3, fmt.Errorf("TrimSpace 匹配到了 %d 处，请提供更多的上下文代码以确保唯一性", count)
		}
		if count > 1 && replaceAll {
			return strings.ReplaceAll(normalizedContent, trimmedOld, newText), 3, nil
		}
	}

	// L4: 逐行去缩进匹配 —— 最强力的容错：消除大模型遗漏缩进的幻觉
	return lineByLineReplace(normalizedContent, normalizedOld, newText, replaceAll)
}

// =========================================================================
// lineByLineReplace 将文本按行切分，去掉每行首尾空白后进行滑动窗口匹配。
// 匹配成功后执行行级替换：保留原始行的缩进上下文。
// =========================================================================
func lineByLineReplace(content, oldText, newText string, replaceAll bool) (string, int, error) {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(strings.TrimSpace(oldText), "\n")

	if len(oldLines) == 0 || len(contentLines) < len(oldLines) {
		return "", 4, fmt.Errorf("在文件中未找到 old_text，请先调用 read_file 仔细确认文件内容和缩进")
	}

	// 清理 oldLines 的每行首尾空白
	for i := range oldLines {
		oldLines[i] = strings.TrimSpace(oldLines[i])
	}

	// 滑动窗口在原始文件中寻找所有匹配块
	type matchWindow struct {
		startLine int // 匹配起始行号 (0-based)
		endLine   int // 匹配结束行号 (0-based, exclusive)
	}
	var matches []matchWindow

	for i := 0; i <= len(contentLines)-len(oldLines); i++ {
		isMatch := true
		for j := 0; j < len(oldLines); j++ {
			if strings.TrimSpace(contentLines[i+j]) != oldLines[j] {
				isMatch = false
				break
			}
		}
		if isMatch {
			matches = append(matches, matchWindow{startLine: i, endLine: i + len(oldLines)})
		}
	}

	if len(matches) == 0 {
		return "", 4, fmt.Errorf("在文件中未找到 old_text，请先调用 read_file 仔细确认文件内容和缩进")
	}
	if len(matches) > 1 && !replaceAll {
		return "", 4, fmt.Errorf("模糊匹配到了 %d 处相似代码（位于第 %d 行等），请提供更多上下行代码以精确定位",
			len(matches), matches[0].startLine+1)
	}

	// 执行行级替换 (从后往前替换以保持行号稳定)
	// 每次替换前提取匹配窗口的"基准缩进"，自动补齐到 newText 每一行，
	// 避免深层嵌套代码因模型省略缩进而格式崩塌。
	if replaceAll {
		for i := len(matches) - 1; i >= 0; i-- {
			m := matches[i]
			baseIndent := extractBaseIndent(contentLines[m.startLine:m.endLine])
			anchored := reindentText(newText, baseIndent)
			contentLines = spliceLines(contentLines, m.startLine, m.endLine, anchored)
		}
	} else {
		m := matches[0]
		baseIndent := extractBaseIndent(contentLines[m.startLine:m.endLine])
		anchored := reindentText(newText, baseIndent)
		contentLines = spliceLines(contentLines, m.startLine, m.endLine, anchored)
	}

	return strings.Join(contentLines, "\n"), 4, nil
}

// =========================================================================
// 缩进智能锚定 —— 解决 newText 丢失基准缩进导致格式崩塌的问题
// =========================================================================

// extractBaseIndent 从匹配到的原始行中提取基准缩进前缀。
// 取窗口内第一个非空行的前导空白作为该嵌套深度的缩进基准。
func extractBaseIndent(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	}
	return ""
}

// extractMinIndent 取 newText 所有非空行中前导空白最短的那个，
// 作为模型在 newText 内部使用的"相对缩进基线"。
func extractMinIndent(lines []string) string {
	var minIndent string
	first := true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if first || len(indent) < len(minIndent) {
			minIndent = indent
			first = false
		}
	}
	return minIndent
}

// reindentText 将 newText 的缩进重新锚定到 baseIndent。
// 剥掉 newText 自带的最小缩进（模型自己加的），换上匹配窗口的基准缩进，
// 从而保持块内相对缩进不变、绝对缩进与上下文一致。
func reindentText(text, baseIndent string) string {
	lines := strings.Split(text, "\n")
	minIndent := extractMinIndent(lines)

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			// 纯空行：保留为空，不填充空白前缀
			lines[i] = ""
			continue
		}
		if minIndent != "" && strings.HasPrefix(line, minIndent) {
			lines[i] = baseIndent + line[len(minIndent):]
		} else {
			lines[i] = baseIndent + strings.TrimLeft(line, " \t")
		}
	}
	return strings.Join(lines, "\n")
}

// spliceLines 将 lines[start:end] 替换为 newText（已拆分为行），
// 返回新的行切片。等价于 append(prefix, newText行..., suffix...)
func spliceLines(lines []string, start, end int, newText string) []string {
	var result []string
	result = append(result, lines[:start]...)
	if newText != "" {
		result = append(result, strings.Split(newText, "\n")...)
	}
	result = append(result, lines[end:]...)
	return result
}
