// internal/engine/session.go
package engine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// Session 代表了一次持续的人机交互过程。它负责维护该会话的完整历史。
type Session struct {
	ID        string
	WorkDir   string // 该会话绑定的物理工作区
	CreatedAt time.Time
	UpdatedAt time.Time

	// 存放此 Session 中所有的用户输入、大模型回复和工具调用结果
	history []schema.Message
	mu      sync.RWMutex // 读写锁，防止并发读写历史时发生 Data Race
	// 注意：Token 计费统计已移至 observability.CostTracker + context.Session，
	// 每次 provider.Generate 调用会自动通过装饰器记录消耗。
}

func NewSession(id string, workDir string) *Session {
	return &Session{
		ID:        id,
		WorkDir:   workDir,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		history:   make([]schema.Message, 0),
	}
}

// Append 线程安全地向 Session 中追加消息
func (s *Session) Append(msgs ...schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, msgs...)
	s.UpdatedAt = time.Now()

	// 【持久化】：每次追加消息后同步写入磁盘（JSONL 格式）
	if s.WorkDir != "" {
		s.saveToDisk(msgs)
	}
}

// saveToDisk 将新追加的消息以 JSONL 格式写入磁盘。
// 使用 Append 模式（O_APPEND），不会覆盖已有数据。
// 文件路径：{WorkDir}/.claw/sessions/{ID}.jsonl
// 注意：调用方已持有 s.mu 锁，此方法不可再次加锁。
func (s *Session) saveToDisk(msgs []schema.Message) {
	sessDir := filepath.Join(s.WorkDir, ".claw", "sessions")
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		log.Printf("[Session] ⚠️ 创建会话目录失败: %v\n", err)
		return
	}

	filePath := filepath.Join(sessDir, fmt.Sprintf("%s.jsonl", s.ID))
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("[Session] ⚠️ 打开会话文件失败: %v\n", err)
		return
	}
	defer f.Close()

	writer := bufio.NewWriter(f)
	for _, msg := range msgs {
		line, err := json.Marshal(msg)
		if err != nil {
			log.Printf("[Session] ⚠️ 序列化消息失败: %v\n", err)
			continue
		}
		writer.Write(line)
		writer.WriteByte('\n')
	}
	writer.Flush()
}

// LoadFromDisk 从磁盘恢复会话历史（JSONL 格式）。
// 在 GetOrCreate 时调用，实现跨进程断点续跑。
func (s *Session) LoadFromDisk() error {
	filePath := filepath.Join(s.WorkDir, ".claw", "sessions", fmt.Sprintf("%s.jsonl", s.ID))
	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在是正常情况（新会话）
		}
		return fmt.Errorf("打开会话文件失败: %w", err)
	}
	defer f.Close()

	var loaded []schema.Message
	scanner := bufio.NewScanner(f)
	// 设置更大的缓冲区（1MB），防止超长消息行导致扫描失败
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var msg schema.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			log.Printf("[Session] ⚠️ 跳过无法解析的消息行: %v\n", err)
			continue
		}
		loaded = append(loaded, msg)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取会话文件失败: %w", err)
	}

	s.mu.Lock()
	s.history = loaded
	s.mu.Unlock()

	log.Printf("[Session] 📂 从磁盘恢复了 %d 条消息 (会话: %s)\n", len(loaded), s.ID)
	return nil
}

// ClearHistory 清空会话历史，用于 /reset 命令。
func (s *Session) ClearHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = make([]schema.Message, 0)
	s.UpdatedAt = time.Now()
}

