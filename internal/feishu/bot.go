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
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/lhuan/go-tiny-claw/internal/engine"
)

// =============================================================================
// 1. Context 传递机制：解决并发 Reporter 的提取
// =============================================================================

// reporterKey 定义 Context 中存放 Reporter 的专属键
type reporterKey struct{}

// ContextWithReporter 将专属的 Reporter 封入上下文，供底层 Middleware 提取
func ContextWithReporter(ctx context.Context, r engine.Reporter) context.Context {
	return context.WithValue(ctx, reporterKey{}, r)
}

// ReporterFromContext 供底层的 Middleware 提取专属的 Reporter（如审批卡片推送）
func ReporterFromContext(ctx context.Context) engine.Reporter {
	if r, ok := ctx.Value(reporterKey{}).(engine.Reporter); ok {
		return r
	}
	return nil
}

// =============================================================================
// 2. 飞书 Bot 核心调度器
// =============================================================================

// AgentEngineFactory 允许每次收到消息时，根据 Session 动态创建引擎实例，
// 实现 per-session 的计费隔离（CostTracker 物理隔离）。
type AgentEngineFactory func(session *engine.Session) *engine.AgentEngine

// FeishuBot 封装飞书机器人的长连接配置与核心业务流。
type FeishuBot struct {
	client    *lark.Client // API 客户端（用于发送消息）
	appID     string
	appSecret string
	workDir   string              // 工作区路径
	factory   AgentEngineFactory  // 工厂模式：per-session 创建引擎
	wsClient  *larkws.Client      // WebSocket 长连接客户端
	botName   string              // 机器人名称（从飞书 API 自动获取）

	// 任务控制：追踪每个会话的运行中任务，支持 /stop 中断
	runningTasks   map[string]context.CancelFunc
	runningTasksMu sync.Mutex
}

// NewFeishuBot 创建一个基于长连接的飞书 Bot（工厂模式）。
// factory 在每次收到消息时为当前会话动态创建引擎实例，实现 per-session 计费隔离。
// appID 和 appSecret 由调用方从配置中获取后传入。
func NewFeishuBot(factory AgentEngineFactory, workDir string, appID, appSecret string) *FeishuBot {
	if appID == "" || appSecret == "" {
		log.Fatal("飞书配置缺失：app_id 和 app_secret 不能为空")
	}

	log.Printf("[Feishu] ✅ APP_ID=%s", appID)
	log.Printf("[Feishu] 连接模式: WebSocket 长连接（无需公网 IP）")

	client := lark.NewClient(appID, appSecret)

	bot := &FeishuBot{
		client:       client,
		appID:        appID,
		appSecret:    appSecret,
		workDir:      workDir,
		factory:      factory,
		runningTasks: make(map[string]context.CancelFunc),
	}

	// 启动时自动获取机器人名称
	bot.fetchBotName()

	// 从环境变量读取加密配置（生产环境必须设置）
	encryptKey := os.Getenv("FEISHU_ENCRYPT_KEY")
	verifyToken := os.Getenv("FEISHU_VERIFY_TOKEN")

	// 构建事件分发器，注册消息接收处理函数
	eventHandler := dispatcher.NewEventDispatcher(verifyToken, encryptKey).
		OnP2MessageReceiveV1(bot.onMessageReceived).
		OnP2ChatAccessEventBotP2pChatEnteredV1(bot.onChatEntered).
		OnP2MessageReadV1(bot.onMessageRead)

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

// onMessageRead 处理消息已读事件，静默忽略。
func (b *FeishuBot) onMessageRead(ctx context.Context, event *larkim.P2MessageReadV1) error {
	return nil
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

	// 【命令拦截】：检查是否为控制命令
	switch {
	case contentStr == "/stop" || contentStr == "停止":
		b.runningTasksMu.Lock()
		cancel, exists := b.runningTasks[chatID]
		b.runningTasksMu.Unlock()
		if exists {
			cancel()
			b.sendToChat(chatID, "⏹️ 已中断当前任务")
			log.Printf("[Feishu] 会话 %s: 任务已中断", chatID)
		} else {
			b.sendToChat(chatID, "当前没有运行中的任务")
		}
		return nil

	case contentStr == "/reset" || contentStr == "重置":
		// 先中断正在运行的任务
		b.runningTasksMu.Lock()
		if cancel, exists := b.runningTasks[chatID]; exists {
			cancel()
			delete(b.runningTasks, chatID)
		}
		b.runningTasksMu.Unlock()
		// 清空会话历史
		sessionID := "feishu:" + chatID
		sess := engine.GlobalSessionMgr.GetOrCreate(sessionID, b.workDir)
		sess.ClearHistory()
		b.sendToChat(chatID, "🔄 会话已重置，历史已清空")
		log.Printf("[Feishu] 会话 %s: 会话已重置", chatID)
		return nil
	}

	// 【审批拦截】：检查是否为人工审批口令
	if strings.HasPrefix(contentStr, "approve ") {
		taskID := strings.TrimPrefix(contentStr, "approve ")
		taskID = strings.TrimSpace(taskID)
		GlobalApprovalMgr.ResolveApproval(taskID, true, "人类管理员已批准操作")
		log.Printf("[Feishu] 会话 %s: ✅ 已为任务 %s 发放批准", chatID, taskID)
		return nil
	}

	if strings.HasPrefix(contentStr, "reject ") {
		taskID := strings.TrimPrefix(contentStr, "reject ")
		taskID = strings.TrimSpace(taskID)
		GlobalApprovalMgr.ResolveApproval(taskID, false, "人类管理员已拒绝操作")
		log.Printf("[Feishu] 会话 %s: ❌ 已为任务 %s 发放拒绝", chatID, taskID)
		return nil
	}

	// 异步启动 Agent，不阻塞事件回调
	go b.runAgent(chatID, contentStr)

	return nil
}

// sendToChat 发送文本消息到指定会话（Bot 自用）。
func (b *FeishuBot) sendToChat(chatID string, text string) {
	content := map[string]string{"text": text}
	data, _ := json.Marshal(content)
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(string(data)).
			Build()).
		Build()
	if _, err := b.client.Im.V1.Message.Create(context.Background(), req); err != nil {
		log.Printf("[Feishu] 发送消息失败: %v", err)
	}
}

