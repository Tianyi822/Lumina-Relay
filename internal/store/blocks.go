// Package store 实现 lumina-relay 的密文块文件存储。
//
// 服务端是"按 hash 存取密文块"的哑存储（见 sync-design §2.5 / §6.1）：
// blocks/ 目录按 id 前 2/前 4 字符分桶，避免单目录百万文件。
// 写入用 O_CREATE|O_EXCL 天然去重；服务端不碰明文、不解密。
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

// 哨兵错误。调用方用 errors.Is 判定，不应比较字符串。
var (
	// ErrAlreadyExists 表示指定 blockId 已存在（PutNew 语义：幂等去重）。
	ErrAlreadyExists = errors.New("block already exists")
	// ErrNotFound 表示指定 blockId 不存在。
	ErrNotFound = errors.New("block not found")
)

// minIDLen 是分桶所需的最短 id 长度（id[0:2] 与 id[0:4] 必须可取）。
// 实际 blockId=sha256 恒为 64 hex 字符，远超此下限；此约束仅防御异常输入。
const minIDLen = 4

// BlockStore 在 root 目录下按分桶布局存取密文块。
type BlockStore struct {
	root string
}

// NewBlockStore 构造一个 BlockStore。root 应为 ~/.lumina-relay/blocks（生产）
// 或 t.TempDir()/blocks（测试）。调用方负责确保 root 已存在并具正确权限。
func NewBlockStore(root string) *BlockStore {
	return &BlockStore{root: root}
}

// PathFor 返回指定 blockId 在分桶布局下的文件路径：root/<id[0:2]>/<id[0:4]>/<id>。
// 不校验 id 长度（PutNew/Get 会校验）；供测试与运维排查使用。
func (s *BlockStore) PathFor(id string) string {
	return filepath.Join(s.root, id[0:2], id[0:4], id)
}

// PutNew 先在目标 shard 写临时文件并 fsync，再用 hard-link no-replace 语义
// 原子安装；目标已存在时绝不覆盖。
func (s *BlockStore) PutNew(id string, data []byte) (created bool, err error) {
	if len(id) < minIDLen {
		return false, fmt.Errorf("blockId 过短（len=%d，需 ≥%d）", len(id), minIDLen)
	}
	path := s.PathFor(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("创建分桶目录：%w", err)
	}

	if _, err := os.Stat(path); err == nil {
		return false, ErrAlreadyExists
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+id+".tmp-")
	if err != nil {
		return false, fmt.Errorf("创建块临时文件：%w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("设置块临时文件权限：%w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("写入块内容：%w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("同步块内容：%w", err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("关闭块临时文件：%w", err)
	}
	if err := os.Link(tempPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, ErrAlreadyExists
		}
		return false, fmt.Errorf("原子安装块文件：%w", err)
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return true, nil
}

// Get 读取指定块的全部内容。块不存在时返回 ErrNotFound。
func (s *BlockStore) Get(id string) ([]byte, error) {
	if len(id) < minIDLen {
		return nil, fmt.Errorf("blockId 过短（len=%d，需 ≥%d）", len(id), minIDLen)
	}
	data, err := os.ReadFile(s.PathFor(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("读取块文件：%w", err)
	}
	return data, nil
}

// Exists 判断指定块文件是否已存在。
func (s *BlockStore) Exists(id string) bool {
	if len(id) < minIDLen {
		return false
	}
	_, err := os.Stat(s.PathFor(id))
	return err == nil
}

// Delete 删除一个物理密文块。不存在视为幂等成功。
// 数据库层必须先确认已经没有任何账户关联，调用方不得据此实现越权删除。
func (s *BlockStore) Delete(id string) error {
	if len(id) < minIDLen {
		return fmt.Errorf("blockId 过短（len=%d，需 ≥%d）", len(id), minIDLen)
	}
	if err := os.Remove(s.PathFor(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("删除块文件：%w", err)
	}
	return nil
}

// CleanupTempFiles 清理崩溃遗留且超过 maxAge 的 shard 临时文件。
func (s *BlockStore) CleanupTempFiles(maxAge time.Duration) error {
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
