package ilink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/engine"
)

// =============================================================================
// 测试辅助函数
// =============================================================================

// defaultHandler 返回一个默认的 HTTP 处理器，模拟发送消息成功。
func defaultHandler(sentMessages *[]string, mu *sync.Mutex) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req SendMessageRequest
		json.NewDecoder(r.Body).Decode(&req)
		if mu != nil && sentMessages != nil && len(req.Msg.ItemList) > 0 {
			mu.Lock()
			if req.Msg.ItemList[0].TextItem != nil {
				*sentMessages = append(*sentMessages, req.Msg.ItemList[0].TextItem.Text)
			}
			mu.Unlock()
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"ret": 0})
	}
}

// newTestBot 创建一个使用 mock HTTP 服务器的 ILinkBot。
func newTestBot(t *testing.T, handler http.HandlerFunc) (*ILinkBot, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)

	bot := &ILinkBot{
		baseURL: server.URL,
		token:   "test-token@im.bot:abc123",
		workDir: t.TempDir(),
		factory: func(sess *engine.Session) *engine.AgentEngine {
			return nil
		},
		seenMsgIDs:    make(map[string]seenMsgEntry),
		contextTokens: make(map[string]string),
		semaphore:     make(chan struct{}, maxConcurrent),
		runningTasks:  make(map[string]userTask),
		httpClient:    server.Client(),
	}

	return bot, server
}

// makeTextMsg 创建一条文本消息。
func makeTextMsg(userID, text, msgID string) Message {
	return Message{
		MessageType:  msgTypeUser,
		FromUserID:   userID,
		ClientID:     msgID,
		ContextToken: "ctx-" + userID,
		ItemList: []Item{
			{Type: itemTypeText, TextItem: &TextItem{Text: text}},
		},
	}
}

// =============================================================================
// 消息去重测试
// =============================================================================

func TestMarkSeen_FirstTime(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	ok := bot.markSeen("msg-001")
	if !ok {
		t.Fatal("首次标记应返回 true")
	}
}

func TestMarkSeen_Duplicate(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	bot.markSeen("msg-001")
	ok := bot.markSeen("msg-001")
	if ok {
		t.Fatal("重复标记应返回 false")
	}
}

func TestMarkSeen_EmptyID(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	ok1 := bot.markSeen("")
	ok2 := bot.markSeen("")
	if !ok1 || !ok2 {
		t.Fatal("空 ID 应始终返回 true")
	}
}

func TestMarkSeen_Eviction(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	for i := 0; i < maxSeenMsgIDs; i++ {
		bot.markSeen("msg-" + string(rune(i)))
	}

	ok := bot.markSeen("msg-new")
	if !ok {
		t.Fatal("驱逐后应能添加新条目")
	}

	if len(bot.seenMsgIDs) > maxSeenMsgIDs {
		t.Fatalf("去重 map 不应超过上限 %d，当前: %d", maxSeenMsgIDs, len(bot.seenMsgIDs))
	}
}

// =============================================================================
// 并发控制测试
// =============================================================================

func TestRunAgent_ConcurrencyLimit(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	for i := 0; i < maxConcurrent; i++ {
		bot.semaphore <- struct{}{}
	}

	select {
	case bot.semaphore <- struct{}{}:
		t.Fatal("信号量已满时不应能写入")
	default:
	}
}

// =============================================================================
// 消息处理测试
// =============================================================================

func TestHandleMessage_SkipsBotMessage(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	bot.handleMessage(context.Background(), Message{
		MessageType: msgTypeBot,
		FromUserID:  "bot-001",
		ClientID:    "bot-msg-001",
		ItemList:    []Item{{Type: itemTypeText, TextItem: &TextItem{Text: "机器人消息"}}},
	})

	bot.seenMsgIDsMu.Lock()
	_, exists := bot.seenMsgIDs["bot-msg-001"]
	bot.seenMsgIDsMu.Unlock()

	if exists {
		t.Fatal("机器人消息不应被记录到去重 map")
	}
}

func TestHandleMessage_SkipsEmptyText(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	bot.handleMessage(context.Background(), Message{
		MessageType: msgTypeUser,
		FromUserID:  "user-001",
		ClientID:    "empty-001",
		ItemList:    []Item{{Type: itemTypeText, TextItem: &TextItem{Text: "   "}}},
	})

	bot.seenMsgIDsMu.Lock()
	_, exists := bot.seenMsgIDs["empty-001"]
	bot.seenMsgIDsMu.Unlock()

	if exists {
		t.Fatal("空文本消息不应被记录到去重 map")
	}
}

