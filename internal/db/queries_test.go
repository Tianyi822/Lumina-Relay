package db

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// TestQueries_CreateAccount_GetAccountDEK 验证写入账户后能读回 DEK 信封字段，
// 且字段值与写入完全一致（字节级）。
func TestQueries_CreateAccount_GetAccountDEK(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().Unix()

	want := CreateAccountParams{
		AccountID:        "acc-1",
		RecoveryCodeHash: []byte("recovery-hash"),
		DekSalt:          []byte("salt-1234"),
		DekNonce:         []byte("nonce-1234"),
		DekCt:            []byte("ciphertext"),
		CreatedAt:        now,
	}
	if err := q.CreateAccount(ctx, want); err != nil {
		t.Fatalf("CreateAccount 失败：%v", err)
	}

	row, err := q.GetAccountDEK(ctx, "acc-1")
	if err != nil {
		t.Fatalf("GetAccountDEK 失败：%v", err)
	}
	if !bytes.Equal(row.DekSalt, want.DekSalt) {
		t.Errorf("DekSalt 不匹配：got %q want %q", row.DekSalt, want.DekSalt)
	}
	if !bytes.Equal(row.DekNonce, want.DekNonce) {
		t.Errorf("DekNonce 不匹配：got %q want %q", row.DekNonce, want.DekNonce)
	}
	if !bytes.Equal(row.DekCt, want.DekCt) {
		t.Errorf("DekCt 不匹配：got %q want %q", row.DekCt, want.DekCt)
	}
}

// TestQueries_GetAccountDEK_NotFound 验证读取不存在的账户时返回 sql.ErrNoRows，
// 而非静默返回零值（避免上层误判账户存在）。
func TestQueries_GetAccountDEK_NotFound(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := q.GetAccountDEK(ctx, "missing"); err == nil {
		t.Fatal("期望返回错误（账户不存在），得到 nil")
	}
}
