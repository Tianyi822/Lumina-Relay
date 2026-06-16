package logger

import (
	"errors"
	"testing"
)

func TestFieldConstructors(t *testing.T) {
	cases := []struct {
		name string
		got  Field
		key  string
	}{
		{"String", String("k", "v"), "k"},
		{"Int", Int("k", 1), "k"},
		{"Int64", Int64("k", 2), "k"},
		{"Bool", Bool("k", true), "k"},
		{"Any", Any("k", 123), "k"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got.Key != c.key {
				t.Fatalf("Key = %q, want %q", c.got.Key, c.key)
			}
			if c.got.Val == nil {
				t.Fatalf("Val 不应为 nil")
			}
		})
	}
}

func TestFieldError(t *testing.T) {
	err := errors.New("boom")
	f := Err(err)
	if f.Key != "error" {
		t.Fatalf("Key = %q, want \"error\"", f.Key)
	}
	if f.Val.(error).Error() != "boom" {
		t.Fatalf("Val 不是预期 error")
	}
}

func TestPackageLevelFunctionsForwardToGlobal(t *testing.T) {
	rec := &recordingLogger{}
	withGlobal(t, rec)

	Debug("d", String("a", "1"))
	Info("i", Int("b", 2))
	Warn("w")
	Error("e", Bool("c", true))

	if len(rec.entries) != 4 {
		t.Fatalf("捕获到 %d 条，want 4", len(rec.entries))
	}
	wantLevels := []string{"debug", "info", "warn", "error"}
	wantMsgs := []string{"d", "i", "w", "e"}
	for i, e := range rec.entries {
		if e.level != wantLevels[i] || e.msg != wantMsgs[i] {
			t.Fatalf("entry[%d] = {%s,%s}, want {%s,%s}", i, e.level, e.msg, wantLevels[i], wantMsgs[i])
		}
	}
	if rec.entries[1].fields[0].Key != "b" {
		t.Fatalf("Info 字段未透传：got %+v", rec.entries[1].fields)
	}
}

func TestPackageWithReturnsLogger(t *testing.T) {
	rec := &recordingLogger{}
	withGlobal(t, rec)
	if l := With(String("ctx", "v")); l == nil {
		t.Fatal("With 返回 nil")
	}
}

func TestPackageSyncForwardsToGlobal(t *testing.T) {
	rec := &recordingLogger{}
	withGlobal(t, rec)
	if err := Sync(); err != nil {
		t.Fatalf("Sync 返回错误：%v", err)
	}
}
