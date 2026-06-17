package db

import (
	"context"
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
