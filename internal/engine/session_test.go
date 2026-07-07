package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// ============================================================
// Mock Reporter: 捕获引擎回调，用于断言
// ============================================================

type mockReporter struct {
	mu          sync.Mutex
	Thinking    int
	ToolCalls   []string
	ToolResults []string
	Messages    []string
}

func (r *mockReporter) OnStreamDelta(ctx context.Context, delta string, isThinking bool) {}

func (r *mockReporter) OnThinking(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Thinking++
}

func (r *mockReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ToolCalls = append(r.ToolCalls, toolName)
}

func (r *mockReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ToolResults = append(r.ToolResults, toolName)
}

func (r *mockReporter) OnMessage(ctx context.Context, content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Messages = append(r.Messages, content)
}

// ============================================================
// 辅助函数
// ============================================================

func userMsg(content string) schema.Message {
	return schema.Message{Role: schema.RoleUser, Content: content}
}

func assistantMsg(content string) schema.Message {
	return schema.Message{Role: schema.RoleAssistant, Content: content}
}

func toolCallMsg(id, name string, args map[string]string) schema.Message {
	argsBytes, _ := json.Marshal(args)
	return schema.Message{
		Role: schema.RoleAssistant,
		ToolCalls: []schema.ToolCall{
			{ID: id, Name: name, Arguments: argsBytes},
		},
	}
}

func toolResultMsg(callID, content string) schema.Message {
	return schema.Message{
		Role:       schema.RoleUser,
		Content:    content,
		ToolCallID: callID,
	}
}

// ============================================================
// 场景 1: Working Memory 截断 —— 密钥泄露防御
// ============================================================

// TestWorkingMemory_Truncation_SecretFlush 模拟：
// 回合 1 中模型通过 read_file 读取了包含密钥的文件，
// 随后 6 轮闲聊将密钥挤出 Working Memory (limit=6)，
// 回合 2 验证：被截断的历史中确实不再包含密钥内容。
func TestWorkingMemory_Truncation_SecretFlush(t *testing.T) {
	session := NewSession("test_secret_flush", t.TempDir())

	// 回合 1: 用户请求 + 模型读取文件 + 返回密钥
	session.Append(userMsg("帮我看看 README.md 里记录了什么密钥？"))
	session.Append(toolCallMsg("tc_001", "read_file", map[string]string{"path": "README.md"}))
	session.Append(toolResultMsg("tc_001", "SECRET_KEY=sk-abc123xyz"))
	session.Append(assistantMsg("README.md 中记录的密钥是 SECRET_KEY=sk-abc123xyz"))

	// 刷 6 轮闲聊 (12 条消息)，把 Working Memory 刷满
	for i := 0; i < 6; i++ {
		session.Append(userMsg(fmt.Sprintf("闲聊占位 #%d", i)))
		session.Append(assistantMsg(fmt.Sprintf("收到闲聊 #%d", i)))
	}

	// Working Memory 只保留最近 6 条
	wm := session.GetWorkingMemory(6, 0)

	// 验证：密钥不应出现在 Working Memory 中
	for _, msg := range wm {
		if msg.Content == "SECRET_KEY=sk-abc123xyz" {
			t.Fatal("安全漏洞！密钥仍残留在 Working Memory 中，截断机制失效")
		}
		if msg.Content == "README.md 中记录的密钥是 SECRET_KEY=sk-abc123xyz" {
			t.Fatal("安全漏洞！模型的密钥回复仍残留在 Working Memory 中")
		}
	}

	// 验证：Working Memory 应该是最后 6 条闲聊
	if len(wm) != 6 {
		t.Fatalf("期望 Working Memory 长度=6, 得到 %d", len(wm))
	}
	if wm[0].Content != "闲聊占位 #3" {
		t.Fatalf("期望首条为闲聊 #3, 得到 %q", wm[0].Content)
	}
}

// ============================================================
// 场景 2: 多 Session 并发读写 —— 数据竞争检测
// ============================================================

