// internal/tools/task_manager.go
// TaskManager 是微型 OS 的进程生命周期管理器。
//
// 设计理念借鉴操作系统的 PCB (Process Control Block):
//   - Spawn():   fork + exec,立即返回 PID(taskID),不阻塞调用者
//   - Get():     读取进程控制块快照(stdout/stderr 环形缓冲区 + 退出状态)
//   - Kill():    向子进程发送终止信号
//   - List():    枚举当前所有任务(类似 ps 命令)
//   - Shutdown(): 引擎退出时批量收割,防止孤儿进程泄漏
//
// 与"同步 Bash 模式"的关键区别:
//   - 同步: CombinedOutput() 阻塞,30s 超时强制杀死 → 适合短命令
//   - 后台: cmd.Start() + goroutine 异步收割 → 适合 npm run dev / python server.py
package tools

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// TaskStatus 描述后台任务的当前生命周期阶段
type TaskStatus int

const (
	TaskRunning   TaskStatus = iota // 进程正在运行中
	TaskCompleted                    // 进程正常退出 (exit code 可为 0 或非 0)
	TaskKilled                       // 被 TaskStop 工具显式终止
	TaskError                        // 进程启动失败或异常崩溃
)

// String 返回 TaskStatus 的中文可读表示
func (s TaskStatus) String() string {
	switch s {
	case TaskRunning:
		return "running"
	case TaskCompleted:
		return "completed"
	case TaskKilled:
		return "killed"
	case TaskError:
		return "error"
	default:
		return "unknown"
	}
}

// Emoji 返回 TaskStatus 对应的状态图标
func (s TaskStatus) Emoji() string {
	switch s {
	case TaskRunning:
		return "🟢"
	case TaskCompleted:
		return "✅"
	case TaskKilled:
		return "🛑"
	case TaskError:
		return "❌"
	default:
		return "❓"
	}
}

// BackgroundTask 代表一个后台运行的子进程及其 I/O 缓冲区。
// 它是 TaskManager 中受管理的最小单元,类比操作系统的 PCB。
type BackgroundTask struct {
	ID        string        // 全局唯一标识 (格式: "bg_N")
	Command   string        // 原始命令字符串
	Status    TaskStatus    // 当前生命周期状态
	ExitCode  int           // 进程退出码 (仅当 Status != TaskRunning 时有效)
	StartTime time.Time     // 进程启动时刻
	EndTime   time.Time     // 进程结束时刻 (零值表示尚未结束)

	cmd    *exec.Cmd           // 底层 OS 进程句柄
	stdout *RingBuffer         // stdout 环形缓冲区 (64KB)
	stderr *RingBuffer         // stderr 环形缓冲区 (64KB)
	done   chan struct{}       // 进程退出时关闭,用于异步通知等待者
	cancel context.CancelFunc  // 用于主动杀死子进程

	killedByUser bool       // 标记是否由 Kill() 主动触发 (用于区分主动 kill vs 进程自身崩溃)
	mu           sync.RWMutex // 保护 Status / ExitCode / EndTime / killedByUser 的并发读写
}

// TaskSnapshot 是 BackgroundTask 的只读快照。
// 调用者通过 Get() 获得此结构,无需持有锁即可安全读取。
type TaskSnapshot struct {
	ID        string
	Command   string
	Status    TaskStatus
	ExitCode  int
	StartTime time.Time
	EndTime   time.Time
	Stdout    string
	Stderr    string
	Done      bool // 进程是否已退出 (便于非阻塞轮询)
}

// Snapshot 返回当前任务的线程安全只读快照
func (t *BackgroundTask) Snapshot() TaskSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// 注意: stdout/stderr 的 String() 自身是线程安全的,
	// 这里不加 task 锁也可以调用,但包含在 RLock 内更安全
	return TaskSnapshot{
		ID:        t.ID,
		Command:   t.Command,
		Status:    t.Status,
		ExitCode:  t.ExitCode,
		StartTime: t.StartTime,
		EndTime:   t.EndTime,
		Stdout:    t.stdout.String(),
		Stderr:    t.stderr.String(),
		Done:      t.Status != TaskRunning,
	}
}

