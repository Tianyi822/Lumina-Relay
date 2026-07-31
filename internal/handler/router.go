package handler

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"lumina-relay/internal/db"
	"lumina-relay/internal/middleware"
	"lumina-relay/internal/service"
)

type Deps struct {
	ConnectionService  *service.ConnectionService
	SyncService        *service.SyncService
	ManifestService    *service.ManifestService
	BlocksService      *service.BlocksService
	SessionFileService *service.SessionFileService
	EventHub           *service.EventHub
	EventTickets       *service.EventTicketStore
	Queries            *db.Queries
	JWTSecret          []byte
	InstanceID         string
	ConnectionLimiter  *middleware.HashedLimiter
	UsernameLimiter    *middleware.HashedLimiter
	SessionLimiter     *middleware.HashedLimiter
	SyncCodeLimiter    *middleware.HashedLimiter
}

func NewRouter(deps Deps) *gin.Engine {
	if deps.EventHub == nil {
		deps.EventHub = service.NewEventHub()
	}
	if deps.EventTickets == nil {
		deps.EventTickets = service.NewEventTicketStore()
	}
	if deps.ConnectionLimiter == nil {
		deps.ConnectionLimiter = middleware.NewHashedLimiter(30, time.Minute)
	}
	if deps.UsernameLimiter == nil {
		deps.UsernameLimiter = middleware.NewHashedLimiter(10, time.Minute)
	}
	if deps.SessionLimiter == nil {
		deps.SessionLimiter = middleware.NewHashedLimiter(30, time.Minute)
	}
	if deps.SyncCodeLimiter == nil {
		deps.SyncCodeLimiter = middleware.NewHashedLimiter(5, 10*time.Minute)
	}

	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	if err := router.SetTrustedProxies([]string{"127.0.0.1"}); err != nil {
		panic(fmt.Errorf("配置可信代理失败：%w", err))
	}
	router.Use(middleware.AccessLog())
	router.Use(middleware.SecurityHeaders())

	router.GET("/health", Health)
	router.GET("/.well-known/lumina-relay", Discovery(deps.InstanceID))

	router.POST("/connections/start",
		middleware.LimitByClientIP(deps.ConnectionLimiter),
		middleware.BodyLimitJSON(),
		StartConnection(deps))
	router.POST("/connections/complete",
		middleware.LimitByClientIP(deps.ConnectionLimiter),
		middleware.BodyLimitJSON(),
		CompleteConnection(deps))
	router.POST("/session-challenges",
		middleware.LimitByClientIP(deps.SessionLimiter),
		middleware.BodyLimitJSON(),
		StartSession(deps))
	router.POST("/sessions",
		middleware.LimitByClientIP(deps.SessionLimiter),
		middleware.BodyLimitJSON(),
		CompleteSession(deps))

	session := middleware.RequireSession(deps.Queries, deps.JWTSecret, deps.InstanceID)
	proof := middleware.RequireDeviceProof(deps.Queries)

	router.GET("/bootstrap",
		session, middleware.BodyLimitJSON(), proof, GetBootstrap(deps))
	router.POST("/sync-codes",
		session, middleware.BodyLimitJSON(), proof, GenerateSyncCode(deps))
	router.POST("/sync-codes/redeem",
		session, middleware.BodyLimitJSON(), proof, RedeemSyncCode(deps))
	router.GET("/devices",
		session, middleware.BodyLimitJSON(), proof, ListDevices(deps))
	router.DELETE("/devices/:deviceId",
		session, middleware.BodyLimitJSON(), proof, RevokeDevice(deps))
	router.POST("/sync-groups/discard-others",
		session, middleware.BodyLimitJSON(), proof, DiscardOtherGroups(deps))

	router.GET("/manifests",
		session, middleware.BodyLimitJSON(), proof, ListManifestHeads(deps))
	router.GET("/manifests/:deviceId/:version",
		session, middleware.BodyLimitJSON(), proof, GetManifest(deps))
	router.PUT("/manifests/self/:baseVersion",
		session, middleware.BodyLimitManifest(), proof, PutOwnManifest(deps))

	router.POST("/blocks/missing",
		session, middleware.BodyLimitJSON(), proof, MissingBlocks(deps))
	router.PUT("/blocks/:blockId",
		session, middleware.BodyLimitBlock(), proof, PutBlock(deps))
	router.GET("/blocks/:blockId",
		session, middleware.BodyLimitJSON(), proof, GetBlock(deps))

	router.GET("/session-files",
		session, middleware.BodyLimitJSON(), proof, ListSessionFiles(deps))
	router.GET("/session-files/:sessionId",
		session, middleware.BodyLimitJSON(), proof, GetSessionFile(deps))
	router.PUT("/session-files/:sessionId/:baseVersion",
		session, middleware.BodyLimitSessionFile(), proof, PutSessionFile(deps))
	router.DELETE("/session-files/:sessionId/:baseVersion",
		session, middleware.BodyLimitJSON(), proof, DeleteSessionFile(deps))

	router.POST("/event-tickets",
		session, middleware.BodyLimitJSON(), proof, CreateEventTicket(deps))
	router.GET("/events", Events(deps))
	return router
}
