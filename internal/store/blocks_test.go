package store

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

// newTestStore 在 t.TempDir() 下建一个 BlockStore，测试一律不碰 ~/.lumina-relay。
func newTestStore(t *testing.T) *BlockStore {
	t.Helper()
	return NewBlockStore(t.TempDir())
}

// TestBlockStore_PutNewGet_RoundTrip 验证写入后能完整读回相同字节。
func TestBlockStore_PutNewGet_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	id := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	body := []byte("some ciphertext payload")

	created, err := s.PutNew(id, body)
	if err != nil {
		t.Fatalf("PutNew 失败：%v", err)
	}
	if !created {
		t.Fatal("首次写入 created 应为 true")
	}

	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get 失败：%v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("读回内容不一致：got %d bytes, want %d bytes", len(got), len(body))
	}
}

// TestBlockStore_PutNew_Exclusive 验证 O_CREATE|O_EXCL 去重：
// 同一 id 第二次 PutNew 返回 ErrAlreadyExists 且 created=false，
// 且不覆盖既有内容。
func TestBlockStore_PutNew_Exclusive(t *testing.T) {
	s := newTestStore(t)
	id := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	original := []byte("original")

	if _, err := s.PutNew(id, original); err != nil {
		t.Fatalf("首次 PutNew 失败：%v", err)
	}

	created, err := s.PutNew(id, []byte("overwrite attempt"))
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("第二次 PutNew 应返回 ErrAlreadyExists，得到 %v", err)
	}
	if created {
		t.Fatal("已存在时 created 应为 false")
	}

	// 验证未被覆盖
	got, err := s.Get(id)
	if err != nil {
		t.Fatalf("Get 失败：%v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("内容被覆盖：got %q, want %q", got, original)
	}
}

// TestBlockStore_Exists 验证 Exists 在写入前后返回正确布尔值。
func TestBlockStore_Exists(t *testing.T) {
	s := newTestStore(t)
	id := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

	if s.Exists(id) {
		t.Fatal("未写入时 Exists 不应为 true")
	}

	if _, err := s.PutNew(id, []byte("x")); err != nil {
		t.Fatalf("PutNew 失败：%v", err)
	}
	if !s.Exists(id) {
		t.Fatal("写入后 Exists 应为 true")
	}
}

// TestBlockStore_Get_Missing 验证读取不存在的块返回包装错误（可被 errors.Is 判定）。
func TestBlockStore_Get_Missing(t *testing.T) {
	s := newTestStore(t)
	id := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

	if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("读取缺失块应返回 ErrNotFound，得到 %v", err)
	}
}

// TestBlockStore_PathFor_BucketLayout 验证分桶路径布局：
// root/<id[0:2]>/<id[0:4]>/<id>（见 sync-design §2.5）。
func TestBlockStore_PathFor_BucketLayout(t *testing.T) {
	root := t.TempDir()
	s := NewBlockStore(root)
	id := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

	want := filepath.Join(root, "ab", "abcd", id)
	if got := s.PathFor(id); got != want {
		t.Fatalf("PathFor = %q, want %q", got, want)
	}
}

// TestBlockStore_PutNew_ShortIDRejected 验证 len(id)<4 时拒绝写入，
// 避免 id 被切割到错误的分桶路径。
func TestBlockStore_PutNew_ShortIDRejected(t *testing.T) {
	s := newTestStore(t)
	cases := []string{"", "ab", "abc"}
	for _, id := range cases {
		if _, err := s.PutNew(id, []byte("x")); err == nil {
			t.Fatalf("短 id %q 应被拒绝，但 PutNew 返回 nil", id)
		}
	}
}
