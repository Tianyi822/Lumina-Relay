# Lumina 端到端加密数据同步 — 设计文档

> 适用范围：跨设备同步 `~/.lumina/` 全量数据（论文、批注、会话、知识库、配置等）  
> 涉及版本：设计阶段（2026-06）  
> 相关代码：`src/main/services/sync/`（待新增）、独立仓库 `lumina-relay`（待新增）  
> 状态：**设计完成，待实现**

---

## 0. 决策记录

本设计基于以下关键决策（逐个澄清确认）：

| 维度 | 选择 | 理由 |
|------|------|------|
| 同步后端 | **自建服务端**（用户/运维者自己的服务器） | 满足"首次连接握手生成唯一密钥"的设想，可实现端到端加密 |
| 加密模型 | **端到端加密（E2EE）** | 服务端只看到密文，即使被攻破也无法解密用户数据 |
| 同步范围 | **全量同步**（所有数据，约 326MB） | 体验完整，但需内容寻址分块支持高效增量 |
| 冲突处理 | **最后写入胜（LWW）** + 冲突区兜底 | 论文阅读场景下"同时编辑同一项"概率低，LWW 简单可接受 |
| 多设备密钥 | **主密码 + 设备密钥双层** | 可吊销设备、改密码不重传数据，性价比最高 |
| 账户身份 | **账户恢复码**（服务端可验证哈希） | 无邮件依赖、自托管友好、匿名性强 |
| 后端语言 | **Go** | Lumina Relay 极薄（IO 密集、低并发、快速迭代），正好是 Go 的甜区；Rust 的性能与内存安全优势在此场景兑现不了 |
| 后端框架 | **Gin** | 作者更熟 Gin；服务端薄，Gin 的"重"无感；star 多、问题易搜 |
| 后端数据访问 | **sqlc** | 编译期 SQL 代码生成（非 ORM），类型安全、SQL 可审计、运行时零反射；适合固定 CRUD + 密码学字段的审计需求 |

**开源约束**：仓库公开，代码中**绝不能**硬编码任何主密钥、盐、IV。所有密钥运行时生成或由用户输入。

---

## 1. 背景与动机

Lumina 目前是纯本地应用，所有数据存于 `~/.lumina/`：

| 数据 | 大小（实测） | 敏感度 | 结构 |
|------|------|--------|------|
| `papers/` | 270MB | 中 | 每篇一目录：`source.pdf` + OCR 结果 + 页图 + 翻译缓存 + `annotations.json` + `meta.json` |
| `sessions/` | 6.8MB | 中 | 每个 JSON 文件一个会话 |
| `knowledge/` | 49MB | 中 | LanceDB 向量库 + 原始文件 |
| `tool-stats/` | 476KB | 低 | 使用统计 |
| `config.json` | 4KB | **高** | 内含 DeepSeek/Qwen 等 API key |
| `lab/`（SSH） | — | **高** | SSH 连接配置（已用 `safeStorage` 加密） |

现有加密基础：仅 SSH 密码使用 Electron `safeStorage`（操作系统密钥环），其余全明文落盘。**目前没有任何同步/云相关代码**。

用户诉求：跨设备（如 Mac + Windows 台式机）阅读论文时，批注、阅读进度、会话等数据保持一致，且数据在传输和存储全程加密。

---

## 2. 系统总览与信任边界

### 2.1 架构图

```
┌─────────────────────────────────────────────────────────────┐
│ 设备 A (Mac)                      设备 B (Win)               │
│  Lumina 客户端                     Lumina 客户端              │
│ ┌──────────────────────────┐    ┌──────────────────────────┐ │
│ │ 同步引擎 (新增)           │    │ 同步引擎 (新增)           │ │
│ │  ├ 变更监听 (chokidar)    │    │  ├ 拉取/合并              │ │
│ │  ├ 快照分块 + 内容寻址     │    │  ├ 解密 + 落盘            │ │
│ │  ├ E2EE 加密层            │    │  └ E2EE 加密层            │ │
│ │  └ 密钥库 (safeStorage)   │    │     ↑ 输主密码取回主密钥   │ │
│ └─────────────┬────────────┘    └─────────────┬────────────┘ │
│               │ HTTPS (TLS)                    │              │
└───────────────┼────────────────────────────────┼──────────────┘
                │                                │
                ▼                                ▼
        ┌────────────────────────────────────────────┐
        │   Lumina Relay (自建, 开源, 独立仓库)            │
        │  ├ REST: 认证 / 设备注册 / 元数据清单          │
        │  ├ 块存储: 只存密文内容块 (内容寻址 sha256)    │
        │  └ 事件日志: append-only (设备,时间,块ID)      │
        │   ↑ 服务端看到的永远只是密文 + 元数据清单       │
        └────────────────────────────────────────────┘
```

### 2.2 信任边界（E2EE 的核心承诺）

| 角色 | 能看到 | 看不到 |
|------|--------|--------|
| **服务端运营者（哪怕是自己）** | 密文块、文件大小、修改时间、设备数量 | 任何明文：论文内容、批注、API key、对话 |
| **拥有主密码的合法用户** | 自己的全部明文 | — |
| **没有主密码的攻击者（拿到服务器全部数据）** | 密文，无法解密 | 任何明文 |

### 2.3 关键密码学构件

1. **主密码 → 主密钥 (MK)**：Argon2id（内存硬化 KDF）派生，用户人脑记忆，**永不离开设备、永不上传**。
2. **数据加密主密钥 (DEK)**：随机生成的 256 位密钥，用 MK（经 XChaCha20-Poly1305）加密后存服务端——即"首次连接握手生成的唯一密钥"，每用户一份。
3. **设备密钥**：每台设备一对 X25519 密钥对（公钥上送、私钥进 safeStorage），用于传输层握手和设备身份。
4. **数据块加密**：每块独立 XChaCha20-Poly1305，nonce 随机，得到认证加密密文。

