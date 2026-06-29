# go-tiny-claw

> 轻量级、自托管的 Go 语言 AI Agent 引擎 —— 用 OS 思维构建 AI 大脑

go-tiny-claw 是一个用 Go 语言编写的轻量级 AI Agent 引擎，实现了完整的 **ReAct（Reason + Act）循环**。它将大语言模型（LLM）连接到本地文件系统工具和 Shell 命令，让 AI 能够自主完成复杂的工程任务。

## 特性

- **ReAct 引擎** — Thinking（推理规划）+ Action（工具调用）双阶段循环，Agent 自主决策直至任务完成
- **流式输出** — 基于 Go Channel 的 SSE 流式架构，实时推送 Thinking / Text / ToolCall 事件
- **四层模糊匹配** — edit_file 工具实现 L1 精确 → L2 换行标准化 → L3 TrimSpace → L4 滑动窗口智能缩进锚定
- **后台进程管理** — OS PCB 模型，支持 Spawn / Get / Kill / List / Shutdown 全生命周期管理
- **并发控制** — 读写分离（读操作并行 + 写操作串行）、信号量限流、环形缓冲区（kfifo）捕获输出
- **会话管理** — 按用户/聊天隔离的 Session，双维度滑动窗口截断（消息条数 + 字符预算）
- **自适应上下文压缩** — 5 级压缩策略（无/轻柔/标准/激进/紧急），基于实际 Token 利用率动态调整
- **渐进式技能系统** — 类似 Claude Code 的 SKILL.md 技能定义，按需加载，元数据优先暴露
- **子代理系统** — spawn_subagent 异步派发只读子代理，自动通知注入，实现任务分解
- **Plan 模式** — 强制外部化状态管理（PLAN.md/TODO.md），断点续跑，严格步骤化执行
- **动态权限引擎** — Copy-on-Write 架构，正则规则热重载，支持 allow / ask / deny 三级策略
- **死循环检测** — MD5 指纹 + 参数规范化，连续 3 次相同失败自动注入强纠正指令
- **错误恢复** — RecoveryManager 基于错误码注入恢复提示，引导 Agent 自我修正
- **可观测性** — CostTracker 费用追踪（装饰器模式）+ 分布式追踪（Span 层级 + JSON 导出）
- **评估框架** — BenchmarkRunner 沙箱化测试执行，支持 setup / validate 脚本，输出通过率和费用报告
- **多平台接入** — 终端 CLI / 飞书（WebSocket 长连接）/ 企业微信（HTTP Webhook）三种运行模式
- **飞书审批** — 人工介入审批流（approve / reject 命令），超时自动取消
- **多模型支持** — Anthropic Claude、OpenAI 兼容协议（DeepSeek、智谱 GLM 等）

## 架构总览

```
┌─────────────────────────────────────────────────┐
│              Platform Layer (平台层)              │
│    Terminal CLI  │  Feishu Bot  │  WeChat Bot    │
├─────────────────────────────────────────────────┤
│              Engine Layer (引擎层)               │
│  ReAct Loop │ Session │ Reminder │ Subagent     │
├─────────────────────────────────────────────────┤
│             Context Layer (上下文层)             │
│  PromptComposer │ Compactor │ RecoveryManager   │
├─────────────────────────────────────────────────┤
│              Tools Layer (工具层)                │
│  read/write/edit_file │ bash │ spawn_subagent   │
│  read_skill │ TaskOutput │ TaskStop │ RingBuf   │
├─────────────────────────────────────────────────┤
│           Permissions Layer (权限层)             │
│  Dynamic Engine │ COW │ Hot Reload │ Regex      │
├─────────────────────────────────────────────────┤
│          Observability Layer (可观测层)          │
│  CostTracker │ Distributed Tracing │ Eval       │
├─────────────────────────────────────────────────┤
│            Provider Layer (模型层)               │
│    Anthropic Claude  │  OpenAI Compatible       │
├─────────────────────────────────────────────────┤
│             Schema Layer (数据层)                │
│    Message  │  ToolCall  │  ToolResult  │  ...   │
└─────────────────────────────────────────────────┘
```