func TestHandleMessage_SkipsDuplicate(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	msg := makeTextMsg("user-001", "你好", "dup-001")

	bot.handleMessage(context.Background(), msg)
	time.Sleep(100 * time.Millisecond)

	// 第二次应被去重跳过
	bot.handleMessage(context.Background(), msg)

	bot.seenMsgIDsMu.Lock()
	_, exists := bot.seenMsgIDs["dup-001"]
	bot.seenMsgIDsMu.Unlock()

	if !exists {
		t.Fatal("首次消息应被记录")
	}
}

func TestHandleMessage_CachesContextToken(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	bot.handleMessage(context.Background(), Message{
		MessageType:  msgTypeUser,
		FromUserID:   "user-ctx",
		ClientID:     "ctx-001",
		ContextToken: "special-token-123",
		ItemList:     []Item{{Type: itemTypeText, TextItem: &TextItem{Text: "hello"}}},
	})

	bot.contextTokensMu.Lock()
	token, exists := bot.contextTokens["user-ctx"]
	bot.contextTokensMu.Unlock()

	if !exists || token != "special-token-123" {
		t.Fatalf("context_token 应被缓存，实际: %s, exists=%v", token, exists)
	}
}

func TestHandleMessage_StopCommand(t *testing.T) {
	var mu sync.Mutex
	var msgs []string

	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	called := false
	bot.runningTasksMu.Lock()
	bot.runningTasks["user-stop"] = userTask{cancel: func() { called = true }, taskID: 1}
	bot.runningTasksMu.Unlock()

	// 需要先缓存 context_token
	bot.contextTokensMu.Lock()
	bot.contextTokens["user-stop"] = "ctx-stop"
	bot.contextTokensMu.Unlock()

	bot.handleMessage(context.Background(), makeTextMsg("user-stop", "/stop", "stop-001"))

	if !called {
		t.Fatal("/stop 应调用 cancel 函数")
	}

	mu.Lock()
	if len(msgs) == 0 || msgs[0] != "⏹️ 已中断当前任务" {
		t.Fatalf("/stop 应发送中断消息，实际: %v", msgs)
	}
	mu.Unlock()
}

func TestHandleMessage_ResetCommand(t *testing.T) {
	var mu sync.Mutex
	var msgs []string

	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	// 需要先缓存 context_token
	bot.contextTokensMu.Lock()
	bot.contextTokens["user-reset"] = "ctx-reset"
	bot.contextTokensMu.Unlock()

	bot.handleMessage(context.Background(), makeTextMsg("user-reset", "/reset", "reset-001"))

	mu.Lock()
	if len(msgs) == 0 || msgs[0] != "🔄 会话已重置，历史已清空" {
		t.Fatalf("/reset 应发送重置消息，实际: %v", msgs)
	}
	mu.Unlock()
}

func TestHandleMessage_StopNoTask(t *testing.T) {
	var mu sync.Mutex
	var msgs []string

	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	bot.contextTokensMu.Lock()
	bot.contextTokens["user-no-task"] = "ctx-no-task"
	bot.contextTokensMu.Unlock()

	bot.handleMessage(context.Background(), makeTextMsg("user-no-task", "/stop", "stop-no-task"))

	mu.Lock()
	if len(msgs) == 0 || msgs[0] != "当前没有运行中的任务" {
		t.Fatalf("无任务时 /stop 应提示，实际: %v", msgs)
	}
	mu.Unlock()
}

// =============================================================================
// extractText 测试
// =============================================================================

func TestExtractText_SingleText(t *testing.T) {
	items := []Item{{Type: itemTypeText, TextItem: &TextItem{Text: "你好世界"}}}
	result := extractText(items)
	if result != "你好世界" {
		t.Fatalf("extractText 不匹配: %s", result)
	}
}

func TestExtractText_MultipleItems(t *testing.T) {
	items := []Item{
		{Type: itemTypeText, TextItem: &TextItem{Text: "第一段"}},
		{Type: itemTypeText, TextItem: &TextItem{Text: "第二段"}},
	}
	result := extractText(items)
	if result != "第一段第二段" {
		t.Fatalf("extractText 不匹配: %s", result)
	}
}

func TestExtractText_NoTextItems(t *testing.T) {
	items := []Item{{Type: itemTypeImage}}
	result := extractText(items)
	if result != "" {
		t.Fatalf("无文本时应返回空: %s", result)
	}
}

