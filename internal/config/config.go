// Package config 负责 lumina-relay 的应用配置加载。
package config

import "lumina-relay/internal/logger"

// AppConfig 是应用全局配置。
// 随业务扩展逐步增加字段（server、storage 等）。
type AppConfig struct {
	Log     logger.LogConfig `yaml:"log"`
	Server  ServerConfig     `yaml:"server"`
	Storage StorageConfig    `yaml:"storage"`
}

// ServerConfig 是 HTTP 服务配置（占位，后续接入 Gin 时填充）。
type ServerConfig struct {
	Port           int      `yaml:"port"`           // 默认 8443
	Host           string   `yaml:"host"`           // 默认 "0.0.0.0"
	TrustedProxies []string `yaml:"trustedProxies"` // 默认 ["127.0.0.1"]；反代所在网段需显式配置，否则 c.ClientIP() 会误信 X-Forwarded-For 或全站共享反代 IP 的限流桶
}

// StorageConfig 是存储相关配置（数据库、密文块、配额）。
type StorageConfig struct {
	QuotaMB int `yaml:"quotaMB"` // 默认 1024
}

// Default 提供全局默认值。每次返回独立实例。
// 日志文件默认写入 ~/.lumina-relay/logs/lumina-relay.log。
func Default() AppConfig {
	logCfg := logger.DefaultConfig()
	if p, err := DefaultLogPath(); err == nil {
		logCfg.File.Path = p
	}
	return AppConfig{
		Log:     logCfg,
		Server:  ServerConfig{Port: 8443, Host: "0.0.0.0", TrustedProxies: []string{"127.0.0.1"}},
		Storage: StorageConfig{QuotaMB: 1024},
	}
}
