# go-tiny-claw 项目规范

## 技术栈
- Go 1.25+，追求极致性能，零外部运行时依赖
- 7 层架构：Schema → Provider → Tools → Permissions → Context → Engine → Platform

## 工具使用策略
- 分析远程项目时，优先用 `fetch_url` 读取 README 页面，不要直接 git clone
- 只有需要修改代码或深入分析源码时才 clone 仓库
- 读取单个远程文件用 `fetch_url`，不要用 bash + curl

## 红线规则
- 所有错误信息必须使用中文，绝对禁止英文抛错
- API 返回 JSON 格式，包含 `code` 和 `message` 字段
- 不允许删除根目录的任何文件
- 不允许将密钥（API Key、AppSecret）硬编码到源文件中

## 编码规范
- 新增工具需实现 `BaseTool` 接口（Name + Definition + Execute）
- 新增工具需在 `cmd/claw/main.go` 中注册
- 新增工具需编写单元测试（`*_test.go`）
- 写入操作工具需在 `isWriteTool()` 中标记为写入工具（串行执行）
- 只读工具可参与并行执行（信号量控制并发上限）

## 目录约定
- `cmd/claw/` — CLI 入口，仅负责模式检测和引擎启动
- `internal/` — 所有业务代码，不允许被外部 import
- `.claw/skills/` — SKILL.md 技能定义
- `.claw/permissions.yaml` — 权限规则配置
- `docs/` — 架构设计和构建流程文档
