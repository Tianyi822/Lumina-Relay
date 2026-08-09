package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
	"lumina-relay/internal/logger"
	"lumina-relay/internal/middleware"
	"lumina-relay/internal/service"
)

func CreateEventTicket(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ticket, expiresAtMS, err := deps.EventTickets.Issue(
			c.GetString(middleware.CtxAccountID),
			c.GetString(middleware.CtxDeviceID),
			c.GetString(middleware.CtxGroupID),
		)
		if err != nil {
			writeAPIError(c, apperr.New(apperr.CodeInternalError, "生成事件票据失败"))
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"ticket": ticket, "expiresAtMs": expiresAtMS,
			"subprotocol": "lumina-events",
		})
	}
}

// writeEvent 以带超时的 ctx 写一个事件帧。写阻塞超过 timeout（客户端停止
// 读取、内核发送缓冲被填满）时返回错误，由调用方关闭连接。没有该超时，
// 半开连接会让事件循环无限卡死在写调用上，select 循环里的心跳与慢消费者
// 保护同时失效，连接与 goroutine 泄漏直到内核 TCP keepalive 介入。
func writeEvent(ctx context.Context, connection *websocket.Conn, event any, timeout time.Duration) error {
	writeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return wsjson.Write(writeCtx, connection, event)
}

func Events(deps Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ticketValue := eventTicketFromSubprotocol(c.GetHeader("Sec-WebSocket-Protocol"))
		ticket, err := deps.EventTickets.Consume(ticketValue)
		if err != nil {
			writeAPIError(c, apperr.New(
				apperr.CodeInvalidDeviceProof, "事件票据无效"))
			return
		}
		device, err := deps.Queries.GetDevice(c.Request.Context(), ticket.DeviceID)
		if err != nil || device.Status != "active" || !device.SyncGroupID.Valid ||
			device.AccountID != ticket.AccountID ||
			device.SyncGroupID.String != ticket.GroupID {
			writeAPIError(c, apperr.New(
				apperr.CodeInvalidDeviceProof, "事件票据无效"))
			return
		}
		connection, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
			Subprotocols: []string{"lumina-events"},
		})
		if err != nil {
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "")
		connection.SetReadLimit(4 << 10)

		events, cancel, err := deps.EventHub.Subscribe(ticket.DeviceID)
		if err != nil {
			// 每设备并发订阅超限：以 1013 提示客户端稍后重试（防资源耗尽）。
			closeCode := websocket.StatusInternalError
			if errors.Is(err, service.ErrTooManySubscriptions) {
				closeCode = websocket.StatusTryAgainLater
			}
			_ = connection.Close(closeCode, "subscribe failed")
			return
		}
		defer cancel()
		ctx, stop := context.WithCancel(c.Request.Context())
		defer stop()
		readDone := make(chan error, 1)
		go func() {
			// 子 goroutine panic 不拖垮进程：记录后通过 readDone 通知主循环
			// 关闭连接（否则主循环会一直等 readDone 造成泄漏）。
			defer func() {
				if r := recover(); r != nil {
					logger.Error("WS 读循环 panic",
						logger.Any("panic", r),
						logger.String("stack", string(debug.Stack())),
					)
					readDone <- fmt.Errorf("websocket read panic: %v", r)
				}
			}()
			for {
				if _, _, err := connection.Read(ctx); err != nil {
					readDone <- err
					return
				}
			}
		}()

		group, err := deps.Queries.GetSyncGroup(ctx, ticket.GroupID)
		if err != nil {
			return
		}
		if err := writeEvent(ctx, connection, service.Event{
			Type: "ready", GroupRevision: group.Revision,
			ServerTimeMS: time.Now().UnixMilli(),
		}, deps.WSWriteTimeout); err != nil {
			return
		}
		heartbeat := time.NewTicker(30 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					_ = connection.Close(websocket.StatusPolicyViolation, "slow consumer")
					return
				}
				if err := writeEvent(ctx, connection, event, deps.WSWriteTimeout); err != nil {
					return
				}
			case <-heartbeat.C:
				pingCtx, cancelPing := context.WithTimeout(ctx, 5*time.Second)
				err := connection.Ping(pingCtx)
				cancelPing()
				if err != nil {
					return
				}
			case <-readDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}
}

func eventTicketFromSubprotocol(value string) string {
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "ticket.") {
			return strings.TrimPrefix(part, "ticket.")
		}
	}
	return ""
}
