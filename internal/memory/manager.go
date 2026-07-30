// internal/memory/manager.go
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// LLMProvider 是记忆系统需要的 LLM 接口（与 provider.LLMProvider 签名一致）。
type LLMProvider interface {
	Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error)
}

// Manager 是分层记忆系统的核心管理器。
// 负责中期摘要和长期事实的提取、存储、检索。
type Manager struct {
	storage   *Storage
	provider  LLMProvider
	config    MemoryConfig
	sessionID string

	// 计数器：跟踪对话轮次，决定何时触发摘要/提取
	turnCount atomic.Int32

	// 异步控制
	mu      sync.Mutex
	running bool // 防止并发触发

	// 会话状态
	lastSummaryTurn int // 上次摘要到第几轮
	lastExtractTurn int // 上次提取到第几轮
}

// NewManager 创建记忆管理器。
func NewManager(workDir, sessionID string, provider LLMProvider, config MemoryConfig) *Manager {
	return &Manager{
		storage:   NewStorage(workDir),
		provider:  provider,
		config:    config,
		sessionID: sessionID,
	}
}

// IncrementTurn 增加轮次计数，并检查是否需要触发摘要或提取。
// 应在每轮对话结束后调用（异步触发，不阻塞）。
func (m *Manager) IncrementTurn(ctx context.Context, session HistoryReader) {
	if !m.config.Enabled {
		return
	}

	turn := int(m.turnCount.Add(1))

	// 异步触发摘要
	if turn-m.lastSummaryTurn >= m.config.SummarizeEveryN {
		go m.triggerSummarize(ctx, session)
	}

	// 异步触发事实提取
	if turn-m.lastExtractTurn >= m.config.ExtractEveryN {
		go m.triggerExtractFacts(ctx, session)
	}
}

// HistoryReader 用于读取会话历史的接口。
type HistoryReader interface {
	GetAllMessages() []schema.Message
}

