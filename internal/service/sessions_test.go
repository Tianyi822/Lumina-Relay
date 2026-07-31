package service

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"lumina-relay/internal/db"
)

func newSessionFileFixture(t *testing.T) (*SessionFileService, *db.Queries, func()) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "relay.db")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := db.New(database)
	return NewSessionFileService(q), q, func() { _ = database.Close() }
}

func seedSessionAccount(t *testing.T, q *db.Queries, accountID, deviceID, groupID string) {
	t.Helper()
	ctx := context.Background()
	if err := q.InsertAccount(ctx, db.CreateAccountParams{
		AccountID: accountID, Username: "alice-" + accountID,
		AuthSalt:             bytes.Repeat([]byte{1}, 16),
		LoginPublicKey:       bytes.Repeat([]byte{2}, 32),
		DEKEnvelope:          bytes.Repeat([]byte{5}, 72),
		AccountAuthPublicKey: bytes.Repeat([]byte{3}, 32),
		QuotaBytes:           8 << 20, CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.CreateDeviceEnrollment(ctx, db.CreateDeviceParams{
		DeviceID: deviceID, AccountID: accountID, SyncGroupID: groupID,
		SigningPublicKey: bytes.Repeat([]byte{4}, 32),
		DeviceName:       deviceID, CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionServicePutGetAndDeleteCAS(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()
	sid := "session-1-a1b2c3"

	first, err := svc.Put(ctx, "acc", "grp", "A", sid, 0, []byte("cipher-v1"))
	if err != nil || first.Version != 1 {
		t.Fatalf("创建=%+v err=%v", first, err)
	}
	second, err := svc.Put(ctx, "acc", "grp", "A", sid, 1, []byte("cipher-v2"))
	if err != nil || second.Version != 2 {
		t.Fatalf("覆盖=%+v err=%v", second, err)
	}
	content, err := svc.Get(ctx, "acc", "grp", sid)
	if err != nil || content.Version != 2 || string(content.Data) != "cipher-v2" {
		t.Fatalf("读取=%+v err=%v", content, err)
	}
	stale, err := svc.Delete(ctx, "acc", "grp", "A", sid, 1)
	if !errors.Is(err, ErrStaleSessionFile) || stale.CurrentVersion != 2 {
		t.Fatalf("过期删除=%+v err=%v", stale, err)
	}
	deleted, err := svc.Delete(ctx, "acc", "grp", "A", sid, 2)
	if err != nil || !deleted.Deleted {
		t.Fatalf("删除=%+v err=%v", deleted, err)
	}
}

func TestSessionServiceRejectsInvalidSessionID(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()

	bad := []string{
		"../etc/passwd",
		"session-1-a/b",
		"session-1-A",             // 大写非法
		"notasession",             // 前缀错
		"session--x",              // 时间戳段为空
		"session-1-",              // 随机后缀为空
		"session-1-" + longSuffix, // 超长
	}
	for _, id := range bad {
		if _, err := svc.Put(ctx, "acc", "grp", "A", id, 0, []byte("x")); !errors.Is(err, ErrInvalidSessionID) {
			t.Errorf("Put(%q) err=%v want ErrInvalidSessionID", id, err)
		}
		if _, err := svc.Get(ctx, "acc", "grp", id); !errors.Is(err, ErrInvalidSessionID) {
			t.Errorf("Get(%q) err=%v want ErrInvalidSessionID", id, err)
		}
		if _, err := svc.Delete(ctx, "acc", "grp", "A", id, 1); !errors.Is(err, ErrInvalidSessionID) {
			t.Errorf("Delete(%q) err=%v want ErrInvalidSessionID", id, err)
		}
	}
	// 合法 ID 放行
	if _, err := svc.Put(ctx, "acc", "grp", "A", "session-1753857600000-a1b2c3", 0, []byte("x")); err != nil {
		t.Fatalf("合法 sessionId 被拒：%v", err)
	}
}

var longSuffix = string(bytes.Repeat([]byte("a"), 40))

func TestSessionServiceInputLimits(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()
	sid := "session-1-a1b2c3"

	oversize := bytes.Repeat([]byte("x"), MaxSessionFileBytes+1)
	if _, err := svc.Put(ctx, "acc", "grp", "A", sid, 0, oversize); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("超限写入 err=%v want ErrInvalidInput", err)
	}
	// 空 body 也拒绝
	if _, err := svc.Put(ctx, "acc", "grp", "A", sid, 0, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空 body err=%v want ErrInvalidInput", err)
	}
	// 负 baseVersion 拒绝
	if _, err := svc.Put(ctx, "acc", "grp", "A", sid, -1, []byte("x")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("负 baseVersion err=%v want ErrInvalidInput", err)
	}
	// 删除的 baseVersion 必须 >= 1
	if _, err := svc.Delete(ctx, "acc", "grp", "A", sid, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("删除 baseVersion=0 err=%v want ErrInvalidInput", err)
	}
}

func TestSessionServiceSessionIDConflictAcrossGroups(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp-a")
	ctx := context.Background()
	sid := "session-1-a1b2c3"

	if err := q.CreateDeviceEnrollment(ctx, db.CreateDeviceParams{
		DeviceID: "B", AccountID: "acc", SyncGroupID: "grp-b",
		SigningPublicKey: bytes.Repeat([]byte{4}, 32),
		DeviceName:       "B", CreatedAt: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "acc", "grp-a", "A", sid, 0, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "acc", "grp-b", "B", sid, 0, []byte("b")); !errors.Is(err, ErrSessionIDConflict) {
		t.Fatalf("跨组同 ID err=%v want ErrSessionIDConflict", err)
	}
}

func TestSessionServiceGetMissingReturnsNotFound(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()

	if _, err := svc.Get(ctx, "acc", "grp", "session-1-a1b2c3"); !errors.Is(err, ErrSessionFileNotFound) {
		t.Fatalf("err=%v want ErrSessionFileNotFound", err)
	}
}

func TestSessionServiceDeleteAndList(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()
	sid := "session-1-a1b2c3"

	if _, err := svc.Put(ctx, "acc", "grp", "A", sid, 0, []byte("x")); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, "acc", "grp")
	if err != nil || len(list) != 1 || list[0].SessionID != sid {
		t.Fatalf("列表=%v err=%v", list, err)
	}
	deleted, err := svc.Delete(ctx, "acc", "grp", "A", sid, 1)
	if err != nil || !deleted.Deleted {
		t.Fatalf("删除=%+v err=%v", deleted, err)
	}
	if _, err := svc.Get(ctx, "acc", "grp", sid); !errors.Is(err, ErrSessionFileNotFound) {
		t.Fatalf("删除后读取 err=%v want ErrSessionFileNotFound", err)
	}
}
