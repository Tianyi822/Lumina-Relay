-- SQLite 老版本不支持 DROP COLUMN，用重建表方式回滚。
CREATE TABLE devices_old_dropped AS SELECT
    device_id, account_id, device_pub_key, device_name, created_at, revoked_at
FROM devices;
DROP TABLE devices;
CREATE TABLE devices (
    device_id       TEXT    PRIMARY KEY,
    account_id      TEXT    NOT NULL REFERENCES accounts(account_id),
    device_pub_key  TEXT    NOT NULL,
    device_name     TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    revoked_at      INTEGER
);
INSERT INTO devices (device_id, account_id, device_pub_key, device_name, created_at, revoked_at)
SELECT device_id, account_id, device_pub_key, device_name, created_at, revoked_at
FROM devices_old_dropped;
DROP TABLE devices_old_dropped;