// runAgent 在独立 goroutine 中运行 Agent 任务。
// 通过工厂模式为当前会话创建专属引擎，并将 Reporter 注入 Context。
func (b *FeishuBot) runAgent(chatID string, prompt string) {
	reporter := &FeishuReporter{
		client: b.client,
		chatID: chatID,
	}

	// 设置较长的超时（飞书消息 API 调用本身有超时）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 注册 cancel 函数，支持 /stop 中断
	b.runningTasksMu.Lock()
	b.runningTasks[chatID] = cancel
	b.runningTasksMu.Unlock()
	defer func() {
		b.runningTasksMu.Lock()
		delete(b.runningTasks, chatID)
		b.runningTasksMu.Unlock()
	}()

	// 1. 获取物理隔离的 Session
	session := engine.GlobalSessionMgr.GetOrCreate("feishu:"+chatID, b.workDir)

	// 2. 通过工厂模式，为当前会话生成一个挂好了专属 CostTracker 的新引擎
	eng := b.factory(session)
	// 注入机器人名称到 System Prompt
	if b.botName != "" {
		eng.SetBotName(b.botName)
	}

	// 3. 将专属的 Reporter 塞入 Context，供底层 Middleware 提取
	runCtx := ContextWithReporter(ctx, reporter)

	if err := eng.Run(runCtx, session, prompt, reporter); err != nil {
		reporter.sendMsg(fmt.Sprintf("❌ Agent 运行失败: %v", err))
	}
}

// =============================================================================
// FeishuReporter: 通过飞书消息 API 推送引擎状态
// =============================================================================

type FeishuReporter struct {
	client          *lark.Client
	chatID          string
	streamMsgID     string         // 流式推送的消息 ID（首次发送后获取）
	streamBuf       strings.Builder // 流式文本缓冲区
	streamMu        sync.Mutex     // 保护 streamBuf
	lastFlushLen    int            // 上次推送时的文本长度
	isStreaming     bool           // 是否正在流式推送
}

// sendMsg 发送文本消息，返回消息 ID（用于后续流式更新）。
func (r *FeishuReporter) sendMsg(text string) string {
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

	resp, err := r.client.Im.V1.Message.Create(context.Background(), req)
	if err != nil {
		log.Printf("[Feishu] 发送消息失败: %v", err)
		return ""
	}
	if resp.Data != nil && resp.Data.MessageId != nil {
		return *resp.Data.MessageId
	}
	return ""
}

// patchMsg 更新已发送的消息内容（飞书 PATCH API）。
func (r *FeishuReporter) patchMsg(msgID string, text string) {
	content := map[string]string{"text": text}
	b, _ := json.Marshal(content)

	req := larkim.NewPatchMessageReqBuilder().
		MessageId(msgID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(string(b)).
			Build()).
		Build()

	_, err := r.client.Im.V1.Message.Patch(context.Background(), req)
	if err != nil {
		log.Printf("[Feishu] 更新消息失败: %v", err)
	}
}

