package provider

import (
	"context"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// LLMProvider 定义了与大模型通信的统一契约
type LLMProvider interface {
	// Generate 接收当前的上下文历史、可用工具列表，并发起一次大模型推理
	// 注意：当 availableTools 为 nil 或长度为 0 时，代表引擎正在强制模型进入慢思考阶段。
	Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error)
}
