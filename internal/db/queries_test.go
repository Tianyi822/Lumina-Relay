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

// TestQueries_GetAccountByRecoveryHash 验证按恢复码哈希反查账户，
// 返回 accountId + DEK 信封字段。
func TestQueries_GetAccountByRecoveryHash(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	wantHash := []byte("recovery-lookup")
	if err := q.CreateAccount(ctx, CreateAccountParams{
		AccountID:        "acc-lookup",
		RecoveryCodeHash: wantHash,
		DekSalt:          []byte("s2"),
		DekNonce:         []byte("n2"),
		DekCt:            []byte("c2"),
		CreatedAt:        1,
	}); err != nil {
		t.Fatalf("CreateAccount 失败：%v", err)
	}

	row, err := q.GetAccountByRecoveryHash(ctx, wantHash)
	if err != nil {
		t.Fatalf("GetAccountByRecoveryHash 失败：%v", err)
	}
	if row.AccountID != "acc-lookup" {
		t.Errorf("AccountID = %q, want acc-lookup", row.AccountID)
	}
	if !bytes.Equal(row.DekSalt, []byte("s2")) || !bytes.Equal(row.DekNonce, []byte("n2")) || !bytes.Equal(row.DekCt, []byte("c2")) {
		t.Errorf("DEK 字段不匹配：%+v", row)
	}
}

// TestQueries_GetAccountByRecoveryHash_NotFound 验证查无此户返回错误。
func TestQueries_GetAccountByRecoveryHash_NotFound(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	if _, err := q.GetAccountByRecoveryHash(context.Background(), []byte("nope")); err == nil {
		t.Fatal("查无此户应报错")
	}
}

// TestQueries_CreateDevice_SetsLastSeenAt 验证新建设备时 last_seen_at 等于 created_at，
// GetDevice 能读回该字段。
func TestQueries_CreateDevice_SetsLastSeenAt(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-ls")

	if err := q.CreateDevice(ctx, CreateDeviceParams{
		DeviceID: "dev-ls", AccountID: "acc-ls",
		DevicePubKey: "p", DeviceName: "n", CreatedAt: 777,
	}); err != nil {
		t.Fatalf("CreateDevice 失败：%v", err)
	}
	dev, err := q.GetDevice(ctx, "dev-ls")
	if err != nil {
		t.Fatalf("GetDevice 失败：%v", err)
	}
	if dev.LastSeenAt != 777 {
		t.Fatalf("last_seen_at = %d, want 777（应等于 created_at）", dev.LastSeenAt)
	}
}

// TestQueries_TouchDeviceLastSeen 验证更新 last_seen_at 生效。
func TestQueries_TouchDeviceLastSeen(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-touch")
	if err := q.CreateDevice(ctx, CreateDeviceParams{
		DeviceID: "dev-touch", AccountID: "acc-touch",
		DevicePubKey: "p", DeviceName: "n", CreatedAt: 1,
	}); err != nil {
		t.Fatalf("CreateDevice 失败：%v", err)
	}
	if err := q.TouchDeviceLastSeen(ctx, "dev-touch", 999); err != nil {
		t.Fatalf("TouchDeviceLastSeen 失败：%v", err)
	}
	dev, _ := q.GetDevice(ctx, "dev-touch")
	if dev.LastSeenAt != 999 {
		t.Fatalf("last_seen_at = %d, want 999", dev.LastSeenAt)
	}
}

