package tools

import (
	"strings"
	"testing"
	"time"
)

// TestRingBufferBasic 验证环形缓冲区的基本读写
func TestRingBufferBasic(t *testing.T) {
	rb := NewRingBuffer(16)

	// 写入 10 字节,未满
	rb.Write([]byte("hello"))
	rb.Write([]byte("world"))
	if s := rb.String(); s != "helloworld" {
		t.Fatalf("期望 helloworld, 得到 %q", s)
	}
	if rb.Len() != 10 {
		t.Fatalf("期望 len=10, 得到 %d", rb.Len())
	}

	// 写入 10 字节,超出容量 16,旧数据应被覆盖
	rb.Write([]byte("1234567890"))
	result := rb.String()
	if len(result) != 16 {
		t.Fatalf("期望 len=16 (容量上限), 得到 %d: %q", len(result), result)
	}
	// 10 + 10 = 20 字节写入 16 容量,最旧 4 字节 "hell" 被覆盖
	// 剩余: "oworld" + "1234567890" = 16 字节
	if result != "oworld1234567890" {
		t.Fatalf("期望 oworld1234567890, 得到 %q", result)
	}
}

// TestRingBufferReset 验证重置功能
func TestRingBufferReset(t *testing.T) {
	rb := NewRingBuffer(64)
	rb.Write([]byte("some data"))
	rb.Reset()
	if rb.Len() != 0 {
		t.Fatalf("重置后 len 应为 0, 得到 %d", rb.Len())
	}
	if rb.String() != "" {
		t.Fatalf("重置后应为空字符串, 得到 %q", rb.String())
	}
	// 重置后可以继续写入
	rb.Write([]byte("new data"))
	if rb.String() != "new data" {
		t.Fatalf("重置后写入失败: %q", rb.String())
	}
}

// TestTaskManagerSpawnAndWait 验证后台进程启动和正常退出
func TestTaskManagerSpawnAndWait(t *testing.T) {
	tm := GetTaskManager()

	// 启动一个短命后台进程
	taskID, err := tm.Spawn("echo hello-from-background && sleep 0.5", "")
	if err != nil {
		t.Fatalf("Spawn 失败: %v", err)
	}
	if !strings.HasPrefix(taskID, "bg_") {
		t.Fatalf("Task ID 格式异常: %s", taskID)
	}

	// 立即查询 —— 应该还在运行 (或刚好完成)
	snap, ok := tm.Get(taskID)
	if !ok {
		t.Fatalf("Task %s 刚创建却找不到", taskID)
	}
	if snap.Command != "echo hello-from-background && sleep 0.5" {
		t.Fatalf("命令记录不一致: %s", snap.Command)
	}

	// 等待进程退出 (最多 3 秒)
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatalf("后台进程 %s 在 3 秒内未退出", taskID)
		case <-ticker.C:
			snap, _ := tm.Get(taskID)
			if snap.Done {
				goto done
			}
		}
	}
done:
	snap, _ = tm.Get(taskID)
	if snap.Status != TaskCompleted {
		t.Fatalf("期望 TaskCompleted, 得到 %s", snap.Status.String())
	}
	if snap.ExitCode != 0 {
		t.Fatalf("echo 命令应返回 exit=0, 得到 %d", snap.ExitCode)
	}
	if !strings.Contains(snap.Stdout, "hello-from-background") {
		t.Fatalf("stdout 应包含 'hello-from-background', 得到: %s", snap.Stdout)
	}

	// 清理
	_ = tm.Remove(taskID)
}

// TestTaskManagerKill 验证主动终止进程
func TestTaskManagerKill(t *testing.T) {
	tm := GetTaskManager()

	// 启动一个长时间睡眠进程
	taskID, err := tm.Spawn("sleep 30", "")
	if err != nil {
		t.Fatalf("Spawn 失败: %v", err)
	}

	// 确认正在运行
	snap, _ := tm.Get(taskID)
	if snap.Status != TaskRunning {
		t.Fatalf("期望 TaskRunning, 得到 %s", snap.Status.String())
	}

	// 杀死它
	if err := tm.Kill(taskID); err != nil {
		t.Fatalf("Kill 失败: %v", err)
	}

	// 确认状态
	snap, _ = tm.Get(taskID)
	if snap.Status != TaskKilled {
		t.Fatalf("期望 TaskKilled, 得到 %s", snap.Status.String())
	}

	// 清理
	_ = tm.Remove(taskID)
}

// TestTaskManagerList 验证列出所有任务
func TestTaskManagerList(t *testing.T) {
	tm := GetTaskManager()

	// 确保干净的起点: 清理上一轮测试残留
	for _, task := range tm.List() {
		if task.Status == TaskRunning {
			_ = tm.Kill(task.ID)
		}
	}
	time.Sleep(100 * time.Millisecond)
	for _, task := range tm.List() {
		_ = tm.Remove(task.ID)
	}

	// 启动两个任务
	id1, _ := tm.Spawn("sleep 1", "")
	id2, _ := tm.Spawn("sleep 1", "")

	tasks := tm.List()
	if len(tasks) < 2 {
		t.Fatalf("期望至少 2 个任务, 得到 %d", len(tasks))
	}

	// 等待它们退出并清理
	time.Sleep(2 * time.Second)
	_ = tm.Remove(id1)
	_ = tm.Remove(id2)
}
