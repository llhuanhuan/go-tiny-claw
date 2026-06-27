package permissions

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testConfig = `version: "1.0"
rules:
  - id: "deny-rm-rf"
    pattern: 'rm\s+(-[rRf]+\s+)*(/|\*)'
    action: "deny"
    reason: "禁止递归删除根目录"
    priority: 100
    enabled: true
  - id: "ask-rm"
    pattern: 'rm\s+.*'
    action: "ask"
    reason: "删除文件需要确认"
    priority: 50
    enabled: true
  - id: "allow-ls"
    pattern: 'ls\s*.*'
    action: "allow"
    reason: "只读命令允许"
    priority: 10
    enabled: true
settings:
  default_action: "ask"
  hot_reload_interval: 1
`

func TestEngine_Load(t *testing.T) {
	// 创建临时配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "permissions.yaml")
	os.WriteFile(configPath, []byte(testConfig), 0644)

	engine := NewEngine(configPath)
	if err := engine.Load(); err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	stats := engine.GetStats()
	if !stats["compiled"].(bool) {
		t.Error("期望引擎已编译")
	}
	if stats["rules_count"].(int) != 3 {
		t.Errorf("期望 3 条规则，实际 %d", stats["rules_count"])
	}
}

func TestEngine_Check_Deny(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "permissions.yaml")
	os.WriteFile(configPath, []byte(testConfig), 0644)

	engine := NewEngine(configPath)
	engine.Load()

	result := engine.Check(context.Background(), "rm -rf /")
	if result.Action != ActionDeny {
		t.Errorf("期望 deny，实际 %s", result.Action)
	}
	if result.RuleID != "deny-rm-rf" {
		t.Errorf("期望匹配 deny-rm-rf，实际 %s", result.RuleID)
	}
}

func TestEngine_Check_Ask(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "permissions.yaml")
	os.WriteFile(configPath, []byte(testConfig), 0644)

	engine := NewEngine(configPath)
	engine.Load()

	result := engine.Check(context.Background(), "rm test.txt")
	if result.Action != ActionAsk {
		t.Errorf("期望 ask，实际 %s", result.Action)
	}
	if result.RuleID != "ask-rm" {
		t.Errorf("期望匹配 ask-rm，实际 %s", result.RuleID)
	}
}

func TestEngine_Check_Allow(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "permissions.yaml")
	os.WriteFile(configPath, []byte(testConfig), 0644)

	engine := NewEngine(configPath)
	engine.Load()

	result := engine.Check(context.Background(), "ls -la")
	if result.Action != ActionAllow {
		t.Errorf("期望 allow，实际 %s", result.Action)
	}
	if result.RuleID != "allow-ls" {
		t.Errorf("期望匹配 allow-ls，实际 %s", result.RuleID)
	}
}

func TestEngine_Check_Default(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "permissions.yaml")
	os.WriteFile(configPath, []byte(testConfig), 0644)

	engine := NewEngine(configPath)
	engine.Load()

	// 没有匹配的规则，使用默认策略 (ask)
	result := engine.Check(context.Background(), "unknown-command")
	if result.Action != ActionAsk {
		t.Errorf("期望 ask（默认），实际 %s", result.Action)
	}
	if result.Matched {
		t.Error("期望未匹配到规则")
	}
}

func TestEngine_HotReload(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "permissions.yaml")
	os.WriteFile(configPath, []byte(testConfig), 0644)

	engine := NewEngine(configPath)
	engine.Load()

	// 初始检查
	result := engine.Check(context.Background(), "rm test.txt")
	if result.Action != ActionAsk {
		t.Errorf("初始配置：期望 ask，实际 %s", result.Action)
	}

	// 修改配置文件（添加 allow 规则）
	newConfig := `version: "1.0"
rules:
  - id: "allow-rm"
    pattern: 'rm\s+.*'
    action: "allow"
    reason: "允许删除"
    priority: 100
    enabled: true
settings:
  default_action: "ask"
  hot_reload_interval: 1
`
	time.Sleep(200 * time.Millisecond) // 确保文件系统时间戳更新
	os.WriteFile(configPath, []byte(newConfig), 0644)

	// 启动热更新（使用较短的检查间隔）
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go engine.StartHotReload(ctx)

	// 等待热更新（增加等待时间）
	time.Sleep(3 * time.Second)

	// 再次检查
	result = engine.Check(context.Background(), "rm test.txt")
	if result.Action != ActionAllow {
		t.Errorf("热更新后：期望 allow，实际 %s", result.Action)
	}
}

func TestEngine_Stats(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "permissions.yaml")
	os.WriteFile(configPath, []byte(testConfig), 0644)

	engine := NewEngine(configPath)
	engine.Load()

	// 执行多次检查
	engine.Check(context.Background(), "rm -rf /")
	engine.Check(context.Background(), "rm test.txt")
	engine.Check(context.Background(), "ls -la")
	engine.Check(context.Background(), "unknown")

	stats := engine.GetStats()
	if stats["total_checks"].(int64) != 4 {
		t.Errorf("期望 4 次检查，实际 %d", stats["total_checks"])
	}
	if stats["deny_count"].(int64) != 1 {
		t.Errorf("期望 1 次 deny，实际 %d", stats["deny_count"])
	}
	if stats["ask_count"].(int64) != 2 { // rm + unknown (default)
		t.Errorf("期望 2 次 ask，实际 %d", stats["ask_count"])
	}
	if stats["allow_count"].(int64) != 1 {
		t.Errorf("期望 1 次 allow，实际 %d", stats["allow_count"])
	}
}
