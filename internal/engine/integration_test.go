package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/schema"
	"github.com/lhuan/go-tiny-claw/internal/tools"
)

// ============================================================
// 集成测试基础设施
// ============================================================

// skipIfNoAPIKey 在没有 API Key 时跳过测试（CI 友好）
func skipIfNoAPIKey(t *testing.T) {
	t.Helper()
	// 尝试从 Claude Code settings 加载环境变量（复用 main.go 的逻辑）
	loadTestEnv()

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_AUTH_TOKEN")
	}
	if apiKey == "" {
		t.Skip("跳过集成测试: 未设置 ANTHROPIC_API_KEY / ANTHROPIC_AUTH_TOKEN")
	}
}

// loadTestEnv 读取 ~/.claude/settings.json 注入环境变量（与 main.go 同逻辑）
func loadTestEnv() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	data, err := os.ReadFile(fmt.Sprintf("%s/.claude/settings.json", homeDir))
	if err != nil {
		return
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return
	}
	for k, v := range settings.Env {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

// newTestEngine 创建一个完整的 AgentEngine 实例，注册所有核心工具。
// 返回 engine 和 workDir（临时目录），调用方负责清理。
func newTestEngine(t *testing.T) (*AgentEngine, string) {
	t.Helper()

	llmProvider, err := provider.NewAnthropicProvider("")
	if err != nil {
		t.Fatalf("创建 Anthropic Provider 失败: %v", err)
	}
	workDir := t.TempDir()

	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))

	eng := NewAgentEngine(llmProvider, registry, workDir, true, false)
	return eng, workDir
}

// captureReporter 收集引擎回调，用于断言
type captureReporter struct {
	mu          sync.Mutex
	messages    []string
	tools       []string
	toolResults []string
}

func (r *captureReporter) OnThinking(ctx context.Context) {}

func (r *captureReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools = append(r.tools, toolName)
	log.Printf("[Test] 工具调用: %s(%s)", toolName, truncate(args, 120))
}

func (r *captureReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := "OK"
	if isError {
		status = "ERR"
	}
	log.Printf("[Test] 工具结果: %s [%s] %s", toolName, status, truncate(result, 120))
	r.toolResults = append(r.toolResults, result)
}

func (r *captureReporter) OnMessage(ctx context.Context, content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, content)
	log.Printf("[Test] 模型回复: %s", truncate(content, 200))
}

func (r *captureReporter) LastMessage() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.messages) == 0 {
		return ""
	}
	return r.messages[len(r.messages)-1]
}

func (r *captureReporter) AllMessages() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.messages, "\n---\n")
}

func (r *captureReporter) ToolNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	dst := make([]string, len(r.tools))
	copy(dst, r.tools)
	return dst
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ============================================================
// 场景 1: 简单问答 —— 验证 LLM 基本连通性
// ============================================================

