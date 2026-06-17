package service

import (
	"context"
	"testing"

	"lumina-relay/internal/db"
)

// seedAccountForManifest 建账户（含 manifest_head，version=0）。
func seedAccountForManifest(t *testing.T, q *db.Queries) string {
	t.Helper()
	accountID := "acc-manifest"
	if err := q.CreateAccount(context.Background(), db.CreateAccountParams{
		AccountID: accountID, RecoveryCodeHash: []byte("h"),
		DekSalt: []byte("s"), DekNonce: []byte("n"), DekCt: []byte("c"), CreatedAt: 1,
	}); err != nil {
		t.Fatalf("建账户失败：%v", err)
	}
	if err := q.InsertManifestHead(context.Background(), accountID, 1); err != nil {
		t.Fatalf("初始化 manifest_head 失败：%v", err)
	}
	return accountID
}

// TestManifestService_Put_IncrementsVersion 验证：baseVersion 与 head 一致时，
// 插入新 manifest 并递增 head 版本。
// 见 sync-design §643-647。
func TestManifestService_Put_IncrementsVersion(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()
	accountID := seedAccountForManifest(t, q)

	svc := NewManifestService(q)
	out, err := svc.Put(context.Background(), ManifestPutInput{
		AccountID:   accountID,
		Ciphertext:  []byte("encrypted-manifest-v1"),
		BaseVersion: 0,
		DeviceID:    "dev-1",
	})
	if err != nil {
		t.Fatalf("Put 失败：%v", err)
	}
	if out.NewVersion != 1 {
		t.Fatalf("NewVersion = %d, want 1", out.NewVersion)
	}

	// head 应已前移
	head, _ := q.GetManifestHead(context.Background(), accountID)
	if head.CurrentVersion != 1 {
		t.Fatalf("head.current_version = %d, want 1", head.CurrentVersion)
	}
}

// TestManifestService_Put_StaleBase 验证：baseVersion 落后于 head 时返回冲突，
// 且 extra 含 currentVersion。
// 见 sync-design §648：响应 409 + currentVersion。
func TestManifestService_Put_StaleBase(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()
	accountID := seedAccountForManifest(t, q)

	svc := NewManifestService(q)
	// 先成功提交 v1
	if _, err := svc.Put(context.Background(), ManifestPutInput{
		AccountID: accountID, Ciphertext: []byte("v1"), BaseVersion: 0, DeviceID: "d",
	}); err != nil {
		t.Fatalf("首次 Put 失败：%v", err)
	}

	// 用过期的 baseVersion=0 再提交 → 冲突
	out, err := svc.Put(context.Background(), ManifestPutInput{
		AccountID: accountID, Ciphertext: []byte("v2"), BaseVersion: 0, DeviceID: "d",
	})
	if err != nil {
		t.Fatalf("冲突不应返 error（用 Conflict 字段表达）：%v", err)
	}
	if !out.Conflict {
		t.Fatal("baseVersion 过期应标记 Conflict")
	}
	if out.CurrentVersion != 1 {
		t.Fatalf("CurrentVersion = %d, want 1", out.CurrentVersion)
	}
	// 冲突时不应插入新版本
	if out.NewVersion != 0 {
		t.Fatalf("冲突时 NewVersion 应为 0，得到 %d", out.NewVersion)
	}
}

// TestManifestService_Get_Current 验证读取当前版本的 manifest ciphertext。
// 见 sync-design §407：GET /manifest → 拿到最新版本 + 内容。
func TestManifestService_Get_Current(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()
	accountID := seedAccountForManifest(t, q)

	svc := NewManifestService(q)
	wantCT := []byte("encrypted-manifest-v1")
	if _, err := svc.Put(context.Background(), ManifestPutInput{
		AccountID: accountID, Ciphertext: wantCT, BaseVersion: 0, DeviceID: "d",
	}); err != nil {
		t.Fatalf("Put 失败：%v", err)
	}

	out, err := svc.Get(context.Background(), accountID)
	if err != nil {
		t.Fatalf("Get 失败：%v", err)
	}
	if out.Version != 1 {
		t.Fatalf("Version = %d, want 1", out.Version)
	}
	if string(out.Ciphertext) != string(wantCT) {
		t.Fatalf("Ciphertext 不匹配")
	}
}

// TestManifestService_Get_Empty 验证无任何 manifest 版本时（version=0）返回空 + version 0。
// 首次同步场景：客户端 localManifestVersion=null，GET 拿到 version 0 表示"无内容"。
func TestManifestService_Get_Empty(t *testing.T) {
	q, cleanup := openQueries(t)
	defer cleanup()
	accountID := seedAccountForManifest(t, q)

	svc := NewManifestService(q)
	out, err := svc.Get(context.Background(), accountID)
	if err != nil {
		t.Fatalf("Get 空账户失败：%v", err)
	}
	if out.Version != 0 {
		t.Fatalf("Version = %d, want 0", out.Version)
	}
	if len(out.Ciphertext) != 0 {
		t.Fatalf("空账户 Ciphertext 应为 nil")
	}
}
