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

// ErrAccountLocked 表示账户恢复码已被锁定（连续失败超阈值）。handler 据此映射 429。
var ErrAccountLocked = errors.New("account recovery locked")

// 恢复码爆破防护阈值（C3）。
const (
	// recoveryFailThreshold 触发锁定的连续失败次数。
	recoveryFailThreshold = 5
	// recoveryLockDuration 锁定时长。
	recoveryLockDuration = 15 * time.Minute
)

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
// 恢复码不匹配返回 ErrBadRecoveryCode；账户不存在返回 ErrAccountNotFound；
// 账户被锁定（连续失败超阈值）返回 ErrAccountLocked。
// 成功返回新 deviceId（uuid）。
//
// 爆破防护（C3）：恢复码校验失败累加计数，达到 recoveryFailThreshold 后
// 锁定 recoveryLockDuration，期间即使恢复码正确也拒绝（返回 ErrAccountLocked）。
// 成功后计数清零。
func (s *DeviceService) RegisterDevice(ctx context.Context, in DeviceRegisterInput) (DeviceRegisterOutput, error) {
	stored, err := s.q.GetAccountRecoveryHash(ctx, in.AccountID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 账户不存在：返回与"恢复码错误"相同的错误，避免账户存在性枚举（I2）。
			return DeviceRegisterOutput{}, ErrBadRecoveryCode
		}
		return DeviceRegisterOutput{}, fmt.Errorf("读取恢复码哈希：%w", err)
	}

	// 锁定检查：若仍在锁定期内，直接拒绝（不泄露"恢复码是否正确"）
	lock, err := s.q.GetRecoveryLock(ctx, in.AccountID)
	if err != nil {
		return DeviceRegisterOutput{}, fmt.Errorf("读取恢复码锁定状态：%w", err)
	}
	now := time.Now().Unix()
	if lock.RecoveryLockedUntil > now {
		return DeviceRegisterOutput{}, ErrAccountLocked
	}
	// 锁已过期：重置失败计数，避免"过期后一次失误立即重锁"。
	// （ResetRecoveryLock 同时清零 count 与 locked_until）
	if lock.RecoveryLockedUntil != 0 {
		if err := s.q.ResetRecoveryLock(ctx, in.AccountID); err != nil {
			return DeviceRegisterOutput{}, fmt.Errorf("重置过期锁定：%w", err)
		}
	}

	if !bytes.Equal(stored, in.RecoveryCodeHash) {
		// 失败：累加计数，达阈值则锁定
		if err := s.q.IncRecoveryFail(ctx, in.AccountID); err != nil {
			return DeviceRegisterOutput{}, fmt.Errorf("累加失败计数：%w", err)
		}
		// 读回最新计数判断是否触发锁定（避免并发竞争的 off-by-one）
		lockAfter, err := s.q.GetRecoveryLock(ctx, in.AccountID)
		if err != nil {
			return DeviceRegisterOutput{}, fmt.Errorf("读取失败计数：%w", err)
		}
		if lockAfter.RecoveryFailCount >= recoveryFailThreshold {
			if err := s.q.LockRecovery(ctx, in.AccountID, now+int64(recoveryLockDuration.Seconds())); err != nil {
				return DeviceRegisterOutput{}, fmt.Errorf("锁定恢复码：%w", err)
			}
		}
		return DeviceRegisterOutput{}, ErrBadRecoveryCode
	}

	// 成功：重置计数与锁定
	if err := s.q.ResetRecoveryLock(ctx, in.AccountID); err != nil {
		return DeviceRegisterOutput{}, fmt.Errorf("重置恢复码锁定：%w", err)
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

// DeviceInfo 是设备列表项（不含已吊销设备），GET /devices 返回。
type DeviceInfo struct {
	DeviceID     string
	DeviceName   string
	DevicePubKey string
	CreatedAt    int64
	LastSeenAt   int64
}

// ListDevices 列出账户下所有未吊销设备，按创建时间升序。
func (s *DeviceService) ListDevices(ctx context.Context, accountID string) ([]DeviceInfo, error) {
	rows, err := s.q.ListDevicesByAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("列出设备：%w", err)
	}
	out := make([]DeviceInfo, len(rows))
	for i, r := range rows {
		out[i] = DeviceInfo{
			DeviceID:     r.DeviceID,
			DeviceName:   r.DeviceName,
			DevicePubKey: r.DevicePubKey,
			CreatedAt:    r.CreatedAt,
			LastSeenAt:   r.LastSeenAt,
		}
	}
	return out, nil
}
