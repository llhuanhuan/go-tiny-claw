// internal/eval/benchmark_test.go
package eval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// ── Mock Provider ──────────────────────────────────────────────────────────────

// mockProvider 是一个轻量级 LLMProvider mock，不做任何真实 API 调用。
// 它在第一次 Generate 时返回固定的纯文本回复（无工具调用），让引擎在 1 个 Turn 内结束。
type mockProvider struct {
	response string // 模型返回的固定文本
}

func (m *mockProvider) Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	return &schema.Message{
		Role:    schema.RoleAssistant,
		Content: m.response,
	}, nil
}

func (m *mockProvider) StreamGenerate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: provider.StreamEventTextDelta, Delta: m.response}
	ch <- provider.StreamEvent{Type: provider.StreamEventDone}
	close(ch)
	return ch, nil
}

// ── 辅助函数 ──────────────────────────────────────────────────────────────────

// newTestRunner 创建一个使用 mock provider 的 BenchmarkRunner，不依赖真实 API。
func newTestRunner(response string) *BenchmarkRunner {
	r := NewBenchmarkRunner("mock-model")
	r.ProviderFactory = func(model string) provider.LLMProvider {
		return &mockProvider{response: response}
	}
	return r
}

// ── 单元测试 ──────────────────────────────────────────────────────────────────

func TestNewBenchmarkRunner(t *testing.T) {
	r := NewBenchmarkRunner("glm-4.5-air")
	if r.modelName != "glm-4.5-air" {
		t.Fatalf("expected modelName 'glm-4.5-air', got '%s'", r.modelName)
	}
	if r.ProviderFactory == nil {
		t.Fatal("providerFactory should not be nil")
	}
}

func TestRunSuite_PassCase(t *testing.T) {
	runner := newTestRunner("任务已完成")

	tc := TestCase{
		ID:             "ut_pass",
		Name:           "校验脚本应成功",
		TaskPrompt:     "请直接回复完成",
		ValidateScript: `echo "ok"`, // exit 0 = pass
	}

	runner.RunSuite(context.Background(), []TestCase{tc})

	// RunSuite 只打印日志，不返回值。如果没 panic 就算基本通过。
	// 我们再用 runSingleTest 做精确断言。
}

func TestRunSingleTest_Pass(t *testing.T) {
	runner := newTestRunner("done")

	tc := TestCase{
		ID:             "ut_single_pass",
		Name:           "单用例通过",
		TaskPrompt:     "hello",
		ValidateScript: `echo "pass"`,
	}

	res := runner.runSingleTest(context.Background(), tc)

	if !res.Passed {
		t.Fatalf("expected Passed=true, got false. ErrorMsg: %s", res.ErrorMsg)
	}
	if res.TestCaseID != "ut_single_pass" {
		t.Fatalf("expected TestCaseID 'ut_single_pass', got '%s'", res.TestCaseID)
	}
	if res.DurationMs < 0 {
		t.Fatalf("DurationMs should be >= 0, got %d", res.DurationMs)
	}
	if res.ErrorMsg != "" {
		t.Fatalf("expected empty ErrorMsg, got '%s'", res.ErrorMsg)
	}
}

func TestRunSingleTest_FailValidation(t *testing.T) {
	runner := newTestRunner("done")

	tc := TestCase{
		ID:             "ut_single_fail",
		Name:           "校验脚本失败",
		TaskPrompt:     "hello",
		ValidateScript: `exit 1`, // 非零退出码 = fail
	}

	res := runner.runSingleTest(context.Background(), tc)

	if res.Passed {
		t.Fatal("expected Passed=false when validation script exits non-zero")
	}
	if res.TestCaseID != "ut_single_fail" {
		t.Fatalf("expected TestCaseID 'ut_single_fail', got '%s'", res.TestCaseID)
	}
	if !strings.Contains(res.ErrorMsg, "验证脚本执行失败") {
		t.Fatalf("expected ErrorMsg to contain '验证脚本执行失败', got '%s'", res.ErrorMsg)
	}
}

func TestRunSingleTest_SetupScript(t *testing.T) {
	runner := newTestRunner("done")

	tc := TestCase{
		ID:          "ut_setup",
		Name:        "Setup 脚本创建文件",
		TaskPrompt:  "hello",
		SetupScript: `echo "hello world" > test.txt`,
		ValidateScript: `test -f test.txt && grep -q "hello world" test.txt`,
	}

	res := runner.runSingleTest(context.Background(), tc)

	if !res.Passed {
		t.Fatalf("expected Passed=true, got false. ErrorMsg: %s", res.ErrorMsg)
	}
}

