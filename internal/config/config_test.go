package config

import (
	"path/filepath"
	"testing"

	"lumina-relay/internal/logger"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Server.Port != 8443 {
		t.Fatalf("Port = %d, want 8443", cfg.Server.Port)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Fatalf("Host = %q, want 0.0.0.0", cfg.Server.Host)
	}
	if cfg.Log.Level != "info" {
		t.Fatalf("Log.Level = %q", cfg.Log.Level)
	}
	if !cfg.Log.File.Enabled {
		t.Fatal("Log.File.Enabled 应为 true")
	}
}

func TestDefault_LogPathUnderDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := Default()
	want := filepath.Join(home, ".lumina-relay", "logs", "lumina-relay.log")
	if cfg.Log.File.Path != want {
		t.Fatalf("Log.File.Path = %q, want %q", cfg.Log.File.Path, want)
	}
}

func TestDefaultIsIndependentInstance(t *testing.T) {
	a := Default()
	b := Default()
	a.Server.Port = 1234
	if b.Server.Port == 1234 {
		t.Fatal("Default() 返回的实例间应互不影响")
	}
}

func TestAppConfigEmbedsLogConfig(t *testing.T) {
	// 确保 AppConfig.Log 类型确实是 logger.LogConfig（编译期保证）
	var _ logger.LogConfig = Default().Log
}
