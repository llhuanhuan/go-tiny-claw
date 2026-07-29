package ilink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lhuan/go-tiny-claw/internal/engine"
)

// =============================================================================
// AgentEngineFactory: per-session 引擎工厂（复用飞书模式）
// =============================================================================

// AgentEngineFactory 允许每次收到消息时，根据 Session 动态创建引擎实例，
// 实现 per-session 的计费隔离（CostTracker 物理隔离）。
type AgentEngineFactory func(session *engine.Session) *engine.AgentEngine

// =============================================================================
// ILinkBot: 个人微信机器人主控
// =============================================================================

// ILinkBot 封装了 iLink Bot 的长轮询配置与核心业务流。
type ILinkBot struct {
	baseURL string             // iLink API 基础地址
	token   string             // Bearer Token（格式: xxx@im.bot:xxx）
	workDir string             // 工作区路径
	factory AgentEngineFactory // 工厂模式：per-session 创建引擎

	// 消息去重：带 TTL 的 map，防止长轮询重放导致重复处理
	seenMsgIDs   map[string]seenMsgEntry
	seenMsgIDsMu sync.Mutex

	// context_token 缓存：每个用户需要 context_token 才能回复
	contextTokens   map[string]string
	contextTokensMu sync.Mutex

	// 并发控制：全局信号量 + per-user 任务取消
	semaphore      chan struct{}       // 全局并发上限
	runningTasks   map[string]userTask // per-user 运行中任务
	runningTasksMu sync.Mutex
	nextTaskID     uint64 // 任务 ID 自增计数器

	// 可替换的 HTTP 客户端（测试用）
	httpClient *http.Client
}

// userTask 追踪单个用户的运行中任务。
type userTask struct {
	cancel context.CancelFunc
	taskID uint64 // 唯一任务 ID，用于防止误删新任务
}

// NewILinkBot 创建一个 iLink Bot 实例。
// factory 在每次收到消息时为当前会话动态创建引擎实例，实现 per-session 计费隔离。
func NewILinkBot(factory AgentEngineFactory, workDir string, token string, baseURL string) *ILinkBot {
	if token == "" {
		log.Fatal("[iLink] 配置缺失：token 不能为空")
	}
	if baseURL == "" {
		baseURL = "https://ilinkai.weixin.qq.com"
	}

	log.Printf("[iLink] ✅ Token 已配置")
	log.Printf("[iLink] API 地址: %s", baseURL)
	log.Printf("[iLink] 连接模式: HTTP 长轮询")

	return &ILinkBot{
		baseURL:       strings.TrimRight(baseURL, "/"),
		token:         token,
		workDir:       workDir,
		factory:       factory,
		seenMsgIDs:    make(map[string]seenMsgEntry),
		contextTokens: make(map[string]string),
		semaphore:     make(chan struct{}, maxConcurrent),
		runningTasks:  make(map[string]userTask),
		httpClient:    &http.Client{Timeout: 65 * time.Second}, // 略大于长轮询超时
	}
}

// Start 启动长轮询循环，阻塞直到 context 取消。
func (b *ILinkBot) Start(ctx context.Context) error {
	log.Println("[iLink] 正在启动长轮询...")

	// 启动去重清理 goroutine
	go b.cleanupSeenMsgIDs(ctx)

	var syncBuf string // 同步缓冲区（get_updates_buf）

	for {
		select {
		case <-ctx.Done():
			log.Println("[iLink] 收到退出信号，长轮询已停止")
			return nil
		default:
		}

		resp, err := b.getUpdates(ctx, syncBuf)
		if err != nil {
			if ctx.Err() != nil {
				return nil // context 取消，正常退出
			}
			log.Printf("[iLink] 长轮询请求失败: %v，3 秒后重试", err)
			time.Sleep(3 * time.Second)
			continue
		}

		// 更新同步缓冲区
		if resp.GetUpdatesBuf != "" {
			syncBuf = resp.GetUpdatesBuf
		}

		for _, msg := range resp.Msgs {
			b.handleMessage(ctx, msg)
		}
	}
}

// =============================================================================
// HTTP API 调用
// =============================================================================

// getUpdates 调用长轮询接口获取新消息。
func (b *ILinkBot) getUpdates(ctx context.Context, syncBuf string) (*GetUpdatesResponse, error) {
	reqBody := GetUpdatesRequest{
		GetUpdatesBuf: syncBuf,
		BaseInfo:      BaseInfo{ChannelVersion: channelVersion},
	}

	body, err := b.doAPIRequest(ctx, endpointGetUpdates, reqBody)
	if err != nil {
		return nil, fmt.Errorf("请求 getupdates 失败: %w", err)
	}

	var resp GetUpdatesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("解析 getupdates 响应失败: %w", err)
	}

	return &resp, nil
}

