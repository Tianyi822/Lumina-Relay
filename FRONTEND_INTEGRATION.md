# Lumina Relay 客户端协议

## 1. 产品流程

设置页只需要 Relay 地址、用户名和密码。客户端先访问：

```http
GET /.well-known/lumina-relay
```

随后调用 `POST /connections/start`。响应中的 `accountExists` 决定客户端完成注册还是登录，但页面不需要让用户选择：

- `false`：客户端生成 accountId、随机 DEK、密码派生登录 key、DEK envelope、account-auth key 和设备 key，提交创建证明；
- `true`：客户端用返回的 salt 从密码派生登录 key，签署 challenge 和新设备身份。

每次密码登录得到的设备都处于新的空白同步组。成功响应中的 `hasOtherSyncData` 只用于提示“其他设备存在可同步数据”，不会自动读取旧数据。

日常启动应优先使用已经保存在安全存储中的 deviceId 和设备私钥，通过 `/session-challenges`、`/sessions` 续期，不再要求密码。

## 2. 发现、连接与 session API

### `GET /.well-known/lumina-relay`

客户端首先调用此接口，以 pin `instanceId`、校准时间并读取限制：

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
    "maxMissingIds": 1000,
    "maxDeviceNameBytes": 128,
    "blockGcGraceSeconds": 86400
  }
}
```

### `POST /connections/start`

请求为 `{"username":"alice"}`。用户名限定 3–64 个 ASCII 字母、数字、点、下划线和连字符，服务端统一转小写。

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

### `POST /connections/complete`

注册和已有账号登录都提交以下字段：

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

仅注册时还要提交：

```json
{
  "accountId": "client-generated-canonical-uuid",
  "loginPublicKey": "base64url-32-bytes",
  "accountAuthPublicKey": "base64url-32-bytes",
  "dekEnvelope": "base64url-72-bytes",
  "accountProof": "base64url-64-bytes"
}
```

成功时返回：

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

并发抢注返回 `409 account_became_existing`，客户端重新调用 start；密码或 proof 错误返回 `401 invalid_credentials`，且绝不会创建设备。

### `POST /session-challenges` 与 `POST /sessions`

保存过设备私钥时，先提交 `{"deviceId":"uuid"}` 获取 `attemptId` 和 challenge，再以设备私钥签名 session transcript 并提交：

```json
{ "attemptId": "...", "signature": "base64url-64-bytes" }
```

响应与连接完成时的 session/bootstrap 结构相同。

## 3. 密码派生

密码 UTF-8 bytes 不做 Unicode normalization。固定参数：

```text
passwordRoot = Argon2id(
  password,
  authSalt,
  memory=65536 KiB,
  iterations=3,
  parallelism=1,
  output=32 bytes
)
```

再使用 HKDF-SHA256 做域分离：

```text
loginSeed  = HKDF(passwordRoot, info="lumina-login-ed25519", 32)
envelopeKey = HKDF(passwordRoot, info="lumina-dek-envelope-key", 32)
```

以上 HKDF 的 salt 为零长度空值（不是 `authSalt`；Argon2id 已使用该 salt）。

`loginSeed` 只构造 Ed25519 登录 key。`envelopeKey` 只使用
XChaCha20-Poly1305 包裹随机 32 字节 DEK。envelope 的唯一字节格式为：

```text
24-byte random nonce || Seal(DEK, aad)  // 32-byte ciphertext + 16-byte tag
```

因此 `dekEnvelope` 解码后必须恰好 72 字节。AAD 使用下节相同的长度前缀编码：

```text
lumina-dek-envelope:
  instanceId, normalizedUsername, accountId, authSalt
