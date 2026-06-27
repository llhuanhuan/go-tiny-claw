package feishu

import (
	"testing"
	"time"
)

func TestApprovalManager_BasicFlow(t *testing.T) {
	// 创建独立的测试实例，不用全局变量
	mgr := &ApprovalManager{
		pending: make(map[string]chan bool),
	}

	// 模拟引擎端：注册待审批任务
	ch := mgr.RequestApproval("test-task-001")
	if ch == nil {
		t.Fatal("RequestApproval 返回了 nil channel")
	}

	// 验证待审批数量
	if mgr.PendingCount() != 1 {
		t.Fatalf("期望 1 个待审批任务，实际 %d", mgr.PendingCount())
	}

	// 模拟飞书端：审批通过
	go func() {
		time.Sleep(100 * time.Millisecond) // 模拟网络延迟
		mgr.ResolveApproval("test-task-001", true, "测试通过")
	}()

	// 引擎端阻塞等待结果
	approved := <-ch
	if !approved {
		t.Error("期望审批通过，实际被拒绝")
	}

	// 验证任务已从 pending 中移除
	if mgr.PendingCount() != 0 {
		t.Errorf("期望 0 个待审批任务，实际 %d", mgr.PendingCount())
	}
}

func TestApprovalManager_Reject(t *testing.T) {
	mgr := &ApprovalManager{
		pending: make(map[string]chan bool),
	}

	ch := mgr.RequestApproval("test-task-002")

	go func() {
		mgr.ResolveApproval("test-task-002", false, "测试拒绝")
	}()

	approved := <-ch
	if approved {
		t.Error("期望审批被拒绝，实际通过")
	}
}

func TestApprovalManager_ResolveNonExistent(t *testing.T) {
	mgr := &ApprovalManager{
		pending: make(map[string]chan bool),
	}

	// 解决一个不存在的任务，不应 panic
	mgr.ResolveApproval("non-existent", true, "无意义")
}

func TestApprovalManager_Concurrent(t *testing.T) {
	mgr := &ApprovalManager{
		pending: make(map[string]chan bool),
	}

	// 并发注册多个任务
	for i := 0; i < 100; i++ {
		go func(id int) {
			ch := mgr.RequestApproval("task-" + string(rune(id)))
			go func() {
				mgr.ResolveApproval("task-"+string(rune(id)), true, "并发测试")
			}()
			<-ch
		}(i)
	}

	time.Sleep(500 * time.Millisecond) // 等待所有协程完成
	if mgr.PendingCount() != 0 {
		t.Errorf("期望 0 个待审批任务，实际 %d", mgr.PendingCount())
	}
}
