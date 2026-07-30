// sessions.go 实现会话 JSONL 文件的不透明同步服务。
//
// 职责边界（见 session-storage-format §9 与 docs/sync-design）：
//   - 服务端不解析 JSONL 行内容——meta 首行结构、meta 行数达 20 的压实、
//     消息 ID 去重全部是客户端职责；
//   - 服务端只提供两种文件效果：全量原子重写（rewrite）与字节追加（append），
//     加上 CAS 版本防并发覆盖、sessionId 校验防路径遍历；
//   - index.json 是客户端可重建的派生缓存，原样透传（仅 rewrite）。
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"lumina-relay/internal/db"
	"lumina-relay/internal/store"
)

// MaxSessionFileBytes 是单个会话文件（与 index.json）的大小上限，与 Manifest 对齐。
const MaxSessionFileBytes = 4 * 1024 * 1024

// sessionIndexName 是 index.json 在组目录下的固定文件名。
const sessionIndexName = "index.json"

var (
	ErrSessionFileNotFound = errors.New("session file not found")
	ErrStaleSessionFile    = errors.New("stale session file")
	ErrInvalidSessionID    = errors.New("invalid session id")
)

// sessionIDPattern 是客户端 sessionId 规则（session-storage-format §6.1）
// 的服务端收紧版：正则天然排除 `/`、`\`、`..`，长度上限另行防御。
var sessionIDPattern = regexp.MustCompile(`^session-[0-9]{1,16}-[a-z0-9]{1,32}$`)

const maxSessionIDLen = 64

// SessionFileService 组合注册表（SQLite，版本权威）与文件存储（不透明字节）。
//
// 写路径顺序固定为「先动文件、后提交注册表 CAS」，并用进程内 per-file 锁
// 串行化同一文件的并发写（单实例 Relay，与 EventHub 同假设）：
// 崩溃窗口只会产生文件比注册表长/新的状态，由 Append 截断与读路径按
// size 截断收敛。
type SessionFileService struct {
	q     *db.Queries
	files *store.SessionStore
	now   func() time.Time

	// locks 按 account/group/name 串行化写操作；条目不回收，
	// 单条仅几十字节，规模与活跃会话数同量级。
	locks sync.Map // map[string]*sync.Mutex
}

func NewSessionFileService(q *db.Queries, files *store.SessionStore) *SessionFileService {
	return &SessionFileService{q: q, files: files, now: time.Now}
}

func (s *SessionFileService) lock(accountID, groupID, name string) func() {
	key := accountID + "/" + groupID + "/" + name
	value, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
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
	case errors.Is(err, db.ErrInactiveDevice):
		return ErrDeviceRevoked
	case errors.Is(err, db.ErrGroupChanged):
		return ErrGroupChanged
	default:
		return err
	}
}

// List 返回同步组内全部会话文件的注册表投影。
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

// Get 读取完整会话文件。按注册表 size 截断，屏蔽崩溃残留的尾部字节。
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
	data, err := s.files.Read(accountID, groupID, sessionID+".jsonl")
	if errors.Is(err, store.ErrNotFound) {
		return SessionFileContent{}, fmt.Errorf("会话文件注册表与磁盘失联（%s）", sessionID)
	}
	if err != nil {
		return SessionFileContent{}, err
	}
	if int64(len(data)) < row.Size {
		return SessionFileContent{}, fmt.Errorf("会话文件短于注册表记录（%s）", sessionID)
	}
	return SessionFileContent{Version: row.Version, Data: data[:row.Size]}, nil
}

type SessionFilePut struct {
	Version        int64
	CurrentVersion int64
	Size           int64
}

// Rewrite 全量原子重写会话文件（baseVersion=0 表示创建）。
// CAS 失败返回 ErrStaleSessionFile，CurrentVersion 供客户端拉最新后按 LWW 决策。
func (s *SessionFileService) Rewrite(
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
	unlock := s.lock(accountID, groupID, sessionID)
	defer unlock()

	// 锁内预检版本：CAS 必然失败时不动文件，避免冲突方污染磁盘内容。
	var current int64
	row, err := s.q.GetSessionFile(ctx, accountID, groupID, sessionID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		current = 0
	case err != nil:
		return SessionFilePut{}, err
	default:
		current = row.Version
	}
	if current != baseVersion {
		return SessionFilePut{CurrentVersion: current}, ErrStaleSessionFile
	}

	if err := s.files.Rewrite(accountID, groupID, sessionID+".jsonl", data); err != nil {
		return SessionFilePut{}, err
	}
	result, err := s.q.UpsertSessionFileCAS(
		ctx, accountID, groupID, deviceID, sessionID,
		baseVersion, int64(len(data)), s.now().Unix())
	if err != nil {
		return SessionFilePut{}, mapSessionDBError(err)
	}
	if result.Conflict {
		return SessionFilePut{CurrentVersion: result.CurrentVersion}, ErrStaleSessionFile
	}
	return SessionFilePut{
		Version: result.Version, CurrentVersion: result.Version,
		Size: int64(len(data)),
	}, nil
}

