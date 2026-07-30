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
	if len(ids) > 1000 {
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
// deleting，使新上传在文件删除窗口内返回可重试冲突。
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
	return collected, errors.Join(errs...)
}
