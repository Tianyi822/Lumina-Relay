package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// SQL 语句定义为包级 const，所有动态值经 ? 占位符绑定（见 data-layer spec §3.4）。
const (
	// sqlCreateAccount 插入一行账户。account_id 由调用方（service 层）生成。
	sqlCreateAccount = `INSERT INTO accounts (
    account_id, recovery_code_hash, dek_salt, dek_nonce, dek_ct, created_at
) VALUES (?, ?, ?, ?, ?, ?)`

	// sqlGetAccountDEK 读取账户的 DEK 信封字段。账户不存在时返回 sql.ErrNoRows。
	sqlGetAccountDEK = `SELECT dek_salt, dek_nonce, dek_ct
FROM accounts
WHERE account_id = ?`

	// sqlGetAccountRecoveryHash 读取账户的恢复码哈希。账户不存在时返回 sql.ErrNoRows。
	sqlGetAccountRecoveryHash = `SELECT recovery_code_hash
FROM accounts
WHERE account_id = ?`

	// sqlGetAccountByRecoveryHash 按恢复码哈希反查账户（含 accountId + DEK）。
	// 供 GET /account/dek?recoveryCodeHash= 使用（换设备流程）。
	sqlGetAccountByRecoveryHash = `SELECT account_id, dek_salt, dek_nonce, dek_ct
FROM accounts
WHERE recovery_code_hash = ?`

	// sqlGetRecoveryLock 读取账户的恢复码锁定状态（失败计数 + 锁定到期时间）。
	// 供 /device/register 在校验恢复码前判断是否被锁定（C3 防爆破）。
	sqlGetRecoveryLock = `SELECT recovery_fail_count, recovery_locked_until
FROM accounts
WHERE account_id = ?`

	// sqlIncRecoveryFail 恢复码失败计数 +1。供 /device/register 恢复码不匹配时调用。
	sqlIncRecoveryFail = `UPDATE accounts SET recovery_fail_count = recovery_fail_count + 1
WHERE account_id = ?`

	// sqlLockRecovery 锁定账户恢复码到指定时间戳（Unix 秒）。
	// 达到失败阈值时调用（当前阈值 5，见 service.RegisterDevice）。
	// 仅当新值 > 现有值时更新，避免并发请求用较早的 now 覆盖更长的锁（I3）。
	sqlLockRecovery = `UPDATE accounts SET recovery_locked_until = ?
WHERE account_id = ? AND recovery_locked_until < ?`

	// sqlResetRecoveryLock 重置失败计数与锁定（恢复码校验成功时调用）。
	sqlResetRecoveryLock = `UPDATE accounts SET recovery_fail_count = 0, recovery_locked_until = 0
WHERE account_id = ?`

	// sqlUpdateAccountDEK 替换账户的 DEK 信封（改主密码场景，sync-design §280）。
	sqlUpdateAccountDEK = `UPDATE accounts
SET dek_salt = ?, dek_nonce = ?, dek_ct = ?
WHERE account_id = ?`

	// sqlCreateDevice 插入一行设备，绑定到已存在的 account。
	// last_seen_at 初始等于 created_at（注册即"活跃过一次"）。
	sqlCreateDevice = `INSERT INTO devices (
    device_id, account_id, device_pub_key, device_name, created_at, last_seen_at
) VALUES (?, ?, ?, ?, ?, ?)`

	// sqlGetDevice 读取设备记录。不存在时返回 sql.ErrNoRows。
	sqlGetDevice = `SELECT device_id, account_id, device_pub_key, device_name, created_at, revoked_at, last_seen_at
FROM devices
WHERE device_id = ?`

	// sqlRevokeDevice 置设备的 revoked_at（吊销）。已吊销则保持原值。
	sqlRevokeDevice = `UPDATE devices SET revoked_at = ? WHERE device_id = ? AND revoked_at IS NULL`

	// sqlTouchDeviceLastSeen 更新设备最后活跃时间（Session 认证成功时调用）。
	sqlTouchDeviceLastSeen = `UPDATE devices SET last_seen_at = ? WHERE device_id = ?`

	// sqlListDevicesByAccount 列出账户下未吊销设备，按创建时间升序。
	sqlListDevicesByAccount = `SELECT device_id, device_name, device_pub_key, created_at, last_seen_at
FROM devices
WHERE account_id = ? AND revoked_at IS NULL
ORDER BY created_at`

	// sqlInsertManifestHead 初始化账户的 manifest_head（current_version=0）。
	sqlInsertManifestHead = `INSERT INTO manifest_head (account_id, current_version, updated_at)
VALUES (?, 0, ?)`

	// sqlGetManifestHead 读取账户当前 manifest 版本。不存在时返回 sql.ErrNoRows。
	sqlGetManifestHead = `SELECT account_id, current_version, updated_at
FROM manifest_head
WHERE account_id = ?`

	// sqlInsertManifest 插入一条 manifest 历史版本。
	sqlInsertManifest = `INSERT INTO manifests (account_id, version, ciphertext, device_id, received_at)
VALUES (?, ?, ?, ?, ?)`

	// sqlUpdateManifestHead 推进 head 指针（乐观并发 CAS：仅当 current_version = expected 时更新）。
	sqlUpdateManifestHead = `UPDATE manifest_head
SET current_version = ?, updated_at = ?
WHERE account_id = ? AND current_version = ?`

	// sqlGetManifestByAccount 读取账户指定版本的 manifest。不存在返 sql.ErrNoRows。
	sqlGetManifestByAccount = `SELECT account_id, version, ciphertext, device_id, received_at
FROM manifests
WHERE account_id = ? AND version = ?`

	// sqlUpsertBlockMeta 插入块元数据。account_id+block_id 唯一（PK 为 block_id，
	// 但同一账户的块去重靠 block_id 全局唯一）。IGNORE 保证幂等。
	sqlUpsertBlockMeta = `INSERT OR IGNORE INTO blocks (block_id, account_id, size, ref_count, created_at)
VALUES (?, ?, ?, 0, ?)`

	// sqlGetBlockMeta 读取块元数据。不存在返 sql.ErrNoRows。
	sqlGetBlockMeta = `SELECT block_id, account_id, size, ref_count, created_at
FROM blocks
WHERE block_id = ?`

	// sqlSumBlockSizeByAccount 统计账户所有块的总字节数（配额检查用）。
	sqlSumBlockSizeByAccount = `SELECT COALESCE(SUM(size), 0) FROM blocks WHERE account_id = ?`

	// sqlCountRows 统计指定表行数。表名为包内白名单常量，禁止用户输入（见 data-layer spec §3.4）。
	sqlCountRows = `SELECT COUNT(*) FROM %s`
)

