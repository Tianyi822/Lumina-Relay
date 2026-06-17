-- lumina-relay 初始 schema（sync-design §6.1 / data-layer spec §3）
-- 五表：accounts / devices / blocks / manifests / manifest_head
-- 服务端为"按 hash 存取密文块"的哑存储：blocks 表只存元数据，密文内容在文件系统。

CREATE TABLE accounts (
    account_id          TEXT    PRIMARY KEY,
    recovery_code_hash  BLOB    NOT NULL,
    dek_salt            BLOB    NOT NULL,
    dek_nonce           BLOB    NOT NULL,
    dek_ct              BLOB    NOT NULL,
    created_at          INTEGER NOT NULL
);

CREATE TABLE devices (
    device_id       TEXT    PRIMARY KEY,
    account_id      TEXT    NOT NULL REFERENCES accounts(account_id),
    device_pub_key  TEXT    NOT NULL,
    device_name     TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    revoked_at      INTEGER
);

CREATE TABLE blocks (
    block_id    TEXT    PRIMARY KEY,
    account_id  TEXT    NOT NULL REFERENCES accounts(account_id),
    size        INTEGER NOT NULL,
    ref_count   INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);

CREATE TABLE manifests (
    account_id  TEXT    NOT NULL REFERENCES accounts(account_id),
    version     INTEGER NOT NULL,
    ciphertext  BLOB    NOT NULL,
    device_id   TEXT    NOT NULL,
    received_at INTEGER NOT NULL,
    PRIMARY KEY (account_id, version)
);

CREATE TABLE manifest_head (
    account_id      TEXT    PRIMARY KEY REFERENCES accounts(account_id),
    current_version INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
