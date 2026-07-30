// Package server 实现 lumina-relay 的 HTTP 服务编排：
// 路由组装 → net.Listen → http.Server.Serve → ctx.Done 触发优雅 Shutdown。
//
// 本包不直接依赖任何业务 service；依赖经 handler.Deps 注入。
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"lumina-relay/internal/config"
	"lumina-relay/internal/handler"
)

// shutdownTimeout 是优雅关闭时等待在途请求结束的最大时长。
// 与 sync-design §5.6 优雅退出 5s 超时对齐。
const (
	shutdownTimeout   = 5 * time.Second
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 64 << 10
)

// 监听地址在 Serve 前通过 net.Listen 确定，存此包级变量供测试/运维读取。
var (
	addrMu sync.RWMutex
	addr   string
)

// Run 启动 HTTP 服务，在 ctx 取消时优雅关闭。
//
// 流程：构造路由 → net.Listen（确定实际端口，支持 :0）→
// goroutine 内 Serve → ctx.Done 触发 Shutdown（带超时）。
//
// 返回 nil（正常关闭）或 http.ErrServerClosed（Serve 在 Shutdown 后返回），
// 其他错误（端口占用等）原样返回。
func Run(ctx context.Context, cfg config.AppConfig, deps handler.Deps) error {
	router := handler.NewRouter(deps)

	listenAddr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	addrMu.Lock()
	addr = ""
	addrMu.Unlock()
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("监听 %s：%w", listenAddr, err)
	}

	addrMu.Lock()
	addr = ln.Addr().String()
	addrMu.Unlock()

	srv := newHTTPServer(router)

	serveErr := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		// Shutdown 后 Serve 返回 ErrServerClosed，属正常关闭。
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		// Serve 在被 Shutdown 前就退出（端口错误等）。
		return err
	case <-ctx.Done():
		// 优雅关闭：给在途请求最多 shutdownTimeout 收尾。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("优雅关闭：%w", err)
		}
		// 等 Serve goroutine 退出。
		return <-serveErr
	}
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// Addr 返回当前（或上一次）服务的监听地址。
// Port=0 时由 OS 分配端口，测试通过它在服务起来后读取实际端口。
func Addr() string {
	addrMu.RLock()
	defer addrMu.RUnlock()
	return addr
}
