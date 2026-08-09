package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
)

func seedAccountAndDevice(t *testing.T, q *Queries, accountID, username, deviceID, groupID string, now int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := q.GetAccount(ctx, accountID); err != nil {
		err := q.InsertAccount(ctx, CreateAccountParams{
			AccountID: accountID, Username: username,
			AuthSalt:             bytes.Repeat([]byte{1}, 16),
			LoginPublicKey:       bytes.Repeat([]byte{2}, 32),
			DEKEnvelope:          bytes.Repeat([]byte{5}, 72),
			AccountAuthPublicKey: bytes.Repeat([]byte{3}, 32),
			QuotaBytes:           1 << 20, CreatedAt: now,
		})
		if err != nil {
			t.Fatalf("创建账户：%v", err)
		}
	}
	if err := q.CreateDeviceEnrollment(ctx, CreateDeviceParams{
		DeviceID: deviceID, AccountID: accountID, SyncGroupID: groupID,
		SigningPublicKey: bytes.Repeat([]byte{4}, 32),
		DeviceName:       deviceID, CreatedAt: now,
	}); err != nil {
		t.Fatalf("创建设备：%v", err)
	}
}

func TestDeviceManifestHeadsAreIndependent(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)

	ctx := context.Background()
	a, err := q.PutDeviceManifest(ctx, "A", 0, []byte("cipher-A"), 10)
	if err != nil || a.Version != 1 {
		t.Fatalf("A Manifest=%+v err=%v", a, err)
	}
	b, err := q.PutDeviceManifest(ctx, "B", 0, []byte("cipher-B"), 11)
	if err != nil || b.Version != 1 {
		t.Fatalf("B Manifest=%+v err=%v", b, err)
	}
	conflict, err := q.PutDeviceManifest(ctx, "A", 0, []byte("different"), 12)
	if err != nil || !conflict.Conflict || conflict.CurrentVersion != 1 {
		t.Fatalf("A stale=%+v err=%v", conflict, err)
	}
	retry, err := q.PutDeviceManifest(ctx, "A", 0, []byte("cipher-A"), 13)
	if err != nil || !retry.Idempotent || retry.Version != 1 {
		t.Fatalf("A retry=%+v err=%v", retry, err)
	}
}

func TestRedeemSyncCodeMergesTransitively(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	seedAccountAndDevice(t, q, "account", "alice", "C", "group-c", 3)
	ctx := context.Background()

	firstMAC := sha256.Sum256([]byte("first"))
	if err := q.ReplaceSyncCode(ctx, SyncCodeRow{
		CodeID: "code-1", AccountID: "account", SyncGroupID: "group-a",
		InviterDeviceID: "A", CodeMAC: firstMAC[:], ExpiresAt: 100, CreatedAt: 4,
	}, 4); err != nil {
		t.Fatal(err)
	}
	ab, err := q.RedeemSyncCode(ctx, "account", "B", firstMAC[:], 5)
	if err != nil || ab.AlreadyJoined || len(ab.AffectedDeviceIDs) != 2 {
		t.Fatalf("合并 A+B=%+v err=%v", ab, err)
	}

	secondMAC := sha256.Sum256([]byte("second"))
	if err := q.ReplaceSyncCode(ctx, SyncCodeRow{
		CodeID: "code-2", AccountID: "account", SyncGroupID: ab.CanonicalGroupID,
		InviterDeviceID: "B", CodeMAC: secondMAC[:], ExpiresAt: 100, CreatedAt: 6,
	}, 6); err != nil {
		t.Fatal(err)
	}
	abc, err := q.RedeemSyncCode(ctx, "account", "C", secondMAC[:], 7)
	if err != nil || len(abc.AffectedDeviceIDs) != 3 {
		t.Fatalf("合并 C=%+v err=%v", abc, err)
	}
	for _, id := range []string{"A", "B", "C"} {
		device, err := q.GetDevice(ctx, id)
		if err != nil || !device.SyncGroupID.Valid || device.SyncGroupID.String != abc.CanonicalGroupID {
			t.Fatalf("设备 %s 未进入 canonical group：%+v err=%v", id, device, err)
		}
	}
}

