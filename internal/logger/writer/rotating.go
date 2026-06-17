// Package writer 提供日志文件轮转 writer。
// 为避免与 logger 包循环依赖，本包只接收基本类型参数，
// 不 import logger（FileConfig 属于 logger 包，由 logger 转成 RotatingConfig 后传入）。
package writer

import (
	"io"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// RotatingConfig 是 writer 包自己的配置结构（基本类型）。
type RotatingConfig struct {
	Path       string
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	Compress   bool
}

// NewRotatingWriter 预检目录可写性，返回 lumberjack 包装。
// 不可写时返回 error，调用方降级为仅 stderr。
func NewRotatingWriter(cfg RotatingConfig) (io.Writer, error) {
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// 可写性预检：尝试创建/打开文件
	f, err := os.OpenFile(cfg.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	f.Close()

	return &lumberjack.Logger{
		Filename:   cfg.Path,
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   cfg.Compress,
	}, nil
}