func (r *FeishuReporter) OnStreamDelta(ctx context.Context, delta string, isThinking bool) {
	r.streamMu.Lock()
	defer r.streamMu.Unlock()

	// 首次收到流式数据时，发送初始消息获取 message_id
	if !r.isStreaming {
		r.isStreaming = true
		r.streamMsgID = r.sendMsg("🤖 正在生成回复...")
		r.lastFlushLen = 0
	}

	r.streamBuf.WriteString(delta)

	// 每累积 200 字符或遇到换行时推送一次更新（避免 API 限流）
	currentLen := r.streamBuf.Len()
	if currentLen-r.lastFlushLen >= 200 || strings.Contains(delta, "\n") {
		if r.streamMsgID != "" {
			r.patchMsg(r.streamMsgID, r.streamBuf.String())
			r.lastFlushLen = currentLen
		}
	}
}

func (r *FeishuReporter) OnThinking(ctx context.Context) {
	// 流式模式下不单独发"慢思考"消息，由 OnStreamDelta 统一处理
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

	r.streamMu.Lock()
	defer r.streamMu.Unlock()

	if r.isStreaming && r.streamMsgID != "" {
		// 流式模式：最终更新消息为完整内容
		r.patchMsg(r.streamMsgID, content)
		r.isStreaming = false
		r.streamBuf.Reset()
		r.streamMsgID = ""
		r.lastFlushLen = 0
	} else {
		// 非流式模式（无 delta 输出时）：直接发送
		r.sendMsg(content)
	}
}

var _ engine.Reporter = (*FeishuReporter)(nil)

// =============================================================================
// 机器人信息获取
// =============================================================================

// fetchBotName 通过飞书 API 获取机器人自身名称，用于 System Prompt 身份声明。
// 使用 tenant_access_token 调用 GET /open-apis/bot/v3/info/ 接口。
func (b *FeishuBot) fetchBotName() {
	// 1. 获取 tenant_access_token
	tokenURL := "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal"
	tokenBody := fmt.Sprintf(`{"app_id":"%s","app_secret":"%s"}`, b.appID, b.appSecret)

	tokenResp, err := http.Post(tokenURL, "application/json", strings.NewReader(tokenBody))
	if err != nil {
		log.Printf("[Feishu] ⚠️ 获取 tenant_access_token 失败: %v", err)
		return
	}
	defer tokenResp.Body.Close()

	var tokenResult struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenResult); err != nil {
		log.Printf("[Feishu] ⚠️ 解析 token 响应失败: %v", err)
		return
	}
	if tokenResult.Code != 0 || tokenResult.TenantAccessToken == "" {
		log.Printf("[Feishu] ⚠️ 获取 token 失败: code=%d, msg=%s", tokenResult.Code, tokenResult.Msg)
		return
	}

	// 2. 调用 bot info 接口
	botInfoURL := "https://open.feishu.cn/open-apis/bot/v3/info/"
	req, _ := http.NewRequest("GET", botInfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+tokenResult.TenantAccessToken)

	botResp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[Feishu] ⚠️ 获取机器人信息失败: %v", err)
		return
	}
	defer botResp.Body.Close()

	var botResult struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID string `json:"open_id"`
			Name   string `json:"app_name"`
		} `json:"bot"`
	}
	if err := json.NewDecoder(botResp.Body).Decode(&botResult); err != nil {
		log.Printf("[Feishu] ⚠️ 解析机器人信息失败: %v", err)
		return
	}
	if botResult.Code != 0 {
		log.Printf("[Feishu] ⚠️ 获取机器人信息失败: code=%d, msg=%s", botResult.Code, botResult.Msg)
		return
	}

	if botResult.Bot.Name != "" {
		b.botName = botResult.Bot.Name
		log.Printf("[Feishu] 🤖 机器人名称: %s", b.botName)
	}
}

// =============================================================================
// 消息文本提取
// =============================================================================

func extractTextContent(event *larkim.P2MessageReceiveV1) string {
	if event.Event == nil || event.Event.Message == nil || event.Event.Message.Content == nil {
		return ""
	}
	raw := *event.Event.Message.Content

	var content struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(raw), &content) == nil && content.Text != "" {
		return content.Text
	}
	// 回退解析
	raw = strings.TrimPrefix(raw, `{"text":"`)
	raw = strings.TrimSuffix(raw, `"}`)
	return strings.TrimSpace(raw)
}
