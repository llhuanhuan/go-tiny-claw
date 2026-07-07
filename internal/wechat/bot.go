// Package wechat 提供企业微信（WeChat Work）机器人的回调服务与 Reporter 实现。
//
// 支持的部署模式：
//   - 企业微信群机器人（Group Bot）：通过 Webhook URL 收发消息
//   - 企业微信自建应用（Custom App）：通过应用回调 + API 收发消息
//
// 并发模型：每个收到的消息在独立 goroutine 中运行 Agent 任务，
// 多个聊天窗口之间完全隔离、互不阻塞。
package wechat

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/lhuan/go-tiny-claw/internal/engine"
)

// =============================================================================
// WeChatBot: 企业微信机器人主控
// =============================================================================

// WeChatBot 封装了企业微信机器人的配置与核心业务流。
type WeChatBot struct {
	webhookURL string           // 发送消息的 Webhook URL
	token      string           // 回调签名校验 Token
	aesKey     string           // 消息加解密密钥（可选，加密模式需要）
	engine     *engine.AgentEngine // 持有核心引擎引用
}

// WechatBotConfig 是创建 WeChatBot 所需的配置。
type WechatBotConfig struct {
	WebhookURL     string
	Token          string
	EncodingAESKey string
}

// NewWeChatBot 创建一个企业微信 Bot 实例。
// 配置由调用方从配置文件中获取后传入。
func NewWeChatBot(eng *engine.AgentEngine, cfg WechatBotConfig) *WeChatBot {
	if cfg.WebhookURL == "" {
		log.Fatal("微信配置缺失：webhook_url 不能为空")
	}

	return &WeChatBot{
		webhookURL: cfg.WebhookURL,
		token:      cfg.Token,
		aesKey:     cfg.EncodingAESKey,
		engine:     eng,
	}
}

// Engine 返回底层引擎引用。
func (b *WeChatBot) Engine() *engine.AgentEngine {
	return b.engine
}

// =============================================================================
// HTTP Handler: 接收企业微信回调消息
// =============================================================================

