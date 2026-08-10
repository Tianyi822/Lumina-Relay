package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"lumina-relay/internal/db"
	"lumina-relay/internal/store"
)

var (
	ErrBlockHashMismatch = errors.New("block hash mismatch")
	ErrBlockNotFound     = errors.New("block not found")
	ErrBlockBusy         = errors.New("block upload busy")
	ErrQuotaExceeded     = errors.New("quota exceeded")
)

const BlockOrphanGracePeriod = 24 * time.Hour

var blockIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// MaxMissingIDs 是 /blocks/missing 单次请求允许的 ID 数上限。受 64KiB JSON
// body limit 约束：每条 64-hex ID 加引号逗号约 68 字节，65536/68 ≈ 963。
// 取 900 留出数组包装与边界余量，确保不超过 body limit；discovery 与本
// 常量必须一致（见 handler/discovery.go）。
const MaxMissingIDs = 900

type BlocksService struct {
	q   *db.Queries
	bs  *store.BlockStore
	now func() time.Time
}

func NewBlocksService(q *db.Queries, blockStore *store.BlockStore) *BlocksService {
	return &BlocksService{q: q, bs: blockStore, now: time.Now}
}

type BlocksPutResult struct {
	Created bool
}

func (s *BlocksService) Put(
	ctx context.Context,
	accountID, deviceID, blockID string,
	data []byte,
) (BlocksPutResult, error) {
	if !blockIDPattern.MatchString(blockID) {
		return BlocksPutResult{}, ErrBlockHashMismatch
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != blockID {
		return BlocksPutResult{}, ErrBlockHashMismatch
	}
	now := s.now()
	reservation, err := s.q.ReserveBlockUpload(
		ctx, accountID, deviceID, blockID, int64(len(data)), now.Unix())
	if errors.Is(err, db.ErrQuotaExceeded) {
		return BlocksPutResult{}, ErrQuotaExceeded
	}
	if errors.Is(err, db.ErrUploadInProgress) {
		return BlocksPutResult{}, ErrBlockBusy
	}
	if err != nil {
		return BlocksPutResult{}, fmt.Errorf("预留上传配额：%w", err)
	}
	created, err := s.bs.PutNew(blockID, data)
	if err != nil && !errors.Is(err, store.ErrAlreadyExists) {
		_ = s.q.CancelBlockUpload(ctx, reservation.ReservationID)
		return BlocksPutResult{}, fmt.Errorf("保存块文件：%w", err)
	}
	attached, err := s.q.CommitBlockUpload(
		ctx, reservation.ReservationID, now.Unix())
	if errors.Is(err, db.ErrQuotaExceeded) {
		_ = s.q.CancelBlockUpload(ctx, reservation.ReservationID)
		return BlocksPutResult{}, ErrQuotaExceeded
	}
	if errors.Is(err, db.ErrUploadInProgress) {
		_ = s.q.CancelBlockUpload(ctx, reservation.ReservationID)
		return BlocksPutResult{}, ErrBlockBusy
	}
	if err != nil {
		_ = s.q.CancelBlockUpload(ctx, reservation.ReservationID)
		return BlocksPutResult{}, fmt.Errorf("关联块：%w", err)
	}
	return BlocksPutResult{Created: created || attached.AccountAssociation}, nil
}

func (s *BlocksService) Missing(
	ctx context.Context,
	accountID, groupID string,
	ids []string,
) ([]string, error) {
	if len(ids) > MaxMissingIDs {
		return nil, ErrInvalidInput
	}
	missing := make([]string, 0)
	for _, id := range ids {
		if !blockIDPattern.MatchString(id) {
			return nil, ErrInvalidInput
		}
		visible, err := s.q.IsBlockVisible(ctx, accountID, groupID, id)
		if err != nil {
			return nil, err
		}
		if !visible || !s.bs.Exists(id) {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func (s *BlocksService) Get(
	ctx context.Context,
	accountID, groupID, blockID string,
) ([]byte, error) {
	if !blockIDPattern.MatchString(blockID) {
		return nil, ErrBlockNotFound
	}
	visible, err := s.q.IsBlockVisible(ctx, accountID, groupID, blockID)
	if err != nil || !visible {
		return nil, ErrBlockNotFound
	}
	data, err := s.bs.Get(blockID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrBlockNotFound
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (s *BlocksService) Prune(
	ctx context.Context,
	accountID, deviceID, groupID string,
	groupRevision int64,
	keepIDs []string,
) (db.PruneResult, error) {
	keep := make(map[string]struct{}, len(keepIDs))
	for _, id := range keepIDs {
		if !blockIDPattern.MatchString(id) {
			return db.PruneResult{}, ErrInvalidInput
		}
		keep[id] = struct{}{}
	}
	result, err := s.q.PruneGroupBlocks(
		ctx, accountID, deviceID, groupID, groupRevision, keep, s.now().Unix())
	if errors.Is(err, db.ErrInactiveDevice) {
		return db.PruneResult{}, ErrDeviceRevoked
	}
	if errors.Is(err, db.ErrGroupChanged) {
		return db.PruneResult{}, ErrGroupChanged
	}
	if err != nil {
		return db.PruneResult{}, err
	}
	return result, nil
}

// CollectGarbage 回收已超过宽限期的物理孤儿块。数据库先把对象认领为
// deleting，使新上传在文件删除窗口内返回可重试冲突；随后补扫磁盘上
// 没有任何 DB 行的孤儿文件（PutNew 已安装而 CommitBlockUpload 失败的残留）。
func (s *BlocksService) CollectGarbage(ctx context.Context) (int, error) {
	now := s.now()
	ids, err := s.q.ClaimCollectibleBlocks(
		ctx, now.Add(-BlockOrphanGracePeriod).Unix(), now.Unix(), 256)
	if err != nil {
		return 0, err
	}
	collected := 0
	var errs []error
	for _, id := range ids {
		if err := s.bs.Delete(id); err != nil {
			errs = append(errs, err)
			continue
		}
		finalized, err := s.q.FinalizeBlockDeletion(ctx, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !finalized {
			errs = append(errs, fmt.Errorf("块 %s 的 GC 认领已失效", id))
			continue
		}
		collected++
	}

	// 孤儿文件回收作为第二通道：补漏"文件已落盘、元数据从未入库"的中间态，
	// 该路径不依赖 block_objects 行，崩溃残留也能被兜底清理。
	orphaned, err := s.collectOrphanFiles(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	return collected + orphaned, errors.Join(errs...)
}

// collectOrphanFiles 删除磁盘上存在、但 block_objects 与活跃 upload_reservations
// 中都没有对应行的物理孤儿块。这些文件是 PutNew 已原子安装、而后续
// CommitBlockUpload 失败（客户端断连、DB 错误、进程崩溃）时留下的；现有按
// block_objects 行的 GC 认领发现不了它们。
// 仅回收修改时间早于宽限期的文件：上传从 PutNew 到 Commit 通常不足 1 秒，
// 宽限期保证在途上传与刚失败的残留都得到保护，避免误删。
func (s *BlocksService) collectOrphanFiles(ctx context.Context) (int, error) {
	files, err := s.bs.ListFiles()
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}
	known, err := s.q.ListActiveBlockIDs(ctx, s.now().Unix())
	if err != nil {
		return 0, err
	}
	cutoff := s.now().Add(-BlockOrphanGracePeriod)
	collected := 0
	var errs []error
	for _, f := range files {
		if f.ModTime.After(cutoff) {
			continue // 宽限期内，可能是进行中的上传
		}
		if _, ok := known[f.ID]; ok {
			continue // 有 DB 行或活跃预留，不是孤儿
		}
		if err := s.bs.Delete(f.ID); err != nil {
			errs = append(errs, fmt.Errorf("删除孤儿块 %s：%w", f.ID, err))
			continue
		}
		collected++
	}
	return collected, errors.Join(errs...)
}
