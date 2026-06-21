# Lumina Relay API 参考

> 面向客户端开发者。本文档覆盖全部已实现端点，可直接据此开发客户端。

- **Base URL**: `http://<host>:8443`（默认端口 8443，HTTP 明文；生产建议前置 TLS 反代）
- **Content-Type**: 除块上传/下载外，均为 `application/json`
- **所有二进制字段**（密文、哈希、公钥）以 **hex 字符串**传输

---

## 目录

1. [认证机制](#认证机制)
2. [端点速查表](#端点速查表)
3. [端点详情](#端点详情)
4. [错误码](#错误码)
5. [客户端集成示例](#客户端集成示例)

---

## 认证机制

系统有三种认证层级，按端点敏感度递增：

| 层级 | 适用端点 | 机制 |
|---|---|---|
| **无认证** | register, health | 无需任何头 |
| **限流** | dek, device/register | 按 IP 限流（见各端点） |
| **Session**（读操作） | get manifest, blocks/have, get block | `Authorization: Bearer <token>` |
| **Session + 签名**（写操作） | put dek, delete device, put manifest, put block | Session + Ed25519 签名头 |

### Session 认证（Bearer JWT）

登录/注册后获得 `sessionToken`（HS256 JWT，24h 有效）。后续请求携带：

```
Authorization: Bearer <sessionToken>
```

token 携带 `accountId` 与 `deviceId`。设备被吊销后 token 立即失效（返回 `device_revoked`）。

### 写操作签名（Ed25519）

所有写操作（PUT/DELETE）在 Session 基础上**额外**要求三个签名头：

| 头 | 说明 |
|---|---|
| `X-Timestamp` | 当前时间（毫秒），允许 ±5 分钟偏差 |
| `X-Nonce` | 随机字符串（防重放，5 分钟内不可重复） |
| `X-Signature` | Ed25519 签名的 hex 编码 |

#### 签名计算（canonical 串）

```
canonical = method + "\n" + path + "\n" + timestamp + "\n" + nonce + "\n" + hex(sha256(body))
```

- `method`: 大写 HTTP 方法（`PUT`, `DELETE` 等）
- `path`: 请求路径（**不含 query string**），如 `/account/dek`、`/blocks/abcd1234...`
- `timestamp`: 与 `X-Timestamp` 头相同的毫秒时间戳字符串
- `nonce`: 与 `X-Nonce` 头相同的值
- `hex(sha256(body))`: 请求体的 sha256 的**小写 hex**；无 body 时对空字节计算

用设备的 Ed25519 **私钥**对 canonical 串签名，hex 编码后放入 `X-Signature`。

> ⚠️ **哈希算法必须统一用 SHA-256，禁止 BLAKE2b。** 写操作签名 `bodyHash`、块上传 `blockId` 计算、`blocks/have` 查重 id 三处均使用 `crypto/sha256`。客户端若用 libsodium `crypto_generichash`（BLAKE2b）会导致验签失败与块上传 `block_hash_mismatch`。

> ⚠️ `path` 必须是**实际请求路径**（含 blockId 等 path 参数），不含 query string。服务端用 `request.URL.Path` 校验。

---

## 端点速查表

| # | 方法 | 路径 | 认证 | 说明 |
|---|---|---|---|---|
| 1 | GET | `/health` | 无 | 健康检查 |
| 2 | POST | `/account/register` | 无 | 注册账户 + 首台设备 |
| 3 | GET | `/account/dek` | 限流 10/min | 读取账户 DEK 信封（支持 accountId 或 recoveryCodeHash 查询） |
| 4 | POST | `/device/register` | 限流 5/min | 添加新设备（需恢复码） |
| 5 | PUT | `/account/dek` | Session + 签名 | 更新 DEK 信封 |
| 6 | DELETE | `/device/:deviceId` | Session + 签名 | 吊销设备 |
| 7 | GET | `/manifest` | Session | 读取当前 manifest |
| 8 | PUT | `/manifest` | Session + 签名 | 提交 manifest（乐观并发） |
| 9 | POST | `/blocks/have` | Session | 批量查重 |
| 10 | PUT | `/blocks/:blockId` | Session + 签名 | 上传密文块 |
| 11 | GET | `/blocks/:blockId` | Session | 下载密文块 |
| 12 | GET | `/devices` | Session | 列出账户下设备 |

---

## 端点详情

### 1. GET /health

健康检查。

**请求**: 无参数、无 body。

**响应** `200`:
```json
{ "status": "ok" }
```

---

### 2. POST /account/register

注册新账户，同时创建首台设备，初始化 manifest（version 0）。

**请求体**:
| 字段 | 类型 | 说明 |
|---|---|---|
| `recoveryCodeHash` | string(hex) | 客户端生成的恢复码哈希（客户端需自行保存原始 recoveryCode） |
| `dekSalt` | string(hex) | DEK 信封盐 |
| `dekNonce` | string(hex) | DEK 信封 nonce |
| `dekCt` | string(hex) | DEK 密文 |
| `devicePubKey` | string(hex) | 首台设备的 Ed25519 公钥（32 字节） |
| `deviceName` | string | 设备名称 |

**响应** `201`:
```json
{
  "accountId": "uuid",
  "deviceId": "uuid",
  "sessionToken": "jwt"
}
```

> ⚠️ **响应不包含 recoveryCode**。客户端必须在注册时本地保存原始 recoveryCode，否则无法添加新设备。

**错误**: `400`（字段缺失/hex 非法）、`500`（内部错误）

---

### 3. GET /account/dek

读取账户的 DEK 信封。用于新设备恢复密钥。支持两种**互斥**查询参数之一：

**查询参数**（二选一，必填其一）:
| 参数 | 类型 | 用途 |
|---|---|---|
| `accountId` | string | 已知 accountId 时直接取 DEK |
| `recoveryCodeHash` | string(hex) | 换设备流程：新设备仅有恢复码，反查 accountId + DEK |

> 同时传两个 / 都不传 → 400。

**响应（accountId 查询）** `200`:
```json
{
  "dekEnvelope": {
    "salt": "hex",
    "nonce": "hex",
    "ct": "hex"
  }
}
```

**响应（recoveryCodeHash 查询）** `200`:
```json
{
  "accountId": "uuid",
  "dekEnvelope": {
    "salt": "hex",
    "nonce": "hex",
    "ct": "hex"
  }
}
```

> `recoveryCodeHash` 分支多返回 `accountId`：换设备流程下一步 `POST /device/register` 需要它。

**错误**: `400`（参数缺失/互斥冲突/hex 非法）、`404`（账户不存在）

**限流**: 10 次/分钟/IP

---

### 4. POST /device/register

为已有账户添加新设备。需提供正确的恢复码哈希。

**请求体**:
| 字段 | 类型 | 说明 |
|---|---|---|
| `accountId` | string | 目标账户 ID |
| `recoveryCodeHash` | string(hex) | 恢复码哈希（与注册时一致） |
| `devicePubKey` | string(hex) | 新设备的 Ed25519 公钥 |
| `deviceName` | string | 设备名称 |

**响应** `200`:
```json
{
  "deviceId": "uuid",
  "sessionToken": "jwt"
}
```

**错误**: `401 bad_recovery_code`（恢复码错误）、`404`（账户不存在）

**限流**: 5 次/分钟/IP

---

### 5. PUT /account/dek

更新账户的 DEK 信封（改主密码场景）。

**前置**: Session + 签名

**请求体**:
| 字段 | 类型 | 说明 |
|---|---|---|
| `dekSalt` | string(hex) | 新盐 |
| `dekNonce` | string(hex) | 新 nonce |
| `dekCt` | string(hex) | 新密文 |

**响应** `204`（无 body）

**错误**: `400`（hex 非法）、`401`（认证失败）

---

### 6. DELETE /device/:deviceId

吊销设备。只能吊销**自己账户名下**的设备。

**前置**: Session + 签名

**路径参数**: `deviceId` — 要吊销的设备 ID

**响应** `204`（无 body）

**错误**: `403`（设备不属于调用者）、`404`（设备不存在/已吊销）、`401`（认证失败）

---

### 7. GET /manifest

读取当前 manifest 版本与密文。

**前置**: Session

**响应** `200`:
```json
{
  "version": 3,
  "ciphertext": "hex"
}
```

> `version = 0` 表示账户尚无任何 manifest（首次同步），`ciphertext` 为空字符串。

---

### 8. PUT /manifest

提交新 manifest。乐观并发：必须声明 `baseVersion`。

**前置**: Session + 签名

**请求体**:
| 字段 | 类型 | 说明 |
|---|---|---|
| `ciphertext` | string(hex) | 新 manifest 密文 |
| `baseVersion` | integer | 客户端基于的版本（GET /manifest 拿到的 version） |

**响应** `200`（成功）:
```json
{ "version": 4 }
```

**响应** `409`（版本冲突）:
```json
{
  "error": {
    "code": "stale_base",
    "message": "base version 过期",
    "currentVersion": 3
  }
}
```

> 冲突时客户端应重新 GET /manifest，合并后用新的 version 重试。

---

### 9. POST /blocks/have

批量查询哪些块服务端已有，返回缺失列表。

**前置**: Session

**请求体**:
| 字段 | 类型 | 说明 |
|---|---|---|
| `ids` | string[] | blockId 数组（每个为 sha256 的 hex，64 字符） |

**响应** `200`:
```json
{
  "missing": ["blockId1", "blockId2"]
}
```

> 单次最多 1000 个 id。

---

### 10. PUT /blocks/:blockId

上传密文块。内容寻址：`blockId` 必须等于 `sha256(body)`。

**前置**: Session + 签名

**路径参数**: `blockId` — 密文内容的 sha256 hex

**请求体**: 原始二进制（`Content-Type: application/octet-stream`），**非 JSON**

**响应** `201`（新建）或 `200`（已存在，幂等）—— 均**无 body**

**错误**: `400 block_hash_mismatch`（sha256(body) ≠ blockId）、`413 quota_exceeded`（配额已满）

---

### 11. GET /blocks/:blockId

下载密文块。

**前置**: Session

**路径参数**: `blockId`

**响应** `200`: 原始二进制（`Content-Type: application/octet-stream`）

**错误**: `404`（块不存在/不属于该账户）

---

### 12. GET /devices

列出当前账户下所有**未吊销**设备。

**前置**: Session

**响应** `200`（数组）:
```json
[
  {
    "deviceId": "uuid",
    "deviceName": "string",
    "devicePubKey": "hex(32字节)",
    "createdAt": 1700000000,
    "lastSeenAt": 1700000000
  }
]
```

> `createdAt` / `lastSeenAt` 为 Unix 秒。`lastSeenAt` 在每次 Session 认证成功时更新。已吊销设备不在列表中。

**错误**: `401`（认证失败）

---

## 错误码

所有错误响应统一格式：

```json
{
  "error": {
    "code": "snake_case_code",
    "message": "人类可读描述",
    "<extra>": "..."
  }
}
```

| code | HTTP | 触发场景 |
|---|---|---|
| `bad_recovery_code` | 401 | 设备注册时恢复码哈希不匹配 |
| `device_revoked` | 401 | 已吊销设备的 sessionToken |
| `stale_base` | 409 | manifest 提交时 baseVersion 过期（附 `currentVersion`） |
| `block_hash_mismatch` | 400 | 上传块时 sha256(body) ≠ blockId |
| `quota_exceeded` | 413 | 超出账户存储配额 |
| `rate_limited` | 429 | 触发限流 |
| `bad_request` | 400 | 请求体格式错误/参数缺失 |
| `unauthorized` | 401 | sessionToken 缺失/无效/签名校验失败 |
| `forbidden` | 403 | 无权操作（如跨账户吊销设备） |
| `account_not_found` | 404 | 账户不存在 |
| `device_not_found` | 404 | 设备不存在/已吊销 |
| `block_not_found` | 404 | 块不存在 |
| `internal_error` | 500 | 内部错误 |

---

## 客户端集成示例

### 完整同步流程

```
1. 生成 Ed25519 密钥对（设备私钥，安全存储）
2. 生成 recoveryCode（客户端本地保存）
3. POST /account/register           → 拿到 accountId, deviceId, sessionToken
4. 加密笔记 → 拆成块
5. POST /blocks/have (ids=[])       → 拿到缺失块列表
6. 对每个缺失块：PUT /blocks/:blockId
7. PUT /manifest (baseVersion=0)    → 提交清单
```

### 换设备流程

```
1. 新设备生成 Ed25519 密钥对
2. 输入原始 recoveryCode
3. GET /account/dek?recoveryCodeHash=<hex(recoveryCode)>  → 拿到 accountId + DEK 信封
4. 用 recoveryCode 解锁 DEK
5. POST /device/register            → 拿到新 deviceId, sessionToken
6. GET /manifest                    → 拿到当前清单，开始同步
```

### 签名计算示例（伪代码）

```python
import hashlib, ed25519

def sign_request(priv_key, method, path, body_bytes):
    timestamp = str(int(time.time() * 1000))
    nonce = secrets.token_hex(16)
    body_hash = hashlib.sha256(body_bytes).hexdigest()

    canonical = f"{method}\n{path}\n{timestamp}\n{nonce}\n{body_hash}"
    signature = ed25519.sign(priv_key, canonical.encode()).hex()

    return {
        "X-Timestamp": timestamp,
        "X-Nonce": nonce,
        "X-Signature": signature,
    }
```

### curl 示例

```bash
# 健康检查
curl http://localhost:8443/health

# 注册账户
curl -X POST http://localhost:8443/account/register \
  -H "Content-Type: application/json" \
  -d '{
    "recoveryCodeHash": "686173686564",
    "dekSalt": "73616c74",
    "dekNonce": "6e6f6e6365",
    "dekCt": "6374",
    "devicePubKey": "a1b2c3...",
    "deviceName": "my-iphone"
  }'

# 读取 DEK（限流端点）
curl "http://localhost:8443/account/dek?accountId=<accountId>"

# 换设备：用恢复码反查 accountId + DEK
curl "http://localhost:8443/account/dek?recoveryCodeHash=<hex>"

# 列出账户下设备（需 session）
curl http://localhost:8443/devices \
  -H "Authorization: Bearer <sessionToken>"

# 读取 manifest（需 session）
curl http://localhost:8443/manifest \
  -H "Authorization: Bearer <sessionToken>"
```
