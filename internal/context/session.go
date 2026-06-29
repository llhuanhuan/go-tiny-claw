package context

import (
	"sync"
	"time"
)

// Session 是一个轻量级的计费会话，用于 CostTracker 跟踪每个请求的 Token 消耗和成本。
// 它独立于 engine.Session（后者负责对话历史和上下文管理），
// 专门服务于 observability 层的计费逻辑。
type Session struct {
	ID        string
	CreatedAt time.Time

	mu                    sync.Mutex
	TotalPromptTokens     int
	TotalCompletionTokens int
	TotalCostCNY          float64
}

func NewSession(id string) *Session {
	return &Session{
		ID:        id,
		CreatedAt: time.Now(),
	}
}

// TotalTokens 返回当前累计的总 Token 数（Prompt + Completion）。
func (s *Session) TotalTokens() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.TotalPromptTokens + s.TotalCompletionTokens
}

// RecordUsage 累加本次 API 调用的 Token 消耗和成本。
func (s *Session) RecordUsage(prompt int, completion int, cost float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalPromptTokens += prompt
	s.TotalCompletionTokens += completion
	s.TotalCostCNY += cost
}
