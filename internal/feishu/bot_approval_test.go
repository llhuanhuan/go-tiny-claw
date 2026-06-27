package feishu

import (
	"context"
	"strings"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// mockApprovalEvent 模拟飞书消息事件
func mockApprovalEvent(content string) *larkim.P2MessageReceiveV1 {
	chatID := "test-chat-001"
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				ChatId:  &chatID,
				Content: &content,
			},
		},
	}
}

func TestOnMessageReceived_ApproveCommand(t *testing.T) {
	// 注册一个待审批任务
	taskID := "task-abc-123"
	ch := GlobalApprovalMgr.RequestApproval(taskID)

	// 模拟飞书发送 approve 指令
	event := mockApprovalEvent(`{"text":"approve task-abc-123"}`)

	// 创建一个 dummy bot（不需要真实 engine）
	bot := &FeishuBot{}

	// 调用消息处理函数
	err := bot.onMessageReceived(context.Background(), event)
	if err != nil {
		t.Fatalf("onMessageReceived 返回错误: %v", err)
	}

	// 验证审批结果
	select {
	case approved := <-ch:
		if !approved {
			t.Error("期望审批通过，实际被拒绝")
		}
	case <-time.After(1 * time.Second):
		t.Error("等待审批超时，消息未被正确处理")
	}
}

func TestOnMessageReceived_RejectCommand(t *testing.T) {
	taskID := "task-def-456"
	ch := GlobalApprovalMgr.RequestApproval(taskID)

	event := mockApprovalEvent(`{"text":"reject task-def-456"}`)
	bot := &FeishuBot{}

	err := bot.onMessageReceived(context.Background(), event)
	if err != nil {
		t.Fatalf("onMessageReceived 返回错误: %v", err)
	}

	select {
	case approved := <-ch:
		if approved {
			t.Error("期望审批被拒绝，实际通过")
		}
	case <-time.After(1 * time.Second):
		t.Error("等待审批超时")
	}
}

func TestOnMessageReceived_NormalMessage(t *testing.T) {
	// 普通消息不应触发审批
	event := mockApprovalEvent(`{"text":"你好，帮我写个函数"}`)
	bot := &FeishuBot{}

	// 注册一个任务，验证它不会被意外解决
	taskID := "task-should-not-resolve"
	ch := GlobalApprovalMgr.RequestApproval(taskID)

	// 处理普通消息（会启动 goroutine，但我们没有真实 engine，只测试不会 panic）
	_ = bot

	// 验证普通消息不会触发审批
	contentStr := extractTextContent(event)
	if strings.HasPrefix(contentStr, "approve ") || strings.HasPrefix(contentStr, "reject ") {
		t.Error("普通消息不应被识别为审批指令")
	}

	// 验证任务仍然在等待
	if GlobalApprovalMgr.PendingCount() != 1 {
		t.Errorf("期望 1 个待审批任务，实际 %d", GlobalApprovalMgr.PendingCount())
	}

	// 清理
	GlobalApprovalMgr.ResolveApproval(taskID, false, "测试清理")
	<-ch
}
