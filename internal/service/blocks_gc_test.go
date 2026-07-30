package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
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
