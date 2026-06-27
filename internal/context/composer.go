package context

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// Compactor 负责监控和压缩上下文内存，防止大模型发生 OOM
type Compactor struct {
	MaxChars       int // 触发压缩的最大字符数阈值 (水位线，可参考使用的大模型的token窗口大小)
	RetainLastMsgs int // Working Memory 保护区：最近的 N 条消息
}

func NewCompactor(maxChars int, retainLastMsgs int) *Compactor {
	return &Compactor{
		MaxChars:       maxChars,
		RetainLastMsgs: retainLastMsgs,
	}
}

// Compact 接收准备发送给大模型的消息数组。
// 如果总长度超标，对远期历史区进行全量掩码 (Masking)，对短期保护区进行超长局部截断 (Truncation)。
func (c *Compactor) Compact(msgs []schema.Message) []schema.Message {
	currentLength := c.estimateLength(msgs)

	// 如果没有超过水位线，直接返回原数组 (大多数情况下的正常路径)
	if currentLength < c.MaxChars {
		return msgs
	}

	log.Printf("[Compactor] ⚠️ 内存告警：当前上下文长度 (%d 字符) 超过阈值 (%d)，触发压缩清理...\n", currentLength, c.MaxChars)

	var compacted []schema.Message
	msgCount := len(msgs)

	// 计算受保护的 Working Memory 起始索引
	protectStartIndex := msgCount - c.RetainLastMsgs
	if protectStartIndex < 0 {
		protectStartIndex = 0
	}

	for i, msg := range msgs {
		// 1. 系统提示词 (System Prompt) 绝对不能动，直接保留
		if msg.Role == schema.RoleSystem {
			compacted = append(compacted, msg)
			continue
		}

		// 我们必须拷贝一份新消息，因为在并发环境中直接修改原引用可能导致底层数据结构被污染
		newMsg := msg

		isInWorkingMemory := i >= protectStartIndex

		// 【核心驾驭逻辑】: 双重降级防线
		if msg.Role == schema.RoleUser && msg.ToolCallID != "" {
			// 对于工具的返回结果 (Observation/ToolResult)
			if !isInWorkingMemory {
				// 【第一道防线：远期历史】如果是早期对话，执行无情替换 (Full Masking)
				if len(msg.Content) > 200 {
					newMsg.Content = fmt.Sprintf("...[为了节省内存，早期的工具输出已被系统强制清理。原始长度: %d 字节]...", len(msg.Content))
				}
			} else {
				// 【第二道防线：短期记忆】即使处于近期保护区，只要单条内容过大，也必须截断防 OOM (Head-Tail Truncation)
				// 我们保留前 500 字符和后 500 字符（掐头去尾法，大模型通常只需要看开头报错和结尾总结）
				const maxKeep = 1000
				if len(msg.Content) > maxKeep {
					head := msg.Content[:500]
					tail := msg.Content[len(msg.Content)-500:]
					newMsg.Content = fmt.Sprintf("%s\n\n...[内容过长，中间 %d 字节已被系统截断]...\n\n%s", head, len(msg.Content)-maxKeep, tail)
				}
			}
		} else if msg.Role == schema.RoleAssistant && msg.Content != "" {
			// 对于大模型的冗长推理废话 (Thinking Trace)
			if !isInWorkingMemory && len(msg.Content) > 200 {
				newMsg.Content = "...[早期的推理思考过程已折叠]..."
			}
		}

		// 注意：我们绝不会去动 msg.ToolCalls，因为这是模型行动的证据，是维系逻辑链的关键！
		compacted = append(compacted, newMsg)
	}

	newLength := c.estimateLength(compacted)
	log.Printf("[Compactor] ✅ 压缩完成。上下文长度从 %d 降至 %d 字符。\n", currentLength, newLength)

	return compacted
}

// estimateLength 粗略计算当前上下文的总字符长度
func (c *Compactor) estimateLength(msgs []schema.Message) int {
	length := 0
	for _, msg := range msgs {
		length += len(msg.Content)
		for _, tc := range msg.ToolCalls {
			length += len(tc.Name) + len(tc.Arguments)
		}
	}
	return length
}

// PromptComposer 负责根据工作区环境动态生成 System Prompt
type PromptComposer struct {
	workDir     string
	skillLoader *SkillLoader
}