## 快速开始

### 环境要求

- Go 1.25+
- Anthropic API Key（或其他支持的 LLM API Key）

### 安装

```bash
git clone https://github.com/lhuan/go-tiny-claw.git
cd go-tiny-claw
go build -o claw.exe ./cmd/claw
go build -o bench.exe ./cmd/bench
```

### 配置

复制示例配置文件：

```bash
cp config.yaml.example config.yaml
```

编辑 `config.yaml`：

```yaml
server:
  port: 48080
  mode: debug

model:
  name: glm-4.5-air          # 计费查询用的模型名
  max_context_window: 200000  # 上下文窗口大小
  plan_mode: false            # Plan 模式开关（PLAN.md/TODO.md）

feishu:
  app_id: ""        # 设置后自动启用飞书模式
  app_secret: ""

wechat:
  webhook_url: ""   # 设置后自动启用企业微信模式
  token: ""
  encoding_aes_key: ""
```

LLM API Key 通过环境变量配置：

```bash
# Anthropic Claude
export ANTHROPIC_API_KEY="sk-ant-..."
export ANTHROPIC_MODEL="claude-sonnet-4-20250514"  # 可选

# DeepSeek
export DEEPSEEK_API_KEY="sk-..."

# 智谱 GLM
export ZHIPU_API_KEY="..."
```

也可以通过 `~/.claude/settings.json` 自动注入环境变量（与 Claude Code 配置系统集成）。

### 运行

```bash
# 终端 CLI 模式（默认）
./claw.exe "请帮我分析当前目录的代码结构"

# 交互式
./claw.exe

# 指定工作目录 + 会话 ID（断点续跑）
./claw.exe -dir /path/to/project -session my-session-id

# 飞书模式 — 设置 config.yaml 中的 feishu.app_id 后直接运行
./claw.exe

# 企业微信模式 — 设置 config.yaml 中的 wechat.webhook_url 后直接运行
./claw.exe
```

运行模式由配置自动检测：
1. `feishu.app_id` 非空 → 飞书模式
2. `wechat.webhook_url` 非空 → 企业微信模式
3. 其他 → 终端 CLI 模式

## 项目结构

