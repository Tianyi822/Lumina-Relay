package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestSessionStore(t *testing.T) *SessionStore {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return NewSessionStore(root)
}

func TestSessionStoreRewriteAndRead(t *testing.T) {
	s := newTestSessionStore(t)
	first := []byte("{\"kind\":\"meta\"}\n")
	if err := s.Rewrite("acc", "grp", "session-1-a.jsonl", first); err != nil {
		t.Fatalf("首次重写：%v", err)
	}
	got, err := s.Read("acc", "grp", "session-1-a.jsonl")
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("读回=%q err=%v", got, err)
	}
	// 覆盖重写：允许替换已有文件
	second := []byte("{\"kind\":\"meta\",\"v\":1}\n{\"kind\":\"message\"}\n")
	if err := s.Rewrite("acc", "grp", "session-1-a.jsonl", second); err != nil {
		t.Fatalf("覆盖重写：%v", err)
	}
	got, err = s.Read("acc", "grp", "session-1-a.jsonl")
	if err != nil || !bytes.Equal(got, second) {
		t.Fatalf("覆盖后读回=%q err=%v", got, err)
	}
}

func TestSessionStoreReadMissingReturnsNotFound(t *testing.T) {
	s := newTestSessionStore(t)
	if _, err := s.Read("acc", "grp", "missing.jsonl"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
}

func TestSessionStoreAppend(t *testing.T) {
	s := newTestSessionStore(t)
	base := []byte("line-1\n")
	if err := s.Rewrite("acc", "grp", "s.jsonl", base); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("acc", "grp", "s.jsonl", int64(len(base)), []byte("line-2\n")); err != nil {
		t.Fatalf("追加：%v", err)
	}
	got, err := s.Read("acc", "grp", "s.jsonl")
	if err != nil || string(got) != "line-1\nline-2\n" {
		t.Fatalf("追加后读回=%q err=%v", got, err)
	}
}

func TestSessionStoreAppendTruncatesCrashResidue(t *testing.T) {
	s := newTestSessionStore(t)
	base := []byte("line-1\n")
	if err := s.Rewrite("acc", "grp", "s.jsonl", base); err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃残留：文件比注册表记录长（上次追加未提交数据库）
	if err := s.Append("acc", "grp", "s.jsonl", int64(len(base)), []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	// 客户端按注册表 size 重试追加：残留的 "orphan" 应被截掉
	if err := s.Append("acc", "grp", "s.jsonl", int64(len(base)), []byte("line-2\n")); err != nil {
		t.Fatalf("截断重试追加：%v", err)
	}
	got, err := s.Read("acc", "grp", "s.jsonl")
	if err != nil || string(got) != "line-1\nline-2\n" {
		t.Fatalf("截断后读回=%q err=%v", got, err)
	}
}

func TestSessionStoreAppendRejectsShrunkFile(t *testing.T) {
	s := newTestSessionStore(t)
	if err := s.Rewrite("acc", "grp", "s.jsonl", []byte("ab")); err != nil {
		t.Fatal(err)
	}
	// 注册表声称 10 字节，实际只有 2 字节：文件损坏
	if err := s.Append("acc", "grp", "s.jsonl", 10, []byte("x")); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("err=%v want ErrSizeMismatch", err)
	}
}

func TestSessionStoreAppendMissingReturnsNotFound(t *testing.T) {
	s := newTestSessionStore(t)
	if err := s.Append("acc", "grp", "missing.jsonl", 0, []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
}

func TestSessionStoreDeleteIsIdempotent(t *testing.T) {
	s := newTestSessionStore(t)
	if err := s.Rewrite("acc", "grp", "s.jsonl", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("acc", "grp", "s.jsonl"); err != nil {
		t.Fatalf("删除：%v", err)
	}
	if err := s.Delete("acc", "grp", "s.jsonl"); err != nil {
		t.Fatalf("重复删除应幂等：%v", err)
	}
	if _, err := s.Read("acc", "grp", "s.jsonl"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后读取 err=%v want ErrNotFound", err)
	}
}

func TestSessionStoreRejectsTraversalComponents(t *testing.T) {
	s := newTestSessionStore(t)
	bad := [][3]string{
		{"..", "grp", "s.jsonl"},
		{"acc", "../other", "s.jsonl"},
		{"acc", "grp", "../escape"},
		{"acc", "grp", "a/b.jsonl"},
		{"acc", "grp", ".hidden"},
		{"acc", "grp", ""},
	}
	for _, parts := range bad {
		if err := s.Rewrite(parts[0], parts[1], parts[2], []byte("x")); err == nil {
			t.Errorf("Rewrite(%q,%q,%q) 应拒绝", parts[0], parts[1], parts[2])
		}
		if _, err := s.Read(parts[0], parts[1], parts[2]); err == nil {
			t.Errorf("Read(%q,%q,%q) 应拒绝", parts[0], parts[1], parts[2])
		}
	}
}

func TestSessionStoreDeleteGroupDir(t *testing.T) {
	s := newTestSessionStore(t)
	if err := s.Rewrite("acc", "grp", "s.jsonl", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Rewrite("acc", "grp", "index.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGroupDir("acc", "grp"); err != nil {
		t.Fatalf("删除组目录：%v", err)
	}
	if _, err := s.Read("acc", "grp", "s.jsonl"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("组目录删除后仍可读取 err=%v", err)
	}
	if err := s.DeleteGroupDir("acc", "grp"); err != nil {
		t.Fatalf("重复删除组目录应幂等：%v", err)
	}
}

func TestSessionStoreCleanupTempFiles(t *testing.T) {
	s := newTestSessionStore(t)
	dir := filepath.Join(s.root, "acc", "grp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, ".s.jsonl.tmp-123")
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, ".s.jsonl.tmp-456")
	if err := os.WriteFile(fresh, []byte("writing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupTempFiles(time.Hour); err != nil {
		t.Fatalf("清理临时文件：%v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("过期临时文件应被删除")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("未过期临时文件不应被删除")
	}
}
