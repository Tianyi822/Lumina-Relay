# 前端对接说明（feat/client-sync-api-alignment）

> 本文档汇总后端本次改动中**前端必须知晓/修改**的契约差异与安全约束。
> 对应分支 `feat/client-sync-api-alignment`，权威 API 详情见 `api-reference.md`。

---

## 一、前端必须修改的项（阻塞）

### 1. 哈希算法：BLAKE2b → SHA-256

前端 `feat/client-data-sync` 分支用了 libsodium `crypto_generichash`（BLAKE2b）。**后端统一且仅支持 SHA-256**。三处必须改：

| 场景 | 现状（错误） | 应改为 |
|---|---|---|
| 写操作签名 `bodyHash` | `BLAKE2b(body)` | `hex(sha256(body))` |
| `blockId` 计算 | `BLAKE2b(ciphertext)` | `hex(sha256(ciphertext))` |
| `blocks/have` 查重 id | BLAKE2b hex | sha256 hex |

**不改的后果**：所有 PUT/DELETE 验签失败、块上传返回 `block_hash_mismatch`、查重结果错乱。

### 2. 恢复码反查路径：`/account` → `/account/dek`

前端 issue 原文写的是 `GET /account?recoveryCodeHash=<hex>`。**后端没有 `/account` 这个 GET 路由**，实现为：

```
GET /account/dek?recoveryCodeHash=<hex>
```

**不改的后果**：换设备流程第 3 步 404。

响应结构（recoveryCodeHash 分支，比 accountId 分支多一个 `accountId` 字段）：
```json
{
  "accountId": "uuid",
  "dekEnvelope": { "salt": "hex", "nonce": "hex", "ct": "hex" }
}
```

### 3. `recoveryCodeHash` 必须为 32 字节

注册账户（`POST /account/register`）、注册设备（`POST /device/register`）、恢复码反查（`GET /account/dek`）三处的 `recoveryCodeHash` **都必须是 SHA-256 的 32 字节输出（64 个 hex 字符）**。

非 32 字节 → `400 bad_request`。这是防短哈希爆破的硬约束，前端必须用标准 SHA-256 生成恢复码哈希。

### 4. 恢复码失败锁定（新增）

`POST /device/register` 同一账户恢复码**连续失败 5 次**后，该账户锁定 **15 分钟**：
- 锁定期间即使恢复码正确也返回 `429 rate_limited`
- 响应：`{ "error": { "code": "rate_limited", "message": "恢复码尝试过多，请稍后再试" } }`
- 锁定**过期后自动恢复**（失败计数清零，不再因单次失误重锁）
- 成功注册后计数清零

**账户不存在与恢复码错误统一返回 `401 bad_recovery_code`**（防账户存在性枚举）：前端无法区分"账户不存在"和"恢复码错误"，按恢复码错误处理即可。

**前端需处理**：换设备流程收到 429 时，提示用户"尝试过多，请 15 分钟后重试"，不要无限重试。

### 5. `device/register` 状态码：201 → 200（mock 对齐）

后端 `POST /device/register` 返回 **200**（非 201）。前端若用 `response.ok`（200-299）判断不受影响；若 mock 硬断言 `=== 201`，需改为 200。

> 注：`POST /account/register` 仍是 **201**（创建资源），这两个端点状态码不同，别混淆。

---

## 二、时间戳单位约定（易踩坑）

| 字段 | 单位 | 出现位置 |
|---|---|---|
| `createdAt`、`lastSeenAt`（GET /devices） | **Unix 秒**（10 位） | 响应体 |
| `X-Timestamp`（签名头） | **Unix 毫秒**（13 位） | 请求头 |

后端存储/响应的时间戳统一是**秒**。前端不要把 `lastSeenAt`/`createdAt` 当毫秒处理（否则显示成 1970 年）。

---

## 三、新增端点：GET /devices

```
GET /devices
Authorization: Bearer <sessionToken>
```

