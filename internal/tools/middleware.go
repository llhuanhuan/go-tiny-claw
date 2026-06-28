package tools

import (
	"context"
	"log"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// ExecutionTimer 是一个环绕式中间件，用于记录每个工具在本地物理执行的真实耗时。
//
// 工作原理：
//   - 在 next(call) 前记录 startTime
//   - next(call) 执行真实的工具逻辑（bash 编译、文件读写等）
//   - next(call) 返回后计算 time.Since(startTime)
//   - 将耗时日志打印到控制台
//
// 挂载方式（在 main.go 中）：
//
//	registry.UseToolMiddleware(tools.ExecutionTimer())
func ExecutionTimer() ToolMiddlewareFunc {
	return func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult {
		start := time.Now()

		// 真正执行工具（可能耗时数毫秒到数分钟）
		result := next(ctx, call)

		elapsed := time.Since(start)

		if result.IsError {
			log.Printf("[Timer] ⏱️ 工具 %-15s 执行失败 | 耗时: %v\n", call.Name, elapsed)
		} else {
			log.Printf("[Timer] ⏱️ 工具 %-15s 执行成功 | 耗时: %v | 输出: %d 字节\n",
				call.Name, elapsed, len(result.Output))
		}

		return result
	}
}
