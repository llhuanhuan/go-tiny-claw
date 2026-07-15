package provider

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// RetryableProvider 是 LLMProvider 的装饰器，为 API 调用添加自动重试和指数退避。
// 适用于 429（限流）、500/503（服务端错误）等瞬时故障。
//
// 设计：装饰器模式，与 CostTracker 正交，可叠加使用。
type RetryableProvider struct {
	underlying LLMProvider
	maxRetries int           // 最大重试次数（默认 3）
	baseDelay  time.Duration // 基础退避时间（默认 1s）
}

// RetryOption 配置重试行为的函数选项。
type RetryOption func(*RetryableProvider)

// WithMaxRetries 设置最大重试次数。
func WithMaxRetries(n int) RetryOption {
	return func(r *RetryableProvider) {
		if n > 0 {
			r.maxRetries = n
		}
	}
}

// WithBaseDelay 设置基础退避时间。
func WithBaseDelay(d time.Duration) RetryOption {
	return func(r *RetryableProvider) {
		if d > 0 {
			r.baseDelay = d
		}
	}
}

// NewRetryableProvider 创建带重试能力的 Provider 装饰器。
func NewRetryableProvider(underlying LLMProvider, opts ...RetryOption) *RetryableProvider {
	r := &RetryableProvider{
		underlying: underlying,
		maxRetries: 3,
		baseDelay:  1 * time.Second,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Generate 带重试的阻塞式推理。
func (r *RetryableProvider) Generate(ctx context.Context, messages []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error) {
	var lastErr error
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		if attempt > 0 {
			delay := r.calculateDelay(attempt)
			log.Printf("[Retry] 第 %d 次重试，等待 %v...", attempt, delay)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		msg, err := r.underlying.Generate(ctx, messages, tools)
		if err == nil {
			return msg, nil
		}

		if !isRetryable(err) {
			return nil, err
		}
		lastErr = err
		log.Printf("[Retry] 可重试错误: %v (尝试 %d/%d)", err, attempt+1, r.maxRetries+1)
	}
	return nil, fmt.Errorf("重试 %d 次后仍然失败: %w", r.maxRetries, lastErr)
}

// StreamGenerate 带重试的流式推理。
func (r *RetryableProvider) StreamGenerate(ctx context.Context, messages []schema.Message, tools []schema.ToolDefinition) (<-chan StreamEvent, error) {
	var lastErr error
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		if attempt > 0 {
			delay := r.calculateDelay(attempt)
			log.Printf("[Retry] 流式第 %d 次重试，等待 %v...", attempt, delay)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		ch, err := r.underlying.StreamGenerate(ctx, messages, tools)
		if err == nil {
			return ch, nil
		}

		if !isRetryable(err) {
			return nil, err
		}
		lastErr = err
		log.Printf("[Retry] 流式可重试错误: %v (尝试 %d/%d)", err, attempt+1, r.maxRetries+1)
	}
	return nil, fmt.Errorf("流式重试 %d 次后仍然失败: %w", r.maxRetries, lastErr)
}

// calculateDelay 计算指数退避 + 随机抖动。
// 公式：baseDelay * 2^(attempt-1) + random jitter
func (r *RetryableProvider) calculateDelay(attempt int) time.Duration {
	exp := math.Pow(2, float64(attempt-1))
	base := time.Duration(float64(r.baseDelay) * exp)
	// 添加 0-50% 的随机抖动，避免雷鸣群（thundering herd）
	jitter := time.Duration(rand.Int63n(int64(base) / 2))
	return base + jitter
}

// isRetryable 判断错误是否值得重试。
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// 429 Too Many Requests
	if strings.Contains(msg, "429") || strings.Contains(msg, "rate_limit") {
		return true
	}
	// 500 Internal Server Error
	if strings.Contains(msg, "500") || strings.Contains(msg, "internal server error") {
		return true
	}
	// 502 Bad Gateway
	if strings.Contains(msg, "502") || strings.Contains(msg, "bad gateway") {
		return true
	}
	// 503 Service Unavailable
	if strings.Contains(msg, "503") || strings.Contains(msg, "service unavailable") {
		return true
	}
	// 529 (Anthropic overloaded)
	if strings.Contains(msg, "529") || strings.Contains(msg, "overloaded") {
		return true
	}
	// 连接超时/重置
	if strings.Contains(msg, "connection reset") || strings.Contains(msg, "timeout") {
		return true
	}
	return false
}