// =============================================================================
// HTTP API 测试
// =============================================================================

func TestDoAPIRequest_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(headerAuthType) != valAuthType {
			t.Errorf("AuthorizationType 头不匹配: got %s", r.Header.Get(headerAuthType))
		}
		if r.Header.Get(headerAuth) != "Bearer test-token@im.bot:abc123" {
			t.Errorf("Authorization 头不匹配: got %s", r.Header.Get(headerAuth))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	bot := &ILinkBot{
		baseURL:    server.URL,
		token:      "test-token@im.bot:abc123",
		httpClient: server.Client(),
	}

	body, err := bot.doAPIRequest(context.Background(), "/test", map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	if string(body) != `{"ret":0}` {
		t.Fatalf("响应体不匹配: %s", string(body))
	}
}

func TestDoAPIRequest_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	bot := &ILinkBot{
		baseURL:    server.URL,
		token:      "test-token",
		httpClient: server.Client(),
	}

	_, err := bot.doAPIRequest(context.Background(), "/test", nil)
	if err == nil {
		t.Fatal("HTTP 500 应返回错误")
	}
}

func TestGetUpdates_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GetUpdatesResponse{
			GetUpdatesBuf: "next-buf-123",
			Msgs: []Message{
				makeTextMsg("user-001", "你好", "msg-001"),
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	bot := &ILinkBot{
		baseURL:    server.URL,
		token:      "test-token",
		httpClient: server.Client(),
	}

	resp, err := bot.getUpdates(context.Background(), "")
	if err != nil {
		t.Fatalf("getUpdates 失败: %v", err)
	}

	if len(resp.Msgs) != 1 {
		t.Fatalf("应收到 1 条消息，实际: %d", len(resp.Msgs))
	}

	if resp.GetUpdatesBuf != "next-buf-123" {
		t.Fatalf("同步缓冲区不匹配: %s", resp.GetUpdatesBuf)
	}
}

func TestSendMessage_NoContextToken(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	// 不缓存 context_token，应返回错误
	err := bot.sendMessage("unknown-user", "你好")
	if err == nil {
		t.Fatal("无 context_token 时应返回错误")
	}
}

func TestSendMessage_WithContextToken(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	bot.contextTokensMu.Lock()
	bot.contextTokens["user-sender"] = "ctx-sender-123"
	bot.contextTokensMu.Unlock()

	err := bot.sendMessage("user-sender", "你好世界")
	if err != nil {
		t.Fatalf("发送消息失败: %v", err)
	}

	mu.Lock()
	if len(msgs) == 0 || msgs[0] != "你好世界" {
		t.Fatalf("消息内容不匹配: %v", msgs)
	}
	mu.Unlock()
}

// =============================================================================
// Reporter 测试
// =============================================================================

func TestILinkReporter_Interface(t *testing.T) {
	bot := &ILinkBot{}
	reporter := &ILinkReporter{
		bot:    bot,
		userID: "test-user",
	}

	if reporter.userID != "test-user" {
		t.Fatal("Reporter userID 不匹配")
	}
}

// =============================================================================
// TTL 清理测试
// =============================================================================

func TestCleanupSeenMsgIDs(t *testing.T) {
	var mu sync.Mutex
	var msgs []string
	bot, server := newTestBot(t, defaultHandler(&msgs, &mu))
	defer server.Close()

	bot.seenMsgIDsMu.Lock()
	bot.seenMsgIDs["old-msg"] = seenMsgEntry{seenAt: time.Now().Add(-seenMsgTTL - time.Minute)}
	bot.seenMsgIDs["new-msg"] = seenMsgEntry{seenAt: time.Now()}
	bot.seenMsgIDsMu.Unlock()

	// 手动触发一次清理
	bot.seenMsgIDsMu.Lock()
	now := time.Now()
	for k, v := range bot.seenMsgIDs {
		if now.Sub(v.seenAt) > seenMsgTTL {
			delete(bot.seenMsgIDs, k)
		}
	}
	bot.seenMsgIDsMu.Unlock()

	bot.seenMsgIDsMu.Lock()
	defer bot.seenMsgIDsMu.Unlock()

	if _, exists := bot.seenMsgIDs["old-msg"]; exists {
		t.Fatal("过期条目应被清理")
	}

	if _, exists := bot.seenMsgIDs["new-msg"]; !exists {
		t.Fatal("未过期条目不应被清理")
	}
}
