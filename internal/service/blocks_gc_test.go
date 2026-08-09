package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lumina-relay/internal/store"
)

func TestBlockGarbageCollectionHonorsGracePeriod(t *testing.T) {
	fixture, cleanup := newConnectionFixture(t)
	defer cleanup()
	fixture.register(t)

	ctx := context.Background()
	device, err := fixture.q.GetDevice(ctx, fixture.deviceAID)
	if err != nil {
		t.Fatal(err)
	}
	blockStore := store.NewBlockStore(filepath.Join(t.TempDir(), "blocks"))
	blocks := NewBlocksService(fixture.q, blockStore)
	now := time.Unix(1_800_000_000, 0)
	blocks.now = func() time.Time { return now }

	ciphertext := []byte("ciphertext waiting for safe collection")
	hash := sha256.Sum256(ciphertext)
	blockID := hex.EncodeToString(hash[:])
	if _, err := blocks.Put(
		ctx, fixture.accountID, fixture.deviceAID, blockID, ciphertext); err != nil {
		t.Fatal(err)
	}
	group, err := fixture.q.GetSyncGroup(ctx, device.SyncGroupID.String)
	if err != nil {
		t.Fatal(err)
	}
	result, err := blocks.Prune(
		ctx, fixture.accountID, fixture.deviceAID,
		group.GroupID, group.Revision, nil)
	if err != nil || result.ReclaimedBytes != int64(len(ciphertext)) {
		t.Fatalf("prune=%+v err=%v", result, err)
	}
	if collected, err := blocks.CollectGarbage(ctx); err != nil || collected != 0 {
		t.Fatalf("宽限期内 collected=%d err=%v", collected, err)
	}
	if !blockStore.Exists(blockID) {
		t.Fatal("宽限期内物理块被提前删除")
	}
	if _, err := blocks.Put(
		ctx, fixture.accountID, fixture.deviceAID, blockID, ciphertext); err != nil {
		t.Fatalf("宽限期内恢复块关联：%v", err)
	}
	object, err := fixture.q.GetBlockObject(ctx, blockID)
	if err != nil || object.OrphanedAt.Valid || object.State != "active" {
		t.Fatalf("恢复关联后的块=%+v err=%v", object, err)
	}
	if _, err := blocks.Prune(
		ctx, fixture.accountID, fixture.deviceAID,
		group.GroupID, group.Revision, nil); err != nil {
		t.Fatalf("再次 prune：%v", err)
	}

	now = now.Add(BlockOrphanGracePeriod + time.Second)
	if collected, err := blocks.CollectGarbage(ctx); err != nil || collected != 1 {
		t.Fatalf("宽限期后 collected=%d err=%v", collected, err)
	}
	if blockStore.Exists(blockID) {
		t.Fatal("宽限期后物理块仍存在")
	}
	if _, err := fixture.q.GetBlockObject(ctx, blockID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("块元数据 err=%v want sql.ErrNoRows", err)
	}
}

// TestCollectGarbageRemovesOrphanFiles 验证 GC 回收"磁盘存在但 block_objects
// 无对应行"的孤儿物理块（PutNew 已原子安装、CommitBlockUpload 失败或进程
// 崩溃时留下），同时不误删正常块，且宽限期内不回收（保护在途上传）。
func TestCollectGarbageRemovesOrphanFiles(t *testing.T) {
	fixture, cleanup := newConnectionFixture(t)
	defer cleanup()
	fixture.register(t)

	ctx := context.Background()
	blockStore := store.NewBlockStore(filepath.Join(t.TempDir(), "blocks"))
	blocks := NewBlocksService(fixture.q, blockStore)

	// 正常块：走完整上传流程，有 DB 记录，孤儿回收必须保留。
	normalBody := []byte("normal block with db row")
	normalHash := sha256.Sum256(normalBody)
	normalID := hex.EncodeToString(normalHash[:])
	if _, err := blocks.Put(
		ctx, fixture.accountID, fixture.deviceAID, normalID, normalBody); err != nil {
		t.Fatalf("上传正常块：%v", err)
	}

	// 孤儿文件：直接写盘、不建 DB 行（模拟 PutNew 安装后 Commit 失败）。
	orphanBody := []byte("orphan file without db row")
	orphanHash := sha256.Sum256(orphanBody)
	orphanID := hex.EncodeToString(orphanHash[:])
	if _, err := blockStore.PutNew(orphanID, orphanBody); err != nil {
		t.Fatalf("写孤儿文件：%v", err)
	}

	// 宽限期内（孤儿 mtime 为当前时间）：不回收，正常块也不受影响。
	if collected, err := blocks.CollectGarbage(ctx); err != nil || collected != 0 {
		t.Fatalf("宽限期内 collected=%d err=%v", collected, err)
	}
	if !blockStore.Exists(orphanID) {
		t.Fatal("宽限期内孤儿文件被提前删除")
	}

	// 把孤儿文件 mtime 拨到超过宽限期，GC 应回收它但保留正常块。
	past := time.Now().Add(-BlockOrphanGracePeriod - time.Hour)
	if err := os.Chtimes(blockStore.PathFor(orphanID), past, past); err != nil {
		t.Fatalf("修改孤儿文件 mtime：%v", err)
	}
	if collected, err := blocks.CollectGarbage(ctx); err != nil || collected != 1 {
		t.Fatalf("宽限期后 collected=%d err=%v", collected, err)
	}
	if blockStore.Exists(orphanID) {
		t.Fatal("孤儿文件未被回收")
	}
	if !blockStore.Exists(normalID) {
		t.Fatal("正常块被孤儿回收误删")
	}
}

// TestCollectGarbageKeepsRecentReuploadedOrphan 验证带活跃上传预留的孤儿文件
// 不会被回收：文件 mtime 已超过宽限期，但 upload_reservations 仍持有引用
// （旧孤儿正在被客户端重新上传），删除会破坏进行中的提交。
func TestCollectGarbageKeepsReuploadedOrphan(t *testing.T) {
	fixture, cleanup := newConnectionFixture(t)
	defer cleanup()
	fixture.register(t)

	ctx := context.Background()
	blockStore := store.NewBlockStore(filepath.Join(t.TempDir(), "blocks"))
	blocks := NewBlocksService(fixture.q, blockStore)

	body := []byte("orphan being reuploaded")
	hash := sha256.Sum256(body)
	orphanID := hex.EncodeToString(hash[:])
	if _, err := blockStore.PutNew(orphanID, body); err != nil {
		t.Fatalf("写孤儿文件：%v", err)
	}
	past := time.Now().Add(-BlockOrphanGracePeriod - time.Hour)
	if err := os.Chtimes(blockStore.PathFor(orphanID), past, past); err != nil {
		t.Fatal(err)
	}
	// 模拟重新上传：文件已存在（PutNew 返回 ErrAlreadyExists），预留已建立。
	if _, err := fixture.q.ReserveBlockUpload(
		ctx, fixture.accountID, fixture.deviceAID, orphanID, int64(len(body)), time.Now().Unix()); err != nil {
		t.Fatalf("建立上传预留：%v", err)
	}
	if collected, err := blocks.CollectGarbage(ctx); err != nil || collected != 0 {
		t.Fatalf("带预留的孤儿 collected=%d err=%v", collected, err)
	}
	if !blockStore.Exists(orphanID) {
		t.Fatal("带活跃预留的孤儿文件被误删")
	}
}
