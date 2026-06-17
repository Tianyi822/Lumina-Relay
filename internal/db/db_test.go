package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
)

// openTestQueries 准备一个迁移完成、可立即使用的 Queries 实例。
// 返回的 cleanup 关闭底层连接。测试一律基于 t.TempDir()，不碰 ~/.lumina-relay。
func openTestQueries(t *testing.T) (*Queries, func()) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	if err := MigrateUp(dsn); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	return New(db), func() { _ = db.Close() }
}

// TestOpen_Connects 验证 Open 返回的 *sqlx.DB 可 Ping 通。
func TestOpen_Connects(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open 失败：%v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Fatalf("Ping 失败：%v", err)
	}
}

// TestQueries_WithTx_RollbackOnError 验证 fn 返回 error 时事务回滚，
// 写入的数据在事务外不可见。
func TestQueries_WithTx_RollbackOnError(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()

	// 先迁移好 schema 后，accounts 表存在；事务内插入一行后返回 err，
	// 期望回滚后 accounts 表为空。
	triggerErr := q.WithTx(ctx, func(txq *Queries) error {
		if _, err := txq.db.ExecContext(ctx, "INSERT INTO accounts (account_id, recovery_code_hash, dek_salt, dek_nonce, dek_ct, created_at) VALUES ('rollback', x'', x'', x'', x'', 0)"); err != nil {
			return err
		}
		return errSimulated
	})
	if triggerErr != errSimulated {
		t.Fatalf("WithTx 应原样返回 fn 的 error，得到 %v", triggerErr)
	}

	if !rowMissing(ctx, t, q, "rollback") {
		t.Fatalf("回滚后不应存在 account_id='rollback' 的行")
	}
}

// TestQueries_WithTx_CommitsOnNil 验证 fn 返回 nil 时事务提交，
// 写入的数据在事务外可见。
func TestQueries_WithTx_CommitsOnNil(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()

	if err := q.WithTx(ctx, func(txq *Queries) error {
		if _, err := txq.db.ExecContext(ctx, "INSERT INTO accounts (account_id, recovery_code_hash, dek_salt, dek_nonce, dek_ct, created_at) VALUES ('commit', x'', x'', x'', x'', 0)"); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("WithTx 返回非 nil：%v", err)
	}

	if rowMissing(ctx, t, q, "commit") {
		t.Fatalf("提交后应存在 account_id='commit' 的行")
	}
}

// rowMissing 查询给定 account_id 在 accounts 表中是否不存在。
// 用裸 QueryRowContext + Scan，避免依赖 sqlx 的反射绑定（4a 骨架 Queries 只持有 ExtContext）。
func rowMissing(ctx context.Context, t *testing.T, q *Queries, accountID string) bool {
	t.Helper()
	var n int
	if err := q.db.QueryRowxContext(ctx, "SELECT COUNT(*) FROM accounts WHERE account_id = ?", accountID).Scan(&n); err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	return n == 0
}

// errSimulated 是测试用哨兵错误，确保断言比较的是同一引用。
var errSimulated = newSentinelError("simulated failure")

func newSentinelError(msg string) error {
	return &sentinelError{msg: msg}
}

type sentinelError struct{ msg string }

func (e *sentinelError) Error() string { return e.msg }

// 确保 *sqlx.DB 类型在测试中被引用（编译期保证 sqlx 依赖可用）。
var _ = (*sqlx.DB)(nil)