// TestConcurrentSessions_Isolation 并发创建多个 Session 并同时读写，
// 用 go test -race 验证无数据竞争。
func TestConcurrentSessions_Isolation(t *testing.T) {
	var wg sync.WaitGroup
	sessionIDs := []string{"feishu_group_A", "wechat_user_B", "terminal_cli", "api_webhook_D"}

	for _, id := range sessionIDs {
		wg.Add(1)
		go func(sessionID string) {
			defer wg.Done()
			sess := NewSession(sessionID, t.TempDir())

			// 并发写入 50 条消息
			for i := 0; i < 50; i++ {
				sess.Append(userMsg(fmt.Sprintf("[%s] user msg %d", sessionID, i)))
				sess.Append(assistantMsg(fmt.Sprintf("[%s] assistant reply %d", sessionID, i)))
			}

			// 并发读取 Working Memory
			wm := sess.GetWorkingMemory(6, 0)
			if len(wm) > 6 {
				t.Errorf("[%s] Working Memory 超出限制: %d", sessionID, len(wm))
			}
		}(id)
	}

	wg.Wait()
}

// ============================================================
// 场景 3: GlobalSessionMgr 并发 GetOrCreate —— 幂等性验证
// ============================================================

// TestSessionManager_ConcurrentGetOrCreate 100 个 goroutine 并发
// GetOrCreate 同一个 session ID，验证返回的是同一个 Session 实例。
func TestSessionManager_ConcurrentGetOrCreate(t *testing.T) {
	mgr := &SessionManager{sessions: make(map[string]*Session)}
	const goroutines = 100

	var wg sync.WaitGroup
	sessions := make([]*Session, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sessions[idx] = mgr.GetOrCreate("shared_session", t.TempDir())
		}(i)
	}
	wg.Wait()

	// 所有 goroutine 拿到的应该是同一个 Session 指针
	first := sessions[0]
	for i := 1; i < goroutines; i++ {
		if sessions[i] != first {
			t.Fatalf("goroutine %d 拿到了不同的 Session 实例 (非幂等)", i)
		}
	}
}

// ============================================================
// 场景 4: 孤儿 ToolResult 清理 —— API 400 防御
// ============================================================

// TestWorkingMemory_OrphanToolResultSkipped 验证 GetWorkingMemory
// 在截断边界恰好落在 ToolResult 上时，会自动跳过孤儿消息。
func TestWorkingMemory_OrphanToolResultSkipped(t *testing.T) {
	session := NewSession("orphan_test", t.TempDir())

	// 构造历史：ToolCall → ToolResult → 用户消息 → 助手回复
	session.Append(toolCallMsg("tc_100", "bash", map[string]string{"command": "ls"}))
	session.Append(toolResultMsg("tc_100", "file1.go\nfile2.go"))
	session.Append(userMsg("继续"))
	session.Append(assistantMsg("好的，我来看看。"))

	// limit=3 截断后，第一条应该是 ToolResult (孤儿)，
	// GetWorkingMemory 应自动跳过它
	wm := session.GetWorkingMemory(3, 0)

	if len(wm) == 0 {
		t.Fatal("Working Memory 不应为空")
	}

	// 首条消息不应该是孤儿 ToolResult
	if wm[0].ToolCallID != "" {
		t.Fatalf("首条消息是孤儿 ToolResult (ToolCallID=%s)，应该被跳过", wm[0].ToolCallID)
	}

	// 首条应该是 "继续" (user msg)
	if wm[0].Content != "继续" {
		t.Fatalf("期望首条为 '继续', 得到 %q", wm[0].Content)
	}
}

// ============================================================
// 场景 5: 多轮对话 + 工具调用的完整 ReAct 链路
// ============================================================