```
go-tiny-claw/
├── cmd/
│   ├── claw/
│   │   ├── main.go              # 入口：模式检测、引擎启动
│   │   └── config.go            # YAML 配置加载（环境变量覆盖）
│   └── bench/
│       └── main.go              # Benchmark 独立 CLI 入口
├── internal/
│   ├── schema/
│   │   └── message.go           # 核心数据类型：Message, ToolCall, ToolResult, TokenUsage
│   ├── provider/
│   │   ├── interface.go         # LLMProvider 接口 + GenerateBlocking
│   │   ├── claude.go            # Anthropic Claude 提供者（流式 + 阻塞）
│   │   ├── opaenai.go           # OpenAI 兼容提供者（DeepSeek、智谱）
│   │   ├── stream.go            # StreamEvent 类型定义（7 种事件）
│   │   └── accumulator.go       # StreamAccumulator：流式事件组装
│   ├── tools/
│   │   ├── registry.go          # BaseTool 接口 + Registry（工具路由分发 + 中间件链）
│   │   ├── read_file.go         # 文件读取（8KB 截断保护）
│   │   ├── write_file.go        # 文件写入（自动创建目录）
│   │   ├── edit_file.go         # 4 级模糊匹配替换引擎
│   │   ├── bash.go              # Shell 执行器（同步 30s 超时 + 异步后台）
│   │   ├── read_skill.go        # 渐进式技能加载器
│   │   ├── subagent.go          # spawn_subagent + check_subagent 工具
│   │   ├── subagent_manager.go  # 子代理生命周期管理器
│   │   ├── task_manager.go      # 后台进程生命周期管理器（OS PCB 模型）
│   │   ├── task_output.go       # 查询后台任务状态/日志
│   │   ├── task_stop.go         # 终止后台任务
│   │   ├── ringbuf.go           # 线程安全环形缓冲区（64KB kfifo）
│   │   ├── errors.go            # 标准化错误码（ToolError）
│   │   ├── middleware.go        # ExecutionTimer 中间件
│   │   └── *_test.go            # 单元测试
│   ├── engine/
│   │   ├── loop.go              # ReAct 核心循环 + RunSub 子代理循环
│   │   ├── session.go           # 会话管理 + 双维度滑动窗口 + GlobalSessionMgr
│   │   ├── reporter.go          # Reporter 接口（可插拔输出层）
│   │   ├── terminal_reporter.go # CLI 终端 Reporter 实现
│   │   ├── reminder.go          # 死循环检测（MD5 指纹 + 参数规范化）
│   │   └── *_test.go            # 单元/集成测试
│   ├── context/
│   │   ├── composer.go          # PromptComposer（3 段式 System Prompt）+ Compactor（5 级压缩）
│   │   ├── skill.go             # SKILL.md 解析器 + 渐进式加载
│   │   ├── session.go           # Billing Session（Token/费用追踪）
│   │   ├── recovery.go          # RecoveryManager（错误码恢复提示）
│   │   └── *_test.go            # 单元测试
│   ├── permissions/
│   │   ├── engine.go            # 动态权限引擎（COW + 热重载 + 正则规则）
│   │   ├── utils.go             # 文件读取/modtime 辅助
│   │   └── engine_test.go       # 单元测试
│   ├── observability/
│   │   ├── tracker.go           # CostTracker（装饰器模式，按 Session 计费）
│   │   ├── trace.go             # 分布式追踪（Span 层级 + JSON 导出）
│   │   └── tracker_test.go      # 单元测试
│   ├── feishu/
│   │   ├── bot.go               # 飞书 WebSocket 机器人 + FeishuReporter
│   │   ├── approval.go          # 飞书审批管理器（approve/reject + 超时自动取消）
│   │   └── *_test.go            # 单元测试
│   ├── wechat/
│   │   └── bot.go               # 企业微信 Webhook 机器人 + WechatReporter
│   ├── eval/
│   │   ├── benchmark.go         # BenchmarkRunner（沙箱化测试 + setup/validate 脚本）
│   │   └── benchmark_test.go    # 单元/集成测试
│   └── compression_test.go      # 3 层压缩防御综合测试
├── .claw/
│   ├── skills/                  # 技能定义目录
│   │   └── git-workflow/SKILL.md
│   ├── permissions.yaml         # 动态权限规则（17 条 deny/ask/allow）
│   └── traces/                  # 分布式追踪 JSON 导出目录
├── AGENTS.md                    # Agent 规则（注入 System Prompt）
├── config.yaml                  # 活跃配置
├── config.yaml.example          # 配置模板
└── go.mod
```

## 内置工具

| 工具 | 说明 |
|------|------|
| `read_file` | 读取文件内容，超过 8KB 自动截断 |
| `write_file` | 创建/覆盖文件，自动创建父目录 |
| `edit_file` | 模糊字符串替换，4 级降级匹配策略 |
| `bash` | 执行 Shell 命令，支持同步（30s 超时）和异步后台模式 |
| `read_skill` | 渐进式加载 SKILL.md 技能定义 |
| `TaskOutput` | 查询后台任务状态和输出日志 |
| `TaskStop` | 终止后台任务或列出所有任务 |
| `spawn_subagent` | 启动异步子代理（只读工具集），用于并行探索任务 |
| `check_subagent` | 轮询子代理完成状态，获取执行结果 |

## 核心机制

### ReAct 循环

每轮执行流程：