func NewPromptComposer(workDir string) *PromptComposer {
	return &PromptComposer{
		workDir:     workDir,
		skillLoader: NewSkillLoader(workDir),
	}
}

// SkillLoader 返回内部的 SkillLoader 引用，供外部注册 read_skill 工具使用。
func (c *PromptComposer) SkillLoader() *SkillLoader {
	return c.skillLoader
}

// Build 组装并返回一条完整的 RoleSystem 消息。
//
// 三段式结构：
//  1. 极简内核 (Minimal Core) —— 身份与红线纪律
//  2. 外部化状态 (AGENTS.md) —— 项目专属规范
//  3. 技能元数据索引 (Skills Metadata) —— 渐进式暴露，通过 read_skill 按需加载正文
func (c *PromptComposer) Build() schema.Message {
	var promptBuilder strings.Builder

	// ═══════════════════════════════════════════════════════════════
	// 1. 极简内核 (Minimal Core)
	//    仅确立基本身份与最底线的红线纪律
	// ═══════════════════════════════════════════════════════════════
	promptBuilder.WriteString(`# 核心身份
你名叫 go-tiny-claw，一个由驾驭工程驱动的骨灰级研发助手。
你具备极简主义哲学，拒绝废话。你能通过系统提供的内置工具，创建、读取、修改和执行工作区中的代码。

# 核心纪律 (CRITICAL)
1. 如需检查文件是否存在，请使用 bash 的 ls 或 test -f，而不是对目录使用 read_file。
2. 创建新文件时，务必使用 write_file，并同时提供 path 和 content 参数。
3. 编辑文件前务必先读取现有文件，以理解上下文。
4. 无论何时你需要写代码或创建文件，都要直接使用 write_file 工具。
5. 遇到工具执行报错时，仔细阅读 stderr，尝试自己修正命令并重试。
6. 始终用中文回复，以便传达你的进展和想法。
`)

	// ═══════════════════════════════════════════════════════════════
	// 2. 外部化状态：加载项目专属规范 (AGENTS.md)
	//    借鉴 OpenClaw 哲学，将易变的业务规范剥离出核心引擎。
	// ═══════════════════════════════════════════════════════════════
	agentsMDPath := filepath.Join(c.workDir, "AGENTS.md")
	content, err := os.ReadFile(agentsMDPath)
	if err == nil {
		promptBuilder.WriteString("\n# 项目专属指南 (来自 AGENTS.md)\n")
		promptBuilder.WriteString("以下是当前工作区特有的架构规范与注意事项，你的行为必须绝对符合以下要求：\n")
		promptBuilder.WriteString("```markdown\n")
		promptBuilder.WriteString(string(content))
		promptBuilder.WriteString("\n```\n")
	}

	// ═══════════════════════════════════════════════════════════════
	// 3. 技能元数据索引 (Progressive Disclosure)
	//
	//    设计原则：
	//    - 仅注入技能的 YAML 元数据（名称 + 触发描述），正文不进入 System Prompt
	//    - 模型根据触发描述判断是否启用，通过 read_skill 工具按需拉取正文
	//    - 即使 50 个技能包，元数据索引也仅消耗百级 Token，避免 Eager Loading
	//      "开局吃掉几万 Token" 的痛点
	// ═══════════════════════════════════════════════════════════════
	skills := c.skillLoader.LoadAllMetadata()
	if len(skills) > 0 {
		promptBuilder.WriteString("\n### 可用专业技能 (Agent Skills)\n")
		promptBuilder.WriteString("以下技能已安装于当前工作区。每个技能的**完整指令正文**不会在此列出——\n")
		promptBuilder.WriteString("当你判断当前任务匹配某个技能的触发条件时，必须主动调用 `read_skill` 工具\n")
		promptBuilder.WriteString("（参数为技能名称）来获取其详细执行指南。\n\n")
		promptBuilder.WriteString("**切勿假设你已知道某项技能的正文内容——你必须通过 read_skill 工具主动拉取！**\n\n")

		for _, skill := range skills {
			promptBuilder.WriteString(fmt.Sprintf("- **%s**: %s\n", skill.Name, skill.Description))
		}
	}

	return schema.Message{
		Role:    schema.RoleSystem,
		Content: promptBuilder.String(),
	}
}
