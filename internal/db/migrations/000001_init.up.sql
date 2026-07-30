-- Lumina Relay 无版本协议初始 schema。
-- 服务端只保存密码学验证材料、同步组关系、密文 Manifest 和密文块。

CREATE TABLE relay_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE accounts (
    account_id               TEXT    PRIMARY KEY,
    username                 TEXT    NOT NULL UNIQUE,
    auth_salt                BLOB    NOT NULL CHECK (length(auth_salt) = 16),
    login_public_key         BLOB    NOT NULL CHECK (length(login_public_key) = 32),
    dek_envelope             BLOB    NOT NULL CHECK (length(dek_envelope) = 72),
    account_auth_public_key  BLOB    NOT NULL CHECK (length(account_auth_public_key) = 32),
    crypto_state_revision    INTEGER NOT NULL DEFAULT 1 CHECK (crypto_state_revision >= 1),
    dek_epoch                INTEGER NOT NULL DEFAULT 1 CHECK (dek_epoch >= 1),
    quota_bytes              INTEGER NOT NULL CHECK (quota_bytes >= 0),
    used_bytes               INTEGER NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
    created_at               INTEGER NOT NULL
);

CREATE TABLE sync_groups (
    group_id    TEXT    PRIMARY KEY,
    account_id  TEXT    NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_sync_groups_account ON sync_groups(account_id);

CREATE TABLE devices (
    device_id           TEXT    PRIMARY KEY,
    account_id          TEXT    NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    sync_group_id       TEXT    REFERENCES sync_groups(group_id) ON DELETE SET NULL,
    signing_public_key  BLOB    NOT NULL CHECK (length(signing_public_key) = 32),
    device_name         TEXT    NOT NULL,
    status              TEXT    NOT NULL DEFAULT 'active'
                                CHECK (status IN ('active', 'revoked')),
    key_version         INTEGER NOT NULL DEFAULT 1 CHECK (key_version >= 1),
    created_at          INTEGER NOT NULL,
    last_seen_at        INTEGER NOT NULL,
    revoked_at          INTEGER
);
CREATE INDEX idx_devices_account_group ON devices(account_id, sync_group_id);

CREATE TABLE manifest_heads (
    device_id        TEXT    PRIMARY KEY REFERENCES devices(device_id) ON DELETE CASCADE,
    current_version  INTEGER NOT NULL DEFAULT 0 CHECK (current_version >= 0),
    updated_at       INTEGER NOT NULL
);

CREATE TABLE manifests (
    device_id        TEXT    NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    version          INTEGER NOT NULL CHECK (version > 0),
    ciphertext       BLOB    NOT NULL,
    ciphertext_hash  BLOB    NOT NULL CHECK (length(ciphertext_hash) = 32),
    received_at      INTEGER NOT NULL,
    PRIMARY KEY (device_id, version)
);

CREATE TABLE sync_codes (
    code_id            TEXT    PRIMARY KEY,
    account_id         TEXT    NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    sync_group_id      TEXT    NOT NULL REFERENCES sync_groups(group_id) ON DELETE CASCADE,
    inviter_device_id  TEXT    NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    code_mac            BLOB    NOT NULL CHECK (length(code_mac) = 32),
    expires_at          INTEGER NOT NULL,
    consumed_at         INTEGER,
    created_at          INTEGER NOT NULL
);
CREATE INDEX idx_sync_codes_inviter ON sync_codes(inviter_device_id, expires_at);
CREATE UNIQUE INDEX idx_sync_codes_active_mac
    ON sync_codes(account_id, code_mac)
    WHERE consumed_at IS NULL;

CREATE TABLE request_nonces (
    device_id   TEXT    NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    nonce_hash  BLOB    NOT NULL CHECK (length(nonce_hash) = 32),
    expires_at  INTEGER NOT NULL,
    PRIMARY KEY (device_id, nonce_hash)
);
CREATE INDEX idx_request_nonces_expiry ON request_nonces(expires_at);

CREATE TABLE block_objects (
    block_id     TEXT    PRIMARY KEY CHECK (length(block_id) = 64),
    size         INTEGER NOT NULL CHECK (size >= 0),
    state        TEXT    NOT NULL DEFAULT 'active'
                         CHECK (state IN ('active', 'deleting')),
    orphaned_at  INTEGER,
    created_at   INTEGER NOT NULL
);
CREATE INDEX idx_block_objects_gc ON block_objects(state, orphaned_at);

CREATE TABLE account_blocks (
    account_id  TEXT    NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    block_id    TEXT    NOT NULL REFERENCES block_objects(block_id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (account_id, block_id)
);

CREATE TABLE device_blocks (
    device_id   TEXT    NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    block_id    TEXT    NOT NULL REFERENCES block_objects(block_id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (device_id, block_id)
);
CREATE INDEX idx_device_blocks_block ON device_blocks(block_id);

-- 会话 JSONL 文件注册表：服务端只记 (version, size, updated_at)，
-- 文件字节存在 sessions/<accountId>/<groupId>/ 目录下（不透明，不解析行内容）。
-- 注册表是 (version, size) 的唯一权威，读路径按 size 截断屏蔽崩溃残留字节。
CREATE TABLE session_files (
    account_id     TEXT    NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    sync_group_id  TEXT    NOT NULL REFERENCES sync_groups(group_id) ON DELETE CASCADE,
    session_id     TEXT    NOT NULL,
    version        INTEGER NOT NULL CHECK (version >= 1),
    size           INTEGER NOT NULL CHECK (size >= 0),
    updated_at     INTEGER NOT NULL,
    PRIMARY KEY (account_id, sync_group_id, session_id)
);

-- 会话索引文件（index.json）注册表：每同步组一行，全量重写、无追加。
CREATE TABLE session_indexes (
    account_id     TEXT    NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    sync_group_id  TEXT    NOT NULL REFERENCES sync_groups(group_id) ON DELETE CASCADE,
    version        INTEGER NOT NULL CHECK (version >= 1),
    size           INTEGER NOT NULL CHECK (size >= 0),
    updated_at     INTEGER NOT NULL,
    PRIMARY KEY (account_id, sync_group_id)
);

CREATE TABLE upload_reservations (
    reservation_id  TEXT    PRIMARY KEY,
    account_id      TEXT    NOT NULL REFERENCES accounts(account_id) ON DELETE CASCADE,
    device_id       TEXT    NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    block_id        TEXT    NOT NULL,
    size            INTEGER NOT NULL CHECK (size >= 0),
    expires_at      INTEGER NOT NULL,
    created_at      INTEGER NOT NULL
);
CREATE INDEX idx_upload_reservations_expiry ON upload_reservations(expires_at);
CREATE UNIQUE INDEX idx_upload_reservations_account_block
    ON upload_reservations(account_id, block_id);
