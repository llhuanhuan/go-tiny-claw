package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// BaseTool 是所有具体工具必须实现的通用接口
type BaseTool interface {
	// Name 返回工具的全局唯一名称 (大模型通过这个名字调用它)
	Name() string
	// Definition 返回用于提交给大模型的工具元信息和参数 JSON Schema
	Definition() schema.ToolDefinition
	// Execute 接收大模型吐出的 JSON 参数，执行具体业务逻辑
	// // 注意：参数是 json.RawMessage，反序列化由各个具体工具内部自行处理
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// MiddlewareFunc 定义了前置拦截器的签名。
// 它在工具执行前运行，决定是否放行。
// 适用于权限检查、参数校验等"门卫"场景。
type MiddlewareFunc func(ctx context.Context, call schema.ToolCall) (allowed bool, rejectReason string)

// ToolHandler 是工具实际执行函数的签名，供环绕式中间件调用 next 时使用。
type ToolHandler func(ctx context.Context, call schema.ToolCall) schema.ToolResult

// ToolMiddlewareFunc 定义了环绕式中间件的签名（经典 Decorator 模式）。
// 它可以包裹整个工具执行过程：在 next(call) 前后插入逻辑（如计时、日志、重试）。
//
// 使用示例：
//
//	func TimerMiddleware(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult {
//	    start := time.Now()
//	    result := next(call)              // 真正执行工具
//	    log.Printf("耗时: %v", time.Since(start))
//	    return result
//	}
type ToolMiddlewareFunc func(ctx context.Context, call schema.ToolCall, next ToolHandler) schema.ToolResult

// Registry 定义了工具的注册与分发接口
type Registry interface {
	// Register 挂载一个新的工具到系统中
	Register(tool BaseTool)

	// GetAvailableTools 返回当前系统挂载的所有工具的 Schema，供 Main Loop 交给 Provider
	GetAvailableTools() []schema.ToolDefinition

	// Execute 实际路由并执行模型请求的工具调用
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult

	// Use 挂载前置拦截器（门卫模式：approve/reject）
	Use(mw MiddlewareFunc)

	// UseToolMiddleware 挂载环绕式中间件（装饰器模式：包裹整个执行过程）
	// 执行顺序：后挂载的先执行（类似洋葱模型）
	UseToolMiddleware(mw ToolMiddlewareFunc)
}

// registryImpl 是 Registry 接口的默认实现
type registryImpl struct {
	// 使用 map 以工具的 Name 作为 Key 进行快速 O(1) 路由查找
	tools map[string]BaseTool
	// 保存挂载的前置拦截器链
	middlewares []MiddlewareFunc
	// 保存挂载的环绕式中间件链
	toolMiddlewares []ToolMiddlewareFunc
}

func NewRegistry() Registry {
	return &registryImpl{
		tools:       make(map[string]BaseTool),
		middlewares: make([]MiddlewareFunc, 0),
	}
}

func (r *registryImpl) Register(tool BaseTool) {
	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		log.Printf("[Warning] 工具 '%s' 已经被注册，将被覆盖。\n", name)
	}
	r.tools[name] = tool
	log.Printf("[Registry] 成功挂载工具: %s\n", name)
}

func (r *registryImpl) GetAvailableTools() []schema.ToolDefinition {
	var defs []schema.ToolDefinition
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	return defs
}

func (r *registryImpl) Use(mv MiddlewareFunc) {
	r.middlewares = append(r.middlewares, mv)
}

func (r *registryImpl) UseToolMiddleware(mw ToolMiddlewareFunc) {
	r.toolMiddlewares = append(r.toolMiddlewares, mw)
}

func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	// 1. 路由查找：如果在注册表中找不到该工具，这是模型产生了幻觉，直接向模型抛出错误
	tool, exists := r.tools[call.Name]
	if !exists {
		errMsg := fmt.Sprintf("Error: 系统中不存在名为 '%s' 的工具。", call.Name)
		return schema.ToolResult{
			ToolCallID: call.ID,
			Output:     errMsg,
			IsError:    true, // 标记为错误，模型看到后会尝试纠正
		}
	}

	// 2. 【核心防御】在执行底层逻辑前，依次运行所有的前置 Middleware
	for _, mv := range r.middlewares {
		allowed, reason := mv(ctx, call)
		if !allowed {
			log.Printf("[Registry] ⚠️ 工具 %s 被 Middleware 拦截: %s\n", call.Name, reason)
			return schema.ToolResult{
				ToolCallID: call.ID,
				Output:     fmt.Sprintf("执行被系统拦截。原因: %s", reason),
				IsError:    true, // 必须返回 Error，强制大模型阅读拒绝理由
			}
		}
	}

	// 3. 构建环绕式中间件链（洋葱模型：后挂载的先执行）
	//    最内层是真正的工具执行逻辑
	handler := r.buildToolHandler(ctx, tool)

	// 4. 执行整个中间件链
	return handler(ctx, call)
}

// buildToolHandler 构建环绕式中间件链。
// 从最内层（真实执行）开始，逐层向外包裹。
// 执行顺序：middleware[N-1](最外层) → ... → middleware[0](最内层) → tool.Execute
func (r *registryImpl) buildToolHandler(ctx context.Context, tool BaseTool) ToolHandler {
	// 最内层：真实的工具执行逻辑
	handler := func(ctx context.Context, call schema.ToolCall) schema.ToolResult {
		output, err := tool.Execute(ctx, call.Arguments)
		if err != nil {
			return schema.ToolResult{
				ToolCallID: call.ID,
				Output:     fmt.Sprintf("Error executing %s: %v", call.Name, err),
				IsError:    true,
			}
		}
		return schema.ToolResult{
			ToolCallID: call.ID,
			Output:     output,
			IsError:    false,
		}
	}

	// 从前往后逐层包裹：后挂载的中间件自然成为最外层
	// middleware[0] 包裹 tool → middleware[1] 包裹 middleware[0] → ...
	for i := 0; i < len(r.toolMiddlewares); i++ {
		mw := r.toolMiddlewares[i]
		inner := handler // 捕获当前 handler
		handler = func(c context.Context, call schema.ToolCall) schema.ToolResult {
			return mw(c, call, inner)
		}
	}

	return handler
}