// sendMessage 调用发送消息接口。
func (b *ILinkBot) sendMessage(toUserID string, text string) error {
	// 获取 context_token
	b.contextTokensMu.Lock()
	ctxToken, exists := b.contextTokens[toUserID]
	b.contextTokensMu.Unlock()

	if !exists {
		return fmt.Errorf("用户 %s 无 context_token（24 小时窗口已过期或未收到消息）", toUserID)
	}

	reqBody := SendMessageRequest{
		Msg: SendMsg{
			FromUserID:   "",
			ToUserID:     toUserID,
			ClientID:     uuid.New().String(),
			MessageType:  msgTypeBot,
			MessageState: 2,
			ContextToken: ctxToken,
			ItemList: []Item{
				{
					Type:     itemTypeText,
					TextItem: &TextItem{Text: text},
				},
			},
		},
		BaseInfo: BaseInfo{ChannelVersion: channelVersion},
	}

	body, err := b.doAPIRequest(context.Background(), endpointSendMessage, reqBody)
	if err != nil {
		return fmt.Errorf("发送消息失败: %w", err)
	}

	// 检查响应中是否有错误
	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err == nil {
		if ret, ok := resp["ret"].(float64); ok && ret != 0 {
			errMsg, _ := resp["errmsg"].(string)
			return fmt.Errorf("sendmessage 返回错误: ret=%v, errmsg=%s", ret, errMsg)
		}
	}

	return nil
}

