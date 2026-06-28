package main

import (
	"fmt"
	"log"
	"os"

	"gopkg.in/yaml.v3"
)

// AppConfig 是应用的完整配置，对应 config.yaml 的顶层结构。
type AppConfig struct {
	Server ServerConfig `yaml:"server"`
	Feishu FeishuConfig `yaml:"feishu"`
	Wechat WechatConfig `yaml:"wechat"`
	Model  ModelConfig  `yaml:"model"`
}

// ServerConfig 定义了服务启动的核心参数。
type ServerConfig struct {
	Port int    `yaml:"port"`
	Mode string `yaml:"mode"` // "debug" | "release"
}

// FeishuConfig 定义飞书机器人的连接配置。
type FeishuConfig struct {
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
}

// WechatConfig 定义企业微信机器人的连接配置。
type WechatConfig struct {
	WebhookURL     string `yaml:"webhook_url"`
	Token          string `yaml:"token"`
	EncodingAESKey string `yaml:"encoding_aes_key"`
}

// ModelConfig 定义模型相关的配置，用于自适应压缩决策。
type ModelConfig struct {
	Name             string `yaml:"name"`              // 模型名称，用于计费查找 (如 "glm-4.5-air")
	MaxContextWindow int    `yaml:"max_context_window"` // 模型上下文窗口 Token 数，默认 200000
	PlanMode         bool   `yaml:"plan_mode"`          // 计划模式开关：开启后 Agent 使用 PLAN.md/TODO.md 进行长程任务管理
}

// DefaultConfig 返回一套开箱即用的默认配置。
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Server: ServerConfig{
			Port: 48080,
			Mode: "debug",
		},
		Model: ModelConfig{
			Name:             "glm-4.5-air", // 默认模型，用于计费查找
			MaxContextWindow: 200000,         // 默认 200k Token（适配 Claude Sonnet）
		},
	}
}

// LoadConfig 从指定路径加载 YAML 配置文件。
// 文件不存在时返回默认配置（不报错，保持向后兼容）。
// 环境变量可覆盖 YAML 中的对应字段（env > yaml > 默认值）。
func LoadConfig(path string) (*AppConfig, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[Config] 配置文件 %s 不存在，使用默认配置", path)
		} else {
			return nil, fmt.Errorf("读取配置文件失败: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析配置文件失败: %w", err)
		}
		log.Printf("[Config] 已加载配置文件: %s", path)
	}

	// 环境变量覆盖（优先级最高）
	cfg.applyEnvOverrides()

	return cfg, nil
}

// applyEnvOverrides 用环境变量覆盖配置文件中的值。
// 只在环境变量非空时覆盖，避免清空已有配置。
func (c *AppConfig) applyEnvOverrides() {
	// 飞书
	if v := os.Getenv("FEISHU_APP_ID"); v != "" {
		c.Feishu.AppID = v
	}
	if v := os.Getenv("FEISHU_APP_SECRET"); v != "" {
		c.Feishu.AppSecret = v
	}

	// 微信
	if v := os.Getenv("WECHAT_WEBHOOK_URL"); v != "" {
		c.Wechat.WebhookURL = v
	}
	if v := os.Getenv("WECHAT_TOKEN"); v != "" {
		c.Wechat.Token = v
	}
	if v := os.Getenv("WECHAT_ENCODING_AES_KEY"); v != "" {
		c.Wechat.EncodingAESKey = v
	}

	// 服务器端口
	if v := os.Getenv("PORT"); v != "" {
		var port int
		if _, err := fmt.Sscanf(v, "%d", &port); err == nil && port > 0 {
			c.Server.Port = port
		}
	}

	// 模型上下文窗口
	if v := os.Getenv("MAX_CONTEXT_WINDOW"); v != "" {
		var window int
		if _, err := fmt.Sscanf(v, "%d", &window); err == nil && window > 0 {
			c.Model.MaxContextWindow = window
		}
	}

	// 计划模式
	if v := os.Getenv("PLAN_MODE"); v == "true" || v == "1" {
		c.Model.PlanMode = true
	}
}
