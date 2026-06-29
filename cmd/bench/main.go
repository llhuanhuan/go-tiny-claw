package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/lhuan/go-tiny-claw/internal/eval"
	"github.com/lhuan/go-tiny-claw/internal/provider"
)

func main() {
	// 1. 初始化 LLM Provider（参考 cmd/claw/main.go 的 detectProvider 模式）
	rawProvider, modelName := detectProvider()

	// 2. 构建跑分执行器，注入检测到的 Provider
	runner := eval.NewBenchmarkRunner(modelName)
	runner.ProviderFactory = func(model string) provider.LLMProvider {
		return rawProvider
	}

	// 3. 构建一套微型评测集
	testcases := []eval.TestCase{
		{
			ID:   "test_001_edit",
			Name: "测试模糊替换工具的准确性",
			// 准备靶机：生成一个有错误的 json 文件
			SetupScript: `echo '{"name": "tiny-claw", "version": "v1.0.0"}' > config.json`,
			// 考题：要求修改版本号
			TaskPrompt: `当前目录下有一个 config.json。请你使用 edit_file 工具，将其中的 version 从 v1.0.0 改为 v2.0.0。不要做其他多余操作。`,
			// 判卷脚本：使用 grep 检查文件是否包含 v2.0.0
			ValidateScript: `grep '"version": "v2.0.0"' config.json`,
		},
		{
			ID:   "test_002_code_gen",
			Name: "测试代码阅读与创建新文件的综合能力",
			// 准备靶机：生成一个简单的乘法函数
			SetupScript: `printf 'package math\n\nfunc Multiply(a, b int) int {\n\treturn a * b\n}\n' > math.go`,
			// 考题：要求 Agent 根据刚才的代码，自己去写一份单元测试
			TaskPrompt: `当前目录下有一个 math.go。请你仔细阅读它，然后在同级目录下，帮我写一个规范的单元测试文件 math_test.go，用来测试 Multiply 函数。请务必包含正常的测试用例。`,
			// 判卷脚本：直接运行 go test！如果不通过则直接 0 分。
			ValidateScript: `go mod init bench && go test -v ./...`,
		},
	}

	// 4. 启动跑分执行器！
	runner.RunSuite(context.Background(), testcases)
}

// detectProvider 参考 cmd/claw/main.go 的同名函数：
// 自动从 ~/.claude/settings.json 注入环境变量，根据可用的 API Key 选择 Provider。
// 优先使用 ZHIPU_API_KEY (glm-4.5-air)，回退到 Anthropic (Claude Code 配置)。
func detectProvider() (provider.LLMProvider, string) {
	loadClaudeCodeEnv()

	switch {
	case os.Getenv("ZHIPU_API_KEY") != "":
		log.Println("[Bootstrap] 检测到 ZHIPU_API_KEY，使用 glm-4.5-air 模型")
		return provider.NewZhipuOpenAIProvider("glm-4.5-air"), "glm-4.5-air"
	case os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "":
		log.Println("[Bootstrap] 检测到 Anthropic 凭据，使用 Anthropic Provider")
		return provider.NewAnthropicProvider(""), "anthropic"
	default:
		log.Fatal("请设置 ZHIPU_API_KEY 或 ANTHROPIC_API_KEY（可通过 Claude Code settings.json 配置）")
		return nil, ""
	}
}

// claudeCodeSettings 是 ~/.claude/settings.json 的部分结构。
type claudeCodeSettings struct {
	Env map[string]string `json:"env"`
}

// loadClaudeCodeEnv 读取 Claude Code 的 settings.json，将其中的 env 字段
// 注入到当前进程的环境变量中（不覆盖已存在的变量）。
func loadClaudeCodeEnv() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("[Bootstrap] 无法获取用户主目录: %v", err)
		return
	}

	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		log.Printf("[Bootstrap] 无法读取 Claude Code 配置 %s: %v", settingsPath, err)
		return
	}

	var settings claudeCodeSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		log.Printf("[Bootstrap] 解析 Claude Code 配置失败: %v", err)
		return
	}

	injected := 0
	for key, val := range settings.Env {
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
			injected++
		}
	}
	log.Printf("[Bootstrap] 已从 Claude Code 配置注入 %d 个环境变量", injected)
}
