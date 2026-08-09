package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestWriteEventStallReturnsError 锁定 H2 核心修复：写调用阻塞超过 timeout
// 时，writeEvent 必须返回错误并让连接被关闭，而不是无限挂起（半开连接
// 泄漏）。构造方式：客户端连上后不读取，服务端写入远超内核发送缓冲的
// payload（8MiB → base64 ≈ 11MiB），写必然阻塞，随后 ctx 超时触发
// coder/websocket 关闭连接。若去掉 writeEvent 内部的 WithTimeout，本测试
// 会因 writeEvent 永久挂起而超时失败。
func TestWriteEventStallReturnsError(t *testing.T) {
	acceptDone := make(chan *websocket.Conn, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			close(acceptDone)
			return
		}
		acceptDone <- conn
		<-r.Context().Done() // 挂起 handler，保持连接直到服务关闭
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, _, err := websocket.Dial(context.Background(),
		"ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(websocket.StatusNormalClosure, "")
	serverConn := <-acceptDone
	// 客户端不启动读取 goroutine：服务端写最终被内核发送缓冲卡住。

	event := map[string]any{"payload": make([]byte, 8<<20)} // 8MiB
	done := make(chan error, 1)
	go func() {
		done <- writeEvent(context.Background(), serverConn, event, 300*time.Millisecond)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("写阻塞后 writeEvent 应返回错误")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writeEvent 未在写超时内返回（连接永久挂起）")
	}

	// 写超时后连接应已被 coder/websocket 关闭（ctx AfterFunc 关闭底层连接）。
	if _, _, err := serverConn.Read(context.Background()); err == nil {
		t.Fatal("写超时后服务端连接未被关闭")
	}
}
