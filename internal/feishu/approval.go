package feishu

import (
	"log"
	"sync"
)

// ApprovalManager 管理人工审批请求。
// 当引擎遇到需要人工确认的操作时，会挂起并等待飞书端的审批指令。
type ApprovalManager struct {
	mu       sync.Mutex
	pending  map[string]chan bool // taskID -> 审批结果通道
}

// GlobalApprovalMgr 全局审批管理器，供引擎和飞书 Bot 共同访问
var GlobalApprovalMgr = &ApprovalManager{
	pending: make(map[string]chan bool),
}

// RequestApproval 注册一个待审批任务，返回一个阻塞通道。
// 引擎协程调用此方法后会阻塞，直到飞书端调用 ResolveApproval 唤醒。
func (m *ApprovalManager) RequestApproval(taskID string) chan bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan bool, 1)
	m.pending[taskID] = ch
	log.Printf("[Approval] 任务 %s 等待人工审批...", taskID)
	return ch
}

// ResolveApproval 飞书端收到审批口令后调用此方法，唤醒挂起的引擎协程。
func (m *ApprovalManager) ResolveApproval(taskID string, approved bool, reason string) {
	m.mu.Lock()
	ch, exists := m.pending[taskID]
	if exists {
		delete(m.pending, taskID)
	}
	m.mu.Unlock()

	if !exists {
		log.Printf("[Approval] 未找到待审批任务: %s", taskID)
		return
	}

	ch <- approved
	close(ch)
	log.Printf("[Approval] 任务 %s 审批结果: approved=%v, 原因: %s", taskID, approved, reason)
}

// PendingCount 返回当前待审批任务数量
func (m *ApprovalManager) PendingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}
