package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	ctxpkg "github.com/lhuan/go-tiny-claw/internal/context"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// ReadSkillTool 实现了"渐进式暴露 (Progressive Disclosure)"的核心工具。
//
// 设计理念：
//
//	System Prompt 中只注入技能的元数据（名称 + 触发描述），模型在判断当前任务
//	需要某个技能时，主动调用本工具将技能正文按需加载到上下文中。
//
// 相比 Eager Loading，这个工具将一个 50 技能项目从"开局吃掉几万 Token"
// 降低为"用几个 Token 看索引，用几十个 Token 调工具，用到哪个加载哪个"。
type ReadSkillTool struct {
	skillLoader *ctxpkg.SkillLoader
}

// NewReadSkillTool 创建一个新的 ReadSkillTool 实例。
// skillLoader 是 SkillLoader 的指针，用于查找和加载技能正文。
func NewReadSkillTool(skillLoader *ctxpkg.SkillLoader) *ReadSkillTool {
	return &ReadSkillTool{skillLoader: skillLoader}
}

func (t *ReadSkillTool) Name() string {
	return "read_skill"
}

func (t *ReadSkillTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name: t.Name(),
		Description: strings.Join([]string{
			"按需加载指定技能的完整指令正文（渐进式暴露）。",
			"System Prompt 中已列出所有可用技能的【名称】与【触发条件】，",
			"当你的任务匹配某个技能的触发条件时，调用此工具获取该技能的详细执行指南。",
			"不要在 System Prompt 阶段假设已知技能正文内容——",
			"你必须通过此工具主动拉取！",
		}, " "),
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "要加载的技能名称，必须与 System Prompt 中列出的技能名称精确匹配。",
				},
			},
			"required": []string{"name"},
		},
	}
}

type readSkillArgs struct {
	Name string `json:"name"`
}

func (t *ReadSkillTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input readSkillArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	if input.Name == "" {
		available := t.skillLoader.AvailableSkillNames()
		return "", fmt.Errorf("技能名称不能为空。可用技能: %s", strings.Join(available, ", "))
	}

	body, err := t.skillLoader.LoadBody(input.Name)
	if err != nil {
		// 增强错误信息：列出所有可用技能，帮助模型自愈
		available := t.skillLoader.AvailableSkillNames()
		return "", fmt.Errorf("技能加载失败: %w\n当前可用的技能列表: %s", err, strings.Join(available, ", "))
	}

	// 返回技能正文，模型将在下一轮推理中遵循其指令
	return fmt.Sprintf("【技能 '%s' 已加载】\n\n%s", input.Name, body), nil
}
