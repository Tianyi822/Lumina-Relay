# AGENTS.md

This file provides guidance to Qoder (qoder.com) when working with code in this repository.
`CLAUDE.md` is a stub that delegates here — all substantive instruction lives in this file.

## 项目概述

Lumina Relay 是一个**端到端加密（E2EE）笔记同步服务**的后端。服务端是"按 hash 存取密文块的哑存储"——**从不接触明文、不解密任何内容**。所有加密/解密在客户端完成；服务端只存密码派生登录公钥、DEK 密文信封、设备级密文 Manifest、同步组关系和按内容 hash 寻址的密文块。

- **Go 1.26**，纯 Go 无 cgo（SQLite 用 `modernc.org/sqlite`）
- HTTP 路由基于 gin，**无 `/v1` 前缀、无 legacy 路由**——不要恢复旧版路由、类型、迁移或密码学标签

核心密码学约定（**改任何一处都会破坏前后端契约**，见 `FRONTEND_INTEGRATION.md`）：
- **哈希统一用 SHA-256，禁止 BLAKE2b**。三处必须一致：写操作签名 `bodyHash`、`blockId` 计算（`sha256(body)`）、`blocks/missing` 查缺 id。
- **登录与同步授权分离**：密码 proof 允许新设备登录并创建空白同步组；六位码只用于永久合并同步组。
- **时间戳单位**：存储/响应字段（`createdAt`/`lastSeenAt`）用 **Unix 秒**；签名头 `X-Timestamp` 用 **Unix 毫秒**。
- **两类签名串必须区分**，别混用：
  - `BuildCanonical`（`internal/auth/signature.go`，设备级请求 PoP）：`method\npath\ntimestamp\nnonce\nhex(sha256(body))`，被 Ed25519 直接签名，路径不含 query。
  - `BuildTranscript`（`internal/auth/transcript.go`，账户生命周期签名：建账号/登录/会话/丢弃同步组、DEK 信封 AAD）：`domain` 原文 + 每字段 `uint32be` 长度前缀拼接，**消除字段边界歧义**。加新签名域必须走 transcript 而非裸拼接。

## 常用命令

```bash
go build ./...                              # 编译
go run .                                    # 启动服务（默认监听 :8443，需先建 ~/.lumina-relay）
go test ./...                               # 全量测试
go test ./internal/handler -run TestABCTransitiveSyncAndBlockIsolation -v  # 单个测试
go test ./internal/db -v                    # 单个包测试
go test -race ./...                         # 竞态检测（限流器/nonce store 等并发逻辑推荐带上）
go vet ./...                                # 静态检查（CI 同款，零容忍）
go mod tidy                                 # 整理依赖

# 压测工具（阶梯并发，采集 QPS/P50/P95/P99）
go run ./cmd/loadtest -target http://localhost:8443 -endpoint health
go run ./cmd/loadtest -target http://localhost:8443 -endpoint discovery
```

仓库无 CI 配置文件；`go vet ./...` + `go test ./...` 即本地 CI。

运行时配置与数据默认落在 `~/.lumina-relay/`（`config.yaml`、`db/relay.db`、`blocks/`、`jwt_secret`、`logs/`）。配置文件不存在时自动写入默认值；可用 `LUMINA_SERVER_PORT` / `LUMINA_SERVER_HOST` 等 env 覆盖（见 `internal/config/loader.go` 的 `applyEnv`）。

## 架构与分层

请求链路：`main.go` → `server.Run`（`net.Listen` + 优雅关闭）→ `handler.NewRouter`（组装 gin 路由 + 中间件）→ `handler` → `service` → `db.Queries`（SQL）+ `store.BlockStore`（文件）。

关键设计点（需读多个文件才能看清的）：

### 依赖注入集中在 `handler.Deps`
所有 service、JWT secret、`*db.Queries`、EventHub 与 ticket store 通过 `handler.Deps` 结构体注入（`internal/handler/router.go`），**不使用全局变量**——便于测试注入与生命周期管理。`main.go` 的 `runServer` 负责把 deps 接好。