// ServeHTTP 实现 http.Handler 接口，处理企业微信服务器的回调请求。
//
// 支持两种 HTTP 方法：
//   - GET：URL 验证（企业微信后台配置回调 URL 时触发）
//   - POST：消息回调（用户发送消息时触发）
//
// 用法：
//
//	http.Handle("/webhook/wechat", bot)
func (b *WeChatBot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		b.handleURLVerify(w, r)
	case http.MethodPost:
		b.handleMessageCallback(w, r)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// handleURLVerify 处理企业微信回调 URL 验证（GET 请求）。
func (b *WeChatBot) handleURLVerify(w http.ResponseWriter, r *http.Request) {
	// 提取验证参数
	msgSignature := r.URL.Query().Get("msg_signature")
	timestamp := r.URL.Query().Get("timestamp")
	nonce := r.URL.Query().Get("nonce")
	echostr := r.URL.Query().Get("echostr")

	if msgSignature == "" || timestamp == "" || nonce == "" || echostr == "" {
		http.Error(w, "missing parameters", http.StatusBadRequest)
		return
	}

	// 验证签名
	if !b.verifySignature(msgSignature, timestamp, nonce, echostr) {
		log.Printf("[WeChat] URL 验证失败：签名不匹配")
		http.Error(w, "signature verification failed", http.StatusForbidden)
		return
	}

	// 返回解密后的 echostr（明文模式下直接返回原值）
	log.Printf("[WeChat] URL 验证成功")
	w.Write([]byte(echostr))
}

// handleMessageCallback 处理消息回调（POST 请求）。
func (b *WeChatBot) handleMessageCallback(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[WeChat] 读取回调 Body 失败: %v", err)
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	log.Printf("[WeChat] 收到回调: %s", string(body))

	// 解析 XML 消息体
	var msg callbackMessage
	if err := xml.Unmarshal(body, &msg); err != nil {
		log.Printf("[WeChat] 解析 XML 失败: %v", err)
		http.Error(w, "parse xml failed", http.StatusBadRequest)
		return
	}

	// 提取文本内容
	contentStr := strings.TrimSpace(msg.Content())
	if contentStr == "" {
		log.Printf("[WeChat] 收到空消息或非文本消息，忽略")
		w.Write([]byte("success"))
		return
	}

	chatID := msg.ChatID()
	if chatID == "" {
		chatID = msg.FromUserName // 单聊场景使用发送者 ID
	}

	log.Printf("[WeChat] 收到会话 %s 消息: %s\n", chatID, contentStr)

	// 【驾驭并发】：不阻塞 HTTP 回调，异步启动 Agent 任务
	go b.handleAgentRun(chatID, contentStr)

	// 立即返回 success，否则企业微信会重试
	w.Write([]byte("success"))
}

// =============================================================================
// Agent 桥接
// =============================================================================

// handleAgentRun 在独立 goroutine 中运行 Agent 任务。
func (b *WeChatBot) handleAgentRun(chatID string, prompt string) {
	reporter := &WechatReporter{
		webhookURL: b.webhookURL,
		chatID:     chatID,
	}

	session := engine.GlobalSessionMgr.GetOrCreate("wechat:"+chatID, b.engine.WorkDir)
	err := b.engine.Run(context.Background(), session, prompt, reporter)
	if err != nil {
		reporter.sendMsg(fmt.Sprintf("❌ Agent 运行崩溃: %v", err))
	}
}

// =============================================================================
// 签名验证
// =============================================================================

// verifySignature 验证企业微信回调签名。
//
// 算法：SHA1(sort(token, timestamp, nonce, echostr))
func (b *WeChatBot) verifySignature(msgSignature, timestamp, nonce, echostr string) bool {
	if b.token == "" {
		// 未配置 Token 时跳过签名校验（仅用于开发调试）
		log.Printf("[WeChat] 警告：未配置 WECHAT_TOKEN，跳过签名校验")
		return true
	}

	strs := sort.StringSlice{b.token, timestamp, nonce, echostr}
	sort.Strings(strs)

	h := sha1.New()
	h.Write([]byte(strings.Join(strs, "")))
	calculated := fmt.Sprintf("%x", h.Sum(nil))

	return calculated == msgSignature
}

// =============================================================================
// XML 消息结构体
// =============================================================================

// callbackMessage 企业微信回调 XML 消息体的通用结构。
//
// 文本消息示例：
//
//	<xml>
//	  <ToUserName><![CDATA[toUser]]></ToUserName>
//	  <AgentID><![CDATA[1001]]></AgentID>
//	  <MsgType><![CDATA[text]]></MsgType>
//	  <Content><![CDATA[帮我写代码]]></Content>
//	  <MsgId><![CDATA[msg-id]]></MsgId>
//	  <ChatId><![CDATA[chat-id]]></ChatId>
//	</xml>
type callbackMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   string   `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Content_     string   `xml:"Content"`
	MsgID        string   `xml:"MsgId"`
	AgentID      string   `xml:"AgentID"`
	ChatID_      string   `xml:"ChatId"`
	ChatType     string   `xml:"ChatType"`
}

// Content 返回消息文本内容。
func (m *callbackMessage) Content() string {
	return strings.TrimSpace(m.Content_)
}

// ChatID 返回聊天会话 ID（群聊场景）。
func (m *callbackMessage) ChatID() string {
	return m.ChatID_
}

// =============================================================================
// WechatReporter: 将引擎输出通过企业微信 Webhook 发送
// =============================================================================

// WechatReporter 实现 engine.Reporter 接口，将 Agent 引擎的运行状态
// 通过企业微信群机器人 Webhook 实时推送到聊天窗口。
type WechatReporter struct {
	webhookURL string
	chatID     string
}

// sendMsg 通过企业微信 Webhook 发送 Markdown 消息。
func (r *WechatReporter) sendMsg(text string) {
	// 企业微信群机器人 Webhook 支持 text 和 markdown 两种消息类型。
	// 这里使用 markdown 以获得更好的可读性。
	payload := map[string]interface{}{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": text,
		},
	}

	body, _ := json.Marshal(payload)
	resp, err := http.Post(r.webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[WeChat] 发送消息失败: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[WeChat] 发送消息返回非 200: status=%d body=%s", resp.StatusCode, string(respBody))
	}
}

// OnThinking 当模型开始慢思考时调用。
func (r *WechatReporter) OnStreamDelta(ctx context.Context, delta string, isThinking bool) {
	// 企业微信模式暂不支持逐字推送，文本通过 OnMessage 一次性发送
}

func (r *WechatReporter) OnThinking(ctx context.Context) {
	r.sendMsg("🤔 **模型正在慢思考** (Thinking)...")
}

// OnToolCall 当模型决定调用工具时调用。
func (r *WechatReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	// 截断过长参数
	displayArgs := args
	if len(displayArgs) > 200 {
		displayArgs = displayArgs[:200] + "..."
	}
	// 替换 Markdown 特殊字符防止渲染异常
	displayArgs = strings.ReplaceAll(displayArgs, "`", "'")
	r.sendMsg(fmt.Sprintf("🛠️ **执行工具**：`%s`\n> %s", toolName, displayArgs))
}

// OnToolResult 当工具执行完毕时调用。
func (r *WechatReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		r.sendMsg(fmt.Sprintf("⚠️ **执行报错** (%s)\n> %s", toolName, result))
	} else {
		r.sendMsg(fmt.Sprintf("✅ **执行成功** (%s)", toolName))
	}
}

// OnMessage 当模型输出最终纯文本回答时调用。
func (r *WechatReporter) OnMessage(ctx context.Context, content string) {
	// 企业微信 Markdown 消息有长度限制（约 4096 字符），超长分段发送
	const maxLen = 4000
	if len(content) > maxLen {
		// 分段发送
		for start := 0; start < len(content); start += maxLen {
			end := start + maxLen
			if end > len(content) {
				end = len(content)
			}
			r.sendMsg(content[start:end])
		}
		return
	}
	r.sendMsg(content)
}

// 编译时类型检查：确保 WechatReporter 实现了 Reporter 接口
var _ engine.Reporter = (*WechatReporter)(nil)
