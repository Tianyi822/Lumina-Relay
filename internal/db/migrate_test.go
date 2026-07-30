package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateUpCreatesUnversionedSchema(t *testing.T) {
	dsn := testDSN(t)
	if err := MigrateUp(dsn); err != nil {
		t.Fatalf("MigrateUp 失败：%v", err)
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	rows, err := database.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got[name] = true
	}
	want := []string{
		"relay_meta", "accounts", "sync_groups", "devices",
		"manifest_heads", "manifests", "sync_codes", "request_nonces",
		"block_objects", "account_blocks", "device_blocks", "upload_reservations",
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("缺少表 %q", name)
		}
	}
	for _, removed := range []string{
		"blocks", "manifest_head", "request_nonces_v1",
		"account_storage_v1", "account_idempotency_v1",
	} {
		if got[removed] {
			t.Errorf("不应保留旧表 %q", removed)
		}
	}
}

func TestMigrateUpIsIdempotent(t *testing.T) {
	dsn := testDSN(t)
	if err := MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	if err := MigrateUp(dsn); err != nil {
		t.Fatalf("重复迁移失败：%v", err)
	}
}

func testDSN(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/relay.db"
}
