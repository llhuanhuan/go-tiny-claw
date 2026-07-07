# 贡献指南

感谢你对 go-tiny-claw 的关注！

## 开发环境

- Go 1.25+
- Git

## 快速开始

```bash
git clone https://github.com/lhuan/go-tiny-claw.git
cd go-tiny-claw
go build ./...
go test ./internal/... -count=1
```

## 代码规范

- 所有错误信息必须使用中文
- API 返回 JSON 格式，包含 `code` 和 `message` 字段
- 新增工具需实现 `BaseTool` 接口并在 `cmd/claw/main.go` 中注册
- 新增工具需编写单元测试

## 提交规范

使用 Conventional Commits 格式：

```
feat: 新增 xxx 功能
fix: 修复 xxx 问题
docs: 更新文档
refactor: 重构 xxx
test: 补充测试
```

## Pull Request

1. Fork 本仓库
2. 创建特性分支：`git checkout -b feat/my-feature`
3. 提交更改：`git commit -m "feat: add my feature"`
4. 推送分支：`git push origin feat/my-feature`
5. 创建 Pull Request

## License

提交 PR 即表示你同意将代码以 MIT License 发布。