```

DEK 再独立派生 account-auth Ed25519 seed：

```text
accountAuthSeed = HKDF-SHA256(
  DEK,
  salt=empty,
  info="lumina-account-auth-ed25519",
  output=32 bytes
)
```

三类 key 不得复用。

所有二进制 JSON 字段使用无 padding 的规范 base64url。

## 4. 连接证明 transcript

所有 transcript 都按以下方式编码，不能使用 JSON、分隔符拼接或字段排序：

```text
UTF8(domain) || repeat(uint32be(byteLength(field)) || fieldBytes)
```

字段顺序固定如下：

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

`groupId` 取 bootstrap 的 `syncGroupId`。组合并后客户端必须刷新 bootstrap，
不能继续用合并前的 groupId/revision 构造 discard 证明。

注册 transcript 分别由 login key、account-auth key 和新设备 key 签名。已有账号登录 transcript 分别由 login key 和新设备 key 签名。session transcript 由已保存的设备 key 签名；discard transcript 由 account-auth key 签名。

## 5. HTTP 设备证明

除连接、session challenge、health 和 discovery 外，所有 HTTP 请求同时需要：

```http
Authorization: Bearer <session token>
X-Timestamp: <Unix 毫秒>
X-Nonce: <base64url 16..32 随机字节>
X-Signature: <base64url Ed25519 signature>
```

待签名内容：

```text
UPPER(method) + "\n" +
path + "\n" +
timestamp + "\n" +
nonce + "\n" +
hex(sha256(exactBodyBytes))
```

`path` 不含 query；签名接口不得携带 query。JSON 必须先序列化成最终 bytes，再对同一 bytes 计算签名并发送。服务端接受 ±5 分钟时差，nonce 在验签后持久占用。

## 6. 已认证 HTTP API

以下全部接口均要求上一节的 Bearer session 和设备 PoP。所有 JSON 响应均为 `application/json`；原始密文 Manifest/block/会话快照响应为 `application/octet-stream`。

| 方法 | 路径 | 请求或行为 |
|---|---|---|
| GET | `/bootstrap` | 刷新当前设备的 bootstrap 根状态 |
| POST | `/sync-codes` | 生成六位同步码 |
| POST | `/sync-codes/redeem` | `{"code":"123456"}`，永久合并同步组 |
| GET | `/devices` | 列出当前同步组设备 |
| DELETE | `/devices/:deviceId` | 吊销当前组设备 |
| POST | `/sync-groups/discard-others` | `{"groupRevision":3,"accountProof":"..."}`，永久放弃其他组 |
| GET | `/manifests` | 列出当前组各设备最新 head 和 groupRevision |
| GET | `/manifests/:deviceId/:version` | 下载指定密文 Manifest |
| PUT | `/manifests/self/:baseVersion` | 上传调用设备的原始密文 Manifest，设备级 CAS |
| POST | `/blocks/missing` | `{"ids":["64-char-sha256",...]}` |
| PUT | `/blocks/:blockId` | 上传原始密文 block，`blockId=hex(sha256(body))` |
| GET | `/blocks/:blockId` | 下载当前组可见 block |
| GET | `/session-files` | 列出当前组全部会话快照元数据 |
| GET | `/session-files/:sessionId` | 下载完整会话密文快照 |
| PUT | `/session-files/:sessionId/:baseVersion` | 上传完整会话密文快照，CAS |
| DELETE | `/session-files/:sessionId/:baseVersion` | CAS 删除会话快照，幂等 |
| POST | `/event-tickets` | 创建 30 秒单次 WebSocket ticket |

`PUT /manifests/self/:baseVersion` 的同设备 CAS 冲突返回：

```json
{
  "error": {
    "code": "stale_manifest",
    "currentVersion": 7
  }
}
```

组合并后，旧 revision 的 discard 返回 `409 group_changed`。所有 JSON 字段必须严格匹配定义，不接受未知字段、重复字段或 query 参数。

## 7. 同步组

任一设备调用 `POST /sync-codes` 获得五分钟有效、单次使用的六位码。另一台同账号设备调用 `POST /sync-codes/redeem` 后，两个同步组永久合并：

- A 邀请 B 后，A/B 均可读取双方数据；
- A 或 B 均可继续邀请 C；
- 同一账号但未合并的设备不能读取或探测对方 Manifest/blocks；
- 登录不需要旧设备确认，六位码只负责数据组授权。

WebSocket 通过 `POST /event-tickets` 取得 30 秒单次 ticket。连接 `/events` 时发送：

```http
Sec-WebSocket-Protocol: lumina-events, ticket.<ticket>
```

事件只有 `ready`、`manifest_updated`、`session_file_updated`、`session_file_deleted`、`sync_group_merged` 和 `device_revoked`。断线后必须重新拉取 `/manifests` 与 `/session-files`，不能把事件当作可靠数据源。

事件 payload 示例：

```json
{
  "type": "manifest_updated",
  "deviceId": "uuid",
  "version": 8,
  "groupRevision": 3,
  "serverTimeMs": 1784304000000
}
```

## 8. 设备级加密 Manifest

每台设备只推进自己的 `/manifests/self/:baseVersion`。组内设备先读 `/manifests`，再按 `{deviceId, version}` 拉取变化的密文。

Manifest 明文由客户端加密前组织，最低契约为：

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

整个对象加密，服务端看不到上述字段。合并时按以下元组字典序选择最后修改：

```text
(hlc.physicalMs, hlc.logical, writerDeviceId, operationId)
```

客户端通过 discovery/bootstrap 的 `serverTimeMs` 校准 HLC。删除必须写 tombstone，不能用“条目消失”表示；tombstone 保留在最新快照中，防止长期离线设备让数据复活。

## 9. 会话密文快照

会话数据以整文件密文快照形式同步，服务端存 SQLite BLOB，从不解析内容；快照结构、合并、去重全部是客户端职责。

```text
GET    /session-files
GET    /session-files/:sessionId
PUT    /session-files/:sessionId/:baseVersion
DELETE /session-files/:sessionId/:baseVersion
```

基础规则：

- `sessionId` 由客户端生成，必须匹配 `^session-[0-9]{1,16}-[a-z0-9]{1,32}$` 且总长不超过 64 字节，否则返回 `400 invalid_session_id`；
- `sessionId` 在**账号内唯一**：PUT 时若被同账号其他同步组占用返回 `409 session_id_conflict`；GET 对其他组占用的 ID 视为不存在（`404 session_file_not_found`），DELETE 视为幂等成功（`200 {"deleted": false}`）；
- 单个快照密文上限 4 MiB（discovery 的 `maxSessionFileBytes`），超限返回 `413 body_too_large`；密文大小计入账号配额，超配额返回 `413 quota_exceeded`；
- **不存在 append/index 路由**（`POST /session-files/:sessionId/append/*`、`GET|PUT /session-files-index*` 均为 404），增量合并完全在客户端完成。

`GET /session-files` 返回当前同步组全部快照元数据（`updatedAt` 为 Unix 秒）：

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

`GET /session-files/:sessionId` 返回 `application/octet-stream` 密文，当前版本在响应头 `X-Session-File-Version`；不存在返回 `404 session_file_not_found`。

`PUT /session-files/:sessionId/:baseVersion` 上传完整密文快照（请求体即密文 bytes，非空）：

- `baseVersion=0` 表示创建；覆盖时必须携带当前版本，服务端在单个 SQLite 事务内 CAS 递增版本；
- 成功返回 `200 {"version": 2, "size": 9}`；
- `baseVersion` 过期返回 `409 stale_session_file` 并携带 `currentVersion`，客户端应拉取最新快照后按 LWW 决策重传。

`DELETE /session-files/:sessionId/:baseVersion` 按版本 CAS 删除并释放配额（`baseVersion` 至少为 1）：

- 成功返回 `200 {"deleted": true}`；记录不存在视为幂等成功，返回 `200 {"deleted": false}`；
- 版本不匹配返回 `409 stale_session_file` 并携带 `currentVersion`。

CAS 冲突响应结构与 Manifest 一致：

```json
{
  "error": {
    "code": "stale_session_file",
    "currentVersion": 2
  }
}
```

PUT 成功向组内全部设备广播 `session_file_updated`（`version` 为新版本），DELETE 实际删除记录时广播 `session_file_deleted`（不携带 `version` 字段；幂等的 `"deleted": false` 响应不产生事件）：

```json
{
  "type": "session_file_updated",
  "deviceId": "uuid",
  "sessionId": "session-1753857600000-a1b2c3",
  "version": 2,
  "serverTimeMs": 1784304000000
}
```

同步组合并后双方会话快照全部保留且组内可见；`POST /sync-groups/discard-others` 会随被放弃组一并删除其会话快照并释放账号配额。

## 10. 块与限制

- `blockId = hex(sha256(ciphertextBytes))`，统一使用 SHA-256；
- 单块上限 1 MiB；
- 密文 Manifest 上限 4 MiB；
- 会话密文快照上限 4 MiB；
- 普通 JSON 上限 64 KiB；
- `/blocks/missing` 每次最多 1000 个 ID；
- 时间字段 `createdAt`/`lastSeenAt`/`updatedAt`/`expiresAt` 使用 Unix 秒，`serverTimeMs`/`X-Timestamp` 使用 Unix 毫秒。

当前不公开通用块 prune 端点，避免在另一设备“已上传 block、尚未提交引用它的
Manifest”窗口内误删关联。用户显式放弃其他同步组时会释放被放弃组的账号配额（含会话快照占用）；
由此产生的物理孤儿块仍保留 discovery 中 `blockGcGraceSeconds` 指定的安全宽限期
（当前为 24 小时）。后台 GC 只删除无账号引用且无上传预留的块。

## 11. 无恢复语义

本期没有密码重置或恢复包。旧同步组的全部设备都丢失时，新设备继续使用空白组：

- 任一旧成员设备恢复上线后仍可生成同步码；
- 用户明确放弃旧数据时，客户端用 account-auth key 签署 discard transcript，并调用 `/sync-groups/discard-others`；
- 服务端不会根据离线时长自动删除数据。

## 12. 错误格式与稳定错误码

错误统一为：

```json
{
  "error": {
    "code": "snake_case",
    "message": "human readable",
    "requestId": "uuid"
  }
}
```

| code | HTTP |
|---|---:|
| `invalid_credentials`、`invalid_device_proof`、`device_revoked`、`invalid_sync_code` | 401 |
| `account_became_existing`、`already_joined`、`stale_manifest`、`stale_session_file`、`session_id_conflict`、`group_changed`、`block_busy` | 409 |
| `bad_request`、`block_hash_mismatch`、`invalid_session_id` | 400 |
| `block_not_found`、`manifest_not_found`、`session_file_not_found` | 404 |
| `body_too_large`、`quota_exceeded` | 413 |
| `rate_limited` | 429 |