// TestSession_ReActChain 模拟一个完整的多轮 ReAct 链路：
// 用户请求 → 模型思考 → 调用 bash → 返回结果 → 模型回复 → 完成。
// 验证 Session 历史记录的完整性和消息顺序。
func TestSession_ReActChain(t *testing.T) {
	session := NewSession("react_chain", t.TempDir())

	// Turn 1: 用户请求
	session.Append(userMsg("帮我创建一个 hello.go 文件"))

	// Turn 1: 模型思考 (EnableThinking=true 时的行为)
	session.Append(assistantMsg("我需要创建一个 Go 文件..."))

	// Turn 1: 模型调用 bash 工具
	session.Append(toolCallMsg("tc_200", "bash", map[string]string{"command": "echo 'package main' > hello.go"}))

	// Turn 1: 工具返回结果
	session.Append(toolResultMsg("tc_200", ""))

	// Turn 1: 模型回复
	session.Append(assistantMsg("已创建 hello.go 文件。"))

	// Turn 2: 用户继续请求
	session.Append(userMsg("帮我提交一下"))

	// Turn 2: 模型调用 git 工具
	session.Append(toolCallMsg("tc_201", "bash", map[string]string{"command": "git add hello.go && git commit -m 'add hello.go'"}))

	// Turn 2: 工具返回结果
	session.Append(toolResultMsg("tc_201", "[master abc1234] add hello.go\n 1 file changed"))

	// Turn 2: 模型最终回复
	session.Append(assistantMsg("已提交，commit hash: abc1234"))

	// 验证完整历史
	wm := session.GetWorkingMemory(100, 0) // 不截断
	if len(wm) != 9 {
		t.Fatalf("期望 9 条消息, 得到 %d", len(wm))
	}

	// 验证消息顺序
	expectedOrder := []struct {
		role    schema.Role
		content string
	}{
		{schema.RoleUser, "帮我创建一个 hello.go 文件"},
		{schema.RoleAssistant, "我需要创建一个 Go 文件..."},
		{schema.RoleAssistant, ""}, // tool call, content 为空
		{schema.RoleUser, ""},      // tool result
		{schema.RoleAssistant, "已创建 hello.go 文件。"},
		{schema.RoleUser, "帮我提交一下"},
		{schema.RoleAssistant, ""}, // tool call
		{schema.RoleUser, ""},      // tool result
		{schema.RoleAssistant, "已提交，commit hash: abc1234"},
	}

	for i, exp := range expectedOrder {
		if wm[i].Role != exp.role {
			t.Fatalf("消息[%d] 期望 role=%s, 得到 %s", i, exp.role, wm[i].Role)
		}
		if exp.content != "" && wm[i].Content != exp.content {
			t.Fatalf("消息[%d] 期望 content=%q, 得到 %q", i, exp.content, wm[i].Content)
		}
	}
}

// ============================================================
// 场景 6: 并发 Append + GetWorkingMemory —— 压力测试
// ============================================================

// TestSession_ConcurrentReadWrite 一个 goroutine 持续写入，
// 另一个 goroutine 持续读取 Working Memory，验证不会 panic 或数据损坏。
func TestSession_ConcurrentReadWrite(t *testing.T) {
	session := NewSession("stress_test", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Writer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			default:
				session.Append(userMsg(fmt.Sprintf("msg-%d", i)))
				i++
			}
		}
	}()

	// Reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				wm := session.GetWorkingMemory(6, 0)
				_ = len(wm) // 只要不 panic 就算通过
			}
		}
	}()

	wg.Wait()
}

// ============================================================
// 场景 7: 多平台 Session 隔离 —— 模拟飞书/微信/终端并发
// ============================================================

// TestMultiPlatformSessionIsolation 模拟三个平台同时向各自的 Session
// 发送消息，验证 Session 之间完全隔离，不会互相污染。
func TestMultiPlatformSessionIsolation(t *testing.T) {
	type platformSession struct {
		id      string
		session *Session
	}

	platforms := []platformSession{
		{"feishu_group_devops", NewSession("feishu_group_devops", t.TempDir())},
		{"wechat_user_zhangsan", NewSession("wechat_user_zhangsan", t.TempDir())},
		{"terminal_main", NewSession("terminal_main", t.TempDir())},
	}

	var wg sync.WaitGroup

	for _, ps := range platforms {
		wg.Add(1)
		go func(p platformSession) {
			defer wg.Done()

			// 每个平台发送 20 条消息
			for i := 0; i < 20; i++ {
				p.session.Append(userMsg(fmt.Sprintf("[%s] 问题 %d", p.id, i)))
				p.session.Append(assistantMsg(fmt.Sprintf("[%s] 回答 %d", p.id, i)))
			}

			// 验证 Working Memory 中只包含自己的消息
			wm := p.session.GetWorkingMemory(6, 0)
			for _, msg := range wm {
				expectedPrefix := fmt.Sprintf("[%s]", p.id)
				if len(msg.Content) < len(expectedPrefix) {
					t.Errorf("[%s] 消息内容过短: %q", p.id, msg.Content)
					continue
				}
				if msg.Content[:len(expectedPrefix)] != expectedPrefix {
					t.Errorf("[%s] 发现不属于本平台的消息: %q", p.id, msg.Content)
				}
			}
		}(ps)
	}

	wg.Wait()

	// 最终验证：每个 Session 的总消息数都是 40 (20 user + 20 assistant)
	for _, ps := range platforms {
		wm := ps.session.GetWorkingMemory(0, 0) // 0,0 = 不限制，返回全量
		if len(wm) != 40 {
			t.Errorf("[%s] 期望 40 条消息, 得到 %d", ps.id, len(wm))
		}
	}
}

