package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"lumina-relay/internal/db"
)

const MaxManifestBytes = 4 * 1024 * 1024

var (
	ErrManifestNotFound = errors.New("manifest not found")
	ErrStaleManifest    = errors.New("stale manifest")
)

type ManifestService struct {
	q   *db.Queries
	now func() time.Time
}

func NewManifestService(q *db.Queries) *ManifestService {
	return &ManifestService{q: q, now: time.Now}
}

type ManifestHeads struct {
	GroupRevision int64
	Heads         []db.ManifestHeadRow
}

func (s *ManifestService) ListHeads(
	ctx context.Context,
	accountID, groupID string,
) (ManifestHeads, error) {
	group, err := s.q.GetSyncGroup(ctx, groupID)
	if err != nil || group.AccountID != accountID {
		return ManifestHeads{}, ErrGroupChanged
	}
	heads, err := s.q.ListManifestHeads(ctx, accountID, groupID)
	if err != nil {
		return ManifestHeads{}, err
	}
	return ManifestHeads{GroupRevision: group.Revision, Heads: heads}, nil
}

func (s *ManifestService) Get(
	ctx context.Context,
	accountID, groupID, deviceID string,
	version int64,
) (db.ManifestRow, error) {
	row, err := s.q.GetVisibleManifest(ctx, accountID, groupID, deviceID, version)
	if errors.Is(err, sql.ErrNoRows) {
		return db.ManifestRow{}, ErrManifestNotFound
	}
	if err != nil {
		return db.ManifestRow{}, fmt.Errorf("读取 Manifest：%w", err)
	}
	return row, nil
}

func (s *ManifestService) Put(
	ctx context.Context,
	deviceID string,
	baseVersion int64,
	ciphertext []byte,
) (db.ManifestPutResult, error) {
	if baseVersion < 0 || len(ciphertext) == 0 || len(ciphertext) > MaxManifestBytes {
		return db.ManifestPutResult{}, ErrInvalidInput
	}
	result, err := s.q.PutDeviceManifest(
		ctx, deviceID, baseVersion, ciphertext, s.now().Unix())
	if errors.Is(err, db.ErrInactiveDevice) {
		return db.ManifestPutResult{}, ErrDeviceRevoked
	}
	if err != nil {
		return db.ManifestPutResult{}, err
	}
	if result.Conflict {
		return result, ErrStaleManifest
	}
	return result, nil
}
