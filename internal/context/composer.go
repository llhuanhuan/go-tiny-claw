package context

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/lhuan/go-tiny-claw/internal/schema"
)

// Compactor 负责监控和压缩上下文内存，防止大模型发生 OOM。
//
// 自适应压缩机制 (Adaptive Compression)：
//   - 当 Provider 返回真实的 Token 消耗时，Compactor 根据 利用率 = PromptTokens / MaxWindowTokens
//     动态调整压缩策略的激进程度。
//   - 当没有真实 Token 数据时（首次调用或 Provider 不支持），回退到字符估算模式。
//
// 压缩级别梯度：
//
//	利用率 < 50%  → 不压缩
//	利用率 50-70% → 温和压缩（仅掩码远期历史）
//	利用率 70-85% → 标准压缩（远期掩码 + 近期掐头去尾 500+500）
//	利用率 85-95% → 激进压缩（远期掩码 + 近期掐头去尾 200+200）
//	利用率 > 95%  → 紧急压缩（全部掩码，仅保留最近 2 条）
type Compactor struct {
	MaxWindowTokens int // 模型的最大上下文窗口 Token 数（从配置读取，默认 200000）
	RetainLastMsgs  int // Working Memory 保护区：最近的 N 条消息

	// 自适应状态（由 UpdateUsage 更新）
	lastPromptTokens int  // 上次 API 返回的真实 PromptTokens
	useTokenMode     bool // 是否已收到过真实 Token 数据
}

// CompressionLevel 压缩级别
type CompressionLevel int

const (
	LevelNone       CompressionLevel = iota // 不压缩
	LevelGentle                             // 温和：仅掩码远期
	LevelStandard                           // 标准：远期掩码 + 近期 500+500
	LevelAggressive                         // 激进：远期掩码 + 近期 200+200
	LevelEmergency                          // 紧急：全部掩码，仅保留最近 2 条
)

// NewCompactor 创建一个新的自适应压缩器。
//   - maxWindowTokens: 模型上下文窗口大小（Token 数）
//   - retainLastMsgs:  保护区消息数
func NewCompactor(maxWindowTokens int, retainLastMsgs int) *Compactor {
	if maxWindowTokens <= 0 {
		maxWindowTokens = 200000 // 默认 200k Token 窗口
	}
	return &Compactor{
		MaxWindowTokens: maxWindowTokens,
		RetainLastMsgs:  retainLastMsgs,
	}
}

// UpdateUsage 由 Engine 在每次 API 响应后调用，喂入真实 Token 消耗数据。
// 这是自适应压缩的核心输入——Compactor 根据 PromptTokens 与模型窗口的比值决定压缩策略。
func (c *Compactor) UpdateUsage(promptTokens int) {
	if promptTokens > 0 {
		c.lastPromptTokens = promptTokens
		c.useTokenMode = true
	}
}

// evaluateLevel 根据当前状态评估压缩级别
func (c *Compactor) evaluateLevel(estimatedChars int) CompressionLevel {
	if c.useTokenMode && c.MaxWindowTokens > 0 {
		// Token 模式：基于真实 Token 利用率
		utilization := float64(c.lastPromptTokens) / float64(c.MaxWindowTokens)

		switch {
		case utilization < 0.50:
			return LevelNone
		case utilization < 0.70:
			return LevelGentle
		case utilization < 0.85:
			return LevelStandard
		case utilization < 0.95:
			return LevelAggressive
		default:
			return LevelEmergency
		}
	}

	// 降级模式：基于字符估算（经验值：1 Token ≈ 3 中文字 ≈ 4 英文字母）
	// 保守取 3 字符/Token 作为中文为主的场景的估算
	estimatedTokens := estimatedChars / 3
	if estimatedTokens < c.MaxWindowTokens/2 {
		return LevelNone
	}
	return LevelStandard
}

// levelName 返回压缩级别的可读名称
func levelName(level CompressionLevel) string {
	switch level {
	case LevelNone:
		return "不压缩"
	case LevelGentle:
		return "温和"
	case LevelStandard:
		return "标准"
	case LevelAggressive:
		return "激进"
	case LevelEmergency:
		return "紧急"
	default:
		return "未知"
	}
}

