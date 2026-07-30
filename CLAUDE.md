# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

本仓库的项目说明、架构约束、测试命令和安全规则统一维护在
[`AGENTS.md`](./AGENTS.md)。开始修改代码前请完整阅读该文件，并以
[`FRONTEND_INTEGRATION.md`](./FRONTEND_INTEGRATION.md) 作为无版本客户端协议契约。

## 常用命令

```bash
go build ./...                              # 编译
go test ./...                               # 全量测试
go test -race ./...                         # 竞态检测（限流器/nonce store 等并发逻辑推荐）
go vet ./...                                # 静态检查（CI 零容忍）
go run .                                    # 启动服务（默认 :8443）
go run ./cmd/loadtest -target http://localhost:8443 -endpoint health  # 压测
```

运行单个测试或单个包的测试：

```bash
go test ./internal/handler -run TestName -v
go test ./internal/db -v
```

## 技术栈

- **Go 1.26**，纯 Go 无 cgo（SQLite 用 `modernc.org/sqlite`）
- HTTP：gin + 手写路由，无 `/v1` 前缀、无 legacy 路由
- DB：SQLite WAL 模式，`golang-migrate` 管理迁移
- 日志：slog 兜底 → zap（启动顺序严格，见 `main.go`）
- 密文块：文件系统按 `id[0:2]/id[0:4]/<id>` 分桶，临时文件 hard-link 原子安装

## 关键设计约束

以下约束跨多个文件才能看清，改代码时务必保留：

### 中间件顺序
- **body limit 必须挂在 `RequireDeviceProof` 之前**。proof 中间件会读 body 算 canonical、再重建 body——若 body limit 在它之后，超大请求会先触发 proof 计算再被拒绝。
- gin 的 `HandleBodyReadError` **必须显式写 413**——gin ResponseWriter 不实现 stdlib `requestTooLarge` 接口，否则超大上传会静默返回 200。
- `SetTrustedProxies([]string{"127.0.0.1"})` 仅信任本机反代；部署架构变化需同步调整。

### 时间单位
- 存储/响应字段（`createdAt`/`lastSeenAt`）用 **Unix 秒**
- 签名头 `X-Timestamp` 用 **Unix 毫秒**
- 时间敏感逻辑通过注入可控 `now` 函数解耦，不依赖真实 sleep

### 哈希统一用 SHA-256
三处必须一致：写操作签名 `bodyHash`、`blockId` 计算（`sha256(body)`）、`blocks/missing` 查缺 id。禁止 BLAKE2b。

### 认证与同步授权分离
密码 proof 允许新设备登录并创建空白同步组；六位码只用于永久合并同步组。

### 依赖注入
所有 service、JWT secret、`*db.Queries`、EventHub 通过 `handler.Deps` 注入，**不使用全局变量**。

## 安全约束

不要恢复 legacy 或 `/v1` 路由、类型、迁移和密码学标签。当前协议的关键原则是：

- 服务端只保存密码学验证材料、密文 DEK envelope、设备级密文 Manifest 与密文块
- 密码登录只创建独立同步组，六位码只负责永久合并同步组
- 所有账户数据 HTTP 请求都需要 session 加 Ed25519 设备 PoP
- 所有内容 hash 与请求 body hash 都使用 SHA-256
- 修改协议、schema 或安全事务时必须同步更新 golden vectors、公开文档和真实 SQLite 集成测试