// TaskManager 是全局唯一的后台进程注册表。
//
// 零值不可用,必须通过 GetTaskManager() 获取全局单例。
// 所有方法线程安全。
type TaskManager struct {
	mu    sync.RWMutex
	tasks map[string]*BackgroundTask
	idSeq atomic.Int64 // 单调递增的 Task ID 序号
}

// 全局单例 —— 在包初始化时构建,整个进程生命周期唯一
var globalTaskManager = &TaskManager{
	tasks: make(map[string]*BackgroundTask),
}

// GetTaskManager 返回全局 TaskManager 单例。
// 所有工具( BashTool / TaskOutput / TaskStop )通过此函数共享同一实例。
func GetTaskManager() *TaskManager {
	return globalTaskManager
}

// Spawn 启动一个后台子进程,立即返回全局唯一 taskID。
//
// 参数:
//   - command: 完整的 shell 命令 (通过 bash -c 执行)
//   - workDir: 子进程的工作目录
//
// 返回:
//   - taskID: 格式为 "bg_N" 的唯一标识,后续通过 TaskOutput/TaskStop 操作
//   - error:  cmd.Start() 失败时返回非 nil
func (tm *TaskManager) Spawn(command string, workDir string) (string, error) {
	// 使用 context.Background() 而非带超时的 Context,
	// 后台进程理论上可以无限期运行 —— 只有显式 Kill 或 Shutdown 会终止它。
	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = workDir

	// 【关键】在 Start() 之前连接管道。
	// 如果先 Start() 再 StdoutPipe(),Go 标准库会 panic。
	// 如果连接了管道但不读取,子进程写满 OS 管道缓冲区后会阻塞 —— 经典死锁。
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return "", fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return "", fmt.Errorf("创建 stderr 管道失败: %w", err)
	}

	// 【关键】cmd.Start() 而非 cmd.Run() —— fork 后立即返回,不等待子进程
	if err := cmd.Start(); err != nil {
		cancel()
		return "", fmt.Errorf("后台进程启动失败: %w", err)
	}

	id := fmt.Sprintf("bg_%d", tm.idSeq.Add(1))

	task := &BackgroundTask{
		ID:        id,
		Command:   command,
		Status:    TaskRunning,
		StartTime: time.Now(),
		cmd:       cmd,
		stdout:    NewRingBuffer(64 * 1024), // 64KB 硬上限
		stderr:    NewRingBuffer(64 * 1024),
		done:      make(chan struct{}),
		cancel:    cancel,
	}

	tm.mu.Lock()
	tm.tasks[id] = task
	tm.mu.Unlock()

	// 在后台 goroutine 中异步收割子进程 —— 这是整个设计最关键的一步:
	//  1. 双 goroutine 并行 drain stdout/stderr 管道 (防止子进程写阻塞)
	//  2. cmd.Wait() 回收进程资源 (防止僵尸)
	//  3. close(done) 通知等待者
	go tm.reap(task, stdoutPipe, stderrPipe)

	return id, nil
}

// reap 在独立 goroutine 中执行,负责:
//  1. 持续读取 stdout/stderr 到环形缓冲区
//  2. 等待子进程退出
//  3. 更新任务状态并关闭 done 通道
//
// 必须用两个 goroutine 分别读取 stdout 和 stderr ——
// 如果只在一个 goroutine 中顺序读 (先 stdout 再 stderr),
// stderr 管道可能先写满导致子进程阻塞,而当前 goroutine 还在等 stdout 的 EOF。
func (tm *TaskManager) reap(task *BackgroundTask, stdout, stderr io.Reader) {
	// 双 goroutine 并行 drain 管道
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(task.stdout, stdout)
	}()
	go func() {
		defer wg.Done()
		io.Copy(task.stderr, stderr)
	}()
	wg.Wait()

	// 管道读完意味着子进程已退出 (EOF),此时收割
	waitErr := task.cmd.Wait()

	task.mu.Lock()
	task.EndTime = time.Now()
	if waitErr != nil {
		// killedByUser 标记由 Kill() 方法在调用 cancel() 前设置，
		// 用于区分"用户主动终止"和"进程自身崩溃/非零退出"。
		if task.killedByUser {
			task.Status = TaskKilled
		} else {
			task.Status = TaskError
		}
	} else {
		task.Status = TaskCompleted
		task.ExitCode = task.cmd.ProcessState.ExitCode()
	}
	task.mu.Unlock()

	close(task.done)
}

