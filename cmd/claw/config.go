package main

import (
	"fmt"
	"log"
)

// ServerConfig 定义了服务启动的核心参数
type ServerConfig struct {
	Host string
	Port int
	Mode string // "debug" | "release"
}

// DefaultConfig 返回一套开箱即用的默认配置
func DefaultConfig() *ServerConfig {
	return &ServerConfig{
		Host: "127.0.0.1",
		Port: 9527,
		Mode: "debug",
	}
}

// Validate 对配置进行合法性校验，自动修正越界值
func (c *ServerConfig) Validate() error {
	if c.Host == "" {
		return fmt.Errorf("host 不能为空")
	}
	if c.Port <= 0 {
		return fmt.Errorf("端口号必须为正整数: %d", c.Port)
	}
	// 端口越界自动钳制
	if c.Port > 65535 {
		log.Printf("[Config] 端口 %d 越界，已自动钳制为 65535", c.Port)
		c.Port = 65535
	}
	// 模式白名单校验
	switch c.Mode {
	case "debug":
		log.Println("[Config] ★ 当前运行在 Debug 模式 —— 千秋万载，一统江湖！")
	case "release":
		log.Println("[Config] 运行在 Release 模式")
	default:
		return fmt.Errorf("未知的运行模式: %s (仅支持 debug/release)", c.Mode)
	}
	return nil
}
