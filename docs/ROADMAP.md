# 开发路线图

> go-tiny-claw 的功能演进计划，基于竞品分析和 agency-agents 多角色评审。

## 当前状态

**版本**: v0.2.0（2026-07-08）
**已完成功能**:

```
✅ ReAct 引擎（Thinking + Action 双阶段，流式输出）
✅ 11 个内置工具（read/write/edit_file, bash, search_files, fetch_url 等）
✅ 会话持久化（JSONL 格式，支持断点续跑）
✅ 自适应上下文压缩（5 级，基于 Token 利用率）
✅ 动态权限引擎（COW + 正则热重载 + 审批流）
✅ 子代理系统（异步 spawn，context 取消传播）
✅ 死循环检测（MD5 指纹 + 参数规范化）
✅ 多平台接入（CLI / 飞书 / 企业微信）
✅ 可观测性（CostTracker + 分布式追踪）
✅ 评估框架（BenchmarkRunner）
✅ API 限流器（令牌桶算法）
✅ 配置校验（Validate）
✅ 跨平台 bash（Windows 自动使用 Git Bash）
✅ Docker 部署
✅ CI/CD（GitHub Actions）
✅ /stop 中断 + /reset 重置命令
✅ 代理配置（config.yaml proxy 段）
```

## 竞品参考

| 项目 | Stars | go-tiny-claw 缺少的关键特性 |
|------|-------|--------------------------|
| charmbracelet/crush | 26k | MCP、LSP、TUI |
| whale (usewhale) | 898 | 98% prompt cache、MCP、Plugin |
| google/adk-go | 8k | 模型无关、评估工具包 |
| matrixclaw | 27 | SQLite 记忆、MCP、Telegram |
| trpc-agent-go | 1.5k | 图工作流、MCP、A2A |

## 独特优势（竞品没有）

- 唯一有飞书 + 企微原生集成的 Go Agent
- 唯一有 COW 权限引擎 + 正则热重载 + 审批流
- 5 级自适应上下文压缩（竞品只做简单截断）
- OS PCB 模型后台进程管理
- 真正的单二进制零依赖部署

---

## Sprint 1：稳定性基础 ✅ 已完成

| 功能 | 状态 | 文件 |
|------|------|------|
| API 重试 + 指数退避 | ✅ 代码完成 | `internal/provider/retry.go` |
| 工具参数 Schema 校验 | ✅ 代码完成 | `internal/tools/validate.go` |
| 飞书流式推送 | ✅ 代码完成 | `internal/feishu/bot.go` |
| /stop + /reset 命令 | ✅ 已提交 | `internal/feishu/bot.go` |

**待办**: struct tag 格式修复 → 编译+测试验证 → 提交推送

---

## Sprint 2：核心能力扩展（待开始）

### 2.1 MCP 客户端
- **目标**: 支持 MCP (Model Context Protocol)，接入 1000+ 社区工具服务器
- **方案**: stdio 模式启动 MCP Server 子进程，JSON-RPC 2.0 通信
- **接口**: `MCPClient` 实现 `BaseTool` 接口，动态注册外部工具
- **复杂度**: 高（~500 行）
- **依赖**: Sprint 1（重试 + Schema 校验）

### 2.2 Plugin 系统
- **目标**: 支持 YAML 配置定义外部工具，无需改代码即可扩展
- **方案**: `plugins/` 目录扫描 `*.yaml`，每个映射为一个 `BaseTool`
- **支持类型**: HTTP endpoint / subprocess / shell command
- **复杂度**: 中（~200 行）

### 2.3 持久化记忆
- **目标**: 跨会话的长期记忆，用户偏好和项目知识持久保存
- **方案**: SQLite 存储 + 向量搜索（sqlite-vec 或 cosine 距离）
- **接口**: `MemoryProvider` 接口 + SQLite 实现
- **复杂度**: 高（~400 行，需引入 embedding 模型）

---

## Sprint 3：生产就绪（待开始）

### 3.1 Web API + SSE
- **目标**: HTTP 接口，支持程序化调用和 Web 前端集成
- **方案**: `net/http` server + SSE 流式端点
- **端点**: `POST /chat`、`GET /chat/:id/stream`、`GET /health`
- **复杂度**: 中（~250 行）

### 3.2 多模型路由
- **目标**: 按任务类型/成本/延迟自动选择模型，支持 fallback
- **方案**: `RouterProvider` 包装多个 `LLMProvider`
- **复杂度**: 中（~150 行）

### 3.3 golangci-lint CI
- **目标**: CI 增加静态分析、安全扫描
- **方案**: CI workflow 加 golangci-lint + gosec step
- **复杂度**: 低

---

## Sprint 4：完善打磨（待开始）

### 4.1 goreleaser 版本发布
- 方案: `.goreleaser.yaml` + GitHub Actions release workflow
- 复杂度: 低

### 4.2 用户指南
- 方案: `docs/` 下补充 quickstart、configuration、troubleshooting
- 复杂度: 低

### 4.3 Token 精确计数
- 方案: 引入 `tiktoken-go` 或 `cl100k_base` 编码器
- 复杂度: 中

---

## 远期规划

| 方向 | 说明 |
|------|------|
| 子代理沙箱隔离 | chroot/容器隔离，防止子代理误操作文件系统 |
| OpenTelemetry | Span 导出为 OTLP 格式，对接 Grafana/Jaeger |
| 配置热重载 | 主配置文件变更自动生效（权限引擎已有） |
| 成本预算告警 | 每用户/每会话 Token 预算，超限自动停止 |
| Agent 工作流 | 多 Agent 协作编排（链式/并行/循环） |
| 桌面 TUI | 基于 Bubble Tea 的终端 UI |