func TestRunSingleTest_SetupScriptFails(t *testing.T) {
	runner := newTestRunner("done")

	tc := TestCase{
		ID:          "ut_setup_fail",
		Name:        "Setup 脚本失败",
		TaskPrompt:  "hello",
		SetupScript: `exit 1`, // setup 失败
		ValidateScript: `echo "should not reach"`,
	}

	res := runner.runSingleTest(context.Background(), tc)

	if res.Passed {
		t.Fatal("expected Passed=false when setup script fails")
	}
	if !strings.Contains(res.ErrorMsg, "靶机 Setup 失败") {
		t.Fatalf("expected ErrorMsg to contain '靶机 Setup 失败', got '%s'", res.ErrorMsg)
	}
}

func TestRunSingleTest_SandboxIsolation(t *testing.T) {
	runner := newTestRunner("done")

	tc1 := TestCase{
		ID:             "ut_iso_1",
		Name:           "沙箱隔离测试 1",
		TaskPrompt:     "hello",
		SetupScript:    `echo "from_case_1" > marker.txt`,
		ValidateScript: `test -f marker.txt && grep -q "from_case_1" marker.txt`,
	}
	tc2 := TestCase{
		ID:             "ut_iso_2",
		Name:           "沙箱隔离测试 2 — 不应看到 case_1 的文件",
		TaskPrompt:     "hello",
		ValidateScript: `test ! -f marker.txt`, // 确保 marker.txt 不存在
	}

	res1 := runner.runSingleTest(context.Background(), tc1)
	res2 := runner.runSingleTest(context.Background(), tc2)

	if !res1.Passed {
		t.Fatalf("case 1 should pass, got: %s", res1.ErrorMsg)
	}
	if !res2.Passed {
		t.Fatalf("case 2 should pass (sandbox isolated), got: %s", res2.ErrorMsg)
	}
}

func TestRunSingleTest_WorkspaceCleanedUp(t *testing.T) {
	runner := newTestRunner("done")

	tc := TestCase{
		ID:             "ut_cleanup",
		Name:           "验证沙箱目录被创建",
		TaskPrompt:     "hello",
		ValidateScript: `pwd`,
	}

	_ = runner.runSingleTest(context.Background(), tc)

	// 沙箱目录应该在 cwd/workspace/ut_cleanup_* 下
	cwd, _ := os.Getwd()
	pattern := filepath.Join(cwd, "workspace", "ut_cleanup_*")
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		t.Fatal("expected sandbox directory to be created under workspace/")
	}

	// 清理测试产物
	for _, m := range matches {
		os.RemoveAll(m)
	}
}

func TestRunSuite_PassAndFailMix(t *testing.T) {
	runner := newTestRunner("done")

	testcases := []TestCase{
		{
			ID:             "ut_mix_pass",
			Name:           "通过的用例",
			TaskPrompt:     "hello",
			ValidateScript: `true`,
		},
		{
			ID:             "ut_mix_fail",
			Name:           "失败的用例",
			TaskPrompt:     "hello",
			ValidateScript: `false`,
		},
	}

	// RunSuite 只打印日志，不会 panic
	runner.RunSuite(context.Background(), testcases)

	// 分别验证单个结果
	res1 := runner.runSingleTest(context.Background(), testcases[0])
	res2 := runner.runSingleTest(context.Background(), testcases[1])

	if !res1.Passed {
		t.Fatal("ut_mix_pass should pass")
	}
	if res2.Passed {
		t.Fatal("ut_mix_fail should fail")
	}

	// 清理 workspace
	cwd, _ := os.Getwd()
	os.RemoveAll(filepath.Join(cwd, "workspace"))
}

func TestRunSuite_EmptySuite(t *testing.T) {
	runner := newTestRunner("done")

	// 空用例集不应 panic
	runner.RunSuite(context.Background(), []TestCase{})
}

// ── 真实模型集成测试 ──────────────────────────────────────────────────────────

