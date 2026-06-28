package engine

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ctxpkg "github.com/lhuan/go-tiny-claw/internal/context"

	"github.com/lhuan/go-tiny-claw/internal/observability"
	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/schema"
	"github.com/lhuan/go-tiny-claw/internal/tools"
)

// AgentEngine 是微型 OS 的核心驱动
type AgentEngine struct {
	provider provider.LLMProvider
	registry tools.Registry
	composer *ctxpkg.PromptComposer
	WorkDir  string // 工作区根目录，供外部（bot）创建 Session 时使用

	EnableThinking bool // 慢思考模式开关

	// 后台任务追踪: 引擎自动感知本轮启动的后台进程,并在后续 Turn 中
	// 当进程退出时主动通知模型 (异步事件注入),无需模型手动轮询 TaskOutput。
	taskManager      *tools.TaskManager
	trackedTaskIDs   map[string]struct{} // 本轮启动的 Task ID 集合
	trackedTaskIDsMu sync.Mutex

	// wsRWMu 是工作区读写锁 (Workspace RWMutex)，保护 WorkDir 文件系统在多个
	// 并发 Run() 调用之间的一致性：
	//   - 纯读批次 (read_file, TaskOutput): 持有 RLock，多个 Run() 可并发读
	//   - 含写批次 (bash, write_file, edit_file, TaskStop): 持有 Lock，独占工作区
	//
	// 锁粒度控制在工具批次级而非整个 Run() 生命周期，避免 Agent 在纯 LLM 推理
	// 阶段白白阻塞其他 Agent 的读操作。
	wsRWMu    sync.RWMutex
	compactor *ctxpkg.Compactor       // 压缩器实例，防止大模型上下文 OOM
	PlanMode  bool                    // 暴露给外部的计划模式开关
	recovery  *ctxpkg.RecoveryManager // 自愈管理器
	injector  *ReminderInjector       // 提醒注入器
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool, planMode bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		composer:       ctxpkg.NewPromptComposer(workDir),
		WorkDir:        workDir,
		EnableThinking: enableThinking,
		taskManager:    tools.GetTaskManager(),
		trackedTaskIDs: make(map[string]struct{}),
		// 【初始化压缩器】：为了便于今天的极端测试，我们将水位线阈值设积极（例如 3000 字符），
		//  并保护最近的 6 条消息（大约两轮 Turn 的交互）
		compactor: ctxpkg.NewCompactor(3000, 6),
		PlanMode:  planMode,
		recovery:  ctxpkg.NewRecoveryManager(),
		injector:  NewReminderInjector(),
	}
}

// SkillLoader 返回引擎内部 Composer 持有的 SkillLoader 引用，
// 供外部注册 read_skill 工具使用（渐进式暴露架构）。
func (e *AgentEngine) SkillLoader() *ctxpkg.SkillLoader {
	return e.composer.SkillLoader()
}

// SetMaxContextWindow 设置模型的上下文窗口大小（Token 数），供 Compactor 进行自适应压缩决策。
// 应在引擎创建后、运行前调用。
func (e *AgentEngine) SetMaxContextWindow(maxTokens int) {
	e.compactor.MaxWindowTokens = maxTokens
}

