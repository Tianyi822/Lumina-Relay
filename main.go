package main

import (
	"lumina-relay/internal/config"
	"lumina-relay/internal/logger"
)

const version = "0.1.0"

func main() {
	// 1. panic 兜底：第一行，保证任何 panic 都有记录
	defer logger.Recover()

	// 2. slog 兜底立即就绪（幂等）
	logger.InitBootstrap()
	logger.Info("lumina-relay 启动中", logger.String("version", version))

	// 3. 加载配置文件系统（走 slog 兜底）
	appCfg, err := config.Load("config.yaml")
	if err != nil {
		// 配置解析失败：退出（非"文件不存在"的错误才到这里）
		logger.Error("配置加载失败，退出", logger.Err(err))
		return
	}
	logger.Info("配置加载完成",
		logger.String("level", appCfg.Log.Level),
		logger.Int("port", appCfg.Server.Port),
	)

	// 4. 切换到 zap（从配置取 LogConfig）
	if _, err := logger.InitZap(appCfg.Log); err != nil {
		// zap 失败：降级到兜底运行，不退出
		logger.Error("zap 初始化失败，降级到兜底日志运行", logger.Err(err))
	} else {
		defer logger.Sync() // 正常路径：退出前 flush
		logger.Info("zap 日志系统就绪",
			logger.String("level", appCfg.Log.Level),
			logger.String("file", appCfg.Log.File.Path),
		)
	}

	// 5. 业务代码（未来 Gin/sqlc 接入点）
	runServer(appCfg)
}

func runServer(cfg config.AppConfig) {
	// 子 goroutine 必须各自 defer logger.Recover()
	go func() {
		defer logger.Recover()
		// 后台任务占位...
		_ = cfg
	}()
	// ... Gin server 启动占位 ...
	logger.Info("服务即将就绪（占位，尚未接入 Gin）",
		logger.String("host", cfg.Server.Host),
		logger.Int("port", cfg.Server.Port),
	)
}
