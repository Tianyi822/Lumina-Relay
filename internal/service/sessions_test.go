package service

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"lumina-relay/internal/db"
	"lumina-relay/internal/store"
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
	files := store.NewSessionStore(filepath.Join(t.TempDir(), "sessions"))
	return NewSessionFileService(q, files), q, func() { _ = database.Close() }
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
		QuotaBytes:           1 << 20, CreatedAt: 1,
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

func TestSessionServiceRejectsInvalidSessionID(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()

	bad := []string{
		"../etc/passwd",
		"session-1-a/b",
		"session-1-A",              // 大写非法
		"notasession",             // 前缀错
		"session--x",              // 时间戳段为空
		"session-1-",              // 随机后缀为空
		"session-1-" + longSuffix, // 超长
	}
	for _, id := range bad {
		if _, err := svc.Rewrite(ctx, "acc", "grp", "A", id, 0, []byte("x")); !errors.Is(err, ErrInvalidSessionID) {
			t.Errorf("Rewrite(%q) err=%v want ErrInvalidSessionID", id, err)
		}
		if _, err := svc.Get(ctx, "acc", "grp", id); !errors.Is(err, ErrInvalidSessionID) {
			t.Errorf("Get(%q) err=%v want ErrInvalidSessionID", id, err)
		}
	}
	// 合法 ID 放行
	if _, err := svc.Rewrite(ctx, "acc", "grp", "A", "session-1753857600000-a1b2c3", 0, []byte("x")); err != nil {
		t.Fatalf("合法 sessionId 被拒：%v", err)
	}
}

var longSuffix = string(bytes.Repeat([]byte("a"), 40))

func TestSessionServiceRewriteAppendGet(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()
	sid := "session-1-a1b2c3"

	create, err := svc.Rewrite(ctx, "acc", "grp", "A", sid, 0, []byte("meta\n"))
	if err != nil || create.Version != 1 {
		t.Fatalf("创建=%+v err=%v", create, err)
	}
	appended, err := svc.Append(ctx, "acc", "grp", "A", sid, 1, []byte("msg\n"))
	if err != nil || appended.Version != 2 || appended.Size != 9 {
		t.Fatalf("追加=%+v err=%v", appended, err)
	}
	content, err := svc.Get(ctx, "acc", "grp", sid)
	if err != nil || content.Version != 2 || string(content.Data) != "meta\nmsg\n" {
		t.Fatalf("读回=%+v err=%v", content, err)
	}
}

func TestSessionServiceStaleConflict(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()
	sid := "session-1-a1b2c3"

	if _, err := svc.Rewrite(ctx, "acc", "grp", "A", sid, 0, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	// 再用 baseVersion=0 重写 → 冲突，带回当前版本
	result, err := svc.Rewrite(ctx, "acc", "grp", "A", sid, 0, []byte("v2"))
	if !errors.Is(err, ErrStaleSessionFile) || result.CurrentVersion != 1 {
		t.Fatalf("冲突结果=%+v err=%v", result, err)
	}
	// 冲突时文件未被污染
	content, err := svc.Get(ctx, "acc", "grp", sid)
	if err != nil || string(content.Data) != "v1" {
		t.Fatalf("冲突后文件被污染=%+v err=%v", content, err)
	}
	// append baseVersion 不匹配 → 冲突
	if _, err := svc.Append(ctx, "acc", "grp", "A", sid, 5, []byte("x")); !errors.Is(err, ErrStaleSessionFile) {
		t.Fatalf("append 冲突 err=%v", err)
	}
}

func TestSessionServiceAppendMissingReturnsNotFound(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()
	if _, err := svc.Append(ctx, "acc", "grp", "A", "session-1-a1b2c3", 1, []byte("x")); !errors.Is(err, ErrSessionFileNotFound) {
		t.Fatalf("err=%v want ErrSessionFileNotFound", err)
	}
}

func TestSessionServiceSizeLimit(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()
	sid := "session-1-a1b2c3"

	oversize := bytes.Repeat([]byte("x"), MaxSessionFileBytes+1)
	if _, err := svc.Rewrite(ctx, "acc", "grp", "A", sid, 0, oversize); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("超限重写 err=%v want ErrInvalidInput", err)
	}
	// 空 body 也拒绝
	if _, err := svc.Rewrite(ctx, "acc", "grp", "A", sid, 0, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空 body err=%v want ErrInvalidInput", err)
	}
	// 追加超过总上限
	near := bytes.Repeat([]byte("x"), MaxSessionFileBytes-1)
	if _, err := svc.Rewrite(ctx, "acc", "grp", "A", sid, 0, near); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Append(ctx, "acc", "grp", "A", sid, 1, []byte("xx")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("追加越总上限 err=%v want ErrInvalidInput", err)
	}
}

func TestSessionServiceDeleteAndList(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()
	sid := "session-1-a1b2c3"

	if _, err := svc.Rewrite(ctx, "acc", "grp", "A", sid, 0, []byte("x")); err != nil {
		t.Fatal(err)
	}
	list, err := svc.List(ctx, "acc", "grp")
	if err != nil || len(list) != 1 || list[0].SessionID != sid {
		t.Fatalf("列表=%v err=%v", list, err)
	}
	deleted, err := svc.Delete(ctx, "acc", "grp", "A", sid)
	if err != nil || !deleted {
		t.Fatalf("删除=%v err=%v", deleted, err)
	}
	if _, err := svc.Get(ctx, "acc", "grp", sid); !errors.Is(err, ErrSessionFileNotFound) {
		t.Fatalf("删除后读取 err=%v want ErrSessionFileNotFound", err)
	}
}

func TestSessionServiceIndex(t *testing.T) {
	svc, q, cleanup := newSessionFileFixture(t)
	defer cleanup()
	seedSessionAccount(t, q, "acc", "A", "grp")
	ctx := context.Background()

	if _, err := svc.GetIndex(ctx, "acc", "grp"); !errors.Is(err, ErrSessionFileNotFound) {
		t.Fatalf("空索引 err=%v want ErrSessionFileNotFound", err)
	}
	put, err := svc.PutIndex(ctx, "acc", "grp", "A", 0, []byte("{\"schemaVersion\":1}"))
	if err != nil || put.Version != 1 {
		t.Fatalf("写索引=%+v err=%v", put, err)
	}
	content, err := svc.GetIndex(ctx, "acc", "grp")
	if err != nil || content.Version != 1 || string(content.Data) != "{\"schemaVersion\":1}" {
		t.Fatalf("读索引=%+v err=%v", content, err)
	}
}
