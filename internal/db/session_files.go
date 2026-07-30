// session_files.go 维护会话 JSONL 文件与 index.json 的注册表。
//
// 服务端不解析文件内容，只登记 (version, size, updated_at) 做 CAS 并发控制；
// 文件字节由 store.SessionStore 落盘。所有写操作在同一写事务内复核调用设备
// 仍为 active、仍属 session 中的账号和同步组（AGENTS 安全约束）。
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type SessionFileRow struct {
	SessionID string
	Version   int64
	Size      int64
	UpdatedAt int64
}

// SessionIndexRow 是 index.json 的注册表行（每同步组一行）。
type SessionIndexRow struct {
	Version   int64
	Size      int64
	UpdatedAt int64
}

// SessionFilePutResult 与 ManifestPutResult 同构：CAS 冲突时带回当前版本。
type SessionFilePutResult struct {
	Version        int64
	CurrentVersion int64
	Conflict       bool
}

func (q *Queries) ListSessionFiles(ctx context.Context, accountID, groupID string) ([]SessionFileRow, error) {
	rows, err := q.db.QueryxContext(ctx, `
SELECT session_id, version, size, updated_at
FROM session_files
WHERE account_id = ? AND sync_group_id = ?
ORDER BY session_id`, accountID, groupID)
	if err != nil {
		return nil, fmt.Errorf("列出会话文件：%w", err)
	}
	defer rows.Close()
	var out []SessionFileRow
	for rows.Next() {
		var item SessionFileRow
		if err := rows.Scan(&item.SessionID, &item.Version, &item.Size, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("扫描会话文件：%w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (q *Queries) GetSessionFile(ctx context.Context, accountID, groupID, sessionID string) (SessionFileRow, error) {
	var out SessionFileRow
	err := q.db.QueryRowxContext(ctx, `
SELECT session_id, version, size, updated_at
FROM session_files
WHERE account_id = ? AND sync_group_id = ? AND session_id = ?`,
		accountID, groupID, sessionID).Scan(
		&out.SessionID, &out.Version, &out.Size, &out.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionFileRow{}, sql.ErrNoRows
		}
		return SessionFileRow{}, fmt.Errorf("读取会话文件注册表：%w", err)
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

// UpsertSessionFileCAS 以 baseVersion 做 CAS 推进会话文件版本：
//   - 行不存在时要求 baseVersion == 0，插入 version=1；
//   - 行存在时要求 version == baseVersion，推进到 baseVersion+1；
//   - 不匹配返回 Conflict=true 与 CurrentVersion（行不存在时为 0）。
//
// 追加与全量重写共用此 CAS（newSize 语义分别为「追加后总大小」与「新文件大小」）。
func (q *Queries) UpsertSessionFileCAS(
	ctx context.Context,
	accountID, groupID, deviceID, sessionID string,
	baseVersion, newSize, now int64,
) (SessionFilePutResult, error) {
	var out SessionFilePutResult
	err := q.WithTx(ctx, func(txq *Queries) error {
		if err := txq.revalidateSessionWriter(ctx, accountID, groupID, deviceID); err != nil {
			return err
		}
		var current int64
		err := txq.db.QueryRowxContext(ctx, `
SELECT version FROM session_files
WHERE account_id = ? AND sync_group_id = ? AND session_id = ?`,
			accountID, groupID, sessionID).Scan(&current)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if baseVersion != 0 {
				out = SessionFilePutResult{CurrentVersion: 0, Conflict: true}
				return nil
			}
			if _, err := txq.db.ExecContext(ctx, `
INSERT INTO session_files (account_id, sync_group_id, session_id, version, size, updated_at)
VALUES (?, ?, ?, 1, ?, ?)`, accountID, groupID, sessionID, newSize, now); err != nil {
				return fmt.Errorf("创建会话文件注册行：%w", err)
			}
			out = SessionFilePutResult{Version: 1, CurrentVersion: 1}
			return nil
		case err != nil:
			return fmt.Errorf("读取会话文件版本：%w", err)
		}
		if current != baseVersion {
			out = SessionFilePutResult{CurrentVersion: current, Conflict: true}
			return nil
		}
		next := current + 1
		result, err := txq.db.ExecContext(ctx, `
UPDATE session_files SET version = ?, size = ?, updated_at = ?
WHERE account_id = ? AND sync_group_id = ? AND session_id = ? AND version = ?`,
			next, newSize, now, accountID, groupID, sessionID, current)
		if err != nil {
			return fmt.Errorf("推进会话文件版本：%w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("推进会话文件版本：CAS 未命中")
		}
		out = SessionFilePutResult{Version: next, CurrentVersion: next}
		return nil
	})
	return out, err
}

// DeleteSessionFile 删除注册行；不存在返回 false。事务内复核设备归属。
func (q *Queries) DeleteSessionFile(
	ctx context.Context,
	accountID, groupID, deviceID, sessionID string,
) (bool, error) {
	var deleted bool
	err := q.WithTx(ctx, func(txq *Queries) error {
		if err := txq.revalidateSessionWriter(ctx, accountID, groupID, deviceID); err != nil {
			return err
		}
		result, err := txq.db.ExecContext(ctx, `
DELETE FROM session_files
WHERE account_id = ? AND sync_group_id = ? AND session_id = ?`,
			accountID, groupID, sessionID)
		if err != nil {
			return fmt.Errorf("删除会话文件注册行：%w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		deleted = affected == 1
		return nil
	})
	return deleted, err
}

func (q *Queries) GetSessionIndex(ctx context.Context, accountID, groupID string) (SessionIndexRow, error) {
	var out SessionIndexRow
	err := q.db.QueryRowxContext(ctx, `
SELECT version, size, updated_at
FROM session_indexes
WHERE account_id = ? AND sync_group_id = ?`,
		accountID, groupID).Scan(&out.Version, &out.Size, &out.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SessionIndexRow{}, sql.ErrNoRows
		}
		return SessionIndexRow{}, fmt.Errorf("读取会话索引注册表：%w", err)
	}
	return out, nil
}

// UpsertSessionIndexCAS 与 UpsertSessionFileCAS 同语义，作用于 session_indexes。
func (q *Queries) UpsertSessionIndexCAS(
	ctx context.Context,
	accountID, groupID, deviceID string,
	baseVersion, newSize, now int64,
) (SessionFilePutResult, error) {
	var out SessionFilePutResult
	err := q.WithTx(ctx, func(txq *Queries) error {
		if err := txq.revalidateSessionWriter(ctx, accountID, groupID, deviceID); err != nil {
			return err
		}
		var current int64
		err := txq.db.QueryRowxContext(ctx, `
SELECT version FROM session_indexes
WHERE account_id = ? AND sync_group_id = ?`,
			accountID, groupID).Scan(&current)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if baseVersion != 0 {
				out = SessionFilePutResult{CurrentVersion: 0, Conflict: true}
				return nil
			}
			if _, err := txq.db.ExecContext(ctx, `
INSERT INTO session_indexes (account_id, sync_group_id, version, size, updated_at)
VALUES (?, ?, 1, ?, ?)`, accountID, groupID, newSize, now); err != nil {
				return fmt.Errorf("创建会话索引注册行：%w", err)
			}
			out = SessionFilePutResult{Version: 1, CurrentVersion: 1}
			return nil
		case err != nil:
			return fmt.Errorf("读取会话索引版本：%w", err)
		}
		if current != baseVersion {
			out = SessionFilePutResult{CurrentVersion: current, Conflict: true}
			return nil
		}
		next := current + 1
		result, err := txq.db.ExecContext(ctx, `
UPDATE session_indexes SET version = ?, size = ?, updated_at = ?
WHERE account_id = ? AND sync_group_id = ? AND version = ?`,
			next, newSize, now, accountID, groupID, current)
		if err != nil {
			return fmt.Errorf("推进会话索引版本：%w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("推进会话索引版本：CAS 未命中")
		}
		out = SessionFilePutResult{Version: next, CurrentVersion: next}
		return nil
	})
	return out, err
}
