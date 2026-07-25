// internal/observability/trace_provider.go
package observability

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// TraceProvider 是 Tracing 子系统的入口，管理所有 Exporter 的生命周期。
//
// 职责：
//  1. 管理 Exporter 列表（支持多个同时生效）
//  2. 在根 Span 结束时触发所有 Exporter 并行导出
//  3. 引擎退出时优雅关闭所有 Exporter
//
// 使用示例：
//
//	provider := NewTraceProvider(
//	    NewFileExporter(workDir, sessionID),
//	    MustNewOTelExporter(ctx, "localhost:4317"),
//	)
//	defer provider.Shutdown(ctx)
//
//	// ... Agent 运行 ...
//
//	rootSpan.EndSpan()
//	provider.Export(ctx, rootSpan)
type TraceProvider struct {
	exporters []Exporter
}

// NewTraceProvider 创建一个新的 TraceProvider，注册一组 Exporter。
func NewTraceProvider(exporters ...Exporter) *TraceProvider {
	return &TraceProvider{exporters: exporters}
}

// Export 将 Span 树并行导出到所有已注册的 Exporter。
// 每个 Exporter 在独立的 goroutine 中运行，通过 WaitGroup 保证全部完成后才返回。
// 返回聚合错误（errors.Join），调用方可选择忽略：_ = tp.Export(ctx, rootSpan)
func (tp *TraceProvider) Export(ctx context.Context, rootSpan *Span) error {
	if len(tp.exporters) == 0 {
		return nil
	}

	var (
		wg   sync.WaitGroup
		errs []error
		mu   sync.Mutex
	)

	for _, exp := range tp.exporters {
		wg.Add(1)
		go func(e Exporter) {
			defer wg.Done()
			if err := e.Export(ctx, rootSpan); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("exporter %T: %w", e, err))
				mu.Unlock()
			}
		}(exp)
	}
	wg.Wait() // 等待所有 Exporter 完成，防止引擎退出时数据丢失

	return errors.Join(errs...)
}

// Shutdown 优雅关闭所有 Exporter，刷新各自的缓冲区。
// 返回聚合错误（errors.Join），调用方可选择忽略：_ = tp.Shutdown(ctx)
func (tp *TraceProvider) Shutdown(ctx context.Context) error {
	var errs []error
	for _, exp := range tp.exporters {
		if err := exp.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown %T: %w", exp, err))
		}
	}
	return errors.Join(errs...)
}
