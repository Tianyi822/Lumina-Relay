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

	// sqlUpdateAccountDEK 替换账户的 DEK 信封（改主密码场景，sync-design §280）。
	sqlUpdateAccountDEK = `UPDATE accounts
SET dek_salt = ?, dek_nonce = ?, dek_ct = ?
WHERE account_id = ?`

	// sqlCreateDevice 插入一行设备，绑定到已存在的 account。
	sqlCreateDevice = `INSERT INTO devices (
    device_id, account_id, device_pub_key, device_name, created_at
) VALUES (?, ?, ?, ?, ?)`

	// sqlGetDevice 读取设备记录。不存在时返回 sql.ErrNoRows。
	sqlGetDevice = `SELECT device_id, account_id, device_pub_key, device_name, created_at, revoked_at
FROM devices
WHERE device_id = ?`

	// sqlRevokeDevice 置设备的 revoked_at（吊销）。已吊销则保持原值。
	sqlRevokeDevice = `UPDATE devices SET revoked_at = ? WHERE device_id = ? AND revoked_at IS NULL`

	// sqlInsertManifestHead 初始化账户的 manifest_head（current_version=0）。
	sqlInsertManifestHead = `INSERT INTO manifest_head (account_id, current_version, updated_at)
VALUES (?, 0, ?)`

	// sqlGetManifestHead 读取账户当前 manifest 版本。不存在时返回 sql.ErrNoRows。
	sqlGetManifestHead = `SELECT account_id, current_version, updated_at
FROM manifest_head
WHERE account_id = ?`

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

// CreateDevice 插入一行设备记录。
func (q *Queries) CreateDevice(ctx context.Context, p CreateDeviceParams) error {
	_, err := q.db.ExecContext(ctx, sqlCreateDevice,
		p.DeviceID, p.AccountID, p.DevicePubKey, p.DeviceName, p.CreatedAt)
	if err != nil {
		return fmt.Errorf("插入设备：%w", err)
	}
	return nil
}

// DeviceRow 是 GetDevice 的返回行。
// RevokedAt 用 sql.NullInt64 表达可空的吊销时间戳。
type DeviceRow struct {
	DeviceID     string
	AccountID    string
	DevicePubKey string
	DeviceName   string
	CreatedAt    int64
	RevokedAt    sql.NullInt64
}

// GetDevice 读取设备记录。设备不存在时返回 sql.ErrNoRows。
func (q *Queries) GetDevice(ctx context.Context, deviceID string) (DeviceRow, error) {
	var row DeviceRow
	if err := q.db.QueryRowxContext(ctx, sqlGetDevice, deviceID).Scan(
		&row.DeviceID, &row.AccountID, &row.DevicePubKey, &row.DeviceName,
		&row.CreatedAt, &row.RevokedAt,
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
