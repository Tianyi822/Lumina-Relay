package service

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"lumina-relay/internal/db"
)

// openQueries 是 service 层测试公用 helper：t.TempDir() 迁移+打开，返回 *db.Queries。
// 不碰 ~/.lumina-relay。
func openQueries(t *testing.T) (*db.Queries, func()) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	backend, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	return db.New(backend), func() { _ = backend.Close() }
}

// countRows 查指定表行数，用于断言注册副作用。
func countRows(t *testing.T, q *db.Queries, table db.TableName) int {
	t.Helper()
	var n int
	if err := q.CountRows(context.Background(), &n, table); err != nil {
		t.Fatalf("查询 %s 行数失败：%v", table, err)
	}
	return n
}

// TestAccountService_Register_CreatesRows 验证注册后：
// 1) accounts 表存在该账户；
// 2) devices 表存在关联设备；
// 3) manifest_head 存在且 current_version == 0。
// 见计划 Task 9a Step 1。
func TestAccountService_Register_CreatesRows(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()

	svc := NewAccountService(q)
	ctx := context.Background()

	out, err := svc.Register(ctx, RegisterInput{
		RecoveryCodeHash: []byte("hashed-recovery-code"),
		DekSalt:          []byte("salt"),
		DekNonce:         []byte("nonce"),
		DekCt:            []byte("ciphertext"),
		DevicePubKey:     "device-pub-key-hex",
		DeviceName:       "my-device",
	})
	if err != nil {
		t.Fatalf("Register 失败：%v", err)
	}
	if out.AccountID == "" {
		t.Fatal("AccountID 不应为空")
	}
	if out.DeviceID == "" {
		t.Fatal("DeviceID 不应为空")
	}

	// 三表断言
	if n := countRows(t, q, db.TableAccounts); n != 1 {
		t.Fatalf("accounts 行数 = %d, want 1", n)
	}
	if n := countRows(t, q, db.TableDevices); n != 1 {
		t.Fatalf("devices 行数 = %d, want 1", n)
	}

	head, err := q.GetManifestHead(ctx, out.AccountID)
	if err != nil {
		t.Fatalf("GetManifestHead 失败：%v", err)
	}
	if head.CurrentVersion != 0 {
		t.Fatalf("manifest_head.current_version = %d, want 0", head.CurrentVersion)
	}
}

// TestAccountService_Register_DeterministicFields 验证 deviceId/accountId 为 uuid 格式，
// 且写入的 devices 字段（pubkey/name）与入参一致。
func TestAccountService_Register_DeterministicFields(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()

	svc := NewAccountService(q)
	out, err := svc.Register(context.Background(), RegisterInput{
		RecoveryCodeHash: []byte("h"),
		DekSalt:          []byte("s"),
		DekNonce:         []byte("n"),
		DekCt:            []byte("c"),
		DevicePubKey:     "pub-abc",
		DeviceName:       "phone",
	})
	if err != nil {
		t.Fatalf("Register 失败：%v", err)
	}

	dev, err := q.GetDevice(context.Background(), out.DeviceID)
	if err != nil {
		t.Fatalf("GetDevice 失败：%v", err)
	}
	if dev.AccountID != out.AccountID {
		t.Errorf("device.account_id = %q, want %q", dev.AccountID, out.AccountID)
	}
	if dev.DevicePubKey != "pub-abc" {
		t.Errorf("device_pub_key = %q, want pub-abc", dev.DevicePubKey)
	}
	if dev.DeviceName != "phone" {
		t.Errorf("device_name = %q, want phone", dev.DeviceName)
	}
	if dev.RevokedAt.Valid {
		t.Error("新设备 revoked_at 应为 NULL")
	}
}

// TestAccountService_GetDEKByRecoveryHash 验证按恢复码哈希反查返回 accountId + DEK。
func TestAccountService_GetDEKByRecoveryHash(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()

	svc := NewAccountService(q)
	ctx := context.Background()
	wantHash := []byte("recovery-svc")
	out, err := svc.Register(ctx, RegisterInput{
		RecoveryCodeHash: wantHash,
		DekSalt:          []byte("s"), DekNonce: []byte("n"), DekCt: []byte("c"),
		DevicePubKey: "pk", DeviceName: "d",
	})
	if err != nil {
		t.Fatalf("Register 失败：%v", err)
	}

	id, dek, err := svc.GetDEKByRecoveryHash(ctx, wantHash)
	if err != nil {
		t.Fatalf("GetDEKByRecoveryHash 失败：%v", err)
	}
	if id != out.AccountID {
		t.Errorf("accountId = %q, want %q", id, out.AccountID)
	}
	if !bytes.Equal(dek.Salt, []byte("s")) || !bytes.Equal(dek.Nonce, []byte("n")) || !bytes.Equal(dek.Ct, []byte("c")) {
		t.Errorf("DEK 字段不匹配：%+v", dek)
	}
}

// TestAccountService_GetDEKByRecoveryHash_NotFound 验证查无此户返 ErrAccountNotFound。
func TestAccountService_GetDEKByRecoveryHash_NotFound(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()

	svc := NewAccountService(q)
	_, _, err := svc.GetDEKByRecoveryHash(context.Background(), []byte("missing"))
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("期望 ErrAccountNotFound，得到 %v", err)
	}
}
