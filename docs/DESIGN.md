# 设计文档

> go-tiny-claw 的架构设计、核心机制和技术演进。

## 目录

- [系统架构](#系统架构)
- [核心机制](#核心机制)
  - [ReAct 引擎](#react-引擎)
  - [并发模型](#并发模型)
  - [会话管理](#会话管理)
  - [自适应上下文压缩](#自适应上下文压缩)
  - [编辑工具的 4 级匹配策略](#编辑工具的-4-级匹配策略)
  - [动态权限引擎](#动态权限引擎)
  - [子代理系统](#子代理系统)
  - [流式输出架构](#流式输出架构)
  - [可观测性](#可观测性)
  - [技能系统](#技能系统)
- [构建演进](#构建演进)
  - [Phase 1: 引擎骨架](#phase-1-引擎骨架)
  - [Phase 2: 四大原语](#phase-2-四大原语)
  - [Phase 3: 并发安全](#phase-3-并发安全)
  - [Phase 4: 多平台接入](#phase-4-多平台接入)
  - [Phase 5: 上下文工程](#phase-5-上下文工程)
  - [Phase 6: 工程化](#phase-6-工程化)
- [从零复现指南](#从零复现指南)

---

## 系统架构

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│                        入口交互层  Entry & UI Layer                                 │
│                                                                                   │
│    CLI (cobra)            飞书 Bot               企业微信 Bot                       │
│    run · repl ·           WebSocket              Webhook                           │
│    server · feishu        长连接 · 审批卡片       回调 · 审批                        │
│                                                                                   │
│                统一 Reporter 接口：Terminal · Feishu · WeChat                       │
│                Bootstrap 引擎初始化栈 · 管道模式 · ANSI 终端输出                      │
├───────────────────────────────────────────────────────────────────────────────────┤
│                        核心引擎层  Core Engine Layer                                │
│                                                                                   │
│    ReAct 循环              Provider 适配           Schema 定义层                     │
│    Thinking + Action       Claude · OpenAI         Message · ToolCall              │
│    并行/串行工具执行       重试 · 限流 · Mock       ToolResult · TokenUsage          │
│    死循环检测              StreamAccumulator                                       │
│                                                                                   │
│               Session 管理：JSONL 持久化 · 多用户隔离 · 断点续跑                     │
│               Subagent 调度：异步 spawn · context 取消传播 · 自动通知                │
├───────────────────────────────────────────────────────────────────────────────────┤
│                     上下文工程层  Context Engineering Layer                          │
│                                                                                   │
│    PromptComposer          Compactor              可观测性                          │
│    三段式 System Prompt    5 级自适应压缩         CostTracker 费用追踪               │
│    技能元数据索引          Token 利用率驱动       Span 树分布式追踪                  │
│    RecoveryManager         SkillLoader            File · Log · OTel Exporter       │
│                                                                                   │
│               错误恢复注入 · 渐进式技能暴露 · 压缩级别自动切换                       │
├───────────────────────────────────────────────────────────────────────────────────┤
│                     工具与执行层  Tool Execution Layer                               │
│                                                                                   │
│    11 个内置工具            权限引擎               子代理系统                        │
│    read/write/edit_file    COW 无锁读取           SubagentManager                  │
│    bash · search_files     正则热重载             只读隔离 · context 传播            │
│    fetch_url · read_skill  14 条内置规则          后台进程 PCB 模型                  │
│    spawn/check_subagent    审批流集成             TaskManager · RingBuffer          │
│                                                                                   │
│              只读并行(信号量=5) · 写入串行 · Workspace RWMutex                      │
└───────────────────────────────────────────────────────────────────────────────────┘
```

依赖方向严格自上而下，无循环引用。`tools` 包通过 `AgentRunner` 接口打破对 `engine` 包的依赖（依赖倒置）。

---

## 核心机制

### ReAct 引擎

每轮执行流程：

```
用户输入
  ▼
上下文组装（System Prompt + Working Memory）
  ▼
子代理通知注入 → Plan 模式提醒 → 自适应压缩
  ▼
Phase 1: Thinking（可选，不带工具，强制慢思考）
  ▼
Phase 2: Action（带全部工具定义）
  ├── 无 ToolCall → 循环终止，等待下一条用户输入
  └── 有 ToolCall → 工具执行（只读并行 / 写入串行）
        ▼
      错误恢复（RecoveryManager 注入救援指南）
        ▼
      死循环检测（MD5 指纹，3 次相同失败 → 强纠正）
        ▼
      回到 Phase 1
```

**关键设计**：Thinking 和 Action 均使用流式输出，通过 `Reporter.OnStreamDelta` 逐字推送。写入 Session 的永远是全量真实响应，Compact 只作用于发给模型的临时 Context。

**两阶段设计动机**：

```
Phase 1: Thinking
  provider.Generate(ctx, history, nil)  // 不传工具定义，强制纯推理
  → 输出持久化到 Session，供 Action 阶段参考

Phase 2: Action
  provider.Generate(ctx, history, tools)
  → 无 ToolCall → break（任务完成）
  → 有 ToolCall → executeToolsInParallel()
```

---

### 并发模型

```
              ┌──────────────────┐
              │    Run() 请求     │
              └────────┬─────────┘
                       │
              ┌────────▼─────────┐
              │   Workspace 锁    │  ← RWMutex
              └────────┬─────────┘
                       │
        ┌──────────────┼──────────────┐
        │              │              │
  ┌─────▼─────┐ ┌─────▼─────┐ ┌─────▼─────┐
  │  只读工具   │ │  只读工具   │ │  写入工具   │
  │  (并行)    │ │  (并行)    │ │  (串行)    │
  │  信号量=5  │ │  信号量=5  │ │  独占执行   │
  └───────────┘ └───────────┘ └───────────┘
```

**三级并发控制**：

| 层级 | 机制 | 作用 |
|------|------|------|
| WaitGroup | 屏障同步 | 等待所有 goroutine 完成 |
| Semaphore | Buffered Channel (cap=5) | 限制并发上限，防触发 API 限流 |
| RWMutex | 工作区读写锁 | 跨 Run() 文件系统一致性 |

**核心洞察**：ReAct 同一 Turn 内模型决策已在 LLM 生成阶段"凝固"，不存在 TOCTOU。只读批次安全并发，含写批次退化为顺序执行。

**并行执行的有序性保障**：
- 预分配 `slots []slot`，每个 goroutine 通过值传递捕获索引 `i`，写入 `slots[i]`
- 各 goroutine 操作不同内存地址，无数据竞争
- `wg.Wait()` 作为 happens-before 屏障

**锁粒度**：控制在工具批次级而非整个 `Run()` 生命周期。LLM 推理阶段不持有任何锁，其他 Run() 可自由读写。

---

### 会话管理

每个用户/聊天拥有独立的 Session，通过 `GlobalSessionMgr`（`sync.RWMutex` + Map）管理。

#### 持久化

```
Session.Append(msgs)
  ├── 内存：追加到 s.history
  └── 磁盘：JSONL 追加到 .claw/sessions/{id}.jsonl

SessionManager.GetOrCreate(id)
  ├── 内存命中 → 直接返回
  └── 内存未命中 → 从磁盘 LoadFromDisk() 恢复
```

#### 双维度滑动窗口截断

`GetWorkingMemory(limit=6, maxChars=50000)` 从尾部向前遍历，两个维度先到先停。

**孤儿 ToolResult 清理**：截断后自动检测并移除没有对应 ToolCall 的 ToolResult，防止 API 400 错误。

---

### 自适应上下文压缩

Compactor 根据 Token 利用率（`PromptTokens / MaxWindowTokens`）动态选择压缩级别：

| 利用率 | 级别 | 策略 |
|--------|------|------|
| < 50% | 无压缩 | 保持完整上下文 |
| 50–70% | 轻柔 | 仅遮蔽远端历史 |
| 70–85% | 标准 | 遮蔽远端 + 最近消息首尾各 500 字符 |
| 85–95% | 激进 | 遮蔽远端 + 最近消息首尾各 200 字符 |
| > 95% | 紧急 | 遮蔽全部，仅保留最近 2 条消息 |

首次调用无真实 Token 数据时，回退到字符估算模式（1 Token ≈ 3 中文字）。

---

### 编辑工具的 4 级匹配策略

`edit_file` 在精确匹配失败时依次降级：

```
L1: 精确匹配          — 原始字符串完全一致
L2: 换行标准化        — \r\n → \n 后匹配
L3: TrimSpace 匹配    — 去除首尾空白后匹配
L4: 滑动窗口匹配      — 逐行去缩进 + 智能缩进锚定
```

每一级都做唯一性校验：匹配到多处时拒绝执行，要求提供更多上下文。

**为什么需要 4 级匹配**：LLM 生成的代码片段经常带不一致缩进（尤其是从流式输出中拼接时），精确匹配极易失败。4 级降级策略吸收 LLM 的格式误差。

---

### 动态权限引擎

基于 **Copy-on-Write** 架构：

```
Check(ctx, command)
  ├── atomic.Load(configSnapshot)  ← 无锁读取
  ├── 遍历预排序规则（按 Priority 降序）
  │   └── regex.MatchString(command) → 返回 PermissionResult
  └── 无匹配 → 使用 DefaultAction
```

**热重载**：后台 goroutine 轮询文件 modtime，检测到变更后重新编译正则、创建新快照、原子替换指针。读操作零开销。

**审批集成**：`ask` 策略通过 `ApprovalHandler` 接口接入审批流：
- CLI 模式：`ConsoleApprovalHandler`（终端 stdin 交互）
- 飞书模式：`FeishuApprovalHandler`（阻塞等待飞书审批卡片）
- 权限配置文件不存在时默认 allow，不阻塞正常使用

**内置安全规则**：14 条预置规则覆盖 `rm -rf`、`DROP DATABASE`、`kubectl delete namespace` 等危险操作（5 deny + 5 ask + 4 allow）。

---

### 子代理系统

```
spawn_subagent(task_prompt)
  ▼
SubagentManager.Spawn(parentCtx, runner, prompt, readOnlyRegistry)
  ├── 创建可取消的子 context（parentCtx 取消 → 子代理自动取消）
  └── goroutine: runner.RunSub(subCtx, prompt, readOnlyRegistry)
        ├── 最多 10 轮
        └── 完成后更新 task.Done = true

主循环每轮自动检查：
  injectSubagentNotifications()
    └── 遍历所有 Done 且 !Notified 的子代理 → 注入 User 消息
```

**只读隔离**：子代理的 `readOnlyRegistry` 仅包含 `read_file`、`bash`（带权限引擎）、`read_skill`。

**context 取消传播**：主循环 Ctrl+C 时子代理自动收到取消信号，防止孤儿 goroutine 持续消耗 API 额度。

---

### 流式输出架构

```
Provider (goroutine)
  SDK SSE Stream → type-switch → chan StreamEvent (cap=16)
  defer close(ch)

Engine (main goroutine)
  consumeStream(ctx, ch, reporter, isThinking)
  ├── TextDelta      → reporter.OnStreamDelta(delta, false) + acc.Ingest
  ├── ThinkingDelta  → reporter.OnStreamDelta(delta, true)  + acc.Ingest
  ├── ToolCallBegin  → acc.Ingest
  ├── ToolCallArgsDelta → acc.Ingest
  ├── Done           → return acc.Finalize()
  └── Error          → return err
```

**Reporter 接口**（可插拔输出层）：

```go
type Reporter interface {
    OnThinking(ctx context.Context)
    OnStreamDelta(ctx context.Context, delta string, isThinking bool)
    OnToolCall(ctx context.Context, toolName string, args string)
    OnToolResult(ctx context.Context, toolName string, result string, isError bool)
    OnMessage(ctx context.Context, content string)
}
```

三种实现：`TerminalReporter`（CLI）、`FeishuReporter`（飞书）、`WechatReporter`（企业微信）。

---

### 可观测性

**费用追踪**：`CostTracker` 以装饰器模式包装 `LLMProvider`，自动记录每次 API 调用的 Token 消耗，按模型定价查表计算费用，按 Session 汇总。

**分布式追踪**：基于 Span 层级的追踪系统，支持 Parent-Child 关系的 Span 树，JSON 格式导出到 `.claw/traces/` 目录。

---

### 技能系统

**渐进式暴露**（Progressive Disclosure）：

```
System Prompt 中仅注入：name + description（每个 20 Token）
LLM 需要使用时：调用 read_skill(name="xxx") → 返回完整 SKILL.md

50 技能 × 20 Token = 1,000 Token（vs 急切加载的 100,000 Token）
```

---

## 构建演进

go-tiny-claw 经过 6 个 Phase 从零构建：

```
Phase 1: 引擎骨架
  引擎初始化 → 真实 LLM Provider → 流式 SSE → 工具路由

Phase 2: 四大原语
  ReadFile → WriteFile + Bash → 后台进程管理 (PCB + RingBuffer) → EditFile (4 级匹配)

Phase 3: 并发安全
  读写分离策略 → 信号量限流 → 工作区 RWMutex

Phase 4: 多平台接入
  飞书 (WebSocket) + 企业微信 (Webhook) + CLI | Reporter 统一抽象

Phase 5: 上下文工程
  PromptComposer → 渐进式技能系统 → Session 持久化 → 双维度滑动窗口 → 自适应压缩

Phase 6: 工程化
  Claude Code 集成 → YAML 配置 → 密钥安全 → Docker → 会话持久化 → 审批集成
```

### Phase 1: 引擎骨架

搭建最小可运行的 Agent 循环：ReAct 两阶段、并行工具执行（WaitGroup + 预分配 slots 保证有序性）、流式架构（Channel 桥接 SSE 与 Engine）。

### Phase 2: 四大原语

实现 read_file（8KB 截断）、write_file（自动创建目录）、bash（30s 超时）、edit_file（4 级降级匹配）。引入后台进程管理（OS PCB 模型 + 64KB RingBuffer）。

### Phase 3: 并发安全

读写分离（只读并行 + 写入串行）、信号量限流（Buffered Channel cap=5）、工作区 RWMutex（锁粒度在工具批次级）。

### Phase 4: 多平台接入

飞书 WebSocket 长连接（无需公网 IP）、企业微信（Group Bot Webhook + Custom App 回调）。Reporter 接口统一抽象，引擎不感知底层平台。

### Phase 5: 上下文工程

三段式 System Prompt（极简内核 + AGENTS.md + 技能元数据）、渐进式技能暴露、Session 会话管理、双维度滑动窗口截断、5 级自适应压缩。

### Phase 6: 工程化

Claude Code 配置集成、YAML 配置文件、.gitignore 密钥安全、Docker 多阶段构建、JSONL 会话持久化、审批流集成、API 限流器、跨平台 bash、配置校验。

---

## 从零复现指南

按以下顺序实现：

1. **数据层** — `schema/message.go`：Message、ToolCall、ToolResult
2. **Provider 接口** — `provider/interface.go`：LLMProvider、StreamEvent
3. **Provider 实现** — `claude.go` + `openai.go`（先 blocking，再 streaming）
4. **工具层** — `tools/registry.go`：BaseTool 接口 + Registry
5. **引擎** — `engine/loop.go`：ReAct 循环 + 并行执行
6. **四大工具** — read_file → write_file → bash → edit_file
7. **并发控制** — 读写分离 → 信号量 → RWMutex
8. **平台接入** — Terminal → 飞书 → 企业微信
9. **上下文工程** — PromptComposer → 技能 → Session → 压缩
10. **工程化** — Claude Code 集成 → YAML 配置 → Docker
