<h1 align="center">go-tiny-claw</h1>

<p align="center">
  <strong>轻量级、自托管的 Go 语言 AI Agent 引擎</strong><br>
  <em>用 OS 思维构建 AI 大脑 —— ReAct 循环 × 文件系统工具 × Shell 命令</em>
</p>

<p align="center">
  <a href="https://github.com/lhuan/go-tiny-claw/actions/workflows/ci.yml"><img src="https://img.shields.io/badge/CI-passing-brightgreen?logo=github&logoColor=white" alt="CI"></a>
  <a href="https://go.dev/"><img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white" alt="Go 1.25+"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-green" alt="MIT License"></a>
</p>

---

## 为什么需要 go-tiny-claw

| | Claude Code | Aider | go-tiny-claw |
|---|---|---|---|
| 语言 | TypeScript | Python | **Go（单二进制，零依赖部署）** |
| 部署 | 需要 Node.js 运行时 | 需要 Python 运行时 | **`scp claw && ./claw`** |
| 模型 | 仅 Anthropic | 多模型 | **Anthropic + OpenAI 兼容** |
| 平台接入 | 终端 | 终端 | **终端 + 飞书 + 个人微信 + 企业微信（⚠️ 尚未实测）** |
| 权限控制 | 简单 allow/deny | 无 | **COW 引擎 + 正则热重载 + 审批流** |
| 会话持久化 | 本地 SQLite | 无 | **JSONL + 断点续跑** |
| 子代理 | 无 | 无 | **异步 spawn + 自动通知** |
| 上下文压缩 | 基础截断 | 基础截断 | **5 级自适应（基于 Token 利用率）** |
| 后台进程 | 无 | 无 | **OS PCB 模型，全生命周期管理** |

**一句话**：如果你需要一个可以部署到任意服务器、接入飞书/个人微信、支持多模型、具备完整权限控制的 AI Agent 引擎，go-tiny-claw 是目前唯一的 Go 原生方案。

## 快速开始

```bash
# 1. 构建
git clone https://github.com/lhuan/go-tiny-claw.git && cd go-tiny-claw
go build -o claw ./cmd/claw

# 2. 配置
cp config.yaml.example config.yaml
# 编辑 config.yaml 填入模型名称，然后设置环境变量（二选一）：
# 方式 A：Anthropic Claude
export ANTHROPIC_BASE_URL="https://api.anthropic.com"
export ANTHROPIC_API_KEY="sk-ant-..."
# 方式 B：OpenAI 兼容（DeepSeek、智谱、中转站等）
export OPENAI_BASE_URL="https://api.deepseek.com/"
export OPENAI_API_KEY="sk-..."
export OPENAI_MODEL="deepseek-chat"

# 3. 运行
./claw run "分析当前目录的代码结构"
```

<details>
<summary><strong>CLI 子命令</strong></summary>

```bash
# 单次执行（最常用）
./claw run "你的任务描述"

# 从文件读取 prompt
./claw run -f task.txt

# 管道输入（适合脚本集成）
echo "解释这段代码" | ./claw run
cat error.log | ./claw run "分析错误日志"

# 交互式 REPL（带命令历史）
./claw repl

# 查看所有命令
./claw --help

# 向后兼容 — 旧写法仍然有效
./claw -prompt "hello"
```

</details>

<details>
<summary><strong>子命令一览</strong></summary>

| 命令 | 说明 |
|------|------|
| `claw run [prompt]` | 单次执行任务 |
| `claw repl` | 启动交互式 REPL |
| `claw serve --feishu --ilink` | 同时启动多个消息渠道 |
| `claw feishu` | 启动飞书 Bot |
| `claw ilink` | 启动 iLink Bot（个人微信） |
| `claw server` | 启动 HTTP API 服务（企业微信，⚠️ 尚未实测） |
| `claw session list` | 列出所有会话 |
| `claw session clean [id]` | 清理会话数据 |
| `claw config show` | 显示当前配置 |
| `claw config init` | 生成示例配置 |
| `claw version` | 显示版本信息 |

</details>

<details>
<summary><strong>REPL 交互模式</strong></summary>

```bash
./claw repl
# 🦞 你好，请输入任务描述
# 🦞 帮我写一个 Go HTTP 服务器
# ... Agent 执行中 ...
# 🦞 /reset          ← 重置会话
# 🦞 /history        ← 查看输入历史
# 🦞 /exit           ← 退出
```