### 文件按功能拆分（非旧的单文件大包）
`feat/client-sync-api-alignment` 把 handler/service/auth 按功能拆细了，改代码前先认路径：
- **handler**（`internal/handler/`）：`connection.go`（`/connections/*`、`/session-challenges`、`/sessions`）、`discovery.go`、`manifests.go`、`blocks.go`（`blocks.go` 已含 prune/missing）、`sync.go`（sync-codes、sync-groups/discard-others、devices 列表/吊销）、`events.go`（`/event-tickets` + `/events` WebSocket）、`health.go`、`http.go`（共享写响应 helper）、`router.go`（唯一挂路由处）。
- **service**（`internal/service/`）：`connection.go`/`sync.go`/`manifest.go`/`blocks.go`/`events.go` 一一对应；旧的 `account.go`/`device.go` 已删，其职责并入 `connection.go`+`sync.go`。
- **auth**（`internal/auth/`）：`signature.go`（Ed25519 验签 + `BuildCanonical`）、`transcript.go`（`BuildTranscript` 及各生命周期 transcript）、`challenge.go`（一次性 challenge store，失败也消费防在线猜测）、`secret.go`（JWT 密钥 load-or-generate）、`jwt.go`（HS256 session token 签发/解析）。
- **db**（`internal/db/`）：`queries.go`（所有手写 SQL）、`relay_meta.go`（**Relay instanceId 单例持久化**，`GetOrCreateInstanceID`：32 字节随机 + base64url，`INSERT OR IGNORE` 收敛并发首调）、`db.go`（Open + pragma）、`migrate.go`（golang-migrate）。

### 认证与同步授权（`internal/middleware/`）
路由按用途挂不同中间件组合：
| 层级 | 端点 | 中间件 |
|---|---|---|
| 无认证 | discovery、connection/session challenge、health | `LimitByClientIP`（HashedLimiter）+ body limit |
| 账户数据 | bootstrap、sync-codes、devices、manifests、blocks、prune、sync-groups、event-tickets | `RequireSession` + body limit + `RequireDeviceProof` |
| WebSocket | `/events` | 30 秒单次 event ticket（`Sec-WebSocket-Protocol: lumina-events, ticket.<t>`） |

- `RequireSession` 严格解析绑定 instance/device 的 HS256 JWT，查设备记录，拒绝已吊销设备，并把 `accountId/deviceId/devicePublicKey/syncGroupId` 注入 context。
- `RequireDeviceProof` 校验所有账户数据请求的 `X-Timestamp`(±5min)/`X-Nonce`/`X-Signature`(Ed25519)。验签后才把 nonce 写入 SQLite，重启后仍防重放。**它会读 body 算 canonical，再重建 body**——因此 body limit 必须挂在它之前。
- 该中间件还会**先于一切校验拒绝任何带 query string 或非规范化 RequestURI 的签名请求**（`RawQuery != "" || RequestURI != URL.Path`）——客户端绝不能在已认证端点上拼 query，否则直接 `401 invalid_device_proof`。
- canonical 串格式见上文「两类签名串」；`path` 用 `request.URL.Path`（不含 query），字段顺序/换行必须稳定。
- **认证中间件全在 `internal/middleware/auth.go`**（`RequireSession`/`RequireDeviceProof`），IP/用户名/会话/同步码限流是 `hashed_limiter.go` 的 `HashedLimiter`（**不存原文 key，只存 SHA-256**）。旧的 `session.go`/`signed.go`/`ratelimit.go` 已在 `client-sync-api-alignment` 重构中删除，别再引用。

### 数据层：SQLite + 手写 SQL + 文件分桶
- **驱动是 `modernc.org/sqlite`（纯 Go，无 cgo）**。DSN 自动附加 pragma（`internal/db/db.go`）：
  - `busy_timeout=5000`：写锁竞争等待 5 秒再返回 SQLITE_BUSY（session 每请求一次 UPDATE，并发写依赖此）
  - `journal_mode=WAL`：读写并发，不互相阻塞
  - `foreign_keys=ON`：强制外键约束，防御性兜底
  - `_txlock=immediate`：写事务启动即获取 reservation lock，避免读→写升级时死锁
