package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// FetchURLTool 发起 HTTP GET 请求获取网页或 API 内容。
// 支持超时控制和内容截断，防止超大响应撑爆内存。
type FetchURLTool struct{}

func NewFetchURLTool() *FetchURLTool {
	return &FetchURLTool{}
}

func (t *FetchURLTool) Name() string { return "fetch_url" }

func (t *FetchURLTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: "fetch_url",
		Description: "发起 HTTP GET 请求获取指定 URL 的内容。" +
			"适用于获取网页、API 响应、文档等。自动截断超大响应（默认 50KB）。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url": map[string]interface{}{
					"type":        "string",
					"description": "要请求的 URL 地址",
				},
				"timeout": map[string]interface{}{
					"type":        "integer",
					"description": "请求超时时间（秒），默认 15",
				},
				"max_bytes": map[string]interface{}{
					"type":        "integer",
					"description": "最大返回字节数，默认 51200（50KB）",
				},
			},
			"required": []string{"url"},
		},
	}
}

type fetchURLArgs struct {
	URL      string `json:"url"`
	Timeout  int    `json:"timeout"`
	MaxBytes int    `json:"max_bytes"`
}

func (t *FetchURLTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input fetchURLArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", NewToolError(ErrParamParseFailed, "参数解析失败", err)
	}

	if input.URL == "" {
		return "", NewToolError(ErrParamParseFailed, "URL 不能为空", nil)
	}

	// 安全检查：只允许 http/https 协议
	if !strings.HasPrefix(input.URL, "http://") && !strings.HasPrefix(input.URL, "https://") {
		return "", NewToolError(ErrParamParseFailed, "仅支持 http:// 和 https:// 协议", nil)
	}

	timeout := input.Timeout
	if timeout <= 0 {
		timeout = 15
	}

	maxBytes := input.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 50 * 1024 // 50KB
	}

	client := &http.Client{Timeout: time.Duration(timeout) * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", input.URL, nil)
	if err != nil {
		return "", NewToolError(ErrParamParseFailed, fmt.Sprintf("创建请求失败: %v", err), err)
	}
	req.Header.Set("User-Agent", "go-tiny-claw/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求失败 [%s]: %v", input.URL, err)
	}
	defer resp.Body.Close()

	// 限制读取大小
	limitedReader := io.LimitReader(resp.Body, int64(maxBytes))
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %v", err)
	}

	result := string(body)
	truncated := ""
	if len(body) >= maxBytes {
		truncated = fmt.Sprintf("\n\n... [内容已截断，最大 %d 字节]", maxBytes)
	}

	return fmt.Sprintf("[HTTP %d] %s\nContent-Length: %d\n\n%s%s",
		resp.StatusCode, input.URL, resp.ContentLength, result, truncated), nil
}
