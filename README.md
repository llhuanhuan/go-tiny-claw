<h1 align="center">go-tiny-claw</h1>

<p align="center">
  <strong>轻量级、自托管的 Go 语言 AI Agent 引擎</strong><br>
  <em>用 OS 思维构建 AI 大脑 —— ReAct 循环 × 文件系统工具 × Shell 命令</em>
</p>

<p align="center">
  <a href="https://github.com/lhuan/go-tiny-claw/actions"><img src="https://github.com/lhuan/go-tiny-claw/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://go.dev/"><img src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white" alt="Go 1.25+"></a>
  <a href="https://goreportcard.com/report/github.com/lhuan/go-tiny-claw"><img src="https://goreportcard.com/badge/github.com/lhuan/go-tiny-claw" alt="Go Report Card"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-green" alt="MIT License"></a>
</p>

---

## 为什么需要 go-tiny-claw

| | Claude Code | Aider | go-tiny-claw |
|---|---|---|---|
| 语言 | TypeScript | Python | **Go（单二进制，零依赖部署）** |
| 部署 | 需要 Node.js 运行时 | 需要 Python 运行时 | **`scp claw && ./claw`** |
| 模型 | 仅 Anthropic | 多模型 | **Anthropic + OpenAI 兼容** |
| 平台接入 | 终端 | 终端 | **终端 + 飞书 + 企业微信** |
| 权限控制 | 简单 allow/deny | 无 | **COW 引擎 + 正则热重载 + 审批流** |
| 会话持久化 | 本地 SQLite | 无 | **JSONL + 断点续跑** |
| 子代理 | 无 | 无 | **异步 spawn + 自动通知** |
| 上下文压缩 | 基础截断 | 基础截断 | **5 级自适应（基于 Token 利用率）** |
| 后台进程 | 无 | 无 | **OS PCB 模型，全生命周期管理** |

**一句话**：如果你需要一个可以部署到任意服务器、接入飞书/微信、支持多模型、具备完整权限控制的 AI Agent 引擎，go-tiny-claw 是目前唯一的 Go 原生方案。

## 快速开始

```bash
# 1. 构建
git clone https://github.com/lhuan/go-tiny-claw.git && cd go-tiny-claw
go build -o claw ./cmd/claw

# 2. 配置
cp config.yaml.example config.yaml
# 编辑 config.yaml 填入模型名称，或设置环境变量：
export ANTHROPIC_API_KEY="sk-ant-..."

# 3. 运行
./claw -prompt "分析当前目录的代码结构"
```

<details>
<summary><strong>更多运行模式</strong></summary>

```bash
# 指定工作目录 + 会话 ID（断点续跑）
./claw -prompt "继续上次的任务" -dir /path/to/project -session my-session-id

# 交互式终端（无 -prompt 时进入）
./claw

# 飞书模式 — 设置 config.yaml 中的 feishu.app_id 后直接运行
./claw

# 企业微信模式 — 设置 config.yaml 中的 wechat.webhook_url 后直接运行
./claw

# Docker
docker build -t go-tiny-claw .
docker run -e ANTHROPIC_API_KEY=sk-... go-tiny-claw -prompt "hello"
```

</details>

<details>
<summary><strong>支持的模型</strong></summary>

通过环境变量切换 Provider：

| Provider | 环境变量 | 模型示例 |
|----------|---------|---------|
| Anthropic Claude | `ANTHROPIC_API_KEY` | claude-sonnet-4-20250514 |
| DeepSeek | `DEEPSEEK_API_KEY` | deepseek-chat |
| 智谱 GLM | `ZHIPU_API_KEY` | glm-4.5-air |
| 任意 OpenAI 兼容 | `OPENAI_BASE_URL` + `OPENAI_API_KEY` | — |

也可通过 `~/.claude/settings.json` 自动注入环境变量（与 Claude Code 配置集成）。

</details>

## 架构

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
- **17 条内置规则**：覆盖 `rm -rf`、`DROP DATABASE`、`kubectl delete namespace` 等

## 测试

```bash
# 单元测试（无需 API Key）
go test ./internal/tools/... ./internal/context/... ./internal/permissions/... -v

# 全量验证
go build ./... && go vet ./... && go test ./internal/...

# 集成测试（需要 API Key）
go test ./internal/engine/... -v -run TestIntegration
```

## 项目结构

```
go-tiny-claw/
├── cmd/claw/          # CLI 入口 + 配置加载
├── cmd/bench/         # Benchmark CLI
├── internal/
│   ├── engine/        # ReAct 循环、会话管理、子代理
│   ├── provider/      # LLM Provider（Claude / OpenAI）+ 限流 + Mock
│   ├── tools/         # 11 个内置工具 + 注册表 + 中间件
│   ├── context/       # Prompt 组装、压缩、技能、错误恢复
│   ├── permissions/   # 动态权限引擎（COW + 热重载）
│   ├── observability/ # 费用追踪 + 分布式追踪
│   ├── feishu/        # 飞书 Bot + 审批
│   ├── wechat/        # 企业微信 Bot
│   └── eval/          # Benchmark 框架
├── .claw/             # 技能定义 + 权限规则 + 追踪导出
├── Dockerfile         # 多阶段构建
└── config.yaml.example
```

## 文档

- [设计文档](docs/DESIGN.md) — 架构设计、核心机制、构建演进、从零复现指南
- [贡献指南](CONTRIBUTING.md) — 开发环境、代码规范、PR 流程

## License

[MIT](LICENSE)