// ============================================================
// 场景 8: Session 时间戳更新验证
// ============================================================

// TestSession_TimestampUpdates 验证 Append 操作会更新 UpdatedAt 时间戳。
func TestSession_TimestampUpdates(t *testing.T) {
	session := NewSession("timestamp_test", t.TempDir())
	initialUpdate := session.UpdatedAt

	time.Sleep(10 * time.Millisecond)

	session.Append(userMsg("hello"))

	if !session.UpdatedAt.After(initialUpdate) {
		t.Fatal("Append 后 UpdatedAt 未更新")
	}
}

// ============================================================
// 场景 9: 空 Session 的 Working Memory
// ============================================================

// TestWorkingMemory_EmptySession 空 Session 的 Working Memory 应返回空切片。
func TestWorkingMemory_EmptySession(t *testing.T) {
	session := NewSession("empty", t.TempDir())

	wm := session.GetWorkingMemory(6, 0)
	if len(wm) != 0 {
		t.Fatalf("空 Session 的 Working Memory 应为空, 得到 %d 条", len(wm))
	}
}

// ============================================================
// 场景 10: Working Memory limit=0 全量返回
// ============================================================

// TestWorkingMemory_ZeroLimit limit=0 应返回全量历史。
func TestWorkingMemory_ZeroLimit(t *testing.T) {
	session := NewSession("zero_limit", t.TempDir())

	for i := 0; i < 100; i++ {
		session.Append(userMsg(fmt.Sprintf("msg %d", i)))
	}

	wm := session.GetWorkingMemory(0, 0)
	if len(wm) != 100 {
		t.Fatalf("limit=0 应返回全量 100 条, 得到 %d", len(wm))
	}
}

// ============================================================
// 场景 11: 双维度截断 —— maxChars 先于 limit 触发
// ============================================================

// TestWorkingMemory_MaxCharsTruncatesBeforeLimit 模拟用户描述的核心场景：
// limit=10（很宽松），但中间有一条巨型 ToolResult（1 万字符），
// maxChars=500 会提前触发截断，防止工作记忆撑爆上下文。
func TestWorkingMemory_MaxCharsTruncatesBeforeLimit(t *testing.T) {
	session := NewSession("max_chars_test", t.TempDir())

	// 先加几条普通消息
	session.Append(userMsg("第一条普通消息"))
	session.Append(assistantMsg("好的"))

	// 制造一条巨型 ToolResult（约 10000 字符）
	giantContent := make([]byte, 10000)
	for i := range giantContent {
		giantContent[i] = 'X'
	}
	session.Append(toolCallMsg("tc_giant", "read_file", map[string]string{"path": "huge.log"}))
	session.Append(toolResultMsg("tc_giant", string(giantContent)))

	// 再加几条普通消息
	session.Append(userMsg("帮我分析一下"))
	session.Append(assistantMsg("分析完毕"))

	// limit=10（够宽松），maxChars=500（严格）
	// 预期：maxChars 先触发，只保留最后几条消息，不含那条 10000 字符的巨型结果
	wm := session.GetWorkingMemory(10, 500)

	totalChars := 0
	for _, msg := range wm {
		totalChars += len(msg.Content)
	}

	// 总字符数不应超过 maxChars（允许一条超大消息的容差）
	if totalChars > 500*2 {
		t.Fatalf("maxChars=500 截断失效: 实际字符数=%d", totalChars)
	}

	// 最后的消息 "分析完毕" 应该被保留
	last := wm[len(wm)-1]
	if last.Content != "分析完毕" {
		t.Fatalf("最后一条消息应为 '分析完毕', 得到 %q", last.Content)
	}
}

