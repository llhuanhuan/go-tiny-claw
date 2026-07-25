// internal/observability/exporter.go
package observability

import "context"

// Exporter 定义了 Trace 数据的输出目标接口。
//
// 所有导出策略（文件、Jaeger、日志）都实现此接口。
// TraceProvider 在根 Span 结束时遍历所有 Exporter 并行导出。
//
// 设计原则：
//   - 接口极简（只有 Export + Shutdown），降低接入成本
//   - 每个 Exporter 独立负责自己的错误处理，不阻塞其他 Exporter
//   - Export 调用是异步的（由 TraceProvider 在 goroutine 中调用）
type Exporter interface {
    // Export 将一棵完整的 Span 树导出到目标系统。
    // rootSpan 包含完整的层级结构（Children 递归嵌套）。
    Export(ctx context.Context, rootSpan *Span) error

    // Shutdown 优雅关闭，刷新缓冲区。
    // 引擎退出时调用，确保不丢失数据。
    Shutdown(ctx context.Context) error
}
