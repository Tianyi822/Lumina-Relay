package writer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewRotatingWriter_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "deep", "app.log") // 多级不存在目录

	w, err := NewRotatingWriter(RotatingConfig{
		Path: path, MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 1, Compress: false,
	})
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if w == nil {
		t.Fatal("writer 为 nil")
	}

	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("目录未创建：%v", err)
	}
	if n, err := w.Write([]byte("hello")); err != nil || n != 5 {
		t.Fatalf("写入失败：n=%d err=%v", n, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件未创建：%v", err)
	}
}

// 可靠失败：把路径中间段设成一个已存在的【文件】，MkdirAll 会因
// "not a directory" 失败。跨平台、不依赖 root/权限假设。
func TestNewRotatingWriter_NotWritable(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup 失败：%v", err)
	}
	// blocker 是文件，在其下创建子目录必然失败
	_, err := NewRotatingWriter(RotatingConfig{
		Path: filepath.Join(blocker, "sub", "app.log"),
	})
	if err == nil {
		t.Fatal("期望错误，got nil")
	}
}
