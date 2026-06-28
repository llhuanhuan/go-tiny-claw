package schema

import "encoding/json"

// Role 定义消息的角色，这是与大模型沟通的基石
type Role string

const (
	RoleSystem    Role = "system"    // 系统提示词：确立 Agent 的性格与红线
	RoleUser      Role = "user"      // 用户输入 / 工具执行的返回结果 (Observation)
	RoleAssistant Role = "assistant" // 模型的输出：包含推理(Reasoning)或工具调用(ToolCall)
)

type Message struct {
	Role       Role        `json:"role"`
	Content    string      `json:"content"`              // 存放纯文本内容
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"` // 如果模型决定调用工具，此字段将被填充 (支持并行调用多个工具)
	ToolCallID string      `json:"tool_call_id,omitempty"`
	Usage      *TokenUsage `json:"usage,omitempty"` // API 返回的 Token 消耗（仅 assistant 消息携带）
}

// TokenUsage 记录单次 API 调用的 Token 消耗，用于自适应压缩决策。
// 各大模型 API（OpenAI / Anthropic / 智谱 / DeepSeek）均在 Response 中返回此数据。
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`     // 输入 Token 数（系统提示 + 历史消息 + 工具定义）
	CompletionTokens int `json:"completion_tokens"` // 输出 Token 数（模型生成的内容 + 工具调用参数）
	TotalTokens      int `json:"total_tokens"`      // 总 Token 数
}

// ToolCall 代表模型请求调用某个具体的工具
type ToolCall struct {
	ID   string `json:"id"`   // 工具调用的唯一 ID
	Name string `json:"name"` // 想要调用的工具名称 (例如 "bash")
	// Arguments 存放 JSON 参数。使用 RawMessage 是为了延迟解析，将解析责任交给具体的工具
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult 代表工具在本地执行完毕后返回的物理结果
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Output     string `json:"output"`   // 工具执行的控制台输出或报错堆栈
	IsError    bool   `json:"is_error"` // 标记是否失败，供后续的驾驭工程进行错误自愈
}

// ToolDefinition 描述了一个大模型可以调用的工具元信息 (供模型理解工具有什么用)
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"` // 对应 JSON Schema
}

// Usage 记录了单次大模型 API 调用的 Token 消耗
type Usage struct {
	PromptTokens     int `json: "prompt_tokens"`     // 输入的 Token 数量
	CompletionTokens int `json: "completion_tokens"` // 产生的 Token 数量
}
