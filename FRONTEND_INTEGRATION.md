# Lumina Relay 客户端协议与 API 接口文档

> 面向客户端团队的唯一接入文档：完整覆盖认证机制、全部 REST/WebSocket 接口、安全约束、业务流程与错误处理。内容以当前服务端实现为准，与 `AGENTS.md` 的安全约束保持一致。
>
> 适用范围：无版本前缀的当前协议（无 `/v1`、无 legacy 路由）。

---

## 目录

1. [系统概述与基础约定](#1-系统概述与基础约定)
2. [服务发现（Discovery）](#2-服务发现discovery)
3. [认证机制](#3-认证机制)
4. [连接管理 API](#4-连接管理-api)
5. [同步组管理 API](#5-同步组管理-api)
6. [Manifest 管理 API](#6-manifest-管理-api)
7. [块存储 API](#7-块存储-api)
8. [会话文件 API](#8-会话文件-api)
9. [WebSocket 事件](#9-websocket-事件)
10. [安全约束与限制](#10-安全约束与限制)
11. [典型业务流程](#11-典型业务流程)
12. [错误处理](#12-错误处理)
13. [客户端数据映射与对接契约清单](#13-客户端数据映射与对接契约清单)

---

## 1. 系统概述与基础约定

### 1.1 端到端加密原则

Lumina Relay 是端到端加密（E2EE）笔记同步服务的后端。服务端是"按 hash 存取密文的哑存储"：

- **服务端从不接触明文、从不解密任何内容**；
- 所有加密/解密在客户端完成；
- 服务端只保存：密码派生登录公钥、DEK 密文信封、设备级密文 Manifest、会话密文快照、同步组关系、按内容 hash 寻址的密文块。

产品交互上，设置页只需要 **Relay 地址、用户名和密码**三项输入；注册与登录由 `accountExists` 自动分流，页面不需要让用户选择。

### 1.2 基础约定

| 约定 | 说明 |
|---|---|
| 路由前缀 | 无版本前缀，直接挂根路径（如 `/bootstrap`） |
| JSON 编码 | 请求/响应均为 `application/json`；字段严格匹配定义，**不接受未知字段、重复字段** |
| 二进制字段 | 一律使用**无 padding 的规范 base64url**（RFC 4648 URL-safe，去掉 `=`） |
| 原始密文 | Manifest / block / 会话快照的下载响应为 `application/octet-stream`，上传请求体即密文原始字节 |
| ID 格式 | `accountId` / `deviceId` / `syncGroupId` 为客户端或服务端生成的规范 UUID 字符串 |
| 哈希 | **统一 SHA-256，禁止 BLAKE2b**（详见 [§10.1](#101-哈希规范)） |
| 时间戳 | 存储/响应字段（`createdAt`/`lastSeenAt`/`updatedAt`/`expiresAt`）为 **Unix 秒**；`serverTimeMs`/`expiresAtMs`/`X-Timestamp` 为 **Unix 毫秒**（详见 [§10.2](#102-时间戳单位)） |
| Query 参数 | **所有已认证接口禁止携带 query string**，否则直接 `401 invalid_device_proof` |
| 签名算法 | Ed25519（登录 key、account-auth key、设备 key 三类，不得复用） |

### 1.3 接口鉴权分层总览

| 层级 | 端点 | 鉴权要求 |
|---|---|---|
| 无认证 | `GET /health`、`GET /.well-known/lumina-relay` | 无 |
| 无认证 + 限流 | `POST /connections/start`、`POST /connections/complete`、`POST /session-challenges`、`POST /sessions` | IP/用户名限流 + body limit |
| 账户数据 | `/bootstrap`、`/sync-codes*`、`/devices*`、`/sync-groups/*`、`/manifests*`、`/blocks*`、`/session-files*`、`/event-tickets` | `Bearer` 会话 Token + 设备证明签名（PoP） |
| WebSocket | `GET /events` | 30 秒单次 event ticket |

---

## 2. 服务发现（Discovery）

### 2.1 `GET /.well-known/lumina-relay`

客户端接入的**第一个调用**。用于：

1. 固定（pin）`instanceId` —— 后续所有 transcript 签名都绑定该值；
2. 用 `serverTimeMs` 校准本地时钟与 HLC；
3. 读取 capabilities 与各类大小限制。

**响应 `200 OK`：**

```json
{
  "protocol": "lumina-relay",
  "instanceId": "persistent-random-id",
  "serverTimeMs": 1784304000000,
  "capabilities": [
    "password-proof",
    "device-proof",
    "sync-groups",
    "device-manifests",
    "session-files",
    "websocket-events"
  ],
  "limits": {
    "maxJsonBytes": 65536,
    "maxManifestBytes": 4194304,
    "maxSessionFileBytes": 4194304,
    "maxBlockBytes": 1048576,
    "maxMissingIds": 900,
    "maxDeviceNameBytes": 128,
    "blockGcGraceSeconds": 86400
  }
}
```

Relay 尚未初始化时返回 `503`，错误码 `relay_not_initialized`。

### 2.2 `GET /health`

存活探测，无依赖检查。响应 `200 {"status": "ok"}`。

---

## 3. 认证机制

认证体系分三层，客户端必须全部正确实现：

1. **账户生命周期签名（Transcript）**：注册 / 登录 / 会话续期 / 放弃同步组时，用对应私钥对结构化 transcript 做 Ed25519 签名；
2. **会话 Token（JWT）**：连接或会话完成后服务端签发的 Bearer Token，24 小时有效；
3. **每请求设备证明（PoP）**：所有账户数据请求额外携带 Ed25519 签名头，防重放。

### 3.1 密码派生

密码取 UTF-8 字节，**不做 Unicode normalization**。固定 KDF 参数（与 `/connections/start` 返回的 `kdf` 一致）：

```text
passwordRoot = Argon2id(
  password,
  authSalt,           // start 响应中的 base64url 16 字节
  memory      = 65536 KiB,
  iterations  = 3,
  parallelism = 1,
  output      = 32 bytes
)
```

再用 HKDF-SHA256 做域分离（HKDF 的 salt 为**零长度空值**，不是 `authSalt`——Argon2id 已使用该 salt）：

```text
loginSeed   = HKDF(passwordRoot, info="lumina-login-ed25519",     32)
envelopeKey = HKDF(passwordRoot, info="lumina-dek-envelope-key",  32)
```

- `loginSeed` 只用于构造 **Ed25519 登录 key**；
- `envelopeKey` 只用于 **XChaCha20-Poly1305** 包裹随机 32 字节 DEK。

**DEK envelope 唯一字节格式（解码后必须恰好 72 字节）：**

```text
24-byte random nonce || Seal(DEK, aad)   // 32 字节密文 + 16 字节 tag
```

AAD 使用 §3.2 相同的长度前缀编码：

```text
lumina-dek-envelope:
  instanceId, normalizedUsername, accountId, authSalt
```

DEK 再独立派生 account-auth Ed25519 seed：

```text
accountAuthSeed = HKDF-SHA256(DEK, salt=empty,
                              info="lumina-account-auth-ed25519", 32)
```

**登录 key、account-auth key、设备 key 三类不得复用。**

### 3.2 Transcript 签名（账户生命周期）

所有 transcript 都按以下方式编码——**不能使用 JSON、分隔符拼接或字段排序**：

```text
UTF8(domain) || repeat( uint32be(byteLength(field)) || fieldBytes )
```

即：domain 原文，之后每个字段先写 4 字节大端长度，再写字段字节，消除字段边界歧义。字段顺序固定：

```text
lumina-account-create:
  instanceId, attemptId, challenge, normalizedUsername, accountId,
  authSalt, loginPublicKey, accountAuthPublicKey, sha256(dekEnvelope),
  deviceId, deviceName, devicePublicKey

lumina-login-proof:
  instanceId, attemptId, normalizedUsername, challenge,
  deviceId, deviceName, devicePublicKey

lumina-device-session:
  instanceId, attemptId, challenge, deviceId

lumina-discard-sync-groups:
  instanceId, accountId, deviceId, groupId, uint64be(groupRevision)
```

**签名 key 对应关系：**

| 场景 | Transcript | 签名者 |
|---|---|---|
| 注册（`connections/complete`） | `lumina-account-create` | login key（`loginProof`）、account-auth key（`accountProof`）、新设备 key（`deviceProof`）**各签一次** |
| 已有账号登录 | `lumina-login-proof` | login key（`loginProof`）、新设备 key（`deviceProof`） |
| 会话续期（`/sessions`） | `lumina-device-session` | 已保存的设备 key |
| 放弃其他同步组 | `lumina-discard-sync-groups` | account-auth key |

注意：

- `challenge` 字段为服务端下发的**原始 32 字节**（客户端从 base64url 解码后拼入 transcript）；
- discard transcript 的 `groupId`/`groupRevision` 取**最新 bootstrap** 的 `syncGroupId`/`groupRevision`。组合并后必须刷新 bootstrap，不能用合并前的值构造证明，否则返回 `409 group_changed`。

### 3.3 Challenge 机制

`/connections/start` 与 `/session-challenges` 返回一次性 challenge：

- 有效期 **5 分钟**（响应 `expiresAt` 为 Unix 秒）；
- **单次使用，验证失败也会消费**（防在线猜测），失败后必须重新获取；
- `attemptId` 标识本次尝试，complete 时原样提交。

### 3.4 会话 Token（JWT）

连接完成或会话完成后，响应中的 `session` 对象：

```json
{
  "token": "opaque-jwt",
  "expiresAt": 1784390400,
  "proofBinding": "opaque-jti"
}
```

- HS256 签名的 JWT，**有效期 24 小时**，`expiresAt` 为 Unix 秒；
- Token 绑定 Relay instance 与设备，客户端将其视为**不透明字符串**，不要解析内容；
- 所有账户数据请求携带 `Authorization: Bearer <token>`；
- 服务端每次请求都会核对设备记录：**已吊销设备立即返回 `401 device_revoked`**，即使 Token 未过期；
- Token 过期或失效后，用已保存的设备私钥走 `/session-challenges` → `/sessions` 续期，**不需要密码**。

### 3.5 每请求设备证明（PoP）

除 discovery、health、connection、session-challenge 四类外，**所有 HTTP 请求**同时需要以下请求头：

```http
Authorization: Bearer <session token>
X-Timestamp: <Unix 毫秒>
X-Nonce: <base64url 编码的 16~32 随机字节>
X-Signature: <base64url Ed25519 签名>
```

**canonical 待签名串**（`BuildCanonical`，用设备私钥直接签名）：

```text
UPPER(method) + "\n" +
path + "\n" +
timestamp + "\n" +
nonce + "\n" +
hex(sha256(exactBodyBytes))
```

严格规范：

- `path` 为请求路径，**不含 query**；已认证接口**不得携带任何 query string**，否则先于一切校验直接 `401 invalid_device_proof`；
- `timestamp` / `nonce` 使用请求头中的**原始字符串**（timestamp 必须是规范十进制整数，无前导零/符号）；
- 无请求体时对**空字节串**求 SHA-256；有 JSON 体时**先序列化成最终字节，对同一字节计算签名并原样发送**——签名后不得再改动一个字节；
- 服务端接受 **±5 分钟**时钟偏差；
- nonce 为 base64url 编码的 16~32 字节随机值，**验签通过后持久占用（SQLite 存储，重启后仍防重放）**，同一设备重复 nonce 会被拒绝。

失败一律返回 `401 invalid_device_proof`，不区分具体原因。

---

## 4. 连接管理 API

### 4.1 `POST /connections/start`

设置页提交用户名，开启连接尝试。**限流**：按客户端 IP 30 次/分钟 + 按规范化用户名 hash 10 次/分钟。

**请求：**

```json
{ "username": "alice" }
```

用户名限定 3–64 个 ASCII 字母、数字、点、下划线和连字符，服务端统一转小写。

**响应 `200 OK`：**

```json
{
  "accountExists": true,
  "attemptId": "base64url",
  "challenge": "base64url-32-bytes",
  "authSalt": "base64url-16-bytes",
  "expiresAt": 1784304300,
  "kdf": {
    "name": "argon2id",
    "memoryKiB": 65536,
    "iterations": 3,
    "parallelism": 1,
    "outputBytes": 32
  }
}
```

`accountExists` 决定客户端走注册还是登录分支：

- `false`：客户端生成 accountId、随机 DEK、密码派生登录 key、DEK envelope、account-auth key 和设备 key，走注册；
- `true`：客户端用返回的 `authSalt` 从密码派生登录 key，签署 challenge 与新设备身份，走登录。

> 账户存在性按产品要求公开，但服务端同时按 IP 和用户名 hash 限流；**错误密码绝不会创建设备**。

### 4.2 `POST /connections/complete`

**注册和登录共同字段：**

```json
{
  "attemptId": "...",
  "deviceId": "client-generated-canonical-uuid",
  "deviceName": "auto-derived name",
  "devicePublicKey": "base64url-32-bytes",
  "loginProof": "base64url-64-bytes",
  "deviceProof": "base64url-64-bytes"
}
```

**仅注册时额外提交：**

```json
{
  "accountId": "client-generated-canonical-uuid",
  "loginPublicKey": "base64url-32-bytes",
  "accountAuthPublicKey": "base64url-32-bytes",
  "dekEnvelope": "base64url-72-bytes",
  "accountProof": "base64url-64-bytes"
}
```

**响应 `200 OK`：**

```json
{
  "accountExists": true,
  "session": {
    "token": "opaque-jwt",
    "expiresAt": 1784390400,
    "proofBinding": "opaque-jti"
  },
  "bootstrap": {
    "accountId": "uuid",
    "username": "alice",
    "deviceId": "uuid",
    "dekEnvelope": "base64url",
    "accountAuthPublicKey": "base64url-32-bytes",
    "cryptoStateRevision": 1,
    "dekEpoch": 1,
    "syncGroupId": "uuid",
    "groupRevision": 1,
    "hasOtherSyncData": true,
    "serverTimeMs": 1784304000000
  }
}
```

关键语义：

- **每次密码登录得到的设备都处于新的空白同步组**；`hasOtherSyncData` 只提示"该账号其他同步组存在可同步数据（Manifest / block / 会话快照任意一类）"，不会自动读取旧数据；
- 并发抢注同一用户名返回 `409 account_became_existing`，客户端应重新调用 start 走登录分支；
- 密码或 proof 错误返回 `401 invalid_credentials`。

### 4.3 `POST /session-challenges`

日常启动时优先使用安全存储中的 deviceId 和设备私钥续期。**限流**：按 IP 30 次/分钟。

**请求：** `{ "deviceId": "uuid" }`

**响应 `200 OK`：**

```json
{
  "attemptId": "...",
  "challenge": "base64url-32-bytes",
  "expiresAt": 1784304300
}
```

### 4.4 `POST /sessions`

用设备私钥签名 `lumina-device-session` transcript 后提交。**限流**：按 IP 30 次/分钟。

**请求：**

```json
{ "attemptId": "...", "signature": "base64url-64-bytes" }
```

**响应 `200 OK`：** 与 `connections/complete` 相同的 `{ accountExists, session, bootstrap }` 结构。

### 4.5 `GET /bootstrap`

**鉴权**：Bearer + PoP。刷新当前设备的 bootstrap 根状态（组合并、discard 后必须调用）。

**响应 `200 OK`：** 与上文 `bootstrap` 对象结构完全一致（不含 `session` 包装）。

---

## 5. 同步组管理 API

以下接口均要求 Bearer + PoP。核心原则：**登录与同步授权分离**——密码只允许新设备登录进入空白组，六位同步码才能永久合并同步组。

### 5.1 `POST /sync-codes`

生成六位同步码。**限流**：按设备 5 次/10 分钟（生成与兑换共享该配额）。

**请求：** 空 body。

**响应 `201 Created`：**

```json
{ "code": "123456", "expiresAt": 1784304300 }
```

- 有效期 **5 分钟**、**单次使用**；
- 数据库只存 HMAC，不存原文。

### 5.2 `POST /sync-codes/redeem`

同账号另一台 active 设备兑换六位码，**两个同步组永久合并**。**限流**：按设备 5 次/10 分钟。

**请求：** `{ "code": "123456" }`

**响应 `200 OK`：**

```json
{
  "joined": true,
  "syncGroupId": "canonical-group-uuid",
  "groupRevision": 4
}
```

合并语义：

- A 邀请 B 后，A/B 均可读取双方全部数据（Manifest、block、会话快照）；
- A 或 B 均可继续邀请 C（合并可传递）；
- 双方会话快照全部保留并对组内可见；
- 合并成功后向受影响设备广播 `sync_group_merged` 事件；
- **客户端必须随后刷新 `GET /bootstrap`**，获取新的 `syncGroupId`/`groupRevision`。

错误：

- 兑换码无效/过期/非同账号：`401 invalid_sync_code`；
- 两设备已在同一组：`409 already_joined`（附 `groupRevision`）。

### 5.3 `GET /devices`

列出当前同步组设备。

**响应 `200 OK`：**

```json
{
  "devices": [
    {
      "deviceId": "uuid",
      "deviceName": "MacBook Pro",
      "createdAt": 1784300000,
      "lastSeenAt": 1784304000,
      "status": "active"
    }
  ]
}
```

时间字段为 Unix 秒。

### 5.4 `DELETE /devices/:deviceId`

吊销当前组内的指定设备。

**响应 `200 OK`：** `{ "revoked": true }`（目标不存在或已吊销时为 `false`）。

吊销成功后向组内设备广播 `device_revoked` 事件；被吊销设备的后续请求返回 `401 device_revoked`。

### 5.5 `POST /sync-groups/discard-others`

永久放弃当前组以外的其他同步组（含其全部设备、Manifest、block 引用与会话快照）。需要 account-auth key 签名的 discard transcript（见 §3.2）。

**请求：**

```json
{
  "groupRevision": 3,
  "accountProof": "base64url-64-bytes"
}
```

**响应 `200 OK`：**

```json
{
  "discardedDevices": 1,
  "reclaimedBytes": 1234
}
```

- `reclaimedBytes` 包含被放弃组释放的 block 配额与**会话快照配额**；
- `groupRevision` 与当前组不一致（如组刚合并过）返回 `409 group_changed`，客户端刷新 bootstrap 后重试；
- 被吊销的设备各广播一条 `device_revoked` 事件；
- 由此产生的物理孤儿块仍保留 `blockGcGraceSeconds`（24 小时）宽限期后由后台 GC 回收。

---

## 6. Manifest 管理 API

Manifest 是**设备级密文文档**：每台设备只推进自己的 head（CAS），组内互相读取后由客户端解密并按 HLC LWW 合并。服务端不解析内容。

### 6.1 `GET /manifests`

列出当前组各设备最新 head 与组 revision。

**响应 `200 OK`：**

```json
{
  "groupRevision": 3,
  "heads": [
    {
      "deviceId": "uuid",
      "currentVersion": 8,
      "updatedAt": 1784304000
    }
  ]
}
```

### 6.2 `GET /manifests/:deviceId/:version`

下载指定设备指定版本的密文 Manifest。

- **响应 `200 OK`**：`application/octet-stream` 密文字节；响应头 `ETag: "<deviceId>:<version>"`；
- `version` 非正整数：`400 bad_request`；
- 不存在或不属于当前组：`404 manifest_not_found`。

### 6.3 `PUT /manifests/self/:baseVersion`

上传**调用设备自己**的密文 Manifest，设备级 CAS。

- 请求体：密文原始字节，上限 **4 MiB**；
- `baseVersion=0` 表示首次创建，否则必须等于当前版本；
- **响应 `200 OK`**：

```json
{ "version": 8, "idempotent": false }
```

（`idempotent=true` 表示重复提交了完全相同的内容，版本未推进。）

- CAS 冲突 `409`：

```json
{
  "error": {
    "code": "stale_manifest",
    "currentVersion": 7
  }
}
```

客户端应 `GET /manifests` + 拉取新版本，合并后以新 `currentVersion` 为 base 重传。

成功后向组内设备广播 `manifest_updated` 事件。

### 6.4 Manifest 明文契约（客户端侧约定）

明文在客户端加密前组织，最低契约：

```json
{
  "deviceId": "uuid",
  "deviceVersion": 12,
  "entries": [
    {
      "key": "logical-item-id-or-path",
      "hlc": { "physicalMs": 1784304000000, "logical": 2 },
      "writerDeviceId": "uuid",
      "operationId": "uuid",
      "tombstone": false,
      "blocks": ["64-char-sha256"],
      "size": 1234
    }
  ]
}
```

整个对象加密后上传，服务端看不到上述字段。合并规则：按元组 `(hlc.physicalMs, hlc.logical, writerDeviceId, operationId)` 字典序取最后修改。HLC 用 discovery/bootstrap 的 `serverTimeMs` 校准。**删除必须写 tombstone**，不能用“条目消失”表示；tombstone 保留在最新快照中，防止长期离线设备让数据复活。

### 6.5 内容类型与同步通道的映射

服务端是**内容无关**的密文存储，只提供两条数据通道，**不存在也不会新增“配置同步”等内容专用接口**：

| 通道 | 适用内容 | 说明 |
|---|---|---|
| **Manifest + blocks**（§6 + §7） | 除会话外的一切内容：论文/附件、批注、知识库、**客户端配置（如 config.json、API key、SSH 连接配置）**、使用统计等 | 每项内容作为一个 manifest entry（`key` 由客户端自定义命名，如 `config/settings` 或文件路径），内容切块加密后按 `blocks` 引用；服务端无法区分条目是配置还是论文 |
| **会话快照**（§8） | 仅会话数据 | 唯一拥有专用 API 的内容类型（整文件密文 CAS，不走分块） |

客户端实现配置等其他内容的同步时注意：

- `key` 命名空间由客户端自行规划（建议按前缀分类，如 `papers/…`、`knowledge/…`、`config/…`），服务端不感知、不校验；
- 高敏感内容（API key、SSH 凭证）与普通内容共用同一套 E2EE 通道，加密强度一致；是否额外套一层客户端侧加密（如 safeStorage）由客户端自决，与本协议无关；
- 配置类小条目同样遵循 HLC LWW 合并与 tombstone 删除规则，不要为其发明单独的冲突策略；
- 服务端自身的运行时配置（`~/.lumina-relay/config.yaml`）是 Relay 部署配置，与客户端数据同步无关，不在本文档范围内。

---

## 7. 块存储 API

密文块按内容寻址：`blockId = hex(sha256(ciphertextBytes))`，64 个小写十六进制字符。

### 7.1 `POST /blocks/missing`

查询哪些块需要上传。

**请求：**

```json
{ "ids": ["64-char-sha256", "..."] }
```

每次最多 **900 个 ID**（discovery 的 `maxMissingIds`；受 64KiB JSON body limit 约束）。

**响应 `200 OK`：**

```json
{ "missing": ["64-char-sha256"] }
```

返回当前组不可见、需要上传的 ID 子集。

### 7.2 `PUT /blocks/:blockId`

上传密文块。请求体即密文字节，上限 **1 MiB**。

- `blockId` 必须等于 `hex(sha256(body))`，不匹配返回 `400 block_hash_mismatch`；
- **响应**：新建 `201 {"created": true}`；已存在（对当前组可见）幂等返回 `200 {"created": false}`；
- 超配额：`413 quota_exceeded`；并发上传同一块冲突：`409 block_busy`（稍后重试）。

### 7.3 `GET /blocks/:blockId`

下载当前组可见的块。

- **响应 `200 OK`**：`application/octet-stream` 密文字节；
- 不可见或不存在：`404 block_not_found`——同账号但未合并的组**不能读取或探测**对方的块。

### 7.4 块回收说明

当前**不公开**通用块 prune 端点（避免在"已上传 block、尚未提交引用它的 Manifest"窗口内误删）。配额释放的途径：

- `POST /sync-groups/discard-others` 释放被放弃组的配额；
- 后台 GC 只删除无账号引用且无上传预留的物理块，且保留 `blockGcGraceSeconds`（24 小时）宽限期。

---

## 8. 会话文件 API

会话数据以**整文件密文快照**形式同步，服务端存 SQLite BLOB、从不解析内容；快照结构、增量合并、去重全部是客户端职责。**不存在 append/index 路由**（`POST /session-files/:sessionId/append/*`、`GET|PUT /session-files-index*` 均为 404）。

基础规则：

- `sessionId` 由客户端生成，必须匹配 `^session-[0-9]{1,16}-[a-z0-9]{1,32}$` 且总长不超过 64 字节，否则 `400 invalid_session_id`；
- `sessionId` **账号内唯一**：PUT 时若被同账号其他同步组占用返回 `409 session_id_conflict`；GET 对其他组占用的 ID 视为不存在（`404 session_file_not_found`），DELETE 视为幂等成功（`200 {"deleted": false}`）；
- 单个快照密文上限 **4 MiB**（discovery 的 `maxSessionFileBytes`），超限 `413 body_too_large`；密文大小计入账号配额，超配额 `413 quota_exceeded`；
- 所有写操作在**单个 SQLite 事务**内完成设备复核、CAS 与配额调整。

### 8.1 `GET /session-files`

列出当前同步组全部快照元数据（按 `sessionId` 排序，`updatedAt` 为 Unix 秒）。

**响应 `200 OK`：**

```json
{
  "sessions": [
    {
      "sessionId": "session-1753857600000-a1b2c3",
      "version": 2,
      "size": 9,
      "updatedAt": 1784304000
    }
  ]
}
```

### 8.2 `GET /session-files/:sessionId`

下载完整会话密文快照。

- **响应 `200 OK`**：`application/octet-stream` 密文字节；当前版本在响应头 **`X-Session-File-Version`**；
- 不存在（含被其他组占用）：`404 session_file_not_found`。

### 8.3 `PUT /session-files/:sessionId/:baseVersion`

上传完整密文快照（请求体即密文字节，**非空**）。

- `baseVersion=0` 表示创建；覆盖时必须携带当前版本，服务端在单个 SQLite 事务内 CAS 递增版本；
- **响应 `200 OK`**：

```json
{ "version": 2, "size": 9 }
```

- CAS 冲突 `409`（结构与 Manifest 一致）：

```json
{
  "error": {
    "code": "stale_session_file",
    "currentVersion": 2
  }
}
```

客户端应拉取最新快照，按 LWW 决策合并后以 `currentVersion` 为 base 重传。

成功后向组内全部设备广播 `session_file_updated`（`version` 为新版本）。

### 8.4 `DELETE /session-files/:sessionId/:baseVersion`

按版本 CAS 删除并释放配额。`baseVersion` 至少为 1。

- 成功：`200 {"deleted": true}`；
- 记录不存在（含被其他组占用）视为幂等成功：`200 {"deleted": false}`；
- 版本不匹配：`409 stale_session_file`（附 `currentVersion`）。

实际删除记录时广播 `session_file_deleted`（**不携带 `version` 字段**；幂等的 `"deleted": false` 响应不产生事件）。

### 8.5 生命周期语义

- **组合并**（sync-code redeem）：双方会话快照全部保留，迁移到合并后的组，组内可见；
- **放弃其他组**（discard-others）：被放弃组的会话快照随组删除，释放的字节计入 `reclaimedBytes`；
- 仅含会话快照的其他组同样会让 `hasOtherSyncData` 为 `true`。

---

## 9. WebSocket 事件

### 9.1 `POST /event-tickets`

**鉴权**：Bearer + PoP。创建 **30 秒有效、单次使用**的 WebSocket ticket。

**请求：** 空 body。

**响应 `201 Created`：**

```json
{
  "ticket": "base64url-32-bytes",
  "expiresAtMs": 1784304030000,
  "subprotocol": "lumina-events"
}
```

### 9.2 `GET /events`（WebSocket 升级）

连接时通过子协议头携带 ticket：

```http
Sec-WebSocket-Protocol: lumina-events, ticket.<ticket>
```

- ticket 无效/过期/重复使用，或设备已吊销/换组：`401 invalid_device_proof`；
- 握手成功后服务端确认子协议 `lumina-events`，并立即推送 `ready` 事件；
- 服务端每 30 秒发送一次 Ping 心跳；
- **慢消费者语义**：每连接事件缓冲 32 条，客户端消费不及时会被服务端以 policy violation 断开——断开即视为可能丢事件。

### 9.3 事件类型与 payload

事件只有以下六种（`serverTimeMs` 恒为 Unix 毫秒；其余字段按需出现）：

| type | 字段 | 触发时机 |
|---|---|---|
| `ready` | `groupRevision`, `serverTimeMs` | 连接建立后立即推送 |
| `manifest_updated` | `deviceId`, `version`, `groupRevision`, `serverTimeMs` | 组内设备 PUT Manifest 成功 |
| `session_file_updated` | `deviceId`, `sessionId`, `version`, `serverTimeMs` | 组内设备 PUT 会话快照成功 |
| `session_file_deleted` | `deviceId`, `sessionId`, `serverTimeMs`（无 `version`） | 组内设备实际删除会话快照 |
| `sync_group_merged` | `groupRevision`, `serverTimeMs` | 同步组合并完成 |
| `device_revoked` | `deviceId`, `serverTimeMs` | 设备被吊销（含 discard 批量吊销） |

payload 示例：

```json
{
  "type": "manifest_updated",
  "deviceId": "uuid",
  "version": 8,
  "groupRevision": 3,
  "serverTimeMs": 1784304000000
}
```

### 9.4 可靠性语义（重要）

事件是**尽力而为的通知总线，不是可靠数据源**。客户端必须：

- 断线/重连后重新拉取 `GET /manifests` 与 `GET /session-files` 全量对账；
- 收到 `sync_group_merged` 后刷新 `GET /bootstrap`；
- 不要基于事件序列重建状态，数据库（HTTP API）才是事实来源。

---

## 10. 安全约束与限制

### 10.1 哈希规范

**统一使用 SHA-256，禁止 BLAKE2b**。三处必须一致：

1. 设备证明 canonical 串中的 `hex(sha256(body))`；
2. `blockId = hex(sha256(ciphertextBytes))`；
3. `POST /blocks/missing` 提交的查缺 ID。

另有：注册 transcript 中的 `sha256(dekEnvelope)`；HKDF 均为 HKDF-SHA256。

### 10.2 时间戳单位

| 用途 | 单位 | 示例字段 |
|---|---|---|
| 存储/响应时间字段 | **Unix 秒** | `createdAt`、`lastSeenAt`、`updatedAt`、`expiresAt`（challenge/session/sync-code） |
| 签名头与事件时间 | **Unix 毫秒** | `X-Timestamp`、`serverTimeMs`、`expiresAtMs`（event ticket） |

### 10.3 请求体大小限制

| 端点类型 | 上限 | discovery 字段 |
|---|---:|---|
| 普通 JSON 端点 | 64 KiB | `maxJsonBytes` |
| 块上传 `PUT /blocks/:blockId` | 1 MiB | `maxBlockBytes` |
| Manifest 上传 `PUT /manifests/self/:baseVersion` | 4 MiB | `maxManifestBytes` |
| 会话快照上传 `PUT /session-files/:sessionId/:baseVersion` | 4 MiB | `maxSessionFileBytes` |

超限返回 `413 body_too_large`。

### 10.4 账号配额

- 每账号默认配额 **1024 MiB**（服务端 `config.yaml` 的 `storage.quotaMB` 可调）；
- block 密文与会话快照密文共同计入 `used_bytes`；上传中的 block 预留（reservation）也占用配额判定；
- 超配额返回 `413 quota_exceeded`；释放途径：删除会话快照（CAS DELETE）、`discard-others`、块 GC。

### 10.5 限流机制

服务端限流器**不存原文 key，只存 SHA-256**。触发时返回 `429 rate_limited`，错误对象附 `retryAfterMs`（IP 限流同时携带 `Retry-After` 响应头，单位秒）：

| 维度 | 端点 | 配额 |
|---|---|---|
| 客户端 IP | `POST /connections/start`、`POST /connections/complete` | 30 次/分钟 |
| 规范化用户名 hash | `POST /connections/start` | 10 次/分钟 |
| 客户端 IP | `POST /session-challenges`、`POST /sessions` | 30 次/分钟 |
| 设备 ID | `POST /sync-codes`、`POST /sync-codes/redeem` | 5 次/10 分钟 |

> 服务端仅信任 `127.0.0.1` 反向代理的 `X-Forwarded-For`；直连时以 TCP 对端 IP 计。

### 10.6 其他强制约束

- **已认证接口禁止 query 参数**：`RawQuery != ""` 或非规范化 `RequestURI` 直接 `401 invalid_device_proof`；
- 无路径自动纠偏：不做 trailing-slash 重定向和路径修正，路径必须精确匹配；
- JSON 请求严格解析：未知字段、重复字段、类型不符均 `400 bad_request`；
- 所有跨账户/跨组写操作在服务端单个写事务内复核设备 active、账号与组归属——中间件通过不代表写入必然成功，客户端要处理 `401 device_revoked`/`409 group_changed` 等事务内复核失败。

---

## 11. 典型业务流程

### 11.1 首次注册（新账号）

```text
1. GET /.well-known/lumina-relay        → pin instanceId、校准时间
2. POST /connections/start {username}   → accountExists=false, challenge, authSalt
3. 客户端本地：
   - 生成 accountId、deviceId、设备 Ed25519 key
   - Argon2id(password, authSalt) → passwordRoot
   - HKDF → loginSeed（登录 key）、envelopeKey
   - 随机 32B DEK → XChaCha20-Poly1305 封装为 72B dekEnvelope（带 AAD）
   - HKDF(DEK) → account-auth key
   - 构造 lumina-account-create transcript，
     分别用 login/account-auth/device key 签名
4. POST /connections/complete {全部字段} → session token + bootstrap
5. 安全存储：deviceId、设备私钥、DEK（或按需重新解封 envelope）
```

### 11.2 已有账号新设备登录

```text
1. GET /.well-known/lumina-relay
2. POST /connections/start {username}   → accountExists=true, challenge, authSalt
3. 客户端派生登录 key，生成新 deviceId/设备 key，
   构造 lumina-login-proof transcript，用 login key 和设备 key 签名
4. POST /connections/complete {共同字段} → session + bootstrap
   - 新设备位于空白同步组；bootstrap.dekEnvelope 用 envelopeKey 解封得到 DEK
   - hasOtherSyncData=true 时提示用户"其他设备存在可同步数据"
5. 如需合并数据：在旧设备生成同步码 → 新设备 redeem（见 11.5）
```

### 11.3 日常启动（会话续期，无需密码）

```text
1. POST /session-challenges {deviceId}  → attemptId, challenge
2. 设备私钥签名 lumina-device-session transcript
3. POST /sessions {attemptId, signature} → 新 session token + bootstrap
4. 收到 401 device_revoked → 本设备已被吊销，清空本地会话并回到登录流程
```

### 11.4 数据同步循环

```text
推送（本设备有变更）：
1. 变更条目切块加密 → 计算各 blockId = hex(sha256(cipher))
2. POST /blocks/missing {ids}           → 仅上传 missing 子集
3. PUT /blocks/:blockId（逐个）
4. PUT /manifests/self/:baseVersion     → 409 stale_manifest 时先拉取合并再重传
5. 会话数据变更：PUT /session-files/:sessionId/:baseVersion（整文件快照 CAS）

拉取（收到事件或定时对账）：
1. GET /manifests                       → 比对各设备 currentVersion
2. GET /manifests/:deviceId/:version    → 解密后按 HLC LWW 合并
3. GET /blocks/:blockId                 → 拉取缺失块
4. GET /session-files → 比对 version → GET /session-files/:sessionId

实时性：
- POST /event-tickets → GET /events（WebSocket）
- 事件仅作触发信号；断线重连后必须全量对账
```

### 11.5 设备加入同步组（六位码合并）

```text
旧设备 A：POST /sync-codes              → 六位码（5 分钟有效，念给用户）
新设备 B：POST /sync-codes/redeem {code} → 两组永久合并
A、B：收到 sync_group_merged 事件 → GET /bootstrap 刷新
      syncGroupId/groupRevision → 全量对账拉取对方数据
```

### 11.6 放弃其他同步组

```text
1. GET /bootstrap                        → 最新 syncGroupId、groupRevision
2. account-auth key 签名 lumina-discard-sync-groups transcript
3. POST /sync-groups/discard-others {groupRevision, accountProof}
   → 409 group_changed：组已变化，回到第 1 步
   → 200 {discardedDevices, reclaimedBytes}
4. 其他组设备、Manifest、block 引用、会话快照被永久删除并释放配额
```

### 11.7 无恢复语义（重要）

本期**没有密码重置或恢复包**。旧同步组的全部设备都丢失时，新设备只能继续使用空白组：

- 任一旧成员设备恢复上线后仍可生成同步码合并；
- 用户明确放弃旧数据时走 11.6 流程；
- 服务端**不会**根据离线时长自动删除数据。

---

## 12. 错误处理

### 12.1 错误响应格式

所有业务错误统一为以下结构；额外字段（如 `currentVersion`、`groupRevision`、`retryAfterMs`）平铺在 `error` 对象内：

```json
{
  "error": {
    "code": "snake_case",
    "message": "human readable",
    "requestId": "uuid"
  }
}
```

- `code` 为**稳定的机器可读错误码**，客户端只依据 code 分支，不解析 message；
- **handler 层业务错误**（4xx/5xx 业务分支）附带 `requestId`：回显请求头 `X-Request-ID`（≤128 字符），未提供时服务端生成 UUID——客户端排障时建议主动携带 `X-Request-ID`；
- **认证中间件与限流错误**（`invalid_device_proof`、session 校验失败、`rate_limited` 的 IP 限流路径、`body_too_large`）不携带 `requestId`，客户端不得依赖该字段必然存在。

### 12.2 稳定错误码总表与客户端应对策略

| code | HTTP | 含义 | 客户端应对 |
|---|---:|---|---|
| `invalid_credentials` | 401 | 密码 proof 或注册/登录证明错误 | 提示密码错误；不会产生设备，重新 start |
| `invalid_device_proof` | 401 | PoP 签名/时间戳/nonce 无效，或携带 query，或 event ticket 无效 | 检查 canonical 构造、时钟偏差（±5 分钟）、nonce 唯一性；WebSocket 场景重新领 ticket |
| `device_revoked` | 401 | 本设备已被吊销 | 清空本地会话与密钥引导，回到登录流程 |
| `invalid_sync_code` | 401 | 同步码错误/过期/已用/非同账号 | 提示重新生成六位码 |
| `account_became_existing` | 409 | 注册期间用户名被抢注 | 重新调用 `connections/start` 走登录分支 |
| `already_joined` | 409 | 兑换双方已在同一组（附 `groupRevision`） | 视为成功态，刷新 bootstrap |
| `stale_manifest` | 409 | Manifest baseVersion 过期（附 `currentVersion`） | 拉取新版本合并后以 currentVersion 重传 |
| `stale_session_file` | 409 | 会话快照 baseVersion 过期（附 `currentVersion`） | 拉取最新快照按 LWW 合并后重传/重删 |
| `session_id_conflict` | 409 | sessionId 被同账号**其他组**占用（仅 PUT） | 换新 sessionId，或先合并同步组 |
| `group_changed` | 409 | discard 的 groupRevision 已过期 | `GET /bootstrap` 刷新后重签 transcript 重试 |
| `block_busy` | 409 | 同一块并发上传冲突 | 短暂退避后重试 PUT |
| `bad_request` | 400 | JSON 结构/字段/路径参数非法 | 修正请求，不重试原样请求 |
| `block_hash_mismatch` | 400 | blockId ≠ sha256(body) | 检查客户端哈希实现（必须 SHA-256） |
| `invalid_session_id` | 400 | sessionId 不匹配正则或超长 | 修正 sessionId 生成规则 |
| `block_not_found` | 404 | 块对当前组不可见或不存在 | 视为数据缺失；检查是否未合并同步组 |
| `manifest_not_found` | 404 | Manifest 版本不存在或不属于当前组 | 重新 `GET /manifests` 对账 |
| `session_file_not_found` | 404 | 会话快照不存在（含被其他组占用） | 从列表移除本地引用 |
| `body_too_large` | 413 | 请求体超过对应端点上限 | 客户端切块/压缩，检查 discovery limits |
| `quota_exceeded` | 413 | 账号配额不足 | 提示用户清理（删除快照/discard），不自动重试 |
| `rate_limited` | 429 | 触发限流（附 `retryAfterMs`） | 按 `retryAfterMs` 退避后重试 |
| `internal_error` | 500 | 服务端内部错误 | 指数退避重试；持续失败上报 |
| `relay_not_initialized` | 503 | Relay 尚未初始化（仅 discovery） | 稍后重试 |

### 12.3 通用重试建议

- **幂等安全重试**：GET、`PUT /blocks/:blockId`（内容寻址天然幂等）、带同一 baseVersion 的 CAS PUT/DELETE；
- **必须换 nonce**：任何重试都要生成新的 `X-Nonce` 并重签 canonical（nonce 单次使用）；
- **409 冲突类不要盲目重试**：先拉取最新状态（bootstrap / manifests / session-files）再决策；
- **401 分流**：`invalid_device_proof` 查签名实现与时钟；session token 过期走 `/session-challenges` 续期；`device_revoked` 退出登录；
- **413/429 不要立即原样重试**：分别属于永久性（需改请求）与临时性（按 `retryAfterMs`）失败。

---

## 13. 客户端数据映射与对接契约清单

> 本章面向**客户端同步层实现者**：把 Lumina 客户端磁盘数据（见客户端 `sync-data-spec.md`）落到本协议的两条通道，并列出必须遵守的适配约束。**服务端始终内容无关**（见 §6.5）——本章为客户端侧落地约定，不改变任何服务端契约。以下结论已对照后端源码与客户端 `sparrow-manus` 实际代码核对。

### 13.1 数据类型 → 通道映射

| Lumina 数据 | 通道 | 建议 `key` / 标识 | 备注 |
|---|---|---|---|
| `config.json` | Manifest + blocks | `config/settings` | 含明文密钥，全程 E2EE 加密；机器相关条目本机优先合并（见 §13.6） |
| `sessions/{id}.jsonl` | 会话快照（§8） | `sessionId` | 整文件快照，见 §13.3 |
| `sessions/index.json` | Manifest + blocks 或不同步 | `sessions/index` | 派生索引，缺失可重建 |
| `papers/{id}/**` | Manifest + blocks | `papers/{id}/<相对路径>` | 条目粒度风险见 §13.5；`reader-document.json` 不同步 |
| `knowledge/**` | Manifest + blocks | `knowledge/<相对路径>` | `data/db/`（LanceDB 向量库）**不同步**，目标端重建 |
| `writing/**` | Manifest + blocks | `writing/<相对路径>` | `revision` 语义见 §13.6 |
| `logs/`、`tool-stats/`、渲染进程 localStorage | 不同步 | — | 设备诊断 / UI 状态，跨设备合并无意义 |

### 13.2 sessionId 契约（已核对兼容）

后端强制 `^session-[0-9]{1,16}-[a-z0-9]{1,32}$` 且总长 ≤ 64 字节（`internal/service/sessions.go`）。客户端各会话工厂统一生成 `session-${Date.now()}-${Math.random().toString(36).substring(2,8)}`：

- 时间戳 13 位毫秒 ≤ 16 位、随机段 6 位 base36 属 `[a-z0-9]` ≤ 32 位、总长约 28 字符 ≤ 64——**完全落在后端正则子集内**，无需任何一方改动。
- **低优先级隐患（客户端既有，非同步阻断）**：① `Math.random()` 退化时 `substring(2,8)` 可能返回空 / 极短随机段，生成的 ID 连客户端自身校验都过不了（概率近乎为零）；② 6 位 base36 + 毫秒时间戳在同组多设备下存在生日碰撞，碰撞会让两个会话被当作同一 `(account, sessionId)` 快照互相覆盖。**建议启用多设备会话同步前把随机段加长或改用 `crypto.randomUUID()`。**

### 13.3 会话同步模型

- **整文件快照 CAS，无 append / offset 端点**：客户端 `session-storage-format.md` 设想的“按字节偏移增量上传”在本后端**不成立**。`appendMessages` 仅是本地磁盘优化；每次会话变更（追加消息 / 改 meta / 压实）都要**整包重传** ≤ 4 MiB 密文快照，无块级去重。
- **主键 `(account, sessionId)`**：sessionId 账号内唯一；被同账号**其他未合并组**占用时 PUT 返回 `session_id_conflict`。
- **同步白名单**：仅 `*.jsonl`（可选 `index.json` 走 Manifest 通道，非会话通道）；**必须排除** `*.json`（旧格式）、`*.migrated`、`*.tmp`。

### 13.4 分块契约

- **单块密文 ≤ 1 MiB**（`maxBlockBytes`）：早期设计稿的“大二进制 4MB 块”**作废**，`source.pdf`（1–10 MB）须切 2–10 块。
- `blockId = hex(sha256(密文))`，64 位小写十六进制，内容寻址；客户端哈希必须统一 **SHA-256**。
- `POST /blocks/missing` 单批 **≤ 900 个 ID**（受 64KiB JSON body limit 约束）。

### 13.5 Manifest 契约与粒度风险

- **单设备一个 Manifest**（带版本），整包加密后 ≤ 4 MiB，承载**除会话外的全部条目**；`key` 命名空间由客户端规划（建议前缀分类）。
- 冲突：设备级 head CAS（`stale_manifest`），跨设备收敛靠**客户端 HLC LWW + tombstone**（删除必写墓碑）。
- **粒度风险**：papers 若按文件粒度（页图 + 双份 OCR + 切块图）条目激增，**约 55+ 篇重度论文即可把 Manifest 压至 4 MiB 上限**。客户端须在“细粒度去重”与“粗粒度打包（如每篇论文一条目 + 子索引）”间权衡。

### 13.6 敏感 / 机器相关 / 引用 / 派生数据处理（客户端职责）

- **敏感密钥**（config 的 `api_key`、`knowledge-bases.json` 的 `embeddingConfig.apiKey`、会话内嵌密钥等）：经 E2EE 统一加密，与普通内容同强度，服务端永不见明文。
- **config 本机优先合并**：`mcpServers` / `embeddingModels` 同名条目本机不被远端覆盖——须客户端解密后做**字段级语义合并**，**不可把 config 当不透明整块 LWW**，否则覆盖本机 stdio 命令 / localhost 条目。
- **绝对路径字段**（`filePath` / `absolutePath` / `pageAssets[].imagePath`）：客户端落地时自愈重写，服务端透传密文。
- **跨通道引用**（session ↔ paper，如 `chatSessionId` / `selectedPaperId`）：会话快照与 Manifest+blocks 是**两个独立 CAS 通道，无事务原子性**；须容忍短暂悬空引用。
- **writing `revision`**：客户端乐观锁，**后端无 `revision_conflict` 错误码**；writing 走 Manifest+blocks，冲突用 `stale_manifest` + HLC LWW 处理，`revision` 仅辅助客户端判定。
- **派生数据**（`sessions/index.json`、`papers/*/reader-document.json`、`knowledge/data/db/`）：不同步、目标端重建。

### 13.7 设计文档时效声明

客户端 `docs/sync-design.md` 为早期设计稿，**已过时**：其描述的恢复码身份、`/account/register`、`/account/dek`、`/device/register`、单一 `PUT /manifest`、长轮询 `/sync/poll`、4MB 分块等**均未被服务端采用**。客户端同步层一律以**本文件**为准（用户名 + 密码 proof、`/connections/*`、设备级 `PUT /manifests/self/:baseVersion`、同步组 + 六位码、WebSocket `/events`、1 MiB 分块）。

---

*本文档基于当前服务端实现编写；错误码、HTTP 状态与字段名受无版本协议契约固定，任何服务端变更须同步更新本文档。*
