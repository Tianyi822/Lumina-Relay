package logger

import (
	"log/slog"
	"os"
	"sync"
)

var bootstrapOnce sync.Once

// InitBootstrap 立即把全局 logger 设为 slog 兜底。
// 幂等，重复调用无副作用。绝不返回 error。
func InitBootstrap() {
	bootstrapOnce.Do(func() {
		h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug, // 兜底期全量记录
		})
		bl := &bootstrapLogger{sl: slog.New(h)}
		global = bl
		fallback = bl // 保留引用以备 panic 回退
	})
}

type bootstrapLogger struct{ sl *slog.Logger }

func (b *bootstrapLogger) Debug(msg string, fields ...Field) { b.sl.Debug(msg, toSlogArgs(fields)...) }
func (b *bootstrapLogger) Info(msg string, fields ...Field)  { b.sl.Info(msg, toSlogArgs(fields)...) }
func (b *bootstrapLogger) Warn(msg string, fields ...Field)  { b.sl.Warn(msg, toSlogArgs(fields)...) }
func (b *bootstrapLogger) Error(msg string, fields ...Field) { b.sl.Error(msg, toSlogArgs(fields)...) }

func (b *bootstrapLogger) With(fields ...Field) Logger {
	return &bootstrapLogger{sl: b.sl.With(toSlogArgs(fields)...)}
}

// slog 无缓冲，无需 flush。
func (b *bootstrapLogger) Sync() error { return nil }

// toSlogArgs 把通用 Field 切片转成 slog 的 key-value 可变参数序列。
func toSlogArgs(fields []Field) []any {
	args := make([]any, 0, len(fields)*2)
	for _, f := range fields {
		args = append(args, f.Key, f.Val)
	}
	return args
}
