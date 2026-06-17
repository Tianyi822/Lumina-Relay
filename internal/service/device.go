package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"lumina-relay/internal/db"
)

// ErrBadRecoveryCode 表示恢复码哈希不匹配。handler 据此映射 401 bad_recovery_code。
var ErrBadRecoveryCode = errors.New("bad recovery code")

// DeviceRegisterInput 是设备注册的入参。
// RecoveryCodeHash 由客户端算好上传（与注册时同一算法），服务端做字节比对。
type DeviceRegisterInput struct {
	AccountID        string
	RecoveryCodeHash []byte
	DevicePubKey     string
	DeviceName       string
}

// DeviceRegisterOutput 是设备注册成功后返回的标识。
type DeviceRegisterOutput struct {
	DeviceID string
}

// DeviceService 封装设备相关业务逻辑。
type DeviceService struct {
	q *db.Queries
}

// NewDeviceService 构造 DeviceService。
func NewDeviceService(q *db.Queries) *DeviceService {
	return &DeviceService{q: q}
}

// RegisterDevice 校验恢复码哈希后为新设备写入 devices 表。
// 恢复码不匹配返回 ErrBadRecoveryCode；账户不存在返回 ErrAccountNotFound。
// 成功返回新 deviceId（uuid）。
func (s *DeviceService) RegisterDevice(ctx context.Context, in DeviceRegisterInput) (DeviceRegisterOutput, error) {
	stored, err := s.q.GetAccountRecoveryHash(ctx, in.AccountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeviceRegisterOutput{}, ErrAccountNotFound
		}
		return DeviceRegisterOutput{}, fmt.Errorf("读取恢复码哈希：%w", err)
	}
	if !bytes.Equal(stored, in.RecoveryCodeHash) {
		return DeviceRegisterOutput{}, ErrBadRecoveryCode
	}

	deviceID := uuid.NewString()
	if err := s.q.CreateDevice(ctx, db.CreateDeviceParams{
		DeviceID:     deviceID,
		AccountID:    in.AccountID,
		DevicePubKey: in.DevicePubKey,
		DeviceName:   in.DeviceName,
		CreatedAt:    time.Now().Unix(),
	}); err != nil {
		return DeviceRegisterOutput{}, fmt.Errorf("创建设备：%w", err)
	}
	return DeviceRegisterOutput{DeviceID: deviceID}, nil
}
