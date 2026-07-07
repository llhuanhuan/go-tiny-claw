// internal/tools/subagent.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// AgentRunner 是一个打破循环依赖的抽象接口。
// 因为 SubagentTool 存在于 tools 包，而完整的 AgentEngine 存在于 engine 包。
// 为了让 Tool 能拉起 Engine，我们定义一个接口供外部注入。
type AgentRunner interface {
	// RunSub 启动一个匿名的、一次性的子智能体任务，并返回其最终梳理出的纯文本总结
	RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry Registry, reporter interface{}) (string, error)
}

// ═══════════════════════════════════════════════════════════════
// SubagentTool — 异步委派工具
//
// 采用 Spawn + Poll 模式：
//   - Execute() 立即返回 subagent_id，不阻塞主循环
//   - 子智能体在后台 goroutine 中执行
//   - 主循环每轮自动注入已完成子智能体的通知
//   - 也可通过 check_subagent 主动轮询
// ═══════════════════════════════════════════════════════════════

type SubagentTool struct {
	runner           AgentRunner
	readOnlyRegistry Registry
	reporter         interface{}
}

func NewSubagentTool(runner AgentRunner, readOnlyRegistry Registry, reporter interface{}) *SubagentTool {
	return &SubagentTool{
		runner:           runner,
		readOnlyRegistry: readOnlyRegistry,
		reporter:         reporter,
	}
}

func (t *SubagentTool) Name() string { return "spawn_subagent" }

func (t *SubagentTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: "spawn_subagent",
		Description: "派出一个后台子智能体执行深度探索任务（非阻塞）。" +
			"子智能体启动后立即返回 ID，它会在后台独立工作。" +
			"完成后系统会自动通知你结果，你也可以随时调用 check_subagent 主动查询。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_prompt": map[string]interface{}{
					"type":        "string",
					"description": "给子智能体下达的明确探索指令。",
				},
			},
			"required": []string{"task_prompt"},
		},
	}
}

type subagentArgs struct {
	TaskPrompt string `json:"task_prompt"`
}

func (t *SubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input subagentArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	// 【核心改造】：Spawn 立即返回 ID，不阻塞主循环
	id := GetSubagentManager().Spawn(ctx, t.runner, input.TaskPrompt, t.readOnlyRegistry, t.reporter)

	return fmt.Sprintf(
		"子智能体已启动，ID: %s。它将在后台执行探索任务，完成后会自动通知你结果。"+
			"你也可以随时调用 check_subagent(id=\"%s\") 主动查询进度。"+
			"现在你可以继续做其他工作。", id, id), nil
}

// ═══════════════════════════════════════════════════════════════
// CheckSubagentTool — 子智能体状态查询工具
//
// 主 Agent 主动轮询子智能体是否已完成，获取探索报告。
// ═══════════════════════════════════════════════════════════════

type CheckSubagentTool struct{}

func NewCheckSubagentTool() *CheckSubagentTool {
	return &CheckSubagentTool{}
}

func (t *CheckSubagentTool) Name() string { return "check_subagent" }

func (t *CheckSubagentTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        "check_subagent",
		Description: "查询子智能体的执行状态。传入 spawn_subagent 返回的 ID，获取探索报告或查看进度。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{
					"type":        "string",
					"description": "子智能体 ID（如 sa_1）。",
				},
			},
			"required": []string{"id"},
		},
	}
}

type checkSubagentArgs struct {
	ID string `json:"id"`
}

func (t *CheckSubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input checkSubagentArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	snapshot, ok := GetSubagentManager().Get(input.ID)
	if !ok {
		return fmt.Sprintf("子智能体 ID '%s' 不存在。请检查 ID 是否正确。", input.ID), nil
	}

	if !snapshot.Done {
		return fmt.Sprintf("子智能体 %s 仍在运行中（任务: %s）。请稍后再查询，或先去做其他工作。", snapshot.ID, snapshot.Prompt), nil
	}

	if snapshot.Error != nil {
		return fmt.Sprintf("子智能体 %s 执行失败: %v", snapshot.ID, snapshot.Error), nil
	}

	return fmt.Sprintf("【子智能体探索报告】(ID: %s):\n%s", snapshot.ID, snapshot.Summary), nil
}