// TestQueries_ListDevicesByAccount 验证列出未吊销设备，且已吊销设备被过滤，按 created_at 升序。
func TestQueries_ListDevicesByAccount(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-list")
	// 两台活跃设备（created_at 升序：dev-a=1, dev-b=2）
	if err := q.CreateDevice(ctx, CreateDeviceParams{DeviceID: "dev-a", AccountID: "acc-list", DevicePubKey: "ka", DeviceName: "A", CreatedAt: 1}); err != nil {
		t.Fatalf("建 dev-a 失败：%v", err)
	}
	if err := q.CreateDevice(ctx, CreateDeviceParams{DeviceID: "dev-b", AccountID: "acc-list", DevicePubKey: "kb", DeviceName: "B", CreatedAt: 2}); err != nil {
		t.Fatalf("建 dev-b 失败：%v", err)
	}
	// 一台已吊销设备
	if err := q.CreateDevice(ctx, CreateDeviceParams{DeviceID: "dev-c", AccountID: "acc-list", DevicePubKey: "kc", DeviceName: "C", CreatedAt: 3}); err != nil {
		t.Fatalf("建 dev-c 失败：%v", err)
	}
	if _, err := q.RevokeDevice(ctx, "dev-c", 500); err != nil {
		t.Fatalf("吊销 dev-c 失败：%v", err)
	}

	rows, err := q.ListDevicesByAccount(ctx, "acc-list")
	if err != nil {
		t.Fatalf("ListDevicesByAccount 失败：%v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("设备数 = %d, want 2（应排除已吊销）", len(rows))
	}
	if rows[0].DeviceID != "dev-a" || rows[1].DeviceID != "dev-b" {
		t.Fatalf("顺序错误：%+v", rows)
	}
	if rows[0].DeviceName != "A" || rows[0].DevicePubKey != "ka" {
		t.Errorf("字段不匹配：%+v", rows[0])
	}
	if rows[0].LastSeenAt != 1 {
		t.Errorf("dev-a last_seen_at = %d, want 1", rows[0].LastSeenAt)
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

// TestQueries_ManifestCAS 验证 manifest 的 Insert + CAS Update + Get 全链路。
func TestQueries_ManifestCAS(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-m")
	if err := q.InsertManifestHead(ctx, "acc-m", 1); err != nil {
		t.Fatalf("InsertManifestHead 失败：%v", err)
	}

	// 插入 v1
	if err := q.InsertManifest(ctx, InsertManifestParams{
		AccountID: "acc-m", Version: 1, Ciphertext: []byte("ct1"),
		DeviceID: "d", ReceivedAt: 100,
	}); err != nil {
		t.Fatalf("InsertManifest 失败：%v", err)
	}

	// CAS：expected=0 → 推进到 1
	n, err := q.UpdateManifestHeadIfExpected(ctx, "acc-m", 0, 1, 100)
	if err != nil {
		t.Fatalf("CAS 失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("CAS 受影响行数 = %d, want 1", n)
	}

	// CAS：expected=0 再来（已到 1）→ 0 行（版本不符）
	n, _ = q.UpdateManifestHeadIfExpected(ctx, "acc-m", 0, 2, 200)
	if n != 0 {
		t.Fatalf("版本不符时 CAS 行数 = %d, want 0", n)
	}

	// 读取 v1
	row, err := q.GetManifestByAccount(ctx, "acc-m", 1)
	if err != nil {
		t.Fatalf("GetManifestByAccount 失败：%v", err)
	}
	if string(row.Ciphertext) != "ct1" {
		t.Fatalf("ciphertext = %q, want ct1", row.Ciphertext)
	}
}

// TestQueries_BlockMeta 验证 UpsertBlockMeta（幂等）+ GetBlockMeta + SumBlockSizeByAccount。
func TestQueries_BlockMeta(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-b")

	// 首次插入
	n, err := q.UpsertBlockMeta(ctx, "blk1", "acc-b", 100, 1)
	if err != nil {
		t.Fatalf("UpsertBlockMeta 失败：%v", err)
	}
	if n != 1 {
		t.Fatalf("首次受影响行数 = %d, want 1", n)
	}

	// 幂等：重复插入返 0
	n, _ = q.UpsertBlockMeta(ctx, "blk1", "acc-b", 100, 1)
	if n != 0 {
		t.Fatalf("重复插入行数 = %d, want 0", n)
	}

	// 读取元数据
	meta, err := q.GetBlockMeta(ctx, "blk1")
	if err != nil {
		t.Fatalf("GetBlockMeta 失败：%v", err)
	}
	if meta.Size != 100 || meta.AccountID != "acc-b" {
		t.Fatalf("meta = %+v", meta)
	}

	// 配额统计
	total, err := q.SumBlockSizeByAccount(ctx, "acc-b")
	if err != nil {
		t.Fatalf("SumBlockSizeByAccount 失败：%v", err)
	}
	if total != 100 {
		t.Fatalf("total = %d, want 100", total)
	}

	// 无块的账户返 0
	total, _ = q.SumBlockSizeByAccount(ctx, "acc-empty")
	if total != 0 {
		t.Fatalf("空账户 total = %d, want 0", total)
	}
}

// TestQueries_RecoveryLock_Defaults 验证新账户的恢复码锁定状态为默认值（0, 0）。
func TestQueries_RecoveryLock_Defaults(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-lock")

	row, err := q.GetRecoveryLock(ctx, "acc-lock")
	if err != nil {
		t.Fatalf("GetRecoveryLock 失败：%v", err)
	}
	if row.RecoveryFailCount != 0 || row.RecoveryLockedUntil != 0 {
		t.Fatalf("新账户锁定状态应为 (0,0)，得到 (%d,%d)", row.RecoveryFailCount, row.RecoveryLockedUntil)
	}
}

// TestQueries_IncRecoveryFail 验证失败计数累加生效。
func TestQueries_IncRecoveryFail(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-inc")
	for i := 0; i < 3; i++ {
		if err := q.IncRecoveryFail(ctx, "acc-inc"); err != nil {
			t.Fatalf("IncRecoveryFail #%d 失败：%v", i, err)
		}
	}
	row, _ := q.GetRecoveryLock(ctx, "acc-inc")
	if row.RecoveryFailCount != 3 {
		t.Fatalf("失败计数 = %d, want 3", row.RecoveryFailCount)
	}
}

// TestQueries_LockAndResetRecovery 验证锁定写入与重置清零。
func TestQueries_LockAndResetRecovery(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	ctx := context.Background()
	seedAccount(t, q, "acc-lr")

	// 锁定到 t=9999
	if err := q.LockRecovery(ctx, "acc-lr", 9999); err != nil {
		t.Fatalf("LockRecovery 失败：%v", err)
	}
	row, _ := q.GetRecoveryLock(ctx, "acc-lr")
	if row.RecoveryLockedUntil != 9999 {
		t.Fatalf("locked_until = %d, want 9999", row.RecoveryLockedUntil)
	}

	// I3：较小的值不应覆盖较大的现有值
	if err := q.LockRecovery(ctx, "acc-lr", 100); err != nil {
		t.Fatalf("LockRecovery(较小值) 失败：%v", err)
	}
	row, _ = q.GetRecoveryLock(ctx, "acc-lr")
	if row.RecoveryLockedUntil != 9999 {
		t.Fatalf("较小值不应覆盖：locked_until = %d, want 仍为 9999", row.RecoveryLockedUntil)
	}

	// 重置
	if err := q.ResetRecoveryLock(ctx, "acc-lr"); err != nil {
		t.Fatalf("ResetRecoveryLock 失败：%v", err)
	}
	row, _ = q.GetRecoveryLock(ctx, "acc-lr")
	if row.RecoveryFailCount != 0 || row.RecoveryLockedUntil != 0 {
		t.Fatalf("重置后应为 (0,0)，得到 (%d,%d)", row.RecoveryFailCount, row.RecoveryLockedUntil)
	}
}