- **Ctrl+C**：中断当前任务（不退出 REPL）
- **上下箭头**：浏览历史命令
- 历史自动保存到 `~/.claw_history`

</details>

<details>
<summary><strong>管道与脚本集成</strong></summary>

```bash
# 输出纯净内容（无装饰），适合管道传递
./claw run "分析当前目录" | grep "TODO"

# 退出码：0=成功，1=失败，130=中断
./claw run "运行测试" && echo "通过" || echo "失败"

# CI/CD 集成示例
./claw run "检查代码质量" -session "ci-$BUILD_ID"
```

管道模式下 stdout 只输出内容，装饰信息走 stderr，可安全串联。

</details>

<details>
<summary><strong>更多运行模式</strong></summary>

```bash
# 指定会话 ID（断点续跑）
./claw run "继续上次的任务" --session my-session-id

# 飞书模式 — 设置 config.yaml 中的 feishu.app_id 后
./claw feishu

# 个人微信模式 — 设置 config.yaml 中的 ilink.token 后
./claw ilink

# 同时启动飞书和个人微信
./claw serve --feishu --ilink

# 企业微信模式 — ⚠️ 尚未实测
# ./claw server

# Docker（Anthropic Claude）
docker build -t go-tiny-claw .
docker run -e ANTHROPIC_BASE_URL=https://api.anthropic.com -e ANTHROPIC_API_KEY=sk-... go-tiny-claw run "hello"

# Docker（OpenAI 兼容，如 DeepSeek）
docker run -e OPENAI_BASE_URL=https://api.deepseek.com/ -e OPENAI_API_KEY=sk-... -e OPENAI_MODEL=deepseek-chat go-tiny-claw run "hello"
```

</details>

<details>
<summary><strong>iLink Bot 创建指南（个人微信）</strong></summary>

### 什么是 iLink Bot？

iLink 是微信官方推出的个人微信机器人平台，允许开发者通过 API 接收和发送微信消息。

### 创建步骤

#### 1. 访问 iLink 平台

- **官方地址**：https://ilinkai.weixin.qq.com
- **备用地址**：https://ilink.weixin.qq.com
- ⚠️ **如果无法访问**：
  - 使用代理访问（推荐）
  - 或在微信客户端内打开链接
  - 或使用微信开发者工具内置浏览器

#### 2. 登录并创建 Bot

1. 使用微信扫码登录平台
2. 进入控制台（Dashboard）
3. 点击「创建机器人」或「Create Bot」
4. 填写信息：
   - **机器人名称**：给你的 Bot 起个名字
   - **机器人描述**：可选，描述 Bot 用途
   - **类型**：选择「个人微信」

#### 3. 扫码授权

1. 创建成功后，平台会显示一个二维码
2. 用**个人微信小号**扫描该二维码
3. 确认授权登录
4. ⚠️ **重要**：务必使用小号，避免主号被封风险

#### 4. 获取 API Token

1. 授权成功后，进入 Bot 详情页
2. 找到「API Token」或「API Key」字段
3. 复制 Token，格式类似：
   ```
   xxxxxxxx@im.bot:xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
   ```
4. ⚠️ **妥善保管 Token，不要泄露**

#### 5. 配置到 go-tiny-claw

编辑 `claw.yaml` 文件：

```yaml
ilink:
  token: "你获取的 Bot Token"
  base_url: "https://ilinkai.weixin.qq.com"
```

#### 6. 启动服务

```bash
# 方式一：单独启动 iLink
./claw ilink

# 方式二：同时启动飞书和 iLink
./claw serve --feishu --ilink
```

### 验证是否成功

启动后，向你的微信小号发送消息，观察：
1. 终端是否显示收到消息
2. 是否自动回复

### 常见问题

#### Q: 访问 ilinkai.weixin.qq.com 显示 405 错误？
A: 这是正常的，说明服务器存在。使用浏览器访问时应该能看到登录页面。

#### Q: Token 无效或过期？
A: 重新登录平台，在 Bot 详情页获取新的 Token。

#### Q: 收不到消息？
A: 检查：
1. Token 是否正确配置
2. 微信小号是否在线
3. 网络是否通畅

### 安全提醒

- ⚠️ **封号风险**：使用非官方微信机器人可能导致封号
- ⚠️ **使用小号**：强烈建议使用微信小号，不要使用主号
- ⚠️ **遵守规则**：遵守微信使用条款，不要发送垃圾消息

</details>

<details>
<summary><strong>支持的模型</strong></summary>

通过环境变量切换 Provider（代码只判断 `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` 两条路径）：

