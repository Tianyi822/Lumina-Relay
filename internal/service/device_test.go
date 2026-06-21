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
	return seedAccountWithID(t, q, "acc-device-test", []byte("the-real-recovery-hash"))
}

// seedAccountWithID 建指定 id 的账户，供需要多账户的测试复用。
func seedAccountWithID(t *testing.T, q *db.Queries, accountID string, hash []byte) (string, []byte) {
	t.Helper()
	if err := q.CreateAccount(context.Background(), db.CreateAccountParams{
		AccountID:        accountID,
		RecoveryCodeHash: hash,
		DekSalt:          []byte("s"),
		DekNonce:         []byte("n"),
		DekCt:            []byte("c"),
		CreatedAt:        1,
	}); err != nil {
		t.Fatalf("seedAccountWithID(%s) 失败：%v", accountID, err)
	}
	return accountID, hash
}

// TestRevokeDevice_OwnDevice 验证账户能吊销自己名下的设备。
func TestRevokeDevice_OwnDevice(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()
	accountID, _ := seedAccountForDevice(t, q)

	// 为该账户建一台设备
	svc := NewDeviceService(q)
	out, err := svc.RegisterDevice(context.Background(), DeviceRegisterInput{
		AccountID: accountID, RecoveryCodeHash: []byte("the-real-recovery-hash"),
		DevicePubKey: "pub", DeviceName: "d",
	})
	if err != nil {
		t.Fatalf("建设备失败：%v", err)
	}

	if err := svc.RevokeDevice(context.Background(), accountID, out.DeviceID); err != nil {
		t.Fatalf("吊销自己设备应成功，得到 %v", err)
	}
}

// TestRevokeDevice_CrossAccount_Rejected 是安全修复的核心测试：
// 账户 B 试图吊销账户 A 的设备，必须被拒（ErrDeviceForbidden）。
// 修复前：RevokeDevice 不校验归属，B 能吊销 A 的设备（越权）。
func TestRevokeDevice_CrossAccount_Rejected(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()

	// 建两个账户
	seedAccountWithID(t, q, "acc-owner", []byte("h1"))
	seedAccountWithID(t, q, "acc-attacker", []byte("h2"))

	// 为 owner 建设备
	ownerSvc := NewDeviceService(q)
	ownerDev, err := ownerSvc.RegisterDevice(context.Background(), DeviceRegisterInput{
		AccountID: "acc-owner", RecoveryCodeHash: []byte("h1"),
		DevicePubKey: "pub-owner", DeviceName: "owner-dev",
	})
	if err != nil {
		t.Fatalf("建 owner 设备失败：%v", err)
	}

	// attacker 试图吊销 owner 的设备
	err = ownerSvc.RevokeDevice(context.Background(), "acc-attacker", ownerDev.DeviceID)
	if !errors.Is(err, ErrDeviceForbidden) {
		t.Fatalf("跨账户吊销应返 ErrDeviceForbidden，得到 %v", err)
	}

	// 验证 owner 的设备未被吊销
	dev, _ := q.GetDevice(context.Background(), ownerDev.DeviceID)
	if dev.RevokedAt.Valid {
		t.Fatal("attacker 吊销失败后，owner 设备不应被标记为已吊销")
	}
}

