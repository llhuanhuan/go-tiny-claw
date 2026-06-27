package feishu

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	// DefaultApprovalTimeout 默认审批超时时间
	DefaultApprovalTimeout = 5 * time.Minute
)

// pendingApproval 待审批任务信息
type pendingApproval struct {
	ch        chan bool
	createdAt time.Time
	taskID    string
}

// ApprovalManager 管理人工审批请求。
// 当引擎遇到需要人工确认的操作时，会挂起并等待飞书端的审批指令。
// 支持超时自动取消，防止协程泄漏。
type ApprovalManager struct {
	mu       sync.Mutex
	pending  map[string]*pendingApproval // taskID -> 待审批任务
	stopCh   chan struct{}                // 停止清理协程
}

// GlobalApprovalMgr 全局审批管理器，供引擎和飞书 Bot 共同访问
var GlobalApprovalMgr = NewApprovalManager()

// NewApprovalManager 创建审批管理器并启动后台清理协程
func NewApprovalManager() *ApprovalManager {
	m := &ApprovalManager{
		pending: make(map[string]*pendingApproval),
		stopCh:  make(chan struct{}),
	}
	// 启动后台清理协程，定期清理过期的审批请求
	go m.cleanupLoop()
	return m
}

// RequestApproval 注册一个待审批任务，返回一个阻塞通道。
// 引擎协程调用此方法后会阻塞，直到：
// 1. 飞书端调用 ResolveApproval 唤醒
// 2. 超时自动取消（默认 5 分钟）
// 3. context 被取消
func (m *ApprovalManager) RequestApproval(ctx context.Context, taskID string) (bool, error) {
	ch := make(chan bool, 1)

	m.mu.Lock()
	m.pending[taskID] = &pendingApproval{
		ch:        ch,
		createdAt: time.Now(),
		taskID:    taskID,
	}
	m.mu.Unlock()

	log.Printf("[Approval] 任务 %s 等待人工审批（超时: %v）...", taskID, DefaultApprovalTimeout)

	// 等待审批结果、超时或 context 取消
	select {
	case approved := <-ch:
		return approved, nil
	case <-time.After(DefaultApprovalTimeout):
		// 超时清理
		m.cleanupTask(taskID)
		log.Printf("[Approval] 任务 %s 审批超时，默认拒绝", taskID)
		return false, fmt.Errorf("审批超时")
	case <-ctx.Done():
		// context 取消清理
		m.cleanupTask(taskID)
		log.Printf("[Approval] 任务 %s 审批被取消: %v", taskID, ctx.Err())
		return false, ctx.Err()
	}
}

// ResolveApproval 飞书端收到审批口令后调用此方法，唤醒挂起的引擎协程。
func (m *ApprovalManager) ResolveApproval(taskID string, approved bool, reason string) {
	m.mu.Lock()
	pending, exists := m.pending[taskID]
	if exists {
		delete(m.pending, taskID)
	}
	m.mu.Unlock()

	if !exists {
		log.Printf("[Approval] 未找到待审批任务: %s", taskID)
		return
	}

	// 非阻塞发送，防止 channel 已关闭时 panic
	select {
	case pending.ch <- approved:
		close(pending.ch)
	default:
		// channel 已满或已关闭，忽略
	}
	log.Printf("[Approval] 任务 %s 审批结果: approved=%v, 原因: %s", taskID, approved, reason)
}

// cleanupTask 清理指定任务
func (m *ApprovalManager) cleanupTask(taskID string) {
	m.mu.Lock()
	if pending, exists := m.pending[taskID]; exists {
		delete(m.pending, taskID)
		close(pending.ch)
	}
	m.mu.Unlock()
}

// cleanupLoop 后台清理协程，定期清理过期的审批请求
func (m *ApprovalManager) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.cleanupExpired()
		case <-m.stopCh:
			return
		}
	}
}

// cleanupExpired 清理所有过期的审批请求
func (m *ApprovalManager) cleanupExpired() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for taskID, pending := range m.pending {
		if now.Sub(pending.createdAt) > DefaultApprovalTimeout {
			delete(m.pending, taskID)
			close(pending.ch)
			log.Printf("[Approval] 任务 %s 审批过期，自动清理", taskID)
		}
	}
}

// Stop 停止后台清理协程
func (m *ApprovalManager) Stop() {
	close(m.stopCh)
}

// PendingCount 返回当前待审批任务数量
func (m *ApprovalManager) PendingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}
