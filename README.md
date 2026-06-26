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
- **渐进式技能系统** — 类似 Claude Code 的 SKILL.md 技能定义，按需加载，元数据优先暴露
- **多平台接入** — 终端 CLI / 飞书（WebSocket 长连接）/ 企业微信（HTTP Webhook）三种运行模式
- **多模型支持** — Anthropic Claude、OpenAI 兼容协议（DeepSeek、智谱 GLM 等）

## 架构总览

```
┌─────────────────────────────────────────────────┐
│              Platform Layer (平台层)              │
│    Terminal CLI  │  Feishu Bot  │  WeChat Bot    │
├─────────────────────────────────────────────────┤
│              Engine Layer (引擎层)               │
│    ReAct Loop  │  Session  │  PromptComposer    │
├─────────────────────────────────────────────────┤
│              Tools Layer (工具层)                │
│  read_file │ write_file │ edit_file │ bash      │
│  read_skill │ TaskOutput │ TaskStop │ RingBuf   │
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
├── cmd/claw/
│   ├── main.go              # 入口：模式检测、引擎启动
│   └── config.go            # YAML 配置加载（环境变量覆盖）
├── internal/
│   ├── schema/
│   │   └── message.go       # 核心数据类型：Message, ToolCall, ToolResult
│   ├── provider/
│   │   ├── interface.go     # LLMProvider 接口 + StreamEvent 类型
│   │   ├── claude.go        # Anthropic Claude 提供者（流式 + 阻塞）
│   │   ├── opaenai.go       # OpenAI 兼容提供者（DeepSeek、智谱）
│   │   ├── stream.go        # StreamEvent 类型定义
│   │   └── accumulator.go   # StreamAccumulator：流式事件组装
│   ├── tools/
│   │   ├── registry.go      # BaseTool 接口 + Registry（工具路由分发）
│   │   ├── read_file.go     # 文件读取（8KB 截断保护）
│   │   ├── write_file.go    # 文件写入（自动创建目录）
│   │   ├── edit_file.go     # 4 级模糊匹配替换引擎
│   │   ├── bash.go          # Shell 执行器（同步 30s 超时 + 异步后台）
│   │   ├── read_skill.go    # 渐进式技能加载器
│   │   ├── task_manager.go  # 后台进程生命周期管理器
│   │   ├── task_output.go   # 查询后台任务状态/日志
│   │   ├── task_stop.go     # 终止后台任务
│   │   ├── ringbuf.go       # 线程安全环形缓冲区（64KB）
│   │   └── task_manager_test.go
│   ├── engine/
│   │   ├── loop.go          # ReAct 核心循环
│   │   ├── session.go       # 会话管理 + 双维度滑动窗口
│   │   ├── reporter.go      # Reporter 接口（可插拔输出层）
│   │   ├── terminal_reporter.go
│   │   ├── session_test.go  # 15 个单元测试
│   │   └── integration_test.go  # 10 个集成测试
│   ├── context/
│   │   ├── composer.go      # System Prompt 构建器（3 段式架构）
│   │   └── skill.go         # SKILL.md 解析器 + 渐进式加载
│   ├── feishu/
│   │   └── bot.go           # 飞书 WebSocket 机器人
│   ├── wechat/
│   │   └── bot.go           # 企业微信 Webhook 机器人
│   └── memory/              # （预留）记忆模块
├── .claw/skills/            # 技能定义目录
│   └── git-workflow/SKILL.md
├── AGENTS.md                # Agent 规则（注入 System Prompt）
├── config.yaml              # 活跃配置
├── config.yaml.example      # 配置模板
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

## 编辑工具的 4 级匹配策略

`edit_file` 工具在精确匹配失败时，会依次尝试：

```
L1: 精确匹配          — 原始字符串完全一致
L2: 换行标准化        — \r\n → \n 后匹配
L3: TrimSpace 匹配    — 去除首尾空白后匹配
L4: 滑动窗口匹配      — 逐行去缩进 + 智能缩进锚定
```

这种设计确保 LLM 生成的代码片段（可能带有不一致的缩进）能够可靠地定位到目标文件中的替换位置。

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

- **只读工具**（read_file, read_skill, TaskOutput）：通过信号量（buffered channel, cap=5）并行执行
- **写入工具**（write_file, edit_file, bash）：严格串行执行，保证文件系统一致性
- **后台任务**：bash 的 `run_in_background=true` 模式，通过 TaskManager 异步管理

## 会话管理

每个用户/聊天拥有独立的 Session，通过 `GlobalSessionMgr`（并发安全 Map）管理：

- **双维度滑动窗口截断**：同时限制消息条数（默认 6 条）和字符预算（默认 50,000 字符）
- **孤儿 ToolResult 清理**：截断后自动检测并移除没有对应 ToolCall 的 ToolResult，防止 API 400 错误
- **密钥遗忘机制**：敏感信息（API Key 等）在截断时被安全刷新

## 测试

```bash
# 单元测试（无需 API Key）
go test ./internal/engine/... -v
go test ./internal/tools/... -v

# 集成测试（需要 ANTHROPIC_API_KEY）
go test ./internal/engine/... -v -run TestIntegration
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
