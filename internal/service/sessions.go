// sessions.go 实现会话密文快照的不透明同步服务。
//
// 职责边界（见 session-storage-format §9 与 docs/sync-design）：
//   - 服务端不解析密文内容——快照结构、合并、去重全部是客户端职责；
//   - 服务端只提供整文件原子快照 PUT/GET/DELETE，密文与版本在单个
//     SQLite 事务内 CAS 提交，sessionId 校验防路径遍历；
//   - sessionId 在账号内唯一，被其他同步组占用返回 ErrSessionIDConflict。
package service

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"

	"lumina-relay/internal/db"
)

// MaxSessionFileBytes 是单个会话快照密文的大小上限，与 Manifest 对齐。
const MaxSessionFileBytes = 4 * 1024 * 1024

var (
	ErrSessionFileNotFound = errors.New("session file not found")
	ErrStaleSessionFile    = errors.New("stale session file")
	ErrInvalidSessionID    = errors.New("invalid session id")
	ErrSessionIDConflict   = errors.New("session id conflict")
)

// sessionIDPattern 是客户端 sessionId 规则（session-storage-format §6.1）
// 的服务端收紧版：正则天然排除 `/`、`\`、`..`，长度上限另行防御。
var sessionIDPattern = regexp.MustCompile(`^session-[0-9]{1,16}-[a-z0-9]{1,32}$`)

const maxSessionIDLen = 64

// SessionFileService 提供会话快照的 SQLite 原子存储：
// 密文、版本 CAS、设备复核与配额调整全部在 db 层单事务内完成，
// service 层只做输入校验与错误映射。
type SessionFileService struct {
	q   *db.Queries
	now func() time.Time
}

func NewSessionFileService(q *db.Queries) *SessionFileService {
	return &SessionFileService{q: q, now: time.Now}
}

func validateSessionID(sessionID string) error {
	if len(sessionID) > maxSessionIDLen || !sessionIDPattern.MatchString(sessionID) {
		return ErrInvalidSessionID
	}
	return nil
}

// mapSessionDBError 把 db 层哨兵错误映射到 service 层哨兵错误。
func mapSessionDBError(err error) error {
	switch {
	case errors.Is(err, db.ErrSessionIDConflict):
		return ErrSessionIDConflict
	case errors.Is(err, db.ErrQuotaExceeded):
		return ErrQuotaExceeded
	case errors.Is(err, db.ErrInactiveDevice):
		return ErrDeviceRevoked
	case errors.Is(err, db.ErrGroupChanged):
		return ErrGroupChanged
	default:
		return err
	}
}

// List 返回同步组内全部会话快照的元数据（不含密文）。
func (s *SessionFileService) List(
	ctx context.Context,
	accountID, groupID string,
) ([]db.SessionFileRow, error) {
	group, err := s.q.GetSyncGroup(ctx, groupID)
	if err != nil || group.AccountID != accountID {
		return nil, ErrGroupChanged
	}
	return s.q.ListSessionFiles(ctx, accountID, groupID)
}

type SessionFileContent struct {
	Version int64
	Data    []byte
}

// Get 读取完整会话快照密文与当前版本。
func (s *SessionFileService) Get(
	ctx context.Context,
	accountID, groupID, sessionID string,
) (SessionFileContent, error) {
	if err := validateSessionID(sessionID); err != nil {
		return SessionFileContent{}, err
	}
	row, err := s.q.GetSessionFile(ctx, accountID, groupID, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionFileContent{}, ErrSessionFileNotFound
	}
	if err != nil {
		return SessionFileContent{}, err
	}
	return SessionFileContent{Version: row.Version, Data: row.Ciphertext}, nil
}

type SessionFilePut struct {
	Version        int64
	CurrentVersion int64
	Size           int64
}

// Put 原子写入会话快照（baseVersion=0 表示创建）。
// CAS 失败返回 ErrStaleSessionFile，CurrentVersion 供客户端拉最新后按 LWW 决策。
func (s *SessionFileService) Put(
	ctx context.Context,
	accountID, groupID, deviceID, sessionID string,
	baseVersion int64,
	data []byte,
) (SessionFilePut, error) {
	if err := validateSessionID(sessionID); err != nil {
		return SessionFilePut{}, err
	}
	if baseVersion < 0 || len(data) == 0 || len(data) > MaxSessionFileBytes {
		return SessionFilePut{}, ErrInvalidInput
	}
	result, err := s.q.PutSessionFileCAS(
		ctx, accountID, groupID, deviceID, sessionID,
		baseVersion, data, s.now().Unix())
	if err != nil {
		return SessionFilePut{}, mapSessionDBError(err)
	}
	if result.Conflict {
		return SessionFilePut{CurrentVersion: result.CurrentVersion}, ErrStaleSessionFile
	}
	return SessionFilePut{
		Version:        result.Version,
		CurrentVersion: result.CurrentVersion,
		Size:           result.Size,
	}, nil
}

type SessionFileDelete struct {
	Deleted        bool
	CurrentVersion int64
}

// Delete 按 baseVersion CAS 删除会话快照并释放配额。
// 记录不存在视为幂等成功（Deleted=false）；版本不匹配返回 ErrStaleSessionFile。
func (s *SessionFileService) Delete(
	ctx context.Context,
	accountID, groupID, deviceID, sessionID string,
	baseVersion int64,
) (SessionFileDelete, error) {
	if err := validateSessionID(sessionID); err != nil {
		return SessionFileDelete{}, err
	}
	if baseVersion < 1 {
		return SessionFileDelete{}, ErrInvalidInput
	}
	result, err := s.q.DeleteSessionFileCAS(
		ctx, accountID, groupID, deviceID, sessionID, baseVersion)
	if err != nil {
		return SessionFileDelete{}, mapSessionDBError(err)
	}
	if result.Conflict {
		return SessionFileDelete{CurrentVersion: result.CurrentVersion}, ErrStaleSessionFile
	}
	return SessionFileDelete{Deleted: result.Deleted}, nil
}