**库选型**：`libsodium-wrappers`（XChaCha20-Poly1305、Argon2id、X25519 一库全覆盖）。**绝不自己实现密码学原语**。

### 2.4 三个产出物

| # | 产物 | 仓库 | 角色 |
|---|------|------|------|
| 1 | 同步引擎 SDK | 本仓库 `src/main/services/sync/` | 客户端：监听、分块、加解密、合并 |
| 2 | Lumina Relay | 新建独立开源仓库 `lumina-relay` | 纯存储：认证、密文块存储、事件日志 |
| 3 | 同步设置 UI | 本仓库 `renderer` | 主密码设置、连接配置、同步状态 |

### 2.5 后端技术选型（Lumina Relay）

#### 技术栈

| 层 | 选型 | 说明 |
|----|------|------|
| 语言 | **Go** | Lumina Relay 极薄（IO 密集、低并发、快速迭代、要吸引社区），是 Go 的甜区。Rust 的两大王牌（性能、内存安全）在"不碰明文、个人同步量级"场景兑现不了 |
| HTTP 框架 | **Gin** | 作者更熟 Gin；服务端 11 个 CRUD 端点很薄，Gin 的 `gin.Context` 耦合在这个规模无感；star ~80k，问题易搜 |
| 数据库 | **SQLite** | 单文件、零运维、Docker 挂一个卷即可；个人同步量级（5 表、KB-MB 级记录）绰绰有余。用 `modernc.org/sqlite` 纯 Go 驱动，支持 `CGO_ENABLED=0` 静态编译 |
| 数据访问 | **sqlc** | 编译期 SQL 代码生成（非 ORM）。写 SQL → 生成强类型 Go 函数。类型安全、SQL 完全暴露可审计、运行时零反射。适合固定 CRUD + 密码学字段审计 |
| 密文块存储 | 本地文件系统（分桶） | `blocks/<前2字符>/<前4字符>/<blockId>` 分桶，避免单目录百万文件；`O_CREAT\|O_EXCL` 天然去重 |
| 密码学 | `golang.org/x/crypto` | argon2（恢复码哈希）、ed25519（设备密钥验签），标准库扩展，审计充分 |
| 日志 | `log/slog` | Go 1.21+ 标准库结构化日志，无外部依赖 |
| 迁移 | `golang-migrate` | SQL 迁移文件版本化管理 schema，启动时自动应用 |
| 测试 | `testing` + `testify` | 与客户端 `node:test` 风格对齐 |
| 部署 | 多阶段 Docker，alpine 基础镜像 | 最终镜像 <20MB，单二进制 + 数据卷 |

#### sqlc 工作流与自动化

sqlc 是**独立 CLI 工具**（非项目依赖，不进 `go.mod`），推荐用 `brew install sqlc` 或下载预编译二进制安装。

**开发时**：改 `schema.sql` / `query.sql` → 跑 `sqlc generate` → 生成 `.go` 文件 → **提交进 git**。

**部署时**：完全不碰 sqlc。生成的 `.go` 文件已在仓库里，`go build` 照常编译。CI、生产服务器、其他开发者都不需要装 sqlc。

**防漂移**：生成的 `.go` 提交进 git 是关键前提。配合 pre-commit hook 自动 `sqlc generate && git add`，保证 SQL 与代码永远一致。

#### 部署形态

**推荐：Docker 单容器 + 持久卷 + Caddy 反代自动 HTTPS**

```bash
docker run -d \
  --name lumina-relay \
  -p 8443:8443 \
  -v lumina-data:/data \
  ghcr.io/<owner>/lumina-relay:latest
```

- **单二进制 + 单 `.db` 文件 + 一个 blocks 目录**——部署本质是"一个二进制 + 一个数据目录"
- SQLite 单文件，备份即 `sqlite3 .backup` + `tar blocks/`
- 升级即"换二进制、重启"，schema 迁移由 `golang-migrate` 启动时自动应用
- TLS 必须强制（E2EE 第一道防线）；Caddy 反代把证书自动化（Let's Encrypt）
- **数据卷必须持久化**（命名卷或绑定宿主机目录），否则容器删除丢数据——部署文档需加粗警告

备选形态：裸机 systemd 直接跑二进制（适合极简 VPS）。

---

## 3. 密钥握手与设备配对协议

### 3.1 密钥层级（四层）

```
主密码 P  ──(用户人脑, 永不上传)──►  Argon2id 派生
                                        │
                                        ▼
                              主密钥 MK (设备内存, 重启即消失)
                                        │ 解密
                                        ▼
              数据加密主密钥 DEK ◄── (随机生成, 每用户全局唯一一份)
              │   ↑ 这就是「首次握手生成的唯一密钥」
              │   ↑ 加密后(MK包裹)存服务端, 明文永不出现在服务端
              │
              ├──► HKDF 派生 ──► 块加密密钥(每数据块) + ...
              │
设备密钥对 (X25519) ──(每台设备独立, 私钥进 safeStorage)──► 传输身份认证
```

### 3.2 账户身份：恢复码（与主密码正交）

E2EE 下服务端不能、也不应该知道主密码，否则违背"服务端看不到明文"。因此账户身份与数据密钥解耦成两个正交的东西：

```
账户身份 = "你有权接入这个账户吗?"   ← 服务端可验证（恢复码哈希，不涉及明文）
数据密钥 = "你能解开数据吗?"         ← 客户端本地验证（主密码，服务端不可见）
```

