package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var (
	ErrInvalidSyncCode  = errors.New("invalid sync code")
	ErrGroupChanged     = errors.New("sync group changed")
	ErrQuotaExceeded    = errors.New("quota exceeded")
	ErrUploadInProgress = errors.New("upload in progress")
	ErrInactiveDevice   = errors.New("inactive device")
)

// Queries 同时支持 *sqlx.DB 与事务内的 *sqlx.Tx。
type Queries struct {
	db sqlx.ExtContext
}

func New(database *sqlx.DB) *Queries {
	return &Queries{db: database}
}

// WithTx 使用 Open 中配置的 BEGIN IMMEDIATE，串行化“先读后写”事务。
func (q *Queries) WithTx(ctx context.Context, fn func(*Queries) error) error {
	database, ok := q.db.(*sqlx.DB)
	if !ok {
		return fmt.Errorf("WithTx 仅允许在 *sqlx.DB 上调用，实际类型 %T", q.db)
	}
	tx, err := database.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务：%w", err)
	}
	txq := &Queries{db: tx}
	if err := fn(txq); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return fmt.Errorf("回滚事务失败（原错误 %v）：%w", err, rollbackErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务：%w", err)
	}
	return nil
}

type AccountRow struct {
	AccountID            string
	Username             string
	AuthSalt             []byte
	LoginPublicKey       []byte
	DEKEnvelope          []byte
	AccountAuthPublicKey []byte
	CryptoStateRevision  int64
	DEKEpoch             int64
	QuotaBytes           int64
	UsedBytes            int64
	CreatedAt            int64
}

func (q *Queries) GetAccountByUsername(ctx context.Context, username string) (AccountRow, error) {
	return q.scanAccount(q.db.QueryRowxContext(ctx, `
SELECT account_id, username, auth_salt, login_public_key, dek_envelope,
       account_auth_public_key, crypto_state_revision, dek_epoch,
       quota_bytes, used_bytes, created_at
FROM accounts WHERE username = ?`, username))
}