// Run 启动 Agent 的生命周期。
//
// reporter 是引擎向外界汇报状态的唯一出口。传入 nil 时引擎将静默运行
// （仅保留 log.Printf 级别的内部日志）。
func (e *AgentEngine) Run(ctx context.Context, session *Session, userPrompt string, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，锁定工作区: %s\n", session.ID, session.WorkDir)

	// 【埋点 1】：开启 Root Span，记录整个任务的生命周期
	ctx, rootSpan := observability.StartSpan(ctx, "Agent.Run")
	rootSpan.AddAttribute("SessionID", session.ID)
	rootSpan.AddAttribute("WorkDir", session.WorkDir)

	// defer 保证在引擎退出时，无论成功失败，都能结束根 Span 并导出 Trace 报告
	defer func() {
		rootSpan.EndSpan()
		_ = observability.ExportTraceToFile(rootSpan, session.WorkDir, session.ID)
		log.Printf("📊 [Tracing] 本次任务的执行回放链路已保存至工作区的 .claw/traces 目录下\n")
	}()

	// 将用户输入追加到 Session 历史，确保：
	// 1. Thinking 阶段至少有一条 User 消息（Anthropic API 强制要求）
	// 2. 多轮对话时 Working Memory 能正确截取到本轮用户输入
	session.Append(schema.Message{Role: schema.RoleUser, Content: userPrompt})

	// 根据当前 Session 的工作区，动态组装最新的 System Prompt
	composer := ctxpkg.NewPromptComposer(session.WorkDir, e.PlanMode)
	// 【核心修改】动态组装 System Prompt，彻底替换掉以前硬编码的面条提示词！
	systemMsg := composer.Build()

	// ═══════════════════════════════════════════════════════════════
	// Plan Mode: 引擎级强制注入 (Engine-Level Enforcement)
	//
	// 纯靠 System Prompt 的软约束不够可靠——LLM 可能忽略 Plan Mode 指令。
	// 这里由引擎程序检查文件系统状态，注入对应的 User 消息作为"硬约束"。
	// ═══════════════════════════════════════════════════════════════
	if e.PlanMode {
		planPath := filepath.Join(session.WorkDir, "PLAN.md")
		todoPath := filepath.Join(session.WorkDir, "TODO.md")
		if _, err := os.Stat(planPath); os.IsNotExist(err) {
			// ═══════════════════════════════════════════════════════
			// 全新任务：引擎直接创建骨架文件，LLM 只需填充内容
			// 这比"要求 LLM 从零创建"可靠得多——文件已存在，LLM 只需 edit
			// ═══════════════════════════════════════════════════════
			log.Printf("[Engine] 📋 Plan Mode: 创建骨架文件 PLAN.md + TODO.md\n")

			skeletonPlan := fmt.Sprintf("# PLAN — %s\n\n## 任务理解\n（待填充：请用 write_file 重写此文件，写下你对任务的理解）\n\n## 架构设计\n（待填充）\n\n## 技术选型\n（待填充）\n", userPrompt)
			skeletonTodo := fmt.Sprintf("# TODO\n\n（待填充：请用 write_file 重写此文件，使用 - [ ] 格式拆解可执行步骤）\n")

			if err := os.WriteFile(planPath, []byte(skeletonPlan), 0644); err != nil {
				log.Printf("[Engine] ⚠️ 创建 PLAN.md 失败: %v\n", err)
			}
			if err := os.WriteFile(todoPath, []byte(skeletonTodo), 0644); err != nil {
				log.Printf("[Engine] ⚠️ 创建 TODO.md 失败: %v\n", err)
			}

			session.Append(schema.Message{
				Role: schema.RoleUser,
				Content: fmt.Sprintf("【Plan Mode 强制指令 — STEP 1】引擎已为你创建了 PLAN.md 和 TODO.md 的骨架文件。\n"+
					"你的首要任务是填充它们：\n"+
					"第 1 步：调用 read_file 读取 PLAN.md，了解骨架结构。\n"+
					"第 2 步：调用 write_file 重写 PLAN.md（path=\"PLAN.md\"），将「（待填充）」替换为你对任务「%s」的理解、架构设计、技术选型。\n"+
					"第 3 步：调用 write_file 重写 TODO.md（path=\"TODO.md\"），拆解出可执行步骤（使用 - [ ] 格式）。\n"+
					"在 PLAN.md 和 TODO.md 填充完毕之前，禁止写任何业务代码！", userPrompt),
			})
		} else {
			// 断点续传：强制要求读取 PLAN.md
			log.Printf("[Engine] 📋 Plan Mode: PLAN.md 已存在，注入 resume 指令\n")
			session.Append(schema.Message{
				Role: schema.RoleUser,
				Content: "【Plan Mode 强制指令 — STEP 1】PLAN.md 已存在。你必须立即执行以下操作：\n" +
					"1. 调用 read_file 读取 PLAN.md，了解全局目标。\n" +
					"2. 调用 read_file 读取 TODO.md，找到第一个 - [ ] 未完成任务。\n" +
					"3. 从该任务直接继续执行，绝对不要覆盖已有文件！",
			})
		}
	}

	// 确保引擎退出时清理所有后台进程
	defer e.taskManager.Shutdown()

	turnCount := 0
	for {
		turnCount++
		// 【埋点 2】：开启 Turn 子跨度，记录单次 ReAct 循环的生命周期
		turnCtx, turnSpan := observability.StartSpan(ctx, fmt.Sprintf("Turn-%d", turnCount))

		availableTools := e.registry.GetAvailableTools()
		// 1. 【上下文组装】: System Prompt + 双维度截取 Working Memory
		//    - 最多 6 条消息（条数维度）
		//    - 最多 50000 字符（Token 预算维度，防止巨型 ToolResult 撑爆上下文）
		workingMemory := session.GetWorkingMemory(6, 50000)

		// Plan Mode: 每轮追加轻量级提醒（不写入 Session，避免污染历史）
		if e.PlanMode {
			planPath := filepath.Join(session.WorkDir, "PLAN.md")
			reminder := "[Plan Mode] 你当前处于计划模式。"
			planContent, err := os.ReadFile(planPath)
			if err != nil {
				reminder += "PLAN.md 读取失败！请检查文件系统。"
			} else if strings.Contains(string(planContent), "（待填充）") {
				reminder += "PLAN.md 仍为骨架状态！你的首要任务是用 write_file 重写 PLAN.md，将「（待填充）」替换为实际内容。在填充完毕之前禁止写业务代码！"
			} else {
				reminder += "每完成一个子任务，必须立即将 TODO.md 中对应行标记为 - [x]。不要一口气写完再打勾。"
			}
			workingMemory = append(workingMemory, schema.Message{
				Role:    schema.RoleUser,
				Content: reminder,
			})
		}

		// ═══════════════════════════════════════════════════════════════
		// 异步子智能体通知注入：每轮检查是否有子智能体已完成
		// 完成的子智能体结果作为 User 消息注入 workingMemory，
		// 主 Agent 在本轮推理时即可感知到结果，无需手动调用 check_subagent。
		// ═══════════════════════════════════════════════════════════════
		e.injectSubagentNotifications(&workingMemory)

		var contextHistory []schema.Message

		contextHistory = append(contextHistory, systemMsg)

		contextHistory = append(contextHistory, workingMemory...)

		// 2. 【核心注入点】: 在向 Provider 发起推理前，过一遍内存压缩器！
		//无论你带出了多少上下文，如果字符总数超标，早期日志将被掩码化，超大日志将被掐头去尾

		// 2. 【核心注入点】: 在向 Provider 发起推理前，过一遍内存压缩器！
		compactedContext := e.compactor.Compact(contextHistory)
		turnSpan.AddAttribute("context_message_count", len(compactedContext))

		// 3. 后续的 Provider.Generate 全面使用被保护过的新鲜上下文 (compactedContext)
		// 2. ================= Phase 1: Thinking =================

		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}

			// 【埋点 3】：记录 Thinking 调用
			thinkCtx, thinkSpan := observability.StartSpan(turnCtx, "LLM.Thinking")
			thinkResp, err := e.provider.Generate(thinkCtx, compactedContext, nil)
			thinkSpan.EndSpan() // 结束思考跨度
			if err != nil {
				return fmt.Errorf("Thinking 阶段生成失败: %w", err)
			}
			if thinkResp.Content != "" {
				// 将思考过程持久化到 Session 中！
				session.Append(*thinkResp)
				// 把它追加到当前这一轮的临时上下文中，供 Action 阶段使用
				compactedContext = append(compactedContext, *thinkResp)
			}

		}
		// 3. ================= Phase 2: Action =================
		// 【埋点 4】：记录 Action 调用

		actCtx, actSpan := observability.StartSpan(turnCtx, "LLM.Action")
		actionResp, err := e.provider.Generate(actCtx, compactedContext, availableTools)
		actSpan.EndSpan() // 结束行动跨度
		if err != nil {
			return fmt.Errorf("Action 阶段生成失败: %w", err)
		}

		// 【驾驭精髓】：注意，写入 Session（硬盘/全量内存）的永远是全量的真实响应，不受 Compact 影响！
		// // Compact 只作用于本轮发给大模型的那个临时 Context。
		session.Append(*actionResp)

		compactedContext = append(compactedContext, *actionResp)

		if actionResp.Content != "" && reporter != nil {
			reporter.OnMessage(ctx, actionResp.Content)
		}

		if len(actionResp.ToolCalls) == 0 {
			// 如果没有工具调用，说明本次任务已完成，打破 ReAct 循环，挂起等待人类的下一条指令
			turnSpan.EndSpan() // 结束 Turn 跨度
			break
		}

		// 4. ================= 并发执行底层工具 =================
		lastToolCall, lastResult := e.executeToolsInParallel(turnCtx, actionResp.ToolCalls, &contextHistory, reporter)

		// 将本轮工具执行结果（Observation）持久化到 Session 中
		observationMsgs := contextHistory[len(contextHistory)-len(actionResp.ToolCalls):]
		session.Append(observationMsgs...)

		// 2. 【核心防线】：在准备进入下一轮之前，进行死循环探测！
		//    取本轮最后一个工具调用及其执行结果，交给 ReminderInjector 诊断。
		//    如果连续 N 次相同参数的失败被检测到，注入严厉提醒打破执念。
		reminderMsg := e.injector.CheckAndInject(lastToolCall, lastResult)
		if reminderMsg != nil {
			// 如果触发了干预规则，将这条严厉的提醒作为 User 消息，强制追加到 Session 的最末尾！
			// 大模型在下一轮被唤醒时，第一眼就会看到这句话，从而打破局部执念。
			session.Append(*reminderMsg)
			log.Printf("[Engine] ⚠️ 触发死循环干预！注入修正指令到 Session\n")
		}

		turnSpan.EndSpan() // 结束 Turn 跨度（进入下一轮前）
	}

	return nil
}