// loadClaudeCodeEnv 参考 cmd/claw/main.go 的做法：
// 从 ~/.claude/settings.json 读取 env 字段，注入到当前进程环境变量中。
// 这样集成测试无需手动 export 即可复用 Claude Code 已配置的 API 凭据。
func loadClaudeCodeEnv() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(homeDir, ".claude", "settings.json"))
	if err != nil {
		return
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(data, &settings) != nil {
		return
	}
	for k, v := range settings.Env {
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

// requireProvider 跳过无法创建 LLM Provider 的环境。
// 优先使用 ZHIPU_API_KEY (glm-4.5-air)，回退到 Anthropic (Claude Code 配置)。
func requireProvider(t *testing.T) string {
	t.Helper()
	loadClaudeCodeEnv()
	if os.Getenv("ZHIPU_API_KEY") != "" {
		return "zhipu"
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		return "anthropic"
	}
	t.Skip("跳过集成测试: 未设置 ZHIPU_API_KEY 或 ANTHROPIC_API_KEY/ANTHROPIC_AUTH_TOKEN")
	return ""
}

// newIntegrationRunner 根据可用的 provider 创建真实的 BenchmarkRunner。
func newIntegrationRunner(t *testing.T) *BenchmarkRunner {
	t.Helper()
	pType := requireProvider(t)
	switch pType {
	case "zhipu":
		return NewBenchmarkRunner("glm-4.5-air")
	case "anthropic":
		r := NewBenchmarkRunner("anthropic")
		r.ProviderFactory = func(model string) provider.LLMProvider {
			p, err := provider.NewAnthropicProvider("")
			if err != nil {
				t.Fatalf("创建 Anthropic Provider 失败: %v", err)
			}
			return p
		}
		return r
	}
	t.Fatal("unreachable")
	return nil
}

func TestIntegration_EditFile(t *testing.T) {
	runner := newIntegrationRunner(t)

	tc := TestCase{
		ID:   "integ_edit",
		Name: "测试 edit_file 工具的准确性",
		SetupScript: strings.Join([]string{
			`echo '{`,
			`  "name": "tiny-claw",`,
			`  "version": "v1.0.0"`,
			`}' > config.json`,
		}, "\n"),
		TaskPrompt:     `当前目录下有一个 config.json。请你使用 edit_file 工具，将其中的 "version": "v1.0.0" 改为 "version": "v2.0.0"。不要做其他多余操作。`,
		ValidateScript: `grep '"version": "v2.0.0"' config.json`,
	}

	res := runner.runSingleTest(context.Background(), tc)

	if !res.Passed {
		t.Fatalf("用例 [%s] 失败: %s", tc.ID, res.ErrorMsg)
	}
	if res.TotalCostCNY < 0 {
		t.Fatalf("TotalCostCNY 应 >= 0, got %f", res.TotalCostCNY)
	}
	t.Logf("✅ 通过 | 耗时: %dms | 花费: ¥%.6f", res.DurationMs, res.TotalCostCNY)
}

func TestIntegration_CodeGen(t *testing.T) {
	runner := newIntegrationRunner(t)

	tc := TestCase{
		ID:   "integ_codegen",
		Name: "测试代码阅读与创建新文件的综合能力",
		SetupScript: strings.Join([]string{
			`cat > math.go << 'GOEOF'`,
			`package math`,
			``,
			`func Multiply(a, b int) int {`,
			`	return a * b`,
			`}`,
			`GOEOF`,
		}, "\n"),
		TaskPrompt:     `当前目录下有一个 math.go。请你仔细阅读它，然后在同级目录下写一个 math_test.go 单元测试文件，测试 Multiply 函数。请包含正常的测试用例。写完后不要运行测试。`,
		ValidateScript: `test -f math.go && head -1 math_test.go | grep -q "package math"`,
	}

	res := runner.runSingleTest(context.Background(), tc)

	if !res.Passed {
		t.Fatalf("用例 [%s] 失败: %s", tc.ID, res.ErrorMsg)
	}
	t.Logf("✅ 通过 | 耗时: %dms | 花费: ¥%.6f", res.DurationMs, res.TotalCostCNY)
}

func TestIntegration_BashTool(t *testing.T) {
	runner := newIntegrationRunner(t)

	tc := TestCase{
		ID:             "integ_bash",
		Name:           "测试 bash 工具执行能力",
		TaskPrompt:     `请使用 bash 工具执行命令 echo "hello from agent" > output.txt，不要做其他操作。`,
		ValidateScript: `grep -q "hello from agent" output.txt`,
	}

	res := runner.runSingleTest(context.Background(), tc)

	if !res.Passed {
		t.Fatalf("用例 [%s] 失败: %s", tc.ID, res.ErrorMsg)
	}
	t.Logf("✅ 通过 | 耗时: %dms | 花费: ¥%.6f", res.DurationMs, res.TotalCostCNY)
}

func TestIntegration_RunSuite(t *testing.T) {
	runner := newIntegrationRunner(t)

	testcases := []TestCase{
		{
			ID:   "integ_suite_edit",
			Name: "Suite: 编辑文件",
			SetupScript: strings.Join([]string{
				`echo 'name=old' > app.conf`,
			}, "\n"),
			TaskPrompt:     `当前目录下有 app.conf，请用 edit_file 工具将 name=old 改为 name=new。不要做其他操作。`,
			ValidateScript: `grep -q "name=new" app.conf`,
		},
		{
			ID:   "integ_suite_create",
			Name: "Suite: 创建文件",
			TaskPrompt:     `请使用 write_file 工具创建一个 hello.txt，内容为 "benchmark ok"。不要做其他操作。`,
			ValidateScript: `grep -q "benchmark ok" hello.txt`,
		},
	}

	// RunSuite 会打印报表，我们在这里验证每个用例单独的结果
	for _, tc := range testcases {
		tc := tc
		t.Run(tc.ID, func(t *testing.T) {
			t.Parallel()
			res := runner.runSingleTest(context.Background(), tc)
			if !res.Passed {
				t.Fatalf("用例 [%s] 失败: %s", tc.ID, res.ErrorMsg)
			}
			t.Logf("✅ 通过 | 耗时: %dms | 花费: ¥%.6f", res.DurationMs, res.TotalCostCNY)
		})
	}
}
