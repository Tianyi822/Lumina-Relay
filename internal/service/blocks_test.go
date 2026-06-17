package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"

	"lumina-relay/internal/db"
	"lumina-relay/internal/store"
)

// openBlockEnv 构造 Queries + BlockStore（基于 t.TempDir），返回两者 + cleanup。
func openBlockEnv(t *testing.T) (*db.Queries, *store.BlockStore, string, func()) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	backend, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	q := db.New(backend)
	bs := store.NewBlockStore(filepath.Join(t.TempDir(), "blocks-root"))
	accountID := "acc-blocks"
	if err := q.CreateAccount(context.Background(), db.CreateAccountParams{
		AccountID: accountID, RecoveryCodeHash: []byte("h"),
		DekSalt: []byte("s"), DekNonce: []byte("n"), DekCt: []byte("c"), CreatedAt: 1,
	}); err != nil {
		t.Fatalf("建账户失败：%v", err)
	}
	return q, bs, accountID, func() { _ = backend.Close() }
}

// sha256Hex 计算内容的 sha256 并返回 hex（即 blockId）。
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hexEncode(sum[:])
}

func hexEncode(b []byte) string {
	const chars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = chars[v>>4]
		out[i*2+1] = chars[v&0xf]
	}
	return string(out)
}

// TestBlocksService_Put_HashMismatch 验证 sha256(body) != blockId 时拒绝。
// 见 sync-design §665-668。
func TestBlocksService_Put_HashMismatch(t *testing.T) {
	q, bs, accountID, cleanup := openBlockEnv(t)
	defer cleanup()

	svc := NewBlocksService(q, bs, 1024)
	_, err := svc.Put(context.Background(), BlocksPutInput{
		AccountID: accountID,
		BlockID:   "deadbeef", // 显然不是 body 的 sha256
		Data:      []byte("some content"),
	})
	if !errors.Is(err, ErrBlockHashMismatch) {
		t.Fatalf("hash 不匹配应返 ErrBlockHashMismatch，得到 %v", err)
	}
}

// TestBlocksService_Put_Idempotent 验证重复 PUT 同一块返回 created=false 且不报错。
// 见 sync-design §350/664。
func TestBlocksService_Put_Idempotent(t *testing.T) {
	q, bs, accountID, cleanup := openBlockEnv(t)
	defer cleanup()

	svc := NewBlocksService(q, bs, 1024)
	data := []byte("the ciphertext payload")
	blockID := sha256Hex(data)

	out1, err := svc.Put(context.Background(), BlocksPutInput{
		AccountID: accountID, BlockID: blockID, Data: data,
	})
	if err != nil {
		t.Fatalf("首次 Put 失败：%v", err)
	}
	if !out1.Created {
		t.Fatal("首次应 Created=true")
	}

	out2, err := svc.Put(context.Background(), BlocksPutInput{
		AccountID: accountID, BlockID: blockID, Data: data,
	})
	if err != nil {
		t.Fatalf("二次 Put 失败：%v", err)
	}
	if out2.Created {
		t.Fatal("重复 Put 应 Created=false")
	}
}

// TestBlocksService_Have_Missing 验证批量查重返回缺失的 id。
// 见 sync-design §653-657。
func TestBlocksService_Have_Missing(t *testing.T) {
	q, bs, accountID, cleanup := openBlockEnv(t)
	defer cleanup()

	svc := NewBlocksService(q, bs, 1024)
	data := []byte("existing block")
	existID := sha256Hex(data)
	_, _ = svc.Put(context.Background(), BlocksPutInput{
		AccountID: accountID, BlockID: existID, Data: data,
	})

	missingID := sha256Hex([]byte("non-existing"))
	missing, err := svc.Have(context.Background(), accountID, []string{existID, missingID})
	if err != nil {
		t.Fatalf("Have 失败：%v", err)
	}
	if len(missing) != 1 || missing[0] != missingID {
		t.Fatalf("missing = %v, want [%s]", missing, missingID)
	}
}

// TestBlocksService_Get_RoundTrip 验证 PUT 后能 GET 回相同内容。
// 见 sync-design §596：GET /blocks/{blockId} 下载。
func TestBlocksService_Get_RoundTrip(t *testing.T) {
	q, bs, accountID, cleanup := openBlockEnv(t)
	defer cleanup()

	svc := NewBlocksService(q, bs, 1024)
	data := []byte("downloadable ciphertext")
	blockID := sha256Hex(data)
	_, _ = svc.Put(context.Background(), BlocksPutInput{
		AccountID: accountID, BlockID: blockID, Data: data,
	})

	got, err := svc.Get(context.Background(), accountID, blockID)
	if err != nil {
		t.Fatalf("Get 失败：%v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("内容不匹配：got %d bytes, want %d bytes", len(got), len(data))
	}
}

// TestBlocksService_Get_NotFound 验证下载不存在的块返回错误。
func TestBlocksService_Get_NotFound(t *testing.T) {
	q, bs, accountID, cleanup := openBlockEnv(t)
	defer cleanup()

	svc := NewBlocksService(q, bs, 1024)
	_, err := svc.Get(context.Background(), accountID, sha256Hex([]byte("nope")))
	if !errors.Is(err, ErrBlockNotFound) {
		t.Fatalf("应返 ErrBlockNotFound，得到 %v", err)
	}
}

// TestBlocksService_Put_QuotaExceeded 验证超配额时拒绝。
// 见 sync-design §700：默认 1GB/账户。
func TestBlocksService_Put_QuotaExceeded(t *testing.T) {
	q, bs, accountID, cleanup := openBlockEnv(t)
	defer cleanup()

	// quotaMB=0：任何块都超限
	svc := NewBlocksService(q, bs, 0)
	data := []byte("x")
	_, err := svc.Put(context.Background(), BlocksPutInput{
		AccountID: accountID, BlockID: sha256Hex(data), Data: data,
	})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("超配额应返 ErrQuotaExceeded，得到 %v", err)
	}
}
