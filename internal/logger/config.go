package logger

import (
	"fmt"
	"strings"

	"go.uber.org/zap/zapcore"
)

// LogConfig 是日志子系统配置（AppConfig.Log 的类型）。
type LogConfig struct {
	Level string     `yaml:"level"` // "debug"/"info"/"warn"/"error"，默认 "info"
	File  FileConfig `yaml:"file"`
}

// FileConfig 是文件日志配置。
type FileConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Path       string `yaml:"path"`
	MaxSizeMB  int    `yaml:"maxSizeMB"`
	MaxBackups int    `yaml:"maxBackups"`
	MaxAgeDays int    `yaml:"maxAgeDays"`
	Compress   bool   `yaml:"compress"`
}

// DefaultConfig 提供合理默认值，无配置文件也能正常工作。
func DefaultConfig() LogConfig {
	return LogConfig{
		Level: "info",
		File: FileConfig{
			Enabled: true, Path: "logs/lumina-relay.log",
			MaxSizeMB: 100, MaxBackups: 7, MaxAgeDays: 28, Compress: true,
		},
	}
}

// parseLevel 把级别字符串转成 zapcore.Level。空串或未知值回退 info（未知值带 error）。
func parseLevel(s string) (zapcore.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return zapcore.DebugLevel, nil
	case "info", "":
		return zapcore.InfoLevel, nil
	case "warn":
		return zapcore.WarnLevel, nil
	case "error":
		return zapcore.ErrorLevel, nil
	default:
		return zapcore.InfoLevel, fmt.Errorf("未知日志级别 %q，回退到 info", s)
	}
}
