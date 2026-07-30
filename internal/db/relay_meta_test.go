package db

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"sync"
	"testing"
)

// TestQueries_GetOrCreateInstanceID_Persists 验证 instanceId 首次随机生成后写入数据库，
// 关闭并重新打开同一数据库仍返回完全相同的值。
func TestQueries_GetOrCreateInstanceID_Persists(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "relay.db")
	if err := MigrateUp(dsn); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	firstDB, err := Open(dsn)
	if err != nil {
		t.Fatalf("首次打开数据库失败：%v", err)
	}
	firstID, err := New(firstDB).GetOrCreateInstanceID(context.Background())
	if err != nil {
		_ = firstDB.Close()
		t.Fatalf("首次获取 instanceId 失败：%v", err)
	}
	if err := firstDB.Close(); err != nil {
		t.Fatalf("关闭首次数据库连接失败：%v", err)
	}

	raw, err := base64.RawURLEncoding.DecodeString(firstID)
	if err != nil {
		t.Fatalf("instanceId 不是合法 base64url：%v", err)
	}
	if len(raw) != instanceIDByteLen {
		t.Fatalf("instanceId 解码长度 = %d, want %d", len(raw), instanceIDByteLen)
	}

	secondDB, err := Open(dsn)
	if err != nil {
		t.Fatalf("重新打开数据库失败：%v", err)
	}
	defer secondDB.Close()

	secondID, err := New(secondDB).GetOrCreateInstanceID(context.Background())
	if err != nil {
		t.Fatalf("重启后获取 instanceId 失败：%v", err)
	}
	if secondID != firstID {
		t.Fatalf("重启后 instanceId 改变：first=%q second=%q", firstID, secondID)
	}

	var rows int
	if err := secondDB.QueryRow("SELECT COUNT(*) FROM relay_meta").Scan(&rows); err != nil {
		t.Fatalf("统计 relay_meta 失败：%v", err)
	}
	if rows != 1 {
		t.Fatalf("relay_meta 行数 = %d, want 1", rows)
	}
}

// TestQueries_GetOrCreateInstanceID_Concurrent 验证空数据库上的并发初始化只会持久化一个
// instanceId，所有调用方最终观察到同一个值。
func TestQueries_GetOrCreateInstanceID_Concurrent(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()

	const workers = 16
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			id, err := q.GetOrCreateInstanceID(context.Background())
			if err != nil {
				errs <- err
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)

	for err := range errs {
		t.Errorf("并发获取 instanceId 失败：%v", err)
	}

	var first string
	for id := range ids {
		if first == "" {
			first = id
			continue
		}
		if id != first {
			t.Errorf("并发调用返回不同 instanceId：first=%q got=%q", first, id)
		}
	}
	if first == "" {
		t.Fatal("并发调用没有返回 instanceId")
	}
}

// TestQueries_GetOrCreateInstanceID_RejectsCorruptPersistentValue 验证数据库损坏时
// 不把空值/非规范 ID 暴露给 discovery 或签名 transcript。
func TestQueries_GetOrCreateInstanceID_RejectsCorruptPersistentValue(t *testing.T) {
	q, cleanup := openTestQueries(t)
	defer cleanup()

	if _, err := q.db.ExecContext(context.Background(),
		"INSERT INTO relay_meta (key, value) VALUES ('instance_id', ?)", "not-base64url!",
	); err != nil {
		t.Fatalf("插入损坏 instanceId 失败：%v", err)
	}
	if _, err := q.GetOrCreateInstanceID(context.Background()); err == nil {
		t.Fatal("损坏的持久 instanceId 应导致启动失败")
	}
}