func TestRedeemSyncCodePreservesBothGroupsSessionFiles(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	ctx := context.Background()

	if _, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "A", "session-1-a", 0, []byte("A"), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PutSessionFileCAS(
		ctx, "account", "group-b", "B", "session-2-b", 0, []byte("BB"), 4); err != nil {
		t.Fatal(err)
	}
	mac := sha256.Sum256([]byte("merge-sessions"))
	if err := q.ReplaceSyncCode(ctx, SyncCodeRow{
		CodeID: "session-merge", AccountID: "account", SyncGroupID: "group-a",
		InviterDeviceID: "A", CodeMAC: mac[:], ExpiresAt: 100, CreatedAt: 5,
	}, 5); err != nil {
		t.Fatal(err)
	}
	merged, err := q.RedeemSyncCode(ctx, "account", "B", mac[:], 6)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := q.ListSessionFiles(ctx, "account", merged.CanonicalGroupID)
	if err != nil || len(rows) != 2 ||
		rows[0].SessionID != "session-1-a" ||
		rows[1].SessionID != "session-2-b" {
		t.Fatalf("合并后 sessions=%+v err=%v", rows, err)
	}
}

func TestSyncCodeExpiresAndCannotBeReplayed(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	ctx := context.Background()

	expiredMAC := sha256.Sum256([]byte("expired"))
	if err := q.ReplaceSyncCode(ctx, SyncCodeRow{
		CodeID: "expired", AccountID: "account", SyncGroupID: "group-a",
		InviterDeviceID: "A", CodeMAC: expiredMAC[:], ExpiresAt: 10, CreatedAt: 3,
	}, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RedeemSyncCode(ctx, "account", "B", expiredMAC[:], 10); !errors.Is(err, ErrInvalidSyncCode) {
		t.Fatalf("过期邀请码 err=%v want ErrInvalidSyncCode", err)
	}

	activeMAC := sha256.Sum256([]byte("active"))
	if err := q.ReplaceSyncCode(ctx, SyncCodeRow{
		CodeID: "active", AccountID: "account", SyncGroupID: "group-a",
		InviterDeviceID: "A", CodeMAC: activeMAC[:], ExpiresAt: 100, CreatedAt: 11,
	}, 11); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RedeemSyncCode(ctx, "account", "B", activeMAC[:], 12); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RedeemSyncCode(ctx, "account", "B", activeMAC[:], 13); !errors.Is(err, ErrInvalidSyncCode) {
		t.Fatalf("重放邀请码 err=%v want ErrInvalidSyncCode", err)
	}
}

func TestSyncCodeCannotCrossAccounts(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account-a", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account-a", "alice", "B", "group-b", 2)
	seedAccountAndDevice(t, q, "account-b", "bob", "X", "group-x", 3)
	ctx := context.Background()
	codeMAC := sha256.Sum256([]byte("account-bound"))
	if err := q.ReplaceSyncCode(ctx, SyncCodeRow{
		CodeID: "account-bound", AccountID: "account-a", SyncGroupID: "group-a",
		InviterDeviceID: "A", CodeMAC: codeMAC[:], ExpiresAt: 100, CreatedAt: 4,
	}, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RedeemSyncCode(
		ctx, "account-b", "X", codeMAC[:], 5); !errors.Is(err, ErrInvalidSyncCode) {
		t.Fatalf("跨账号兑换 err=%v want ErrInvalidSyncCode", err)
	}
	if _, err := q.RedeemSyncCode(ctx, "account-a", "B", codeMAC[:], 6); err != nil {
		t.Fatalf("跨账号失败不应消费原邀请码：%v", err)
	}
}

func TestRedeemSyncCodeAlreadyJoinedConsumesCode(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	ctx := context.Background()

	joinMAC := sha256.Sum256([]byte("join-first"))
	if err := q.ReplaceSyncCode(ctx, SyncCodeRow{
		CodeID: "join-first", AccountID: "account", SyncGroupID: "group-a",
		InviterDeviceID: "A", CodeMAC: joinMAC[:], ExpiresAt: 100, CreatedAt: 3,
	}, 3); err != nil {
		t.Fatal(err)
	}
	merged, err := q.RedeemSyncCode(ctx, "account", "B", joinMAC[:], 4)
	if err != nil {
		t.Fatal(err)
	}

	sameGroupMAC := sha256.Sum256([]byte("same-group"))
	if err := q.ReplaceSyncCode(ctx, SyncCodeRow{
		CodeID: "same-group", AccountID: "account", SyncGroupID: merged.CanonicalGroupID,
		InviterDeviceID: "A", CodeMAC: sameGroupMAC[:], ExpiresAt: 100, CreatedAt: 5,
	}, 5); err != nil {
		t.Fatal(err)
	}
	result, err := q.RedeemSyncCode(ctx, "account", "B", sameGroupMAC[:], 6)
	if err != nil || !result.AlreadyJoined {
		t.Fatalf("同组兑换=%+v err=%v", result, err)
	}
	if _, err := q.RedeemSyncCode(ctx, "account", "B", sameGroupMAC[:], 7); !errors.Is(err, ErrInvalidSyncCode) {
		t.Fatalf("同组邀请码重放 err=%v want ErrInvalidSyncCode", err)
	}
}

func TestConcurrentRedeemSyncCodeHasSingleWinner(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	seedAccountAndDevice(t, q, "account", "alice", "C", "group-c", 3)
	ctx := context.Background()
	codeMAC := sha256.Sum256([]byte("race"))
	if err := q.ReplaceSyncCode(ctx, SyncCodeRow{
		CodeID: "race", AccountID: "account", SyncGroupID: "group-a",
		InviterDeviceID: "A", CodeMAC: codeMAC[:], ExpiresAt: 100, CreatedAt: 4,
	}, 4); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, deviceID := range []string{"B", "C"} {
		deviceID := deviceID
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := q.RedeemSyncCode(ctx, "account", deviceID, codeMAC[:], 5)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var succeeded, rejected int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrInvalidSyncCode):
			rejected++
		default:
			t.Fatalf("并发兑换出现意外错误：%v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("并发兑换 succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestBlockVisibilityFollowsSyncGroup(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	ctx := context.Background()
	blockID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := q.AttachBlock(ctx, "account", "A", blockID, 10, 3); err != nil {
		t.Fatal(err)
	}
	visible, err := q.IsBlockVisible(ctx, "account", "group-b", blockID)
	if err != nil || visible {
		t.Fatalf("合并前 B 不应可见：visible=%v err=%v", visible, err)
	}
	mac := sha256.Sum256([]byte("join"))
	if err := q.ReplaceSyncCode(ctx, SyncCodeRow{
		CodeID: "join", AccountID: "account", SyncGroupID: "group-a",
		InviterDeviceID: "A", CodeMAC: mac[:], ExpiresAt: 100, CreatedAt: 4,
	}, 4); err != nil {
		t.Fatal(err)
	}
	merged, err := q.RedeemSyncCode(ctx, "account", "B", mac[:], 5)
	if err != nil {
		t.Fatal(err)
	}
	visible, err = q.IsBlockVisible(ctx, "account", merged.CanonicalGroupID, blockID)
	if err != nil || !visible {
		t.Fatalf("合并后 B 应可见：visible=%v err=%v", visible, err)
	}
}

func TestUploadReservationsPreventConcurrentQuotaOversubscription(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	ctx := context.Background()
	firstID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	secondID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := q.ReserveBlockUpload(ctx, "account", "A", firstID, 700_000, 10); err != nil {
		t.Fatalf("首次 reservation：%v", err)
	}
	if _, err := q.ReserveBlockUpload(ctx, "account", "B", secondID, 700_000, 10); err != ErrQuotaExceeded {
		t.Fatalf("第二次 reservation err=%v want ErrQuotaExceeded", err)
	}
	var reservations int
	if err := q.CountRows(ctx, &reservations, TableUploadReservations); err != nil {
		t.Fatal(err)
	}
	if reservations != 1 {
		t.Fatalf("reservations=%d want=1", reservations)
	}
}
