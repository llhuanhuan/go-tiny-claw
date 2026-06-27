package permissions

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"sync"
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
	DefaultAction     Action `yaml:"default_action"`
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

// =============================================================================
// 权限引擎
// =============================================================================

// Engine 动态权限判定引擎
type Engine struct {
	mu           sync.RWMutex
	config       *PermissionConfig
	configPath   string
	compiled     bool
	stopCh       chan struct{}
	
	// 统计信息
	totalChecks  int64
	allowCount   int64
	askCount     int64
	denyCount    int64
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
func (e *Engine) Load() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	data, err := readFile(e.configPath)
	if err != nil {
		return fmt.Errorf("读取配置文件失败: %w", err)
	}

	var config PermissionConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 编译正则表达式
	if err := e.compileRules(&config); err != nil {
		return fmt.Errorf("编译规则失败: %w", err)
	}

	e.config = &config
	e.compiled = true

	log.Printf("[Permissions] 已加载配置: %s, 规则数: %d", e.configPath, len(config.Rules))
	return nil
}

// compileRules 编译所有规则的正则表达式
func (e *Engine) compileRules(config *PermissionConfig) error {
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
func (e *Engine) Check(ctx context.Context, command string) PermissionResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	e.totalChecks++

	if !e.compiled || e.config == nil {
		return PermissionResult{
			Action:  ActionAsk,
			Reason:  "权限引擎未初始化",
			Matched: false,
		}
	}

	// 按优先级排序规则（高优先级先匹配）
	sortedRules := e.getSortedRules()

	// 匹配第一条命中的规则
	for _, rule := range sortedRules {
		if !rule.Enabled || rule.regex == nil {
			continue
		}

		if rule.regex.MatchString(command) {
			// 更新统计
			switch rule.Action {
			case ActionAllow:
				e.allowCount++
			case ActionAsk:
				e.askCount++
			case ActionDeny:
				e.denyCount++
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
	defaultAction := e.config.Settings.DefaultAction
	switch defaultAction {
	case ActionAllow:
		e.allowCount++
	case ActionAsk:
		e.askCount++
	case ActionDeny:
		e.denyCount++
	}

	return PermissionResult{
		Action:  defaultAction,
		Reason:  "未匹配到任何规则，使用默认策略",
		Matched: false,
	}
}

// getSortedRules 获取按优先级排序的规则
func (e *Engine) getSortedRules() []Rule {
	rules := make([]Rule, len(e.config.Rules))
	copy(rules, e.config.Rules)
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority > rules[j].Priority
	})
	return rules
}

// StartHotReload 启动热更新监听
func (e *Engine) StartHotReload(ctx context.Context) {
	interval := 5 * time.Second
	if e.config != nil && e.config.Settings.HotReloadInterval > 0 {
		interval = time.Duration(e.config.Settings.HotReloadInterval) * time.Second
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
	e.mu.RLock()
	defer e.mu.RUnlock()

	return map[string]interface{}{
		"total_checks": e.totalChecks,
		"allow_count":  e.allowCount,
		"ask_count":    e.askCount,
		"deny_count":   e.denyCount,
		"rules_count":  len(e.config.Rules),
		"compiled":     e.compiled,
	}
}
