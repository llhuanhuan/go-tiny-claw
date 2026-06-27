package feishu

import (
	"context"
	"testing"
	"time"
)

func TestApprovalManager_BasicFlow(t *testing.T) {
	// 创建独立的测试实例
	mgr := NewApprovalManager()
	defer mgr.Stop()

	// 模拟引擎端：注册待审批任务
	go func() {
		time.Sleep(100 * time.Millisecond) // 模拟网络延迟
		mgr.ResolveApproval("test-task-001", true, "测试通过")
	}()

	// 引擎端阻塞等待结果
	approved, err := mgr.RequestApproval(context.Background(), "test-task-001")
	if err != nil {
		t.Fatalf("RequestApproval 返回错误: %v", err)
	}

	if !approved {
		t.Error("期望审批通过，实际被拒绝")
	}

	// 验证任务已从 pending 中移除
	if mgr.PendingCount() != 0 {
		t.Errorf("期望 0 个待审批任务，实际 %d", mgr.PendingCount())
	}
}

func TestApprovalManager_Reject(t *testing.T) {
	mgr := NewApprovalManager()
	defer mgr.Stop()

	go func() {
		mgr.ResolveApproval("test-task-002", false, "测试拒绝")
	}()

	approved, err := mgr.RequestApproval(context.Background(), "test-task-002")
	if err != nil {
		t.Fatalf("RequestApproval 返回错误: %v", err)
	}

	if approved {
		t.Error("期望审批被拒绝，实际通过")
	}
}

func TestApprovalManager_Timeout(t *testing.T) {
	// 测试超时机制（使用较短的超时时间）
	// 注意：DefaultApprovalTimeout 是 5 分钟，这里我们测试 context 超时
	mgr := NewApprovalManager()
	defer mgr.Stop()

	// 使用 context 超时来测试（100ms）
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	approved, err := mgr.RequestApproval(ctx, "test-task-timeout")
	elapsed := time.Since(start)

	if err == nil {
		t.Error("期望超时错误，实际无错误")
	}

	if approved {
		t.Error("期望超时返回 false，实际返回 true")
	}

	// 验证超时时间合理
	if elapsed > 200*time.Millisecond {
		t.Errorf("超时时间过长: %v", elapsed)
	}
}

func TestApprovalManager_ContextCancel(t *testing.T) {
	// 测试 context 取消
	mgr := NewApprovalManager()
	defer mgr.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	approved, err := mgr.RequestApproval(ctx, "test-task-cancel")
	elapsed := time.Since(start)

	if err == nil {
		t.Error("期望 context 取消错误，实际无错误")
	}

	if approved {
		t.Error("期望取消返回 false，实际返回 true")
	}

	// 验证取消时间合理
	if elapsed > 200*time.Millisecond {
		t.Errorf("取消时间过长: %v", elapsed)
	}
}

func TestApprovalManager_ResolveNonExistent(t *testing.T) {
	mgr := NewApprovalManager()
	defer mgr.Stop()

	// 解决一个不存在的任务，不应 panic
	mgr.ResolveApproval("non-existent", true, "无意义")
}

func TestApprovalManager_Concurrent(t *testing.T) {
	mgr := NewApprovalManager()
	defer mgr.Stop()

	// 并发注册多个任务
	for i := 0; i < 10; i++ {
		go func(id int) {
			go func() {
				time.Sleep(10 * time.Millisecond)
				mgr.ResolveApproval("task-"+string(rune(id+'0')), true, "并发测试")
			}()
			mgr.RequestApproval(context.Background(), "task-"+string(rune(id+'0')))
		}(i)
	}

	time.Sleep(500 * time.Millisecond) // 等待所有协程完成
	if mgr.PendingCount() != 0 {
		t.Errorf("期望 0 个待审批任务，实际 %d", mgr.PendingCount())
	}
}
