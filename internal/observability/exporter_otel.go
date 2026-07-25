// internal/observability/exporter_otel.go
package observability

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// OTelExporter 将 Span 树转换为 OpenTelemetry 标准格式，
// 通过 OTLP/gRPC 协议上报到 Jaeger / Tempo / SigNoz 等后端。
//
// 浏览器打开 Jaeger UI (http://localhost:16686) 即可看到甘特图。
//
// 依赖：
//
//	docker compose up -d  (启动 Jaeger all-in-one)
type OTelExporter struct {
	tracerProvider *sdktrace.TracerProvider
	tracer         oteltrace.Tracer
}

// NewOTelExporter 创建一个 OTel 导出器。
//
// 参数：
//   - ctx: 父上下文，用于控制初始化超时
//   - endpoint: Jaeger Collector 的 OTLP gRPC 地址，如 "localhost:4317"
//   - serviceName: 在 Jaeger UI 中显示的服务名，如 "go-tiny-claw"
func NewOTelExporter(ctx context.Context, endpoint string, serviceName string) (*OTelExporter, error) {
	// 1. 创建 OTLP gRPC Exporter（连接 Jaeger Collector:4317）
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(), // 本地开发用；生产环境应使用 TLS
	)
	if err != nil {
		return nil, fmt.Errorf("创建 OTLP gRPC Exporter 失败: %w", err)
	}

	// 2. 创建 Resource（标识当前服务）
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			"",
			attribute.String("service.name", serviceName),
			attribute.String("service.version", "0.1.0"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 Resource 失败: %w", err)
	}

	// 3. 创建 TracerProvider（OTel 的核心管理器）
	//    WithBatcher: 批量上报，减少网络开销（默认 5s 或 512 条触发 flush）
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(2*time.Second), // Agent 任务结束后 2s 内 flush
		),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return &OTelExporter{
		tracerProvider: tp,
		tracer:         tp.Tracer(serviceName + "/engine"),
	}, nil
}

// MustNewOTelExporter 是 NewOTelExporter 的 panic 版本，适合初始化阶段使用。
func MustNewOTelExporter(ctx context.Context, endpoint string, serviceName string) *OTelExporter {
	exp, err := NewOTelExporter(ctx, endpoint, serviceName)
	if err != nil {
		log.Fatalf("[Tracing] 创建 OTel Exporter 失败: %v", err)
	}
	return exp
}

// Export 递归遍历我们的 Span 树，将每个 Span 转换为 OTel Span。
//
// 关键：利用 OTel 的 parent context 维护父子关系 → Jaeger 甘特图自动渲染层级。
func (o *OTelExporter) Export(ctx context.Context, rootSpan *Span) error {
	o.convertSpan(ctx, rootSpan)
	return nil
}

// convertSpan 递归地将我们的 Span 映射为 OTel Span。
func (o *OTelExporter) convertSpan(ctx context.Context, span *Span) {
	// 构建 SpanStartOption
	opts := []oteltrace.SpanStartOption{
		oteltrace.WithTimestamp(span.StartTime),
		oteltrace.WithSpanKind(oteltrace.SpanKindInternal),
	}

	// 创建 OTel Span（自动从 ctx 中获取 parent）
	otelCtx, otelSpan := o.tracer.Start(ctx, span.Name, opts...)

	// 写入 Attributes
	if len(span.Attributes) > 0 {
		otelSpan.SetAttributes(o.convertAttributes(span.Attributes)...)
	}

	// 结束 Span
	otelSpan.End(oteltrace.WithTimestamp(span.EndTime))

	// 递归子 Span（传入 otelCtx 以建立父子关系）
	for _, child := range span.Children {
		o.convertSpan(otelCtx, child)
	}
}

// convertAttributes 将我们的 map[string]interface{} 转换为 OTel 强类型属性。
func (o *OTelExporter) convertAttributes(attrs map[string]interface{}) []attribute.KeyValue {
	result := make([]attribute.KeyValue, 0, len(attrs))
	for k, v := range attrs {
		switch val := v.(type) {
		case string:
			result = append(result, attribute.String(k, val))
		case int:
			result = append(result, attribute.Int(k, val))
		case int64:
			result = append(result, attribute.Int64(k, val))
		case float64:
			result = append(result, attribute.Float64(k, val))
		case bool:
			result = append(result, attribute.Bool(k, val))
		default:
			result = append(result, attribute.String(k, fmt.Sprintf("%v", val)))
		}
	}
	return result
}

// Shutdown 优雅关闭 TracerProvider，刷新所有缓冲区中的 Span。
func (o *OTelExporter) Shutdown(ctx context.Context) error {
	return o.tracerProvider.Shutdown(ctx)
}
