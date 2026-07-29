package ilink

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lhuan/go-tiny-claw/internal/engine"
)

// =============================================================================
// ILinkReporter: 通过 iLink Bot API 推送引擎状态
// =============================================================================

// ILinkReporter 实现 engine.Reporter 接口，将 Agent 引擎的运行状态
// 通过 iLink Bot API 实时推送到个人微信聊天窗口。
type ILinkReporter struct {
	bot    *ILinkBot // 关联的 Bot 实例（用于调用 sendMessage）
	userID string    // 接收者 OpenID
}

// sendMsg 通过 iLink Bot API 发送文本消息。
// 自动处理超长消息的分段发送（按 rune 计算，UTF-8 安全）。
func (r *ILinkReporter) sendMsg(text string) {
	if text == "" {
		return
	}

	// 超长消息分段发送（按 rune 计算，UTF-8 安全）
	runes := []rune(text)
	if len(runes) > defaultMaxMessageLen {
		for start := 0; start < len(runes); start += defaultMaxMessageLen {
			end := start + defaultMaxMessageLen
			if end > len(runes) {
				end = len(runes)
			}
			if err := r.bot.sendMessage(r.userID, string(runes[start:end])); err != nil {
				fmt.Printf("[iLink] 发送消息失败: %v\n", err)
			}
		}
		return
	}
	if err := r.bot.sendMessage(r.userID, text); err != nil {
		fmt.Printf("[iLink] 发送消息失败: %v\n", err)
	}
}

// truncateArgs 截断过长的工具参数用于展示。
func truncateArgs(args string, maxLen int) string {
	if utf8.RuneCountInString(args) <= maxLen {
		return args
	}
	runes := []rune(args)
	return string(runes[:maxLen]) + "..."
}

// OnThinking 当模型开始慢思考时调用。
func (r *ILinkReporter) OnThinking(ctx context.Context) {
	r.sendMsg("🤔 正在深度思考中...")
}

// OnStreamDelta 当流式推理产生文本增量时调用。
// iLink Bot 不支持消息编辑（无 PATCH API），因此缓冲到 OnMessage 一次性发送。
func (r *ILinkReporter) OnStreamDelta(ctx context.Context, delta string, isThinking bool) {
	// iLink Bot 不支持消息编辑，流式增量不单独推送
}

// OnToolCall 当模型决定调用工具时调用。
func (r *ILinkReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	displayArgs := truncateArgs(args, 200)
	displayArgs = strings.ReplaceAll(displayArgs, "`", "'")
	r.sendMsg(fmt.Sprintf("🛠️ 执行工具: %s\n参数: %s", toolName, displayArgs))
}

// OnToolResult 当工具执行完毕时调用。
func (r *ILinkReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		r.sendMsg(fmt.Sprintf("⚠️ 工具 %s 执行出错: %s", toolName, result))
	} else {
		r.sendMsg(fmt.Sprintf("✅ 工具 %s 执行成功", toolName))
	}
}

// OnMessage 当模型输出最终纯文本回答时调用。
func (r *ILinkReporter) OnMessage(ctx context.Context, content string) {
	r.sendMsg(content)
}

// 编译时类型检查：确保 ILinkReporter 实现了 Reporter 接口
var _ engine.Reporter = (*ILinkReporter)(nil)