- **恢复码**：注册时随机生成（如 `AB3K-7P9X-QR2M-8F4T-LN6C`），服务端只存哈希。它干一件事——让服务端知道"这几台设备该归到同一个账户下"。**它不是密码**：服务端不能用它解密，不可更换（是账户的永久坐标），设计上要被复制到各设备。
- **主密码**：用户人脑记忆，服务端永不接触。干一件事——派生 MK 解开 DEK 信封。

两个条件同时成立才算合法同账户设备：

```
能接入账户 = 恢复码哈希对        ← 服务端验证，不涉及明文
能解密数据 = MK 能解开 DEK 信封   ← 客户端本地，服务端看不见
```

- 只有恢复码、没主密码 → 服务端放行你进账户，但拿到一堆解不开的密文。
- 只有主密码、没恢复码 → 连密文都够不着（服务端不认你是账户成员）。

### 3.3 流程 A：首次注册（第一台设备）

第一台设备是"信任根"——它不"判断"，它"创造"：同时生成恢复码和 DEK。

```
设备 A (首次)                              Lumina Relay
    │
    │  ① 用户输入主密码 P
    │     salt = random(16B)
    │     MK = Argon2id(P, salt, t=3, m=64MiB, p=4)
    │     DEK = random(32B)          ← 「唯一密钥」此刻诞生
    │     recoveryCode = random(人可读格式)
    │     生成设备密钥对 (devPrivA, devPubA)
    │     dekEnvelope = {
    │       salt,
    │       nonce: random(24B),
    │       ct: XChaCha20-Poly1305.Seal(MK, nonce, DEK)
    │     }
    │
    ├──── POST /account/register ──────────►
    │          { recoveryCodeHash: Argon2id(recoveryCode),
    │            dekEnvelope,
    │            devicePubKey: devPubA,
    │            deviceName }
    │                                        │ 创建账户:
    │                                        │ { accountId, dekEnvelope,
    │                                        │   devices:[devPubA], createdAt }
    │ ◄────── 200 { accountId, deviceId,
    │               sessionToken,
    │               recoveryCode } ──────────┤  ← 恢复码明文仅此一次返回
    │
    │  ② 本地安全存储:
    │     safeStorage(devPrivA)  → 设备私钥
    │     safeStorage(DEK)        → DEK 明文(日常免输主密码)
    │     safeStorage(recoveryCode) → 恢复码(便于新设备加入)
    │     sessionToken            → 会话令牌
    │
    │  ③ 强制弹窗让用户导出恢复码（复制/打印/存密码管理器），不导出不让关
    ▼  注册完成, 可开始全量同步
```

### 3.4 流程 B：新设备登录（设备 B 加入）

核心洞察：**E2EE 下服务端无法、也不需要验证主密码是否正确**——因为主密码的"正确性"等价于"能用 MK 解开 dekEnvelope 得到有效 DEK"。服务端只负责下发 dekEnvelope，解密成败完全在客户端本地判定。

```
设备 B (新设备)                           Lumina Relay
    │
    │  ① 用户输入 服务器地址 + 账户恢复码 + 主密码
    │     生成设备密钥对 (devPrivB, devPubB)
    │
    ├──── GET /account/dek?accountId=… ─────►
    │                                        │ (rate-limited 10次/分钟/IP)
    │ ◄──── 200 { dekEnvelope } ─────────────┤
    │          { salt, nonce, ct }
    │
    │  ② 本地派生 & 解密:
    │     MK = Argon2id(P, salt, …)
    │     DEK = XChaCha20-Poly1305.Open(MK, nonce, ct)
    │       ├─ AEAD tag 失败 ─► 主密码错, 提示用户 (服务端全程不知)
    │       └─ 成功 ─► 得到 DEK
    │
    ├──── POST /device/register ─────────────►
    │          { accountId, recoveryCode, devicePubKey: devPubB }
    │                                        │ 校验 recoveryCode 哈希
    │                                        │ 加入 devices 列表
    │ ◄──── 200 { deviceId, sessionToken } ───┤
    │
    │  ③ safeStorage(devPrivB) + safeStorage(DEK) 落地
    ▼  可开始全量同步（首次拉取全部 manifest + 块）
```

### 3.5 流程 C：日常启动（免输主密码）

```
App 启动 → safeStorage.decrypt(DEK) → 注入内存 → 同步引擎就绪
```

主密码只在三种情况需要重输：**换设备 / 重装 / 被"清除本地密钥"**。

### 3.6 流程 D：改主密码（无需重新加密所有数据）

```
① 输旧主密码 Po → 派生 MKo → 解出 DEK (顺带验证旧密码)
② 输新主密码 Pn → salt'=random → MKn=Argon2id(Pn,salt',…)
③ 新 dekEnvelope = Seal(MKn, DEK)     ← 只重新包裹 DEK 本身
④ PUT /account/dek { newDekEnvelope }  (session 认证)
   服务端替换 dekEnvelope
```

关键：**改密码不碰任何已加密的数据块**——因为 DEK 没变，只是包裹它的信封换了。这是双层模型的最大优势。

### 3.7 流程 E：吊销设备

```
设备管理页 → 选某设备 → DELETE /device/{deviceId} (session 认证)
服务端: 从 devices 列表删除该公钥, 该设备的 sessionToken 立即失效
```

⚠️ **E2EE 固有限制（必须告知用户）**：吊销只能阻止该设备**未来的**同步访问；它本地已经落盘的明文（若有 DEK 副本）无法被远程擦除。

### 3.8 设备注销与账户存续（抗故障模型）

**核心原则：第一台设备只在"出生那一刻"特殊**——它唯一的特权是在注册瞬间同时生成恢复码和 DEK。过了那一瞬间，它就退化成一台普通设备。

