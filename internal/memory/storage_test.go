package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"feishu", "feishu:oc_abc123", "feishu_oc_abc123"},
		{"ilink", "ilink:user@im.wechat", "ilink_user@im.wechat"},
		{"no_special", "simple_id", "simple_id"},
		{"multiple_colons", "a:b:c", "a_b_c"},
		{"all_illegal", `a:b*c?"<>|/\`, "a_b_c_______"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeFilename(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestStorage_SaveAndLoadSummaries(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorage(tmpDir)
	sessionID := "test:session"

	// 保存摘要
	summary1 := Summary{
		Timestamp: time.Now(),
		TurnStart: 1,
		TurnEnd:   10,
		Content:   "用户在开发会话持久化功能",
	}
	summary2 := Summary{
		Timestamp: time.Now(),
		TurnStart: 11,
		TurnEnd:   20,
		Content:   "讨论了分层记忆系统设计",
	}

	if err := storage.SaveSummary(sessionID, summary1); err != nil {
		t.Fatalf("SaveSummary 失败: %v", err)
	}
	if err := storage.SaveSummary(sessionID, summary2); err != nil {
		t.Fatalf("SaveSummary 失败: %v", err)
	}

	// 加载摘要
	summaries, err := storage.LoadSummaries(sessionID)
	if err != nil {
		t.Fatalf("LoadSummaries 失败: %v", err)
	}

	if len(summaries) != 2 {
		t.Fatalf("期望 2 条摘要, 得到 %d", len(summaries))
	}
	if summaries[0].Content != "用户在开发会话持久化功能" {
		t.Errorf("第一条摘要内容错误: %s", summaries[0].Content)
	}
	if summaries[1].Content != "讨论了分层记忆系统设计" {
		t.Errorf("第二条摘要内容错误: %s", summaries[1].Content)
	}
}

func TestStorage_SaveAndLoadFacts(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorage(tmpDir)
	sessionID := "test:session"

	// 保存会话级事实
	fact1 := Fact{
		Type:       FactUserPreference,
		Content:    "用户偏好中文回复",
		Confidence: 0.9,
		UpdatedAt:  time.Now(),
		SessionID:  sessionID,
	}
	if err := storage.SaveFact(sessionID, fact1); err != nil {
		t.Fatalf("SaveFact 失败: %v", err)
	}

	// 保存全局事实
	globalFact := Fact{
		Type:       FactProjectState,
		Content:    "go-tiny-claw 使用 Go 语言开发",
		Confidence: 1.0,
		UpdatedAt:  time.Now(),
	}
	if err := storage.SaveGlobalFact(globalFact); err != nil {
		t.Fatalf("SaveGlobalFact 失败: %v", err)
	}

	// 加载事实（应包含全局 + 会话级）
	facts, err := storage.LoadFacts(sessionID)
	if err != nil {
		t.Fatalf("LoadFacts 失败: %v", err)
	}

	if len(facts) != 2 {
		t.Fatalf("期望 2 条事实, 得到 %d", len(facts))
	}

	// 全局事实应在前
	if facts[0].Content != "go-tiny-claw 使用 Go 语言开发" {
		t.Errorf("第一条事实应为全局事实, 得到: %s", facts[0].Content)
	}
	if facts[1].Content != "用户偏好中文回复" {
		t.Errorf("第二条事实应为会话级事实, 得到: %s", facts[1].Content)
	}
}

func TestStorage_LoadFromEmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorage(tmpDir)

	// 从空目录加载应返回空切片，不报错
	summaries, err := storage.LoadSummaries("nonexistent")
	if err != nil {
		t.Fatalf("LoadSummaries 不应报错: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("期望空切片, 得到 %d 条", len(summaries))
	}

	facts, err := storage.LoadFacts("nonexistent")
	if err != nil {
		t.Fatalf("LoadFacts 不应报错: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("期望空切片, 得到 %d 条", len(facts))
	}
}

func TestStorage_SummaryPathSanitized(t *testing.T) {
	tmpDir := t.TempDir()
	storage := NewStorage(tmpDir)
	sessionID := "feishu:oc_test123"

	// 保存摘要
	summary := Summary{
		Timestamp: time.Now(),
		TurnStart: 1,
		TurnEnd:   5,
		Content:   "测试摘要",
	}
	if err := storage.SaveSummary(sessionID, summary); err != nil {
		t.Fatalf("SaveSummary 失败: %v", err)
	}

	// 验证文件使用安全文件名
	safePath := filepath.Join(tmpDir, ".claw", "summaries", "feishu_oc_test123.jsonl")
	if _, err := os.Stat(safePath); os.IsNotExist(err) {
		t.Errorf("期望安全文件名路径存在: %s", safePath)
	}
}
