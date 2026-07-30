package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/gin-gonic/gin"

	"lumina-relay/internal/apperr"
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

		events, cancel := deps.EventHub.Subscribe(ticket.DeviceID)
		defer cancel()
		ctx, stop := context.WithCancel(c.Request.Context())
		defer stop()
		readDone := make(chan error, 1)
		go func() {
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
		if err := wsjson.Write(ctx, connection, service.Event{
			Type: "ready", GroupRevision: group.Revision,
			ServerTimeMS: time.Now().UnixMilli(),
		}); err != nil {
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
				if err := wsjson.Write(ctx, connection, event); err != nil {
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
