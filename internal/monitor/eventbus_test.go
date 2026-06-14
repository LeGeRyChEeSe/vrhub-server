package monitor

import (
	"testing"
	"time"
)

func TestEventBus_PublishReceive(t *testing.T) {
	bus := NewEventBus()
	sub := bus.Subscribe()
	defer sub.Unsubscribe()

	bus.Publish(Event{Type: "test", Data: "hello"})

	select {
	case ev := <-sub.Events:
		if ev.Type != "test" {
			t.Errorf("type: got %q, want test", ev.Type)
		}
		if ev.ID == 0 {
			t.Error("ID should be assigned by Publish")
		}
		if ev.Time.IsZero() {
			t.Error("Time should be set by Publish")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("did not receive published event")
	}
}

func TestEventBus_MultipleSubscribers_AllReceive(t *testing.T) {
	bus := NewEventBus()
	s1 := bus.Subscribe()
	s2 := bus.Subscribe()
	s3 := bus.Subscribe()
	defer s1.Unsubscribe()
	defer s2.Unsubscribe()
	defer s3.Unsubscribe()

	bus.Publish(Event{Type: "broadcast", Data: 42})

	for i, s := range []*Subscription{s1, s2, s3} {
		select {
		case ev := <-s.Events:
			if ev.Type != "broadcast" {
				t.Errorf("sub %d: type: got %q, want broadcast", i, ev.Type)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("sub %d: did not receive event", i)
		}
	}
}

func TestEventBus_Unsubscribe_NoMoreEvents(t *testing.T) {
	bus := NewEventBus()
	sub := bus.Subscribe()
	sub.Unsubscribe()

	bus.Publish(Event{Type: "after"})

	// Channel should be closed (or at least no event sent); we check
	// that the bus no longer has the sub.
	if bus.SubscriberCount() != 0 {
		t.Errorf("SubscriberCount: got %d, want 0", bus.SubscriberCount())
	}
}

func TestEventBus_SlowSubscriberDropsEvents(t *testing.T) {
	bus := NewEventBus()
	ResetDroppedEvents()
	sub := bus.Subscribe()
	defer sub.Unsubscribe()

	// Channel buffer is 64; publish 100 events without reading.
	for i := 0; i < 100; i++ {
		bus.Publish(Event{Type: "flood"})
	}

	// Some events were dropped.
	if DroppedEvents() == 0 {
		t.Error("expected some events to be dropped for slow subscriber")
	}
	// Some events are still in the channel (at most 64).
	received := 0
Drain:
	for {
		select {
		case <-sub.Events:
			received++
		default:
			break Drain
		}
	}
	if received > subBufferSize {
		t.Errorf("received %d events, channel buffer is %d", received, subBufferSize)
	}
}

func TestEventBus_PreservesProvidedTime(t *testing.T) {
	bus := NewEventBus()
	sub := bus.Subscribe()
	defer sub.Unsubscribe()

	when := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	bus.Publish(Event{Type: "stamped", Time: when})

	select {
	case ev := <-sub.Events:
		if !ev.Time.Equal(when) {
			t.Errorf("Time: got %v, want %v", ev.Time, when)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("did not receive event")
	}
}

func TestEventBus_IDsMonotonic(t *testing.T) {
	bus := NewEventBus()
	sub := bus.Subscribe()
	defer sub.Unsubscribe()

	for i := 0; i < 5; i++ {
		bus.Publish(Event{Type: "seq"})
	}

	var last uint64
	for i := 0; i < 5; i++ {
		select {
		case ev := <-sub.Events:
			if ev.ID <= last {
				t.Errorf("ID %d not strictly greater than previous %d", ev.ID, last)
			}
			last = ev.ID
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("missing event %d", i)
		}
	}
}
