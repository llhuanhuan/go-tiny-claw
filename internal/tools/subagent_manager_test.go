// internal/tools/subagent_manager_test.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// mockRunner 模拟 AgentRunner，延迟 delay 后返回预设结果
type mockRunner struct {
	delay   time.Duration
	summary string
	err     error
}

func (m *mockRunner) RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry Registry, reporter interface{}) (string, error) {
	time.Sleep(m.delay)
	return m.summary, m.err
}

// ═══════════════════════════════════════════════════════════════
// TestSubagentManager_Spawn_NonBlocking
// 验证 Spawn 立即返回，不阻塞调用者
// ═══════════════════════════════════════════════════════════════

func TestSubagentManager_Spawn_NonBlocking(t *testing.T) {
	mgr := &SubagentManager{tasks: make(map[string]*SubagentTask)}

	runner := &mockRunner{delay: 5 * time.Second, summary: "done"}

	start := time.Now()
	id := mgr.Spawn(runner, "test task", nil, nil)
	elapsed := time.Since(start)

	if elapsed > 200*time.Millisecond {
		t.Errorf("❌ Spawn 阻塞了 %v，预期立即返回", elapsed)
	}

	if !strings.HasPrefix(id, "sa_") {
		t.Errorf("❌ ID 格式错误: %s，预期 sa_N 前缀", id)
	}

	t.Logf("✅ Spawn 立即返回 ID=%s，耗时 %v", id, elapsed)
}

// ═══════════════════════════════════════════════════════════════
// TestSubagentManager_Get_AfterCompletion
// 验证子智能体完成后 Get 能读到结果
// ═══════════════════════════════════════════════════════════════

func TestSubagentManager_Get_AfterCompletion(t *testing.T) {
	mgr := &SubagentManager{tasks: make(map[string]*SubagentTask)}

	runner := &mockRunner{delay: 100 * time.Millisecond, summary: "探索报告：找到了关键代码"}

	id := mgr.Spawn(runner, "搜索关键代码", nil, nil)

	// 刚启动时应该还在运行
	snap, ok := mgr.Get(id)
	if !ok {
		t.Fatal("❌ Get 找不到子智能体")
	}
	if snap.Done {
		t.Error("❌ 子智能体不应在 100ms 内完成")
	}
	t.Logf("  启动后: Done=%v (预期 false)", snap.Done)

	// 等待完成
	time.Sleep(300 * time.Millisecond)

	snap, ok = mgr.Get(id)
	if !ok {
		t.Fatal("❌ Get 找不到子智能体")
	}
	if !snap.Done {
		t.Error("❌ 子智能体应已完成")
	}
	if snap.Error != nil {
		t.Errorf("❌ 子智能体不应报错: %v", snap.Error)
	}
	if snap.Summary != "探索报告：找到了关键代码" {
		t.Errorf("❌ 摘要不匹配: %s", snap.Summary)
	}

	t.Logf("✅ 完成后: Done=%v, Summary=%s", snap.Done, snap.Summary)
}

// ═══════════════════════════════════════════════════════════════
// TestSubagentManager_Get_Error
// 验证子智能体失败时 Get 能读到错误
// ═══════════════════════════════════════════════════════════════

func TestSubagentManager_Get_Error(t *testing.T) {
	mgr := &SubagentManager{tasks: make(map[string]*SubagentTask)}

	runner := &mockRunner{delay: 50 * time.Millisecond, err: fmt.Errorf("探索超时")}

	id := mgr.Spawn(runner, "不可能完成的任务", nil, nil)
	time.Sleep(200 * time.Millisecond)

	snap, ok := mgr.Get(id)
	if !ok {
		t.Fatal("❌ Get 找不到子智能体")
	}
	if !snap.Done {
		t.Error("❌ 子智能体应已完成")
	}
	if snap.Error == nil {
		t.Error("❌ 子智能体应有错误")
	}

	t.Logf("✅ 错误场景: Done=%v, Error=%v", snap.Done, snap.Error)
}

// ═══════════════════════════════════════════════════════════════
// TestSubagentManager_MarkNotified
// 验证通知标记机制
// ═══════════════════════════════════════════════════════════════

func TestSubagentManager_MarkNotified(t *testing.T) {
	mgr := &SubagentManager{tasks: make(map[string]*SubagentTask)}

	runner := &mockRunner{delay: 50 * time.Millisecond, summary: "done"}
	id := mgr.Spawn(runner, "task", nil, nil)
	time.Sleep(200 * time.Millisecond)

	// 未通知前
	snap, _ := mgr.Get(id)
	if snap.Notified {
		t.Error("❌ 初始状态不应已通知")
	}

	// 标记通知
	mgr.MarkNotified(id)
	snap, _ = mgr.Get(id)
	if !snap.Notified {
		t.Error("❌ 标记后应为已通知")
	}

	t.Logf("✅ 通知标记机制正常")
}

