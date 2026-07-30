// internal/memory/types.go
package memory

import "time"

// FactType 表示长期记忆的事实类型。
type FactType string

const (
	FactUserPreference FactType = "user_preference" // 用户偏好
	FactProjectState   FactType = "project_state"   // 项目状态
	FactKeyDecision    FactType = "key_decision"    // 关键决策
	FactPendingTask    FactType = "pending_task"    // 待办事项
	FactToolUsage      FactType = "tool_usage"      // 工具使用模式
)

// Fact 表示一条长期记忆。
type Fact struct {
	Type       FactType  `json:"type"`
	Content    string    `json:"content"`
	Confidence float64   `json:"confidence"` // 0.0-1.0 置信度
	UpdatedAt  time.Time `json:"updated_at"`
	SessionID  string    `json:"session_id,omitempty"` // 来源会话（全局记忆时为空）
}

// Summary 表示一段对话的摘要。
type Summary struct {
	Timestamp time.Time `json:"timestamp"`
	TurnStart int       `json:"turn_start"` // 起始轮次
	TurnEnd   int       `json:"turn_end"`   // 结束轮次
	Content   string    `json:"content"`    // 摘要内容
}

// MemoryConfig 记忆系统配置。
type MemoryConfig struct {
	Enabled          bool `yaml:"enabled"`            // 是否启用记忆系统
	SummarizeEveryN  int  `yaml:"summarize_every_n"`  // 每 N 轮对话触发摘要
	ExtractEveryN    int  `yaml:"extract_every_n"`    // 每 N 轮对话触发事实提取
	MaxSummaryTokens int  `yaml:"max_summary_tokens"` // 摘要最大 token 数
	MaxFacts         int  `yaml:"max_facts"`          // 长期记忆最大条数
}

// DefaultConfig 返回默认配置。
func DefaultConfig() MemoryConfig {
	return MemoryConfig{
		Enabled:          true,
		SummarizeEveryN:  10,
		ExtractEveryN:    15,
		MaxSummaryTokens: 2000,
		MaxFacts:         50,
	}
}