| 场景 | 结果 |
|------|------|
| 第一台设备注销，还有其他设备在线 | ✅ 无忧。每台设备都独立持有 DEK + 恢复码 + 自己的密钥，不依赖母设备 |
| 唯一设备注销，但恢复码曾导出 | ✅ 可恢复。DEK 信封独立存服务端，凭恢复码 + 主密码在任何新设备复活账户 |
| 唯一设备注销，恢复码从未导出 | ❌ 数据不可恢复。这是 E2EE 的固有代价（同 Signal/ProtonMail），靠强制引导避免 |

**防丢失设计**：注册强制导出恢复码；设置页常驻"查看恢复码"；唯一设备点"注销"时二次确认。

### 3.9 防滥用与安全注记

| 威胁 | 缓解 |
|------|------|
| 攻击者拉走 dekEnvelope 想离线爆破主密码 | Argon2id 内存硬化（64MiB×3 轮）+ AEAD，爆破成本极高；服务端 rate-limit `/account/dek` |
| 中间人篡改握手 | 强制 TLS；设备密钥做请求签名（签名 over 请求体+timestamp+nonce） |
| 重放请求 | 每请求带 timestamp + nonce，服务端去重窗口 |
| 服务端运营者 curiosity | 服务端永不接触 MK / DEK 明文 / 任何数据明文 |

---

## 4. 数据分块、内容寻址与增量同步

### 4.1 三层结构：文件清单 → 块引用 → 密文块

```
账户快照 (Manifest)                         服务端视角: 只看到下面两层
┌──────────────────────────────────────┐
│  manifest = {                         │   ← 客户端加密后上传, 服务端不解析
│    version: 137,                       │
│    files: [                            │
│      {                                 │
│        path: "papers/<id>/annotations.json",
│        mtime: 1'718'...,              │
│        size: 42KB,                     │
│        blocks: ["b3a1..","c7f2.."] ◄──┼──► 引用内容寻址的密文块
│      }, ...                            │
│    ]                                   │
│  }                                     │
│  ciphertext = Seal(DEK, manifest)      │
│  manifestId = sha256(ciphertext)       │
└────────────────────────────────────────┘
                    │
                    ▼
┌─────────────────────────────────────────────────┐
│  密文块存储 (服务端, 内容寻址)                    │
│  blockId = sha256(ciphertext)                     │
│  POST /blocks/{blockId}  body: ciphertext         │
│  服务端: 去重存储 (已有同 blockId 则跳过)         │
└─────────────────────────────────────────────────┘
```

**关键性质**：

- **幂等**：`POST /blocks/{blockId}` 无论传多少次结果一样，天然防重传。
- **增量天然成立**：改动一个文件，只有该文件变动的块 + 新 manifest 上传，其余块引用不变。
- **AEAD 完整性**：每个密文块附带 16 字节认证标签，下载后校验，服务端无法悄悄篡改。

### 4.2 分块粒度：按数据类型区别对待

| 数据类型 | 变更模式 | 分块策略 | 理由 |
|---------|---------|---------|------|
| **小 JSON**（`meta.json`/`annotations.json`/`sessions/*`/`config.json`） | 整体重写，粒度小 | 整文件 = 1 块 | KB 级，拆块无收益 |
| **大文本**（`merged.md`，OCR 结果） | 局部修改 | 固定块 256KB，滚动哈希边界 | 边界对齐让局部改动只动少数块 |
| **大二进制**（`source.pdf`/页图 PNG） | 一次写入后只读 | 大块 4MB，不再细分 | 写入后不变，大块省请求 |

```
papers/<id>/annotations.json   →  [整文件1块]  改一条批注: 重传1块 + 新manifest
papers/<id>/merged.md (380KB)  →  [256K块×2]  局部改: 只动命中的块
papers/<id>/source.pdf (5MB)   →  [4MB块×2]   从不变: 首次传完不再传
papers/<id>/pages/*.jpg        →  [整图1块]   OCR产出后不变: 只传一次
```

用户最频繁的操作——**改一条批注**——的同步成本：**1 个小密文块（几 KB） + 1 个新 manifest**。

### 4.3 增量同步算法（上行）

```
                  设备 A 本地                    │              服务端
                                                 │
  ① 检测变更 (chokidar 监听)                      │
     annotations.json mtime 变了                 │
                                                 │
  ② 读取新内容, 切块, 加密, 算 blockId            │
     oldBlocks = ["b3a1.."]  (从本地 manifest 取) │
     newBlocks = ["e5c9.."]  (重新切块加密)       │
     toUpload = newBlocks - oldBlocks            │
                                                 │
  ③ 询问服务端: 这些块你已经有了吗?                │
     POST /blocks/have { ids:["e5c9.."] }       ├────►
                                                 │      ◄── { missing: ["e5c9.."] }
  ④ 只上传缺失块                                  │
     PUT /blocks/e5c9..  (ciphertext)           ├────►
                                                 │      ◄── 200 OK
  ⑤ 构建新 manifest, 加密上传                     │
     PUT /manifest  { ciphertext, baseVersion } ├────►
                                                 │      ◄── 200 { version: 138 }
  ⑥ 本地 manifest 指针前移                        │
```

### 4.4 冲突处理：LWW + 冲突区兜底

```
设备 A  PUT /manifest  (version 138, 改了批注1)
设备 B  PUT /manifest  (version 139, 改了批注2)  ← 晚 2 秒到达

服务端按到达时间排序, 保留最后到达的 manifest 版本:
  currentManifestVersion = 139
  A 的改动在 manifest 层面"输了", 但 A 改的那个块仍在服务端

设备 A 下次同步:
  GET /manifest → 拿到 version 139 (B 的版本)
  A 发现本地版本不在 139 的引用里 → 触发 LWW 合并:
      远端版本 (139, 含批注2) vs 本地版本 (138, 含批注1)
      LWW: 远端 mtime > 本地 mtime → 接受远端版本
      本地 annotations.json 被覆盖前复制到冲突区:
        papers/<id>/conflicts/annotations-2026-06-15T10-22.json
      UI 提示"检测到冲突, 本地版本已保存到冲突区, 7天后清理"
```

