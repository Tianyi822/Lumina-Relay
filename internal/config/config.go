// Package config 负责 lumina-relay 的应用配置加载。
package config

import "lumina-relay/internal/logger"

// AppConfig 是应用全局配置。
// 随业务扩展逐步增加字段（server、storage 等）。
type AppConfig struct {
	Log    logger.LogConfig `yaml:"log"`
	Server ServerConfig     `yaml:"server"`
}

// ServerConfig 是 HTTP 服务配置（占位，后续接入 Gin 时填充）。
type ServerConfig struct {
	Port int    `yaml:"port"` // 默认 8443
	Host string `yaml:"host"` // 默认 "0.0.0.0"
}

// Default 提供全局默认值。每次返回独立实例。
func Default() AppConfig {
	return AppConfig{
		Log:    logger.DefaultConfig(),
		Server: ServerConfig{Port: 8443, Host: "0.0.0.0"},
	}
}