// Append 在会话文件末尾追加字节（高频路径，纯增量不重传全文件）。
// 追加前按注册表 size 截掉上次崩溃残留的未提交字节。
func (s *SessionFileService) Append(
	ctx context.Context,
	accountID, groupID, deviceID, sessionID string,
	baseVersion int64,
	data []byte,
) (SessionFilePut, error) {
	if err := validateSessionID(sessionID); err != nil {
		return SessionFilePut{}, err
	}
	if baseVersion < 1 || len(data) == 0 {
		return SessionFilePut{}, ErrInvalidInput
	}
	unlock := s.lock(accountID, groupID, sessionID)
	defer unlock()

	row, err := s.q.GetSessionFile(ctx, accountID, groupID, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionFilePut{}, ErrSessionFileNotFound
	}
	if err != nil {
		return SessionFilePut{}, err
	}
	if row.Version != baseVersion {
		return SessionFilePut{CurrentVersion: row.Version}, ErrStaleSessionFile
	}
	newSize := row.Size + int64(len(data))
	if newSize > MaxSessionFileBytes {
		return SessionFilePut{}, ErrInvalidInput
	}

	if err := s.files.Append(
		accountID, groupID, sessionID+".jsonl", row.Size, data); err != nil {
		return SessionFilePut{}, err
	}
	result, err := s.q.UpsertSessionFileCAS(
		ctx, accountID, groupID, deviceID, sessionID,
		baseVersion, newSize, s.now().Unix())
	if err != nil {
		return SessionFilePut{}, mapSessionDBError(err)
	}
	if result.Conflict {
		return SessionFilePut{CurrentVersion: result.CurrentVersion}, ErrStaleSessionFile
	}
	return SessionFilePut{
		Version: result.Version, CurrentVersion: result.Version, Size: newSize,
	}, nil
}

// Delete 删除会话文件。注册行删除成功后 best-effort 删除磁盘文件。
func (s *SessionFileService) Delete(
	ctx context.Context,
	accountID, groupID, deviceID, sessionID string,
) (bool, error) {
	if err := validateSessionID(sessionID); err != nil {
		return false, err
	}
	unlock := s.lock(accountID, groupID, sessionID)
	defer unlock()

	deleted, err := s.q.DeleteSessionFile(ctx, accountID, groupID, deviceID, sessionID)
	if err != nil {
		return false, mapSessionDBError(err)
	}
	if deleted {
		_ = s.files.Delete(accountID, groupID, sessionID+".jsonl")
	}
	return deleted, nil
}

// GetIndex 读取组内 index.json。从未上传过返回 ErrSessionFileNotFound。
func (s *SessionFileService) GetIndex(
	ctx context.Context,
	accountID, groupID string,
) (SessionFileContent, error) {
	row, err := s.q.GetSessionIndex(ctx, accountID, groupID)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionFileContent{}, ErrSessionFileNotFound
	}
	if err != nil {
		return SessionFileContent{}, err
	}
	data, err := s.files.Read(accountID, groupID, sessionIndexName)
	if errors.Is(err, store.ErrNotFound) {
		return SessionFileContent{}, fmt.Errorf("会话索引注册表与磁盘失联")
	}
	if err != nil {
		return SessionFileContent{}, err
	}
	if int64(len(data)) < row.Size {
		return SessionFileContent{}, fmt.Errorf("会话索引短于注册表记录")
	}
	return SessionFileContent{Version: row.Version, Data: data[:row.Size]}, nil
}

// PutIndex 全量原子重写 index.json（无追加语义）。
func (s *SessionFileService) PutIndex(
	ctx context.Context,
	accountID, groupID, deviceID string,
	baseVersion int64,
	data []byte,
) (SessionFilePut, error) {
	if baseVersion < 0 || len(data) == 0 || len(data) > MaxSessionFileBytes {
		return SessionFilePut{}, ErrInvalidInput
	}
	unlock := s.lock(accountID, groupID, sessionIndexName)
	defer unlock()

	var current int64
	row, err := s.q.GetSessionIndex(ctx, accountID, groupID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		current = 0
	case err != nil:
		return SessionFilePut{}, err
	default:
		current = row.Version
	}
	if current != baseVersion {
		return SessionFilePut{CurrentVersion: current}, ErrStaleSessionFile
	}

	if err := s.files.Rewrite(accountID, groupID, sessionIndexName, data); err != nil {
		return SessionFilePut{}, err
	}
	result, err := s.q.UpsertSessionIndexCAS(
		ctx, accountID, groupID, deviceID,
		baseVersion, int64(len(data)), s.now().Unix())
	if err != nil {
		return SessionFilePut{}, mapSessionDBError(err)
	}
	if result.Conflict {
		return SessionFilePut{CurrentVersion: result.CurrentVersion}, ErrStaleSessionFile
	}
	return SessionFilePut{
		Version: result.Version, CurrentVersion: result.Version,
		Size: int64(len(data)),
	}, nil
}

// RemoveGroupFiles 在 DiscardOtherGroups 后 best-effort 清理被丢弃组的
// 会话文件目录。注册表行已随 sync_groups 级联删除，此处只清磁盘。
func (s *SessionFileService) RemoveGroupFiles(accountID string, groupIDs []string) {
	for _, groupID := range groupIDs {
		_ = s.files.DeleteGroupDir(accountID, groupID)
	}
}