// injectBackgroundNotifications 检查被追踪的后台任务,将已完成/异常退出的进程
// 作为系统通知注入到对话上下文中。这样模型无需主动调用 TaskOutput 也能获知
// 后台进程的状态变更 —— 类比操作系统中的异步信号机制。
func (e *AgentEngine) injectBackgroundNotifications(contextHistory *[]schema.Message, turnCount int) {
	if turnCount <= 1 {
		return // 第一轮不需要通知
	}

	e.trackedTaskIDsMu.Lock()
	defer e.trackedTaskIDsMu.Unlock()

	if len(e.trackedTaskIDs) == 0 {
		return
	}

	var notices []string

	for taskID := range e.trackedTaskIDs {
		snapshot, ok := e.taskManager.Get(taskID)
		if !ok {
			// 任务已被清理 (不应该发生,但防御性处理)
			delete(e.trackedTaskIDs, taskID)
			continue
		}

		if snapshot.Done {
			notice := fmt.Sprintf(
				"[后台进程通知] 任务 %s (%s) 已结束: status=%s exit=%d。如需要可查看 TaskOutput 获取完整日志。",
				taskID, snapshot.Command, snapshot.Status.String(), snapshot.ExitCode,
			)
			notices = append(notices, notice)
			delete(e.trackedTaskIDs, taskID) // 已通知,不再追踪
			log.Printf("[Engine] 异步通知: 后台任务 %s 已退出 (status=%s)\n", taskID, snapshot.Status.String())
		}
	}

	if len(notices) == 0 {
		return
	}

	// 将通知作为 System Message 注入上下文
	// 每条通知单独一条消息,更清晰
	for _, notice := range notices {
		*contextHistory = append(*contextHistory, schema.Message{
			Role:    schema.RoleUser, // 使用 User Role 让模型感知这是"外部事件"
			Content: notice,
		})
	}

	log.Printf("[Engine] 注入了 %d 条后台任务通知到对话上下文\n", len(notices))
}

