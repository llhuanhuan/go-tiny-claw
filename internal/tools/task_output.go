// internal/tools/task_output.go
// TaskOutput 工具允许大模型在后续 Turn 中查看后台进程的实时输出日志和运行状态。
// 类比操作系统中的 "tail -f" + "ps" 的组合。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// TaskOutputTool 实现对后台任务状态和日志的查询
type TaskOutputTool struct{}

func NewTaskOutputTool() *TaskOutputTool {
	return &TaskOutputTool{}
}

func (t *TaskOutputTool) Name() string {
	return "TaskOutput"
}

func (t *TaskOutputTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: "获取指定后台任务的运行状态和输出日志。后台任务由 Bash 工具在 run_in_background 模式下创建。" +
			"返回内容包括: 运行状态(运行中/已完成/已终止/异常)、退出码、运行时长、stdout 和 stderr 日志(最多保留最近 64KB)。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_id": map[string]interface{}{
					"type":        "string",
					"description": "后台任务的唯一 ID (由 Bash 工具后台模式返回，格式: bg_N)。",
				},
			},
			"required": []string{"task_id"},
		},
	}
}

type taskOutputArgs struct {
	TaskID string `json:"task_id"`
}

func (t *TaskOutputTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input taskOutputArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("TaskOutput 参数解析失败: %w", err)
	}

	if input.TaskID == "" {
		return "错误: 缺少必填参数 task_id。提示: 使用 TaskStop 工具无参调用可列出所有后台任务。", nil
	}

	snapshot, ok := GetTaskManager().Get(input.TaskID)
	if !ok {
		return fmt.Sprintf(
			"错误: 任务 '%s' 不存在。\n"+
				"可能的原因:\n"+
				"  1. Task ID 输入有误\n"+
				"  2. 该任务已被清理\n"+
				"提示: 使用 TaskStop 工具无参调用可列出当前所有后台任务。",
			input.TaskID,
		), nil
	}

	// 构造可读的状态报告
	runningDuration := time.Since(snapshot.StartTime).Round(time.Second)
	if snapshot.Done {
		runningDuration = snapshot.EndTime.Sub(snapshot.StartTime).Round(time.Second)
	}

	var statusLine string
	if snapshot.Done {
		statusLine = fmt.Sprintf(
			"%s 已结束 (状态: %s, exit=%d)",
			snapshot.Status.Emoji(), snapshot.Status.String(), snapshot.ExitCode,
		)
	} else {
		statusLine = fmt.Sprintf(
			"%s 运行中 (已运行 %v)",
			snapshot.Status.Emoji(), runningDuration,
		)
	}

	result := fmt.Sprintf(
		"任务 %s: %s\n"+
			"命令     : %s\n"+
			"启动时间 : %s\n"+
			"运行时长 : %v\n",
		snapshot.ID, statusLine,
		snapshot.Command,
		snapshot.StartTime.Format("15:04:05"),
		runningDuration,
	)

	if snapshot.Done {
		result += fmt.Sprintf("结束时间 : %s\n", snapshot.EndTime.Format("15:04:05"))
	}

	// stdout 输出
	if snapshot.Stdout != "" {
		out := snapshot.Stdout
		const maxLen = 4000 // stdout/stderr 各 4000 字符上限,合计 8000
		if len(out) > maxLen {
			out = out[len(out)-maxLen:] // 保留最新(尾部)的 4000 字符 —— 日志尾部通常更重要
			out = "\n...[stdout 过长，仅显示最近 " + fmt.Sprintf("%d", maxLen) + " 字节]...\n" + out
		}
		result += "\n--- stdout ---\n" + out
	} else {
		result += "\n--- stdout ---\n(无输出)"
	}

	// stderr 输出
	if snapshot.Stderr != "" {
		errOut := snapshot.Stderr
		const maxLen = 4000
		if len(errOut) > maxLen {
			errOut = errOut[len(errOut)-maxLen:]
			errOut = "\n...[stderr 过长，仅显示最近 " + fmt.Sprintf("%d", maxLen) + " 字节]...\n" + errOut
		}
		result += "\n--- stderr ---\n" + errOut
	} else {
		result += "\n--- stderr ---\n(无输出)"
	}

	return result, nil
}
