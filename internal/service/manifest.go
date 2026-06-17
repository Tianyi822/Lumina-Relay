package service

import (
	"context"
	"fmt"
	"time"

	"lumina-relay/internal/db"
)

// ManifestPutInput 是 PUT /manifest 的入参。
type ManifestPutInput struct {
	AccountID  string
	Ciphertext []byte // 客户端加密后的 manifest 密文
	BaseVersion int64 // 客户端声明基于的版本（乐观并发）
	DeviceID   string // 提交设备
}

// ManifestPutOutput 是 PUT /manifest 的结果。
// Conflict=true 表示 baseVersion 落后于 head（409 stale_base），此时 CurrentVersion 为服务端实际版本。
// Conflict=false 表示提交成功，NewVersion 为新版本号。
type ManifestPutOutput struct {
	Conflict       bool
	NewVersion     int64 // 成功时为新版本
	CurrentVersion int64 // 冲突时为服务端当前版本
}

// ManifestGetOutput 是 GET /manifest 的结果。
type ManifestGetOutput struct {
	Version    int64
	Ciphertext []byte // version=0 时为 nil
}

// ManifestService 封装 manifest 读写与乐观并发。
type ManifestService struct {
	q *db.Queries
}

// NewManifestService 构造 ManifestService。
func NewManifestService(q *db.Queries) *ManifestService {
	return &ManifestService{q: q}
}

// Put 在单事务内执行 manifest 乐观并发提交（见 data-layer spec §3.6）：
// 读 head → 比较 baseVersion → 插入新版本 → CAS 更新 head。
// baseVersion != head.current_version 时返回 Conflict（不返 error），便于 handler 映射 409。
func (s *ManifestService) Put(ctx context.Context, in ManifestPutInput) (ManifestPutOutput, error) {
	var out ManifestPutOutput
	now := time.Now().Unix()

	err := s.q.WithTx(ctx, func(txq *db.Queries) error {
		head, err := txq.GetManifestHead(ctx, in.AccountID)
		if err != nil {
			return fmt.Errorf("读取 manifest_head：%w", err)
		}

		// 乐观并发检查：baseVersion 必须等于当前 head
		if in.BaseVersion != head.CurrentVersion {
			out.Conflict = true
			out.CurrentVersion = head.CurrentVersion
			return nil // 冲突用字段表达，非 error
		}

		newVersion := head.CurrentVersion + 1

		// 插入 manifest 历史行
		if err := txq.InsertManifest(ctx, db.InsertManifestParams{
			AccountID:  in.AccountID,
			Version:    newVersion,
			Ciphertext: in.Ciphertext,
			DeviceID:   in.DeviceID,
			ReceivedAt: now,
		}); err != nil {
			return err
		}

		// CAS 更新 head（双保险：即使并发，WHERE current_version=? 保证只推进一次）
		n, err := txq.UpdateManifestHeadIfExpected(ctx, in.AccountID, head.CurrentVersion, newVersion, now)
		if err != nil {
			return err
		}
		if n == 0 {
			// 极端竞态：事务内 head 被另一事务改了（SQLite 单写不应发生，但 CAS 是防御）
			out.Conflict = true
			out.CurrentVersion = newVersion
			return nil
		}

		out.NewVersion = newVersion
		return nil
	})
	if err != nil {
		return ManifestPutOutput{}, err
	}
	return out, nil
}

// Get 读取账户当前版本的 manifest。
// version=0（账户刚注册、无任何 manifest）时返回空 ciphertext + version 0。
func (s *ManifestService) Get(ctx context.Context, accountID string) (ManifestGetOutput, error) {
	head, err := s.q.GetManifestHead(ctx, accountID)
	if err != nil {
		return ManifestGetOutput{}, fmt.Errorf("读取 manifest_head：%w", err)
	}
	if head.CurrentVersion == 0 {
		return ManifestGetOutput{Version: 0}, nil
	}

	row, err := s.q.GetManifestByAccount(ctx, accountID, head.CurrentVersion)
	if err != nil {
		return ManifestGetOutput{}, fmt.Errorf("读取当前 manifest：%w", err)
	}
	return ManifestGetOutput{Version: row.Version, Ciphertext: row.Ciphertext}, nil
}