// Compact 接收准备发送给大模型的消息数组，根据自适应策略进行压缩。
func (c *Compactor) Compact(msgs []schema.Message) []schema.Message {
	currentLength := c.estimateLength(msgs)
	level := c.evaluateLevel(currentLength)

	if level == LevelNone {
		return msgs
	}

	// 根据压缩级别确定参数
	var farThreshold int   // 远期消息掩码阈值（字符数）
	var nearHeadTail int   // 近期消息掐头去尾保留量（每端字符数）
	var emergencyRetain int // 紧急模式保留的消息数

	switch level {
	case LevelGentle:
		farThreshold = 200
		nearHeadTail = 0 // 不截断近期
	case LevelStandard:
		farThreshold = 200
		nearHeadTail = 500
	case LevelAggressive:
		farThreshold = 100
		nearHeadTail = 200
	case LevelEmergency:
		farThreshold = 0   // 全部掩码
		nearHeadTail = 100 // 仅保留极少量
		emergencyRetain = 2
	}

	log.Printf("[Compactor] ⚠️ [%s压缩] 当前上下文 %d 字符, Token利用率: %s, 触发压缩清理...\n",
		levelName(level), currentLength, c.utilizationString())

	var compacted []schema.Message
	msgCount := len(msgs)

	// 紧急模式：缩减保护区
	retainCount := c.RetainLastMsgs
	if level == LevelEmergency {
		retainCount = emergencyRetain
	}

	protectStartIndex := msgCount - retainCount
	if protectStartIndex < 0 {
		protectStartIndex = 0
	}

	for i, msg := range msgs {
		// 系统提示词绝对不动
		if msg.Role == schema.RoleSystem {
			compacted = append(compacted, msg)
			continue
		}

		newMsg := msg
		isInWorkingMemory := i >= protectStartIndex

		if msg.Role == schema.RoleUser && msg.ToolCallID != "" {
			// 工具返回结果 (ToolResult/Observation)
			if !isInWorkingMemory {
				// 远期历史：全量掩码
				if farThreshold == 0 || len(msg.Content) > farThreshold {
					newMsg.Content = fmt.Sprintf("...[为了节省内存，早期的工具输出已被系统强制清理。原始长度: %d 字节]...", len(msg.Content))
				}
			} else {
				// 近期保护区：掐头去尾
				if nearHeadTail > 0 {
					maxKeep := nearHeadTail * 2
					if len(msg.Content) > maxKeep {
						head := msg.Content[:nearHeadTail]
						tail := msg.Content[len(msg.Content)-nearHeadTail:]
						newMsg.Content = fmt.Sprintf("%s\n\n...[内容过长，中间 %d 字节已被系统截断]...\n\n%s", head, len(msg.Content)-maxKeep, tail)
					}
				}
			}
		} else if msg.Role == schema.RoleAssistant && msg.Content != "" {
			// 模型推理废话
			if !isInWorkingMemory && len(msg.Content) > 200 {
				newMsg.Content = "...[早期的推理思考过程已折叠]..."
			}
		}

		// 绝不修改 ToolCalls
		compacted = append(compacted, newMsg)
	}

	newLength := c.estimateLength(compacted)
	compressionRatio := float64(0)
	if currentLength > 0 {
		compressionRatio = (1 - float64(newLength)/float64(currentLength)) * 100
	}
	log.Printf("[Compactor] ✅ [%s压缩] 上下文长度从 %d 降至 %d 字符 (压缩率 %.1f%%)\n",
		levelName(level), currentLength, newLength, compressionRatio)

	return compacted
}

// utilizationString 返回当前利用率的可读字符串
func (c *Compactor) utilizationString() string {
	if c.useTokenMode && c.MaxWindowTokens > 0 {
		utilization := float64(c.lastPromptTokens) / float64(c.MaxWindowTokens) * 100
		return fmt.Sprintf("%.1f%% (%d/%d tokens)", utilization, c.lastPromptTokens, c.MaxWindowTokens)
	}
	return "未知(降级字符估算模式)"
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
