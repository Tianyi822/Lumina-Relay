package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"lumina-relay/internal/auth"
	"lumina-relay/internal/db"
)

var (
	ErrInvalidInput          = errors.New("invalid input")
	ErrInvalidCredentials    = errors.New("invalid credentials")
	ErrAccountBecameExisting = errors.New("account became existing")
	ErrDeviceRevoked         = errors.New("device revoked")
)

const (
	AuthSaltBytes      = 16
	PublicKeyBytes     = ed25519.PublicKeySize
	DEKEnvelopeBytes   = 24 + 32 + 16
	MaxEnvelopeBytes   = DEKEnvelopeBytes
	MaxDeviceNameBytes = 128
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9._-]{3,64}$`)

func NormalizeUsername(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !usernamePattern.MatchString(value) {
		return "", ErrInvalidInput
	}
	return value, nil
}

type ConnectionService struct {
	q          *db.Queries
	challenges *auth.ChallengeStore
	instanceID string
	jwtSecret  []byte
	quotaBytes int64
	now        func() time.Time
}

func NewConnectionService(
	q *db.Queries,
	challenges *auth.ChallengeStore,
	instanceID string,
	jwtSecret []byte,
	quotaMB int,
) *ConnectionService {
	return &ConnectionService{
		q: q, challenges: challenges, instanceID: instanceID,
		jwtSecret: jwtSecret, quotaBytes: int64(quotaMB) * 1024 * 1024,
		now: time.Now,
	}
}

type ConnectionStart struct {
	AccountExists bool
	AttemptID     string
	Challenge     []byte
	AuthSalt      []byte
	ExpiresAt     int64
}

func (s *ConnectionService) Start(ctx context.Context, username string) (ConnectionStart, error) {
	normalized, err := NormalizeUsername(username)
	if err != nil {
		return ConnectionStart{}, err
	}
	account, err := s.q.GetAccountByUsername(ctx, normalized)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ConnectionStart{}, fmt.Errorf("查询用户名：%w", err)
	}
	var accountID string
	var salt []byte
	if exists {
		accountID = account.AccountID
		salt = append([]byte(nil), account.AuthSalt...)
	} else {
		salt = make([]byte, AuthSaltBytes)
		if _, err := rand.Read(salt); err != nil {
			return ConnectionStart{}, fmt.Errorf("生成认证 salt：%w", err)
		}
	}
	attempt, err := s.challenges.Create(auth.Attempt{
		Kind: auth.AttemptConnection, Username: normalized,
		AccountExists: exists, AccountID: accountID, AuthSalt: salt,
	})
	if err != nil {
		return ConnectionStart{}, err
	}
	return ConnectionStart{
		AccountExists: exists, AttemptID: attempt.ID,
		Challenge: attempt.Challenge, AuthSalt: salt,
		ExpiresAt: attempt.ExpiresAt.Unix(),
	}, nil
}

type CompleteConnectionInput struct {
	AttemptID            string
	AccountID            string
	DeviceID             string
	DeviceName           string
	DevicePublicKey      []byte
	LoginPublicKey       []byte
	AccountAuthPublicKey []byte
	DEKEnvelope          []byte
	LoginProof           string
	AccountProof         string
	DeviceProof          string
}

type Session struct {
	Token        string
	ExpiresAt    int64
	ProofBinding string
}

type Bootstrap struct {
	AccountID            string
	Username             string
	DeviceID             string
	DEKEnvelope          []byte
	AccountAuthPublicKey []byte
	CryptoStateRevision  int64
	DEKEpoch             int64
	SyncGroupID          string
	GroupRevision        int64
	HasOtherSyncData     bool
	ServerTimeMS         int64
}

type ConnectionResult struct {
	AccountExists bool
	Session       Session
	Bootstrap     Bootstrap
}

func (s *ConnectionService) Complete(
	ctx context.Context,
	in CompleteConnectionInput,
) (ConnectionResult, error) {
	attempt, err := s.challenges.Take(in.AttemptID, auth.AttemptConnection)
	if err != nil {
		return ConnectionResult{}, ErrInvalidCredentials
	}
	if err := validateNewDevice(in.DeviceID, in.DeviceName, in.DevicePublicKey); err != nil {
		return ConnectionResult{}, err
	}
	if attempt.AccountExists {
		return s.completeLogin(ctx, attempt, in)
	}
	return s.completeRegistration(ctx, attempt, in)
}

func (s *ConnectionService) completeRegistration(
	ctx context.Context,
	attempt auth.Attempt,
	in CompleteConnectionInput,
) (ConnectionResult, error) {
	if !isCanonicalUUID(in.AccountID) ||
		len(in.LoginPublicKey) != PublicKeyBytes ||
		len(in.AccountAuthPublicKey) != PublicKeyBytes ||
		len(in.DEKEnvelope) != DEKEnvelopeBytes {
		return ConnectionResult{}, ErrInvalidInput
	}
	envelopeHash := sha256.Sum256(in.DEKEnvelope)
	transcript, err := auth.BuildAccountCreateTranscript(
		s.instanceID, attempt.ID, attempt.Username, in.AccountID,
		attempt.Challenge, attempt.AuthSalt,
		in.LoginPublicKey, in.AccountAuthPublicKey,
		envelopeHash[:], in.DeviceID, in.DeviceName, in.DevicePublicKey,
	)
	if err != nil ||
		!auth.VerifySignature(in.LoginPublicKey, transcript, in.LoginProof) ||
		!auth.VerifySignature(in.AccountAuthPublicKey, transcript, in.AccountProof) ||
		!auth.VerifySignature(in.DevicePublicKey, transcript, in.DeviceProof) {
		return ConnectionResult{}, ErrInvalidCredentials
	}
	now := s.now()
	groupID := uuid.NewString()
	err = s.q.WithTx(ctx, func(txq *db.Queries) error {
		if err := txq.InsertAccount(ctx, db.CreateAccountParams{
			AccountID: in.AccountID, Username: attempt.Username,
			AuthSalt: attempt.AuthSalt, LoginPublicKey: in.LoginPublicKey,
			DEKEnvelope:          in.DEKEnvelope,
			AccountAuthPublicKey: in.AccountAuthPublicKey,
			QuotaBytes:           s.quotaBytes, CreatedAt: now.Unix(),
		}); err != nil {
			return err
		}
		if err := txq.InsertSyncGroup(ctx, groupID, in.AccountID, now.Unix()); err != nil {
			return err
		}
		if err := txq.InsertDevice(ctx, db.CreateDeviceParams{
			DeviceID: in.DeviceID, AccountID: in.AccountID, SyncGroupID: groupID,
			SigningPublicKey: in.DevicePublicKey, DeviceName: in.DeviceName,
			CreatedAt: now.Unix(),
		}); err != nil {
			return err
		}
		return txq.InsertManifestHead(ctx, in.DeviceID, now.Unix())
	})
	if err != nil {
		if isUniqueUsernameError(err) {
			return ConnectionResult{}, ErrAccountBecameExisting
		}
		return ConnectionResult{}, fmt.Errorf("注册账户：%w", err)
	}
	return s.connectionResult(ctx, in.AccountID, in.DeviceID, false, now)
}

func (s *ConnectionService) completeLogin(
	ctx context.Context,
	attempt auth.Attempt,
	in CompleteConnectionInput,
) (ConnectionResult, error) {
	if in.AccountID != "" || len(in.LoginPublicKey) != 0 ||
		len(in.AccountAuthPublicKey) != 0 || len(in.DEKEnvelope) != 0 ||
		in.AccountProof != "" {
		return ConnectionResult{}, ErrInvalidInput
	}
	account, err := s.q.GetAccount(ctx, attempt.AccountID)
	if err != nil || account.Username != attempt.Username {
		return ConnectionResult{}, ErrInvalidCredentials
	}
	transcript, err := auth.BuildLoginTranscript(
		s.instanceID, attempt.ID, attempt.Username, attempt.Challenge,
		in.DeviceID, in.DeviceName, in.DevicePublicKey,
	)
	if err != nil ||
		!auth.VerifySignature(account.LoginPublicKey, transcript, in.LoginProof) ||
		!auth.VerifySignature(in.DevicePublicKey, transcript, in.DeviceProof) {
		return ConnectionResult{}, ErrInvalidCredentials
	}
	now := s.now()
	if err := s.q.CreateDeviceEnrollment(ctx, db.CreateDeviceParams{
		DeviceID: in.DeviceID, AccountID: account.AccountID,
		SyncGroupID: uuid.NewString(), SigningPublicKey: in.DevicePublicKey,
		DeviceName: in.DeviceName, CreatedAt: now.Unix(),
	}); err != nil {
		return ConnectionResult{}, fmt.Errorf("登录设备入组：%w", err)
	}
	return s.connectionResult(ctx, account.AccountID, in.DeviceID, true, now)
}

type SessionChallenge struct {
	AttemptID string
	Challenge []byte
	ExpiresAt int64
}

func (s *ConnectionService) StartSession(ctx context.Context, deviceID string) (SessionChallenge, error) {
	device, err := s.q.GetDevice(ctx, deviceID)
	if err != nil || device.Status != "active" || !device.SyncGroupID.Valid {
		return SessionChallenge{}, ErrInvalidCredentials
	}
	attempt, err := s.challenges.Create(auth.Attempt{
		Kind: auth.AttemptSession, DeviceID: deviceID, AccountID: device.AccountID,
	})
	if err != nil {
		return SessionChallenge{}, err
	}
	return SessionChallenge{
		AttemptID: attempt.ID, Challenge: attempt.Challenge,
		ExpiresAt: attempt.ExpiresAt.Unix(),
	}, nil
}

func (s *ConnectionService) CompleteSession(
	ctx context.Context,
	attemptID, signature string,
) (ConnectionResult, error) {
	attempt, err := s.challenges.Take(attemptID, auth.AttemptSession)
	if err != nil {
		return ConnectionResult{}, ErrInvalidCredentials
	}
	device, err := s.q.GetDevice(ctx, attempt.DeviceID)
	if err != nil || device.Status != "active" || !device.SyncGroupID.Valid {
		return ConnectionResult{}, ErrInvalidCredentials
	}
	transcript, err := auth.BuildSessionTranscript(
		s.instanceID, attempt.ID, attempt.Challenge, device.DeviceID)
	if err != nil || !auth.VerifySignature(device.SigningPublicKey, transcript, signature) {
		return ConnectionResult{}, ErrInvalidCredentials
	}
	return s.connectionResult(ctx, device.AccountID, device.DeviceID, true, s.now())
}

func (s *ConnectionService) Bootstrap(
	ctx context.Context,
	accountID, deviceID string,
) (Bootstrap, error) {
	device, err := s.q.GetDevice(ctx, deviceID)
	if err != nil || device.AccountID != accountID || device.Status != "active" ||
		!device.SyncGroupID.Valid {
		return Bootstrap{}, ErrInvalidCredentials
	}
	return s.bootstrap(ctx, device, s.now())
}

func (s *ConnectionService) connectionResult(
	ctx context.Context,
	accountID, deviceID string,
	accountExists bool,
	now time.Time,
) (ConnectionResult, error) {
	device, err := s.q.GetDevice(ctx, deviceID)
	if err != nil {
		return ConnectionResult{}, err
	}
	bootstrap, err := s.bootstrap(ctx, device, now)
	if err != nil {
		return ConnectionResult{}, err
	}
	token, err := auth.IssueSessionToken(
		s.jwtSecret, s.instanceID, accountID, deviceID, now)
	if err != nil {
		return ConnectionResult{}, err
	}
	return ConnectionResult{
		AccountExists: accountExists,
		Session: Session{
			Token: token.Token, ExpiresAt: token.ExpiresAt,
			ProofBinding: token.TokenID,
		},
		Bootstrap: bootstrap,
	}, nil
}

func (s *ConnectionService) bootstrap(
	ctx context.Context,
	device db.DeviceRow,
	now time.Time,
) (Bootstrap, error) {
	account, err := s.q.GetAccount(ctx, device.AccountID)
	if err != nil {
		return Bootstrap{}, err
	}
	group, err := s.q.GetSyncGroup(ctx, device.SyncGroupID.String)
	if err != nil {
		return Bootstrap{}, err
	}
	hasOther, err := s.q.HasOtherSyncData(ctx, account.AccountID, group.GroupID)
	if err != nil {
		return Bootstrap{}, err
	}
	return Bootstrap{
		AccountID: account.AccountID, Username: account.Username,
		DeviceID: device.DeviceID, DEKEnvelope: account.DEKEnvelope,
		AccountAuthPublicKey: account.AccountAuthPublicKey,
		CryptoStateRevision:  account.CryptoStateRevision,
		DEKEpoch:             account.DEKEpoch,
		SyncGroupID:          group.GroupID, GroupRevision: group.Revision,
		HasOtherSyncData: hasOther, ServerTimeMS: now.UnixMilli(),
	}, nil
}

func validateNewDevice(deviceID, deviceName string, publicKey []byte) error {
	if !isCanonicalUUID(deviceID) ||
		len(publicKey) != PublicKeyBytes ||
		deviceName == "" || len([]byte(deviceName)) > MaxDeviceNameBytes {
		return ErrInvalidInput
	}
	return nil
}

func isCanonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func isUniqueUsernameError(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique") && strings.Contains(text, "accounts.username")
}
