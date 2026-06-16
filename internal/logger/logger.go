// Package logger 提供 lumina-relay 的统一日志抽象。
// 启动期先以 slog 兜底（InitBootstrap），配置就绪后切换到 zap（InitZap）。
// 所有调用方只依赖包级 API 与 Logger 接口，对底层后端零感知。
package logger

// Logger 是日志后端的统一抽象，兜底与正式都实现它。
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	With(fields ...Field) Logger // 返回带上下文的新 logger
	Sync() error                 // flush（zap 需要，slog 空实现）
}

// Field 是结构化字段，底层按后端转换（slog attr / zap.Field）。
type Field struct {
	Key string
	Val any
}

// global 是当前生效的 logger：InitBootstrap 设为兜底，InitZap 替换为 zap。
var global Logger

// fallback 保留兜底 logger 引用，panic 回退时用。
// 由 InitBootstrap 赋值；此任务只声明变量（尚未使用），实现见 Task 4 bootstrap.go。
var fallback Logger

// 包级便捷函数，转发给 global。
func Debug(msg string, fields ...Field) { global.Debug(msg, fields...) }
func Info(msg string, fields ...Field)  { global.Info(msg, fields...) }
func Warn(msg string, fields ...Field)  { global.Warn(msg, fields...) }
func Error(msg string, fields ...Field) { global.Error(msg, fields...) }

func With(fields ...Field) Logger { return global.With(fields...) }
func Sync() error                 { return global.Sync() }

// 字段构造器
func String(k, v string) Field      { return Field{Key: k, Val: v} }
func Int(k string, v int) Field     { return Field{Key: k, Val: v} }
func Int64(k string, v int64) Field { return Field{Key: k, Val: v} }
func Bool(k string, v bool) Field   { return Field{Key: k, Val: v} }
func Err(err error) Field           { return Field{Key: "error", Val: err} }
func Any(k string, v any) Field     { return Field{Key: k, Val: v} }