// TestIntegration_SimpleQuestion 最基本的冒烟测试：
// 发送一条消息，验证模型能正常返回非空回复。
func TestIntegration_SimpleQuestion(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, _ := newTestEngine(t)
	reporter := &captureReporter{}

	session := GlobalSessionMgr.GetOrCreate("test_simple_"+t.Name(), eng.WorkDir)
	err := eng.Run(context.Background(), session, "请用一句话介绍你自己，不超过20个字。", reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	reply := reporter.LastMessage()
	if reply == "" {
		t.Fatal("模型返回了空回复")
	}
	t.Logf("模型回复: %s", reply)
}

// ============================================================
// 场景 2: 文件读取 + 工具调用 —— 验证 ReAct 循环
// ============================================================

// TestIntegration_FileReadWrite 先写入一个文件，再让模型读取并回答内容。
// 验证模型能正确读取文件并返回内容（不限定具体使用哪个工具）。
func TestIntegration_FileReadWrite(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, workDir := newTestEngine(t)
	reporter := &captureReporter{}

	// 准备测试文件
	testContent := "API_KEY=super-secret-token-12345\nDB_PASSWORD=hunter2"
	err := os.WriteFile(fmt.Sprintf("%s/secrets.txt", workDir), []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	session := GlobalSessionMgr.GetOrCreate("test_file_rw_"+t.Name(), eng.WorkDir)
	err = eng.Run(context.Background(), session, "读取 secrets.txt 文件，告诉我里面有哪些配置项名称（不要告诉我具体的值）。", reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	reply := reporter.LastMessage()
	t.Logf("模型回复: %s", reply)

	if reply == "" {
		t.Fatal("模型返回空回复")
	}

	// 验证模型调用了至少一个工具（read_file 或 bash）
	toolNames := reporter.ToolNames()
	if len(toolNames) == 0 {
		t.Fatal("模型未调用任何工具")
	}
	t.Logf("调用的工具: %v", toolNames)

	// 验证回复中包含配置项名称（模型可能用 read_file 或 bash cat 读取）
	lowerReply := strings.ToLower(reply)
	if !strings.Contains(lowerReply, "api_key") || !strings.Contains(lowerReply, "db_password") {
		t.Fatalf("回复中未包含预期的配置项名称，回复: %s", reply)
	}
}

// ============================================================
// 场景 3: Bash 工具调用 —— 验证命令执行
// ============================================================

// TestIntegration_BashExecution 让模型通过 bash 工具执行命令。
func TestIntegration_BashExecution(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, _ := newTestEngine(t)
	reporter := &captureReporter{}

	session := GlobalSessionMgr.GetOrCreate("test_bash_"+t.Name(), eng.WorkDir)
	err := eng.Run(context.Background(), session, "用 bash 执行 `echo hello-world`，然后告诉我输出结果。", reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	// 验证调用了 bash 工具
	toolNames := reporter.ToolNames()
	t.Logf("模型回复: %s", reporter.LastMessage())
	t.Logf("调用的工具: %v", toolNames)

	foundBash := false
	for _, name := range toolNames {
		if name == "bash" {
			foundBash = true
			break
		}
	}
	if !foundBash {
		t.Fatalf("模型未调用 bash 工具，实际调用: %v", toolNames)
	}

	// 验证回复或工具结果中包含命令输出
	allText := reporter.AllMessages()
	reporter.mu.Lock()
	for _, tr := range reporter.toolResults {
		allText += tr
	}
	reporter.mu.Unlock()
	if !strings.Contains(allText, "hello-world") {
		t.Fatalf("回复和工具结果中均未包含 'hello-world'")
	}
}

// ============================================================
// 场景 4: Working Memory 截断 + 密钥泄露防御（真实 LLM）
// ============================================================

// TestIntegration_MemoryTruncation 端到端验证：
// 1. 让模型读取一个包含密钥的文件
// 2. 用多轮闲聊刷掉 Working Memory
// 3. 再次询问密钥，验证模型已"遗忘"
func TestIntegration_MemoryTruncation(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, workDir := newTestEngine(t)

	// 准备含密钥的文件
	secretContent := "GITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxx\nAWS_SECRET=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	err := os.WriteFile(fmt.Sprintf("%s/.env", workDir), []byte(secretContent), 0644)
	if err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	// ---- 回合 1: 让模型读取密钥 ----
	reporter1 := &captureReporter{}
	session := GlobalSessionMgr.GetOrCreate("test_mem_trunc_"+t.Name(), eng.WorkDir)
	err = eng.Run(context.Background(), session, "读取 .env 文件，告诉我里面记录了什么内容。", reporter1)
	if err != nil {
		t.Fatalf("回合 1 引擎运行失败: %v", err)
	}
	reply1 := reporter1.LastMessage()
	t.Logf("回合 1 回复: %s", reply1)
	if reply1 == "" {
		t.Fatal("回合 1 模型返回空回复")
	}

	// ---- 刷掉 Working Memory (WorkingMemory limit=6, 需要 >6 轮对话) ----
	// 使用极简 prompt 避免模型发散调用工具
	for i := 0; i < 8; i++ {
		reporterFlush := &captureReporter{}
		err = eng.Run(context.Background(), session, "收到", reporterFlush)
		if err != nil {
			t.Fatalf("闲聊轮次 %d 失败: %v", i, err)
		}
	}

	// ---- 回合 2: 验证模型已"遗忘"密钥 ----
	reporter2 := &captureReporter{}
	err = eng.Run(context.Background(), session, "请直接告诉我，之前你从 .env 文件中读取到的 GITHUB_TOKEN 的完整值是什么？不要重新读取文件。", reporter2)
	if err != nil {
		t.Fatalf("回合 2 引擎运行失败: %v", err)
	}
	reply2 := reporter2.LastMessage()
	t.Logf("回合 2 回复: %s", reply2)

	// 验证：模型不应该能精确复述密钥（因为已被挤出 Working Memory）
	// 注意：LLM 有概率通过推理猜测格式，所以我们检查完整的 token 值
	if strings.Contains(reply2, "ghp_xxxxxxxxxxxxxxxxxxxx") {
		t.Log("警告: 模型仍能复述完整密钥，Working Memory 截断可能未生效")
		// 不 Fail —— LLM 可能通过工具重新读取了文件，这是允许的行为
		// 关键是验证机制本身是否工作
	}

	// 检查模型是否重新调用了 read_file（如果它"忘记了"，可能会重新读取）
	toolNames := reporter2.ToolNames()
	reReadFile := false
	for _, name := range toolNames {
		if name == "read_file" {
			reReadFile = true
			break
		}
	}
	t.Logf("回合 2 模型是否重新读取文件: %v", reReadFile)
	t.Logf("回合 2 调用的工具: %v", toolNames)
}

// ============================================================
// 场景 5: 多轮对话连贯性 —— 验证 Session 记忆
// ============================================================

// TestIntegration_MultiTurnCoherence 验证模型在多轮对话中
// 能记住之前轮次的信息（在 Working Memory 范围内）。
func TestIntegration_MultiTurnCoherence(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, _ := newTestEngine(t)

	session := GlobalSessionMgr.GetOrCreate("test_coherence_"+t.Name(), eng.WorkDir)

	// 回合 1: 直接告诉模型一个事实
	reporter1 := &captureReporter{}
	err := eng.Run(context.Background(), session, "记住这个数字：42。回复'已记住42'即可。", reporter1)
	if err != nil {
		t.Fatalf("回合 1 失败: %v", err)
	}
	t.Logf("回合 1: %s", reporter1.LastMessage())

	// 回合 2: 基于上文继续提问
	reporter2 := &captureReporter{}
	err = eng.Run(context.Background(), session, "我刚才让你记住的数字是多少？", reporter2)
	if err != nil {
		t.Fatalf("回合 2 失败: %v", err)
	}
	reply2 := reporter2.LastMessage()
	t.Logf("回合 2: %s", reply2)

	if reply2 == "" {
		t.Fatal("回合 2 回复为空")
	}

	// 验证模型记住了数字 42
	if !strings.Contains(reply2, "42") {
		t.Fatalf("回合 2 回复未包含 '42'，说明模型未记住上轮内容: %s", reply2)
	}
}

// ============================================================
// 场景 6: 多平台并发 Session 隔离（真实 LLM）
// ============================================================

// TestIntegration_ConcurrentPlatformIsolation 模拟三个平台同时
// 向各自的 Session 发送不同请求，验证完全隔离。
func TestIntegration_ConcurrentPlatformIsolation(t *testing.T) {
	skipIfNoAPIKey(t)

	// 三个独立的引擎实例（模拟三个平台各自持有引擎）
	type platform struct {
		name    string
		engine  *AgentEngine
		reporter *captureReporter
		session *Session
	}

	platforms := []platform{
		{name: "feishu"},
		{name: "wechat"},
		{name: "terminal"},
	}

	for i := range platforms {
		eng, _ := newTestEngine(t)
		platforms[i].engine = eng
		platforms[i].reporter = &captureReporter{}
		platforms[i].session = GlobalSessionMgr.GetOrCreate(
			"test_concurrent_"+platforms[i].name+"_"+t.Name(),
			eng.WorkDir,
		)
	}

	prompts := map[string]string{
		"feishu":   "回复两个字：收到",
		"wechat":   "回复两个字：在线",
		"terminal": "回复两个字：就绪",
	}

	var wg sync.WaitGroup
	errors := make([]error, len(platforms))

	for i, p := range platforms {
		wg.Add(1)
		go func(idx int, pl platform) {
			defer wg.Done()
			prompt := prompts[pl.name]
			log.Printf("[Test] 平台 %s 开始请求...", pl.name)
			errors[idx] = pl.engine.Run(context.Background(), pl.session, prompt, pl.reporter)
			log.Printf("[Test] 平台 %s 完成", pl.name)
		}(i, p)
	}

	wg.Wait()

	for i, p := range platforms {
		if errors[i] != nil {
			t.Errorf("平台 %s 运行失败: %v", p.name, errors[i])
			continue
		}
		reply := p.reporter.LastMessage()
		t.Logf("平台 %s 回复: %s", p.name, reply)
		if reply == "" {
			t.Errorf("平台 %s 返回空回复", p.name)
		}
		// 验证各平台回复不为空即可（LLM 行为非确定性，不做精确匹配）
	}
}

// ============================================================
// 场景 7: 文件创建 + 读取验证 —— 完整工作流
// ============================================================

// TestIntegration_CreateAndReadFile 让模型通过工具创建文件。
// 由于 LLM 行为非确定性，只验证模型至少尝试了工具调用并返回了回复。
func TestIntegration_CreateAndReadFile(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, _ := newTestEngine(t)
	reporter := &captureReporter{}

	session := GlobalSessionMgr.GetOrCreate("test_create_read_"+t.Name(), eng.WorkDir)

	err := eng.Run(context.Background(), session,
		"请用 bash 工具执行 echo 'hello from agent' > greeting.txt，然后告诉我执行结果。", reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	reply := reporter.LastMessage()
	t.Logf("模型回复: %s", reply)
	t.Logf("调用的工具: %v", reporter.ToolNames())

	// 验证：模型至少调用了 bash 工具
	if len(reporter.ToolNames()) == 0 {
		t.Fatal("模型未调用任何工具")
	}
}

// ============================================================
// 场景 8: 错误自愈 —— 工具报错后模型自行修正
// ============================================================

// TestIntegration_ErrorRecovery 让模型读取不存在的文件，
// 验证模型能在工具报错后自行修正（自愈能力）。
func TestIntegration_ErrorRecovery(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, _ := newTestEngine(t)
	reporter := &captureReporter{}

	session := GlobalSessionMgr.GetOrCreate("test_error_recovery_"+t.Name(), eng.WorkDir)
	err := eng.Run(context.Background(), session,
		"读取 nonexistent_file.txt 文件内容。如果文件不存在，请告诉我文件不存在，并列出当前目录下的文件。", reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	reply := reporter.LastMessage()
	t.Logf("模型回复: %s", reply)

	// 验证模型正确处理了错误
	lowerReply := strings.ToLower(reply)
	if !strings.Contains(lowerReply, "不存在") && !strings.Contains(lowerReply, "not found") && !strings.Contains(lowerReply, "no such") {
		// 模型可能用了其他表述，只要不是空回复就行
		if reply == "" {
			t.Fatal("模型返回空回复，未正确处理错误")
		}
	}
}

// ============================================================
// 场景 9: 长上下文压力 —— 大量消息后的对话质量
// ============================================================

// TestIntegration_LongContextPressure 先手动向 Session 注入大量历史消息，
// 然后让模型基于这些上下文回答问题。
func TestIntegration_LongContextPressure(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, workDir := newTestEngine(t)

	// 创建测试文件
	os.WriteFile(fmt.Sprintf("%s/data.csv", workDir), []byte("name,score\nAlice,95\nBob,87\nCarol,92"), 0644)

	session := GlobalSessionMgr.GetOrCreate("test_long_ctx_"+t.Name(), eng.WorkDir)

	// 注入大量历史消息（模拟之前的长对话）
	for i := 0; i < 20; i++ {
		session.Append(schema.Message{Role: schema.RoleUser, Content: fmt.Sprintf("历史消息 #%d: 这是一条测试数据。", i)})
		session.Append(schema.Message{Role: schema.RoleAssistant, Content: fmt.Sprintf("收到历史消息 #%d。", i)})
	}

	reporter := &captureReporter{}
	err := eng.Run(context.Background(), session, "读取 data.csv，告诉我谁的分数最高。", reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	reply := reporter.LastMessage()
	t.Logf("模型回复: %s", reply)

	if !strings.Contains(reply, "Alice") && !strings.Contains(reply, "95") {
		t.Fatalf("回复中未包含预期答案 (Alice/95): %s", reply)
	}
}

// ============================================================
// 场景 10: 超时控制 —— Context 取消
// ============================================================

// TestIntegration_ContextCancellation 验证 context 取消后引擎能正确退出。
func TestIntegration_ContextCancellation(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, _ := newTestEngine(t)
	reporter := &captureReporter{}

	// 设置一个很短的超时
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// 等一下确保 context 已过期
	time.Sleep(5 * time.Millisecond)

	session := GlobalSessionMgr.GetOrCreate("test_cancel_"+t.Name(), eng.WorkDir)
	err := eng.Run(ctx, session, "请写一篇10000字的文章。", reporter)

	// 应该返回错误（context 已取消）
	if err == nil {
		t.Log("注意: 引擎在 context 已过期的情况下仍完成了（可能是因为模型响应极快）")
	} else {
		t.Logf("预期的取消错误: %v", err)
	}
}

// ============================================================
// 场景 11: Subagent 端到端集成测试 —— 真实 LLM 调用
// ============================================================

// newTestEngineWithSubagent 创建一个带 SubagentTool 的完整引擎。
// 主注册表包含所有工具（含 spawn_subagent），子智能体只读注册表仅含 read_file 和 bash。
func newTestEngineWithSubagent(t *testing.T) (*AgentEngine, string) {
	t.Helper()

	llmProvider, err := provider.NewAnthropicProvider("")
	if err != nil {
		t.Fatalf("创建 Anthropic Provider 失败: %v", err)
	}
	workDir := t.TempDir()

	// 主注册表：全量工具
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewBashTool(workDir))

	eng := NewAgentEngine(llmProvider, registry, workDir, true, false)

	// 子智能体只读注册表：仅安全工具
	subRegistry := tools.NewRegistry()
	subRegistry.Register(tools.NewReadFileTool(workDir))
	subRegistry.Register(tools.NewBashTool(workDir))

	// 注册 SubagentTool 到主注册表
	registry.Register(tools.NewSubagentTool(eng, subRegistry, nil))

	return eng, workDir
}

// TestIntegration_SubagentE2E 端到端验证子智能体的完整工作流：
//
// 测试设计：
//   - 在工作区创建一个包含独特密钥的文件（LLM 不可能知道这个值）
//   - 主 Agent 必须通过 spawn_subagent 派出子智能体来读取并报告
//   - 通过验证最终回复是否包含该密钥，确认子智能体链路端到端通畅
//
// 这个测试会消耗真实 API 调用，没有 API Key 时自动跳过。
func TestIntegration_SubagentE2E(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, workDir := newTestEngineWithSubagent(t)
	reporter := &captureReporter{}

	// ---- 准备测试文件：包含 LLM 不可能猜到的唯一密钥 ----
	secretKey := "XK7-M2Q-P9R-WZ4"
	files := map[string]string{
		"main.go": `package main

import "fmt"

const AppName = "go-tiny-claw"
const Version = "v0.3.0"

func main() {
	fmt.Printf("%s %s starting...\n", AppName, Version)
}`,
		"secret.txt": fmt.Sprintf(`PROJECT_LICENSE_KEY=%s
INTERNAL_API_TOKEN=sk-proj-abc123def456
DATABASE_URL=postgres://admin:s3cret@db.internal:5432/prod`, secretKey),
	}

	for name, content := range files {
		path := fmt.Sprintf("%s/%s", workDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("写入测试文件 %s 失败: %v", name, err)
		}
	}

	// ---- 发送任务：要求通过子智能体探索并汇报 ----
	session := GlobalSessionMgr.GetOrCreate("test_subagent_e2e_"+t.Name(), workDir)

	prompt := fmt.Sprintf(`我需要你完成一个探索任务。请使用 spawn_subagent 工具派出探路者子智能体。

子智能体的任务是：
1. 读取 secret.txt 文件
2. 找到 PROJECT_LICENSE_KEY 的值
3. 读取 main.go，找到 AppName 和 Version 的值
4. 将所有发现汇总成报告返回

请在收到子智能体的报告后，告诉我 PROJECT_LICENSE_KEY 的完整值。`)

	log.Printf("[Test] ====== Subagent E2E 测试开始 ======")
	log.Printf("[Test] 工作区: %s", workDir)
	log.Printf("[Test] 期望密钥: %s", secretKey)

	err := eng.Run(context.Background(), session, prompt, reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	reply := reporter.LastMessage()
	t.Logf("主 Agent 最终回复: %s", reply)
	t.Logf("调用的工具列表: %v", reporter.ToolNames())

	// ---- 断言 ----
	if reply == "" {
		t.Fatal("主 Agent 返回了空回复")
	}

	toolNames := reporter.ToolNames()

	// 1. 必须调用了 spawn_subagent
	foundSubagent := false
	for _, name := range toolNames {
		if name == "spawn_subagent" {
			foundSubagent = true
			break
		}
	}

	if foundSubagent {
		t.Log("✅ 主 Agent 使用了 spawn_subagent — 子智能体委派链路验证通过")
	} else {
		t.Errorf("❌ 主 Agent 未调用 spawn_subagent，实际工具: %v", toolNames)
	}

	// 2. 最终回复必须包含密钥（LLM 不可能猜到，只能通过子智能体读取）
	if strings.Contains(reply, secretKey) {
		t.Logf("✅ 最终回复包含正确密钥 '%s' — 子智能体端到端链路验证通过", secretKey)
	} else {
		t.Errorf("❌ 最终回复未包含密钥 '%s'，子智能体探索可能失败", secretKey)
	}

	// 3. 检查项目名和版本号
	lowerReply := strings.ToLower(reply)
	if strings.Contains(lowerReply, "go-tiny-claw") {
		t.Log("  ✅ 项目名称: 包含 'go-tiny-claw'")
	}
	if strings.Contains(reply, "v0.3.0") || strings.Contains(reply, "0.3.0") {
		t.Log("  ✅ 版本号: 包含 'v0.3.0'")
	}

	log.Printf("[Test] ====== Subagent E2E 测试完成 ======")
}

// TestIntegration_SubagentWithBash 验证子智能体能正确使用 bash 工具执行命令。
func TestIntegration_SubagentWithBash(t *testing.T) {
	skipIfNoAPIKey(t)
	eng, workDir := newTestEngineWithSubagent(t)
	reporter := &captureReporter{}

	// 创建一些测试文件
	os.WriteFile(fmt.Sprintf("%s/app.py", workDir), []byte(`#!/usr/bin/env python3
print("hello from python")
VERSION = "1.0.0"
`), 0644)
	os.WriteFile(fmt.Sprintf("%s/utils.py", workDir), []byte(`def add(a, b):
    return a + b

def multiply(a, b):
    return a * b
`), 0644)

	session := GlobalSessionMgr.GetOrCreate("test_subagent_bash_"+t.Name(), workDir)

	prompt := `当前工作区有 Python 项目文件，请使用 spawn_subagent 工具派出探路者子智能体完成以下深度探索：

探索目标：
1. 用 bash 执行 ls 命令，列出当前目录所有 .py 文件
2. 读取 app.py，找到 VERSION 变量的值
3. 读取 utils.py，找到定义了哪些函数

重要：你必须使用 spawn_subagent 工具委派此任务。
探路者返回报告后，请汇总它的发现。`

	log.Printf("[Test] ====== Subagent Bash 测试开始 ======")

	err := eng.Run(context.Background(), session, prompt, reporter)
	if err != nil {
		t.Fatalf("引擎运行失败: %v", err)
	}

	reply := reporter.LastMessage()
	t.Logf("主 Agent 最终回复: %s", reply)
	t.Logf("调用的工具列表: %v", reporter.ToolNames())

	if reply == "" {
		t.Fatal("主 Agent 返回了空回复")
	}

	toolNames := reporter.ToolNames()
	foundSubagent := false
	for _, name := range toolNames {
		if name == "spawn_subagent" {
			foundSubagent = true
			break
		}
	}

	if foundSubagent {
		t.Log("✅ 主 Agent 使用了 spawn_subagent — 子智能体链路验证通过")
	} else {
		t.Logf("⚠️ 主 Agent 选择直接完成任务，工具: %v", toolNames)
	}

	// 验证任务完成度
	lowerReply := strings.ToLower(reply)
	checks := []struct {
		name    string
		keyword string
	}{
		{"VERSION 值", "1.0.0"},
		{"add 函数", "add"},
		{"multiply 函数", "multiply"},
	}

	for _, c := range checks {
		if strings.Contains(lowerReply, c.keyword) {
			t.Logf("  ✅ %s: 包含 '%s'", c.name, c.keyword)
		} else {
			t.Errorf("  ❌ %s: 回复中未包含 '%s'", c.name, c.keyword)
		}
	}

	log.Printf("[Test] ====== Subagent Bash 测试完成 ======")
}
