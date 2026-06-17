package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"lumina-relay/internal/db"
	"lumina-relay/internal/store"
)

// 块业务错误。
var (
	// ErrBlockHashMismatch 表示 sha256(body) != blockId（sync-design §665）。
	ErrBlockHashMismatch = errors.New("block hash mismatch")
	// ErrBlockNotFound 表示请求的块不存在。
	ErrBlockNotFound = errors.New("block not found")
	// ErrQuotaExceeded 表示超过账户存储配额（sync-design §700）。
	ErrQuotaExceeded = errors.New("quota exceeded")
)

// BlocksPutInput 是 PUT /blocks/{blockId} 的入参。
type BlocksPutInput struct {
	AccountID string
	BlockID   string // hex 编码的 sha256(data)
	Data      []byte  // 密文内容
}

// BlocksPutOutput 是 PUT 的结果。
type BlocksPutOutput struct {
	Created bool // true=本次新建，false=已存在（幂等）
}

// BlocksService 编排块文件存储 + 元数据 + 配额。
type BlocksService struct {
	q       *db.Queries
	bs      *store.BlockStore
	quotaMB int // 账户配额上限（MB）；生产默认 1024
}

// NewBlocksService 构造 BlocksService。quotaMB 为配额上限。
func NewBlocksService(q *db.Queries, bs *store.BlockStore, quotaMB int) *BlocksService {
	return &BlocksService{q: q, bs: bs, quotaMB: quotaMB}
}

// Put 上传一个密文块。流程：
// 1. 校验 sha256(data) == blockId（不符→ErrBlockHashMismatch）
// 2. 检查是否已存在（幂等：已存在返回 Created=false）
// 3. 检查配额（超限→ErrQuotaExceeded）
// 4. 写文件 + 插入元数据
func (s *BlocksService) Put(ctx context.Context, in BlocksPutInput) (BlocksPutOutput, error) {
	// 1. hash 自校验（sync-design §665-668）
	sum := sha256.Sum256(in.Data)
	if hex.EncodeToString(sum[:]) != in.BlockID {
		return BlocksPutOutput{}, ErrBlockHashMismatch
	}

	// 2. 幂等检查：先查元数据（GetBlockMeta 包装 sql.ErrNoRows，用 errors.Is 穿透）
	if _, err := s.q.GetBlockMeta(ctx, in.BlockID); err == nil {
		// 已存在：幂等返回（不重写文件）
		return BlocksPutOutput{Created: false}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		// 其他查询错误（非"不存在"），向上传播
		return BlocksPutOutput{}, fmt.Errorf("查询块元数据：%w", err)
	}

	// 3. 配额检查（sync-design §700）。quotaMB<=0 表示零配额（拒绝所有）。
	used, err := s.q.SumBlockSizeByAccount(ctx, in.AccountID)
	if err != nil {
		return BlocksPutOutput{}, fmt.Errorf("配额检查：%w", err)
	}
	quotaBytes := int64(s.quotaMB) * 1024 * 1024
	if used+int64(len(in.Data)) > quotaBytes {
		return BlocksPutOutput{}, ErrQuotaExceeded
	}

	// 4. 写文件（O_CREATE|O_EXCL 天然去重）+ 元数据
	created, err := s.bs.PutNew(in.BlockID, in.Data)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			created = false
		} else {
			return BlocksPutOutput{}, fmt.Errorf("写块文件：%w", err)
		}
	}
	if created {
		if _, err := s.q.UpsertBlockMeta(ctx, in.BlockID, in.AccountID, int64(len(in.Data)), time.Now().Unix()); err != nil {
			return BlocksPutOutput{}, fmt.Errorf("写块元数据：%w", err)
		}
	}
	return BlocksPutOutput{Created: created}, nil
}

// Have 批量查重，返回缺失的 blockId 列表（sync-design §653-657）。
func (s *BlocksService) Have(ctx context.Context, accountID string, ids []string) ([]string, error) {
	var missing []string
	for _, id := range ids {
		if _, err := s.q.GetBlockMeta(ctx, id); err != nil {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// Get 下载一个密文块。块不存在返 ErrBlockNotFound。
func (s *BlocksService) Get(ctx context.Context, accountID, blockID string) ([]byte, error) {
	// 先查元数据确认存在（且属于该账户）
	meta, err := s.q.GetBlockMeta(ctx, blockID)
	if err != nil {
		return nil, ErrBlockNotFound
	}
	if meta.AccountID != accountID {
		return nil, ErrBlockNotFound
	}
	data, err := s.bs.Get(blockID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrBlockNotFound
		}
		return nil, fmt.Errorf("读块文件：%w", err)
	}
	return data, nil
}
