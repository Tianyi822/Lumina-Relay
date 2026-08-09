package service

import (
	"errors"
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
	aEvents, cancelA, err := hub.Subscribe("A")
	if err != nil {
		t.Fatal(err)
	}
	defer cancelA()
	bEvents, cancelB, err := hub.Subscribe("B")
	if err != nil {
		t.Fatal(err)
	}
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
	events, cancel, err := hub.Subscribe("slow")
	if err != nil {
		t.Fatal(err)
	}
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

// TestEventHubSubscriptionLimit 验证单设备并发订阅数达到上限后，后续订阅
// 被拒绝（防已认证设备无限开 WebSocket 连接耗尽进程资源）。
func TestEventHubSubscriptionLimit(t *testing.T) {
	hub := NewEventHub()
	var cancels []func()
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	for range MaxSubscriptionsPerDevice {
		_, cancel, err := hub.Subscribe("device")
		if err != nil {
			t.Fatalf("前 %d 次订阅应成功：%v", MaxSubscriptionsPerDevice, err)
		}
		cancels = append(cancels, cancel)
	}
	if _, _, err := hub.Subscribe("device"); !errors.Is(err, ErrTooManySubscriptions) {
		t.Fatalf("超限订阅应返回 ErrTooManySubscriptions，得到 %v", err)
	}
	// 其他设备的订阅不受本设备上限影响。
	if _, cancel, err := hub.Subscribe("other"); err != nil {
		t.Fatalf("其他设备订阅不应受影响：%v", err)
	} else {
		cancel()
	}
	// 退订一个后应能重新订阅（上限按当前活跃订阅数计）。
	cancels[0]()
	_, cancel, err := hub.Subscribe("device")
	if err != nil {
		t.Fatalf("退订后重订应成功：%v", err)
	}
	cancel()
}
