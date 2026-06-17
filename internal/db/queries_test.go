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

// seedAccount 写一行账户，供依赖 account_id 的查询测试复用。
func seedAccount(t *testing.T, q *Queries, accountID string) {
	t.Helper()
	if err := q.CreateAccount(context.Background(), CreateAccountParams{
		AccountID:        accountID,
		RecoveryCodeHash: []byte("h"),
		DekSalt:          []byte("s"),
		DekNonce:         []byte("n"),
		DekCt:            []byte("c"),
		CreatedAt:        1,
	}); err != nil {
		t.Fatalf("seedAccount 失败：%v", err)
	}
}

// TestQueries_CreateDevice_GetDevice 验证设备写入后能完整读回，且 revoked_at 默认 NULL。
func TestQueries_CreateDevice_GetDevice(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-d")

	if err := q.CreateDevice(ctx, CreateDeviceParams{
		DeviceID:     "dev-1",
		AccountID:    "acc-d",
		DevicePubKey: "pub-hex",
		DeviceName:   "phone",
		CreatedAt:    100,
	}); err != nil {
		t.Fatalf("CreateDevice 失败：%v", err)
	}

	dev, err := q.GetDevice(ctx, "dev-1")
	if err != nil {
		t.Fatalf("GetDevice 失败：%v", err)
	}
	if dev.AccountID != "acc-d" || dev.DevicePubKey != "pub-hex" || dev.DeviceName != "phone" {
		t.Fatalf("设备字段不一致：%+v", dev)
	}
	if dev.RevokedAt.Valid {
		t.Fatal("新设备 revoked_at 应为 NULL")
	}
}

// TestQueries_GetDevice_NotFound 验证读取不存在设备返回错误。
func TestQueries_GetDevice_NotFound(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	if _, err := q.GetDevice(context.Background(), "nope"); err == nil {
		t.Fatal("读取不存在设备应报错")
	}
}

// TestQueries_InsertManifestHead_GetManifestHead 验证初始化版本为 0 并能读回。
func TestQueries_InsertManifestHead_GetManifestHead(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-m")

	if err := q.InsertManifestHead(ctx, "acc-m", 200); err != nil {
		t.Fatalf("InsertManifestHead 失败：%v", err)
	}
	head, err := q.GetManifestHead(ctx, "acc-m")
	if err != nil {
		t.Fatalf("GetManifestHead 失败：%v", err)
	}
	if head.CurrentVersion != 0 {
		t.Fatalf("current_version = %d, want 0", head.CurrentVersion)
	}
	if head.UpdatedAt != 200 {
		t.Fatalf("updated_at = %d, want 200", head.UpdatedAt)
	}
}

// TestQueries_CountRows 验证白名单表计数正确。
func TestQueries_CountRows(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-c")

	var n int
	if err := q.CountRows(ctx, &n, TableAccounts); err != nil {
		t.Fatalf("CountRows 失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("accounts 行数 = %d, want 1", n)
	}

	if err := q.CountRows(ctx, &n, TableDevices); err != nil {
		t.Fatalf("CountRows(devices) 失败：%v", err)
	}
	if n != 0 {
		t.Fatalf("devices 行数 = %d, want 0", n)
	}
}

// TestQueries_GetAccountRecoveryHash 验证能读回注册时存的恢复码哈希。
func TestQueries_GetAccountRecoveryHash(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	wantHash := []byte("recovery-hash-bytes")
	if err := q.CreateAccount(ctx, CreateAccountParams{
		AccountID:        "acc-r",
		RecoveryCodeHash: wantHash,
		DekSalt:          []byte("s"),
		DekNonce:         []byte("n"),
		DekCt:            []byte("c"),
		CreatedAt:        1,
	}); err != nil {
		t.Fatalf("CreateAccount 失败：%v", err)
	}

	got, err := q.GetAccountRecoveryHash(ctx, "acc-r")
	if err != nil {
		t.Fatalf("GetAccountRecoveryHash 失败：%v", err)
	}
	if !bytes.Equal(got, wantHash) {
		t.Fatalf("hash 不匹配：got %q want %q", got, wantHash)
	}
}

// TestQueries_GetAccountRecoveryHash_NotFound 验证账户不存在时报错。
func TestQueries_GetAccountRecoveryHash_NotFound(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	if _, err := q.GetAccountRecoveryHash(context.Background(), "missing"); err == nil {
		t.Fatal("账户不存在应报错")
	}
}

// TestQueries_RevokeDevice 验证吊销置 revoked_at，且幂等（再次吊销返 0）。
func TestQueries_RevokeDevice(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-rev")
	if err := q.CreateDevice(ctx, CreateDeviceParams{
		DeviceID: "dev-rev", AccountID: "acc-rev",
		DevicePubKey: "p", DeviceName: "n", CreatedAt: 1,
	}); err != nil {
		t.Fatalf("CreateDevice 失败：%v", err)
	}

	n, err := q.RevokeDevice(ctx, "dev-rev", 500)
	if err != nil {
		t.Fatalf("RevokeDevice 失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("首次吊销受影响行数 = %d, want 1", n)
	}
	// 验证 revoked_at 已写入
	dev, _ := q.GetDevice(ctx, "dev-rev")
	if !dev.RevokedAt.Valid || dev.RevokedAt.Int64 != 500 {
		t.Fatalf("revoked_at = %v, want 500", dev.RevokedAt)
	}

	// 幂等：再次吊销应返 0（不覆盖原值）
	n, err = q.RevokeDevice(ctx, "dev-rev", 999)
	if err != nil {
		t.Fatalf("二次 RevokeDevice 失败：%v", err)
	}
	if n != 0 {
		t.Fatalf("二次吊销受影响行数 = %d, want 0", n)
	}
	dev, _ = q.GetDevice(ctx, "dev-rev")
	if dev.RevokedAt.Int64 != 500 {
		t.Fatalf("幂等后 revoked_at 被覆盖为 %d, want 500", dev.RevokedAt.Int64)
	}
}
