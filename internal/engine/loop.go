package engine

import (
	"context"
	"fmt"
	"log"
	"sync"

	ctxpkg "github.com/lhuan/go-tiny-claw/internal/context"

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
	compactor *ctxpkg.Compactor // 压缩器实例，防止大模型上下文 OOM
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		composer:       ctxpkg.NewPromptComposer(workDir),
		WorkDir:        workDir,
		EnableThinking: enableThinking,
		taskManager:    tools.GetTaskManager(),
		trackedTaskIDs: make(map[string]struct{}),
		// 自适应压缩器：默认 200k Token 窗口，保护最近 6 条消息
		compactor: ctxpkg.NewCompactor(200000, 6),
	}
}

// SetMaxContextWindow 设置模型的上下文窗口大小（Token 数），供 Compactor 进行自适应压缩决策。
// 应在引擎创建后、运行前调用。不同模型的典型值：
//   - Gemini 3.1 Pro: 1000000
//   - Claude Sonnet:  200000
//   - 智谱 GLM-4:     128000
//   - Llama 本地模型:  8192
func (e *AgentEngine) SetMaxContextWindow(maxTokens int) {
	e.compactor.MaxWindowTokens = maxTokens
}

// SkillLoader 返回引擎内部 Composer 持有的 SkillLoader 引用，
// 供外部注册 read_skill 工具使用（渐进式暴露架构）。
func (e *AgentEngine) SkillLoader() *ctxpkg.SkillLoader {
	return e.composer.SkillLoader()
}

// Run 启动 Agent 的生命周期。
//
// reporter 是引擎向外界汇报状态的唯一出口。传入 nil 时引擎将静默运行
// （仅保留 log.Printf 级别的内部日志）。
func (e *AgentEngine) Run(ctx context.Context, session *Session, userPrompt string, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，锁定工作区: %s\n", session.ID, session.WorkDir)

	// 将用户输入追加到 Session 历史，确保：
	// 1. Thinking 阶段至少有一条 User 消息（Anthropic API 强制要求）
	// 2. 多轮对话时 Working Memory 能正确截取到本轮用户输入
	session.Append(schema.Message{Role: schema.RoleUser, Content: userPrompt})

	// 根据当前 Session 的工作区，动态组装最新的 System Prompt
	composer := ctxpkg.NewPromptComposer(session.WorkDir)
	// 【核心修改】动态组装 System Prompt，彻底替换掉以前硬编码的面条提示词！
	systemMsg := composer.Build()

	// 确保引擎退出时清理所有后台进程
	defer e.taskManager.Shutdown()

	for {
		availableTools := e.registry.GetAvailableTools()
		// 1. 【上下文组装】: System Prompt + 双维度截取 Working Memory
		//    - 最多 6 条消息（条数维度）
		//    - 最多 50000 字符（Token 预算维度，防止巨型 ToolResult 撑爆上下文）
		workingMemory := session.GetWorkingMemory(6, 50000)

		var contextHistory []schema.Message

		contextHistory = append(contextHistory, systemMsg)

		contextHistory = append(contextHistory, workingMemory...)

		// 2. 【核心注入点】: 在向 Provider 发起推理前，过一遍内存压缩器！
		// 无论你带出了多少上下文，如果字符总数超标，早期日志将被掩码化，超大日志将被掐头去尾
		contextHistory = e.compactor.Compact(contextHistory)

		// 3. ================= Phase 1: Thinking =================

		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}
			thinkResp, err := e.provider.Generate(ctx, contextHistory, nil)
			if err != nil {
				return fmt.Errorf("Thinking 阶段生成失败: %w", err)
			}
			if thinkResp.Content != "" {
				// 将思考过程持久化到 Session 中！
				session.Append(*thinkResp)
				// 把它追加到当前这一轮的临时上下文中，供 Action 阶段使用
				contextHistory = append(contextHistory, *thinkResp)
			}

		}
		// 4. ================= Phase 2: Action =================
		actionResp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("Action 阶段生成失败: %w", err)
		}

		// 【自适应压缩】从 API 响应中提取真实 Token 消耗，喂给 Compactor 用于下一轮压缩决策
		if actionResp.Usage != nil && actionResp.Usage.PromptTokens > 0 {
			e.compactor.UpdateUsage(actionResp.Usage.PromptTokens)
			log.Printf("[Engine] 📊 Token 消耗: Prompt=%d, Completion=%d, Total=%d",
				actionResp.Usage.PromptTokens, actionResp.Usage.CompletionTokens, actionResp.Usage.TotalTokens)
		}

		// 将大模型的行动响应持久化到 Session 中
		session.Append(*actionResp)

		contextHistory = append(contextHistory, *actionResp)

		if actionResp.Content != "" && reporter != nil {
			reporter.OnMessage(ctx, actionResp.Content)
		}

		if len(actionResp.ToolCalls) == 0 {
			// 如果没有工具调用，说明本次任务已完成，打破 ReAct 循环，挂起等待人类的下一条指令
			break
		}

		// 5. ================= 并发执行底层工具 =================
		e.executeToolsInParallel(ctx, actionResp.ToolCalls, &contextHistory, reporter)

		// 将本轮工具执行结果（Observation）持久化到 Session 中
		observationMsgs := contextHistory[len(contextHistory)-len(actionResp.ToolCalls):]
		session.Append(observationMsgs...)
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
func (e *AgentEngine) executeToolsInParallel(ctx context.Context, toolCalls []schema.ToolCall, contextHistory *[]schema.Message, reporter Reporter) {
	n := len(toolCalls)

	// 预分配结果槽位，每个 goroutine 写入自己的索引，无需互斥锁
	type slot struct {
		call   schema.ToolCall
		result schema.ToolResult
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

			// 【触发 Reporter】: 汇报工具执行结果（截断过长输出）
			if reporter != nil {
				displayOutput := result.Output
				if len(displayOutput) > 200 {
					displayOutput = displayOutput[:200] + "... (已截断)"
				}
				reporter.OnToolResult(ctx, tc.Name, displayOutput, result.IsError)
			}
			slots[i] = slot{call: tc, result: result}
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

				// 【触发 Reporter】: 汇报工具执行结果（截断过长输出）
				if reporter != nil {
					displayOutput := result.Output
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "... (已截断)"
					}
					reporter.OnToolResult(ctx, call.Name, displayOutput, result.IsError)
				}
				slots[idx] = slot{call: call, result: result}
			}(i, tc)
		}

		wg.Wait()
	}

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
}
func (e *AgentEngine) isWriteTool(name string) bool {
	switch name {
	case "read_file", "TaskOutput":
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
