package engine

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/schema"
	"github.com/lhuan/go-tiny-claw/internal/tools"
)

// AgentEngine 是微型 OS 的核心驱动
type AgentEngine struct {
	provider provider.LLMProvider
	registry tools.Registry

	// WorkDir (工作区): 借鉴 OpenClaw 的理念，Agent 必须有一个明确的物理边界
	WorkDir string

	EnableThinking bool // 【新增】慢思考模式开关
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		WorkDir:        workDir,
		EnableThinking: enableThinking, // 【新增】慢思考模式开关
	}
}

// Run 启动 Agent 的生命周期
func (e *AgentEngine) Run(ctx context.Context, userPrompt string) error {
	log.Printf("[Engine] 引擎启动，锁定工作区: %s\n", e.WorkDir)

	log.Printf("[Engine] 慢思考模式 (Thinking Phase): %v\n", e.EnableThinking)

	// 1. 初始化会话的 Context (上下文内存)
	// 在真实的场景中，这里会由动态 Prompt 组装器加载 AGENTS.md。目前我们先硬编码。
	contextHistory := []schema.Message{
		{
			Role:    schema.RoleSystem,
			Content: "You are go-tiny-claw, an expert coding assistant. You have full access to tools in the workspace.",
		},
		{
			Role:    schema.RoleUser,
			Content: userPrompt,
		},
	}

	turnCount := 0

	// 2. The Main Loop: 心跳开始 (标准的 ReAct 循环)
	for {
		turnCount++
		log.Printf("========== [Turn %d] 开始 ==========\n", turnCount)

		// 获取当前挂载的所有工具定义
		availableTools := e.registry.GetAvailableTools()

		// ====================================================================
		// Phase 1: 慢思考阶段 (Thinking) - 剥夺工具，强制规划 ====================================================================
		if e.EnableThinking {
			log.Println("[Engine][Phase 1] 剥夺工具访问权，强制进入慢思考与规划阶段...")
			// 核心机制：传入的 availableTools 为 nil！
			// 大模型看不到任何 JSON Schema，被迫只能输出纯文本的思考过程

			thinkResp, err := e.provider.Generate(ctx, contextHistory, nil)
			if err != nil {
				return fmt.Errorf("Thinking 阶段生成失败: %w", err)
			}
			// 如果模型输出了思考过程，我们将其作为 Assistant 消息追加到上下文中
			if thinkResp != nil && thinkResp.Content != "" {
				fmt.Printf("🧠 [内部思考 Trace]: %s\n", thinkResp.Content)
				contextHistory = append(contextHistory, *thinkResp)
			}
		}
		// ====================================================================
		// Phase 2: 行动阶段 (Action) - 恢复工具，顺着规划执行
		// ====================================================================
		log.Println("[Engine][Phase 2] 恢复工具挂载，等待模型采取行动...")

		// 此时的 contextHistory 中已经包含了上一阶段模型自己的 Thinking Trace。
		// 模型会顺着自己的逻辑，结合恢复的 availableTools 发起精准的工具调用。

		actionResp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("Action 阶段生成失败: %w", err)
		}

		// 将模型的响应完整追加到上下文历史中
		contextHistory = append(contextHistory, *actionResp)

		// 如果模型回复了纯文本，打印出来 (这通常是它的思考过程，或是最终结果)
		if actionResp.Content != "" {
			fmt.Printf("🤖 [对外回复]: %s\n", actionResp.Content)
		}

		// 3. 退出条件判断
		// 如果模型没有请求任何工具调用，说明它认为任务已经完成，跳出循环。
		if len(actionResp.ToolCalls) == 0 {
			log.Println("[Engine] 模型未请求调用工具，任务宣告完成。")
			break
		}

		// 4. 并行执行工具调用 (Parallel Execution)
		log.Printf("[Engine] 模型请求调用 %d 个工具...\n", len(actionResp.ToolCalls))

		e.executeToolsInParallel(ctx, actionResp.ToolCalls, &contextHistory)

		// 循环回到开头，模型将带着新加入的 Observation 继续它的下一轮思考...
	}

	return nil
}

// executeToolsInParallel 并行执行所有工具调用，并按原始顺序将 Observation 组装回 Context。
//
// 设计要点：
//  1. 所有工具调用并发执行，减少 I/O 密集等待的总耗时。
//  2. 单个工具报错不会中断其他工具的执行——错误结果同样作为 Observation 写回，
//     供模型在下一轮推理中感知并自愈。
//  3. 有序性保障：
//     (a) 写入端：预分配 slots 切片，每个 goroutine 通过值传递捕获索引 i，
//     写入 slots[idx]，各 goroutine 操作不同内存地址，无数据竞争。
//     (b) 读取端：wg.Wait() 作为屏障，Go memory model 保证
//     slots[idx]=... → wg.Done() → wg.Wait() 返回 → 主 goroutine 读取 slots
//     这条 happens-before 链完整。
//     (c) 结论：无论 goroutine 调度顺序如何，最终 contextHistory 中 Observation
//     的追加顺序始终等于模型输出 ToolCalls 的原始顺序。
func (e *AgentEngine) executeToolsInParallel(ctx context.Context, toolCalls []schema.ToolCall, contextHistory *[]schema.Message) {
	n := len(toolCalls)

	// 预分配结果槽位，每个 goroutine 写入自己的索引，无需互斥锁
	type slot struct {
		call   schema.ToolCall
		result schema.ToolResult
	}
	slots := make([]slot, n)

	var wg sync.WaitGroup
	wg.Add(n)

	for i, tc := range toolCalls {
		go func(idx int, call schema.ToolCall) {
			defer wg.Done()

			log.Printf("  -> 🛠️ [并行] 执行工具: %s, 参数: %s\n", call.Name, string(call.Arguments))

			// 通过 Registry 路由并执行底层工具
			result := e.registry.Execute(ctx, call)

			// 写入预分配的槽位（每个 goroutine 独享一个索引，无数据竞争）
			slots[idx] = slot{call: call, result: result}
		}(i, tc)
	}

	wg.Wait()

	// 按原始顺序（索引 0 → n-1）将 Observation 组装回上下文
	for _, s := range slots {
		if s.result.IsError {
			log.Printf("  -> ❌ 工具执行报错 [%s]: %s\n", s.call.Name, s.result.Output)
		} else {
			log.Printf("  -> ✅ 工具执行成功 [%s] (返回 %d 字节)\n", s.call.Name, len(s.result.Output))
		}

		// 将工具执行的观察结果 (Observation) 封装为 User Message 追加到上下文中
		// 注意：ToolCallID 必须携带！这是维系大模型推理链条的关键
		observationMsg := schema.Message{
			Role:       schema.RoleUser,
			Content:    s.result.Output,
			ToolCallID: s.call.ID,
		}
		*contextHistory = append(*contextHistory, observationMsg)
	}
}
