// internal/tools/subagent_manager.go
package tools

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// SubagentSnapshot 是 SubagentTask 的只读快照，供外部安全读取。
type SubagentSnapshot struct {
	ID        string
	Prompt    string
	Done      bool
	Summary   string
	Error     error
	StartTime time.Time
	EndTime   time.Time
	Notified  bool // 是否已通知过主循环（防止重复注入）
}

// SubagentTask 代表一个后台运行的子智能体。
// 设计理念与 TaskManager.BackgroundTask 一致：
//   - Spawn:  立即返回 ID，不阻塞调用者
//   - Get:    读取快照（线程安全）
//   - done:   chan 通知完成
type SubagentTask struct {
	ID        string
	Prompt    string
	Summary   string
	Error     error
	StartTime time.Time
	EndTime   time.Time
	Done      bool
	Notified  bool

	done chan struct{}
	mu   sync.RWMutex
}

// Snapshot 返回线程安全的只读快照
func (t *SubagentTask) Snapshot() SubagentSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return SubagentSnapshot{
		ID:        t.ID,
		Prompt:    t.Prompt,
		Done:      t.Done,
		Summary:   t.Summary,
		Error:     t.Error,
		StartTime: t.StartTime,
		EndTime:   t.EndTime,
		Notified:  t.Notified,
	}
}

// SubagentManager 管理所有异步子智能体的生命周期。
//
// 设计与 TaskManager 并列：
//   - TaskManager   → 管理 OS 进程（bash 后台任务）
//   - SubagentManager → 管理 Go goroutine（子智能体）
//
// 全局单例，所有工具共享同一实例。
type SubagentManager struct {
	mu    sync.RWMutex
	tasks map[string]*SubagentTask
	idSeq atomic.Int64
}

var globalSubagentManager = &SubagentManager{
	tasks: make(map[string]*SubagentTask),
}

// GetSubagentManager 返回全局 SubagentManager 单例。
func GetSubagentManager() *SubagentManager {
	return globalSubagentManager
}

// Spawn 启动一个异步子智能体，立即返回 ID。
//
// 内部启动 goroutine 执行 runner.RunSub()，完成后自动更新状态。
// 调用者无需等待，后续通过 Get() 或 injectSubagentNotifications 获取结果。
// parentCtx 用于取消传播：当父 context 取消时，子智能体也会收到取消信号。
func (m *SubagentManager) Spawn(parentCtx context.Context, runner AgentRunner, prompt string, readOnlyRegistry Registry, reporter interface{}) string {
	id := fmt.Sprintf("sa_%d", m.idSeq.Add(1))

	task := &SubagentTask{
		ID:        id,
		Prompt:    prompt,
		StartTime: time.Now(),
		done:      make(chan struct{}),
	}

	m.mu.Lock()
	m.tasks[id] = task
	m.mu.Unlock()

	log.Printf("[SubagentManager] 🚀 子智能体 %s 已入队，任务: %s\n", id, truncatePrompt(prompt, 60))

	// 创建可取消的子 context：主循环取消时子智能体也会收到取消信号
	// 但子智能体可以独立于主循环完成（Done channel 用于通知完成）
	subCtx, subCancel := context.WithCancel(parentCtx)

	// 在独立 goroutine 中执行，不阻塞调用者
	go func() {
		defer close(task.done)
		defer subCancel() // 确保释放 context 资源

		summary, err := runner.RunSub(subCtx, prompt, readOnlyRegistry, reporter)

		task.mu.Lock()
		task.EndTime = time.Now()
		task.Done = true
		if err != nil {
			task.Error = err
			log.Printf("[SubagentManager] ❌ 子智能体 %s 执行失败: %v\n", id, err)
		} else {
			task.Summary = summary
			log.Printf("[SubagentManager] ✅ 子智能体 %s 已完成，返回 %d 字节摘要\n", id, len(summary))
		}
		task.mu.Unlock()
	}()

	return id
}

// Get 返回指定 ID 的快照。第二个返回值指示是否存在。
func (m *SubagentManager) Get(id string) (SubagentSnapshot, bool) {
	m.mu.RLock()
	task, exists := m.tasks[id]
	m.mu.RUnlock()
	if !exists {
		return SubagentSnapshot{}, false
	}
	return task.Snapshot(), true
}

// List 返回所有子智能体的快照列表。
func (m *SubagentManager) List() []SubagentSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshots := make([]SubagentSnapshot, 0, len(m.tasks))
	for _, task := range m.tasks {
		snapshots = append(snapshots, task.Snapshot())
	}
	return snapshots
}

// MarkNotified 标记指定 ID 的子智能体已被通知，防止重复注入。
func (m *SubagentManager) MarkNotified(id string) {
	m.mu.RLock()
	task, exists := m.tasks[id]
	m.mu.RUnlock()
	if !exists {
		return
	}
	task.mu.Lock()
	task.Notified = true
	task.mu.Unlock()
}

// Remove 删除已完成的子智能体记录。
func (m *SubagentManager) Remove(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, exists := m.tasks[id]
	if !exists {
		return false
	}
	task.mu.RLock()
	done := task.Done
	task.mu.RUnlock()
	if !done {
		return false
	}
	delete(m.tasks, id)
	return true
}

// truncatePrompt 截断提示词用于日志输出
func truncatePrompt(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
