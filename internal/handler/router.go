package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/auth"
	"lumina-relay/internal/db"
	"lumina-relay/internal/middleware"
	"lumina-relay/internal/service"
)

// Deps 是构造路由所需的依赖集合。
// 随 Task 推进增量添加（每个 handler Task 的 Green 阶段补字段）。
//
// 集中在 Deps 而非全局变量，便于测试注入与生命周期管理。
type Deps struct {
	AccountService  *service.AccountService
	DeviceService   *service.DeviceService
	ManifestService *service.ManifestService
	BlocksService   *service.BlocksService
	JWTSecret       []byte           // 签发/解析 session token 的 HS256 密钥
	Queries         *db.Queries      // 数据访问（session 中间件查设备用）
	NonceStore      *auth.NonceStore // 写操作签名防重放
}

// NewRouter 基于 deps 构造 *gin.Engine 并注册全部已实现的路由。
// 限流器在路由层按 sync-design §6.6 配置。
func NewRouter(deps Deps) *gin.Engine {
	r := gin.New()
	r.GET("/health", Health)
	r.POST("/account/register", RegisterAccount(deps))
	// GET /account/dek 限流 10次/分钟/IP（防恢复码爆破，sync-design §696）
	dekLimiter := middleware.NewIPLimiter(10, time.Minute)
	r.GET("/account/dek", middleware.IPLimit(dekLimiter), GetAccountDEK(deps))
	// POST /device/register 限流 5次/分钟/IP（防恢复码爆破，sync-design §697）
	deviceLimiter := middleware.NewIPLimiter(5, time.Minute)
	r.POST("/device/register", middleware.IPLimit(deviceLimiter), RegisterDevice(deps))

	// 写操作：双层认证（Session + Signed）
	// PUT /account/dek 改主密码后换 dekEnvelope（sync-design §590）
	r.PUT("/account/dek",
		middleware.RequireSession(deps.Queries, deps.JWTSecret),
		middleware.RequireSignedWrite(deps.NonceStore),
		PutAccountDEK(deps))
	// DELETE /device/:deviceId 吊销设备（sync-design §591）
	r.DELETE("/device/:deviceId",
		middleware.RequireSession(deps.Queries, deps.JWTSecret),
		middleware.RequireSignedWrite(deps.NonceStore),
		DeleteDevice(deps))
	// GET /manifest 取最新 manifest（Session 认证，sync-design §592）
	r.GET("/manifest",
		middleware.RequireSession(deps.Queries, deps.JWTSecret),
		GetManifest(deps))
	// PUT /manifest 乐观并发提交（Session + Signed，sync-design §593/643）
	r.PUT("/manifest",
		middleware.RequireSession(deps.Queries, deps.JWTSecret),
		middleware.RequireSignedWrite(deps.NonceStore),
		PutManifest(deps))
	// POST /blocks/have 批量查重（Session，sync-design §594/653）
	r.POST("/blocks/have",
		middleware.RequireSession(deps.Queries, deps.JWTSecret),
		BlocksHave(deps))
	// PUT /blocks/:blockId 上传密文块（Session + Signed，sync-design §595/660）
	r.PUT("/blocks/:blockId",
		middleware.RequireSession(deps.Queries, deps.JWTSecret),
		middleware.RequireSignedWrite(deps.NonceStore),
		PutBlock(deps))
	// GET /blocks/:blockId 下载密文块（Session，sync-design §596）
	r.GET("/blocks/:blockId",
		middleware.RequireSession(deps.Queries, deps.JWTSecret),
		GetBlock(deps))
	return r
}

