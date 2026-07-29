// Package ilink 提供 iLink Bot（个人微信机器人）的长轮询集成与 Reporter 实现。
//
// iLink Bot 是腾讯官方的个人微信 Bot 服务，通过 HTTP 长轮询接收消息。
// 协议基于 golembot 的 weixin.js 逆向工程。
//
// 核心特性：
//   - HTTP 长轮询（非 WebSocket）：POST /ilink/bot/getupdates
//   - Bearer Token 鉴权：格式 xxx@im.bot:xxx
//   - 24 小时消息窗口：仅在用户最后一次发消息后 24 小时内可回复
//   - 仅支持单聊（DM），不支持群聊
package ilink

import "time"

// =============================================================================
// iLink Bot API 协议结构体（基于 golembot weixin.js 逆向）
// =============================================================================

// GetUpdatesRequest 长轮询请求体。
type GetUpdatesRequest struct {
	GetUpdatesBuf string   `json:"get_updates_buf"` // 同步缓冲区（上次响应返回）
	BaseInfo      BaseInfo `json:"base_info"`
}

// BaseInfo 基础信息。
type BaseInfo struct {
	ChannelVersion string `json:"channel_version"` // "0.1.0"
}

// GetUpdatesResponse 长轮询响应体。
type GetUpdatesResponse struct {
	GetUpdatesBuf string    `json:"get_updates_buf"` // 下次轮询用的同步缓冲区
	Msgs          []Message `json:"msgs"`            // 消息列表
}

// Message 单条消息（getupdates 返回的消息结构）。
type Message struct {
	MessageType  int    `json:"message_type"`  // 1=用户消息, 2=机器人消息
	FromUserID   string `json:"from_user_id"`  // 发送者 OpenID
	ToUserID     string `json:"to_user_id"`    // 接收者 OpenID
	ClientID     string `json:"client_id"`     // 消息唯一 ID（用于去重）
	ContextToken string `json:"context_token"` // 回复所需的上下文令牌
	ItemList     []Item `json:"item_list"`     // 消息内容列表
}

// Item 消息内容项。
type Item struct {
	Type      int        `json:"type"` // 1=文本, 2=图片, 3=语音, 4=文件, 5=视频
	TextItem  *TextItem  `json:"text_item,omitempty"`
	ImageItem *ImageItem `json:"image_item,omitempty"`
	VoiceItem *VoiceItem `json:"voice_item,omitempty"`
	FileItem  *FileItem  `json:"file_item,omitempty"`
}

// TextItem 文本内容。
type TextItem struct {
	Text string `json:"text"`
}

// ImageItem 图片内容。
type ImageItem struct {
	Media  *MediaInfo `json:"media,omitempty"`
	AesKey string     `json:"aeskey,omitempty"` // AES 解密密钥（hex）
}

// VoiceItem 语音内容。
type VoiceItem struct {
	Media *MediaInfo `json:"media,omitempty"`
	Text  string     `json:"text,omitempty"` // 语音识别文本
}

// FileItem 文件内容。
type FileItem struct {
	Media   *MediaInfo `json:"media,omitempty"`
	FileURL string     `json:"file_url,omitempty"` // 文件名/URL
}

// MediaInfo 媒体信息。
type MediaInfo struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"` // CDN 下载加密参数
	AesKey            string `json:"aes_key,omitempty"`             // AES 密钥（Base64）
}

// SendMessageRequest 发送消息请求体。
type SendMessageRequest struct {
	Msg      SendMsg  `json:"msg"`
	BaseInfo BaseInfo `json:"base_info"`
}

// SendMsg 发送消息的核心结构。
type SendMsg struct {
	FromUserID   string `json:"from_user_id"`  // 留空即可
	ToUserID     string `json:"to_user_id"`    // 接收者 OpenID
	ClientID     string `json:"client_id"`     // UUID
	MessageType  int    `json:"message_type"`  // 2=发送
	MessageState int    `json:"message_state"` // 2
	ContextToken string `json:"context_token"` // 从收到的消息中获取
	ItemList     []Item `json:"item_list"`     // 消息内容
}

// =============================================================================
// 内部数据结构
// =============================================================================

// seenMsgEntry 用于消息去重，记录已处理的消息 ID 和过期时间。
type seenMsgEntry struct {
	seenAt time.Time // 首次看到的时间
}

// =============================================================================
// 常量
// =============================================================================

const (
	// API 端点
	endpointGetUpdates  = "/ilink/bot/getupdates"
	endpointSendMessage = "/ilink/bot/sendmessage"

	// 默认配置
	defaultMaxMessageLen = 2000            // 单条消息最大字符数（rune）
	maxSeenMsgIDs        = 500             // 去重 map 最大容量
	seenMsgTTL           = 5 * time.Minute // 去重条目 TTL
	agentTimeout         = 5 * time.Minute // 单次 Agent 任务超时
	maxConcurrent        = 20              // 全局并发上限

	// HTTP 头
	headerAuthType    = "AuthorizationType"
	headerAuth        = "Authorization"
	headerWechatUin   = "X-WECHAT-UIN"
	headerContentType = "Content-Type"
	valAuthType       = "ilink_bot_token"
	valContentType    = "application/json"

	// 消息类型
	msgTypeUser = 1 // 用户消息
	msgTypeBot  = 2 // 机器人消息

	// 内容类型
	itemTypeText  = 1 // 文本
	itemTypeImage = 2 // 图片
	itemTypeVoice = 3 // 语音
	itemTypeFile  = 4 // 文件
	itemTypeVideo = 5 // 视频

	// 频道版本
	channelVersion = "0.1.0"
)
