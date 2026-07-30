package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"lumina-relay/internal/auth"
	"lumina-relay/internal/db"
)

var (
	ErrInvalidSyncCode = errors.New("invalid sync code")
	ErrAlreadyJoined   = errors.New("already joined")
	ErrGroupChanged    = errors.New("group changed")
)

const syncCodeTTL = 5 * time.Minute

var syncCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

type SyncService struct {
	q          *db.Queries
	instanceID string
	codeSecret []byte
	now        func() time.Time
}

func NewSyncService(
	q *db.Queries,
	instanceID string,
	codeSecret []byte,
) *SyncService {
	return &SyncService{
		q: q, instanceID: instanceID,
		codeSecret: append([]byte(nil), codeSecret...), now: time.Now,
	}
}

type GeneratedSyncCode struct {
	Code      string
	ExpiresAt int64
}

func (s *SyncService) GenerateCode(
	ctx context.Context,
	accountID, deviceID, groupID string,
) (GeneratedSyncCode, error) {
	device, err := s.q.GetDevice(ctx, deviceID)
	if err != nil || device.AccountID != accountID || device.Status != "active" ||
		!device.SyncGroupID.Valid || device.SyncGroupID.String != groupID {
		return GeneratedSyncCode{}, ErrInvalidCredentials
	}
	now := s.now()
	for range 10 {
		value, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
		if err != nil {
			return GeneratedSyncCode{}, fmt.Errorf("生成同步码：%w", err)
		}
		code := fmt.Sprintf("%06d", value.Int64())
		err = s.q.ReplaceSyncCode(ctx, db.SyncCodeRow{
			CodeID: uuid.NewString(), AccountID: accountID, SyncGroupID: groupID,
			InviterDeviceID: deviceID, CodeMAC: s.codeMAC(accountID, code),
			ExpiresAt: now.Add(syncCodeTTL).Unix(), CreatedAt: now.Unix(),
		}, now.Unix())
		if err == nil {
			return GeneratedSyncCode{Code: code, ExpiresAt: now.Add(syncCodeTTL).Unix()}, nil
		}
		if errors.Is(err, db.ErrInactiveDevice) {
			return GeneratedSyncCode{}, ErrDeviceRevoked
		}
		if errors.Is(err, db.ErrGroupChanged) {
			return GeneratedSyncCode{}, ErrGroupChanged
		}
		if !strings.Contains(strings.ToLower(err.Error()), "unique") {
			return GeneratedSyncCode{}, err
		}
	}
	return GeneratedSyncCode{}, fmt.Errorf("生成不冲突同步码失败")
}

func (s *SyncService) RedeemCode(
	ctx context.Context,
	accountID, deviceID, code string,
) (db.MergeGroupsResult, error) {
	if !syncCodePattern.MatchString(code) {
		return db.MergeGroupsResult{}, ErrInvalidSyncCode
	}
	result, err := s.q.RedeemSyncCode(
		ctx, accountID, deviceID, s.codeMAC(accountID, code), s.now().Unix())
	if errors.Is(err, db.ErrInvalidSyncCode) {
		return db.MergeGroupsResult{}, ErrInvalidSyncCode
	}
	if err != nil {
		return db.MergeGroupsResult{}, err
	}
	if result.AlreadyJoined {
		return result, ErrAlreadyJoined
	}
	return result, nil
}

type DiscardResult struct {
	RevokedDeviceIDs  []string
	DiscardedGroupIDs []string
	ReclaimedBytes    int64
}

func (s *SyncService) DiscardOtherGroups(
	ctx context.Context,
	accountID, deviceID, groupID string,
	groupRevision int64,
	accountProof string,
) (DiscardResult, error) {
	account, err := s.q.GetAccount(ctx, accountID)
	if err != nil {
		return DiscardResult{}, ErrInvalidCredentials
	}
	group, err := s.q.GetSyncGroup(ctx, groupID)
	if err != nil || group.AccountID != accountID || group.Revision != groupRevision {
		return DiscardResult{}, ErrGroupChanged
	}
	transcript, err := auth.BuildDiscardGroupsTranscript(
		s.instanceID, accountID, deviceID, groupID, groupRevision)
	if err != nil ||
		!auth.VerifySignature(account.AccountAuthPublicKey, transcript, accountProof) {
		return DiscardResult{}, ErrInvalidCredentials
	}
	result, err := s.q.DiscardOtherGroups(
		ctx, accountID, deviceID, groupID, groupRevision, s.now().Unix())
	if errors.Is(err, db.ErrGroupChanged) {
		return DiscardResult{}, ErrGroupChanged
	}
	if err != nil {
		return DiscardResult{}, err
	}
	return DiscardResult{
		RevokedDeviceIDs:  result.RevokedDeviceIDs,
		DiscardedGroupIDs: result.DiscardedGroupIDs,
		ReclaimedBytes:    result.ReclaimedBytes,
	}, nil
}

func (s *SyncService) ListDevices(
	ctx context.Context,
	accountID, groupID string,
) ([]db.DeviceListRow, error) {
	return s.q.ListDevicesInGroup(ctx, accountID, groupID)
}

func (s *SyncService) RevokeDevice(
	ctx context.Context,
	accountID, callerDeviceID, groupID, deviceID string,
) (bool, error) {
	revoked, err := s.q.RevokeDevice(
		ctx, accountID, callerDeviceID, groupID, deviceID, s.now().Unix())
	if errors.Is(err, db.ErrInactiveDevice) {
		return false, ErrDeviceRevoked
	}
	if errors.Is(err, db.ErrGroupChanged) {
		return false, ErrGroupChanged
	}
	return revoked, err
}

func (s *SyncService) codeMAC(accountID, code string) []byte {
	mac := hmac.New(sha256.New, s.codeSecret)
	_, _ = mac.Write([]byte("lumina-sync-code\x00"))
	_, _ = mac.Write([]byte(accountID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(code))
	return mac.Sum(nil)
}
