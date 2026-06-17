package logger

import (
	"os"

	"lumina-relay/internal/logger/writer"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// InitZap 按配置构建 zap 并替换全局 logger。
// 成功 → 全局替换为 zap（stderr JSON + 文件 Console 双写）。
// 级别非法 → 返回 error（main 决定降级或退出）。
// 文件不可写 → 降级为仅 stderr，返回有效 logger（不报错）。
func InitZap(cfg LogConfig) (*zap.Logger, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}

	// —— stderr core：JSON，给容器日志收集器 ——
	stderrEncoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	stderrCore := zapcore.NewCore(stderrEncoder, zapcore.Lock(os.Stderr), level)

	// —— 文件 core：Console，给运维 tail ——
	cores := []zapcore.Core{stderrCore}
	if cfg.File.Enabled {
		// FileConfig（logger 包）→ RotatingConfig（writer 包），避免循环依赖
		fw, ferr := writer.NewRotatingWriter(writer.RotatingConfig{
			Path:       cfg.File.Path,
			MaxSizeMB:  cfg.File.MaxSizeMB,
			MaxBackups: cfg.File.MaxBackups,
			MaxAgeDays: cfg.File.MaxAgeDays,
			Compress:   cfg.File.Compress,
		})
		if ferr != nil {
			// 文件不可写：禁用文件 core，降级为仅 stderr，记录警告
			// （此时 global 仍是兜底或上一个实例，用包级 Error 记录）
			Error("日志文件不可写，降级为仅 stderr",
				Field{Key: "error", Val: ferr}, String("path", cfg.File.Path))
		} else {
			fileEncoder := zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
			fileCore := zapcore.NewCore(fileEncoder, zapcore.AddSync(fw), level)
			cores = append(cores, fileCore)
		}
	}

	core := zapcore.NewTee(cores...)
	zl := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	global = &zapLogger{zl: zl}
	return zl, nil
}

type zapLogger struct{ zl *zap.Logger }

func (z *zapLogger) Debug(msg string, fields ...Field) { z.zl.Debug(msg, toZapFields(fields)...) }
func (z *zapLogger) Info(msg string, fields ...Field)  { z.zl.Info(msg, toZapFields(fields)...) }
func (z *zapLogger) Warn(msg string, fields ...Field)  { z.zl.Warn(msg, toZapFields(fields)...) }
func (z *zapLogger) Error(msg string, fields ...Field) { z.zl.Error(msg, toZapFields(fields)...) }

func (z *zapLogger) With(fields ...Field) Logger {
	return &zapLogger{zl: z.zl.With(toZapFields(fields)...)}
}

func (z *zapLogger) Sync() error { return z.zl.Sync() }

// toZapFields 把通用 Field 转成 zap.Field。
// nil/未知类型走 zap.Any 兜底，避免转换 panic。
func toZapFields(fields []Field) []zap.Field {
	zf := make([]zap.Field, 0, len(fields))
	for _, f := range fields {
		switch v := f.Val.(type) {
		case string:
			zf = append(zf, zap.String(f.Key, v))
		case int:
			zf = append(zf, zap.Int(f.Key, v))
		case int64:
			zf = append(zf, zap.Int64(f.Key, v))
		case bool:
			zf = append(zf, zap.Bool(f.Key, v))
		case error:
			zf = append(zf, zap.Error(v))
		default:
			zf = append(zf, zap.Any(f.Key, v))
		}
	}
	return zf
}
