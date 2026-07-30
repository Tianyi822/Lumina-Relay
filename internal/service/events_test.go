package service

import (
	"testing"
	"time"
)

func TestEventTicketIsSingleUse(t *testing.T) {
	store := NewEventTicketStore()
	value, _, err := store.Issue("account", "device", "group")
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := store.Consume(value)
	if err != nil || ticket.DeviceID != "device" {
		t.Fatalf("ticket=%+v err=%v", ticket, err)
	}
	if _, err := store.Consume(value); err == nil {
		t.Fatal("ticket 重放应失败")
	}
}

func TestEventHubTargetsOnlyNamedDevices(t *testing.T) {
	hub := NewEventHub()
	aEvents, cancelA := hub.Subscribe("A")
	defer cancelA()
	bEvents, cancelB := hub.Subscribe("B")
	defer cancelB()
	hub.Publish([]string{"A"}, Event{Type: "manifest_updated"})
	select {
	case event := <-aEvents:
		if event.Type != "manifest_updated" {
			t.Fatalf("event=%+v", event)
		}
	default:
		t.Fatal("A 未收到事件")
	}
	select {
	case event := <-bEvents:
		t.Fatalf("B 不应收到事件：%+v", event)
	default:
	}
}

func TestExpiredEventTicketIsRejected(t *testing.T) {
	store := NewEventTicketStore()
	now := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return now }
	value, _, err := store.Issue("account", "device", "group")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * time.Second)
	if _, err := store.Consume(value); err == nil {
		t.Fatal("过期 ticket 不应被接受")
	}
}

func TestEventHubDisconnectsSlowConsumer(t *testing.T) {
	hub := NewEventHub()
	events, cancel := hub.Subscribe("slow")
	defer cancel()
	for version := int64(1); version <= 33; version++ {
		hub.Publish([]string{"slow"}, Event{
			Type: "manifest_updated", Version: version,
		})
	}
	for range 32 {
		if _, ok := <-events; !ok {
			t.Fatal("队列中的既有事件不应提前丢失")
		}
	}
	if _, ok := <-events; ok {
		t.Fatal("慢消费者队列溢出后应被关闭")
	}
}
