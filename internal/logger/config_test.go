package logger

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Level != "info" {
		t.Fatalf("Level = %q, want \"info\"", c.Level)
	}
	if !c.File.Enabled {
		t.Fatal("File.Enabled 应为 true")
	}
	if c.File.Path != "logs/lumina-relay.log" {
		t.Fatalf("Path = %q", c.File.Path)
	}
	if c.File.MaxSizeMB != 100 || c.File.MaxBackups != 7 || c.File.MaxAgeDays != 28 || !c.File.Compress {
		t.Fatalf("轮转默认值不正确：%+v", c.File)
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    zapcore.Level
		wantErr bool
	}{
		{"debug", zapcore.DebugLevel, false},
		{"info", zapcore.InfoLevel, false},
		{"INFO", zapcore.InfoLevel, false}, // 大小写不敏感
		{"warn", zapcore.WarnLevel, false},
		{"error", zapcore.ErrorLevel, false},
		{"", zapcore.InfoLevel, false}, // 空串回退 info
		{"bogus", zapcore.InfoLevel, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseLevel(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望错误，got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("未期望错误：%v", err)
			}
			if got != c.want {
				t.Fatalf("level = %v, want %v", got, c.want)
			}
		})
	}
}

func TestApplyEnvOverrides_Level(t *testing.T) {
	t.Setenv("LUMINA_LOG_LEVEL", "debug")
	cfg := ApplyEnvOverrides(DefaultConfig())
	if cfg.Level != "debug" {
		t.Fatalf("Level = %q, want \"debug\"", cfg.Level)
	}
}

func TestApplyEnvOverrides_FilePath(t *testing.T) {
	t.Setenv("LUMINA_LOG_FILE", "/tmp/custom.log")
	cfg := ApplyEnvOverrides(DefaultConfig())
	if cfg.File.Path != "/tmp/custom.log" {
		t.Fatalf("Path = %q, want /tmp/custom.log", cfg.File.Path)
	}
	if !cfg.File.Enabled {
		t.Fatal("设置 LUMINA_LOG_FILE 后 Enabled 应为 true")
	}
}

func TestApplyEnvOverrides_FileEnabledFalse(t *testing.T) {
	t.Setenv("LUMINA_LOG_FILE_ENABLED", "false")
	cfg := ApplyEnvOverrides(DefaultConfig())
	if cfg.File.Enabled {
		t.Fatal("Enabled 应被覆盖为 false")
	}
}

func TestApplyEnvOverrides_FileEnabledOne(t *testing.T) {
	t.Setenv("LUMINA_LOG_FILE_ENABLED", "1")
	cfg := ApplyEnvOverrides(DefaultConfig())
	if !cfg.File.Enabled {
		t.Fatal("Enabled 应被 \"1\" 覆盖为 true")
	}
}

// 显式清空可能从进程环境继承的 LUMINA_LOG_* 变量，
// 保证"无 env 覆盖"断言不受外部环境污染。
func TestApplyEnvOverrides_NoEnvKeepsDefaults(t *testing.T) {
	t.Setenv("LUMINA_LOG_LEVEL", "")
	t.Setenv("LUMINA_LOG_FILE", "")
	t.Setenv("LUMINA_LOG_FILE_ENABLED", "")
	cfg := ApplyEnvOverrides(DefaultConfig())
	if cfg.Level != "info" || !cfg.File.Enabled {
		t.Fatalf("无 env 覆盖时应保留默认：%+v", cfg)
	}
}
