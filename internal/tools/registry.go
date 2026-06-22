package tools

import (
	"context"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// Registry 定义了工具的注册与分发执行接口

type Registry interface { // GetAvailableTools 返回当前系统挂载的所有可用工具的 Schema
	GetAvailableTools() []schema.ToolDefinition // Execute 实际执行模型请求的工具，并返回结果
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}

// BaseTool 是所有具体工具必须实现的通用接口type BaseTool interface {    // Name 返回工具的全局唯一名称 (大模型通过这个名字调用它)    Name() string    // Definition 返回用于提交给大模型的工具元信息和参数 JSON Schema    Definition() schema.ToolDefinition    // Execute 接收大模型吐出的 JSON 参数，执行具体业务逻辑    // 注意：参数是 json.RawMessage，反序列化由各个具体工具内部自行处理    Execute(ctx context.Context, args json.RawMessage) (string, error)}
