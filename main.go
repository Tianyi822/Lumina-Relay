package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"lumina-relay/internal/auth"
	"lumina-relay/internal/config"
	"lumina-relay/internal/db"
	"lumina-relay/internal/handler"
	"lumina-relay/internal/logger"
	"lumina-relay/internal/server"
	"lumina-relay/internal/service"
	"lumina-relay/internal/store"
)

const version = "0.1.0"

func main() {
	// 1. panic 兜底：第一行，保证任何 panic 都有记录
	defer logger.Recover()

	// 2. slog 兜底立即就绪（幂等）
	logger.InitBootstrap()
	logger.Info("lumina-relay 启动中", logger.String("version", version))

	// 3. 加载配置文件系统（走 slog 兜底）
	appCfg, err := config.LoadDefault()
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

	// 5. 业务代码：runServer 阻塞至收到终止信号
	runServer(appCfg)
}

// runServer 完成服务接线并阻塞运行，直到收到 SIGINT/SIGTERM。
// 流程：signal context → 建目录 → 迁移 → 打开 DB → 构造 deps → server.Run。
func runServer(cfg config.AppConfig) {
	defer logger.Recover()

	// 信号 context：SIGINT/SIGTERM 触发优雅关闭
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 数据目录
	dbPath, err := config.DefaultDBPath()
	if err != nil {
		logger.Error("解析数据库路径失败，退出", logger.Err(err))
		return
	}
	blocksDir, err := config.DefaultBlocksDir()
	if err != nil {
		logger.Error("解析块目录路径失败，退出", logger.Err(err))
		return
	}

	// 建目录（0700：仅属主可读写执行，见 data-layer spec §2.2）
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		logger.Error("创建数据库目录失败，退出", logger.Err(err))
		return
	}
	if err := os.MkdirAll(blocksDir, 0o700); err != nil {
		logger.Error("创建块目录失败，退出", logger.Err(err))
		return
	}

	// 迁移 schema
	if err := db.MigrateUp(dbPath); err != nil {
		logger.Error("数据库迁移失败，退出", logger.Err(err))
		return
	}
	logger.Info("数据库迁移完成", logger.String("db", dbPath))

	// 打开 DB
	backend, err := db.Open(dbPath)
	if err != nil {
		logger.Error("打开数据库失败，退出", logger.Err(err))
		return
	}
	defer backend.Close()
	q := db.New(backend)

	// Relay instanceId 与数据库绑定并持久化，用于客户端 pinning 与所有 proof。
	instanceID, err := q.GetOrCreateInstanceID(ctx)
	if err != nil {
		logger.Error("初始化 Relay instanceId 失败，退出", logger.Err(err))
		return
	}

	// JWT secret：首次启动随机生成并持久化到 ~/.lumina-relay/jwt_secret，
	// 后续启动复用同一密钥。多用户场景下避免重启全员登出。
	jwtSecretPath, err := config.DefaultJWTSecretPath()
	if err != nil {
		logger.Error("解析 JWT 密钥路径失败，退出", logger.Err(err))
		return
	}
	jwtSecret, err := auth.LoadOrGenerateSecret(jwtSecretPath)
	if err != nil {
		logger.Error("加载或生成 JWT 密钥失败，退出", logger.Err(err))
		return
	}

	blockStore := store.NewBlockStore(blocksDir)
	if err := blockStore.CleanupTempFiles(time.Hour); err != nil {
		logger.Warn("清理遗留块临时文件失败", logger.Err(err))
	}
	challenges := auth.NewChallengeStore(4096)
	eventHub := service.NewEventHub()
	eventTickets := service.NewEventTicketStore()
	blocksService := service.NewBlocksService(q, blockStore)
	if collected, err := blocksService.CollectGarbage(ctx); err != nil {
		logger.Warn("启动时回收过期孤儿块失败", logger.Err(err))
	} else if collected > 0 {
		logger.Info("启动时已回收过期孤儿块", logger.Int("count", collected))
	}
	gcDone := make(chan struct{})
	go func() {
		defer close(gcDone)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				collected, err := blocksService.CollectGarbage(ctx)
				if err != nil {
					logger.Warn("回收过期孤儿块失败", logger.Err(err))
				} else if collected > 0 {
					logger.Info("已回收过期孤儿块", logger.Int("count", collected))
				}
			}
		}
	}()
	defer func() {
		stop()
		<-gcDone
	}()

	// 构造无版本协议依赖。
	deps := handler.Deps{
		ConnectionService: service.NewConnectionService(
			q, challenges, instanceID, jwtSecret, cfg.Storage.QuotaMB),
		SyncService:     service.NewSyncService(q, instanceID, jwtSecret),
		ManifestService: service.NewManifestService(q),
		BlocksService:   blocksService,
		EventHub:        eventHub,
		EventTickets:    eventTickets,
		JWTSecret:       jwtSecret,
		Queries:         q,
		InstanceID:      instanceID,
	}

	logger.Info("HTTP 服务即将监听",
		logger.String("host", cfg.Server.Host),
		logger.Int("port", cfg.Server.Port),
	)

	// 阻塞运行，ctx 取消时优雅关闭
	if err := server.Run(ctx, cfg, deps); err != nil {
		logger.Error("HTTP 服务退出", logger.Err(err))
		return
	}
	logger.Info("lumina-relay 已优雅关闭")
}