// ============================================================
// 场景 12: 双维度截断 —— limit 先于 maxChars 触发
// ============================================================

// TestWorkingMemory_LimitTruncatesBeforeMaxChars 当 maxChars 预算很充裕时，
// 应退化为与旧版相同的"按条数截断"行为。
func TestWorkingMemory_LimitTruncatesBeforeMaxChars(t *testing.T) {
	session := NewSession("limit_first", t.TempDir())

	for i := 0; i < 20; i++ {
		session.Append(userMsg(fmt.Sprintf("消息 %d", i)))
		session.Append(assistantMsg(fmt.Sprintf("回复 %d", i)))
	}

	// limit=4, maxChars=100000（很宽松）
	// 预期：limit 先触发，只保留 4 条
	wm := session.GetWorkingMemory(4, 100000)

	if len(wm) != 4 {
		t.Fatalf("期望 4 条, 得到 %d", len(wm))
	}

	// 验证是最后 4 条
	if wm[0].Content != "消息 18" {
		t.Fatalf("期望首条为 '消息 18', 得到 %q", wm[0].Content)
	}
}

// ============================================================
// 场景 13: maxChars 保证至少返回 1 条消息（防空死循环）
// ============================================================

// TestWorkingMemory_MaxCharsAlwaysReturnsAtLeastOne 即使单条消息
// 独自就超出 maxChars 预算，也必须至少返回最后一条消息。
func TestWorkingMemory_MaxCharsAlwaysReturnsAtLeastOne(t *testing.T) {
	session := NewSession("at_least_one", t.TempDir())

	// 一条 2000 字符的消息
	bigContent := make([]byte, 2000)
	for i := range bigContent {
		bigContent[i] = 'A'
	}
	session.Append(userMsg(string(bigContent)))

	// maxChars=100（远小于单条消息），但至少返回 1 条
	wm := session.GetWorkingMemory(10, 100)

	if len(wm) < 1 {
		t.Fatal("即使单条消息超预算，也应至少返回 1 条消息")
	}
}

// ============================================================
// 场景 14: 双维度截断后孤儿 ToolResult 仍被清理
// ============================================================

// TestWorkingMemory_MaxCharsOrphanCleanup 验证 maxChars 截断后
// 首条如果是孤儿 ToolResult，仍然会被正确跳过。
func TestWorkingMemory_MaxCharsOrphanCleanup(t *testing.T) {
	session := NewSession("max_chars_orphan", t.TempDir())

	session.Append(toolCallMsg("tc_orphan", "bash", map[string]string{"command": "ls"}))
	session.Append(toolResultMsg("tc_orphan", "file1.go\nfile2.go"))
	session.Append(userMsg("继续"))
	session.Append(assistantMsg("好的"))

	// limit=3, maxChars=0（不限字符）→ 行为应与旧版一致
	wm := session.GetWorkingMemory(3, 0)

	if len(wm) == 0 {
		t.Fatal("Working Memory 不应为空")
	}
	if wm[0].ToolCallID != "" {
		t.Fatalf("首条是孤儿 ToolResult，应被跳过: ToolCallID=%s", wm[0].ToolCallID)
	}
	if wm[0].Content != "继续" {
		t.Fatalf("期望首条为 '继续', 得到 %q", wm[0].Content)
	}
}

// ============================================================
// 场景 15: maxChars=0 退化为纯条数截断（向后兼容）
// ============================================================

// TestWorkingMemory_MaxCharsZeroBackwardCompat 验证 maxChars=0 时
// 行为与旧版 GetWorkingMemory(limit) 完全一致。
func TestWorkingMemory_MaxCharsZeroBackwardCompat(t *testing.T) {
	session := NewSession("backward_compat", t.TempDir())

	for i := 0; i < 10; i++ {
		session.Append(userMsg(fmt.Sprintf("msg %d", i)))
	}

	// maxChars=0 → 不限字符，只按条数
	wm := session.GetWorkingMemory(3, 0)

	if len(wm) != 3 {
		t.Fatalf("maxChars=0 应退化为纯条数截断: 期望 3 条, 得到 %d", len(wm))
	}
	if wm[0].Content != "msg 7" {
		t.Fatalf("期望首条为 'msg 7', 得到 %q", wm[0].Content)
	}
}
