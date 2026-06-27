// internal/engine/deadlock_test.go
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/schema"
	"github.com/lhuan/go-tiny-claw/internal/tools"
)

// ═══════════════════════════════════════════════════════════════
// Mock 工具：black_hole — 永远返回失败，且输出固定的错误信息
// ═══════════════════════════════════════════════════════════════

type blackHoleTool struct{}

func (b *blackHoleTool) Name() string { return "black_hole" }

func (b *blackHoleTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        "black_hole",
		Description: "一个永远失败的工具，用于测试死循环检测机制。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"data": map[string]interface{}{
					"type":        "string",
					"description": "任意输入",
				},
			},
		},
	}
}

func (b *blackHoleTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return "", fmt.Errorf("black_hole: 此路不通，永远失败")
}

// ═══════════════════════════════════════════════════════════════
// Mock Provider：stuckProvider — 永远返回相同的工具调用
// 大模型"执迷不悟"，反复调用同一个工具、同一组参数
// ═══════════════════════════════════════════════════════════════

type stuckProvider struct {
	callCount    int64 // 原子计数，记录被调用次数
	maxToolCalls int   // 前 N 次返回工具调用，之后只返回文本（让引擎退出循环）
}

func (p *stuckProvider) StreamGenerate(
	ctx context.Context,
	messages []schema.Message,
	availableTools []schema.ToolDefinition,
) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 4)

	go func() {
		defer close(ch)

		callIdx := atomic.AddInt64(&p.callCount, 1)

		if callIdx <= int64(p.maxToolCalls) {
			// 前 N 次：执念文本 + 完全相同的工具调用
			ch <- provider.StreamEvent{
				Type:  provider.StreamEventTextDelta,
				Delta: fmt.Sprintf("让我再试第 %d 次，这次一定可以...", callIdx),
			}

			ch <- provider.StreamEvent{
				Type:          provider.StreamEventToolCallBegin,
				ToolCallIndex: 0,
				ToolCallID:    fmt.Sprintf("call_stuck_%d", callIdx),
				ToolCallName:  "black_hole",
			}

			// 永远相同的参数 — 这就是"死局"的核心
			ch <- provider.StreamEvent{
				Type:          provider.StreamEventToolCallArgsDelta,
				ToolCallIndex: 0,
				Delta:         `{"data": "please_work"}`,
			}
		} else {
			// 第 N+1 次：大模型"终于放弃了"，不再调用工具，回复纯文本
			ch <- provider.StreamEvent{
				Type:  provider.StreamEventTextDelta,
				Delta: "我放弃了，black_hole 工具一直失败，我无法完成这个任务。",
			}
		}

		ch <- provider.StreamEvent{Type: provider.StreamEventDone}
	}()

	return ch, nil
}

func (p *stuckProvider) Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	return provider.GenerateBlocking(ctx, p.StreamGenerate, messages, availableTools)
}

// ═══════════════════════════════════════════════════════════════
// TestDeadlock_Detection 验证死循环探测机制
//
// 构造的"死局"：
//   - Provider 永远返回相同的 ToolCall（black_hole, data="please_work"）
//   - black_hole 工具永远返回失败
//   - 大模型无法自行跳出这个循环
//
// 预期：
//   - 第 1、2 次失败：计数累加，不注入
//   - 第 3 次失败：触发 CheckAndInject，向 Session 注入严厉提醒
//   - Session 中出现 [SYSTEM REMINDER 警告] 关键词
// ═══════════════════════════════════════════════════════════════

func TestDeadlock_Detection(t *testing.T) {
	// ── 组装引擎 ──
	mockProvider := &stuckProvider{maxToolCalls: 5} // 前 5 次执着重试，第 6 次放弃
	registry := tools.NewRegistry()
	registry.Register(&blackHoleTool{})

	tmpDir, err := os.MkdirTemp("", "deadlock_test_*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	eng := NewAgentEngine(mockProvider, registry, tmpDir, false, false)

	// ── 执行：给大模型一个"永远完不成"的任务 ──
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	session := NewSession("test_deadlock", tmpDir)
	reporter := &captureReporter{}

	log.Println("====== 开始死循环测试：大模型将反复调用 black_hole ======")

	err = eng.Run(ctx, session, "请用 black_hole 工具处理 data='please_work'，直到成功为止", reporter)

	log.Println("====== 死循环测试结束 ======")

	// 引擎可能因为 context 超时而返回 error，这是正常的
	if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") &&
		!strings.Contains(err.Error(), "context canceled") {
		// 如果是其他类型的错误，可能是 bug
		t.Logf("引擎返回了非超时错误（可能正常）: %v", err)
	}

	// ── 验证：Session 中是否包含死循环打断提醒 ──
	messages := session.GetWorkingMemory(1000, 1000000)
	var foundReminder bool
	for _, msg := range messages {
		if msg.Role == schema.RoleUser && strings.Contains(msg.Content, "[SYSTEM REMINDER 警告]") {
			foundReminder = true
			t.Logf("✅ 找到死循环打断提醒: %s", truncate(msg.Content, 200))
			break
		}
	}

	if !foundReminder {
		t.Error("❌ 未检测到死循环打断提醒！CheckAndInject 机制可能未生效。")
		t.Log("Session 消息列表:")
		for i, msg := range messages {
			t.Logf("  [%d] role=%s content=%s", i, msg.Role, truncate(msg.Content, 100))
		}
		t.FailNow()
	}

	// ── 验证：black_hole 至少被调用了 3 次 ──
	callCount := atomic.LoadInt64(&mockProvider.callCount)
	if callCount < 3 {
		t.Errorf("❌ black_hole 只被调用了 %d 次，预期至少 3 次", callCount)
	}

	// ── 验证：打断提醒注入在第 3 次失败之后 ──
	// 找到提醒的位置，确认它前面有至少 3 次 black_hole 的工具结果
	var reminderIdx int
	for i, msg := range messages {
		if msg.Role == schema.RoleUser && strings.Contains(msg.Content, "[SYSTEM REMINDER 警告]") {
			reminderIdx = i
			break
		}
	}

	// 统计提醒之前的 black_hole 失败次数
	failureBeforeReminder := 0
	for i := 0; i < reminderIdx; i++ {
		if messages[i].Role == schema.RoleUser && strings.Contains(messages[i].Content, "black_hole") {
			failureBeforeReminder++
		}
	}

	t.Logf("📊 统计: Provider 被调用 %d 次, 提醒前有 %d 次 black_hole 失败记录", callCount, failureBeforeReminder)

	if failureBeforeReminder < 3 {
		t.Errorf("❌ 提醒注入过早：预期至少 3 次失败后才注入，实际只有 %d 次", failureBeforeReminder)
	}

	t.Logf("✅ 死循环探测机制验证通过：第 %d 次失败后成功注入打断提醒", failureBeforeReminder)
}
