# go-tiny-claw 构建流程文档

> 本文档记录了 go-tiny-claw 从零到一的完整构建过程，按功能迭代的时间线组织，每一步都对应一次 git commit。通过本文档，你可以理解每个模块的设计动机、关键技术决策以及它们如何组合成最终的 Agent 引擎。

---

## 目录

- [Phase 1: 引擎骨架](#phase-1-引擎骨架)
  - [Step 1: 初始化引擎 + 并行工具执行 + Thinking 阶段](#step-1-初始化引擎--并行工具执行--thinking-阶段)
  - [Step 2: 接入真实 LLM Provider](#step-2-接入真实-llm-provider)
  - [Step 3: 流式输出 (SSE)](#step-3-流式输出-sse)
  - [Step 4: 真实工具注册表 + 动态路由](#step-4-真实工具注册表--动态路由)
- [Phase 2: 四大图灵完备原语](#phase-2-四大图灵完备原语)
  - [Step 5: ReadFile 工具](#step-5-readfile-工具)
  - [Step 6: WriteFile + Bash 工具](#step-6-writefile--bash-工具)
  - [Step 7: 后台进程管理体系](#step-7-后台进程管理体系)
  - [Step 8: EditFile 工具 + 4 级模糊匹配](#step-8-editfile-工具--4-级模糊匹配)
  - [Step 9-10: 缩进锚定优化](#step-9-10-缩进锚定优化)
- [Phase 3: 并发与安全](#phase-3-并发与安全)
  - [Step 11: 读写分离并发策略](#step-11-读写分离并发策略)
  - [Step 12: 并发上限控制（信号量）](#step-12-并发上限控制信号量)
  - [Step 13: 工作区读写锁](#step-13-工作区读写锁)
- [Phase 4: 多平台接入](#phase-4-多平台接入)
  - [Step 14: 飞书 + 企业微信机器人](#step-14-飞书--企业微信机器人)
- [Phase 5: 上下文工程](#phase-5-上下文工程)
  - [Step 15: 动态 Prompt 编译架构](#step-15-动态-prompt-编译架构)
  - [Step 16: 渐进式技能系统](#step-16-渐进式技能系统)
  - [Step 17: Session 会话管理 + 集成测试](#step-17-session-会话管理--集成测试)
  - [Step 18: 双维度滑动窗口截断](#step-18-双维度滑动窗口截断)
- [Phase 6: 配置与工程化](#phase-6-配置与工程化)
  - [Step 19: Claude Code 配置系统集成](#step-19-claude-code-配置系统集成)
  - [Step 20: YAML 配置文件化](#step-20-yaml-配置文件化)
  - [Step 21: .gitignore 密钥安全](#step-21-gitignore-密钥安全)

---

## Phase 1: 引擎骨架

> 目标：搭建最小可运行的 Agent 循环，能调用 LLM 并执行工具。

### Step 1: 初始化引擎 + 并行工具执行 + Thinking 阶段

**Commit**: `ad3c5ca` — `feat: init go-tiny-claw engine with parallel tool execution and thinking phase`

**做了什么**：
- 创建 Go module `github.com/lhuan/go-tiny-claw`
- 定义核心数据类型：`Message`、`ToolCall`、`ToolResult`、`ToolDefinition`
- 实现 `AgentEngine`，包含完整的 ReAct 循环
- 实现 `LLMProvider` 接口（抽象层，此时为 mock）
- 实现 `Registry` 接口 + `BaseTool` 接口
- 工具并行执行：使用 `sync.WaitGroup` + 预分配 slots 保证有序性
- Thinking 阶段：在 Action 之前，先用无工具的 prompt 让模型进行推理规划

**关键设计决策**：

```
ReAct 循环的两阶段设计：

  ┌──────────────────────────────────────────────────┐
  │  for {                                           │
  │    Phase 1: Thinking (可选)                       │
  │    ─────────────────────                          │
  │    provider.Generate(ctx, history, nil)           │
  │    → 不传工具定义，强制模型进入纯推理模式          │
  │    → 输出持久化到 Session，供 Action 阶段参考      │
  │                                                  │
  │    Phase 2: Action                               │
  │    ────────────────                               │
  │    provider.Generate(ctx, history, tools)         │
  │    → 传入工具定义，模型可选择调用工具或直接回复     │
  │    → 无 ToolCall → break（任务完成）               │
  │    → 有 ToolCall → executeToolsInParallel()       │
  │  }                                               │
  └──────────────────────────────────────────────────┘
```

**并行执行的有序性保障**：
- 预分配 `slots []slot` 切片，每个 goroutine 通过值传递捕获索引 `i`，写入 `slots[i]`
- 各 goroutine 操作不同内存地址，无数据竞争
- `wg.Wait()` 作为 happens-before 屏障，保证主 goroutine 读取时数据已就绪

---

### Step 2: 接入真实 LLM Provider

**Commit**: `015d390` — `feat: integrate real LLM providers via Claude/OpenAI SDK protocols`

**做了什么**：
- 实现 `AnthropicProvider`：接入 Anthropic Claude SDK，支持 blocking 模式
- 实现 `OpenAIProvider`：接入 OpenAI 兼容协议（DeepSeek、智谱 GLM）
- 引入环境变量配置：`ANTHROPIC_API_KEY`、`DEEPSEEK_API_KEY`、`ZHIPU_API_KEY`

**技术细节**：
- Anthropic SDK 使用 `anthropic-sdk-go`，通过 `anthropic.NewOption().WithAPIKey()` 创建客户端
- OpenAI 兼容使用 `openai-go/v3`，通过 `openai.NewClient()` 创建客户端
- 两个 Provider 均实现 `LLMProvider` 接口的 `Generate` 方法（阻塞式）

---

### Step 3: 流式输出 (SSE)

**Commit**: `5b7a92b` — `feat: add streaming (SSE) support to LLMProvider with channel-based event bridge`

**做了什么**：
- 在 `LLMProvider` 接口中新增 `StreamGenerate` 方法
- 定义 `StreamEvent` 类型体系：`ThinkingDelta`、`TextDelta`、`ToolCallBegin`、`ToolCallArgsDelta`、`Done`、`Error`
- 实现 `StreamAccumulator`：将流式事件片段组装为完整的 `schema.Message`
- 用 Go Channel 桥接 SDK 的 push 式 SSE 流与 Engine 的 pull 式消费

**核心架构**：

```
SDK SSE Stream ──▶ goroutine (生产者) ──▶ chan StreamEvent (cap=16) ──▶ Engine (消费者)

事件类型：
  ThinkingDelta   → 模型思考过程的增量文本
  TextDelta       → 模型回复的增量文本
  ToolCallBegin   → 工具调用开始（携带 ID、Name、Index）
  ToolCallArgsDelta → 工具调用参数的增量 JSON 片段
  Done            → 流结束信号
  Error           → 流中错误
```

**为什么用 Channel 而不是回调**：
- Channel 天然支持背压（cap=16 缓冲区）
- Engine 可以用 `select` 同时监听 `ctx.Done()` 和事件流
- 生产者和消费者解耦，各自在独立 goroutine 中运行

---

### Step 4: 真实工具注册表 + 动态路由

**Commit**: `44eafd8` — `feat: implement real tool registry with dynamic tool routing`

**做了什么**：
- 将 `Registry` 从 mock 实现为真实的 `registryImpl`
- 使用 `map[string]BaseTool` 实现 O(1) 工具路由
- 工具执行结果自动封装为 `ToolResult`，错误信息反馈给模型自愈

---

## Phase 2: 四大图灵完备原语

> 目标：实现 read_file、write_file、edit_file、bash 四个核心工具，让 Agent 具备完整的文件系统操作和命令执行能力。

### Step 5: ReadFile 工具

**Commit**: `c076ec3` — `feat: add ReadFile tool implementation and hello.txt test fixture`

**做了什么**：
- 实现 `ReadFileTool`：读取工作区内指定路径的文件内容
- 8KB 截断保护：防止巨型文件撑爆 LLM 上下文
- 创建 `hello.txt` 测试夹具

**设计要点**：
- 文件路径相对于 `WorkDir` 解析，防止路径穿越
- 超过 8KB 的内容自动截断并附加截断提示

---

### Step 6: WriteFile + Bash 工具

**Commit**: `817e199` — `feat: add WriteFile and Bash tools — complete the four Turing-complete primitives`

**做了什么**：
- 实现 `WriteFileTool`：创建/覆盖文件，自动创建父目录
- 实现 `BashTool`：执行 Shell 命令，30 秒超时保护
- 四大原语全部就位：read_file、write_file、edit_file（占位）、bash

**WriteFile 安全设计**：
- 自动 `os.MkdirAll` 创建缺失的父目录
- 写入路径相对于 `WorkDir` 解析

**Bash 安全设计**：
- 30 秒超时，防止无限循环命令（如 `while true; do ...`）
- stdout + stderr 合并捕获，完整返回给模型

---

### Step 7: 后台进程管理体系

**Commit**: `1791f21` — `feat: 引入后台进程管理体系 — Bash工具支持 run_in_background 守护模式`

**做了什么**：
- 实现 `TaskManager`：OS PCB 模型的进程生命周期管理器
- Bash 工具新增 `run_in_background` 参数，支持异步守护模式
- 实现 `RingBuffer`（kfifo 风格）：64KB 环形缓冲区捕获 stdout/stderr
- 实现 `TaskOutputTool`：查询后台任务状态和日志
- 实现 `TaskStopTool`：终止后台任务或列出所有任务

**OS 类比**：

```
TaskManager 方法        对应 OS 概念
─────────────────────────────────────
Spawn(cmd)              fork() + exec()
Get(taskID)             read /proc/[pid]/status + stdout
Kill(taskID)            kill(SIGTERM) + waitpid()
List()                  ps aux
Shutdown()              kill(-1) — 终止所有子进程
```

**RingBuffer 设计**：
- 固定 64KB 容量，写满后自动覆盖最旧数据
- 线程安全：通过 `sync.Mutex` 保护读写
- 目的：防止长时间运行的后台进程（如 `npm run dev`）的无限输出导致 OOM

---

### Step 8: EditFile 工具 + 4 级模糊匹配

**Commit**: `8b89536` — `feat: add EditFile tool with 4-level fuzzy matching + indentation trap in main.go`

**做了什么**：
- 实现 `EditFileTool`：基于字符串查找替换的文件编辑工具
- 4 级降级匹配策略：
  - **L1 精确匹配**：原始字符串完全一致
  - **L2 换行标准化**：`\r\n` → `\n` 后匹配
  - **L3 TrimSpace 匹配**：去除首尾空白后匹配
  - **L4 滑动窗口匹配**：逐行去缩进 + 智能缩进锚定

**为什么需要 4 级匹配**：
LLM 生成的代码片段经常带有不一致的缩进（尤其是从流式输出中拼接时），精确匹配极易失败。4 级降级策略确保在各种边缘情况下都能可靠地定位到目标替换位置。

---

### Step 9-10: 缩进锚定优化

**Commit**: `c8e3822` + `b487879` — `test: indentation trap verified` + `feat: add smart indentation anchoring`

**做了什么**：
- 优化 L4 滑动窗口匹配的缩进处理逻辑
- 引入**智能缩进锚定**：根据目标文件中匹配位置的实际缩进，自动调整替换文本的缩进
- 通过测试验证缩进陷阱场景

---

## Phase 3: 并发与安全

> 目标：在保证正确性的前提下，最大化工具执行的并发度。

### Step 11: 读写分离并发策略

**Commit**: `34b5b9b` — `feat: add read-concurrent/write-serial concurrency strategy in executeToolsInParallel`

**做了什么**：
- 将工具分为**只读工具**和**写入工具**两类
- 只读批次（read_file、TaskOutput）：并发执行
- 含写批次（write_file、edit_file、bash）：退化为顺序执行

**核心洞察**：

```
ReAct 循环同一 Turn 内，模型决策已在 LLM 生成阶段"凝固"，
不存在"读取→模型决策→写入"的 TOCTOU 模式。

数据竞争发生在纯 I/O 层：
  - 纯读批次：所有操作无副作用，安全并发，收益最大
  - 含写批次：退化为顺序执行，从根本上消除写丢失和脏读
```

---

### Step 12: 并发上限控制（信号量）

**Commit**: `34b5b9b` + `65f1e67` — `feat: 引入并发上限控制（Buffered Channel 信号量）`

**做了什么**：
- 使用 Buffered Channel 作为计数信号量（Counting Semaphore）
- 并发上限设为 5（`maxConcurrent = 5`）
- 信号量与 WaitGroup 正交组合：Semaphore 控制"同时最多几个在跑"，WaitGroup 管"是否全部完成"

**为什么需要上限**：

```
本地 read_file 瞬间读 5 个不成问题，但如果挂载了
fetch_web_url / query_jira_api 等网络工具，一次性 50 个并发请求
会触发目标站防火墙或 API Rate Limit 封杀。
```

---

### Step 13: 工作区读写锁

**Commit**: `259f628` — `feat: 引入工作区读写锁（Workspace RWMutex）到 AgentEngine`

**做了什么**：
- 在 `AgentEngine` 中引入 `wsRWMu sync.RWMutex`
- 只读批次持有 `RLock`，多个 `Run()` 可并发读
- 含写批次持有 `Lock`，独占工作区
- 锁粒度控制在工具批次级而非整个 `Run()` 生命周期

**锁粒度设计**：

```
Run() 生命周期：
  [LLM 推理] → [工具批次执行] → [LLM 推理] → [工具批次执行] → ...

  LLM 推理阶段：不持有任何锁 → 其他 Run() 可自由读写
  工具批次执行：按需获取 RLock 或 Lock

这避免了 Agent 在纯 LLM 推理阶段白白阻塞其他 Agent 的读操作。
```

---

## Phase 4: 多平台接入

> 目标：让 Agent 引擎能够通过飞书和企业微信对外提供服务。

### Step 14: 飞书 + 企业微信机器人

**Commit**: `1f4311e` — `feat: 多平台机器人接入 + 飞书 bot_p2p_chat_entered 事件修复`

**做了什么**：
- 实现 `FeishuBot`：通过飞书 SDK 的 WebSocket 长连接模式接入，无需公网 IP
- 实现 `WeChatBot`：通过企业微信 HTTP Webhook 接入，支持 URL 验证和消息回调
- 实现 `FeishuReporter` 和 `WechatReporter`：将引擎事件转换为平台消息
- 实现 `TerminalReporter`：CLI 终端输出
- 飞书 `bot_p2p_chat_entered` 事件处理修复

**Reporter 接口设计**：

```go
type Reporter interface {
    OnThinking(ctx context.Context)                          // Thinking 阶段开始
    OnMessage(ctx context.Context, content string)           // 模型回复文本
    OnToolCall(ctx context.Context, name, args string)       // 即将执行工具
    OnToolResult(ctx context.Context, name, output string, isError bool) // 工具执行结果
}
```

三种 Reporter 实现同一接口，引擎完全不感知底层平台。

---

## Phase 5: 上下文工程

> 目标：精巧地管理 System Prompt、会话历史和技能系统，让 Agent 在有限上下文窗口中发挥最大效能。

### Step 15: 动态 Prompt 编译架构

**Commit**: `c79c680` — `feat: 实现动态 Prompt 编译架构 — PromptComposer + AGENTS.md 外部化 + SKILL.md 技能系统`

**做了什么**：
- 实现 `PromptComposer`：三段式 System Prompt 构建器
- 加载 `AGENTS.md`：项目专属规范外部化
- 实现 `SkillLoader`：扫描 `.claw/skills/*/SKILL.md`，解析 YAML frontmatter

**三段式 System Prompt 结构**：

```
┌─────────────────────────────────────────────┐
│ 1. 极简内核 (Minimal Core)                  │
│    - 身份定义                                │
│    - 核心纪律（红线规则）                    │
├─────────────────────────────────────────────┤
│ 2. 外部化状态 (AGENTS.md)                   │
│    - 项目专属规范                            │
│    - 从工作区根目录动态加载                  │
├─────────────────────────────────────────────┤
│ 3. 技能元数据索引 (Skills Metadata)          │
│    - 仅注入 name + description              │
│    - 正文不进入 System Prompt                │
│    - 通过 read_skill 工具按需加载            │
└─────────────────────────────────────────────┘
```

**为什么分离 AGENTS.md**：
将易变的业务规范剥离出核心引擎，不同项目可以有不同的 AGENTS.md，引擎代码无需修改。

---

### Step 16: 渐进式技能系统

**Commit**: `410de20` — `feat: 技能系统重构 — 从 Eager Loading 到 Progressive Disclosure`

**做了什么**：
- 将技能系统从**急切加载**（Eager Loading）重构为**渐进式暴露**（Progressive Disclosure）
- System Prompt 中仅注入技能元数据（name + description），正文不注入
- 实现 `ReadSkillTool`：当 LLM 需要使用技能时，通过此工具按需加载完整内容

**渐进式暴露的优势**：

```
Eager Loading（旧方案）：
  50 个技能 × 每个 2000 Token = 100,000 Token
  → 开局就吃掉大量上下文窗口

Progressive Disclosure（新方案）：
  50 个技能 × 每个元数据 20 Token = 1,000 Token
  + 按需加载实际使用的 1-3 个技能 = 2,000-6,000 Token
  → 节省 90%+ 的上下文窗口
```

---

### Step 17: Session 会话管理 + 集成测试

**Commit**: `780b7ff` — `feat: Session 会话管理 + 集成测试体系 — 引擎 Session 化改造与端到端验证`

**做了什么**：
- 实现 `Session` 结构体：消息历史存储、工作区目录绑定
- 实现 `GlobalSessionMgr`：并发安全的 Session 管理器（`sync.Map`）
- 实现 `GetWorkingMemory`：从 Session 历史中截取最近的对话上下文
- 编写 15 个单元测试 + 10 个集成测试

**Session 设计**：

```
GlobalSessionMgr
  ├── "terminal" → Session{ID, WorkDir, Messages}
  ├── "feishu:user123" → Session{ID, WorkDir, Messages}
  └── "wechat:chat456" → Session{ID, WorkDir, Messages}

每个用户/聊天拥有独立的 Session，互不干扰。
```

**集成测试覆盖的 10 个场景**：
1. 简单问答
2. 文件读取（read_file 工具调用）
3. 文件写入（write_file 工具调用）
4. Bash 命令执行
5. 记忆截断（密钥遗忘验证）
6. 多轮对话连贯性
7. 并发平台隔离
8. 文件创建
9. 错误恢复/自愈
10. 长上下文压力测试

---

### Step 18: 双维度滑动窗口截断

**Commit**: `aef59fd` — `refactor(session): GetWorkingMemory 双维度滑动窗口截断 — 消息条数 + 字符预算`

**做了什么**：
- `GetWorkingMemory` 从单维度（仅消息条数）升级为双维度截断
- 维度一：消息条数上限（默认 6 条）
- 维度二：字符预算上限（默认 50,000 字符）
- 孤儿 ToolResult 清理：截断后自动检测并移除没有对应 ToolCall 的 ToolResult

**双维度截断逻辑**：

```
输入：完整的 Session 消息历史
                  │
                  ▼
        ┌─────────────────┐
        │ 从尾部向前遍历   │
        │ 同时检查两个维度 │
        └────────┬────────┘
                 │
     ┌───────────┴───────────┐
     │                       │
  消息条数 > 6 ?        字符数 > 50000 ?
     │                       │
     └───────────┬───────────┘
                 │
         任一条件触发即停止
                 │
                 ▼
        ┌─────────────────┐
        │ 孤儿清理阶段     │
        │ 移除没有 ToolCall│
        │ 对应的 ToolResult│
        └─────────────────┘
                 │
                 ▼
          返回截断后的消息切片
```

**为什么要清理孤儿 ToolResult**：
如果截断恰好切掉了一个 ToolCall 消息但保留了其 ToolResult，API 会返回 400 错误（ToolResult 没有对应的 ToolCall）。孤儿清理确保消息链的完整性。

---

## Phase 6: 配置与工程化

> 目标：将硬编码配置外部化，提升工程化水平。

### Step 19: Claude Code 配置系统集成

**Commit**: `e19dced` — `refactor(provider): 接入 Claude Code 配置系统，统一 Anthropic Provider`

**做了什么**：
- 引擎启动时自动读取 `~/.claude/settings.json`
- 将其中的 `env` 字段注入到当前进程环境变量（不覆盖已存在的变量）
- 无需手动设置 `ANTHROPIC_API_KEY` 等环境变量，完全复用 Claude Code 已配置的模型和密钥

---

### Step 20: YAML 配置文件化

**Commit**: `fa5c085` — `refactor(config): 配置文件化改造 — 从环境变量迁移到 YAML 配置文件`

**做了什么**：
- 引入 `config.yaml` 作为主配置文件
- 实现 `LoadConfig()`：YAML 解析 + 环境变量覆盖
- 配置结构：`server`（端口、模式）、`feishu`（AppID、AppSecret）、`wechat`（WebhookURL、Token、EncodingAESKey）
- 配置优先级：环境变量 > config.yaml > 默认值

**配置文件结构**：

```yaml
server:
  port: 48080          # HTTP 服务器端口（企业微信模式使用）
  mode: debug          # debug | release

feishu:
  app_id: ""           # 设置后自动启用飞书模式
  app_secret: ""

wechat:
  webhook_url: ""      # 设置后自动启用企业微信模式
  token: ""
  encoding_aes_key: ""
```

---

### Step 21: .gitignore 密钥安全

**Commit**: `1b151a9` — `chore(git): .gitignore 新增密钥文件忽略规则`

**做了什么**：
- 在 `.gitignore` 中添加密钥文件忽略规则
- 防止 `config.yaml`（可能包含 AppSecret）等敏感文件被意外提交

---

## 构建流程总结

```
Phase 1: 引擎骨架
  ┌─────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐
  │ 引擎初始化 │→│ 真实LLM  │→│ 流式SSE  │→│ 工具路由  │
  │ +ReAct   │   │ Provider │   │ Channel  │   │ Registry │
  └─────────┘   └──────────┘   └──────────┘   └──────────┘

Phase 2: 四大原语
  ┌─────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐
  │ ReadFile │→│Write+Bash│→│ 后台进程  │→│ EditFile │
  │ 8KB截断  │   │ 30s超时  │   │ PCB+Ring │   │ 4级匹配  │
  └─────────┘   └──────────┘   └──────────┘   └──────────┘

Phase 3: 并发安全
  ┌─────────┐   ┌──────────┐   ┌──────────┐
  │ 读写分离  │→│ 信号量=5 │→│ RWMutex  │
  │ 并发策略  │   │ 上限控制  │   │ 工作区锁  │
  └─────────┘   └──────────┘   └──────────┘

Phase 4: 多平台
  ┌─────────────────────────────┐
  │ 飞书(WebSocket) + 企微(Webhook) + CLI │
  │ Reporter 接口统一抽象                   │
  └─────────────────────────────┘

Phase 5: 上下文工程
  ┌─────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐
  │ Prompt   │→│ 渐进式   │→│ Session  │→│ 双维度   │
  │ Composer │   │ 技能系统  │   │ 会话管理  │   │ 滑动窗口  │
  └─────────┘   └──────────┘   └──────────┘   └──────────┘

Phase 6: 工程化
  ┌─────────┐   ┌──────────┐   ┌──────────┐
  │ Claude   │→│ YAML     │→│ .gitignore│
  │ Code集成  │   │ 配置文件  │   │ 密钥安全  │
  └─────────┘   └──────────┘   └──────────┘
```

---

## 附录：从零复现指南

如果你想从零开始复现这个项目，按照以下顺序进行：

1. **初始化 Go Module**
   ```bash
   mkdir go-tiny-claw && cd go-tiny-claw
   go mod init github.com/lhuan/go-tiny-claw
   ```

2. **定义数据层** (`internal/schema/message.go`)
   - `Role`、`Message`、`ToolCall`、`ToolResult`、`ToolDefinition`

3. **定义 Provider 接口** (`internal/provider/interface.go`)
   - `LLMProvider` 接口（`StreamGenerate` + `Generate`）
   - `StreamEvent` 类型体系
   - `StreamAccumulator` 组装器

4. **实现 Provider** (`internal/provider/claude.go`, `opaenai.go`)
   - 先实现 blocking 模式，再实现 streaming 模式

5. **定义工具层** (`internal/tools/registry.go`)
   - `BaseTool` 接口（`Name` + `Definition` + `Execute`）
   - `Registry` 接口 + `registryImpl`

6. **实现引擎** (`internal/engine/loop.go`)
   - ReAct 循环（Thinking + Action）
   - 并行工具执行（`executeToolsInParallel`）

7. **逐个实现工具**
   - `read_file` → `write_file` → `bash` → `edit_file`
   - 后台进程管理：`task_manager` → `ringbuf` → `task_output` → `task_stop`

8. **添加并发控制**
   - 读写分离 → 信号量限流 → 工作区 RWMutex

9. **接入平台**
   - `TerminalReporter` → `FeishuBot` → `WeChatBot`

10. **上下文工程**
    - `PromptComposer` → `SkillLoader` → `Session` → 滑动窗口截断

11. **配置工程化**
    - Claude Code 集成 → YAML 配置 → .gitignore 安全
