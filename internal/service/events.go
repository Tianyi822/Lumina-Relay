package service

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

type Event struct {
	Type          string `json:"type"`
	DeviceID      string `json:"deviceId,omitempty"`
	SessionID     string `json:"sessionId,omitempty"`
	Version       int64  `json:"version,omitempty"`
	GroupRevision int64  `json:"groupRevision,omitempty"`
	ServerTimeMS  int64  `json:"serverTimeMs"`
}

type subscriber struct {
	deviceID string
	events   chan Event
	closed   bool
}

// EventHub 是单实例 Relay 的有界内存通知总线；数据库仍是事实来源。
type EventHub struct {
	mu          sync.Mutex
	subscribers map[string]map[*subscriber]struct{}
}

func NewEventHub() *EventHub {
	return &EventHub{subscribers: make(map[string]map[*subscriber]struct{})}
}

func (h *EventHub) Subscribe(deviceID string) (<-chan Event, func()) {
	sub := &subscriber{deviceID: deviceID, events: make(chan Event, 32)}
	h.mu.Lock()
	if h.subscribers[deviceID] == nil {
		h.subscribers[deviceID] = make(map[*subscriber]struct{})
	}
	h.subscribers[deviceID][sub] = struct{}{}
	h.mu.Unlock()
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.closeSubscriberLocked(sub)
	}
	return sub.events, cancel
}

func (h *EventHub) Publish(deviceIDs []string, event Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, deviceID := range deviceIDs {
		for sub := range h.subscribers[deviceID] {
			select {
			case sub.events <- event:
			default:
				// 慢消费者断开，由客户端按 group revision 补拉。
				h.closeSubscriberLocked(sub)
			}
		}
	}
}

func (h *EventHub) closeSubscriberLocked(sub *subscriber) {
	if sub.closed {
		return
	}
	sub.closed = true
	delete(h.subscribers[sub.deviceID], sub)
	if len(h.subscribers[sub.deviceID]) == 0 {
		delete(h.subscribers, sub.deviceID)
	}
	close(sub.events)
}

type EventTicket struct {
	AccountID string
	DeviceID  string
	GroupID   string
	ExpiresAt time.Time
}

var ErrInvalidEventTicket = errors.New("invalid event ticket")

type EventTicketStore struct {
	mu      sync.Mutex
	now     func() time.Time
	tickets map[string]EventTicket
}

func NewEventTicketStore() *EventTicketStore {
	return &EventTicketStore{now: time.Now, tickets: make(map[string]EventTicket)}
}

func (s *EventTicketStore) Issue(accountID, deviceID, groupID string) (string, int64, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, err
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	expires := s.now().Add(30 * time.Second)
	s.mu.Lock()
	for key, ticket := range s.tickets {
		if !ticket.ExpiresAt.After(s.now()) {
			delete(s.tickets, key)
		}
	}
	s.tickets[value] = EventTicket{
		AccountID: accountID, DeviceID: deviceID, GroupID: groupID,
		ExpiresAt: expires,
	}
	s.mu.Unlock()
	return value, expires.UnixMilli(), nil
}

func (s *EventTicketStore) Consume(value string) (EventTicket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.tickets[value]
	delete(s.tickets, value)
	if !ok || !ticket.ExpiresAt.After(s.now()) {
		return EventTicket{}, ErrInvalidEventTicket
	}
	return ticket, nil
}
