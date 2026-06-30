package provider

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// StreamAccumulator 将零散的 StreamEvent 组装成完整的 schema.Message。
//
// 它是纯数据累积器，不涉及任何 I/O。如果需要实时打印，请在调用方（如 Engine）
// 消费事件时自行处理。
//
// 并发安全：非 goroutine-safe。调用方应保证在单个 goroutine 中顺序调用。
type StreamAccumulator struct {
	content  strings.Builder
	thinking strings.Builder
	toolMap  map[int]*toolSlot
	usage    *schema.TokenUsage // API 返回的 Token 消耗（由 StreamEventUsage 事件写入）
}

type toolSlot struct {
	ID      string
	Name    string
	ArgsBuf strings.Builder
}

// NewStreamAccumulator 创建一个新的累积器实例。
func NewStreamAccumulator() *StreamAccumulator {
	return &StreamAccumulator{
		toolMap: make(map[int]*toolSlot),
	}
}

// Ingest 处理单个流式事件，将增量内容写入对应缓冲区。
func (a *StreamAccumulator) Ingest(ev StreamEvent) {
	switch ev.Type {
	case StreamEventThinkingDelta:
		a.thinking.WriteString(ev.Delta)

	case StreamEventTextDelta:
		a.content.WriteString(ev.Delta)

	case StreamEventToolCallBegin:
		a.toolMap[ev.ToolCallIndex] = &toolSlot{
			ID:   ev.ToolCallID,
			Name: ev.ToolCallName,
		}

	case StreamEventToolCallArgsDelta:
		if slot, ok := a.toolMap[ev.ToolCallIndex]; ok {
			slot.ArgsBuf.WriteString(ev.Delta)
		}

	case StreamEventUsage:
		if a.usage == nil {
			a.usage = ev.Usage
		} else {
			// 合并多次 Usage 事件（Anthropic 流式 API 分 MessageStart 和 MessageDelta 两段返回）
			a.usage.PromptTokens += ev.Usage.PromptTokens
			a.usage.CompletionTokens += ev.Usage.CompletionTokens
			a.usage.TotalTokens += ev.Usage.TotalTokens
		}
	}
}

// Content 返回目前已累积的普通文本内容。
func (a *StreamAccumulator) Content() string {
	return a.content.String()
}

// Thinking 返回目前已累积的思考/推理文本。
func (a *StreamAccumulator) Thinking() string {
	return a.thinking.String()
}

// Finalize 将所有累积的数据组装为完整的 schema.Message。
//
// 组装规则：
//   - Content ← content 缓冲区（如果为空，则使用 thinking 缓冲区）
//   - ToolCalls ← 按 Index 升序排列的工具调用（Args 已拼接为完整 JSON）
func (a *StreamAccumulator) Finalize() *schema.Message {
	content := a.content.String()

	// 如果没有 text 内容但有 thinking 内容，使用 thinking 作为 content
	if content == "" && a.thinking.Len() > 0 {
		content = a.thinking.String()
	}

	msg := &schema.Message{
		Role:    schema.RoleAssistant,
		Content: content,
		Usage:   a.usage, // 携带 API 返回的 Token 消耗
	}

	if len(a.toolMap) == 0 {
		return msg
	}

	// 按 Index 升序排列，保证工具调用的顺序与模型输出顺序一致
	indices := make([]int, 0, len(a.toolMap))
	for i := range a.toolMap {
		indices = append(indices, i)
	}
	sort.Ints(indices)

	for _, idx := range indices {
		s := a.toolMap[idx]
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			ID:        s.ID,
			Name:      s.Name,
			Arguments: json.RawMessage(s.ArgsBuf.String()),
		})
	}

	return msg
}
