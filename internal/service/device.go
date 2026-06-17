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

// ErrDeviceNotFound 表示设备不存在或已吊销。handler 据此映射 404。
var ErrDeviceNotFound = errors.New("device not found")

// ErrDeviceForbidden 表示调用者无权操作该设备（设备不属于其账户）。
// 安全修复：防止跨账户吊销。handler 据此映射 403。
var ErrDeviceForbidden = errors.New("device does not belong to caller")

// RevokeDevice 吊销指定设备（置 revoked_at）。幂等。
// callerAccountID 为调用者账户（session 注入），必须与设备归属一致。
// 设备不存在或已吊销返回 ErrDeviceNotFound；跨账户操作返回 ErrDeviceForbidden。
// 见 sync-design §288-289。
func (s *DeviceService) RevokeDevice(ctx context.Context, callerAccountID, deviceID string) error {
	// 先查设备归属（防越权）
	dev, err := s.q.GetDevice(ctx, deviceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDeviceNotFound
		}
		return fmt.Errorf("查询设备：%w", err)
	}
	if dev.AccountID != callerAccountID {
		return ErrDeviceForbidden
	}

	n, err := s.q.RevokeDevice(ctx, deviceID, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("吊销设备：%w", err)
	}
	if n == 0 {
		// 设备存在但已吊销（幂等场景）
		return ErrDeviceNotFound
	}
	return nil
}