// injectSubagentNotifications 检查 SubagentManager 中所有已完成的子智能体，
// 将其结果作为 User 消息注入 workingMemory。
// 每个已完成的子智能体只通知一次（通过 MarkNotified 防止重复注入）。
func (e *AgentEngine) injectSubagentNotifications(workingMemory *[]schema.Message) {
	mgr := tools.GetSubagentManager()
	injected := 0

	for _, snap := range mgr.List() {
		if !snap.Done || snap.Notified {
			continue
		}

		var content string
		if snap.Error != nil {
			content = fmt.Sprintf("[子智能体通知] 任务 '%s' (ID: %s) 执行失败: %v",
				snap.Prompt, snap.ID, snap.Error)
		} else {
			content = fmt.Sprintf("[子智能体通知] 任务 '%s' (ID: %s) 已完成，探索报告:\n%s",
				snap.Prompt, snap.ID, snap.Summary)
		}

		*workingMemory = append(*workingMemory, schema.Message{
			Role:    schema.RoleUser,
			Content: content,
		})

		mgr.MarkNotified(snap.ID)
		injected++
	}

	if injected > 0 {
		log.Printf("[Engine] 📬 注入了 %d 条子智能体完成通知到对话上下文\n", injected)
	}
}

// streamGenerate 发起流式推理，边接收边打印，最终返回组装好的 schema.Message。
//
// 参数 isThinking 控制提示前缀的显示方式：
//   - true:  打印思考过程，所有文本增量直接输出
//   - false: 打印助手回复，所有文本增量直接输出
func (e *AgentEngine) streamGenerate(
	ctx context.Context,
	messages []schema.Message,
	availableTools []schema.ToolDefinition,
	isThinking bool,
) (*schema.Message, error) {
	ch, err := e.provider.StreamGenerate(ctx, messages, availableTools)
	if err != nil {
		return nil, fmt.Errorf("启动流式请求失败: %w", err)
	}

	return e.consumeStream(ctx, ch)
}

