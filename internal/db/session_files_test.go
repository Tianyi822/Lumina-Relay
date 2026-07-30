package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSessionFileCASCreateAdvanceConflict(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	ctx := context.Background()

	// 创建必须 baseVersion=0
	stale, err := q.UpsertSessionFileCAS(ctx, "account", "group-a", "A", "session-1-a", 3, 10, 5)
	if err != nil || !stale.Conflict || stale.CurrentVersion != 0 {
		t.Fatalf("不存在时 baseVersion=3 结果=%+v err=%v", stale, err)
	}
	created, err := q.UpsertSessionFileCAS(ctx, "account", "group-a", "A", "session-1-a", 0, 10, 5)
	if err != nil || created.Conflict || created.Version != 1 {
		t.Fatalf("创建=%+v err=%v", created, err)
	}
	// 正常推进
	advanced, err := q.UpsertSessionFileCAS(ctx, "account", "group-a", "A", "session-1-a", 1, 20, 6)
	if err != nil || advanced.Conflict || advanced.Version != 2 {
		t.Fatalf("推进=%+v err=%v", advanced, err)
	}
	// 旧 baseVersion 冲突
	conflict, err := q.UpsertSessionFileCAS(ctx, "account", "group-a", "A", "session-1-a", 1, 30, 7)
	if err != nil || !conflict.Conflict || conflict.CurrentVersion != 2 {
		t.Fatalf("冲突=%+v err=%v", conflict, err)
	}
	row, err := q.GetSessionFile(ctx, "account", "group-a", "session-1-a")
	if err != nil || row.Version != 2 || row.Size != 20 || row.UpdatedAt != 6 {
		t.Fatalf("注册行=%+v err=%v", row, err)
	}
}

func TestSessionFileWriterRevalidation(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	ctx := context.Background()

	// 设备不属于声称的组
	if _, err := q.UpsertSessionFileCAS(ctx, "account", "group-a", "B", "s", 0, 1, 3); !errors.Is(err, ErrGroupChanged) {
		t.Fatalf("跨组写入 err=%v want ErrGroupChanged", err)
	}
	// 吊销后的设备不能写
	if _, err := q.RevokeDevice(ctx, "account", "A", "group-a", "A", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertSessionFileCAS(ctx, "account", "group-a", "A", "s", 0, 1, 5); !errors.Is(err, ErrInactiveDevice) {
		t.Fatalf("吊销设备写入 err=%v want ErrInactiveDevice", err)
	}
	if _, err := q.DeleteSessionFile(ctx, "account", "group-a", "A", "s"); !errors.Is(err, ErrInactiveDevice) {
		t.Fatalf("吊销设备删除 err=%v want ErrInactiveDevice", err)
	}
	if _, err := q.UpsertSessionIndexCAS(ctx, "account", "group-a", "A", 0, 1, 6); !errors.Is(err, ErrInactiveDevice) {
		t.Fatalf("吊销设备写索引 err=%v want ErrInactiveDevice", err)
	}
}

func TestSessionFilesIsolatedAcrossGroups(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	ctx := context.Background()

	if _, err := q.UpsertSessionFileCAS(ctx, "account", "group-a", "A", "session-1-a", 0, 10, 3); err != nil {
		t.Fatal(err)
	}
	// 另一组看不到
	if _, err := q.GetSessionFile(ctx, "account", "group-b", "session-1-a"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("跨组读取 err=%v want ErrNoRows", err)
	}
	listB, err := q.ListSessionFiles(ctx, "account", "group-b")
	if err != nil || len(listB) != 0 {
		t.Fatalf("跨组列表=%v err=%v", listB, err)
	}
	// 同 sessionId 可在另一组独立创建
	if _, err := q.UpsertSessionFileCAS(ctx, "account", "group-b", "B", "session-1-a", 0, 20, 4); err != nil {
		t.Fatal(err)
	}
	listA, err := q.ListSessionFiles(ctx, "account", "group-a")
	if err != nil || len(listA) != 1 || listA[0].Size != 10 {
		t.Fatalf("group-a 列表=%v err=%v", listA, err)
	}
}

func TestSessionFileDelete(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	ctx := context.Background()

	if _, err := q.UpsertSessionFileCAS(ctx, "account", "group-a", "A", "s", 0, 10, 2); err != nil {
		t.Fatal(err)
	}
	deleted, err := q.DeleteSessionFile(ctx, "account", "group-a", "A", "s")
	if err != nil || !deleted {
		t.Fatalf("删除=%v err=%v", deleted, err)
	}
	deleted, err = q.DeleteSessionFile(ctx, "account", "group-a", "A", "s")
	if err != nil || deleted {
		t.Fatalf("重复删除=%v err=%v", deleted, err)
	}
}

func TestSessionIndexCAS(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	ctx := context.Background()

	if _, err := q.GetSessionIndex(ctx, "account", "group-a"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("空索引 err=%v want ErrNoRows", err)
	}
	created, err := q.UpsertSessionIndexCAS(ctx, "account", "group-a", "A", 0, 100, 2)
	if err != nil || created.Conflict || created.Version != 1 {
		t.Fatalf("创建索引=%+v err=%v", created, err)
	}
	conflict, err := q.UpsertSessionIndexCAS(ctx, "account", "group-a", "A", 0, 200, 3)
	if err != nil || !conflict.Conflict || conflict.CurrentVersion != 1 {
		t.Fatalf("索引冲突=%+v err=%v", conflict, err)
	}
	row, err := q.GetSessionIndex(ctx, "account", "group-a")
	if err != nil || row.Version != 1 || row.Size != 100 {
		t.Fatalf("索引行=%+v err=%v", row, err)
	}
}

func TestDiscardOtherGroupsCascadesSessionFiles(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()
	seedAccountAndDevice(t, q, "account", "alice", "A", "group-a", 1)
	seedAccountAndDevice(t, q, "account", "alice", "B", "group-b", 2)
	ctx := context.Background()

	if _, err := q.UpsertSessionFileCAS(ctx, "account", "group-a", "A", "session-1-a", 0, 10, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertSessionIndexCAS(ctx, "account", "group-a", "A", 0, 5, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertSessionFileCAS(ctx, "account", "group-b", "B", "session-2-b", 0, 20, 4); err != nil {
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
	// group-a 的注册行被级联删除
	if _, err := q.GetSessionFile(ctx, "account", "group-a", "session-1-a"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("被丢弃组会话文件 err=%v want ErrNoRows", err)
	}
	if _, err := q.GetSessionIndex(ctx, "account", "group-a"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("被丢弃组索引 err=%v want ErrNoRows", err)
	}
	// 当前组不受影响
	if _, err := q.GetSessionFile(ctx, "account", "group-b", "session-2-b"); err != nil {
		t.Fatalf("当前组会话文件 err=%v", err)
	}
}
