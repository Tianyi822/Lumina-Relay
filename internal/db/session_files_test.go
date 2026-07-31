package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestSessionFileSnapshotCASPreservesCommittedCiphertext(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	ctx := context.Background()
	sid := "session-1-a1b2c3"

	missing, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "A", sid, 3, []byte("must-not-create"), 2)
	if err != nil || !missing.Conflict || missing.CurrentVersion != 0 {
		t.Fatalf("不存在记录的非零 base=%+v err=%v", missing, err)
	}
	created, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "A", sid, 0, []byte("cipher-v1"), 3)
	if err != nil || created.Version != 1 || created.Size != 9 {
		t.Fatalf("创建=%+v err=%v", created, err)
	}
	updated, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "A", sid, 1, []byte("cipher-v2-long"), 4)
	if err != nil || updated.Version != 2 || updated.Size != 14 {
		t.Fatalf("覆盖=%+v err=%v", updated, err)
	}
	stale, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "A", sid, 1, []byte("must-not-commit"), 5)
	if err != nil || !stale.Conflict || stale.CurrentVersion != 2 {
		t.Fatalf("过期写=%+v err=%v", stale, err)
	}
	row, err := q.GetSessionFile(ctx, "account", "group-a", sid)
	if err != nil || row.Version != 2 ||
		string(row.Ciphertext) != "cipher-v2-long" || row.Size != 14 {
		t.Fatalf("已提交快照=%+v err=%v", row, err)
	}
}

func TestSessionFileIDIsUniqueWithinAccount(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	ctx := context.Background()
	sid := "session-1-a1b2c3"

	if _, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "A", sid, 0, []byte("a"), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PutSessionFileCAS(
		ctx, "account", "group-b", "B", sid, 0, []byte("b"), 4,
	); !errors.Is(err, ErrSessionIDConflict) {
		t.Fatalf("跨组同 ID err=%v want ErrSessionIDConflict", err)
	}
	if _, err := q.GetSessionFile(
		ctx, "account", "group-b", sid,
	); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("跨组读取 err=%v want sql.ErrNoRows", err)
	}
	deleted, err := q.DeleteSessionFileCAS(
		ctx, "account", "group-b", "B", sid, 1)
	if err != nil || deleted.Deleted || deleted.Conflict {
		t.Fatalf("跨组删除=%+v err=%v", deleted, err)
	}
	row, err := q.GetSessionFile(ctx, "account", "group-a", sid)
	if err != nil || string(row.Ciphertext) != "a" {
		t.Fatalf("跨组删除后原记录=%+v err=%v", row, err)
	}
}

func TestSessionFileDeleteUsesCASAndReleasesQuota(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	ctx := context.Background()
	sid := "session-1-a1b2c3"
	body := []byte("encrypted")

	if _, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "A", sid, 0, body, 2); err != nil {
		t.Fatal(err)
	}
	stale, err := q.DeleteSessionFileCAS(
		ctx, "account", "group-a", "A", sid, 2)
	if err != nil || !stale.Conflict || stale.CurrentVersion != 1 {
		t.Fatalf("过期删除=%+v err=%v", stale, err)
	}
	deleted, err := q.DeleteSessionFileCAS(
		ctx, "account", "group-a", "A", sid, 1)
	if err != nil || !deleted.Deleted ||
		deleted.ReclaimedBytes != int64(len(body)) {
		t.Fatalf("删除=%+v err=%v", deleted, err)
	}
	account, err := q.GetAccount(ctx, "account")
	if err != nil || account.UsedBytes != 0 {
		t.Fatalf("删除后账号=%+v err=%v", account, err)
	}
	again, err := q.DeleteSessionFileCAS(
		ctx, "account", "group-a", "A", sid, 1)
	if err != nil || again.Deleted || again.Conflict {
		t.Fatalf("幂等删除=%+v err=%v", again, err)
	}
}