// consumeStream 消费 StreamEvent 通道，实现"边打印边累积"。
//
// 引擎侧数据流：
//
//	chan StreamEvent ──▶ select {
//	                        case <-ctx.Done():  → 取消
//	                        case ev, ok := <-ch:
//	                          switch ev.Type {
//	                          case ThinkingDelta:
//	                            os.Stdout ← ev.Delta          // 实时逐字打印思考
//	                            acc.thinking.WriteString(ev)  // 累积到思考缓冲区
//	                          case TextDelta:
//	                            os.Stdout ← ev.Delta          // 实时逐字打印回复
//	                            acc.content.WriteString(ev)   // 累积到文本缓冲区
//	                          case ToolCallBegin:
//	                            acc.toolMap[index] = {ID, Name}  // 建立槽位
//	                          case ToolCallArgsDelta:
//	                            acc.toolMap[index].ArgsBuf.WriteString(ev)  // 拼接 JSON
//	                          case Done:
//	                            return acc.Finalize()
//	                            // Finalize: content → msg.Content
//	                            //           argsBuf[i] → json.RawMessage → msg.ToolCalls[i]
//	                            //           sort by index → 保证顺序
//	                          }
//	                        }
//
// 职责：
//  1. 实时将文本/思考增量打印到终端（用户体验）
//  2. 将工具调用片段路由到 StreamAccumulator（结构化累积）
//  3. 监听 ctx.Done() 支持中途取消
//  4. 在流结束时返回组装好的 schema.Message
func (e *AgentEngine) consumeStream(ctx context.Context, ch <-chan provider.StreamEvent) (*schema.Message, error) {
	acc := provider.NewStreamAccumulator()

	for {
		select {
		case <-ctx.Done():
			// 上下文被取消（例如用户 Ctrl+C 或超时）
			return nil, ctx.Err()

		case ev, ok := <-ch:
			if !ok {
				// channel 被关闭但未收到 Done/Error（防御性编程）
				log.Println("[Engine] 警告：流通道意外关闭，使用已累积的内容")
				return acc.Finalize(), nil
			}

			switch ev.Type {
			case provider.StreamEventError:
				return nil, ev.Error

			case provider.StreamEventDone:
				return acc.Finalize(), nil

			case provider.StreamEventThinkingDelta:
				fmt.Print(ev.Delta) // 实时打印思考内容
				acc.Ingest(ev)

			case provider.StreamEventTextDelta:
				fmt.Print(ev.Delta) // 实时打印回复内容
				acc.Ingest(ev)

			case provider.StreamEventToolCallBegin:
				log.Printf("  -> 📞 [流式] 模型请求调用工具 #%d: %s (%s)\n",
					ev.ToolCallIndex, ev.ToolCallName, ev.ToolCallID)
				acc.Ingest(ev)

			case provider.StreamEventToolCallArgsDelta:
				acc.Ingest(ev)
			}
		}
	}
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
//  4. 后台任务感知：引擎在执行 Bash 工具时,通过对比执行前后的 TaskManager 状态
//     自动发现新启动的后台进程,加入追踪集合用于后续异步通知。
func (e *AgentEngine) executeToolsInParallel(ctx context.Context, toolCalls []schema.ToolCall, contextHistory *[]schema.Message, reporter Reporter) (schema.ToolCall, schema.ToolResult) {
	n := len(toolCalls)

	// 预分配结果槽位，每个 goroutine 写入自己的索引，无需互斥锁
	type slot struct {
		call        schema.ToolCall
		result      schema.ToolResult
		finalOutput string
	}
	slots := make([]slot, n)

	// 【后台任务追踪】记录执行前已知的所有 Task ID
	knownTasksBefore := e.snapshotTaskIDs()

	// =========================================================================
	// 【并发策略】只读并发、涉写串行
	//
	// 核心洞察：ReAct 循环同一 Turn 内，模型决策已在 LLM 生成阶段"凝固"，
	// 不存在"读取→模型决策→写入"的 TOCTOU 模式。数据竞争发生在纯 I/O 层：
	//   - 纯读批次：所有操作无副作用，安全并发，收益最大
	//   - 含写批次：退化为顺序执行，从根本上消除写丢失和脏读
	//
	// 若未来需要更细粒度控制，可为 bash 增加 read_only 参数，
	// 允许 grep/ls 等无副作用命令参与纯读并发批次。
	// =========================================================================
	hasWrite := false
	for _, tc := range toolCalls {
		if e.isWriteTool(tc.Name) {
			hasWrite = true
			break
		}
	}

	if hasWrite {
		// 串行路径：按模型声明顺序逐一执行，保证 happens-before 语义。
		// 后续 Observation 追加顺序与执行顺序一致，模型天然可预测。

		// 【工作区写锁】跨 Run() 互斥：同一时刻只有一个 Agent 能修改文件。
		// 锁范围覆盖整个写批次 (batch)，确保同一 Run() 内的多个写工具
		// 原子执行完毕前，其他 Run() 的读/写操作均被阻塞。
		e.wsRWMu.Lock()
		defer e.wsRWMu.Unlock()

		for i, tc := range toolCalls {
			// 【触发 Reporter】: 报告即将执行的工具
			if reporter != nil {
				reporter.OnToolCall(ctx, tc.Name, string(tc.Arguments))
			}
			log.Printf("  -> 🛠️ [串行] 执行工具: %s, 参数: %s\n", tc.Name, string(tc.Arguments))

			result := e.registry.Execute(ctx, tc)

			// 【核心拦截与注入】
			finalOutput := result.Output
			if result.IsError {
				// 发生错误，交由 RecoveryManager 诊断并注入“锦囊妙计”
				finalOutput = e.recovery.AnalyzeAndInject(tc.Name, result.Output)
				log.Printf(" -> [Go-%d] ❌ 注入救援指南: %s\n", i, finalOutput)
			} else {
				log.Printf("  -> [Go-%d] ✅ 工具执行成功 (返回 %d 字节)\n", i, len(result.Output))
			}
			// 【触发 Reporter】: 汇报工具执行结果（截断过长输出）
			if reporter != nil {
				displayOutput := finalOutput
				if len(displayOutput) > 200 {
					displayOutput = displayOutput[:200] + "... (已截断)"
				}
				reporter.OnToolResult(ctx, tc.Name, displayOutput, result.IsError)
			}
			slots[i] = slot{call: tc, result: result, finalOutput: finalOutput}
		}
	} else {
		// 并行路径：全读批次，Goroutine 并发执行。
		// 预分配 slots + 值传递 idx → 不同内存地址，无数据竞争。
		//
		// 【并发上限控制】使用 Buffered Channel 作为计数信号量 (Counting Semaphore):
		//   - sem <- struct{}{} 获取令牌（满则阻塞排队）
		//   - <-sem 归还令牌（唤醒下一个等待者）
		// WaitGroup 只管"是否全部完成"，Semaphore 只管"同时最多几个在跑"——
		// 两个原语正交组合，零侵入。
		//
		// 为什么需要上限？本地 read_file 瞬间读5个不成问题，但如果挂载了
		// fetch_web_url / query_jira_api 等网络工具，一次性50个并发请求
		// 会触发目标站防火墙或 API Rate Limit 封杀。
		const maxConcurrent = 5
		sem := make(chan struct{}, maxConcurrent)
		var wg sync.WaitGroup
		wg.Add(n)

		for i, tc := range toolCalls {
			go func(idx int, call schema.ToolCall) {
				// 获取并发令牌，同时尊重 ctx 取消信号
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					errResult := schema.ToolResult{IsError: true, Output: ctx.Err().Error()}
					if reporter != nil {
						reporter.OnToolResult(ctx, call.Name, ctx.Err().Error(), true)
					}
					slots[idx] = slot{call: call, result: errResult}
					wg.Done()
					return
				}
				defer func() {
					<-sem // 归还令牌
					wg.Done()
				}()

				// 【触发 Reporter】: 报告即将执行的工具
				if reporter != nil {
					reporter.OnToolCall(ctx, call.Name, string(call.Arguments))
				}
				log.Printf("  -> 🛠️ [并行] 执行工具: %s, 参数: %s\n", call.Name, string(call.Arguments))

				// 【工作区读锁】与写者互斥，多个读者之间可并发
				e.wsRWMu.RLock()
				result := e.registry.Execute(ctx, call)
				e.wsRWMu.RUnlock()

				// 【核心拦截与注入】
				finalOutput := result.Output
				if result.IsError {
					// 发生错误，交由 RecoveryManager 诊断并注入“锦囊妙计”
					finalOutput = e.recovery.AnalyzeAndInject(call.Name, result.Output)
					log.Printf(" -> [Go-%d] ❌ 注入救援指南: %s\n", idx, finalOutput)
				} else {
					log.Printf("  -> [Go-%d] ✅ 工具执行成功 (返回 %d 字节)\n", idx, len(result.Output))
				}

				// 【触发 Reporter】: 汇报工具执行结果（截断过长输出）
				if reporter != nil {
					displayOutput := finalOutput
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "... (已截断)"
					}
					reporter.OnToolResult(ctx, call.Name, displayOutput, result.IsError)
				}
				slots[idx] = slot{call: call, result: result, finalOutput: finalOutput}
			}(i, tc)
		}

		wg.Wait()
	}

	// 按原始顺序（索引 0 → n-1）将 Observation 组装回上下文
	for _, s := range slots {
		if s.result.IsError {
			log.Printf("  -> ❌ 工具执行报错 [%s]: %s\n", s.call.Name, s.finalOutput)
		} else {
			log.Printf("  -> ✅ 工具执行成功 [%s] (返回 %d 字节)\n", s.call.Name, len(s.result.Output))
		}

		// 将工具执行的观察结果 (Observation) 封装为 User Message 追加到上下文中
		// 注意：ToolCallID 必须携带！这是维系大模型推理链条的关键
		observationMsg := schema.Message{
			Role:       schema.RoleUser,
			Content:    s.finalOutput,
			ToolCallID: s.call.ID,
		}
		*contextHistory = append(*contextHistory, observationMsg)
	}

	// 【后台任务追踪】对比执行前后的 TaskManager 快照,发现本轮新启动的后台进程
	knownTasksAfter := e.snapshotTaskIDs()
	e.trackedTaskIDsMu.Lock()
	for id := range knownTasksAfter {
		if !knownTasksBefore[id] {
			e.trackedTaskIDs[id] = struct{}{}
			log.Printf("[Engine] 发现新后台任务: %s, 已加入追踪集合\n", id)
		}
	}
	e.trackedTaskIDsMu.Unlock()

	// 返回本轮最后一个工具调用及其原始结果，供上层进行死循环探测
	last := slots[n-1]
	return last.call, last.result
}
func (e *AgentEngine) isWriteTool(name string) bool {
	switch name {
	case "read_file", "TaskOutput", "spawn_subagent":
		// spawn_subagent 本质是只读委派：子智能体持有 readOnlyRegistry，
		// 只能读文件、执行只读 bash，不会修改工作区。因此可以安全地并行执行。
		return false
	default:
		return true
	}
}

