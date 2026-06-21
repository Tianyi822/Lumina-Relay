-- devices 增加 last_seen_at：记录设备最后活跃时间（Session 认证时更新）。
-- 先加 nullable 列，回填历史行为 created_at，再由代码保证新写入非空。
ALTER TABLE devices ADD COLUMN last_seen_at INTEGER;
UPDATE devices SET last_seen_at = created_at WHERE last_seen_at IS NULL;