- **迁移**：`internal/db/migrations/*.up.sql` 经 `go:embed` 内嵌，用 `golang-migrate` 执行（`MigrateUp`，启动时调用，幂等）。当前没有正式客户端和运行时数据库，只保留一份干净的 `000001` 初始 migration，不提供 legacy 数据升级。
- **`db.Queries`**（`internal/db/queries.go`）持有 `sqlx.ExtContext` 接口，所以**同一类型既能基于 `*sqlx.DB`，也能基于事务**（`WithTx` 传入绑定到 tx 的子 Queries）。SQL 全部是包级 `const`，动态值经 `?` 占位符绑定。
- **密文块存储**（`internal/store/blocks.go`）：文件系统按 `id[0:2]/id[0:4]/<id>` 分桶，临时文件 fsync 后用 no-replace hard-link 原子安装。`block_objects` 管物理对象，`account_blocks` 管配额，`device_blocks` 管同步组可见性。
- **Manifest 按设备 CAS**：每台设备只推进自己的 head；组内读取各设备最新密文，由客户端解密后按 HLC LWW 合并。

### 统一错误模型（`internal/apperr/`）
所有业务错误用 `apperr.Error{Code, Message, Extra}`，`Code` 是稳定的 snake_case 字符串，`HTTPStatus()` 做 code→HTTP 映射（未知 code 兜底 500），`WriteJSON` 输出 `{ "error": { "code", "message", <extra> } }`。service 层用哨兵 error，handler 层 `errors.Is` 判定后映射。**改错误码或 HTTP 状态会破坏前端契约**。

### 日志：slog 兜底 → zap
启动顺序严格（`main.go`）：① `logger.Recover()` 兜底 → ② `InitBootstrap()`（slog，立即可用）→ ③ 加载配置（走 slog）→ ④ `InitZap`（成功则 `defer Sync()` flush，失败则降级兜底运行）。所有调用方只依赖包级 `logger.Info/Err/...` 与 `Logger` 接口，对底层后端零感知。

## 测试约定

- **遵循 TDD**：计划与设计文档在 `docs/superpowers/{plans,specs}/`（注意 `/docs/` 在 `.gitignore` 内，本地保留不提交）。新功能先写 plan/spec 再红绿循环。
- handler 集成测试基于 `t.TempDir()` 起真实 SQLite、BlockStore 和全部 service，**不碰 `~/.lumina-relay`**；使用真实 Ed25519 key 和 PoP。
- 时间敏感逻辑（限流器、nonce、邀请码、ticket、块 GC）通过注入可控 `now` 函数解耦，不要依赖真实睡眠。
- 代码注释用**中文**，标识符用英文，保持与现有代码一致。

## 安全约束（改代码时务必保留）

- **`SetTrustedProxies([]string{"127.0.0.1"})`**：仅信任本机反代，否则攻击者可伪造 `X-Forwarded-For` 绕过 IP 限流。部署架构变化（如 K8s pod 网段）需同步调整。
- **请求体大小限制**（`middleware/body_limit.go`）：块上传 1 MiB、JSON 端点 64 KiB、**原始密文 Manifest 4 MiB**。三条独立中间件按路由挂。`HandleBodyReadError` 必须显式写 413——gin 的 ResponseWriter 不实现 stdlib 的 `requestTooLarge` 接口，否则超大上传会静默"成功"(200) 跳过校验。
- **账户存在性按产品要求公开**：`connections/start` 返回 `accountExists`，但同时按 IP 和规范化用户名的不可逆 hash 限流；错误密码不得创建设备。
- **同步码**：仅同账号 active 设备可兑换，5 分钟、单次使用、数据库只存 HMAC；失败按设备限流。
- 跨账户和跨组写操作必须在同一个 SQLite 写事务内复核调用设备仍为 active、仍属于 session 中的账号和同步组；不能只依赖事务外的中间件快照。
