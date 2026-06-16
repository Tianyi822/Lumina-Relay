package logger

import (
	"os"
	"testing"
)

// TestMain 给 logger 包提供一个非 nil 的 global，使未显式隔离的包级
// 日志调用（Info/Warn 等）在测试中不 nil panic。
// 故意【不】调用 InitBootstrap：bootstrap 的初始化逻辑由 bootstrap_test.go
// 自洽验证，避免 TestMain 副作用与 bootstrap 测试耦合。
func TestMain(m *testing.M) {
	global = &recordingLogger{}
	os.Exit(m.Run())
}

// recordingLogger 捕获日志调用，供测试断言。仅测试用。
type recordingLogger struct {
	entries []logEntry
}

type logEntry struct {
	level  string
	msg    string
	fields []Field
}

func (r *recordingLogger) Debug(msg string, fields ...Field) {
	r.entries = append(r.entries, logEntry{"debug", msg, fields})
}
func (r *recordingLogger) Info(msg string, fields ...Field) {
	r.entries = append(r.entries, logEntry{"info", msg, fields})
}
func (r *recordingLogger) Warn(msg string, fields ...Field) {
	r.entries = append(r.entries, logEntry{"warn", msg, fields})
}
func (r *recordingLogger) Error(msg string, fields ...Field) {
	r.entries = append(r.entries, logEntry{"error", msg, fields})
}

// With 在测试场景下返回一个空记录器（这些测试不验证上下文继承）。
func (r *recordingLogger) With(fields ...Field) Logger { return &recordingLogger{} }

func (r *recordingLogger) Sync() error { return nil }

// withGlobal 在测试期间替换包级 global 并在结束时恢复。
func withGlobal(t *testing.T, l Logger) {
	t.Helper()
	prev := global
	global = l
	t.Cleanup(func() { global = prev })
}

// withFallback 在测试期间替换包级 fallback 并在结束时恢复。
func withFallback(t *testing.T, l Logger) {
	t.Helper()
	prev := fallback
	fallback = l
	t.Cleanup(func() { fallback = prev })
}