// GetSummary 获取中期摘要，用于注入上下文。
func (m *Manager) GetSummary() string {
	summaries, err := m.storage.LoadSummaries(m.sessionID)
	if err != nil || len(summaries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## 对话历史摘要\n\n")
	for _, s := range summaries {
		sb.WriteString(fmt.Sprintf("- [第%d-%d轮] %s\n", s.TurnStart, s.TurnEnd, s.Content))
	}
	return sb.String()
}

// GetLongTermFacts 获取长期记忆，用于注入上下文。
func (m *Manager) GetLongTermFacts() string {
	facts, err := m.storage.LoadFacts(m.sessionID)
	if err != nil || len(facts) == 0 {
		return ""
	}

	// 按类型分组
	grouped := make(map[FactType][]Fact)
	for _, f := range facts {
		grouped[f.Type] = append(grouped[f.Type], f)
	}

	var sb strings.Builder
	sb.WriteString("## 长期记忆\n\n")

	typeLabels := map[FactType]string{
		FactUserPreference: "用户偏好",
		FactProjectState:   "项目状态",
		FactKeyDecision:    "关键决策",
		FactPendingTask:    "待办事项",
		FactToolUsage:      "工具使用",
	}

	for ftype, label := range typeLabels {
		items := grouped[ftype]
		if len(items) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("### %s\n", label))
		for _, f := range items {
			sb.WriteString(fmt.Sprintf("- %s\n", f.Content))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// triggerSummarize 触发摘要生成（异步调用）。
func (m *Manager) triggerSummarize(ctx context.Context, session HistoryReader) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()

	messages := session.GetAllMessages()
	if len(messages) == 0 {
		return
	}

	// 获取上次摘要后的消息
	startTurn := m.lastSummaryTurn
	if startTurn >= len(messages) {
		return
	}
	newMessages := messages[startTurn:]

	log.Printf("[Memory] 🔄 触发摘要生成: 第%d-%d轮 (%d条消息)\n", startTurn+1, len(messages), len(newMessages))

	summary, err := m.summarize(ctx, newMessages)
	if err != nil {
		log.Printf("[Memory] ⚠️ 摘要生成失败: %v\n", err)
		return
	}

	s := Summary{
		Timestamp: time.Now(),
		TurnStart: startTurn + 1,
		TurnEnd:   len(messages),
		Content:   summary,
	}

	if err := m.storage.SaveSummary(m.sessionID, s); err != nil {
		log.Printf("[Memory] ⚠️ 摘要保存失败: %v\n", err)
		return
	}

	m.lastSummaryTurn = len(messages)
	log.Printf("[Memory] ✅ 摘要已保存: 第%d-%d轮\n", s.TurnStart, s.TurnEnd)
}

// summarize 调用 LLM 生成对话摘要。
func (m *Manager) summarize(ctx context.Context, messages []schema.Message) (string, error) {
	// 格式化消息历史
	var sb strings.Builder
	for _, msg := range messages {
		role := "用户"
		if msg.Role == schema.RoleAssistant {
			role = "助手"
		} else if msg.Role == schema.RoleSystem {
			continue // 跳过系统消息
		}
		content := msg.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		sb.WriteString(fmt.Sprintf("[%s] %s\n", role, content))
	}

	prompt := fmt.Sprintf(`请将以下对话历史压缩为简洁的摘要，保留关键信息：
- 用户的主要需求和目标
- 讨论的技术方案和决策
- 遇到的问题和解决方案
- 未完成的任务或待办事项

要求：
- 使用中文
- 简洁明了，不超过 500 字
- 使用条目式结构

对话历史：
%s`, sb.String())

	resp, err := m.provider.Generate(ctx, []schema.Message{
		{Role: schema.RoleUser, Content: prompt},
	}, nil)
	if err != nil {
		return "", fmt.Errorf("LLM 调用失败: %w", err)
	}

	return resp.Content, nil
}

// triggerExtractFacts 触发长期记忆提取（异步调用）。
func (m *Manager) triggerExtractFacts(ctx context.Context, session HistoryReader) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()

	messages := session.GetAllMessages()
	if len(messages) == 0 {
		return
	}

	// 获取上次提取后的消息
	startTurn := m.lastExtractTurn
	if startTurn >= len(messages) {
		return
	}
	newMessages := messages[startTurn:]

	log.Printf("[Memory] 🔄 触发事实提取: 第%d-%d轮 (%d条消息)\n", startTurn+1, len(messages), len(newMessages))

	facts, err := m.extractFacts(ctx, newMessages)
	if err != nil {
		log.Printf("[Memory] ⚠️ 事实提取失败: %v\n", err)
		return
	}

	for _, fact := range facts {
		fact.UpdatedAt = time.Now()
		fact.SessionID = m.sessionID

		if err := m.storage.SaveFact(m.sessionID, fact); err != nil {
			log.Printf("[Memory] ⚠️ 事实保存失败: %v\n", err)
		}
	}

	m.lastExtractTurn = len(messages)
	log.Printf("[Memory] ✅ 已提取 %d 条事实\n", len(facts))
}

// extractFacts 调用 LLM 提取关键事实。
func (m *Manager) extractFacts(ctx context.Context, messages []schema.Message) ([]Fact, error) {
	// 格式化消息历史
	var sb strings.Builder
	for _, msg := range messages {
		role := "用户"
		if msg.Role == schema.RoleAssistant {
			role = "助手"
		} else if msg.Role == schema.RoleSystem {
			continue
		}
		content := msg.Content
		if len(content) > 300 {
			content = content[:300] + "..."
		}
		sb.WriteString(fmt.Sprintf("[%s] %s\n", role, content))
	}

	prompt := fmt.Sprintf(`从以下对话中提取关键事实，分类为：
- user_preference: 用户偏好（工具、语言、风格、习惯）
- project_state: 项目状态（架构、技术栈、进度、配置）
- key_decision: 关键决策（为什么选择这个方案）
- pending_task: 待办事项（未完成的任务）
- tool_usage: 工具使用模式（常用工具、使用习惯）

要求：
- 每条事实简洁明确
- 只提取有价值的信息，不要提取显而易见的内容
- 以 JSON 数组格式返回

输出格式：
[{"type": "user_preference", "content": "...", "confidence": 0.9}]

对话：
%s`, sb.String())

	resp, err := m.provider.Generate(ctx, []schema.Message{
		{Role: schema.RoleUser, Content: prompt},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("LLM 调用失败: %w", err)
	}

	// 解析 JSON 响应
	var facts []Fact
	content := resp.Content

	// 尝试提取 JSON 部分（LLM 可能会添加额外文本）
	jsonStart := strings.Index(content, "[")
	jsonEnd := strings.LastIndex(content, "]")
	if jsonStart >= 0 && jsonEnd > jsonStart {
		content = content[jsonStart : jsonEnd+1]
	}

	if err := json.Unmarshal([]byte(content), &facts); err != nil {
		return nil, fmt.Errorf("解析事实 JSON 失败: %w (响应: %s)", err, resp.Content)
	}

	return facts, nil
}

// PromoteToGlobal 将一条会话级记忆提升为全局记忆。
func (m *Manager) PromoteToGlobal(fact Fact) error {
	fact.SessionID = "" // 清除会话标记
	return m.storage.SaveGlobalFact(fact)
}

// GetTurnCount 获取当前轮次。
func (m *Manager) GetTurnCount() int {
	return int(m.turnCount.Load())
}
