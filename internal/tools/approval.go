package tools

import (
	"context"
	"fmt"
	"strings"
)

// ConsoleApprovalHandler 通过终端 stdin/stdout 与用户交互进行审批。
// 适用于 CLI 模式：在终端中显示待审批命令，等待用户输入 y/n。
type ConsoleApprovalHandler struct{}

func NewConsoleApprovalHandler() *ConsoleApprovalHandler {
	return &ConsoleApprovalHandler{}
}

func (h *ConsoleApprovalHandler) RequestApproval(ctx context.Context, taskID string, reason string) (bool, error) {
	fmt.Printf("\n⚠️  命令需要人工审批:\n")
	fmt.Printf("   命令: %s\n", taskID)
	fmt.Printf("   原因: %s\n", reason)
	fmt.Printf("   是否允许执行？(y/n): ")

	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(strings.ToLower(input))

	return input == "y" || input == "yes", nil
}

// FeishuApprovalHandler 通过飞书审批流进行审批。
// 包装了 feishu.GlobalApprovalMgr，适配 tools.ApprovalHandler 接口。
type FeishuApprovalHandler struct {
	requestFunc func(ctx context.Context, taskID string) (bool, error)
}

// NewFeishuApprovalHandler 创建飞书审批处理器。
// requestFunc 是对 feishu.GlobalApprovalMgr.RequestApproval 的闭包包装，
// 避免 tools 包直接依赖 feishu 包。
func NewFeishuApprovalHandler(requestFunc func(ctx context.Context, taskID string) (bool, error)) *FeishuApprovalHandler {
	return &FeishuApprovalHandler{requestFunc: requestFunc}
}

func (h *FeishuApprovalHandler) RequestApproval(ctx context.Context, taskID string, reason string) (bool, error) {
	return h.requestFunc(ctx, taskID)
}
