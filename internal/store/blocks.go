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

// PutNew 写入一个新块。若已存在则返回 ErrAlreadyExists 且不覆盖既有内容。
// 返回 created=true 表示本次创建了文件，false 表示已存在（幂等）。
// id 长度 < minIDLen 时返回错误，避免 id 被切割到错误分桶。
func (s *BlockStore) PutNew(id string, data []byte) (created bool, err error) {
	if len(id) < minIDLen {
		return false, fmt.Errorf("blockId 过短（len=%d，需 ≥%d）", len(id), minIDLen)
	}
	path := s.PathFor(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("创建分桶目录：%w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, ErrAlreadyExists
		}
		return false, fmt.Errorf("创建块文件：%w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return false, fmt.Errorf("写入块内容：%w", err)
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
