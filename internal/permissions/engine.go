package permissions

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// =============================================================================
// 类型定义
// =============================================================================

// Action 权限动作类型
type Action string

const (
	ActionAllow Action = "allow" // 允许执行
	ActionAsk   Action = "ask"   // 需要人工审批
	ActionDeny  Action = "deny"  // 直接拒绝
)

// Rule 权限规则
type Rule struct {
	ID       string   `yaml:"id"`
	Pattern  string   `yaml:"pattern"`
	Action   Action   `yaml:"action"`
	Reason   string   `yaml:"reason"`
	Priority int      `yaml:"priority"`
	Tags     []string `yaml:"tags"`
	Enabled  bool     `yaml:"enabled"`

	// 编译后的正则表达式（不序列化）
	regex *regexp.Regexp `yaml:"-"`
}

// Settings 全局配置
type Settings struct {
	DefaultAction Action `yaml:"default_action"` // 默认 allow
	ApprovalTimeout   int    `yaml:"approval_timeout"`
	LogAllCommands    bool   `yaml:"log_all_commands"`
	HotReloadInterval int    `yaml:"hot_reload_interval"`
	ValidateOnLoad    bool   `yaml:"validate_on_load"`
}

// PermissionConfig 权限配置文件结构
type PermissionConfig struct {
	Version    string    `yaml:"version"`
	LastUpdated string   `yaml:"last_updated"`
	Rules      []Rule    `yaml:"rules"`
	Settings   Settings  `yaml:"settings"`
}

// PermissionResult 权限判定结果
type PermissionResult struct {
	Action  Action
	Reason  string
	RuleID  string
	Matched bool // 是否匹配到规则
}

// configSnapshot 配置快照（用于 Copy-on-Write）
type configSnapshot struct {
	config       *PermissionConfig
	sortedRules  []Rule // 预排序的规则
	compiled     bool
}

// =============================================================================
// 权限引擎
// =============================================================================

// Engine 动态权限判定引擎
// 采用 Copy-on-Write 模式：读操作无锁，写操作原子替换
type Engine struct {
	configPath   string
	stopCh       chan struct{}

	// 原子配置指针（Copy-on-Write）
	config       atomic.Pointer[configSnapshot]

	// 统计信息（原子操作）
	totalChecks  atomic.Int64
	allowCount   atomic.Int64
	askCount     atomic.Int64
	denyCount    atomic.Int64

	// 用于热更新的互斥锁（只保护加载过程）
	loadMu       sync.Mutex
}

// NewEngine 创建权限引擎实例
func NewEngine(configPath string) *Engine {
	e := &Engine{
		configPath: configPath,
		stopCh:     make(chan struct{}),
	}
	return e
}

// Load 加载配置文件
// 使用 Copy-on-Write 模式：先解析到临时变量，再原子替换
func (e *Engine) Load() error {
	e.loadMu.Lock()
	defer e.loadMu.Unlock()

	// 1. 读取并解析配置文件（在锁外进行 I/O）
	data, err := readFile(e.configPath)
	if err != nil {
		return fmt.Errorf("读取配置文件失败: %w", err)
	}

	var config PermissionConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 2. 编译正则表达式（在锁外进行 CPU 密集操作）
	if err := compileRules(&config); err != nil {
		return fmt.Errorf("编译规则失败: %w", err)
	}

	// 3. 预排序规则（避免每次 Check 都排序）
	sortedRules := make([]Rule, len(config.Rules))
	copy(sortedRules, config.Rules)
	sort.Slice(sortedRules, func(i, j int) bool {
		return sortedRules[i].Priority > sortedRules[j].Priority
	})

	// 4. 创建配置快照
	snapshot := &configSnapshot{
		config:      &config,
		sortedRules: sortedRules,
		compiled:    true,
	}

	// 5. 原子替换配置指针（无锁操作）
	e.config.Store(snapshot)

	log.Printf("[Permissions] 已加载配置: %s, 规则数: %d", e.configPath, len(config.Rules))
	return nil
}

// compileRules 编译所有规则的正则表达式
func compileRules(config *PermissionConfig) error {
	for i := range config.Rules {
		rule := &config.Rules[i]
		if !rule.Enabled {
			continue
		}

		regex, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return fmt.Errorf("规则 %s 正则编译失败: %w", rule.ID, err)
		}
		rule.regex = regex
	}
	return nil
}

// Check 检查命令权限
// 无锁读取，高性能并发访问
func (e *Engine) Check(ctx context.Context, command string) PermissionResult {
	// 原子递增统计
	e.totalChecks.Add(1)

	// 原子加载配置快照（无锁）
	snapshot := e.config.Load()
	if snapshot == nil || !snapshot.compiled || snapshot.config == nil {
		e.allowCount.Add(1)
		return PermissionResult{
			Action:  ActionAllow,
			Reason:  "权限引擎未初始化，默认放行",
			Matched: false,
		}
	}

	// 使用预排序的规则（避免每次排序）
	for _, rule := range snapshot.sortedRules {
		if !rule.Enabled || rule.regex == nil {
			continue
		}

		if rule.regex.MatchString(command) {
			// 原子更新统计
			switch rule.Action {
			case ActionAllow:
				e.allowCount.Add(1)
			case ActionAsk:
				e.askCount.Add(1)
			case ActionDeny:
				e.denyCount.Add(1)
			}

			return PermissionResult{
				Action:  rule.Action,
				Reason:  rule.Reason,
				RuleID:  rule.ID,
				Matched: true,
			}
		}
	}

	// 没有匹配到规则，使用默认动作
	defaultAction := snapshot.config.Settings.DefaultAction
	switch defaultAction {
	case ActionAllow:
		e.allowCount.Add(1)
	case ActionAsk:
		e.askCount.Add(1)
	case ActionDeny:
		e.denyCount.Add(1)
	}

	return PermissionResult{
		Action:  defaultAction,
		Reason:  "未匹配到任何规则，使用默认策略",
		Matched: false,
	}
}

// StartHotReload 启动热更新监听
func (e *Engine) StartHotReload(ctx context.Context) {
	// 从配置中获取更新间隔
	snapshot := e.config.Load()
	interval := 5 * time.Second
	if snapshot != nil && snapshot.config != nil && snapshot.config.Settings.HotReloadInterval > 0 {
		interval = time.Duration(snapshot.config.Settings.HotReloadInterval) * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastModTime time.Time

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Permissions] 热更新监听已停止")
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			modTime, err := getFileModTime(e.configPath)
			if err != nil {
				log.Printf("[Permissions] 获取文件修改时间失败: %v", err)
				continue
			}

			if modTime.After(lastModTime) {
				log.Printf("[Permissions] 检测到配置文件更新，重新加载...")
				if err := e.Load(); err != nil {
					log.Printf("[Permissions] 热更新失败: %v", err)
				} else {
					lastModTime = modTime
					log.Printf("[Permissions] 热更新成功")
				}
			}
		}
	}
}

// Stop 停止热更新
func (e *Engine) Stop() {
	close(e.stopCh)
}

// GetStats 获取统计信息
func (e *Engine) GetStats() map[string]interface{} {
	snapshot := e.config.Load()
	rulesCount := 0
	compiled := false
	if snapshot != nil {
		rulesCount = len(snapshot.config.Rules)
		compiled = snapshot.compiled
	}

	return map[string]interface{}{
		"total_checks": e.totalChecks.Load(),
		"allow_count":  e.allowCount.Load(),
		"ask_count":    e.askCount.Load(),
		"deny_count":   e.denyCount.Load(),
		"rules_count":  rulesCount,
		"compiled":     compiled,
	}
}
