package logger

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestToZapFields(t *testing.T) {
	fields := []Field{
		String("s", "x"),
		Int("i", 1),
		Int64("l", 2),
		Bool("b", true),
		Err(errors.New("boom")),
		Any("a", struct{ X int }{X: 9}),
	}
	zf := toZapFields(fields)
	if len(zf) != len(fields) {
		t.Fatalf("长度 = %d, want %d", len(zf), len(fields))
	}
	if zf[0].Type != zapcore.StringType {
		t.Fatalf("string 字段类型错误：%v", zf[0].Type)
	}
	if zf[1].Type != zapcore.Int64Type { // zap.Int 底层是 Int64Type
		t.Fatalf("int 字段类型错误：%v", zf[1].Type)
	}
	if zf[3].Type != zapcore.BoolType {
		t.Fatalf("bool 字段类型错误：%v", zf[3].Type)
	}
	if zf[4].Key != "error" {
		t.Fatalf("error 字段 key 错误：%q", zf[4].Key)
	}
}

func TestInitZap_SuccessWithFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	cfg := LogConfig{
		Level: "info",
		File: FileConfig{
			Enabled: true, Path: logPath,
			MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 1, Compress: false,
		},
	}
	withGlobal(t, &recordingLogger{}) // 隔离 global，InitZap 会覆盖它

	zl, err := InitZap(cfg)
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	if zl == nil {
		t.Fatal("返回 nil logger")
	}
	_ = zl.Sync()

	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("日志文件未创建：%v", err)
	}
}

func TestInitZap_FileNotWritable_DegradesToStderrOnly(t *testing.T) {
	rec := &recordingLogger{}
	withGlobal(t, rec)

	// 路径中间用一个【文件】作为目录，触发不可写（跨平台可靠）
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup 失败：%v", err)
	}

	cfg := LogConfig{
		Level: "info",
		File:  FileConfig{Enabled: true, Path: filepath.Join(blocker, "app.log")},
	}
	zl, err := InitZap(cfg)
	if err != nil {
		t.Fatalf("文件不可写应降级而非报错：%v", err)
	}
	if zl == nil {
		t.Fatal("降级后仍应返回 logger")
	}
	_ = zl.Sync()

	// 降级路径会用包级 Error 记录一条降级信息（此时 global 仍是 rec）
	found := false
	for _, e := range rec.entries {
		if e.level == "error" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("文件不可写时应记录一条 error 降级日志")
	}
}

func TestInitZap_InvalidLevel_ReturnsError(t *testing.T) {
	withGlobal(t, &recordingLogger{})
	_, err := InitZap(LogConfig{Level: "totally-bogus"})
	if err == nil {
		t.Fatal("非法级别应返回错误")
	}
}

func TestInitZap_FileDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := LogConfig{
		Level: "debug",
		File:  FileConfig{Enabled: false, Path: filepath.Join(dir, "should-not-exist.log")},
	}
	withGlobal(t, &recordingLogger{})

	zl, err := InitZap(cfg)
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	_ = zl.Sync()

	if _, err := os.Stat(cfg.File.Path); !os.IsNotExist(err) {
		t.Fatalf("文件不应被创建：%v", err)
	}
}

func TestInitZap_ReplacesGlobal(t *testing.T) {
	cfg := LogConfig{Level: "info", File: FileConfig{Enabled: false}}
	withGlobal(t, &recordingLogger{})

	zl, _ := InitZap(cfg)
	_ = zl.Sync()

	if _, ok := global.(*zapLogger); !ok {
		t.Fatalf("InitZap 成功后 global 应为 *zapLogger，got %T", global)
	}
}

// InitZap 必须返回 *zap.Logger，供 gin 中间件等外部组件直接使用。
// 编译期断言：若返回类型改变，编译失败。
func TestInitZap_ReturnsConcreteZapLogger(t *testing.T) {
	withGlobal(t, &recordingLogger{})
	zl, err := InitZap(LogConfig{Level: "info", File: FileConfig{Enabled: false}})
	if err != nil {
		t.Fatalf("未期望错误：%v", err)
	}
	var _ *zap.Logger = zl // 类型契约
	_ = zl.Sync()
}