// TableName 是受信任的表名白名单类型，供 CountRows 避免注入。
type TableName string

const (
	TableAccounts     TableName = "accounts"
	TableDevices      TableName = "devices"
	TableBlocks       TableName = "blocks"
	TableManifests    TableName = "manifests"
	TableManifestHead TableName = "manifest_head"
)

// CreateAccountParams 是 CreateAccount 的入参。
// 字段语义见 data-layer spec §4（recoveryCodeHash 与 dekEnvelope 均为客户端处理后的产物）。
type CreateAccountParams struct {
	AccountID        string // 账户 ID（调用方生成）
	RecoveryCodeHash []byte // 恢复码哈希
	DekSalt          []byte // DEK 信封盐
	DekNonce         []byte // DEK 信封 nonce
	DekCt            []byte // DEK 密文
	CreatedAt        int64  // 创建时间（Unix 秒）
}

// AccountDEKRow 是 GetAccountDEK 的返回行，仅含 DEK 信封字段。
type AccountDEKRow struct {
	DekSalt  []byte
	DekNonce []byte
	DekCt    []byte
}

// CreateAccount 插入一行账户。
func (q *Queries) CreateAccount(ctx context.Context, p CreateAccountParams) error {
	_, err := q.db.ExecContext(ctx, sqlCreateAccount,
		p.AccountID, p.RecoveryCodeHash, p.DekSalt, p.DekNonce, p.DekCt, p.CreatedAt)
	if err != nil {
		return fmt.Errorf("插入账户：%w", err)
	}
	return nil
}

