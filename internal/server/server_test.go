package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"lumina-relay/internal/config"
	"lumina-relay/internal/handler"
)

// TestRun_ShutdownOnContextCancel 验证：
// 1) Run 启动后能对外提供 /health（200）；
// 2) ctx 取消后 Run 在合理时间内返回（不永久阻塞）。
//
// 用 :0 让 OS 分配空闲端口，避免测试间端口冲突。
func TestRun_ShutdownOnContextCancel(t *testing.T) {
	cfg := config.Default()
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 0 // :0 = 随机端口

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, cfg, handler.Deps{})
	}()

	// 给服务一点时间启动。Run 内部应暴露实际监听地址；
	// 这里通过轮询 /health 判断就绪。
	addr := waitForListenAddr(t)
	url := "http://" + addr + "/health"
	if !waitForOK(t, url, 2*time.Second) {
		t.Fatalf("服务未在 %s 起来", addr)
	}

	// 触发优雅关闭
	cancel()

	select {
	case err := <-runDone:
		// 允许 ErrServerClosed（正常关闭）或 nil
		if err != nil && err != http.ErrServerClosed {
			t.Fatalf("Run 返回非预期错误：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 Run 未在 3s 内返回")
	}
}

// waitForListenAddr 轮询实际监听地址。
// 由于 Port=0 由 OS 分配，Run 必须把真实地址暴露出来（Addr()）。
func waitForListenAddr(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a := Addr(); a != "" {
			return a
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Run 未在 2s 内暴露监听地址")
	return ""
}

// waitForOK 轮询 url 直到返回 200 或超时。
func waitForOK(t *testing.T, url string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