// snapshotTaskIDs 获取当前 TaskManager 中所有任务 ID 的快照集合。
// 用于在工具执行前后做 diff,自动发现新启动的后台进程。
func (e *AgentEngine) snapshotTaskIDs() map[string]bool {
	tasks := e.taskManager.List()
	set := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		set[t.ID] = true
	}
	return set
}

// RunSub 是专为 Subagent 拉起的一次性受限循环。
// 它不依赖外部 Session，打完就跑。
// Reporter：为了让用户在终端看到子智能体的工作轨迹，我们将主线程的 Reporter 透传进来，并打上特殊标记。
func (e *AgentEngine) RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry tools.Registry, reporter any) (string, error) {

	// 【核心优化】：子智能体极其容易偷懒。我们必须在 System Prompt 中严厉警告它必须使用工具！
	contextHistory := []schema.Message{
		{
			Role: schema.RoleSystem,
			Content: `你是一个专门负责深度探索的探路者 (Explorer Subagent)。
你的任务是根据主架构师的指令，在当前工作区内仔细阅读代码、查阅日志，搜集足够的信息。

【核心纪律】
1. 你必须、且只能依靠内置工具（如 bash 的 find/grep，或 read_file）去寻找答案。绝对不允许凭空捏造或猜测！
2. 如果你没有找到确切的答案，你必须继续使用工具深入搜索。
3. 当且仅当你找到了确切的线索后，停止调用工具，直接输出一段纯文本作为你的终极汇报。主架构师会根据你的汇报来做下一步决策。`,
		},
		{
			Role:    schema.RoleUser,
			Content: taskPrompt,
		},
	}

	// 限制子智能体最多只能跑 10 个 Turn，防止它自己卡死
	const maxSubTurns = 10
	turnCount := 0

	for {
		turnCount++
		if turnCount > maxSubTurns {
			return "", fmt.Errorf("子智能体探索过于深入，超过 %d 轮被强制召回，请主 Agent 给它更明确的指令", maxSubTurns)
		}

		// 【埋点 2】：记录单次 Turn 循环
		turnCtx, turnSpan := observability.StartSpan(ctx, fmt.Sprintf("Turn-%d", turnCount))

		defer turnSpan.EndSpan() // 利用 defer，哪怕遇到了 break 或 error 也会计算耗时

		// 【驾驭底线】：子智能体仅能获取传入的只读工具注册表
		availableTools := readOnlyRegistry.GetAvailableTools()

		compactedContext := e.compactor.Compact(contextHistory)

		// 子任务要求急速响应，强制关闭主体的慢思考，直接预测行动
		actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
		if err != nil {
			return "", fmt.Errorf("子智能体推理失败: %w", err)
		}

		contextHistory = append(contextHistory, *actionResp)

		// 【核心退出条件】：子智能体一旦不调用工具了，说明它做好了总结汇报
		if len(actionResp.ToolCalls) == 0 {
			// 直接将它的这段汇报内容剥离出来返回给上层
			return actionResp.Content, nil
		}

		// 执行只读工具的并发循环
		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)
			go func(idx int, call schema.ToolCall) {
				defer wg.Done()

				// 【可视化的关键】：让终端用户看到 Subagent 正在干嘛
				var r Reporter
				if reporter != nil {
					r = reporter.(Reporter)
					r.OnToolCall(ctx, fmt.Sprintf("[Subagent] %s", call.Name), string(call.Arguments))
				}

				result := readOnlyRegistry.Execute(turnCtx, call)

				finalOutput := result.Output
				if result.IsError {
					finalOutput = e.recovery.AnalyzeAndInject(call.Name, result.Output)
				}

				if reporter != nil {
					display := finalOutput
					if len(display) > 200 {
						display = display[:200] + "... (已截断)"
					}
					r.OnToolResult(ctx, fmt.Sprintf("[Subagent] %s", call.Name), display, result.IsError)
				}

				observationMsgs[idx] = schema.Message{
					Role:       schema.RoleUser,
					Content:    finalOutput,
					ToolCallID: call.ID,
				}
			}(i, toolCall)
		}

		wg.Wait()
		contextHistory = append(contextHistory, observationMsgs...)
	}
}
