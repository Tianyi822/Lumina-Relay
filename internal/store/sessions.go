// sessions.go 实现会话 JSONL 文件的不透明存储。
//
// 服务端把客户端的 .jsonl 会话文件与 index.json 当作不透明字节流按
// root/<accountId>/<groupId>/<name> 存取，不解析行内容（E2EE 哑存储定位，
// 见 session-storage-format §9）。
//
// 崩溃一致性模型：SQLite 注册表（session_files/session_indexes）是
// (version, size) 的唯一权威。所有写操作先动文件、后提交数据库；崩溃窗口
// 只会产生「文件比数据库记录长/新」的状态：
//   - 追加多出的未提交字节由下次 Append 的截断逻辑收敛，读路径按数据库
//     size 截断（service 层负责）；
//   - 重写残留的 .tmp- 临时文件由 CleanupTempFiles 清理；
//   - 客户端凭数据库版本 CAS 重试后永远能收敛。
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrSizeMismatch 表示会话文件实际大小小于注册表记录，文件已损坏，
// 调用方应引导客户端走全量重写。
var ErrSizeMismatch = errors.New("session file size mismatch")

// SessionStore 在 root 目录下按账户/同步组分目录存取会话文件。
type SessionStore struct {
	root string
}

// NewSessionStore 构造一个 SessionStore。root 应为 ~/.lumina-relay/sessions
// （生产）或 t.TempDir()/sessions（测试）。调用方负责确保 root 已存在。
func NewSessionStore(root string) *SessionStore {
	return &SessionStore{root: root}
}

// validateComponent 防御路径遍历：accountId/groupId 是服务端生成的 UUID、
// name 由 service 层校验后传入，这里做兜底检查。
func validateComponent(value string) error {
	if value == "" || strings.ContainsAny(value, "/\\") ||
		strings.Contains(value, "..") || strings.HasPrefix(value, ".") {
		return fmt.Errorf("非法路径片段 %q", value)
	}
	return nil
}

// pathFor 返回文件路径：root/<accountId>/<groupId>/<name>。
func (s *SessionStore) pathFor(accountID, groupID, name string) (string, error) {
	for _, part := range []string{accountID, groupID, name} {
		if err := validateComponent(part); err != nil {
			return "", err
		}
	}
	return filepath.Join(s.root, accountID, groupID, name), nil
}

// Rewrite 全量原子重写：写临时文件并 fsync 后 rename 覆盖目标，再 fsync 父目录。
// 与 BlockStore.PutNew 的差别是允许覆盖已有文件（rename 而非 no-replace link）。
func (s *SessionStore) Rewrite(accountID, groupID, name string, data []byte) error {
	path, err := s.pathFor(accountID, groupID, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建会话目录：%w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+name+".tmp-")
	if err != nil {
		return fmt.Errorf("创建会话临时文件：%w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("设置会话临时文件权限：%w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("写入会话内容：%w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("同步会话内容：%w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("关闭会话临时文件：%w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("原子安装会话文件：%w", err)
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// Append 在文件末尾追加字节。expectedSize 是注册表记录的当前大小：
//   - 实际大小 > expectedSize：先截断到 expectedSize（上次崩溃残留的未提交字节）；
//   - 实际大小 < expectedSize：返回 ErrSizeMismatch（文件损坏）；
//
// 追加后 fsync。文件不存在返回 ErrNotFound（注册表与文件失联属内部异常）。
func (s *SessionStore) Append(accountID, groupID, name string, expectedSize int64, data []byte) error {
	path, err := s.pathFor(accountID, groupID, name)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("打开会话文件：%w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("检查会话文件大小：%w", err)
	}
	switch {
	case info.Size() < expectedSize:
		return ErrSizeMismatch
	case info.Size() > expectedSize:
		if err := file.Truncate(expectedSize); err != nil {
			return fmt.Errorf("截断崩溃残留字节：%w", err)
		}
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("追加会话内容：%w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("同步会话追加内容：%w", err)
	}
	return nil
}

// Read 读取指定会话文件的全部内容。文件不存在返回 ErrNotFound。
// 调用方（service 层）负责按注册表 size 截断，屏蔽崩溃残留的尾部字节。
func (s *SessionStore) Read(accountID, groupID, name string) ([]byte, error) {
	path, err := s.pathFor(accountID, groupID, name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("读取会话文件：%w", err)
	}
	return data, nil
}

// Delete 删除一个会话文件。不存在视为幂等成功。
func (s *SessionStore) Delete(accountID, groupID, name string) error {
	path, err := s.pathFor(accountID, groupID, name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("删除会话文件：%w", err)
	}
	return nil
}

// DeleteGroupDir 删除整个同步组目录（DiscardOtherGroups 后的 best-effort 清理）。
// 数据库层必须先删除对应注册表行，调用方不得据此实现越权删除。
func (s *SessionStore) DeleteGroupDir(accountID, groupID string) error {
	for _, part := range []string{accountID, groupID} {
		if err := validateComponent(part); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(filepath.Join(s.root, accountID, groupID)); err != nil {
		return fmt.Errorf("删除同步组会话目录：%w", err)
	}
	return nil
}

// CleanupTempFiles 清理崩溃遗留且超过 maxAge 的重写临时文件。
// rename 是重写的最后一步且注册表是版本权威，孤儿 tmp 一律可删、无需恢复。
func (s *SessionStore) CleanupTempFiles(maxAge time.Duration) error {
	if maxAge <= 0 {
		maxAge = time.Hour
	}
	cutoff := time.Now().Add(-maxAge)
	return filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !strings.Contains(entry.Name(), ".tmp-") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
		return nil
	})
}
