// internal/observability/exporter_file.go
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileExporter 将 Span 树序列化为 JSON 文件，保存到本地工作区。
// 这是最原始的导出方式，保留用于离线分析和调试。
//
// 文件路径: {workDir}/.claw/traces/trace_{sessionID}_{timestamp}.json
type FileExporter struct {
	workDir   string
	sessionID string
}

// NewFileExporter 创建一个文件导出器。
func NewFileExporter(workDir, sessionID string) *FileExporter {
	return &FileExporter{workDir: workDir, sessionID: sessionID}
}

// Export 将 Span 树序列化为美化的 JSON 文件。
func (f *FileExporter) Export(_ context.Context, rootSpan *Span) error {
	traceDir := filepath.Join(f.workDir, ".claw", "traces")
	if err := os.MkdirAll(traceDir, 0755); err != nil {
		return fmt.Errorf("创建 traces 目录失败: %w", err)
	}

	filename := filepath.Join(traceDir, fmt.Sprintf("trace_%s_%d.json", f.sessionID, time.Now().Unix()))

	data, err := json.MarshalIndent(rootSpan, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Span 树失败: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("写入 trace 文件失败: %w", err)
	}

	return nil
}

// Shutdown 文件导出器无需清理。
func (f *FileExporter) Shutdown(_ context.Context) error { return nil }