// doAPIRequest 执行 iLink API 请求，返回响应 body。
func (b *ILinkBot) doAPIRequest(ctx context.Context, endpoint string, reqBody interface{}) ([]byte, error) {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求体失败: %w", err)
	}

	url := b.baseURL + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set(headerContentType, valContentType)
	req.Header.Set(headerAuthType, valAuthType)
	req.Header.Set(headerAuth, "Bearer "+b.token)
	req.Header.Set(headerWechatUin, fmt.Sprintf("%09d", time.Now().UnixNano()%1000000000))

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("执行请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP 状态码 %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// =============================================================================
// 消息处理
// =============================================================================

// handleMessage 处理单条消息。
func (b *ILinkBot) handleMessage(ctx context.Context, msg Message) {
	// 仅处理用户消息（message_type=1）
	if msg.MessageType != msgTypeUser {
		return
	}

	// 提取文本内容
	text := extractText(msg.ItemList)
	if strings.TrimSpace(text) == "" {
		return
	}

	userID := msg.FromUserID
	if userID == "" {
		return
	}

	// 消息去重
	if !b.markSeen(msg.ClientID) {
		return
	}

	// 缓存 context_token（用于回复）
	if msg.ContextToken != "" {
		b.contextTokensMu.Lock()
		b.contextTokens[userID] = msg.ContextToken
		b.contextTokensMu.Unlock()
	}

	log.Printf("[iLink] 收到用户 %s 消息: %s", userID, text)

	// 命令拦截
	switch text {
	case "/stop", "停止":
		b.runningTasksMu.Lock()
		task, exists := b.runningTasks[userID]
		b.runningTasksMu.Unlock()
		if exists {
			task.cancel()
			b.sendText(userID, "⏹️ 已中断当前任务")
			log.Printf("[iLink] 用户 %s: 任务已中断", userID)
		} else {
			b.sendText(userID, "当前没有运行中的任务")
		}
		return

	case "/reset", "重置":
		// 先中断正在运行的任务
		b.runningTasksMu.Lock()
		if task, exists := b.runningTasks[userID]; exists {
			task.cancel()
			delete(b.runningTasks, userID)
		}
		b.runningTasksMu.Unlock()
		// 清空会话历史
		sessionID := "ilink:" + userID
		sess := engine.GlobalSessionMgr.GetOrCreate(sessionID, b.workDir)
		sess.ClearHistory()
		b.sendText(userID, "🔄 会话已重置，历史已清空")
		log.Printf("[iLink] 用户 %s: 会话已重置", userID)
		return
	}

	// 异步启动 Agent 任务
	go b.runAgent(userID, text)
}

// extractText 从 item_list 中提取文本内容。
func extractText(items []Item) string {
	var parts []string
	for _, item := range items {
		if item.Type == itemTypeText && item.TextItem != nil {
			parts = append(parts, item.TextItem.Text)
		}
	}
	return strings.Join(parts, "")
}

// sendText 发送文本消息（内部用，忽略错误）。
func (b *ILinkBot) sendText(userID string, text string) {
	if err := b.sendMessage(userID, text); err != nil {
		log.Printf("[iLink] 发送消息失败: %v", err)
	}
}

// runAgent 在独立 goroutine 中运行 Agent 任务。
// 通过工厂模式为当前会话创建专属引擎，支持并发控制和任务取消。
func (b *ILinkBot) runAgent(userID string, prompt string) {
	// 全局并发信号量
	select {
	case b.semaphore <- struct{}{}:
		defer func() { <-b.semaphore }()
	default:
		b.sendText(userID, "⏳ 当前任务较多，请稍后再试")
		log.Printf("[iLink] 用户 %s: 并发已满，拒绝任务", userID)
		return
	}

	// per-user 单任务：取消旧任务
	b.runningTasksMu.Lock()
	if oldTask, exists := b.runningTasks[userID]; exists {
		oldTask.cancel()
		log.Printf("[iLink] 用户 %s: 取消旧任务", userID)
	}

	// 创建带超时的 context
	ctx, cancel := context.WithTimeout(context.Background(), agentTimeout)
	b.nextTaskID++
	myTaskID := b.nextTaskID
	b.runningTasks[userID] = userTask{cancel: cancel, taskID: myTaskID}
	b.runningTasksMu.Unlock()

	defer func() {
		cancel()
		b.runningTasksMu.Lock()
		// 仅当当前任务仍是自己时才删除（防止误删新任务）
		if cur, ok := b.runningTasks[userID]; ok && cur.taskID == myTaskID {
			delete(b.runningTasks, userID)
		}
		b.runningTasksMu.Unlock()
	}()

	// 1. 创建 Reporter
	reporter := &ILinkReporter{
		bot:    b,
		userID: userID,
	}

	// 2. 获取物理隔离的 Session
	sessionID := "ilink:" + userID
	session := engine.GlobalSessionMgr.GetOrCreate(sessionID, b.workDir)

	// 3. 通过工厂模式创建专属引擎
	eng := b.factory(session)
	if eng == nil {
		reporter.sendMsg("❌ 引擎初始化失败：工厂返回 nil")
		return
	}

	// 4. 运行 Agent
	if err := eng.Run(ctx, session, prompt, reporter); err != nil {
		if ctx.Err() == context.Canceled {
			reporter.sendMsg("⏹️ 任务已被中断")
		} else if ctx.Err() == context.DeadlineExceeded {
			reporter.sendMsg("⏰ 任务超时（5 分钟），已自动停止")
		} else {
			reporter.sendMsg(fmt.Sprintf("❌ Agent 运行失败: %v", err))
		}
	}
}

// =============================================================================
// 消息去重
// =============================================================================

// markSeen 标记消息为已处理，返回 true 表示首次看到。
func (b *ILinkBot) markSeen(msgID string) bool {
	if msgID == "" {
		return true // 空 ID 不去重
	}

	b.seenMsgIDsMu.Lock()
	defer b.seenMsgIDsMu.Unlock()

	if _, exists := b.seenMsgIDs[msgID]; exists {
		return false
	}

	// 容量检查：达到上限时清理最旧的条目
	if len(b.seenMsgIDs) >= maxSeenMsgIDs {
		b.evictOldest()
	}

	b.seenMsgIDs[msgID] = seenMsgEntry{seenAt: time.Now()}
	return true
}

// evictOldest 清理最旧的去重条目（调用方需持有锁）。
func (b *ILinkBot) evictOldest() {
	var oldestKey string
	var oldestTime time.Time

	for k, v := range b.seenMsgIDs {
		if oldestKey == "" || v.seenAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = v.seenAt
		}
	}

	if oldestKey != "" {
		delete(b.seenMsgIDs, oldestKey)
	}
}

// cleanupSeenMsgIDs 定期清理过期的去重条目。
func (b *ILinkBot) cleanupSeenMsgIDs(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.seenMsgIDsMu.Lock()
			now := time.Now()
			for k, v := range b.seenMsgIDs {
				if now.Sub(v.seenAt) > seenMsgTTL {
					delete(b.seenMsgIDs, k)
				}
			}
			b.seenMsgIDsMu.Unlock()
		}
	}
}
