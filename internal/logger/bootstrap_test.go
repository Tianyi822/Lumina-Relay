package logger

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestBootstrapLoggerWritesFieldsToText(t *testing.T) {
	var buf bytes.Buffer
	bl := &bootstrapLogger{sl: slog.New(slog.NewTextHandler(&buf, nil))}

	bl.Info("hello", String("user", "alice"), Int("count", 3))

	out := buf.String()
	if !strings.Contains(out, "hello") {
		t.Fatalf("输出缺少消息：%s", out)
	}
	if !strings.Contains(out, "user=alice") {
		t.Fatalf("输出缺少 string 字段：%s", out)
	}
	if !strings.Contains(out, "count=3") {
		t.Fatalf("输出缺少 int 字段：%s", out)
	}
}

func TestBootstrapLoggerAllLevels(t *testing.T) {
	var buf bytes.Buffer
	bl := &bootstrapLogger{sl: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}

	bl.Debug("d")
	bl.Info("i")
	bl.Warn("w")
	bl.Error("e")

	out := buf.String()
	for _, want := range []string{"d", "i", "w", "e"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q：%s", want, out)
		}
	}
}

func TestBootstrapLoggerWithReturnsLogger(t *testing.T) {
	var buf bytes.Buffer
	bl := &bootstrapLogger{sl: slog.New(slog.NewTextHandler(&buf, nil))}
	child := bl.With(String("req", "r1"))
	if _, ok := child.(*bootstrapLogger); !ok {
		t.Fatalf("With 应返回 *bootstrapLogger，got %T", child)
	}
	child.Info("child-msg")
	if !strings.Contains(buf.String(), "child-msg") {
		t.Fatalf("子 logger 未写入：%s", buf.String())
	}
}

func TestBootstrapLoggerSyncIsNoop(t *testing.T) {
	bl := &bootstrapLogger{sl: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	if err := bl.Sync(); err != nil {
		t.Fatalf("Sync 应返回 nil，got %v", err)
	}
}

// 幂等测试【自洽】：不假设调用前 global 的状态（其他测试或 TestMain 可能已触发
// sync.Once）。只验证：调用后 global 非 nil 且类型正确，重复调用不 panic。
func TestInitBootstrap_SetsGlobalAndIsIdempotent(t *testing.T) {
	InitBootstrap()
	InitBootstrap() // 重复调用应幂等，不 panic

	if global == nil {
		t.Fatal("InitBootstrap 后 global 不应为 nil")
	}
	if _, ok := global.(*bootstrapLogger); !ok {
		t.Fatalf("global 应为 *bootstrapLogger，got %T", global)
	}
}

func TestInitBootstrap_SetsFallback(t *testing.T) {
	// 隔离 fallback：保存/恢复，验证 InitBootstrap 把 fallback 指向 bootstrapLogger。
	// 注意 sync.Once 是包级单例：若 once 已执行，fallback 已被赋值，这里只断言类型。
	prev := fallback
	t.Cleanup(func() { fallback = prev })

	InitBootstrap()
	if fallback == nil {
		t.Fatal("InitBootstrap 后 fallback 不应为 nil")
	}
	if _, ok := fallback.(*bootstrapLogger); !ok {
		t.Fatalf("fallback 应为 *bootstrapLogger，got %T", fallback)
	}
}

func TestToSlogArgs(t *testing.T) {
	fields := []Field{String("a", "1"), Int("b", 2), Bool("c", true)}
	args := toSlogArgs(fields)
	if len(args) != len(fields)*2 {
		t.Fatalf("args 长度 = %d, want %d", len(args), len(fields)*2)
	}
	if args[0] != "a" || args[1] != "1" {
		t.Fatalf("首个字段映射错误：%v", args[:2])
	}
}
