-- accounts 增加恢复码失败锁定字段（C3：防恢复码爆破）。
-- recovery_fail_count：连续失败次数，成功后重置为 0。
-- recovery_locked_until：锁定到期时间戳（Unix 秒），0 表示未锁。
-- 均带默认值，迁移对历史数据无副作用。
ALTER TABLE accounts ADD COLUMN recovery_fail_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN recovery_locked_until INTEGER NOT NULL DEFAULT 0;