1. **上下文组装** — System Prompt（PromptComposer）+ Working Memory（6 条消息 / 50K 字符滑动窗口）
2. **子代理通知注入** — 检查已完成的子代理，将结果注入为 User 消息
3. **Plan 模式提醒** — 注入轻量级 Plan 模式提醒（不持久化到 Session）
4. **自适应压缩** — Compactor 根据实际 Token 利用率动态压缩上下文
5. **Phase 1 - Thinking**（可选）— 不带工具调用 LLM，强制慢思考/推理
6. **Phase 2 - Action** — 带全部工具调用 LLM
7. **工具执行** — 只读工具并行（信号量=5），写入工具串行（Workspace RWMutex）
8. **错误恢复** — RecoveryManager 分析错误并注入恢复提示
9. **死循环检测** — MD5 指纹 + 参数规范化，连续 3 次相同失败注入强纠正
10. **循环终止** — LLM 不再返回工具调用时结束

### 编辑工具的 4 级匹配策略

`edit_file` 工具在精确匹配失败时，会依次尝试：

```
L1: 精确匹配          — 原始字符串完全一致
L2: 换行标准化        — \r\n → \n 后匹配
L3: TrimSpace 匹配    — 去除首尾空白后匹配
L4: 滑动窗口匹配      — 逐行去缩进 + 智能缩进锚定
```

这种设计确保 LLM 生成的代码片段（可能带有不一致的缩进）能够可靠地定位到目标文件中的替换位置。

### 自适应上下文压缩

Compactor 根据 Token 利用率自动选择压缩级别：

| 利用率 | 级别 | 策略 |
|--------|------|------|
| < 50% | 无压缩 | 保持完整上下文 |
| 50-70% | 轻柔 | 仅遮蔽远端历史 |
| 70-85% | 标准 | 遮蔽远端 + 最近消息首尾各 500 字符 |
| 85-95% | 激进 | 遮蔽远端 + 最近消息首尾各 200 字符 |
| > 95% | 紧急 | 遮蔽全部，仅保留最近 2 条消息 |

### 动态权限引擎

基于 Copy-on-Write 架构的权限系统，通过 `.claw/permissions.yaml` 配置：

- **三级策略** — `allow`（放行）、`ask`（询问用户）、`deny`（拒绝）
- **正则匹配** — 支持正则表达式规则匹配工具调用参数
- **热重载** — 配置文件修改后自动生效，无需重启
- **内置安全规则** — 17 条预置规则覆盖 `rm -rf`、`format`、`sudo`、`DROP DATABASE`、`kubectl delete namespace` 等危险操作

### 子代理系统

通过 `spawn_subagent` / `check_subagent` 工具实现任务分解：

- **只读隔离** — 子代理仅获得只读工具集（read_file、bash 等），无法修改文件
- **异步执行** — 子代理在独立 goroutine 中运行，最多 10 轮
- **自动通知** — 子代理完成后自动注入结果到主 Agent 的下一轮对话
- **并发安全** — SubagentManager 管理生命周期，支持并发多个子代理

### 死循环检测

ReminderInjector 通过 MD5 指纹 + 参数规范化识别重复失败：

- **参数规范化** — 路径标准化、bash 命令归一化，避免误判
- **渐进纠正** — 第 1 次失败温和提醒，连续 3 次相同失败注入强纠正指令
- **自动重置** — 不同的工具调用自动重置计数器

### Plan 模式

启用后强制外部化任务状态管理：

- **PLAN.md** — 记录总体计划和当前进度
- **TODO.md** — 记录待办事项和步骤
- **断点续跑** — 任务中断后可从 PLAN.md 恢复继续执行
- **严格步骤化** — 强制使用 checkbox 格式跟踪每一步进度

### 飞书审批

在飞书模式下支持人工介入审批流：

- `approve <taskID>` — 批准任务继续执行
- `reject <taskID>` — 拒绝任务
- 超时自动取消，后台自动清理过期审批

## 并发模型

