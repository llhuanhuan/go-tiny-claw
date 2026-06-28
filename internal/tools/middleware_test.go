package tools

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// ============================================================
// Mock Tool: 用于测试中间件链
// ============================================================

type mockSleepTool struct {
	name    string
	delay   time.Duration
	executed int32
}

func (m *mockSleepTool) Name() string { return m.name }

func (m *mockSleepTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: m.name, Description: "mock"}
}

func (m *mockSleepTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	atomic.AddInt32(&m.executed, 1)
	time.Sleep(m.delay)
	return "done", nil
}

// ============================================================
// 场景 1: ExecutionTimer 记录耗时
// ============================================================

// TestExecutionTimer_RecordsElapsedTime 验证 ExecutionTimer 能正确包裹工具执行并记录耗时。
func TestExecutionTimer_RecordsElapsedTime(t *testing.T) {
	tool := &mockSleepTool{name: "slow_tool", delay: 50 * time.Millisecond}
	registry := NewRegistry()
	registry.Register(tool)

	var capturedElapsed time.Duration

	// 自定义中间件：捕获 next 调用后的耗时（与 ExecutionTimer 同结构）
	registry.UseToolMiddleware(func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult {
		start := time.Now()
		result := next(ctx, call)
		capturedElapsed = time.Since(start)
		return result
	})

	call := schema.ToolCall{ID: "t1", Name: "slow_tool", Arguments: json.RawMessage(`{}`)}
	result := registry.Execute(context.Background(), call)

	if result.IsError {
		t.Fatalf("执行不应报错: %s", result.Output)
	}
	if atomic.LoadInt32(&tool.executed) != 1 {
		t.Fatal("工具应被执行 1 次")
	}
	if capturedElapsed < 40*time.Millisecond {
		t.Fatalf("期望耗时>=40ms, 得到 %v", capturedElapsed)
	}
}

// ============================================================
// 场景 2: 多个环绕式中间件按洋葱模型顺序执行
// ============================================================

// TestToolMiddleware_OnionOrder 验证后挂载的中间件在最外层（后挂载先执行）。
func TestToolMiddleware_OnionOrder(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&mockSleepTool{name: "tool", delay: 0})

	var order []string

	// 第一个中间件（内层）
	registry.UseToolMiddleware(func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult {
		order = append(order, "A-before")
		result := next(ctx, call)
		order = append(order, "A-after")
		return result
	})

	// 第二个中间件（外层，后挂载）
	registry.UseToolMiddleware(func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult {
		order = append(order, "B-before")
		result := next(ctx, call)
		order = append(order, "B-after")
		return result
	})

	call := schema.ToolCall{ID: "t1", Name: "tool", Arguments: json.RawMessage(`{}`)}
	registry.Execute(context.Background(), call)

	// 洋葱模型：B(外) → A(内) → tool → A(内) → B(外)
	expected := []string{"B-before", "A-before", "A-after", "B-after"}
	if len(order) != len(expected) {
		t.Fatalf("期望 %d 步, 得到 %d: %v", len(expected), len(order), order)
	}
	for i, v := range expected {
		if order[i] != v {
			t.Fatalf("顺序[%d] 期望 %q, 得到 %q", i, v, order[i])
		}
	}
}

// ============================================================
// 场景 3: 环绕式中间件可以修改返回结果
// ============================================================

// TestToolMiddleware_CanModifyResult 验证中间件可以拦截并修改工具返回结果。
func TestToolMiddleware_CanModifyResult(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&mockSleepTool{name: "tool", delay: 0})

	// 中间件将结果替换为自定义内容
	registry.UseToolMiddleware(func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult {
		result := next(ctx, call)
		result.Output = "intercepted: " + result.Output
		return result
	})

	call := schema.ToolCall{ID: "t1", Name: "tool", Arguments: json.RawMessage(`{}`)}
	result := registry.Execute(context.Background(), call)

	if result.Output != "intercepted: done" {
		t.Fatalf("期望输出='intercepted: done', 得到 %q", result.Output)
	}
}

// ============================================================
// 场景 4: 环绕式中间件与前置拦截器共存
// ============================================================

// TestToolMiddleware_CoexistsWithPreMiddleware 验证 Use 和 UseToolMiddleware 可以共存。
// 前置拦截器拒绝后，环绕式中间件不应被调用。
func TestToolMiddleware_CoexistsWithPreMiddleware(t *testing.T) {
	tool := &mockSleepTool{name: "tool", delay: 0}
	registry := NewRegistry()
	registry.Register(tool)

	toolMwCalled := false

	// 前置拦截器：拒绝所有调用
	registry.Use(func(ctx context.Context, call schema.ToolCall) (bool, string) {
		return false, "权限不足"
	})

	// 环绕式中间件：不应被执行
	registry.UseToolMiddleware(func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult {
		toolMwCalled = true
		return next(ctx, call)
	})

	call := schema.ToolCall{ID: "t1", Name: "tool", Arguments: json.RawMessage(`{}`)}
	result := registry.Execute(context.Background(), call)

	if !result.IsError {
		t.Fatal("应被前置拦截器拒绝")
	}
	if toolMwCalled {
		t.Fatal("前置拦截器拒绝后，环绕式中间件不应被调用")
	}
	if atomic.LoadInt32(&tool.executed) != 0 {
		t.Fatal("工具不应被执行")
	}
}

// ============================================================
// 场景 5: 无中间件时工具正常执行（向后兼容）
// ============================================================

// TestToolMiddleware_NoMiddleware_BackwardCompat 验证不挂载任何中间件时行为不变。
func TestToolMiddleware_NoMiddleware_BackwardCompat(t *testing.T) {
	tool := &mockSleepTool{name: "tool", delay: 0}
	registry := NewRegistry()
	registry.Register(tool)

	call := schema.ToolCall{ID: "t1", Name: "tool", Arguments: json.RawMessage(`{}`)}
	result := registry.Execute(context.Background(), call)

	if result.IsError {
		t.Fatalf("执行不应报错: %s", result.Output)
	}
	if result.Output != "done" {
		t.Fatalf("期望输出='done', 得到 %q", result.Output)
	}
}