func TestSessionFilePutAdjustsQuotaForGrowAndShrink(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	ctx := context.Background()
	sid := "session-1-a1b2c3"

	for _, step := range []struct {
		base int64
		body string
		used int64
	}{
		{base: 0, body: "1234", used: 4},
		{base: 1, body: "123456789", used: 9},
		{base: 2, body: "12", used: 2},
	} {
		if _, err := q.PutSessionFileCAS(
			ctx, "account", "group-a", "A", sid,
			step.base, []byte(step.body), step.base+2,
		); err != nil {
			t.Fatal(err)
		}
		account, err := q.GetAccount(ctx, "account")
		if err != nil || account.UsedBytes != step.used {
			t.Fatalf("base=%d used=%d want=%d err=%v",
				step.base, account.UsedBytes, step.used, err)
		}
	}
}

func TestSessionFilePutCountsActiveBlockReservations(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	ctx := context.Background()

	if _, err := q.ReserveBlockUpload(
		ctx, "account", "A", strings.Repeat("a", 64), 700_000, 2,
	); err != nil {
		t.Fatal(err)
	}
	_, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "A", "session-1-a",
		0, bytes.Repeat([]byte("x"), 400_000), 3)
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("session PUT err=%v want ErrQuotaExceeded", err)
	}
	if _, err := q.GetSessionFile(
		ctx, "account", "group-a", "session-1-a",
	); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("配额失败后 session err=%v want sql.ErrNoRows", err)
	}
	account, err := q.GetAccount(ctx, "account")
	if err != nil || account.UsedBytes != 0 {
		t.Fatalf("配额失败后账号=%+v err=%v", account, err)
	}
}

func TestSessionFileWriterRevalidation(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	ctx := context.Background()

	if _, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "B", "session-1-a",
		0, []byte("x"), 3,
	); !errors.Is(err, ErrGroupChanged) {
		t.Fatalf("跨组写入 err=%v want ErrGroupChanged", err)
	}
	if _, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "A", "session-1-a",
		0, []byte("x"), 4,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := q.RevokeDevice(
		ctx, "account", "A", "group-a", "A", 5,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "A", "session-1-a",
		1, []byte("y"), 6,
	); !errors.Is(err, ErrInactiveDevice) {
		t.Fatalf("吊销设备写入 err=%v want ErrInactiveDevice", err)
	}
	if _, err := q.DeleteSessionFileCAS(
		ctx, "account", "group-a", "A", "session-1-a", 1,
	); !errors.Is(err, ErrInactiveDevice) {
		t.Fatalf("吊销设备删除 err=%v want ErrInactiveDevice", err)
	}
}

func TestDiscardOtherGroupsCascadesSessionFiles(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	ctx := context.Background()

	if _, err := q.PutSessionFileCAS(
		ctx, "account", "group-a", "A", "session-1-a", 0, []byte("cipher-a"), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PutSessionFileCAS(
		ctx, "account", "group-b", "B", "session-2-b", 0, []byte("cipher-b"), 4); err != nil {
		t.Fatal(err)
	}

	// B 丢弃其他组（group-a 被删）
	result, err := q.DiscardOtherGroups(ctx, "account", "B", "group-b", 1, 10)
	if err != nil {
		t.Fatalf("discard err=%v", err)
	}
	if len(result.DiscardedGroupIDs) != 1 || result.DiscardedGroupIDs[0] != "group-a" {
		t.Fatalf("DiscardedGroupIDs=%v want [group-a]", result.DiscardedGroupIDs)
	}
	// group-a 的快照行被级联删除
	if _, err := q.GetSessionFile(ctx, "account", "group-a", "session-1-a"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("被丢弃组会话文件 err=%v want ErrNoRows", err)
	}
	// 当前组不受影响
	if _, err := q.GetSessionFile(ctx, "account", "group-b", "session-2-b"); err != nil {
		t.Fatalf("当前组会话文件 err=%v", err)
	}
}
