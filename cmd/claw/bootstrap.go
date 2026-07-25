package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	ctxpkg "github.com/lhuan/go-tiny-claw/internal/context"
	"github.com/lhuan/go-tiny-claw/internal/engine"
	"github.com/lhuan/go-tiny-claw/internal/observability"
	"github.com/lhuan/go-tiny-claw/internal/permissions"
	"github.com/lhuan/go-tiny-claw/internal/provider"
	"github.com/lhuan/go-tiny-claw/internal/tools"
)

// BootstrapResult 封装了引擎初始化的全部结果，
// 供 cobra 子命令直接使用，无需重复初始化逻辑。
type BootstrapResult struct {
	Engine         *engine.AgentEngine
	BillingSession *ctxpkg.Session
	Provider       provider.LLMProvider
	Registry       tools.Registry
	Config         *AppConfig
	WorkDir        string
	CancelFunc     context.CancelFunc // 用于停止后台 goroutine（如权限热重载）
}

// Bootstrap 根据配置初始化完整的 Agent 引擎栈。
// 从 main.go 的初始化逻辑提取的公共入口。
func Bootstrap(cfg *AppConfig) (*BootstrapResult, error) {
	// 0. 校验配置
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("配置校验失败: %w", err)
	}

	// 1. 设置代理环境变量
	cfg.SetProxyEnv()
	if cfg.Proxy.HTTP != "" {
		log.Printf("[Bootstrap] 🌐 网络代理已配置: %s", cfg.Proxy.HTTP)
	}

	// 2. 初始化 LLM Provider（含重试装饰器 + 计费追踪）
	rawProvider, err := detectProvider()
	if err != nil {
		return nil, err
	}
	retryProvider := provider.NewRetryableProvider(rawProvider)
	billingSession := ctxpkg.NewSession("global-billing")
	llmProvider := observability.NewCostTracker(retryProvider, cfg.Model.Name, billingSession)

	// 3. 确定工作目录
	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("获取当前工作目录失败: %w", err)
	}

	// 4. 初始化动态权限引擎
	var bootstrapCancel context.CancelFunc
	permConfigPath := filepath.Join(workDir, ".claw", "permissions.yaml")
	permEngine := permissions.NewEngine(permConfigPath)
	if err := permEngine.Load(); err != nil {
		log.Printf("[Bootstrap] ⚠️ 权限配置加载失败，使用默认策略: %v", err)
	} else {
		ctx, cancel := context.WithCancel(context.Background())
		bootstrapCancel = cancel
		go func() {
			defer cancel()
			permEngine.StartHotReload(ctx)
		}()
		log.Printf("[Bootstrap] ✅ 动态权限引擎已启动")
	}

	// 5. 初始化 Tool Registry
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewWriteFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))
	registry.Register(tools.NewBashToolWithPermissions(workDir, permEngine))
	registry.Register(tools.NewTaskOutputTool())
	registry.Register(tools.NewTaskStopTool())
	registry.Register(tools.NewSearchFilesTool(workDir))
	registry.Register(tools.NewFetchURLTool())

	// 6. 创建引擎
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, true, cfg.Model.PlanMode)
	eng.SetMaxContextWindow(cfg.Model.MaxContextWindow)

	// 7. 注册渐进式暴露技能工具 (read_skill)
	skillLoader := eng.SkillLoader()
	registry.Register(tools.NewReadSkillTool(skillLoader))

	// 8. 注册子智能体工具 (spawn_subagent)
	subRegistry := tools.NewRegistry()
	subRegistry.Register(tools.NewReadFileTool(workDir))
	subRegistry.Register(tools.NewBashToolWithPermissions(workDir, permEngine))
	subRegistry.Register(tools.NewReadSkillTool(skillLoader))
	registry.Register(tools.NewSubagentTool(eng, subRegistry, nil))
	registry.Register(tools.NewCheckSubagentTool())

	// 9. 挂载工具执行计时中间件
	registry.UseToolMiddleware(tools.ExecutionTimer())

	// 10. 注入计费 Session
	eng.SetBillingSession(billingSession)

	return &BootstrapResult{
		Engine:         eng,
		BillingSession: billingSession,
		Provider:       llmProvider,
		Registry:       registry,
		Config:         cfg,
		WorkDir:        workDir,
		CancelFunc:     bootstrapCancel,
	}, nil
}
