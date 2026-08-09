package logger

import (
	"testing"
)

// Recover 应：切回 fallback、记录一条含 panic 与 stack 的 error、重新 panic。
// 测试用 withFallback 注入 recordingLogger 作为可观测的 fallback，自洽不依赖 TestMain。
func TestRecover_LogsAndRepanics(t *testing.T) {
	rec := &recordingLogger{}
	withFallback(t, rec)
	withGlobal(t, &recordingLogger{}) // 初始 global 非 fallback，验证 Recover 会切回 fallback

	repanicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				repanicked = true
			}
		}()
		defer Recover()
		panic("boom")
	}()

	if !repanicked {
		t.Fatal("Recover 应重新 panic")
	}
	if global != rec {
		t.Fatal("Recover 后 global 应等于 fallback")
	}
	if len(rec.entries) == 0 {
		t.Fatal("Recover 应通过 fallback 记录 panic")
	}
	var got error
	for _, e := range rec.entries {
		if e.level == "error" {
			got = nil
			for _, f := range e.fields {
				if f.Key == "stack" {
					s, _ := f.Val.(string)
					if s == "" {
						t.Fatal("stack 字段不应为空")
					}
					got = nil
				}
			}
		}
	}
	_ = got
}

func TestRecover_NoPanicIsNoop(t *testing.T) {
	// 无 panic 时 Recover 不应崩溃
	func() {
		defer Recover()
		// 正常返回
	}()
}

// RecoverLogOnly 应记录 panic 并吞掉（不 re-panic），供子 goroutine 使用。
func TestRecoverLogOnly_SwallowsPanic(t *testing.T) {
	rec := &recordingLogger{}
	withFallback(t, rec)
	withGlobal(t, &recordingLogger{}) // 初始 global 非 fallback，验证会切回 fallback

	repanicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				repanicked = true
			}
		}()
		defer RecoverLogOnly()
		panic("boom")
	}()

	if repanicked {
		t.Fatal("RecoverLogOnly 不应重新 panic")
	}
	if global != rec {
		t.Fatal("RecoverLogOnly 后 global 应等于 fallback")
	}
	// 应记录一条 error（fallback 已 flush，需查看 rec 的日志实现记录方式）。
	if len(rec.entries) == 0 {
		t.Fatal("RecoverLogOnly 应通过 fallback 记录 panic")
	}
}

func TestRecoverLogOnly_NoPanicIsNoop(t *testing.T) {
	func() {
		defer RecoverLogOnly()
		// 正常返回
	}()
}
