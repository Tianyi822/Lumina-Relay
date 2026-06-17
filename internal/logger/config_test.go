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
