// internal/provider/config.go
package provider

import (
	"fmt"
	"os"
)

// ProviderConfig 集中管理所有 Provider 的配置项。
// 遵循 "single source of truth" 原则，避免 os.Getenv 分散在各处。
type ProviderConfig struct {
	APIKey  string
	BaseURL string
	Model   string
}

// ProviderOption 是 Functional Option 模式的核心类型。
type ProviderOption func(*ProviderConfig)

// WithAPIKey 设置 API 密钥。
func WithAPIKey(key string) ProviderOption {
	return func(c *ProviderConfig) {
		c.APIKey = key
	}
}

// WithBaseURL 设置自定义 API 端点（代理/中转场景）。
func WithBaseURL(url string) ProviderOption {
	return func(c *ProviderConfig) {
		c.BaseURL = url
	}
}

// WithModel 设置模型名称。
func WithModel(model string) ProviderOption {
	return func(c *ProviderConfig) {
		c.Model = model
	}
}

// loadConfig 从环境变量加载配置，并应用 Functional Options。
// 优先级：Option 显式设置 > 环境变量 > 默认值
//
// envKeys 支持多个环境变量名（逗号分隔），按顺序尝试：
//   - "ANTHROPIC_AUTH_TOKEN,ANTHROPIC_API_KEY" → 先读 AUTH_TOKEN，没有再读 API_KEY
func loadConfig(envKeys string, opts []ProviderOption, defaults ProviderConfig) (*ProviderConfig, error) {
	cfg := &ProviderConfig{
		Model: defaults.Model,
	}

	// 从环境变量加载 API Key（支持多个环境变量名，逗号分隔）
	if envKeys != "" {
		for _, key := range splitEnvKeys(envKeys) {
			if val := os.Getenv(key); val != "" {
				cfg.APIKey = val
				break
			}
		}
	}

	// 应用 Functional Options（覆盖环境变量）
	for _, opt := range opts {
		opt(cfg)
	}

	// 验证必填项
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API key is required: set %s env var or use WithAPIKey option", envKeys)
	}

	return cfg, nil
}

// splitEnvKeys 将逗号分隔的环境变量名拆分为切片。
func splitEnvKeys(keys string) []string {
	var result []string
	start := 0
	for i := 0; i <= len(keys); i++ {
		if i == len(keys) || keys[i] == ',' {
			if start < i {
				result = append(result, keys[start:i])
			}
			start = i + 1
		}
	}
	return result
}