// ═══════════════════════════════════════════════════════════════
// TestSubagentManager_List
// 验证 List 返回所有子智能体
// ═══════════════════════════════════════════════════════════════

func TestSubagentManager_List(t *testing.T) {
	mgr := &SubagentManager{tasks: make(map[string]*SubagentTask)}

	runner := &mockRunner{delay: 50 * time.Millisecond, summary: "done"}
	mgr.Spawn(runner, "task1", nil, nil)
	mgr.Spawn(runner, "task2", nil, nil)
	mgr.Spawn(runner, "task3", nil, nil)

	list := mgr.List()
	if len(list) != 3 {
		t.Errorf("❌ List 应返回 3 个子智能体，实际 %d", len(list))
	}

	t.Logf("✅ List 返回 %d 个子智能体", len(list))
}

// ═══════════════════════════════════════════════════════════════
// TestCheckSubagentTool
// 验证 CheckSubagentTool 的轮询行为
// ═══════════════════════════════════════════════════════════════

func TestCheckSubagentTool(t *testing.T) {
	// 重置全局单例
	original := globalSubagentManager
	globalSubagentManager = &SubagentManager{tasks: make(map[string]*SubagentTask)}
	defer func() { globalSubagentManager = original }()

	runner := &mockRunner{delay: 100 * time.Millisecond, summary: "找到了 3 个关键文件"}
	id := GetSubagentManager().Spawn(runner, "搜索关键文件", nil, nil)

	checkTool := NewCheckSubagentTool()

	// 查询运行中的子智能体
	result, err := checkTool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"id": "%s"}`, id)))
	if err != nil {
		t.Fatalf("❌ Execute 失败: %v", err)
	}
	if !strings.Contains(result, "仍在运行中") {
		t.Errorf("❌ 预期 '仍在运行中'，实际: %s", result)
	}
	t.Logf("  运行中查询: %s", result)

	// 等待完成
	time.Sleep(300 * time.Millisecond)

	// 查询已完成的子智能体
	result, err = checkTool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"id": "%s"}`, id)))
	if err != nil {
		t.Fatalf("❌ Execute 失败: %v", err)
	}
	if !strings.Contains(result, "找到了 3 个关键文件") {
		t.Errorf("❌ 预期包含摘要，实际: %s", result)
	}
	t.Logf("✅ 完成后查询: %s", truncateStr(result, 80))

	// 查询不存在的 ID
	result, err = checkTool.Execute(context.Background(), json.RawMessage(`{"id": "sa_999"}`))
	if err != nil {
		t.Fatalf("❌ Execute 失败: %v", err)
	}
	if !strings.Contains(result, "不存在") {
		t.Errorf("❌ 预期 '不存在'，实际: %s", result)
	}
	t.Logf("  不存在查询: %s", result)
}

// ═══════════════════════════════════════════════════════════════
// TestSubagentManager_ParallelSpawn
// 验证多个子智能体并行启动，全部完成后可查询
// ═══════════════════════════════════════════════════════════════

func TestSubagentManager_ParallelSpawn(t *testing.T) {
	mgr := &SubagentManager{tasks: make(map[string]*SubagentTask)}

	// 并行启动 3 个子智能体
	runner1 := &mockRunner{delay: 100 * time.Millisecond, summary: "报告1"}
	runner2 := &mockRunner{delay: 200 * time.Millisecond, summary: "报告2"}
	runner3 := &mockRunner{delay: 300 * time.Millisecond, summary: "报告3"}

	start := time.Now()
	id1 := mgr.Spawn(runner1, "任务1", nil, nil)
	id2 := mgr.Spawn(runner2, "任务2", nil, nil)
	id3 := mgr.Spawn(runner3, "任务3", nil, nil)
	spawnElapsed := time.Since(start)

	if spawnElapsed > 200*time.Millisecond {
		t.Errorf("❌ 3 个 Spawn 总耗时 %v，预期接近瞬时", spawnElapsed)
	}

	t.Logf("  3 个 Spawn 总耗时: %v", spawnElapsed)

	// 等待全部完成
	time.Sleep(500 * time.Millisecond)

	// 验证所有结果
	for _, id := range []string{id1, id2, id3} {
		snap, ok := mgr.Get(id)
		if !ok || !snap.Done {
			t.Errorf("❌ %s 未完成", id)
		}
		t.Logf("  %s: Summary=%s", id, snap.Summary)
	}

	t.Logf("✅ 3 个子智能体并行完成")
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
