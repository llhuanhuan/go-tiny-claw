// Package feishu 提供飞书机器人的长连接（WebSocket）集成与 Reporter 实现。
//
// 架构角色：
//   - FeishuBot：通过 WebSocket 长连接接收飞书事件，无需公网 IP/域名
//   - FeishuReporter：实现 engine.Reporter 接口，通过飞书 API 实时推送引擎状态
//
// 长连接（WebSocket）模式 vs Webhook 模式：
//   - 无需公网 IP 或域名，本地即可开发调试
//   - SDK 内置加密鉴权，无需手动处理签名验证
//   - 启动程序即监听事件，零部署成本
//
// 飞书平台配置（必须按顺序操作）：
//  1. 在「事件与回调 → 事件配置」中选择「使用长连接接收事件」
//  2. 添加事件订阅，勾选 im.message.receive_v1（接收消息）
//  3. 先启动本地程序，再点击「保存」配置
//
// 并发模型：每个收到的消息在独立 goroutine 中运行 Agent 任务，
// 消息处理必须在 3 秒内返回，否则飞书会超时重推。
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/lhuan/go-tiny-claw/internal/engine"
)

// FeishuBot 封装飞书机器人的长连接配置与核心业务流。
type FeishuBot struct {
	client    *lark.Client   // API 客户端（用于发送消息）
	appID     string
	appSecret string
	engine    *engine.AgentEngine
	wsClient  *larkws.Client // WebSocket 长连接客户端
}

// NewFeishuBot 创建一个基于长连接的飞书 Bot。
// 必需环境变量：FEISHU_APP_ID, FEISHU_APP_SECRET。
func NewFeishuBot(eng *engine.AgentEngine) *FeishuBot {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")

	if appID == "" || appSecret == "" {
		log.Fatal("请设置 FEISHU_APP_ID 和 FEISHU_APP_SECRET 环境变量")
	}

	log.Printf("[Feishu] ✅ FEISHU_APP_ID=%s", appID)
	log.Printf("[Feishu] 连接模式: WebSocket 长连接（无需公网 IP）")

	client := lark.NewClient(appID, appSecret)

	bot := &FeishuBot{
		client:    client,
		appID:     appID,
		appSecret: appSecret,
		engine:    eng,
	}

	// 构建事件分发器，注册消息接收处理函数
	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(bot.onMessageReceived).
		OnP2ChatAccessEventBotP2pChatEnteredV1(bot.onChatEntered)

	// 构建 WebSocket 长连接客户端
	bot.wsClient = larkws.NewClient(appID, appSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)

	return bot
}

// Start 启动飞书长连接，阻塞直到连接断开或 context 取消。
// 内置自动重连和心跳保活。
func (b *FeishuBot) Start(ctx context.Context) error {
	log.Println("[Feishu] 正在建立 WebSocket 长连接...")
	if err := b.wsClient.Start(ctx); err != nil {
		return fmt.Errorf("飞书长连接启动失败: %w", err)
	}
	return nil
}

// onChatEntered 处理用户首次进入机器人 P2P 对话的事件。
// 飞书平台在用户首次打开与机器人的私聊窗口时会推送此事件，无需特殊处理，仅记录日志。
func (b *FeishuBot) onChatEntered(ctx context.Context, event *larkim.P2ChatAccessEventBotP2pChatEnteredV1) error {
	if event.Event == nil {
		return nil
	}
	chatID := safeStrDeref(event.Event.ChatId)
	operatorID := "unknown"
	if event.Event.OperatorId != nil {
		operatorID = safeStrDeref(event.Event.OperatorId.OpenId)
	}
	log.Printf("[Feishu] 用户进入机器人会话, chat_id: %s, operator_id: %s", chatID, operatorID)
	return nil
}

// safeStrDeref 安全解引用 *string，nil 时返回 "(nil)"。
func safeStrDeref(s *string) string {
	if s == nil {
		return "(nil)"
	}
	return *s
}

// onMessageReceived 处理飞书推送的消息事件。
// 必须在 3 秒内返回（飞书超时限制），因此将 Agent 任务丢到 goroutine 异步执行。
func (b *FeishuBot) onMessageReceived(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	contentStr := extractTextContent(event)
	if contentStr == "" {
		return nil
	}

	chatID := *event.Event.Message.ChatId
	log.Printf("[Feishu] 收到会话 %s 消息: %s", chatID, contentStr)

	// 异步启动 Agent，不阻塞事件回调
	go b.runAgent(chatID, contentStr)

	return nil
}

// runAgent 在独立 goroutine 中运行 Agent 任务。
func (b *FeishuBot) runAgent(chatID string, prompt string) {
	reporter := &FeishuReporter{
		client: b.client,
		chatID: chatID,
	}

	// 设置较长的超时（飞书消息 API 调用本身有超时）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	session := engine.GlobalSessionMgr.GetOrCreate("feishu:"+chatID, b.engine.WorkDir)
	if err := b.engine.Run(ctx, session, prompt, reporter); err != nil {
		reporter.sendMsg(fmt.Sprintf("❌ Agent 运行失败: %v", err))
	}
}

// =============================================================================
// FeishuReporter: 通过飞书消息 API 推送引擎状态
// =============================================================================

type FeishuReporter struct {
	client *lark.Client
	chatID string
}

func (r *FeishuReporter) sendMsg(text string) {
	content := map[string]string{"text": text}
	b, _ := json.Marshal(content)

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(r.chatID).
			MsgType("text").
			Content(string(b)).
			Build()).
		Build()

	_, err := r.client.Im.V1.Message.Create(context.Background(), req)
	if err != nil {
		log.Printf("[Feishu] 发送消息失败: %v", err)
	}
}

func (r *FeishuReporter) OnThinking(ctx context.Context) {
	r.sendMsg("🤔 模型正在慢思考...")
}

func (r *FeishuReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	if len(args) > 300 {
		args = args[:300] + "..."
	}
	r.sendMsg(fmt.Sprintf("🛠️ 执行工具 `%s`\n参数: %s", toolName, args))
}

func (r *FeishuReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		r.sendMsg(fmt.Sprintf("⚠️ `%s` 执行出错: %s", toolName, result))
	} else {
		r.sendMsg(fmt.Sprintf("✅ `%s` 执行成功", toolName))
	}
}

func (r *FeishuReporter) OnMessage(ctx context.Context, content string) {
	const maxLen = 30000
	if len(content) > maxLen {
		content = content[:maxLen] + "\n\n... (内容过长已截断)"
	}
	r.sendMsg(content)
}

var _ engine.Reporter = (*FeishuReporter)(nil)

// =============================================================================
// 消息文本提取
// =============================================================================

func extractTextContent(event *larkim.P2MessageReceiveV1) string {
	if event.Event == nil || event.Event.Message == nil || event.Event.Message.Content == nil {
		return ""
	}
	raw := *event.Event.Message.Content

	var content struct{ Text string `json:"text"` }
	if json.Unmarshal([]byte(raw), &content) == nil && content.Text != "" {
		return content.Text
	}
	// 回退解析
	raw = strings.TrimPrefix(raw, `{"text":"`)
	raw = strings.TrimSuffix(raw, `"}`)
	return strings.TrimSpace(raw)
}