| Provider | 必需环境变量 | 可选环境变量 | 模型示例 |
|----------|-------------|-------------|---------|
| Anthropic Claude | `ANTHROPIC_BASE_URL` + (`ANTHROPIC_API_KEY` 或 `ANTHROPIC_AUTH_TOKEN`) | `ANTHROPIC_MODEL` | claude-sonnet-4-20250514 |
| DeepSeek | `OPENAI_BASE_URL` + `OPENAI_API_KEY` | `OPENAI_MODEL` | deepseek-chat |
| 智谱 GLM | `OPENAI_BASE_URL` + `OPENAI_API_KEY` | `OPENAI_MODEL` | glm-4.5-air |
| 任意 OpenAI 兼容 | `OPENAI_BASE_URL` + `OPENAI_API_KEY` | `OPENAI_MODEL` | — |

> **注意**：DeepSeek、智谱等兼容 OpenAI 协议的 Provider 统一通过 `OPENAI_BASE_URL` 接入，没有单独的 `DEEPSEEK_API_KEY` 或 `ZHIPU_API_KEY`。
>
> 也可通过 `~/.claude/settings.json` 的 `env` 字段自动注入环境变量（与 Claude Code 配置集成）。

</details>

## 架构

> 📊 [查看精美架构图](https://llhuanhuan.github.io/go-tiny-claw/architecture.html)

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│                        入口交互层  Entry & UI Layer                                 │
│                                                                                   │
│    CLI (cobra)            飞书 Bot          个人微信 Bot        企业微信 Bot         │
│    run · repl ·           WebSocket         iLink HTTP          Webhook             │
│    serve · feishu         长连接 · 审批卡片  长轮询 · DM         回调 · 审批           │
│    ilink · session        流式卡片编辑      流式回复       (⚠️ 未实测) Markdown     │
│                                                                                   │
│                统一 Reporter 接口：Terminal · Feishu · WeChat · ILink               │
│                serve 多渠道并发 · Bootstrap 引擎初始化栈 · 管道模式                  │
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

## 内置工具

| 工具 | 说明 |
|------|------|
| `read_file` | 读取文件（8KB 截断保护） |
| `write_file` | 写入文件（自动创建父目录） |
| `edit_file` | 4 级模糊匹配替换（L1 精确 → L2 CRLF → L3 TrimSpace → L4 滑动窗口） |
| `bash` | Shell 命令（同步 30s 超时 / 异步后台），跨平台 |
| `search_files` | 正则搜索文件内容，支持通配符过滤 |
| `fetch_url` | HTTP GET（超时 + 大小截断） |
| `spawn_subagent` | 异步子代理（只读隔离，context 取消传播） |
| `check_subagent` | 轮询子代理状态 |
| `read_skill` | 渐进式加载 SKILL.md |
| `TaskOutput` / `TaskStop` | 后台进程管理 |

## 核心设计

### ReAct 循环

```
用户输入 → 上下文组装 → 自适应压缩 → Thinking(可选) → Action → 工具执行 → 错误恢复 → 循环/终止
                                                    ↑                    |
                                                    └────────────────────┘
```

- **流式输出**：Thinking 和 Action 阶段均支持逐字推送（OnStreamDelta）
- **并发策略**：只读工具并行（信号量=5），写入工具串行（Workspace RWMutex）
- **死循环检测**：MD5 指纹 + 参数规范化，连续 3 次相同失败自动注入强纠正

### 自适应上下文压缩

| Token 利用率 | 级别 | 策略 |
|-------------|------|------|
| < 50% | 无压缩 | 保持完整上下文 |
| 50–70% | 轻柔 | 遮蔽远端历史 |
| 70–85% | 标准 | 远端遮蔽 + 近期首尾各 500 字符 |
| 85–95% | 激进 | 远端遮蔽 + 近期首尾各 200 字符 |
| > 95% | 紧急 | 仅保留最近 2 条消息 |

### 权限引擎

```yaml
# .claw/permissions.yaml
rules:
  - id: block-rm-rf
    pattern: "rm\\s+-rf"
    action: deny
    reason: "禁止递归删除"
  - id: ask-sudo
    pattern: "sudo"
    action: ask       # CLI 模式弹终端确认，飞书模式发审批卡片
    reason: "需要提权操作"
```

- **Copy-on-Write**：读操作无锁，写操作原子替换，支持热重载
- **14 条内置规则**：覆盖 `rm -rf`、`DROP DATABASE`、`kubectl delete namespace` 等（5 deny + 5 ask + 4 allow）

### 分布式追踪（Tracing）

Agent 的每次运行被建模为一棵 Span 树，完整记录 ReAct 循环中每个阶段的耗时和属性：

```
Agent.Run (8101ms)
├── Turn-1 (3189ms)
│   ├── LLM.Thinking (1281ms)
│   ├── LLM.Action (1772ms)
│   ├── Tool.Execute (15ms)   read_file
│   └── Tool.Execute (120ms)  bash
├── Turn-2 (2943ms)
│   ├── LLM.Thinking (1364ms)
│   ├── LLM.Action (1570ms)
│   └── Tool.Execute (8ms)    write_file
└── Turn-3 (1969ms)
    └── LLM.Action (1969ms)
```

**Exporter 抽象层**：Trace 数据通过 `Exporter` 接口并行导出到多个后端：

| Exporter | 输出目标 | 用途 |
|----------|---------|------|
| `FileExporter` | `.claw/traces/*.json` | 本地 JSON 回放 |
| `OTelExporter` | Jaeger / Tempo / SigNoz | 甘特图可视化 |
| `LogExporter` | 终端日志 | 调试用文本树 |

**Jaeger 快速启动**：

```bash
# 方式一：Docker
docker run -d --name jaeger \
  -p 16686:16686 -p 4317:4317 \
  jaegertracing/all-in-one:1.57

# 方式二：本地二进制
# 下载 https://github.com/jaegertracing/jaeger/releases
./jaeger-all-in-one &

# 浏览器打开 http://localhost:16686 → 服务选 'go-tiny-claw' → Find Traces
```

## 测试

```bash
# 单元测试（无需 API Key，CI 自动运行）
go test ./internal/tools/... ./internal/context/... ./internal/permissions/... ./internal/observability/... ./internal/engine/... ./internal/provider/... ./internal/feishu/... -v

# 全量验证（含 go vet）
go build ./... && go vet ./... && go test ./internal/...

# 集成测试（需要 API Key，CI 环境自动跳过）
go test ./internal/engine/... -v -run TestIntegration
```

## 项目结构

```
go-tiny-claw/
├── cmd/claw/
│   ├── main.go           # 入口（cobra 根命令 + 飞书/微信启动）
│   ├── root.go           # cobra 根命令定义 + 旧参数兼容
│   ├── run.go            # `claw run` — 单次执行（管道 + 文件输入）
│   ├── repl.go           # `claw repl` — 交互式 REPL（liner）
│   ├── bootstrap.go      # 引擎初始化栈（Provider → Permissions → Tools → Engine）
│   ├── terminal.go       # ANSI 终端输出 + 管道安全检测
│   ├── config.go         # 配置结构定义
│   ├── serve_cmd.go      # `claw serve` 多渠道并发
│   ├── feishu_cmd.go     # `claw feishu` 子命令
│   ├── ilink_cmd.go      # `claw ilink` 子命令
│   ├── server_cmd.go     # `claw server` 子命令
│   ├── session_cmd.go    # `claw session list/clean` 子命令
│   ├── config_cmd.go     # `claw config show/init` 子命令
│   └── version_cmd.go    # `claw version` 子命令
├── cmd/bench/            # Benchmark CLI
├── cmd/trace-demo/       # Tracing 演示程序
├── internal/
│   ├── engine/           # ReAct 循环、会话管理、子代理
│   ├── provider/         # LLM Provider（Claude / OpenAI）+ 限流 + Mock
│   ├── schema/           # 核心数据类型（Message、ToolCall、ToolResult）
│   ├── tools/            # 11 个内置工具 + 注册表 + 中间件
│   ├── context/          # Prompt 组装、压缩、技能、错误恢复
│   ├── permissions/      # 动态权限引擎（COW + 热重载）
│   ├── observability/    # 费用追踪 + 分布式追踪
│   ├── feishu/           # 飞书 Bot + 审批
│   ├── ilink/            # iLink Bot（个人微信）
│   ├── wechat/           # 企业微信 Bot
│   └── eval/             # Benchmark 框架
├── .claw/                # 技能定义 + 权限规则 + 追踪导出
├── Dockerfile            # 多阶段构建
└── config.yaml.example
```

## 文档

- [设计文档](docs/DESIGN.md) — 架构设计、核心机制、构建演进、从零复现指南
- [贡献指南](CONTRIBUTING.md) — 开发环境、代码规范、PR 流程

## License

[MIT](LICENSE)