// GetWorkingMemory 是驾驭工程的核心！
// 它不返回全量历史，而是从后往前截取消息，形成 Agent 的"短期工作记忆"。
//
// 双维度截断算法（类似限流中的滑动窗口）：
//   - limit:    最大消息条数（0 表示不限制条数）
//   - maxChars: 最大字符总数预算（0 表示不限制字符数）
//
// 两个维度取"先到先停"——哪个条件先触发就停止累积。
// 这样即使 limit 设得很宽松（比如 10 条），只要其中一条 ToolResult 有 1 万行输出，
// 字符预算也会提前触发截断，防止工作记忆超出模型上下文窗口的物理上限。
func (s *Session) GetWorkingMemory(limit int, maxChars int) []schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.history)

	// 快速路径：两个维度都不限制，全量返回
	if limit <= 0 && maxChars <= 0 {
		res := make([]schema.Message, total)
		copy(res, s.history)
		return res
	}

	// 快速路径：总消息数未超限且总字符数未超预算，全量返回
	if (limit <= 0 || total <= limit) && (maxChars <= 0 || estimateChars(s.history) <= maxChars) {
		res := make([]schema.Message, total)
		copy(res, s.history)
		return res
	}

	// ——————————————————————————————————————————
	// 核心：从后往前滑动窗口，双维度累积
	// ——————————————————————————————————————————
	usedChars := 0
	end := total // 不含 end（左闭右开 [start, end)）

	for i := total - 1; i >= 0; i-- {
		msgChars := estimateMsgChars(&s.history[i])

		// 检查条数限制：如果加上当前消息会超出 limit，停止
		if limit > 0 && (total-i) > limit {
			break
		}

		// 检查字符预算：如果加上当前消息会超出 maxChars，停止
		if maxChars > 0 && (usedChars+msgChars) > maxChars {
			break
		}

		usedChars += msgChars
		end = i
	}

	// 至少保留最后一条消息（即使它单独就超预算，否则会返回空记忆导致死循环）
	if end == total {
		end = total - 1
	}

	res := make([]schema.Message, total-end)
	copy(res, s.history[end:])

	// 【驾驭防线】：大模型 API 强制要求历史消息的连续性！
	// 如果我们截断的第一条消息恰好是一个 ToolResult (RoleUser 且含有 ToolCallID)，
	// 但发出这个请求的 ToolCall 被我们截断抛弃了，大模型 API 会直接报 400 Bad Request。
	// 因此，如果切片首条属于"孤儿"工具响应，我们必须将其强行舍弃，顺延到下一条正常的 User/Assistant 消息。
	for len(res) > 0 {
		if res[0].Role == schema.RoleUser && res[0].ToolCallID != "" {
			res = res[1:]
		} else {
			break
		}
	}

	return res
}

// estimateMsgChars 估算单条消息的字符开销（作为 Token 数的近似代理）。
// 包含 Content 正文 + 所有 ToolCalls 的 JSON Arguments 序列化长度。
func estimateMsgChars(msg *schema.Message) int {
	n := len(msg.Content)
	for _, tc := range msg.ToolCalls {
		n += len(tc.Name)
		n += len(tc.Arguments)
	}
	return n
}

// estimateChars 批量估算消息切片的总字符开销。
func estimateChars(msgs []schema.Message) int {
	total := 0
	for i := range msgs {
		total += estimateMsgChars(&msgs[i])
	}
	return total
}

// ==========================================
// 全局 Session Manager: 用于多用户/多终端隔离
// ==========================================

type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

// 通过一个带有 sync.RWMutex 锁的 Map 实现了高并发下的物理隔离
var GlobalSessionMgr = &SessionManager{
	sessions: make(map[string]*Session),
}

// GetOrCreate 获取或创建一个会话。
// 如果磁盘上有该会话的持久化文件（.claw/sessions/{id}.jsonl），会自动恢复历史。
func (sm *SessionManager) GetOrCreate(id string, workDir string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sess, exists := sm.sessions[id]; exists {
		return sess
	}
	sess := NewSession(id, workDir)
	// 尝试从磁盘恢复会话历史（断点续跑）
	if err := sess.LoadFromDisk(); err != nil {
		log.Printf("[Session] ⚠️ 恢复会话历史失败: %v\n", err)
	}
	sm.sessions[id] = sess
	return sess
}
