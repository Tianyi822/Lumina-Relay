package service

import (
	"context"
	"errors"
	"testing"

	"lumina-relay/internal/db"
)

// TestDeviceService_RegisterDevice_ValidRecoveryCode 验证：
// 用正确恢复码 hash 注册设备 → 成功，返回 deviceId，且 devices 表多一行。
// 见 sync-design §255-259 与计划 Task 11a Step 1。
//
// 约定（计划 §606）：客户端算 hash 上传，服务端做字节比对。
func TestDeviceService_RegisterDevice_ValidRecoveryCode(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()

	accountID, wantHash := seedAccountForDevice(t, q)

	svc := NewDeviceService(q)
	out, err := svc.RegisterDevice(context.Background(), DeviceRegisterInput{
		AccountID:        accountID,
		RecoveryCodeHash: wantHash,
		DevicePubKey:     "device-pub-b",
		DeviceName:       "laptop",
	})
	if err != nil {
		t.Fatalf("RegisterDevice 失败：%v", err)
	}
	if out.DeviceID == "" {
		t.Fatal("DeviceID 不应为空")
	}

	if n := countRows(t, q, db.TableDevices); n != 1 {
		t.Fatalf("devices 行数 = %d, want 1", n)
	}
	dev, err := q.GetDevice(context.Background(), out.DeviceID)
	if err != nil {
		t.Fatalf("GetDevice 失败：%v", err)
	}
	if dev.AccountID != accountID {
		t.Errorf("device.account_id = %q, want %q", dev.AccountID, accountID)
	}
	if dev.DevicePubKey != "device-pub-b" {
		t.Errorf("device_pub_key = %q, want device-pub-b", dev.DevicePubKey)
	}
}

// TestDeviceService_RegisterDevice_BadRecoveryCode 验证恢复码不匹配时拒绝。
func TestDeviceService_RegisterDevice_BadRecoveryCode(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()

	accountID, _ := seedAccountForDevice(t, q)

	svc := NewDeviceService(q)
	_, err := svc.RegisterDevice(context.Background(), DeviceRegisterInput{
		AccountID:        accountID,
		RecoveryCodeHash: []byte("wrong-hash-bytes"),
		DevicePubKey:     "pub",
		DeviceName:       "x",
	})
	if !errors.Is(err, ErrBadRecoveryCode) {
		t.Fatalf("错误恢复码应返回 ErrBadRecoveryCode，得到 %v", err)
	}
	if n := countRows(t, q, db.TableDevices); n != 0 {
		t.Fatalf("拒绝后不应建设备，devices 行数 = %d", n)
	}
}

// TestDeviceService_RegisterDevice_AccountNotFound 验证账户不存在时拒绝。
func TestDeviceService_RegisterDevice_AccountNotFound(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()

	svc := NewDeviceService(q)
	_, err := svc.RegisterDevice(context.Background(), DeviceRegisterInput{
		AccountID:        "no-such-account",
		RecoveryCodeHash: []byte("h"),
		DevicePubKey:     "pub",
		DeviceName:       "x",
	})
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("账户不存在应返回 ErrAccountNotFound，得到 %v", err)
	}
}

// seedAccountForDevice 直接写一行账户（不经 Register，避免建设备），
// 返回 accountId 与已存的 recoveryCodeHash。测试用同一 hash 模拟客户端正确输入。
func seedAccountForDevice(t *testing.T, q *db.Queries) (string, []byte) {
	t.Helper()
	accountID := "acc-device-test"
	wantHash := []byte("the-real-recovery-hash")
	if err := q.CreateAccount(context.Background(), db.CreateAccountParams{
		AccountID:        accountID,
		RecoveryCodeHash: wantHash,
		DekSalt:          []byte("s"),
		DekNonce:         []byte("n"),
		DekCt:            []byte("c"),
		CreatedAt:        1,
	}); err != nil {
		t.Fatalf("seedAccount 失败：%v", err)
	}
	return accountID, wantHash
}