### 4.5 同步模式边界

| 场景 | 触发 | 代价 |
|------|------|------|
| **首次同步**（新设备登录后） | `localManifestVersion = null` | 拉取最新 manifest → 按引用拉所有块 → 解密落盘。约 326MB 全量下载一次 |
| **增量同步**（日常） | 文件变更 / 定时 / 手动 | 只传/收变动的块 + 新 manifest |
| **全量校验** | 用户手动触发 / 异常恢复 | 重新算所有本地块的 blockId，与远端 manifest 逐项对账 |

---

## 5. 同步引擎架构与变更监听

### 5.1 目录结构

```
src/main/services/sync/                      ← 新增子系统
├── SyncEngine.ts          ← 协调器: 编排上传/下载/合并循环
├── SyncConfig.ts          ← 同步配置(服务器地址、状态)
├── crypto/
│   ├── keyManager.ts      ← 密钥生命周期(MK/DEK/设备密钥/safeStorage)
│   ├── handshake.ts       ← 注册/登录/改密码握手协议
│   └── envelope.ts        ← dekEnvelope 包装/解包
├── store/
│   ├── contentStore.ts    ← 本地密文块缓存(content-addressed)
│   ├── manifestStore.ts   ← 本地 manifest 版本历史
│   └── chunker.ts         ← 分块器(三类策略)
├── transport/
│   ├── SyncClient.ts      ← HTTPS 客户端(认证/重试/限速)
│   └── SyncApi.ts         ← REST 端点封装
├── changeWatch/
│   ├── ChangeWatcher.ts   ← chokidar 监听 ~/.lumina
│   └── changeQueue.ts     ← 变更事件去抖队列
└── conflict/
    └── conflictBin.ts     ← 冲突区管理(7天清理)
```

它是独立服务，不侵入任何现有业务服务——`PaperStorageService` 完全不知道同步的存在。

### 5.2 核心工程原则：同步引擎是旁观者，不是侵入者

Lumina 现有写入路径是单线程串行的（`WriteQueue`）。加入同步后磁盘上会出现第二个写入者。**核心设计原则：同步引擎永远不直接写业务文件，它必须穿过现有的 `WriteQueue` / `StorageService`**。

### 5.3 三条数据流

**流 1：本地变更 → 上行同步**

```
用户改批注
  → PaperService.saveAnnotationStore()  ← 现有代码, 不动
  → WriteQueue 串行写入 annotations.json  ← 现有, 不动
  → 磁盘文件变化
      │
      ▼
ChangeWatcher (chokidar) 捕获 mtime 变化
  → changeQueue 去抖 (500ms, 聚合多次保存)
  → SyncEngine.uploadPending()
      ├─ 切块/加密
      ├─ 询问服务端缺失块
      ├─ 上传块 + 新 manifest
      └─ 本地 manifest 指针前移
```

**流 2：远端变更 → 下行同步**

```
SyncEngine (定时轮询 / 应用启动 / 手动触发)
  → GET /manifest  → 拿到远端最新版本 N
  → N > localManifestVersion ?
      │ 是
      ▼
  diff(oldManifest, newManifest) → 得到变更文件列表
      │
      ▼ 对每个变更文件, 分发到对应业务服务的"同步写入入口":
  annotations.json  → PaperService.applySyncedAnnotations()   ← 新增方法
  merged.md         → PaperStorageService.saveMergedMdFromSync() ← 新增方法
  session/*.json    → SessionService.applySyncedSession()     ← 新增方法
  config.json       → ConfigManager.applySyncedConfig()       ← 新增方法
  source.pdf / 页图 → 直接写文件 (只读资产, 不经业务逻辑)

  每个入口内部都走 WriteQueue 串行化, 做业务校验, 更新内存状态并通知渲染进程
```

**流 3：冲突检测（LWW 落地）**

```
准备应用远端 version N 时:
  本地文件 mtime = T_local
  远端 manifest 里该文件的 mtime = T_remote

  if T_remote >= T_local: 直接应用远端版本 (LWW: 远端更新)
  else:
      本地比远端新 → 上行同步还没跑/失败
      → 把本地版本挪进冲突区: conflicts/<path>-<timestamp>
      → 仍应用远端版本 (manifest 是权威), 保留本地副本 7 天
      → 通知渲染进程: "有冲突, 本地版本已存档"
```

### 5.4 防回环与防风暴

**防回环**：下行同步引擎自己写文件会被 chokidar 捕获触发上行同步——形成回环。解法是**写者抑制标志**：下行写入前标记路径，ChangeWatcher 过滤被标记路径的事件（延迟 2s 移除标记）。

**防风暴**：用户重 OCR 一篇 200 页论文 → 200 个文件事件。解法是**去抖 + 批量上传**：事件攒 500ms 静默期后 flush 成一个批量，统一切块/查重/上传。

### 5.5 同步触发时机

| 触发 | 方向 | 说明 |
|------|------|------|
| 应用启动后 3s | 下行优先，再上行 | 先拉别人的更新，再推自己的 |
| 文件变更（chokidar + 去抖） | 上行 | 用户操作的实时响应 |
| 定时（默认 5 分钟） | 双向 | 兜底，捕获 chokidar 漏掉的情况 |
| 手动（"立即同步"按钮） | 双向 | 用户主动 |
| 从睡眠/网络恢复 | 双向 | Electron `power-resume` / `online` 事件 |
| 应用退出前 | 上行 | 优雅退出流程里加一步 flush（带 5s 超时） |

