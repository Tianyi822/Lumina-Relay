// Package service 实现 lumina-relay 的业务逻辑层。
//
// Service 层编排数据访问（internal/db）与文件存储（internal/store），
// 不感知 HTTP。Handler 层负责请求解析与响应映射。
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"lumina-relay/internal/db"
)

// RegisterInput 是账户注册的入参。
// 所有字段由客户端处理（hash、DEK 信封、公钥），服务端只做存储。
type RegisterInput struct {
	RecoveryCodeHash []byte // 客户端算好的恢复码哈希
	DekSalt          []byte // DEK 信封盐
	DekNonce         []byte // DEK 信封 nonce
	DekCt            []byte // DEK 密文
	DevicePubKey     string // 设备 Ed25519 公钥（hex）
	DeviceName       string // 设备名
}

// RegisterOutput 是注册成功后返回的标识。
// recoveryCode 不在此返回（计划"API 对齐决策"：客户端生成、不回传）。
type RegisterOutput struct {
	AccountID string
	DeviceID  string
}

// AccountService 封装账户相关业务逻辑。
type AccountService struct {
	q *db.Queries
}

// NewAccountService 构造 AccountService。
func NewAccountService(q *db.Queries) *AccountService {
	return &AccountService{q: q}
}

// Register 创建账户、首台设备、并初始化 manifest_head，在一个事务内完成。
// 任一步骤失败则整体回滚。返回生成的 accountId/deviceId。
func (s *AccountService) Register(ctx context.Context, in RegisterInput) (RegisterOutput, error) {
	accountID := uuid.NewString()
	deviceID := uuid.NewString()
	now := time.Now().Unix()
	out := RegisterOutput{AccountID: accountID, DeviceID: deviceID}

	err := s.q.WithTx(ctx, func(txq *db.Queries) error {
		if err := txq.CreateAccount(ctx, db.CreateAccountParams{
			AccountID:        accountID,
			RecoveryCodeHash: in.RecoveryCodeHash,
			DekSalt:          in.DekSalt,
			DekNonce:         in.DekNonce,
			DekCt:            in.DekCt,
			CreatedAt:        now,
		}); err != nil {
			return err
		}
		if err := txq.CreateDevice(ctx, db.CreateDeviceParams{
			DeviceID:     deviceID,
			AccountID:    accountID,
			DevicePubKey: in.DevicePubKey,
			DeviceName:   in.DeviceName,
			CreatedAt:    now,
		}); err != nil {
			return err
		}
		if err := txq.InsertManifestHead(ctx, accountID, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return RegisterOutput{}, fmt.Errorf("注册账户：%w", err)
	}
	return out, nil
}
