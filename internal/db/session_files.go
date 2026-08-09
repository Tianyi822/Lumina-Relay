// session_files.go 维护会话密文快照（SQLite 内原子存储）。
//
// 服务端只保存客户端生成的完整密文 BLOB，不解析、不解密；sessionId 在
// 账号内唯一（客户端全局生成）。所有写操作在同一写事务内完成设备复核、
// CAS 版本推进与账户配额调整（AGENTS 安全约束）。
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SessionFileRow 是会话快照行；List 只填元数据，Get 额外携带密文。
type SessionFileRow struct {
	SessionID  string
	Version    int64
	Size       int64
	UpdatedAt  int64
	Ciphertext []byte
}

// SessionFilePutResult 与 ManifestPutResult 同构：CAS 冲突时带回当前版本。
type SessionFilePutResult struct {
	Version        int64
	CurrentVersion int64
	Size           int64
	Conflict       bool
}

// SessionFileDeleteResult 描述 CAS 删除的结果：
// 行不存在时 Deleted=false 且 Conflict=false（幂等删除）。
type SessionFileDeleteResult struct {
	Deleted        bool
	CurrentVersion int64
	ReclaimedBytes int64
	Conflict       bool
}

// ListSessionFiles 只返回元数据（不含密文），按 session_id 排序。
func (q *Queries) ListSessionFiles(ctx context.Context, accountID, groupID string) ([]SessionFileRow, error) {
	rows, err := q.db.QueryxContext(ctx, `
SELECT session_id, version, size, updated_at
FROM session_files
WHERE account_id = ? AND sync_group_id = ?
ORDER BY session_id`, accountID, groupID)
	if err != nil {
		return nil, fmt.Errorf("列出会话快照：%w", err)
	}
	defer rows.Close()
	var out []SessionFileRow
	for rows.Next() {
		var item SessionFileRow
		if err := rows.Scan(&item.SessionID, &item.Version, &item.Size, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("扫描会话快照：%w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetSessionFile 读取完整快照（含密文）。其他组占用同 ID 时视为不存在。
func (q *Queries) GetSessionFile(ctx context.Context, accountID, groupID, sessionID string) (SessionFileRow, error) {
	var out SessionFileRow
	err := q.db.QueryRowxContext(ctx, `
SELECT session_id, version, size, updated_at, ciphertext
FROM session_files
WHERE account_id = ? AND sync_group_id = ? AND session_id = ?`,
		accountID, groupID, sessionID).Scan(
		&out.SessionID, &out.Version, &out.Size, &out.UpdatedAt, &out.Ciphertext)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionFileRow{}, sql.ErrNoRows
		}
		return SessionFileRow{}, fmt.Errorf("读取会话快照：%w", err)
	}
	return out, nil
}

// revalidateSessionWriter 在写事务内复核设备与同步组归属。
// 必须在 WithTx 内调用；违反归属返回 ErrGroupChanged / ErrInactiveDevice。
func (q *Queries) revalidateSessionWriter(ctx context.Context, accountID, groupID, deviceID string) error {
	device, err := q.GetDevice(ctx, deviceID)
	if err != nil || device.AccountID != accountID ||
		!device.SyncGroupID.Valid || device.SyncGroupID.String != groupID {
		return ErrGroupChanged
	}
	if device.Status != "active" {
		return ErrInactiveDevice
	}
	group, err := q.GetSyncGroup(ctx, groupID)
	if err != nil || group.AccountID != accountID {
		return ErrGroupChanged
	}
	return nil
}

// PutSessionFileCAS 在单个写事务内原子提交密文快照：
//   - 行不存在时要求 baseVersion == 0，插入 version=1；
//   - 行存在时要求 version == baseVersion，推进到 baseVersion+1 并整体替换密文；
//   - 版本不匹配返回 Conflict=true 与 CurrentVersion（行不存在时为 0）；
//   - 同账号其他组占用同 ID → ErrSessionIDConflict；
//   - 增长部分连同 active block reservations 一起做配额检查，超限
//     ErrQuotaExceeded；提交时同事务调整 accounts.used_bytes。
func (q *Queries) PutSessionFileCAS(
	ctx context.Context,
	accountID, groupID, deviceID, sessionID string,
	baseVersion int64,
	ciphertext []byte,
	now int64,
) (SessionFilePutResult, error) {
	var out SessionFilePutResult
	err := q.WithTx(ctx, func(txq *Queries) error {
		if err := txq.revalidateSessionWriter(ctx, accountID, groupID, deviceID); err != nil {
			return err
		}
		// 与块上传共用同一口径：先回收过期 reservation 再统计配额。
		if _, err := txq.db.ExecContext(ctx,
			`DELETE FROM upload_reservations WHERE expires_at <= ?`, now); err != nil {
			return fmt.Errorf("清理过期上传 reservation：%w", err)
		}

		// 按账号级主键查询：其他组占用同 ID 是业务错误而非 CAS 冲突。
		var currentGroupID string
		var currentVersion, oldSize int64
		err := txq.db.QueryRowxContext(ctx, `
SELECT sync_group_id, version, size FROM session_files
WHERE account_id = ? AND session_id = ?`,
			accountID, sessionID).Scan(&currentGroupID, &currentVersion, &oldSize)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if baseVersion != 0 {
				out = SessionFilePutResult{CurrentVersion: 0, Conflict: true}
				return nil
			}
		case err != nil:
			return fmt.Errorf("读取会话快照版本：%w", err)
		case currentGroupID != groupID:
			return ErrSessionIDConflict
		case currentVersion != baseVersion:
			out = SessionFilePutResult{CurrentVersion: currentVersion, Conflict: true}
			return nil
		}

		newSize := int64(len(ciphertext))
		delta := newSize - oldSize
		if delta > 0 {
			var used, quota, reserved int64
			if err := txq.db.QueryRowxContext(ctx, `
SELECT used_bytes, quota_bytes FROM accounts WHERE account_id = ?`,
				accountID).Scan(&used, &quota); err != nil {
				return fmt.Errorf("读取账户配额：%w", err)
			}
			// 与 ReserveBlockUpload 相同的 NOT EXISTS 条件统计 active reservations。
			if err := txq.db.QueryRowxContext(ctx, `
SELECT COALESCE(SUM(r.size), 0)
FROM upload_reservations r
WHERE r.account_id = ?
  AND NOT EXISTS (
      SELECT 1 FROM account_blocks a
      WHERE a.account_id = r.account_id AND a.block_id = r.block_id
  )`, accountID).Scan(&reserved); err != nil {
				return fmt.Errorf("统计上传 reservation：%w", err)
			}
			if used+reserved+delta > quota {
				return ErrQuotaExceeded
			}
		}

		if currentVersion == 0 {
			if _, err := txq.db.ExecContext(ctx, `
INSERT INTO session_files (account_id, sync_group_id, session_id, version, ciphertext, size, updated_at)
VALUES (?, ?, ?, 1, ?, ?, ?)`,
				accountID, groupID, sessionID, ciphertext, newSize, now); err != nil {
				return fmt.Errorf("创建会话快照：%w", err)
			}
			out = SessionFilePutResult{Version: 1, CurrentVersion: 1, Size: newSize}
		} else {
			next := currentVersion + 1
			result, err := txq.db.ExecContext(ctx, `
UPDATE session_files SET version = ?, ciphertext = ?, size = ?, updated_at = ?
WHERE account_id = ? AND session_id = ? AND version = ?`,
				next, ciphertext, newSize, now, accountID, sessionID, currentVersion)
			if err != nil {
				return fmt.Errorf("推进会话快照版本：%w", err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected != 1 {
				return fmt.Errorf("推进会话快照版本：CAS 未命中")
			}
			out = SessionFilePutResult{Version: next, CurrentVersion: next, Size: newSize}
		}
		if delta != 0 {
			if _, err := txq.db.ExecContext(ctx, `
UPDATE accounts SET used_bytes = used_bytes + ? WHERE account_id = ?`,
				delta, accountID); err != nil {
				return fmt.Errorf("调整账户已用配额：%w", err)
			}
		}
		return nil
	})
	if err != nil {
		return SessionFilePutResult{}, err
	}
	return out, nil
}

// DeleteSessionFileCAS 以 baseVersion 做 CAS 删除快照并释放配额：
//   - 当前组无此行 → Deleted=false（幂等）；
//   - 版本不匹配 → Conflict=true 与 CurrentVersion；
//   - 匹配 → 删除行并在同事务扣减 accounts.used_bytes。
func (q *Queries) DeleteSessionFileCAS(
	ctx context.Context,
	accountID, groupID, deviceID, sessionID string,
	baseVersion int64,
) (SessionFileDeleteResult, error) {
	var out SessionFileDeleteResult
	err := q.WithTx(ctx, func(txq *Queries) error {
		if err := txq.revalidateSessionWriter(ctx, accountID, groupID, deviceID); err != nil {
			return err
		}
		var currentVersion, size int64
		err := txq.db.QueryRowxContext(ctx, `
SELECT version, size FROM session_files
WHERE account_id = ? AND sync_group_id = ? AND session_id = ?`,
			accountID, groupID, sessionID).Scan(&currentVersion, &size)
		if errors.Is(err, sql.ErrNoRows) {
			out = SessionFileDeleteResult{}
			return nil
		}
		if err != nil {
			return fmt.Errorf("读取会话快照版本：%w", err)
		}
		if currentVersion != baseVersion {
			out = SessionFileDeleteResult{CurrentVersion: currentVersion, Conflict: true}
			return nil
		}
		result, err := txq.db.ExecContext(ctx, `
DELETE FROM session_files
WHERE account_id = ? AND sync_group_id = ? AND session_id = ? AND version = ?`,
			accountID, groupID, sessionID, currentVersion)
		if err != nil {
			return fmt.Errorf("删除会话快照：%w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("删除会话快照：CAS 未命中")
		}
		if _, err := txq.db.ExecContext(ctx, `
UPDATE accounts SET used_bytes = used_bytes - ? WHERE account_id = ?`,
			size, accountID); err != nil {
			return fmt.Errorf("释放账户已用配额：%w", err)
		}
		out = SessionFileDeleteResult{Deleted: true, ReclaimedBytes: size}
		return nil
	})
	if err != nil {
		return SessionFileDeleteResult{}, err
	}
	return out, nil
}
