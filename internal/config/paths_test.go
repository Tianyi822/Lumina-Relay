package config

import (
	"path/filepath"
	"testing"
)

func TestDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := DataDir()
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if dir != filepath.Join(home, ".lumina-relay") {
		t.Fatalf("DataDir = %q, want %q", dir, filepath.Join(home, ".lumina-relay"))
	}
}

func TestDefaultConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if path != filepath.Join(home, ".lumina-relay", "config.yaml") {
		t.Fatalf("DefaultConfigPath = %q", path)
	}
}

func TestDefaultLogPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := DefaultLogPath()
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if path != filepath.Join(home, ".lumina-relay", "logs", "lumina-relay.log") {
		t.Fatalf("DefaultLogPath = %q", path)
	}
}

func TestDefaultDBPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if path != filepath.Join(home, ".lumina-relay", "db", "relay.db") {
		t.Fatalf("DefaultDBPath = %q", path)
	}
}

func TestDefaultBlocksDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := DefaultBlocksDir()
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if dir != filepath.Join(home, ".lumina-relay", "blocks") {
		t.Fatalf("DefaultBlocksDir = %q", dir)
	}
}