// TestDeviceService_ListDevices 验证列出账户下未吊销设备，已吊销设备被排除。
func TestDeviceService_ListDevices(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()
	ctx := context.Background()

	accountID, wantHash := seedAccountForDevice(t, q)

	svc := NewDeviceService(q)
	// 注册两台设备（RegisterDevice 内部会校验恢复码）
	d1, err := svc.RegisterDevice(ctx, DeviceRegisterInput{
		AccountID: accountID, RecoveryCodeHash: wantHash,
		DevicePubKey: "k1", DeviceName: "one",
	})
	if err != nil {
		t.Fatalf("建设备 d1 失败：%v", err)
	}
	d2, err := svc.RegisterDevice(ctx, DeviceRegisterInput{
		AccountID: accountID, RecoveryCodeHash: wantHash,
		DevicePubKey: "k2", DeviceName: "two",
	})
	if err != nil {
		t.Fatalf("建设备 d2 失败：%v", err)
	}
	// 吊销 d2
	if err := svc.RevokeDevice(ctx, accountID, d2.DeviceID); err != nil {
		t.Fatalf("吊销 d2 失败：%v", err)
	}

	devs, err := svc.ListDevices(ctx, accountID)
	if err != nil {
		t.Fatalf("ListDevices 失败：%v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("设备数 = %d, want 1（排除已吊销）", len(devs))
	}
	if devs[0].DeviceID != d1.DeviceID || devs[0].DeviceName != "one" {
		t.Errorf("字段不匹配：%+v", devs[0])
	}
	if devs[0].LastSeenAt == 0 {
		t.Errorf("d1 last_seen_at 不应为 0")
	}
}

// TestRegisterDevice_FailsThenLocks 验证连续失败 recoveryFailThreshold 次（5 次）后
// 账户被锁定，第 6 次（即使恢复码正确）也返回 ErrAccountLocked。
func TestRegisterDevice_FailsThenLocks(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()
	ctx := context.Background()
	accountID, wantHash := seedAccountForDevice(t, q)
	wrongHash := append([]byte(nil), wantHash...)
	wrongHash[0] ^= 0xff // 翻转首字节，确保不同

	svc := NewDeviceService(q)
	// 前 5 次失败：第 5 次触发锁定
	for i := 0; i < recoveryFailThreshold; i++ {
		_, err := svc.RegisterDevice(ctx, DeviceRegisterInput{
			AccountID: accountID, RecoveryCodeHash: wrongHash,
			DevicePubKey: "k", DeviceName: "d",
		})
		if !errors.Is(err, ErrBadRecoveryCode) {
			t.Fatalf("第 %d 次失败应返回 ErrBadRecoveryCode，得到 %v", i+1, err)
		}
	}
	// 第 6 次：即使恢复码正确，也因锁定被拒
	_, err := svc.RegisterDevice(ctx, DeviceRegisterInput{
		AccountID: accountID, RecoveryCodeHash: wantHash,
		DevicePubKey: "k", DeviceName: "d",
	})
	if !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("锁定后应返回 ErrAccountLocked，得到 %v", err)
	}
}

// TestRegisterDevice_SuccessResetsFailCount 验证恢复码正确时计数重置，
// 不会因零星失败累积到锁定。
func TestRegisterDevice_SuccessResetsFailCount(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()
	ctx := context.Background()
	accountID, wantHash := seedAccountForDevice(t, q)
	wrongHash := append([]byte(nil), wantHash...)
	wrongHash[0] ^= 0xff

	svc := NewDeviceService(q)
	// 失败 2 次（未达阈值）
	for i := 0; i < 2; i++ {
		svc.RegisterDevice(ctx, DeviceRegisterInput{
			AccountID: accountID, RecoveryCodeHash: wrongHash,
			DevicePubKey: "k", DeviceName: "d",
		})
	}
	// 成功一次，计数应清零
	if _, err := svc.RegisterDevice(ctx, DeviceRegisterInput{
		AccountID: accountID, RecoveryCodeHash: wantHash,
		DevicePubKey: "k-correct", DeviceName: "d",
	}); err != nil {
		t.Fatalf("正确恢复码应成功，得到 %v", err)
	}
	// 再失败 4 次（若计数未重置，累计 2+4=6 会锁；重置后 4<5 不锁）
	wrongHash2 := append([]byte(nil), wantHash...)
	wrongHash2[1] ^= 0xff
	for i := 0; i < 4; i++ {
		_, err := svc.RegisterDevice(ctx, DeviceRegisterInput{
			AccountID: accountID, RecoveryCodeHash: wrongHash2,
			DevicePubKey: "k", DeviceName: "d",
		})
		if errors.Is(err, ErrAccountLocked) {
			t.Fatalf("第 %d 次失败不应触发锁定（计数已重置），得到 %v", i+1, err)
		}
	}
}