响应 `200`（数组，不含已吊销设备）：
```json
[
  {
    "deviceId": "uuid",
    "deviceName": "string",
    "devicePubKey": "hex",
    "createdAt": 1700000000,
    "lastSeenAt": 1700000000
  }
]
```

`lastSeenAt` 在每次 Session 认证成功时更新。空账户返回 `[]`（非 `null`）。

---

## 四、请求体大小限制（新增，防 OOM）

| 端点类型 | 上限 | 超限响应 |
|---|---|---|
| 块上传 `PUT /blocks/:blockId` | **1 MiB** | `413 Request Entity Too Large` |
| JSON 端点（register/manifest/blocks-have 等） | **64 KiB** | `413` |

前端单块加密后若超 1 MiB 需自行分块。正常加密块（16-256KB）不受影响。

---

## 五、安全响应头（新增，无需前端处理，仅说明）

后端现在对所有响应附加：
- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`
- `Referrer-Policy: no-referrer`
- `Cache-Control: no-store`（响应不缓存）
- `Strict-Transport-Security`（仅 HTTPS）

原生 App 客户端通常不解析这些头，加着无害。Web 客户端（若有）受益。

---

## 六、已确认一致的项（无需改动）

以下前端实现与后端一致，**不需要改**：
- 注册端点 `POST /account/register` 请求体/响应结构 ✅
- DEK 信封结构（salt/nonce/ct hex）✅
- manifest 端点（GET/PUT）结构 + 乐观并发（baseVersion/stale_base 409）✅
- 签名 canonical 串格式（`method\npath\ntimestamp\nnonce\nbodyHash`）✅
- Ed25519 签名机制（X-Timestamp/X-Nonce/X-Signature 头）✅
- 注册响应不含 recoveryCode（客户端本地生成）✅
- 块上传内容寻址 ✅
- 错误响应格式（`{ error: { code, message } }`）✅

---

## 七、端点速查（最终状态）

| # | 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|---|
| 1 | GET | `/health` | 无 | 健康检查 |
| 2 | POST | `/account/register` | 无 | 注册账户 + 首台设备 |
| 3 | GET | `/account/dek` | 限流 10/min | 读取 DEK（支持 `accountId` 或 `recoveryCodeHash`） |
| 4 | POST | `/device/register` | 限流 5/min | 添加新设备（恢复码失败 5 次锁 15 分钟） |
| 5 | PUT | `/account/dek` | Session + 签名 | 更新 DEK 信封 |
| 6 | DELETE | `/device/:deviceId` | Session + 签名 | 吊销设备 |
| 7 | GET | `/manifest` | Session | 读取当前 manifest |
| 8 | PUT | `/manifest` | Session + 签名 | 提交 manifest（乐观并发） |
| 9 | POST | `/blocks/have` | Session | 批量查重 |
| 10 | PUT | `/blocks/:blockId` | Session + 签名 | 上传密文块（≤1 MiB） |
| 11 | GET | `/blocks/:blockId` | Session | 下载密文块 |
| 12 | GET | `/devices` | Session | 列出账户下设备 |

---

## 八、前端改动清单（Checklist）

- [ ] 三处哈希从 BLAKE2b 改为 SHA-256（签名 bodyHash、blockId、blocks/have id）
- [ ] 恢复码反查路径从 `/account` 改为 `/account/dek`
- [ ] 确保 recoveryCodeHash 是 SHA-256（32 字节 / 64 hex 字符）
- [ ] 处理 `device/register` 的 429（锁定提示）
- [ ] 确认 `device/register` 成功判断用 `response.ok` 而非 `=== 201`
- [ ] 时间戳：`createdAt`/`lastSeenAt` 按秒解析，`X-Timestamp` 按毫秒
- [ ] 块上传单块不超 1 MiB（超出自行分块）
- [ ] 对接新增的 `GET /devices` 设备列表端点
