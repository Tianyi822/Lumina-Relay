package logger

import (
	"fmt"
	"os"
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

// ApplyEnvOverrides 用环境变量覆盖配置（env 优先级最高）。
// 调用时 zap 通常未就绪，日志走 slog 兜底（调用方需先 InitBootstrap）。
func ApplyEnvOverrides(cfg LogConfig) LogConfig {
	if v := os.Getenv("LUMINA_LOG_LEVEL"); v != "" {
		Info("日志级别被环境变量覆盖", String("level", v))
		cfg.Level = v
	}
	if v := os.Getenv("LUMINA_LOG_FILE"); v != "" {
		Info("日志文件路径被环境变量覆盖", String("path", v))
		cfg.File.Path = v
		cfg.File.Enabled = true
	}
	if v := os.Getenv("LUMINA_LOG_FILE_ENABLED"); v != "" {
		cfg.File.Enabled = (v == "true" || v == "1")
	}
	return cfg
}
