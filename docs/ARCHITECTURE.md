# 架构设计文档

> go-tiny-claw 的详细架构设计、核心机制和技术决策。

## 目录

- [系统架构](#系统架构)
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

---

## 系统架构

```
┌──────────────────────────────────────────────────────┐
│                 Platform Layer                        │
│         Terminal CLI · Feishu Bot · WeChat Bot        │
├──────────────────────────────────────────────────────┤
│                 Engine Layer                          │
│      ReAct Loop · Session (JSONL) · Subagent          │
├──────────────────────────────────────────────────────┤
│                Context Layer                          │
│   PromptComposer · Compactor (5-level) · Recovery     │
├──────────────────────────────────────────────────────┤
│                 Tools Layer                           │
│  read/write/edit_file · bash · search_files           │
│  fetch_url · spawn_subagent · read_skill              │
├──────────────────────────────────────────────────────┤
│              Permissions Layer                        │
│     COW Engine · Regex Hot Reload · Approval          │
├──────────────────────────────────────────────────────┤
│             Observability Layer                       │
│        CostTracker · Distributed Tracing              │
├──────────────────────────────────────────────────────┤
│               Provider Layer                          │
│       Anthropic Claude · OpenAI Compatible             │
└──────────────────────────────────────────────────────┘
```

依赖方向严格自上而下，无循环引用。`tools` 包通过 `AgentRunner` 接口打破对 `engine` 包的依赖（依赖倒置）。

---

## ReAct 引擎

每轮执行流程：

```
用户输入
  │
  ▼
上下文组装（System Prompt + Working Memory）
  │
  ▼
子代理通知注入 → Plan 模式提醒 → 自适应压缩
  │
  ▼
Phase 1: Thinking（可选，不带工具，强制慢思考）
  │
  ▼
Phase 2: Action（带全部工具定义）
  │
  ├── 无 ToolCall → 循环终止，等待下一条用户输入
  │
  └── 有 ToolCall → 工具执行（只读并行 / 写入串行）
        │
        ▼
      错误恢复（RecoveryManager 注入救援指南）
        │
        ▼
      死循环检测（MD5 指纹，3 次相同失败 → 强纠正）
        │
        ▼
      回到 Phase 1
```

**关键设计决策**：
- Thinking 和 Action 均使用流式输出（`streamGenerate`），通过 `Reporter.OnStreamDelta` 逐字推送
- 写入 Session 的永远是全量真实响应，Compact 只作用于发给模型的临时 Context

---

## 并发模型

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
| RWMutex | 工作区读写锁 | 跨 Run() 的文件系统一致性 |

**核心洞察**：ReAct 循环同一 Turn 内，模型决策已在 LLM 生成阶段"凝固"，不存在 TOCTOU。只读批次安全并发，含写批次退化为顺序执行。

---

## 会话管理

每个用户/聊天拥有独立的 Session，通过 `GlobalSessionMgr`（`sync.RWMutex` + Map）管理。

### 持久化

```
Session.Append(msgs)
  │
  ├── 内存：追加到 s.history
  │
  └── 磁盘：JSONL 追加到 .claw/sessions/{id}.jsonl

SessionManager.GetOrCreate(id)
  │
  ├── 内存命中 → 直接返回
  │
  └── 内存未命中 → 从磁盘 LoadFromDisk() 恢复
```

### 双维度滑动窗口截断

`GetWorkingMemory(limit=6, maxChars=50000)` 从尾部向前遍历，两个维度先到先停：

```
任一条件触发即停止
  │
  ▼
孤儿 ToolResult 清理（移除没有 ToolCall 对应的 ToolResult，防 API 400）
  │
  ▼
返回截断后的消息切片
```

---

## 自适应上下文压缩

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

## 编辑工具的 4 级匹配策略

`edit_file` 在精确匹配失败时依次降级：

```
L1: 精确匹配          — 原始字符串完全一致
L2: 换行标准化        — \r\n → \n 后匹配
L3: TrimSpace 匹配    — 去除首尾空白后匹配
L4: 滑动窗口匹配      — 逐行去缩进 + 智能缩进锚定
```

每一级都做唯一性校验：匹配到多处时拒绝执行，要求提供更多上下文。

---

## 动态权限引擎

基于 **Copy-on-Write** 架构：

```
Check(ctx, command)
  │
  ├── atomic.Load(configSnapshot)  ← 无锁读取
  │
  ├── 遍历预排序规则（按 Priority 降序）
  │   └── regex.MatchString(command) → 返回 PermissionResult
  │
  └── 无匹配 → 使用 DefaultAction
```

**热重载**：后台 goroutine 轮询文件 modtime，检测到变更后重新编译正则、创建新快照、原子替换指针。读操作零开销。

**审批集成**：`ask` 策略通过 `ApprovalHandler` 接口接入审批流：
- CLI 模式：`ConsoleApprovalHandler`（终端 stdin 交互）
- 飞书模式：`FeishuApprovalHandler`（阻塞等待飞书审批卡片）

---

## 子代理系统

```
spawn_subagent(task_prompt)
  │
  ▼
SubagentManager.Spawn(parentCtx, runner, prompt, readOnlyRegistry)
  │
  ├── 创建可取消的子 context（parentCtx 取消 → 子代理自动取消）
  │
  └── goroutine: runner.RunSub(subCtx, prompt, readOnlyRegistry)
        │
        ├── 最多 10 轮
        │
        └── 完成后更新 task.Done = true

主循环每轮自动检查：
  injectSubagentNotifications()
    └── 遍历所有 Done 且 !Notified 的子代理 → 注入 User 消息
```

**只读隔离**：子代理的 `readOnlyRegistry` 仅包含 `read_file`、`bash`（带权限引擎）、`read_skill`。

---

## 流式输出架构

```
Provider (goroutine)
  │
  SDK SSE Stream → type-switch → chan StreamEvent (cap=16)
  │
  └── defer close(ch)

Engine (main goroutine)
  │
  consumeStream(ctx, ch, reporter, isThinking)
  │
  ├── TextDelta      → reporter.OnStreamDelta(delta, false) + acc.Ingest
  ├── ThinkingDelta  → reporter.OnStreamDelta(delta, true)  + acc.Ingest
  ├── ToolCallBegin  → acc.Ingest（建立 index → {ID, Name} 映射）
  ├── ToolCallArgsDelta → acc.Ingest（拼接 JSON 片段）
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

## 可观测性

### 费用追踪

`CostTracker` 以装饰器模式包装 `LLMProvider`：

```
CostTracker.Generate(ctx, msgs, tools)
  │
  ├── start = time.Now()
  ├── resp = underlying.Generate(ctx, msgs, tools)
  ├── 记录 Input/Output Tokens
  ├── 按模型定价查表计算费用
  └── 累加到 billingSession
```

### 分布式追踪

基于 Span 层级的追踪系统：

```
RootSpan (Agent.Run)
  ├── Turn-1
  │     ├── LLM.Thinking
  │     ├── LLM.Action
  │     └── Tool.Execute (read_file)
  ├── Turn-2
  │     ├── LLM.Action
  │     └── Tool.Execute (bash)
  └── ...
```

导出为 JSON 到 `.claw/traces/` 目录。

---

## 技能系统

**渐进式暴露**（Progressive Disclosure）：

```
System Prompt 中仅注入：
  - 技能名称 (name)
  - 技能描述 (description)

LLM 需要使用时：
  - 调用 read_skill(name="xxx") → 返回完整 SKILL.md 内容
```

50 个技能 × 元数据 20 Token = 1,000 Token（vs 急切加载的 100,000 Token）。

---

## 配置优先级

```
环境变量 > config.yaml > 默认值
```

支持 `~/.claude/settings.json` 自动注入环境变量（与 Claude Code 配置系统集成）。