// Get 返回指定 TaskID 的快照。第二个返回值指示任务是否存在。
func (tm *TaskManager) Get(id string) (TaskSnapshot, bool) {
	tm.mu.RLock()
	task, exists := tm.tasks[id]
	tm.mu.RUnlock()

	if !exists {
		return TaskSnapshot{}, false
	}
	return task.Snapshot(), true
}

// Kill 向指定后台进程发送终止信号并等待回收。
//
// 实现细节:
//   - 调用 cancel() 触发 context.WithCancel → 内核给子进程发 os.Kill
//   - 阻塞等待 <-task.done,确保进程资源已被 Wait() 回收 (无僵尸)
//   - 设置超时保护: 如果 5 秒内进程仍未退出,不再等待 (防止 Kill 本身卡死)
func (tm *TaskManager) Kill(id string) error {
	tm.mu.RLock()
	task, exists := tm.tasks[id]
	tm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("任务 '%s' 不存在", id)
	}

	task.mu.RLock()
	alreadyDead := task.Status != TaskRunning
	task.mu.RUnlock()

	if alreadyDead {
		return fmt.Errorf("任务 '%s' 已经不在运行中 (状态: %s)", id, task.Status)
	}

	// 先设置 killedByUser 标志,再发送 kill 信号。
	// 顺序重要: 确保 reap goroutine 读到 killedByUser=true 后才看到进程退出。
	task.mu.Lock()
	task.killedByUser = true
	task.mu.Unlock()

	task.cancel()

	// 阻塞等待子进程被收割,最多等 5 秒
	select {
	case <-task.done:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("终止任务 '%s' 超时 (5s): 进程可能无响应", id)
	}
}

// List 返回所有已注册任务的快照列表,按启动时间排序 (最早启动的在前)。
func (tm *TaskManager) List() []TaskSnapshot {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	snapshots := make([]TaskSnapshot, 0, len(tm.tasks))
	for _, task := range tm.tasks {
		snapshots = append(snapshots, task.Snapshot())
	}
	// 简单插入排序保证按 StartTime 升序 (任务通常不多,不需要引入 sort 包)
	for i := 1; i < len(snapshots); i++ {
		for j := i; j > 0 && snapshots[j].StartTime.Before(snapshots[j-1].StartTime); j-- {
			snapshots[j], snapshots[j-1] = snapshots[j-1], snapshots[j]
		}
	}
	return snapshots
}

// Remove 从注册表中删除已完成/已终止的任务记录。
// 仅当任务不在 Running 状态时允许删除,防止误删活跃进程导致泄漏。
func (tm *TaskManager) Remove(id string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	task, exists := tm.tasks[id]
	if !exists {
		return fmt.Errorf("任务 '%s' 不存在", id)
	}

	task.mu.RLock()
	isRunning := task.Status == TaskRunning
	task.mu.RUnlock()

	if isRunning {
		return fmt.Errorf("无法删除正在运行的任务 '%s',请先使用 TaskStop 终止它", id)
	}

	delete(tm.tasks, id)
	return nil
}

// Shutdown 终止所有子进程并清空注册表。
// 应在引擎退出时调用,防止孤儿进程泄漏。
// 每个进程最多等待 5 秒,总超时 30 秒。
func (tm *TaskManager) Shutdown() {
	tm.mu.RLock()
	taskIDs := make([]string, 0, len(tm.tasks))
	for id := range tm.tasks {
		taskIDs = append(taskIDs, id)
	}
	tm.mu.RUnlock()

	if len(taskIDs) == 0 {
		return
	}

	// 并行 Kill 所有进程
	var wg sync.WaitGroup
	for _, id := range taskIDs {
		wg.Add(1)
		go func(taskID string) {
			defer wg.Done()
			// 忽略错误 —— 进程可能已经自己退出了
			_ = tm.Kill(taskID)
		}(id)
	}

	// 总超时保护: 不能因为某个顽固进程无限阻塞引擎退出
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// 所有进程已终止
	case <-time.After(30 * time.Second):
		// 超时兜底,强制退出
	}

	// 清空注册表
	tm.mu.Lock()
	tm.tasks = make(map[string]*BackgroundTask)
	tm.mu.Unlock()
}
