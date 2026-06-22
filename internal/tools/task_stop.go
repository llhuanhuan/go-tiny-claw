// internal/tools/task_stop.go
// TaskStop 工具允许大模型主动终止或列出后台进程。
// 类比操作系统中的 "kill" + "ps" 的组合。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// TaskStopTool 实现对后台任务的终止、列出和清理
type TaskStopTool struct{}

func NewTaskStopTool() *TaskStopTool {
	return &TaskStopTool{}
}

func (t *TaskStopTool) Name() string {
	return "TaskStop"
}

func (t *TaskStopTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "终止或列出后台任务。\n" +
			"- 传入 task_id 参数: 终止指定的后台进程 (如 npm run dev)。\n" +
			"- 不传参数 (空对象 {}): 列出当前所有后台任务及其状态。\n" +
			"后台任务由 Bash 工具在 run_in_background 模式下创建。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_id": map[string]interface{}{
					"type":        "string",
					"description": "要终止的后台任务 ID。留空则列出所有任务。",
				},
			},
			// 注意: task_id 不是必填项 —— 无参调用时列出所有任务
		},
	}
}

type taskStopArgs struct {
	TaskID string `json:"task_id"`
}

func (t *TaskStopTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input taskStopArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("TaskStop 参数解析失败: %w", err)
	}

	// ====================================================================
	// 无参数模式: 列出所有后台任务 (类似 ps 命令)
	// ====================================================================
	if input.TaskID == "" {
		tasks := GetTaskManager().List()
		if len(tasks) == 0 {
			return "当前没有后台任务在运行。", nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("共 %d 个后台任务:\n\n", len(tasks)))
		for _, task := range tasks {
			duration := time.Since(task.StartTime).Round(time.Second)
			if task.Done {
				duration = task.EndTime.Sub(task.StartTime).Round(time.Second)
			}

			sb.WriteString(fmt.Sprintf(
				"  %s %-24s %s",
				task.Status.Emoji(), task.ID, task.Status.String(),
			))

			if task.Done {
				sb.WriteString(fmt.Sprintf(" (exit=%d)", task.ExitCode))
			}
			sb.WriteString(fmt.Sprintf("  运行时长: %v", duration))
			sb.WriteString(fmt.Sprintf("\n     命令: %s\n", task.Command))
		}

		sb.WriteString("\n使用 TaskOutput 查看指定任务的实时日志。")
		sb.WriteString("\n使用 TaskStop 传入 task_id 终止指定任务。")
		return sb.String(), nil
	}

	// ====================================================================
	// 带参数模式: 终止指定任务
	// ====================================================================
	if err := GetTaskManager().Kill(input.TaskID); err != nil {
		// Kill 失败通常意味着进程已经自己退出了 —— 给模型更多提示
		snapshot, ok := GetTaskManager().Get(input.TaskID)
		if ok {
			return fmt.Sprintf(
				"任务 '%s' 当前状态为 %s (exit=%d),无需重复终止。\n"+
					"提示: 可以重新使用 Bash (run_in_background: true) 启动新实例。",
				input.TaskID, snapshot.Status.String(), snapshot.ExitCode,
			), nil
		}
		return fmt.Sprintf("终止失败: %v", err), nil
	}

	return fmt.Sprintf(
		"✅ 已向任务 %s 发送终止信号。进程资源将被回收。\n"+
			"提示: 使用 TaskOutput 工具可确认进程的最终退出状态。",
		input.TaskID,
	), nil
}