### 5.6 与优雅退出集成

现有 `app.ts` 的 `requestShutdown()` 并行清理多个服务，每个 5s 超时。同步引擎挂载点：

```
requestShutdown() {
  Promise.all([
    ...,
    syncEngine.flushPending({ timeout: 5000 }),  ← 新增
    ...
  ])
}
```

### 5.7 与 `lumina://` 协议、IPC 的关系

- **`lumina://` 协议**：渲染进程读图片走 `lumina://papers/<id>/pages/...`。同步落盘的图片走同一协议，**无需改动**——路径结构一致。
- **IPC**：新增 `sync` 模块（`src/preload/apis/sync.ts`），暴露 `sync:login` / `sync:status` / `sync:trigger` 等通道。
- **状态推送**：同步状态通过现有主→渲染 IPC 事件推送给设置页。

---

## 6. 服务端 API 契约

### 6.1 服务端存储的数据模型（极薄）

| 表 | 主键 | 字段 | 谁能看明文 |
|----|------|------|-----------|
| **accounts** | `accountId` | `recoveryCodeHash`, `dekEnvelope{salt,nonce,ct}`, `createdAt` | 无人（全密文） |
| **devices** | `deviceId` | `accountId`, `devicePubKey`, `deviceName`, `createdAt`, `revokedAt?` | 无人（公钥不敏感） |
| **blocks** | `blockId`(=sha256(密文)) | `accountId`, `ciphertext`, `size`, `refCount`, `createdAt` | 无人（纯密文） |
| **manifests** | `(accountId, version)` | `ciphertext`, `deviceId`, `receivedAt` | 无人（纯密文） |
| **manifest_head** | `accountId` | `currentVersion`, `updatedAt` | — |

服务端只是个"按 hash 存取密文块"的哑存储。

### 6.2 认证模型（两层）

```
第一层: sessionToken (身份) — 证明"你是某个已注册设备"
        登录/注册时返回, 放 Authorization: Bearer <token>

第二层: 设备密钥签名 (防篡改/防重放) — 关键操作额外签名
        每个变更类请求带:
          X-Device-Id: <deviceId>
          X-Timestamp: <unix ms>
          X-Nonce:     <random 16B hex>
          X-Signature: HMAC(devPriv, method+path+timestamp+nonce+bodyHash)
        服务端: 用 devices.devicePubKey 验签, 校验 timestamp ±60s, nonce 5min 去重
```

读类请求只需 sessionToken；写类请求必须两层都带。

### 6.3 端点总览

| # | 方法 | 路径 | 认证 | 用途 |
|---|------|------|------|------|
| 1 | POST | `/account/register` | 无 | 首设备注册，开账户 |
| 2 | GET | `/account/dek` | 无（限流） | 取 dekEnvelope（新设备登录用） |
| 3 | POST | `/device/register` | 恢复码 | 新设备加入 |
| 4 | PUT | `/account/dek` | 双层 | 改主密码后换 dekEnvelope |
| 5 | DELETE | `/device/{deviceId}` | 双层 | 吊销设备 |
| 6 | GET | `/manifest` | session | 取最新 manifest |
| 7 | PUT | `/manifest` | 双层 | 推送新 manifest |
| 8 | POST | `/blocks/have` | session | 批量查哪些块已存在 |
| 9 | PUT | `/blocks/{blockId}` | 双层 | 上传一个密文块 |
| 10 | GET | `/blocks/{blockId}` | session | 下载一个密文块 |
| 11 | POST | `/sync/poll` | session | 长轮询远端变更 |

### 6.4 关键端点契约

**`POST /account/register`**（首设备开户）

```
请求 (无认证):
{
  "recoveryCodeHash": "<Argon2id(recoveryCode)>",
  "dekEnvelope": { "salt": "<hex16>", "nonce": "<hex24>", "ct": "<hex>" },
  "devicePubKey": "<X25519 pub hex>",
  "deviceName": "Tianyi的MacBook"
}
响应 201:
{
  "accountId": "<uuid>",
  "deviceId":  "<uuid>",
  "sessionToken": "<jwt>",
  "recoveryCode": "AB3K-7P9X-QR2M-8F4T-LN6C"   ← 仅此一次返回明文
}
```

**`GET /account/dek`**（新设备取信封）

```
请求: ?accountId=<uuid>
限流: 同 IP 10次/分钟
响应 200: { "dekEnvelope": { "salt":.., "nonce":.., "ct":.. } }
```

服务端不验证主密码——它没法验证。主密码对错完全由客户端"能不能用 MK 解开 ct"判定。

**`POST /device/register`**（新设备加入）

```
请求 (需 recoveryCode 验证):
{
  "accountId": "<uuid>",
  "recoveryCode": "<明文恢复码>",
  "devicePubKey": "<新设备公钥>",
  "deviceName": "Tianyi的Win台式机"
}
响应 200: { "deviceId":.., "sessionToken":.. }
```

**`PUT /manifest`**（乐观并发控制）

```
请求: { "ciphertext": "<hex>", "baseVersion": 137 }
响应 200: { "version": 138 }
响应 409: { "error": { "code":"stale_base", "currentVersion": 139 } }  ← 并发冲突
```

`baseVersion` 是乐观并发控制：客户端声明"我基于 137 改的"，服务端若发现 head 已是 139，返回 409，客户端拉取 139 重新合并。

**`POST /blocks/have`**（批量查重）

```
请求: { "ids": ["b3a1..","e5c9..", ...] }   (上限 1000 个)
响应 200: { "missing": ["e5c9.."] }
```

**`PUT /blocks/{blockId}`**（上传密文块）

```
body: <原始密文 bytes, Content-Type: application/octet-stream>
响应 201(新建) / 200(已存在, 幂等)
服务端校验: sha256(body) == blockId ? 否则 400
```