```
                    ┌──────────────────┐
                    │    Run() 请求     │
                    └────────┬─────────┘
                             │
                    ┌────────▼─────────┐
                    │   Workspace 锁    │  ← RWMutex，保证跨 Run 文件系统一致性
                    │   (Write Lock)    │
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

- **只读工具**（read_file, read_skill, TaskOutput, spawn_subagent）：通过信号量（buffered channel, cap=5）并行执行
- **写入工具**（write_file, edit_file, bash）：严格串行执行，保证文件系统一致性
- **后台任务**：bash 的 `run_in_background=true` 模式，通过 TaskManager 异步管理

## 会话管理

每个用户/聊天拥有独立的 Session，通过 `GlobalSessionMgr`（并发安全 Map）管理：

- **双维度滑动窗口截断**：同时限制消息条数（默认 6 条）和字符预算（默认 50,000 字符）
- **孤儿 ToolResult 清理**：截断后自动检测并移除没有对应 ToolCall 的 ToolResult，防止 API 400 错误
- **密钥遗忘机制**：敏感信息（API Key 等）在截断时被安全刷新

## 可观测性

### 费用追踪

CostTracker 以装饰器模式包装 LLMProvider，自动追踪：

- 每次调用的 Input/Output Token 数
- 按 Session 汇总费用（基于模型定价查表）
- CLI 模式结束时输出总费用摘要

### 分布式追踪

基于 Span 层级的分布式追踪系统：

- 支持 Parent-Child 关系的 Span 树
- JSON 格式导出到 `.claw/traces/` 目录
- 记录工具调用耗时、LLM 调用耗时等关键指标

## 评估框架

通过 `cmd/bench` 独立 CLI 运行 Benchmark：

```bash
# 构建 Benchmark CLI
go build -o bench.exe ./cmd/bench

# 运行 Benchmark
./bench.exe
```

BenchmarkRunner 特性：

- **沙箱隔离** — 每个测试用例创建独立临时目录
- **Setup / Validate 脚本** — 通过 Shell 脚本准备环境和验证结果
- **Mock Provider** — 单元测试可使用 Mock 模拟 LLM 响应
- **指标输出** — 通过率、总费用、耗时、轮次、试错轮次、恢复 Token 数

测试用例定义示例：

```go
TestCase{
    ID:           "edit-accuracy",
    Name:         "文件编辑准确性测试",
    SetupScript:  `echo '{"version":"1.0"}' > data.json`,
    TaskPrompt:   "将 data.json 中的 version 改为 2.0",
    ValidateScript: `grep -q '"version":"2.0"' data.json`,
    MaxTurns:     5,
}
```

## 测试

```bash
# 单元测试（无需 API Key）
go test ./internal/engine/... -v
go test ./internal/tools/... -v
go test ./internal/context/... -v
go test ./internal/permissions/... -v
go test ./internal/observability/... -v
go test ./internal/eval/... -v

# 集成测试（需要 ANTHROPIC_API_KEY）
go test ./internal/engine/... -v -run TestIntegration
go test ./internal/eval/... -v -run TestIntegration
```

## 技能系统

在 `.claw/skills/` 目录下创建 SKILL.md 文件即可定义技能：

```markdown
---
name: my-skill
description: 技能触发描述
triggers:
  - "触发关键词"
---

技能完整内容...
```

技能采用**渐进式暴露**策略：
- System Prompt 中仅注入技能元数据（name + description）
- 当 LLM 需要使用技能时，通过 `read_skill` 工具按需加载完整内容

## 配置优先级

```
环境变量 > config.yaml > 默认值
```

## 依赖

| 依赖 | 用途 |
|------|------|
| `anthropic-sdk-go` | Anthropic Claude API 客户端 |
| `openai-go/v3` | OpenAI 兼容 API 客户端 |
| `oapi-sdk-go/v3` | 飞书 SDK |
| `yaml.v3` | YAML 配置解析 |

## License

MIT
