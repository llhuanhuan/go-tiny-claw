// internal/observability/exporter_log.go
package observability

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// LogExporter 将 Span 树以缩进树形格式打印到终端日志。
// 主要用于开发调试，生产环境建议使用 FileExporter + OTelExporter。
type LogExporter struct{}

// NewLogExporter 创建一个日志导出器。
func NewLogExporter() *LogExporter {
	return &LogExporter{}
}

// Export 将 Span 树格式化为缩进文本输出到 log。
func (l *LogExporter) Export(_ context.Context, rootSpan *Span) error {
	log.Println("[Tracing] 📊 Span 树结构:")
	printSpanTreeIndented(rootSpan, 0)
	return nil
}

// printSpanTreeIndented 递归打印带缩进的 Span 树。
func printSpanTreeIndented(span *Span, depth int) {
	indent := strings.Repeat("  ", depth)
	prefix := "●"
	if depth > 0 {
		prefix = "├──"
	}

	attrSummary := formatAttributes(span.Attributes)
	duration := span.EndTime.Sub(span.StartTime).Milliseconds()

	log.Printf("%s%s %s (%dms)%s", indent, prefix, span.Name, duration, attrSummary)

	for _, child := range span.Children {
		printSpanTreeIndented(child, depth+1)
	}
}

// formatAttributes 将 Attributes map 格式为一行摘要。
func formatAttributes(attrs map[string]interface{}) string {
	if len(attrs) == 0 {
		return ""
	}
	var parts []string
	for k, v := range attrs {
		val := fmt.Sprintf("%v", v)
		if len(val) > 50 {
			val = val[:50] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, val))
	}
	return " | " + strings.Join(parts, ", ")
}

// Shutdown 日志导出器无需清理。
func (l *LogExporter) Shutdown(_ context.Context) error { return nil }