blockId 自校验：服务端收到后重算 sha256，与 URL 里的 blockId 不符就拒。

### 6.5 统一错误模型

```
{
  "error": {
    "code": "stale_base",        ← 机器可读的稳定 code
    "message": "base version 137 is behind current 139",
    "currentVersion"?: 139
  }
}
```

| code | HTTP | 含义 | 客户端处理 |
|------|------|------|-----------|
| `bad_recovery_code` | 401 | 恢复码错 | 提示用户重新输入 |
| `dek_unlock_failed` | — | （客户端本地）MK 解不开 DEK | 主密码错，提示重输 |
| `device_revoked` | 401 | 设备已吊销 | 登出，引导重新配对 |
| `stale_base` | 409 | manifest 并发冲突 | 拉最新重新 diff |
| `block_hash_mismatch` | 400 | PUT 的块 hash 对不上 | 重传 |
| `quota_exceeded` | 413 | 超存储配额 | 提示清理或扩容 |
| `rate_limited` | 429 | 限流 | 退避重试 |

### 6.6 限流与配额

| 端点 | 限流 | 理由 |
|------|------|------|
| `GET /account/dek` | 10次/分钟/IP | 防恢复码爆破 |
| `POST /device/register` | 5次/分钟/IP | 防恢复码爆破 |
| `PUT /blocks/*` | 100次/分钟/设备 | 防滥用 |
| 其他写 | 60次/分钟/设备 | 一般保护 |
| 存储配额 | 默认 1GB/账户（服务端可配） | 自建场景磁盘是真金白银 |

### 6.7 块 GC（客户端驱动）

服务端不知道哪些块还被 manifest 引用（它看不见 manifest 内容），所以 GC 由客户端发起：

```
客户端定期(如每周):
  ① GET 历史可访问的 manifest 版本列表
  ② 解密 → 收集所有被引用的 blockId 集合 R
  ③ POST /blocks/gc { "keep": R }
  服务端: 删除 accountId 下不在 R 中的块 (保留近 7 天 manifest 历史以防回滚)
```

### 6.8 传输与加密小结

| 层 | 机制 | 防什么 |
|----|------|--------|
| 网络 | 强制 HTTPS/TLS | 窃听、篡改 |
| 身份 | sessionToken | 未授权访问 |
| 完整性 | 设备密钥签名 + nonce 去重 | 伪造请求、重放 |
| 内容 | E2EE（XChaCha20-Poly1305） | 服务端/运维方看到明文 |
| 块一致性 | blockId=sha256 + AEAD tag | 块被篡改/损坏 |

---

## 7. 实施路线图与风险

### 7.1 三阶段递进

```
阶段 0: 密码学地基         阶段 1: 同步 MVP          阶段 2: 全量与体验
(独立, 可单测)             (能跑通最小闭环)          (打磨到可用)
   │                          │                        │
   ▼                          ▼                        ▼
┌──────────┐            ┌──────────┐             ┌──────────┐
│ 3-4 周    │            │ 4-5 周    │             │ 3-4 周    │
└──────────┘            └──────────┘             └──────────┘
```

阶段不可压缩：阶段 0 是纯密码学必须先立（定义所有后续加解密接口）；阶段 1 用最小数据类型验证架构跑得通；阶段 2 补全全量数据和体验。

### 7.2 阶段 0：密码学地基（3-4 周）

| # | 任务 | 产物 | 验收标准 |
|---|------|------|---------|
| 0.1 | 选型并封装 KDF | `crypto/kdf.ts` | Argon2id(t=3,m=64MiB,p=4) 封装，纯函数 |
| 0.2 | DEK/MK 生命周期 | `crypto/keyManager.ts` | 生成/派生/包装/解包 DEK |
| 0.3 | AEAD 块加解密 | `crypto/aead.ts` | XChaCha20-Poly1305 seal/open，nonce 管理 |
| 0.4 | 设备密钥（X25519） | `crypto/deviceKey.ts` | 生成对、签名、验签 |
| 0.5 | dekEnvelope | `crypto/envelope.ts` | MK 包裹/解开 DEK，与阶段 1 握手对齐 |
| 0.6 | safeStorage 适配 | `crypto/secureStore.ts` | DEK/设备私钥落盘，复用既有 SSH 模式 |

库选型：`libsodium-wrappers`，不自实现原语。

### 7.3 阶段 1：同步 MVP（4-5 周）

目标：跑通"注册→改一条批注→另一台设备看到"的最小闭环。**只同步批注一种数据类型**。

| # | 任务 | 依赖 | 验收标准 |
|---|------|------|---------|
| 1.1 | Lumina Relay 骨架（独立仓库） | 阶段 0 契约 | 实现 9 个核心端点 |
| 1.2 | 分块器（仅"整文件=1块"策略） | — | 小 JSON 切块/加密/blockId |
| 1.3 | manifest 模型 + 本地存储 | 1.2 | 构建/序列化/版本管理 |
| 1.4 | SyncClient（HTTPS+认证） | 阶段 0 | sessionToken/签名/重试/限流 |
| 1.5 | ChangeWatcher（chokidar） | — | 监听 + 500ms 去抖 + 自写抑制 |
| 1.6 | SyncEngine 上行循环 | 1.2-1.5 | 检测→切块→查重→上传→manifest 推进 |
| 1.7 | SyncEngine 下行循环 | 1.6 | 轮询→diff→下载→解密→applySynced* |
| 1.8 | `PaperStorageService.applySyncedAnnotations()` | — | 新增方法，走 WriteQueue |
| 1.9 | 同步设置 UI（最小） | 1.6/1.7 | 注册/登录/状态/手动同步按钮 |

**MVP 验收场景**：