// GetAccountDEK 读取指定账户的 DEK 信封字段。
// 账户不存在时返回 sql.ErrNoRows（由 QueryRowxContext 的 Scan 透传）。
func (q *Queries) GetAccountDEK(ctx context.Context, accountID string) (AccountDEKRow, error) {
	var row AccountDEKRow
	if err := q.db.QueryRowxContext(ctx, sqlGetAccountDEK, accountID).Scan(
		&row.DekSalt, &row.DekNonce, &row.DekCt,
	); err != nil {
		return AccountDEKRow{}, fmt.Errorf("读取账户 DEK：%w", err)
	}
	return row, nil
}

// GetAccountRecoveryHash 读取账户的恢复码哈希。账户不存在时返回 sql.ErrNoRows。
func (q *Queries) GetAccountRecoveryHash(ctx context.Context, accountID string) ([]byte, error) {
	var hash []byte
	if err := q.db.QueryRowxContext(ctx, sqlGetAccountRecoveryHash, accountID).Scan(&hash); err != nil {
		return nil, fmt.Errorf("读取恢复码哈希：%w", err)
	}
	return hash, nil
}

// UpdateAccountDEKParams 是 UpdateAccountDEK 的入参。
type UpdateAccountDEKParams struct {
	DekSalt  []byte
	DekNonce []byte
	DekCt    []byte
}

// UpdateAccountDEK 替换账户的 DEK 信封。见 sync-design §280（改密码不碰数据块）。
func (q *Queries) UpdateAccountDEK(ctx context.Context, accountID string, p UpdateAccountDEKParams) error {
	_, err := q.db.ExecContext(ctx, sqlUpdateAccountDEK, p.DekSalt, p.DekNonce, p.DekCt, accountID)
	if err != nil {
		return fmt.Errorf("更新 DEK：%w", err)
	}
	return nil
}

// CreateDeviceParams 是 CreateDevice 的入参。
type CreateDeviceParams struct {
	DeviceID      string
	AccountID     string
	DevicePubKey  string // hex 编码的 Ed25519 公钥
	DeviceName    string
	CreatedAt     int64
}

// CreateDevice 插入一行设备记录。last_seen_at 初始化为 CreatedAt。
func (q *Queries) CreateDevice(ctx context.Context, p CreateDeviceParams) error {
	_, err := q.db.ExecContext(ctx, sqlCreateDevice,
		p.DeviceID, p.AccountID, p.DevicePubKey, p.DeviceName, p.CreatedAt, p.CreatedAt)
	if err != nil {
		return fmt.Errorf("插入设备：%w", err)
	}
	return nil
}

// DeviceRow 是 GetDevice 的返回行。
// RevokedAt 用 sql.NullInt64 表达可空的吊销时间戳。
// LastSeenAt 由迁移保证非 NULL（回填 + CreateDevice 写入）。
type DeviceRow struct {
	DeviceID     string
	AccountID    string
	DevicePubKey string
	DeviceName   string
	CreatedAt    int64
	RevokedAt    sql.NullInt64
	LastSeenAt   int64
}

// GetDevice 读取设备记录。设备不存在时返回 sql.ErrNoRows。
func (q *Queries) GetDevice(ctx context.Context, deviceID string) (DeviceRow, error) {
	var row DeviceRow
	if err := q.db.QueryRowxContext(ctx, sqlGetDevice, deviceID).Scan(
		&row.DeviceID, &row.AccountID, &row.DevicePubKey, &row.DeviceName,
		&row.CreatedAt, &row.RevokedAt, &row.LastSeenAt,
	); err != nil {
		return DeviceRow{}, fmt.Errorf("读取设备：%w", err)
	}
	return row, nil
}

