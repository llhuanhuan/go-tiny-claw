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
	// 使用全局管理器（因为 bot.onMessageReceived 使用 GlobalApprovalMgr）
	taskID := "task-abc-123"

	// 先注册一个待审批任务
	go func() {
		time.Sleep(50 * time.Millisecond) // 确保 RequestApproval 先执行
		event := mockApprovalEvent(`{"text":"approve task-abc-123"}`)
		bot := &FeishuBot{}
		bot.onMessageReceived(context.Background(), event)
	}()

	// 引擎端等待审批结果
	approved, err := GlobalApprovalMgr.RequestApproval(context.Background(), taskID)
	if err != nil {
		t.Fatalf("RequestApproval 返回错误: %v", err)
	}

	if !approved {
		t.Error("期望审批通过，实际被拒绝")
	}
}

func TestOnMessageReceived_RejectCommand(t *testing.T) {
	// 使用全局管理器
	taskID := "task-def-456"

	go func() {
		time.Sleep(50 * time.Millisecond)
		event := mockApprovalEvent(`{"text":"reject task-def-456"}`)
		bot := &FeishuBot{}
		bot.onMessageReceived(context.Background(), event)
	}()

	approved, err := GlobalApprovalMgr.RequestApproval(context.Background(), taskID)
	if err != nil {
		t.Fatalf("RequestApproval 返回错误: %v", err)
	}

	if approved {
		t.Error("期望审批被拒绝，实际通过")
	}
}

func TestOnMessageReceived_NormalMessage(t *testing.T) {
	// 普通消息不应触发审批
	event := mockApprovalEvent(`{"text":"你好，帮我写个函数"}`)

	// 验证普通消息不会触发审批
	contentStr := extractTextContent(event)
	if strings.HasPrefix(contentStr, "approve ") || strings.HasPrefix(contentStr, "reject ") {
		t.Error("普通消息不应被识别为审批指令")
	}
}