```
设备 A: 注册账户, 导出恢复码
设备 A: 打开论文, 加一条批注 "重要"
设备 B: 用恢复码+主密码登录
设备 B: 打开同一篇论文 → 看到 "重要"        ← 闭环成立
设备 B: 改批注为 "非常重要"
设备 A: 点"立即同步" → 看到 "非常重要"       ← 双向成立
设备 A/B 并发改不同批注 → LWW 仲裁 + 冲突区  ← 冲突成立
```

### 7.4 阶段 2：全量数据 + 体验打磨（3-4 周）

| # | 任务 | 验收标准 |
|---|------|---------|
| 2.1 | 大文本滚动哈希分块 | `merged.md` 局部改只动少数块 |
| 2.2 | 大二进制 4MB 分块 | `source.pdf`/页图分块上传，首次传完不再传 |
| 2.3 | 接入其余数据类型 | sessions / config / knowledge / tool-stats 各加 `applySynced*()` |
| 2.4 | 块 GC | 客户端定期 POST keep 集合，服务端清理 |
| 2.5 | 长轮询（`/sync/poll`） | 多设备近实时（<5s） |
| 2.6 | 冲突区 7 天清理 | 定时任务 + UI 查看冲突副本 |
| 2.7 | 全量校验/修复 | 手动触发，本地 blockId 对账远端 manifest |
| 2.8 | 同步设置 UI 完整版 | 设备管理/改主密码/导出恢复码/存储用量/冲突列表 |

### 7.5 测试策略

| 层次 | 方法 | 覆盖 |
|------|------|------|
| 单元 | `node:test`（项目既有规范） | KDF/AEAD/envelope/chunker 纯函数 |
| 契约 | 客户端和服务端共享一份 OpenAPI/JSON Schema | 端点请求响应一致性 |
| 集成 | 本地起 Lumina Relay + 两个客户端实例模拟双设备 | 注册→同步→冲突闭环 |
| 安全 | 已知向量测试 + 错误注入（篡改密文/重放） | AEAD 拒篡改、签名拒伪造 |

遵循项目规范：测试文件旁置，`.ts` 用 `tsAliasLoader.mjs` loader，新增 `yarn test:sync` 命令。

### 7.6 主要风险与缓解

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| **密码学实现错误** | 中 | 致命 | 用 libsodium 而非自实现；阶段 0 强制单测；关键向量对照参考实现 |
| **E2EE 不可恢复性用户感知差** | 高 | 高 | 注册强制导出恢复码；周期提醒；最后设备注销二次确认 |
| **性能：270MB 首次同步慢** | 高 | 中 | 分块并发上传；断点续传；进度显示；后台进行不阻塞 |
| **chokidar 漏事件/跨平台差异** | 中 | 中 | 定时双向同步兜底；Windows 路径归一化 |
| **manifest 并发冲突频繁** | 中 | 中 | LWW + 冲突区；长轮询降低并发窗口 |
| **服务端运维负担** | 中 | 中 | 服务端极薄；Docker 一键部署；详尽部署文档 |
| **Electron safeStorage 跨平台差异** | 低 | 中 | Linux 无密钥环时回退（复用 SSH 模式）；文档说明 |

**最高优先级风险**：密码学正确性。缓解手段是"绝不自己实现密码学原语"——全程用 libsodium。

### 7.7 开源安全自检清单

- [ ] 仓库无任何硬编码密钥/盐/IV
- [ ] 所有密钥运行时生成或用户输入
- [ ] `safeStorage` 不可用时明确告警而非静默降级
- [ ] 示例配置/文档不含真实凭证
- [ ] 服务端默认配置安全（强制 HTTPS、合理限流）
- [ ] `.gitignore` 覆盖本地密钥/测试账户数据
- [ ] 密码学库版本锁定，跟进安全公告

### 7.8 产出物总览

| 仓库 | 阶段 | 产物 |
|------|------|------|
| **sparrow-manus**（本仓库） | 0-2 | `src/main/services/sync/` 全部客户端代码；`src/preload/apis/sync.ts`；渲染端设置 UI；`yarn test:sync` |
| **lumina-relay**（新建） | 1-2 | 11 端点服务端；Docker 部署；部署文档；OpenAPI 契约 |
| **共享** | 0-2 | 密码学契约文档；API 契约文档（两边共用） |

---

## 8. 设计总结

整个方案的核心脉络：

> **自建服务端 + E2EE + 内容寻址分块 + LWW**，以"恢复码（服务端可验证）管归属、主密码（服务端不可见）管解密"的正交身份模型，在开源约束下实现"服务端只看到密文"的信任边界，用三类分块策略让约 326MB 全量数据支持高效增量同步。

**关键设计抉择的理由**：

- **为什么端到端加密而非传输加密+明文存储**：开源项目下，用户对"服务端运营者也看不到我的论文"的信任度是采用意愿的核心。E2EE 是这个信任的唯一技术保证。
- **为什么恢复码而非邮箱**：自建服务端常发不出邮件（SMTP 限制），且邮箱是隐私信息。恢复码零外部依赖、匿名性强，最契合自托管和开源气质。
- **为什么内容寻址分块**：全量同步 326MB 若整体上传，每次改动都重传全量，不可接受。内容寻址让"改一条批注 = 上传几 KB"，是全量同步可行的工程基础。
- **为什么 LWW 而非 CRDT/多版本**：论文阅读场景下"多设备同时编辑同一项"概率极低，LWW + 冲突区兜底已足够，避免 CRDT 的实现复杂度和 UI 负担。
- **为什么主密码+设备密钥双层而非 Signal 式**：双层模型让"改密码不重传数据"和"可吊销设备"同时成立，且实现远比 Signal 式授权简单，是 MVP 性价比最高的选择。