// RevokeDevice 置设备的 revoked_at（Unix 秒）。幂等：已吊销的设备保持原值。
// 返回受影响行数：1 表示本次吊销，0 表示设备不存在或已吊销。
func (q *Queries) RevokeDevice(ctx context.Context, deviceID string, revokedAt int64) (int64, error) {
	res, err := q.db.ExecContext(ctx, sqlRevokeDevice, revokedAt, deviceID)
	if err != nil {
		return 0, fmt.Errorf("吊销设备：%w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取受影响行数：%w", err)
	}
	return n, nil
}

// TouchDeviceLastSeen 更新设备的 last_seen_at（Unix 秒）。供 Session 中间件在认证成功后调用。
func (q *Queries) TouchDeviceLastSeen(ctx context.Context, deviceID string, ts int64) error {
	if _, err := q.db.ExecContext(ctx, sqlTouchDeviceLastSeen, ts, deviceID); err != nil {
		return fmt.Errorf("更新设备活跃时间：%w", err)
	}
	return nil
}

// DeviceListRow 是 ListDevicesByAccount 的返回行（不含已吊销设备）。
type DeviceListRow struct {
	DeviceID     string
	DeviceName   string
	DevicePubKey string
	CreatedAt    int64
	LastSeenAt   int64
}

// ListDevicesByAccount 列出账户下所有未吊销设备，按创建时间升序。
func (q *Queries) ListDevicesByAccount(ctx context.Context, accountID string) ([]DeviceListRow, error) {
	rows, err := q.db.QueryxContext(ctx, sqlListDevicesByAccount, accountID)
	if err != nil {
		return nil, fmt.Errorf("列出设备：%w", err)
	}
	defer rows.Close()
	var out []DeviceListRow
	for rows.Next() {
		var r DeviceListRow
		if err := rows.Scan(
			&r.DeviceID, &r.DeviceName, &r.DevicePubKey, &r.CreatedAt, &r.LastSeenAt,
		); err != nil {
			return nil, fmt.Errorf("扫描设备行：%w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历设备行：%w", err)
	}
	return out, nil
}

// AccountByRecoveryRow 是按恢复码哈希反查的返回行。
type AccountByRecoveryRow struct {
	AccountID string
	DekSalt   []byte
	DekNonce  []byte
	DekCt     []byte
}

// GetAccountByRecoveryHash 按恢复码哈希反查账户，返回 accountId + DEK 信封。
// 账户不存在时返回 sql.ErrNoRows。
func (q *Queries) GetAccountByRecoveryHash(ctx context.Context, hash []byte) (AccountByRecoveryRow, error) {
	var row AccountByRecoveryRow
	if err := q.db.QueryRowxContext(ctx, sqlGetAccountByRecoveryHash, hash).Scan(
		&row.AccountID, &row.DekSalt, &row.DekNonce, &row.DekCt,
	); err != nil {
		return AccountByRecoveryRow{}, fmt.Errorf("按恢复码反查账户：%w", err)
	}
	return row, nil
}

// RecoveryLockRow 是 GetRecoveryLock 的返回行，表达账户的恢复码锁定状态。
type RecoveryLockRow struct {
	RecoveryFailCount  int64
	RecoveryLockedUntil int64 // Unix 秒，0 表示未锁
}

// GetRecoveryLock 读取账户的恢复码锁定状态。账户不存在时返回 sql.ErrNoRows。
func (q *Queries) GetRecoveryLock(ctx context.Context, accountID string) (RecoveryLockRow, error) {
	var row RecoveryLockRow
	if err := q.db.QueryRowxContext(ctx, sqlGetRecoveryLock, accountID).Scan(
		&row.RecoveryFailCount, &row.RecoveryLockedUntil,
	); err != nil {
		return RecoveryLockRow{}, fmt.Errorf("读取恢复码锁定状态：%w", err)
	}
	return row, nil
}

// IncRecoveryFail 恢复码失败计数 +1。
func (q *Queries) IncRecoveryFail(ctx context.Context, accountID string) error {
	if _, err := q.db.ExecContext(ctx, sqlIncRecoveryFail, accountID); err != nil {
		return fmt.Errorf("累加恢复码失败计数：%w", err)
	}
	return nil
}

// LockRecovery 锁定账户恢复码到 lockedUntil（Unix 秒）。
// 仅当 lockedUntil 大于现有值时才更新（避免并发请求用较早时间戳缩短锁，I3）。
func (q *Queries) LockRecovery(ctx context.Context, accountID string, lockedUntil int64) error {
	if _, err := q.db.ExecContext(ctx, sqlLockRecovery, lockedUntil, accountID, lockedUntil); err != nil {
		return fmt.Errorf("锁定恢复码：%w", err)
	}
	return nil
}

// ResetRecoveryLock 重置失败计数与锁定时间（恢复码校验成功后调用）。
func (q *Queries) ResetRecoveryLock(ctx context.Context, accountID string) error {
	if _, err := q.db.ExecContext(ctx, sqlResetRecoveryLock, accountID); err != nil {
		return fmt.Errorf("重置恢复码锁定：%w", err)
	}
	return nil
}

// InsertManifestHead 初始化账户的 manifest_head，current_version 固定为 0。
// createdAt 为 Unix 秒。
func (q *Queries) InsertManifestHead(ctx context.Context, accountID string, createdAt int64) error {
	_, err := q.db.ExecContext(ctx, sqlInsertManifestHead, accountID, createdAt)
	if err != nil {
		return fmt.Errorf("初始化 manifest_head：%w", err)
	}
	return nil
}

// ManifestHeadRow 是 GetManifestHead 的返回行。
type ManifestHeadRow struct {
	AccountID      string
	CurrentVersion int64
	UpdatedAt      int64
}

// GetManifestHead 读取账户当前 manifest 版本。不存在时返回 sql.ErrNoRows。
func (q *Queries) GetManifestHead(ctx context.Context, accountID string) (ManifestHeadRow, error) {
	var row ManifestHeadRow
	if err := q.db.QueryRowxContext(ctx, sqlGetManifestHead, accountID).Scan(
		&row.AccountID, &row.CurrentVersion, &row.UpdatedAt,
	); err != nil {
		return ManifestHeadRow{}, fmt.Errorf("读取 manifest_head：%w", err)
	}
	return row, nil
}

// ManifestRow 是 manifests 表的一行。
type ManifestRow struct {
	AccountID  string
	Version    int64
	Ciphertext []byte
	DeviceID   string
	ReceivedAt int64
}

// InsertManifestParams 是 InsertManifest 的入参。
type InsertManifestParams struct {
	AccountID  string
	Version    int64
	Ciphertext []byte
	DeviceID   string
	ReceivedAt int64
}

// InsertManifest 插入一条 manifest 历史版本。
func (q *Queries) InsertManifest(ctx context.Context, p InsertManifestParams) error {
	_, err := q.db.ExecContext(ctx, sqlInsertManifest,
		p.AccountID, p.Version, p.Ciphertext, p.DeviceID, p.ReceivedAt)
	if err != nil {
		return fmt.Errorf("插入 manifest：%w", err)
	}
	return nil
}

// UpdateManifestHeadIfExpected 是乐观并发 CAS：仅当 head.current_version == expected 时
// 推进到 newVersion。返回受影响行数（1=成功，0=版本不符）。
func (q *Queries) UpdateManifestHeadIfExpected(ctx context.Context, accountID string, expected, newVersion, updatedAt int64) (int64, error) {
	res, err := q.db.ExecContext(ctx, sqlUpdateManifestHead, newVersion, updatedAt, accountID, expected)
	if err != nil {
		return 0, fmt.Errorf("更新 manifest_head：%w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取受影响行数：%w", err)
	}
	return n, nil
}

// GetManifestByAccount 读取账户指定版本的 manifest。不存在返 sql.ErrNoRows。
func (q *Queries) GetManifestByAccount(ctx context.Context, accountID string, version int64) (ManifestRow, error) {
	var row ManifestRow
	if err := q.db.QueryRowxContext(ctx, sqlGetManifestByAccount, accountID, version).Scan(
		&row.AccountID, &row.Version, &row.Ciphertext, &row.DeviceID, &row.ReceivedAt,
	); err != nil {
		return ManifestRow{}, fmt.Errorf("读取 manifest：%w", err)
	}
	return row, nil
}

// BlockMetaRow 是 blocks 表元数据行。
type BlockMetaRow struct {
	BlockID   string
	AccountID string
	Size      int64
	RefCount  int64
	CreatedAt int64
}

// UpsertBlockMeta 插入块元数据（幂等，INSERT OR IGNORE）。
// 返回受影响行数：1=新建，0=已存在。
func (q *Queries) UpsertBlockMeta(ctx context.Context, blockID, accountID string, size, createdAt int64) (int64, error) {
	res, err := q.db.ExecContext(ctx, sqlUpsertBlockMeta, blockID, accountID, size, createdAt)
	if err != nil {
		return 0, fmt.Errorf("插入块元数据：%w", err)
	}
	return res.RowsAffected()
}

// GetBlockMeta 读取块元数据。不存在返 sql.ErrNoRows。
func (q *Queries) GetBlockMeta(ctx context.Context, blockID string) (BlockMetaRow, error) {
	var row BlockMetaRow
	if err := q.db.QueryRowxContext(ctx, sqlGetBlockMeta, blockID).Scan(
		&row.BlockID, &row.AccountID, &row.Size, &row.RefCount, &row.CreatedAt,
	); err != nil {
		return BlockMetaRow{}, fmt.Errorf("读取块元数据：%w", err)
	}
	return row, nil
}

// SumBlockSizeByAccount 返回账户所有块的总字节数（配额检查用）。无块返 0。
func (q *Queries) SumBlockSizeByAccount(ctx context.Context, accountID string) (int64, error) {
	var total int64
	if err := q.db.QueryRowxContext(ctx, sqlSumBlockSizeByAccount, accountID).Scan(&total); err != nil {
		return 0, fmt.Errorf("统计账户块大小：%w", err)
	}
	return total, nil
}

// CountRows 统计指定表行数。
// table 必须是受信任的 TableName 白名单（见 data-layer spec §3.4），杜绝注入。
// 白名单类型的 %s 拼接是本层唯一允许的字符串入 SQL 场景。
func (q *Queries) CountRows(ctx context.Context, dest *int, table TableName) error {
	// 白名单类型保证 table 不可被用户输入污染；此处 %s 安全。
	qstr := fmt.Sprintf(sqlCountRows, table)
	if err := q.db.QueryRowxContext(ctx, qstr).Scan(dest); err != nil {
		return fmt.Errorf("统计 %s 行数：%w", table, err)
	}
	return nil
}

// Queries 封装数据访问层。持有 sqlx.ExtContext 接口，
// 因此同一类型既能基于 *sqlx.DB，也能基于 *sqlx.Tx（事务内）。
type Queries struct {
	db sqlx.ExtContext
}

// New 基于 *sqlx.DB 构造 Queries。
func New(db *sqlx.DB) *Queries {
	return &Queries{db: db}
}

// WithTx 在一个数据库事务内执行 fn：fn 返回 nil 则提交，返回 error 则回滚。
// fn 收到一个绑定到事务的子 Queries，调用方在事务内使用它执行查询。
func (q *Queries) WithTx(ctx context.Context, fn func(*Queries) error) error {
	db, ok := q.db.(*sqlx.DB)
	if !ok {
		return fmt.Errorf("WithTx 仅允许在 *sqlx.DB 上调用，实际类型 %T", q.db)
	}

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务：%w", err)
	}
	txq := &Queries{db: tx}

	if err := fn(txq); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("回滚事务失败（原错误 %v）：%w", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务：%w", err)
	}
	return nil
}
