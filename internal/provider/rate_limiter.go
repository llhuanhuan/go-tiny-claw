package provider

import (
	"context"
	"sync"
	"time"
)

// RateLimiter 控制 LLM API 的并发调用数，防止触发 Provider 限流。
// 使用令牌桶算法：固定速率补充令牌，每次 API 调用消耗一个令牌。
type RateLimiter struct {
	tokens   chan struct{}
	mu       sync.Mutex
	interval time.Duration // 令牌补充间隔
	stopCh   chan struct{}
}

// NewRateLimiter 创建一个 API 限流器。
//   - maxConcurrent: 最大并发数（令牌桶容量）
//   - interval: 令牌补充间隔（0 表示不限速，仅限制并发数）
func NewRateLimiter(maxConcurrent int, interval time.Duration) *RateLimiter {
	if maxConcurrent <= 0 {
		maxConcurrent = 5 // 默认最多 5 个并发 API 调用
	}

	rl := &RateLimiter{
		tokens:   make(chan struct{}, maxConcurrent),
		interval: interval,
		stopCh:   make(chan struct{}),
	}

	// 初始化令牌桶：填满令牌
	for i := 0; i < maxConcurrent; i++ {
		rl.tokens <- struct{}{}
	}

	// 如果设置了补充间隔，启动补充协程
	if interval > 0 {
		go rl.refillLoop()
	}

	return rl
}

// Acquire 获取一个令牌（阻塞直到可用或 context 取消）
func (rl *RateLimiter) Acquire(ctx context.Context) error {
	select {
	case <-rl.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-rl.stopCh:
		return nil // 已停止，放行
	}
}

// Release 归还一个令牌
func (rl *RateLimiter) Release() {
	select {
	case rl.tokens <- struct{}{}:
	default: // 桶满时忽略（防御性编程）
	}
}

// Stop 停止限流器的补充协程
func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
}

func (rl *RateLimiter) refillLoop() {
	ticker := time.NewTicker(rl.interval)
	defer ticker.Stop()

	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			// 补充一个令牌（非阻塞，桶满时跳过）
			select {
			case rl.tokens <- struct{}{}:
			default:
			}
		}
	}
}
