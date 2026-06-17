package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// testDSN 返回基于临时目录的 SQLite 数据库文件路径，供本包测试复用。
func testDSN(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.db")
}

func TestMigrateUp_CreatesAllTables(t *testing.T) {
	dsn := testDSN(t)
	if err := MigrateUp(dsn); err != nil {
		t.Fatalf("MigrateUp 失败：%v", err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if err != nil {
		t.Fatalf("查询 sqlite_master 失败：%v", err)
	}
	defer rows.Close()

	got := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("Scan 失败：%v", err)
		}
		got[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 rows 失败：%v", err)
	}

	want := []string{"accounts", "devices", "blocks", "manifests", "manifest_head"}
	for _, table := range want {
		if !got[table] {
			t.Errorf("迁移后缺少表 %q（实际表：%v）", table, got)
		}
	}
}
