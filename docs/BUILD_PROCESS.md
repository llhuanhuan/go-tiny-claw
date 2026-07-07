# 构建流程

> go-tiny-claw 从零到一的构建过程，按功能迭代的 6 个 Phase 组织。
> 每个 Phase 对应一组 git commit，记录关键设计决策。

## 构建路线图

```
Phase 1: 引擎骨架
  引擎初始化 → 真实 LLM Provider → 流式 SSE → 工具路由

Phase 2: 四大原语
  ReadFile → WriteFile + Bash → 后台进程管理 → EditFile (4 级匹配)

Phase 3: 并发安全
  读写分离策略 → 信号量限流 → 工作区 RWMutex

Phase 4: 多平台接入
  飞书 (WebSocket) + 企业微信 (Webhook) + CLI | Reporter 统一抽象

Phase 5: 上下文工程
  PromptComposer → 渐进式技能系统 → Session 持久化 → 双维度滑动窗口

Phase 6: 工程化
  Claude Code 集成 → YAML 配置 → 密钥安全 → Docker
```

---

## Phase 1: 引擎骨架

> 目标：搭建最小可运行的 Agent 循环。

### ReAct 两阶段设计

```
for {
  Phase 1: Thinking
    provider.Generate(ctx, history, nil)  // 不传工具，强制纯推理
    → 输出持久化到 Session，供 Action 参考

  Phase 2: Action
    provider.Generate(ctx, history, tools)
    → 无 ToolCall → break（任务完成）
    → 有 ToolCall → executeToolsInParallel()
}
```

### 并行执行的有序性保障

- 预分配 `slots []slot`，每个 goroutine 通过值传递捕获索引 `i`
- 各 goroutine 写入 `slots[i]`（不同内存地址，无数据竞争）
- `wg.Wait()` 作为 happens-before 屏障

### 流式架构

```
SDK SSE Stream → goroutine → chan StreamEvent (cap=16) → Engine
```

用 Channel 桥接 SDK 的 push 式 SSE 与 Engine 的 pull 式消费。Channel 天然支持背压，Engine 可用 `select` 同时监听 `ctx.Done()` 和事件流。

---

## Phase 2: 四大图灵完备原语

> 目标：read_file / write_file / edit_file / bash —— 让 Agent 具备完整文件系统操作能力。

### EditFile 的 4 级降级匹配

LLM 生成的代码片段经常带不一致缩进，精确匹配极易失败：

```
L1: 精确匹配          — 原始字符串完全一致
L2: 换行标准化        — \r\n → \n 后匹配
L3: TrimSpace 匹配    — 去除首尾空白后匹配
L4: 滑动窗口匹配      — 逐行去缩进 + 智能缩进锚定
```

每一级都做唯一性校验：匹配到多处时拒绝执行。

### 后台进程管理（OS PCB 模型）

```
TaskManager 方法        对应 OS 概念
─────────────────────────────────────
Spawn(cmd)              fork() + exec()
Get(taskID)             read /proc/[pid]/status + stdout
Kill(taskID)            kill(SIGTERM) + waitpid()
List()                  ps aux
Shutdown()              kill(-1) — 终止所有子进程
```

RingBuffer（64KB kfifo）：固定容量，写满覆盖最旧数据，防 OOM。

---

## Phase 3: 并发安全

> 目标：在保证正确性的前提下最大化并发度。

### 三级并发控制

| 层级 | 机制 | 作用 |
|------|------|------|
| WaitGroup | 屏障同步 | 等待所有 goroutine 完成 |
| Semaphore | Buffered Channel (cap=5) | 限制并发上限 |
| RWMutex | 工作区读写锁 | 跨 Run() 文件系统一致性 |

**核心洞察**：ReAct 同一 Turn 内模型决策已凝固，不存在 TOCTOU。只读批次安全并发，含写批次退化为顺序执行。

---

## Phase 4: 多平台接入

> 目标：终端 / 飞书 / 企业微信三种运行模式。

Reporter 接口统一抽象，引擎完全不感知底层平台：

```go
type Reporter interface {
    OnThinking(ctx context.Context)
    OnStreamDelta(ctx context.Context, delta string, isThinking bool)
    OnToolCall(ctx context.Context, toolName string, args string)
    OnToolResult(ctx context.Context, toolName string, result string, isError bool)
    OnMessage(ctx context.Context, content string)
}
```

---

## Phase 5: 上下文工程

> 目标：在有限上下文窗口中发挥最大效能。

### 三段式 System Prompt

```
┌─────────────────────────────────────┐
│ 1. 极简内核：身份 + 红线纪律        │
├─────────────────────────────────────┤
│ 2. AGENTS.md：项目专属规范（外部化） │
├─────────────────────────────────────┤
│ 3. 技能元数据索引（仅 name + desc）  │
└─────────────────────────────────────┘
```

### 渐进式技能暴露

```
Eager Loading:    50 技能 × 2000 Token = 100,000 Token
Progressive:      50 技能 × 20 Token   = 1,000 Token + 按需加载 1-3 个
                  → 节省 90%+ 上下文窗口
```

### 双维度滑动窗口

从尾部向前遍历，消息条数（6 条）和字符预算（50K）先到先停。截断后自动清理孤儿 ToolResult。

---

## Phase 6: 工程化

> 配置外部化、密钥安全、容器化部署。

- `~/.claude/settings.json` 自动注入环境变量（与 Claude Code 配置集成）
- YAML 配置 + 环境变量覆盖（`env > yaml > 默认值`）
- `.gitignore` 排除 `config.yaml`（含密钥）
- `config.yaml.example` 脱敏模板
- `Dockerfile` 多阶段构建（alpine 运行时）

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