func (q *Queries) GetAccount(ctx context.Context, accountID string) (AccountRow, error) {
	return q.scanAccount(q.db.QueryRowxContext(ctx, `
SELECT account_id, username, auth_salt, login_public_key, dek_envelope,
       account_auth_public_key, crypto_state_revision, dek_epoch,
       quota_bytes, used_bytes, created_at
FROM accounts WHERE account_id = ?`, accountID))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (q *Queries) scanAccount(row rowScanner) (AccountRow, error) {
	var out AccountRow
	if err := row.Scan(
		&out.AccountID, &out.Username, &out.AuthSalt, &out.LoginPublicKey,
		&out.DEKEnvelope, &out.AccountAuthPublicKey, &out.CryptoStateRevision,
		&out.DEKEpoch, &out.QuotaBytes, &out.UsedBytes, &out.CreatedAt,
	); err != nil {
		return AccountRow{}, fmt.Errorf("读取账户：%w", err)
	}
	return out, nil
}

type CreateAccountParams struct {
	AccountID            string
	Username             string
	AuthSalt             []byte
	LoginPublicKey       []byte
	DEKEnvelope          []byte
	AccountAuthPublicKey []byte
	QuotaBytes           int64
	CreatedAt            int64
}

func (q *Queries) InsertAccount(ctx context.Context, p CreateAccountParams) error {
	_, err := q.db.ExecContext(ctx, `
INSERT INTO accounts (
    account_id, username, auth_salt, login_public_key, dek_envelope,
    account_auth_public_key, crypto_state_revision, dek_epoch,
    quota_bytes, used_bytes, created_at
) VALUES (?, ?, ?, ?, ?, ?, 1, 1, ?, 0, ?)`,
		p.AccountID, p.Username, p.AuthSalt, p.LoginPublicKey, p.DEKEnvelope,
		p.AccountAuthPublicKey, p.QuotaBytes, p.CreatedAt)
	if err != nil {
		return fmt.Errorf("创建账户：%w", err)
	}
	return nil
}

type DeviceRow struct {
	DeviceID         string
	AccountID        string
	SyncGroupID      sql.NullString
	SigningPublicKey []byte
	DeviceName       string
	Status           string
	KeyVersion       int64
	CreatedAt        int64
	LastSeenAt       int64
	RevokedAt        sql.NullInt64
}

func (q *Queries) GetDevice(ctx context.Context, deviceID string) (DeviceRow, error) {
	var out DeviceRow
	err := q.db.QueryRowxContext(ctx, `
SELECT device_id, account_id, sync_group_id, signing_public_key, device_name,
       status, key_version, created_at, last_seen_at, revoked_at
FROM devices WHERE device_id = ?`, deviceID).Scan(
		&out.DeviceID, &out.AccountID, &out.SyncGroupID, &out.SigningPublicKey,
		&out.DeviceName, &out.Status, &out.KeyVersion, &out.CreatedAt,
		&out.LastSeenAt, &out.RevokedAt,
	)
	if err != nil {
		return DeviceRow{}, fmt.Errorf("读取设备：%w", err)
	}
	return out, nil
}

type CreateDeviceParams struct {
	DeviceID         string
	AccountID        string
	SyncGroupID      string
	SigningPublicKey []byte
	DeviceName       string
	CreatedAt        int64
}

func (q *Queries) InsertDevice(ctx context.Context, p CreateDeviceParams) error {
	_, err := q.db.ExecContext(ctx, `
INSERT INTO devices (
    device_id, account_id, sync_group_id, signing_public_key, device_name,
    status, key_version, created_at, last_seen_at
) VALUES (?, ?, ?, ?, ?, 'active', 1, ?, ?)`,
		p.DeviceID, p.AccountID, p.SyncGroupID, p.SigningPublicKey,
		p.DeviceName, p.CreatedAt, p.CreatedAt)
	if err != nil {
		return fmt.Errorf("创建设备：%w", err)
	}
	return nil
}

func (q *Queries) InsertSyncGroup(ctx context.Context, groupID, accountID string, now int64) error {
	_, err := q.db.ExecContext(ctx, `
INSERT INTO sync_groups (group_id, account_id, revision, created_at)
VALUES (?, ?, 1, ?)`, groupID, accountID, now)
	if err != nil {
		return fmt.Errorf("创建同步组：%w", err)
	}
	return nil
}

func (q *Queries) InsertManifestHead(ctx context.Context, deviceID string, now int64) error {
	_, err := q.db.ExecContext(ctx, `
INSERT INTO manifest_heads (device_id, current_version, updated_at)
VALUES (?, 0, ?)`, deviceID, now)
	if err != nil {
		return fmt.Errorf("初始化 Manifest head：%w", err)
	}
	return nil
}

// CreateDeviceEnrollment 原子创建一个独立同步组、设备和设备级 Manifest head。
func (q *Queries) CreateDeviceEnrollment(ctx context.Context, p CreateDeviceParams) error {
	return q.WithTx(ctx, func(txq *Queries) error {
		if err := txq.InsertSyncGroup(ctx, p.SyncGroupID, p.AccountID, p.CreatedAt); err != nil {
			return err
		}
		if err := txq.InsertDevice(ctx, p); err != nil {
			return err
		}
		return txq.InsertManifestHead(ctx, p.DeviceID, p.CreatedAt)
	})
}

func (q *Queries) TouchDeviceLastSeen(ctx context.Context, deviceID string, now int64) error {
	_, err := q.db.ExecContext(ctx,
		`UPDATE devices SET last_seen_at = ? WHERE device_id = ?`, now, deviceID)
	if err != nil {
		return fmt.Errorf("更新设备活跃时间：%w", err)
	}
	return nil
}

func (q *Queries) RevokeDevice(
	ctx context.Context,
	accountID, callerDeviceID, groupID, deviceID string,
	now int64,
) (bool, error) {
	var revoked bool
	err := q.WithTx(ctx, func(txq *Queries) error {
		caller, err := txq.GetDevice(ctx, callerDeviceID)
		if err != nil || caller.AccountID != accountID || !caller.SyncGroupID.Valid ||
			caller.SyncGroupID.String != groupID {
			return ErrGroupChanged
		}
		if caller.Status != "active" {
			return ErrInactiveDevice
		}
		result, err := txq.db.ExecContext(ctx, `
UPDATE devices
SET status = 'revoked', revoked_at = COALESCE(revoked_at, ?)
WHERE device_id = ? AND account_id = ? AND sync_group_id = ? AND status = 'active'`,
			now, deviceID, accountID, groupID)
		if err != nil {
			return fmt.Errorf("吊销设备：%w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		revoked = affected == 1
		return nil
	})
	return revoked, err
}

type DeviceListRow struct {
	DeviceID   string
	DeviceName string
	CreatedAt  int64
	LastSeenAt int64
	Status     string
}

func (q *Queries) ListDevicesInGroup(ctx context.Context, accountID, groupID string) ([]DeviceListRow, error) {
	rows, err := q.db.QueryxContext(ctx, `
SELECT device_id, device_name, created_at, last_seen_at, status
FROM devices
WHERE account_id = ? AND sync_group_id = ?
ORDER BY created_at, device_id`, accountID, groupID)
	if err != nil {
		return nil, fmt.Errorf("列出组内设备：%w", err)
	}
	defer rows.Close()
	var out []DeviceListRow
	for rows.Next() {
		var item DeviceListRow
		if err := rows.Scan(&item.DeviceID, &item.DeviceName, &item.CreatedAt, &item.LastSeenAt, &item.Status); err != nil {
			return nil, fmt.Errorf("扫描设备：%w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (q *Queries) ListDeviceIDsInGroup(ctx context.Context, accountID, groupID string) ([]string, error) {
	rows, err := q.db.QueryxContext(ctx, `
SELECT device_id FROM devices
WHERE account_id = ? AND sync_group_id = ?
ORDER BY device_id`, accountID, groupID)
	if err != nil {
		return nil, fmt.Errorf("列出同步组设备：%w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type GroupRow struct {
	GroupID   string
	AccountID string
	Revision  int64
	CreatedAt int64
}

func (q *Queries) GetSyncGroup(ctx context.Context, groupID string) (GroupRow, error) {
	var out GroupRow
	err := q.db.QueryRowxContext(ctx, `
SELECT group_id, account_id, revision, created_at
FROM sync_groups WHERE group_id = ?`, groupID).Scan(
		&out.GroupID, &out.AccountID, &out.Revision, &out.CreatedAt)
	if err != nil {
		return GroupRow{}, fmt.Errorf("读取同步组：%w", err)
	}
	return out, nil
}

func (q *Queries) HasOtherSyncData(ctx context.Context, accountID, currentGroupID string) (bool, error) {
	var exists int
	err := q.db.QueryRowxContext(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM devices d
    JOIN manifest_heads h ON h.device_id = d.device_id
    WHERE d.account_id = ? AND d.sync_group_id IS NOT NULL
      AND d.sync_group_id <> ? AND h.current_version > 0
    UNION ALL
    SELECT 1
    FROM devices d
    JOIN device_blocks b ON b.device_id = d.device_id
    WHERE d.account_id = ? AND d.sync_group_id IS NOT NULL
      AND d.sync_group_id <> ?
    LIMIT 1
)`, accountID, currentGroupID, accountID, currentGroupID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("检查其他同步数据：%w", err)
	}
	return exists == 1, nil
}

type ManifestHeadRow struct {
	DeviceID       string
	CurrentVersion int64
	UpdatedAt      int64
}

func (q *Queries) ListManifestHeads(ctx context.Context, accountID, groupID string) ([]ManifestHeadRow, error) {
	rows, err := q.db.QueryxContext(ctx, `
SELECT h.device_id, h.current_version, h.updated_at
FROM manifest_heads h
JOIN devices d ON d.device_id = h.device_id
WHERE d.account_id = ? AND d.sync_group_id = ?
ORDER BY d.created_at, d.device_id`, accountID, groupID)
	if err != nil {
		return nil, fmt.Errorf("列出 Manifest heads：%w", err)
	}
	defer rows.Close()
	var out []ManifestHeadRow
	for rows.Next() {
		var item ManifestHeadRow
		if err := rows.Scan(&item.DeviceID, &item.CurrentVersion, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type ManifestRow struct {
	DeviceID   string
	Version    int64
	Ciphertext []byte
	ReceivedAt int64
}

func (q *Queries) GetVisibleManifest(
	ctx context.Context,
	accountID, groupID, deviceID string,
	version int64,
) (ManifestRow, error) {
	var out ManifestRow
	err := q.db.QueryRowxContext(ctx, `
SELECT m.device_id, m.version, m.ciphertext, m.received_at
FROM manifests m
JOIN devices d ON d.device_id = m.device_id
WHERE m.device_id = ? AND m.version = ?
  AND d.account_id = ? AND d.sync_group_id = ?`,
		deviceID, version, accountID, groupID).Scan(
		&out.DeviceID, &out.Version, &out.Ciphertext, &out.ReceivedAt)
	if err != nil {
		return ManifestRow{}, fmt.Errorf("读取 Manifest：%w", err)
	}
	return out, nil
}

type ManifestPutResult struct {
	Version        int64
	CurrentVersion int64
	Conflict       bool
	Idempotent     bool
}

func (q *Queries) PutDeviceManifest(
	ctx context.Context,
	deviceID string,
	baseVersion int64,
	ciphertext []byte,
	now int64,
) (ManifestPutResult, error) {
	hash := sha256.Sum256(ciphertext)
	var out ManifestPutResult
	err := q.WithTx(ctx, func(txq *Queries) error {
		var current int64
		var status string
		if err := txq.db.QueryRowxContext(ctx, `
SELECT h.current_version, d.status
FROM manifest_heads h
JOIN devices d ON d.device_id = h.device_id
WHERE h.device_id = ?`, deviceID).Scan(&current, &status); err != nil {
			return fmt.Errorf("读取 Manifest head：%w", err)
		}
		if status != "active" {
			return ErrInactiveDevice
		}
		if current != baseVersion {
			if current == baseVersion+1 {
				var storedHash []byte
				err := txq.db.QueryRowxContext(ctx, `
SELECT ciphertext_hash FROM manifests
WHERE device_id = ? AND version = ?`, deviceID, current).Scan(&storedHash)
				if err == nil && string(storedHash) == string(hash[:]) {
					out = ManifestPutResult{Version: current, CurrentVersion: current, Idempotent: true}
					return nil
				}
			}
			out = ManifestPutResult{CurrentVersion: current, Conflict: true}
			return nil
		}
		next := current + 1
		if _, err := txq.db.ExecContext(ctx, `
INSERT INTO manifests (device_id, version, ciphertext, ciphertext_hash, received_at)
VALUES (?, ?, ?, ?, ?)`, deviceID, next, ciphertext, hash[:], now); err != nil {
			return fmt.Errorf("插入 Manifest：%w", err)
		}
		result, err := txq.db.ExecContext(ctx, `
UPDATE manifest_heads SET current_version = ?, updated_at = ?
WHERE device_id = ? AND current_version = ?`, next, now, deviceID, current)
		if err != nil {
			return fmt.Errorf("推进 Manifest head：%w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("推进 Manifest head：CAS 未命中")
		}
		out = ManifestPutResult{Version: next, CurrentVersion: next}
		return nil
	})
	return out, err
}

type SyncCodeRow struct {
	CodeID          string
	AccountID       string
	SyncGroupID     string
	InviterDeviceID string
	CodeMAC         []byte
	ExpiresAt       int64
	ConsumedAt      sql.NullInt64
	CreatedAt       int64
}

func (q *Queries) ReplaceSyncCode(ctx context.Context, row SyncCodeRow, now int64) error {
	return q.WithTx(ctx, func(txq *Queries) error {
		device, err := txq.GetDevice(ctx, row.InviterDeviceID)
		if err != nil || device.AccountID != row.AccountID ||
			!device.SyncGroupID.Valid ||
			device.SyncGroupID.String != row.SyncGroupID {
			return ErrGroupChanged
		}
		if device.Status != "active" {
			return ErrInactiveDevice
		}
		group, err := txq.GetSyncGroup(ctx, row.SyncGroupID)
		if err != nil || group.AccountID != row.AccountID {
			return ErrGroupChanged
		}
		if _, err := txq.db.ExecContext(ctx, `
UPDATE sync_codes SET consumed_at = ?
WHERE consumed_at IS NULL AND expires_at <= ?`, now, now); err != nil {
			return fmt.Errorf("清理过期邀请码：%w", err)
		}
		if _, err := txq.db.ExecContext(ctx, `
UPDATE sync_codes SET consumed_at = ?
WHERE inviter_device_id = ? AND consumed_at IS NULL`,
			now, row.InviterDeviceID); err != nil {
			return fmt.Errorf("失效旧邀请码：%w", err)
		}
		_, err = txq.db.ExecContext(ctx, `
INSERT INTO sync_codes (
    code_id, account_id, sync_group_id, inviter_device_id,
    code_mac, expires_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			row.CodeID, row.AccountID, row.SyncGroupID, row.InviterDeviceID,
			row.CodeMAC, row.ExpiresAt, row.CreatedAt)
		if err != nil {
			return fmt.Errorf("保存邀请码：%w", err)
		}
		return nil
	})
}

type MergeGroupsResult struct {
	AlreadyJoined     bool
	CanonicalGroupID  string
	RemovedGroupID    string
	GroupRevision     int64
	AffectedDeviceIDs []string
}

func (q *Queries) RedeemSyncCode(
	ctx context.Context,
	accountID, redeemerDeviceID string,
	codeMAC []byte,
	now int64,
) (MergeGroupsResult, error) {
	var out MergeGroupsResult
	err := q.WithTx(ctx, func(txq *Queries) error {
		var code SyncCodeRow
		err := txq.db.QueryRowxContext(ctx, `
SELECT code_id, account_id, sync_group_id, inviter_device_id,
       code_mac, expires_at, consumed_at, created_at
FROM sync_codes
WHERE account_id = ? AND code_mac = ? AND consumed_at IS NULL
ORDER BY created_at DESC LIMIT 1`,
			accountID, codeMAC).Scan(
			&code.CodeID, &code.AccountID, &code.SyncGroupID, &code.InviterDeviceID,
			&code.CodeMAC, &code.ExpiresAt, &code.ConsumedAt, &code.CreatedAt)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInvalidSyncCode
			}
			return fmt.Errorf("读取邀请码：%w", err)
		}
		if code.ConsumedAt.Valid || code.ExpiresAt <= now {
			return ErrInvalidSyncCode
		}

		inviter, err := txq.GetDevice(ctx, code.InviterDeviceID)
		if err != nil || inviter.Status != "active" || !inviter.SyncGroupID.Valid ||
			inviter.AccountID != accountID || inviter.SyncGroupID.String != code.SyncGroupID {
			return ErrInvalidSyncCode
		}
		redeemer, err := txq.GetDevice(ctx, redeemerDeviceID)
		if err != nil || redeemer.Status != "active" || !redeemer.SyncGroupID.Valid ||
			redeemer.AccountID != accountID {
			return ErrInvalidSyncCode
		}

		if inviter.SyncGroupID.String == redeemer.SyncGroupID.String {
			if _, err := txq.db.ExecContext(ctx,
				`UPDATE sync_codes SET consumed_at = ? WHERE code_id = ? AND consumed_at IS NULL`,
				now, code.CodeID); err != nil {
				return err
			}
			group, err := txq.GetSyncGroup(ctx, inviter.SyncGroupID.String)
			if err != nil {
				return err
			}
			ids, err := txq.ListDeviceIDsInGroup(ctx, accountID, group.GroupID)
			if err != nil {
				return err
			}
			out = MergeGroupsResult{
				AlreadyJoined: true, CanonicalGroupID: group.GroupID,
				GroupRevision: group.Revision, AffectedDeviceIDs: ids,
			}
			return nil
		}

		left, err := txq.GetSyncGroup(ctx, inviter.SyncGroupID.String)
		if err != nil {
			return ErrInvalidSyncCode
		}
		right, err := txq.GetSyncGroup(ctx, redeemer.SyncGroupID.String)
		if err != nil {
			return ErrInvalidSyncCode
		}
		if left.AccountID != accountID || right.AccountID != accountID {
			return ErrInvalidSyncCode
		}
		winner, loser := left, right
		if right.CreatedAt < left.CreatedAt ||
			(right.CreatedAt == left.CreatedAt && right.GroupID < left.GroupID) {
			winner, loser = right, left
		}
		nextRevision := max(left.Revision, right.Revision) + 1

		if _, err := txq.db.ExecContext(ctx,
			`UPDATE devices SET sync_group_id = ? WHERE sync_group_id = ?`,
			winner.GroupID, loser.GroupID); err != nil {
			return fmt.Errorf("迁移同步组设备：%w", err)
		}
		if _, err := txq.db.ExecContext(ctx, `
UPDATE sync_codes SET sync_group_id = ?
WHERE sync_group_id = ? AND consumed_at IS NULL`,
			winner.GroupID, loser.GroupID); err != nil {
			return fmt.Errorf("迁移邀请码：%w", err)
		}
		if _, err := txq.db.ExecContext(ctx,
			`UPDATE sync_groups SET revision = ? WHERE group_id = ?`,
			nextRevision, winner.GroupID); err != nil {
			return fmt.Errorf("推进同步组 revision：%w", err)
		}
		if _, err := txq.db.ExecContext(ctx,
			`UPDATE sync_codes SET consumed_at = ? WHERE code_id = ? AND consumed_at IS NULL`,
			now, code.CodeID); err != nil {
			return fmt.Errorf("消费邀请码：%w", err)
		}
		if _, err := txq.db.ExecContext(ctx,
			`DELETE FROM sync_groups WHERE group_id = ?`, loser.GroupID); err != nil {
			return fmt.Errorf("删除已合并同步组：%w", err)
		}
		ids, err := txq.ListDeviceIDsInGroup(ctx, accountID, winner.GroupID)
		if err != nil {
			return err
		}
		out = MergeGroupsResult{
			CanonicalGroupID: winner.GroupID, RemovedGroupID: loser.GroupID,
			GroupRevision: nextRevision, AffectedDeviceIDs: ids,
		}
		return nil
	})
	return out, err
}

func (q *Queries) IsBlockVisible(ctx context.Context, accountID, groupID, blockID string) (bool, error) {
	var exists int
	err := q.db.QueryRowxContext(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM device_blocks b
    JOIN devices d ON d.device_id = b.device_id
    WHERE b.block_id = ? AND d.account_id = ? AND d.sync_group_id = ?
)`, blockID, accountID, groupID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("检查块可见性：%w", err)
	}
	return exists == 1, nil
}

type BlockObjectRow struct {
	BlockID    string
	Size       int64
	State      string
	OrphanedAt sql.NullInt64
	CreatedAt  int64
}

func (q *Queries) GetBlockObject(ctx context.Context, blockID string) (BlockObjectRow, error) {
	var out BlockObjectRow
	err := q.db.QueryRowxContext(ctx,
		`SELECT block_id, size, state, orphaned_at, created_at
FROM block_objects WHERE block_id = ?`,
		blockID).Scan(
		&out.BlockID, &out.Size, &out.State, &out.OrphanedAt, &out.CreatedAt)
	if err != nil {
		return BlockObjectRow{}, fmt.Errorf("读取块对象：%w", err)
	}
	return out, nil
}

type AttachBlockResult struct {
	ObjectCreated      bool
	AccountAssociation bool
	DeviceAssociation  bool
}

type UploadReservation struct {
	ReservationID string
	AccountID     string
	DeviceID      string
	BlockID       string
	Size          int64
	ExpiresAt     int64
}

// ReserveBlockUpload 在写文件前预留账户配额。过期 reservation 在同一事务中回收。
func (q *Queries) ReserveBlockUpload(
	ctx context.Context,
	accountID, deviceID, blockID string,
	size, now int64,
) (UploadReservation, error) {
	var out UploadReservation
	err := q.WithTx(ctx, func(txq *Queries) error {
		if _, err := txq.db.ExecContext(ctx,
			`DELETE FROM upload_reservations WHERE expires_at <= ?`, now); err != nil {
			return fmt.Errorf("清理过期上传 reservation：%w", err)
		}
		device, err := txq.GetDevice(ctx, deviceID)
		if err != nil || device.AccountID != accountID || device.Status != "active" {
			return fmt.Errorf("上传设备无效")
		}
		var objectState string
		err = txq.db.QueryRowxContext(ctx,
			`SELECT state FROM block_objects WHERE block_id = ?`, blockID).
			Scan(&objectState)
		if err == nil && objectState == "deleting" {
			return ErrUploadInProgress
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var existing UploadReservation
		err = txq.db.QueryRowxContext(ctx, `
SELECT reservation_id, account_id, device_id, block_id, size, expires_at
FROM upload_reservations
WHERE account_id = ? AND block_id = ?`, accountID, blockID).Scan(
			&existing.ReservationID, &existing.AccountID, &existing.DeviceID,
			&existing.BlockID, &existing.Size, &existing.ExpiresAt)
		if err == nil {
			return ErrUploadInProgress
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		var accountHas int
		if err := txq.db.QueryRowxContext(ctx, `
SELECT EXISTS (SELECT 1 FROM account_blocks WHERE account_id = ? AND block_id = ?)`,
			accountID, blockID).Scan(&accountHas); err != nil {
			return err
		}
		if accountHas == 0 {
			var used, quota, reserved int64
			if err := txq.db.QueryRowxContext(ctx, `
SELECT used_bytes, quota_bytes FROM accounts WHERE account_id = ?`,
				accountID).Scan(&used, &quota); err != nil {
				return err
			}
			if err := txq.db.QueryRowxContext(ctx, `
SELECT COALESCE(SUM(r.size), 0)
FROM upload_reservations r
WHERE r.account_id = ?
  AND NOT EXISTS (
      SELECT 1 FROM account_blocks a
      WHERE a.account_id = r.account_id AND a.block_id = r.block_id
  )`, accountID).Scan(&reserved); err != nil {
				return err
			}
			if used+reserved+size > quota {
				return ErrQuotaExceeded
			}
		}
		out = UploadReservation{
			ReservationID: uuid.NewString(), AccountID: accountID,
			DeviceID: deviceID, BlockID: blockID, Size: size,
			ExpiresAt: now + 300,
		}
		if _, err := txq.db.ExecContext(ctx, `
INSERT INTO upload_reservations (
    reservation_id, account_id, device_id, block_id, size, expires_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			out.ReservationID, out.AccountID, out.DeviceID, out.BlockID,
			out.Size, out.ExpiresAt, now); err != nil {
			return fmt.Errorf("创建上传 reservation：%w", err)
		}
		return nil
	})
	return out, err
}

func (q *Queries) CancelBlockUpload(ctx context.Context, reservationID string) error {
	_, err := q.db.ExecContext(ctx,
		`DELETE FROM upload_reservations WHERE reservation_id = ?`, reservationID)
	if err != nil {
		return fmt.Errorf("取消上传 reservation：%w", err)
	}
	return nil
}

// CommitBlockUpload 把物理文件对应的元数据、账户配额和设备可见性一次提交，
// 最后消费 reservation。
func (q *Queries) CommitBlockUpload(
	ctx context.Context,
	reservationID string,
	now int64,
) (AttachBlockResult, error) {
	var out AttachBlockResult
	err := q.WithTx(ctx, func(txq *Queries) error {
		var reservation UploadReservation
		err := txq.db.QueryRowxContext(ctx, `
SELECT reservation_id, account_id, device_id, block_id, size, expires_at
FROM upload_reservations WHERE reservation_id = ?`, reservationID).Scan(
			&reservation.ReservationID, &reservation.AccountID,
			&reservation.DeviceID, &reservation.BlockID,
			&reservation.Size, &reservation.ExpiresAt)
		if err != nil {
			return fmt.Errorf("读取上传 reservation：%w", err)
		}
		if reservation.ExpiresAt <= now {
			return fmt.Errorf("上传 reservation 已过期")
		}
		device, err := txq.GetDevice(ctx, reservation.DeviceID)
		if err != nil || device.AccountID != reservation.AccountID ||
			device.Status != "active" {
			return fmt.Errorf("上传设备无效")
		}

		var objectSize int64
		var objectState string
		err = txq.db.QueryRowxContext(ctx,
			`SELECT size, state FROM block_objects WHERE block_id = ?`,
			reservation.BlockID).Scan(&objectSize, &objectState)
		switch {
		case err == nil && objectSize != reservation.Size:
			return fmt.Errorf("块对象大小冲突")
		case err == nil && objectState == "deleting":
			return ErrUploadInProgress
		case errors.Is(err, sql.ErrNoRows):
			if _, err := txq.db.ExecContext(ctx, `
INSERT INTO block_objects (block_id, size, created_at) VALUES (?, ?, ?)`,
				reservation.BlockID, reservation.Size, now); err != nil {
				return err
			}
			out.ObjectCreated = true
		case err != nil:
			return err
		}

		var accountHas int
		if err := txq.db.QueryRowxContext(ctx, `
SELECT EXISTS (SELECT 1 FROM account_blocks WHERE account_id = ? AND block_id = ?)`,
			reservation.AccountID, reservation.BlockID).Scan(&accountHas); err != nil {
			return err
		}
		if accountHas == 0 {
			var used, quota int64
			if err := txq.db.QueryRowxContext(ctx, `
SELECT used_bytes, quota_bytes FROM accounts WHERE account_id = ?`,
				reservation.AccountID).Scan(&used, &quota); err != nil {
				return err
			}
			if used+reservation.Size > quota {
				return ErrQuotaExceeded
			}
			if _, err := txq.db.ExecContext(ctx, `
INSERT INTO account_blocks (account_id, block_id, created_at) VALUES (?, ?, ?)`,
				reservation.AccountID, reservation.BlockID, now); err != nil {
				return err
			}
			if _, err := txq.db.ExecContext(ctx, `
UPDATE accounts SET used_bytes = used_bytes + ? WHERE account_id = ?`,
				reservation.Size, reservation.AccountID); err != nil {
				return err
			}
			out.AccountAssociation = true
		}
		if _, err := txq.db.ExecContext(ctx, `
UPDATE block_objects SET state = 'active', orphaned_at = NULL
WHERE block_id = ?`, reservation.BlockID); err != nil {
			return err
		}
		result, err := txq.db.ExecContext(ctx, `
INSERT OR IGNORE INTO device_blocks (device_id, block_id, created_at)
VALUES (?, ?, ?)`, reservation.DeviceID, reservation.BlockID, now)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		out.DeviceAssociation = affected == 1
		if _, err := txq.db.ExecContext(ctx,
			`DELETE FROM upload_reservations WHERE reservation_id = ?`,
			reservationID); err != nil {
			return err
		}
		return nil
	})
	return out, err
}

func (q *Queries) AttachBlock(
	ctx context.Context,
	accountID, deviceID, blockID string,
	size, now int64,
) (AttachBlockResult, error) {
	var out AttachBlockResult
	err := q.WithTx(ctx, func(txq *Queries) error {
		device, err := txq.GetDevice(ctx, deviceID)
		if err != nil || device.AccountID != accountID || device.Status != "active" {
			return fmt.Errorf("块上传设备无效")
		}
		var objectSize int64
		var objectState string
		err = txq.db.QueryRowxContext(ctx,
			`SELECT size, state FROM block_objects WHERE block_id = ?`,
			blockID).Scan(&objectSize, &objectState)
		switch {
		case err == nil && objectSize != size:
			return fmt.Errorf("块对象大小冲突")
		case err == nil && objectState == "deleting":
			return ErrUploadInProgress
		case errors.Is(err, sql.ErrNoRows):
			if _, err := txq.db.ExecContext(ctx, `
INSERT INTO block_objects (block_id, size, created_at) VALUES (?, ?, ?)`,
				blockID, size, now); err != nil {
				return fmt.Errorf("创建块对象：%w", err)
			}
			out.ObjectCreated = true
		case err != nil:
			return fmt.Errorf("读取块对象：%w", err)
		}

		var accountHas int
		if err := txq.db.QueryRowxContext(ctx, `
SELECT EXISTS (SELECT 1 FROM account_blocks WHERE account_id = ? AND block_id = ?)`,
			accountID, blockID).Scan(&accountHas); err != nil {
			return err
		}
		if accountHas == 0 {
			var used, quota int64
			if err := txq.db.QueryRowxContext(ctx, `
SELECT used_bytes, quota_bytes FROM accounts WHERE account_id = ?`,
				accountID).Scan(&used, &quota); err != nil {
				return err
			}
			if used+size > quota {
				return ErrQuotaExceeded
			}
			if _, err := txq.db.ExecContext(ctx, `
INSERT INTO account_blocks (account_id, block_id, created_at) VALUES (?, ?, ?)`,
				accountID, blockID, now); err != nil {
				return err
			}
			if _, err := txq.db.ExecContext(ctx, `
UPDATE accounts SET used_bytes = used_bytes + ? WHERE account_id = ?`,
				size, accountID); err != nil {
				return err
			}
			out.AccountAssociation = true
		}
		if _, err := txq.db.ExecContext(ctx, `
UPDATE block_objects SET state = 'active', orphaned_at = NULL
WHERE block_id = ?`, blockID); err != nil {
			return err
		}
		result, err := txq.db.ExecContext(ctx, `
INSERT OR IGNORE INTO device_blocks (device_id, block_id, created_at)
VALUES (?, ?, ?)`, deviceID, blockID, now)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		out.DeviceAssociation = affected == 1
		return nil
	})
	return out, err
}

type PruneResult struct {
	OrphanBlockIDs []string
	ReclaimedBytes int64
}

func (q *Queries) PruneGroupBlocks(
	ctx context.Context,
	accountID, deviceID, groupID string,
	expectedRevision int64,
	keep map[string]struct{},
	now int64,
) (PruneResult, error) {
	var out PruneResult
	err := q.WithTx(ctx, func(txq *Queries) error {
		device, err := txq.GetDevice(ctx, deviceID)
		if err != nil || device.AccountID != accountID || !device.SyncGroupID.Valid {
			return ErrGroupChanged
		}
		if device.Status != "active" {
			return ErrInactiveDevice
		}
		if device.SyncGroupID.String != groupID {
			return ErrGroupChanged
		}
		group, err := txq.GetSyncGroup(ctx, groupID)
		if err != nil || group.AccountID != accountID || group.Revision != expectedRevision {
			return ErrGroupChanged
		}
		rows, err := txq.db.QueryxContext(ctx, `
SELECT DISTINCT b.block_id
FROM device_blocks b
JOIN devices d ON d.device_id = b.device_id
WHERE d.account_id = ? AND d.sync_group_id = ?`, accountID, groupID)
		if err != nil {
			return err
		}
		var candidates []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if _, ok := keep[id]; !ok {
				candidates = append(candidates, id)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range candidates {
			if _, err := txq.db.ExecContext(ctx, `
DELETE FROM device_blocks
WHERE block_id = ? AND device_id IN (
    SELECT device_id FROM devices WHERE account_id = ? AND sync_group_id = ?
)`, id, accountID, groupID); err != nil {
				return err
			}
			if err := txq.releaseUnreferencedAccountBlock(
				ctx, accountID, id, now, &out); err != nil {
				return err
			}
		}
		return nil
	})
	sort.Strings(out.OrphanBlockIDs)
	return out, err
}

type DiscardGroupsResult struct {
	RevokedDeviceIDs []string
	OrphanBlockIDs   []string
	ReclaimedBytes   int64
}

func (q *Queries) DiscardOtherGroups(
	ctx context.Context,
	accountID, callerDeviceID, currentGroupID string,
	expectedRevision int64,
	now int64,
) (DiscardGroupsResult, error) {
	var out DiscardGroupsResult
	err := q.WithTx(ctx, func(txq *Queries) error {
		device, err := txq.GetDevice(ctx, callerDeviceID)
		if err != nil || device.AccountID != accountID ||
			device.Status != "active" || !device.SyncGroupID.Valid ||
			device.SyncGroupID.String != currentGroupID {
			return ErrGroupChanged
		}
		group, err := txq.GetSyncGroup(ctx, currentGroupID)
		if err != nil || group.AccountID != accountID ||
			group.Revision != expectedRevision {
			return ErrGroupChanged
		}
		rows, err := txq.db.QueryxContext(ctx, `
SELECT device_id FROM devices
WHERE account_id = ? AND sync_group_id IS NOT NULL AND sync_group_id <> ?`,
			accountID, currentGroupID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			out.RevokedDeviceIDs = append(out.RevokedDeviceIDs, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(out.RevokedDeviceIDs) == 0 {
			return nil
		}

		blockRows, err := txq.db.QueryxContext(ctx, `
SELECT DISTINCT b.block_id
FROM device_blocks b
JOIN devices d ON d.device_id = b.device_id
WHERE d.account_id = ? AND d.sync_group_id <> ?`, accountID, currentGroupID)
		if err != nil {
			return err
		}
		var blockIDs []string
		for blockRows.Next() {
			var id string
			if err := blockRows.Scan(&id); err != nil {
				blockRows.Close()
				return err
			}
			blockIDs = append(blockIDs, id)
		}
		if err := blockRows.Close(); err != nil {
			return err
		}

		if _, err := txq.db.ExecContext(ctx, `
DELETE FROM manifests WHERE device_id IN (
    SELECT device_id FROM devices
    WHERE account_id = ? AND sync_group_id <> ?
)`, accountID, currentGroupID); err != nil {
			return err
		}
		if _, err := txq.db.ExecContext(ctx, `
DELETE FROM manifest_heads WHERE device_id IN (
    SELECT device_id FROM devices
    WHERE account_id = ? AND sync_group_id <> ?
)`, accountID, currentGroupID); err != nil {
			return err
		}
		if _, err := txq.db.ExecContext(ctx, `
DELETE FROM device_blocks WHERE device_id IN (
    SELECT device_id FROM devices
    WHERE account_id = ? AND sync_group_id <> ?
)`, accountID, currentGroupID); err != nil {
			return err
		}
		if _, err := txq.db.ExecContext(ctx, `
UPDATE devices
SET status = 'revoked', revoked_at = COALESCE(revoked_at, ?)
WHERE account_id = ? AND sync_group_id <> ?`,
			now, accountID, currentGroupID); err != nil {
			return err
		}
		if _, err := txq.db.ExecContext(ctx, `
DELETE FROM sync_groups WHERE account_id = ? AND group_id <> ?`,
			accountID, currentGroupID); err != nil {
			return err
		}
		var released PruneResult
		for _, id := range blockIDs {
			if err := txq.releaseUnreferencedAccountBlock(
				ctx, accountID, id, now, &released); err != nil {
				return err
			}
		}
		out.OrphanBlockIDs = append(out.OrphanBlockIDs, released.OrphanBlockIDs...)
		out.ReclaimedBytes += released.ReclaimedBytes
		return nil
	})
	sort.Strings(out.RevokedDeviceIDs)
	sort.Strings(out.OrphanBlockIDs)
	return out, err
}

// releaseUnreferencedAccountBlock 必须在写事务内调用。
func (q *Queries) releaseUnreferencedAccountBlock(
	ctx context.Context,
	accountID, blockID string,
	now int64,
	out *PruneResult,
) error {
	var stillReferenced int
	if err := q.db.QueryRowxContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM device_blocks b
    JOIN devices d ON d.device_id = b.device_id
    WHERE d.account_id = ? AND b.block_id = ?
)`, accountID, blockID).Scan(&stillReferenced); err != nil {
		return err
	}
	if stillReferenced == 1 {
		return nil
	}
	var size int64
	err := q.db.QueryRowxContext(ctx, `
SELECT o.size
FROM block_objects o
JOIN account_blocks a ON a.block_id = o.block_id
WHERE a.account_id = ? AND a.block_id = ?`, accountID, blockID).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx,
		`DELETE FROM account_blocks WHERE account_id = ? AND block_id = ?`,
		accountID, blockID); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, `
UPDATE accounts SET used_bytes = MAX(0, used_bytes - ?) WHERE account_id = ?`,
		size, accountID); err != nil {
		return err
	}
	out.ReclaimedBytes += size
	var otherAccounts int
	if err := q.db.QueryRowxContext(ctx, `
SELECT EXISTS (SELECT 1 FROM account_blocks WHERE block_id = ?)`,
		blockID).Scan(&otherAccounts); err != nil {
		return err
	}
	if otherAccounts == 0 {
		if _, err := q.db.ExecContext(ctx,
			`UPDATE block_objects
SET orphaned_at = COALESCE(orphaned_at, ?)
WHERE block_id = ? AND state = 'active'`, now, blockID); err != nil {
			return err
		}
		out.OrphanBlockIDs = append(out.OrphanBlockIDs, blockID)
	}
	return nil
}

// ClaimCollectibleBlocks 原子认领已经超过宽限期且没有任何账户引用或上传预留的块。
// deleting 状态用于崩溃恢复，并阻止新上传在物理删除窗口内重新关联该对象。
func (q *Queries) ClaimCollectibleBlocks(
	ctx context.Context,
	orphanedBefore int64,
	now int64,
	limit int,
) ([]string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 256
	}
	var ids []string
	err := q.WithTx(ctx, func(txq *Queries) error {
		if _, err := txq.db.ExecContext(ctx,
			`DELETE FROM upload_reservations WHERE expires_at <= ?`, now); err != nil {
			return fmt.Errorf("清理过期上传 reservation：%w", err)
		}
		rows, err := txq.db.QueryxContext(ctx, `
SELECT o.block_id
FROM block_objects o
WHERE (
        o.state = 'deleting'
        OR (o.state = 'active' AND o.orphaned_at IS NOT NULL AND o.orphaned_at <= ?)
      )
  AND NOT EXISTS (
      SELECT 1 FROM account_blocks a WHERE a.block_id = o.block_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM upload_reservations r WHERE r.block_id = o.block_id
  )
ORDER BY COALESCE(o.orphaned_at, 0), o.block_id
LIMIT ?`, orphanedBefore, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := txq.db.ExecContext(ctx, `
UPDATE block_objects SET state = 'deleting'
WHERE block_id = ?
  AND NOT EXISTS (
      SELECT 1 FROM account_blocks a WHERE a.block_id = block_objects.block_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM upload_reservations r WHERE r.block_id = block_objects.block_id
  )`, id); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

// FinalizeBlockDeletion 只删除仍处于 deleting 且无人引用/预留的元数据。
func (q *Queries) FinalizeBlockDeletion(ctx context.Context, blockID string) (bool, error) {
	result, err := q.db.ExecContext(ctx, `
DELETE FROM block_objects
WHERE block_id = ? AND state = 'deleting'
  AND NOT EXISTS (
      SELECT 1 FROM account_blocks a WHERE a.block_id = block_objects.block_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM upload_reservations r WHERE r.block_id = block_objects.block_id
  )`, blockID)
	if err != nil {
		return false, fmt.Errorf("完成块回收：%w", err)
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (q *Queries) UseRequestNonce(
	ctx context.Context,
	deviceID string,
	nonceHash []byte,
	expiresAt, now int64,
) (bool, error) {
	if _, err := q.db.ExecContext(ctx,
		`DELETE FROM request_nonces WHERE expires_at <= ?`, now); err != nil {
		return false, fmt.Errorf("清理过期 nonce：%w", err)
	}
	result, err := q.db.ExecContext(ctx, `
INSERT OR IGNORE INTO request_nonces (device_id, nonce_hash, expires_at)
VALUES (?, ?, ?)`, deviceID, nonceHash, expiresAt)
	if err != nil {
		return false, fmt.Errorf("占用请求 nonce：%w", err)
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

type TableName string

const (
	TableAccounts           TableName = "accounts"
	TableDevices            TableName = "devices"
	TableSyncGroups         TableName = "sync_groups"
	TableManifests          TableName = "manifests"
	TableManifestHeads      TableName = "manifest_heads"
	TableBlockObjects       TableName = "block_objects"
	TableAccountBlocks      TableName = "account_blocks"
	TableDeviceBlocks       TableName = "device_blocks"
	TableSyncCodes          TableName = "sync_codes"
	TableRequestNonces      TableName = "request_nonces"
	TableUploadReservations TableName = "upload_reservations"
)

var tableWhitelist = map[TableName]struct{}{
	TableAccounts: {}, TableDevices: {}, TableSyncGroups: {},
	TableManifests: {}, TableManifestHeads: {}, TableBlockObjects: {},
	TableAccountBlocks: {}, TableDeviceBlocks: {}, TableSyncCodes: {},
	TableRequestNonces: {}, TableUploadReservations: {},
}

func (q *Queries) CountRows(ctx context.Context, dest *int, table TableName) error {
	if _, ok := tableWhitelist[table]; !ok || strings.ContainsAny(string(table), " ;") {
		return fmt.Errorf("表名不在白名单")
	}
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	if err := q.db.QueryRowxContext(ctx, query).Scan(dest); err != nil {
		return fmt.Errorf("统计 %s：%w", table, err)
	}
	return nil
}
