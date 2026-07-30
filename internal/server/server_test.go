package server

import (
	"context"
	"net/http"
	"strings"
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
	addr := waitForListenAddr(t, runDone)
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

// TestNewHTTPServerTimeouts 验证外部 HTTP server 同时限制 header、完整读取、
// 响应写入和 keep-alive idle，不能只防 slow header。
func TestNewHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer(http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 ||
		srv.ReadTimeout <= 0 ||
		srv.WriteTimeout <= 0 ||
		srv.IdleTimeout <= 0 ||
		srv.MaxHeaderBytes != maxHeaderBytes {
		t.Fatalf(
			"HTTP 限制未完整配置：readHeader=%s read=%s write=%s idle=%s headers=%d",
			srv.ReadHeaderTimeout,
			srv.ReadTimeout,
			srv.WriteTimeout,
			srv.IdleTimeout,
			srv.MaxHeaderBytes,
		)
	}
}

// waitForListenAddr 轮询实际监听地址。
// 由于 Port=0 由 OS 分配，Run 必须把真实地址暴露出来（Addr()）。
func waitForListenAddr(t *testing.T, runDone <-chan error) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a := Addr(); a != "" {
			return a
		}
		select {
		case err := <-runDone:
			if err != nil && strings.Contains(err.Error(), "operation not permitted") {
				t.Skipf("当前沙箱不允许 net.Listen：%v", err)
			}
			t.Fatalf("Run 在监听前退出：%v", err)
		default:
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
