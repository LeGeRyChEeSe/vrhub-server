// Package monitor implements a simple in-memory event bus for the admin
// monitoring dashboard (Story 7.4). Events are broadcast to all
// subscribers via buffered channels; slow subscribers drop events rather
// than block the publisher.
//
// Scope: single-process, in-memory. No persistence, no replay. Subscribers
// that connect after an event is published do NOT see it (use the
// Last-Event-ID header on SSE reconnect for any future replay story).
package monitor

import (
	"sync"
	"sync/atomic"
	"time"
)

// subBufferSize is the per-subscriber channel buffer. Larger values give
// slow subscribers more headroom but consume memory proportional to the
// number of concurrent subscribers.
const subBufferSize = 64

// Event is the unit published on the bus. ID is a monotonically
// increasing counter assigned at Publish time; Time is wall-clock UTC at
// publish; Type is a short string (e.g. "http", "download", "error")
// that consumers can switch on; Data is the type-specific payload (any).
type Event struct {
	ID   uint64    `json:"id"`
	Time time.Time `json:"time"`
	Type string    `json:"type"`
	Data any       `json:"data,omitempty"`
}

// EventBus is a fan-out broker. Zero value is NOT ready; use NewEventBus.
type EventBus struct {
	mu     sync.RWMutex
	subs   map[*Subscription]struct{}
	nextID atomic.Uint64
}

// Subscription is a subscriber handle. The Events channel receives events
// until Unsubscribe is called or the EventBus is shut down. Channel is
// closed on Unsubscribe so consumers ranging over it can detect cleanup.
type Subscription struct {
	Events chan Event
	id     uint64
	bus    *EventBus
	done   chan struct{}
}

// NewEventBus returns a ready-to-use bus.
func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[*Subscription]struct{})}
}

// Publish assigns an ID + timestamp to e and fans it out to every current
// subscriber. Subscribers whose channel buffer is full DROP the event
// (non-blocking) — this protects the publisher from slow consumers at
// the cost of a brief loss of fidelity in the monitoring feed. Each
// dropped event is recorded in a process-wide counter that operators
// can inspect via debug.
func (b *EventBus) Publish(e Event) {
	e.ID = b.nextID.Add(1)
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.Events <- e:
		default:
			// Slow subscriber — drop. Monitoring is best-effort.
			droppedEvents.Add(1)
		}
	}
}

// Subscribe registers a new subscriber and returns its handle. The
// caller MUST eventually call Unsubscribe to release the channel and
// stop receiving events (typically via defer right after Subscribe).
func (b *EventBus) Subscribe() *Subscription {
	s := &Subscription{
		Events: make(chan Event, subBufferSize),
		bus:    b,
		done:   make(chan struct{}),
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Unsubscribe removes the subscription and closes its channel. Safe to
// call multiple times.
func (s *Subscription) Unsubscribe() {
	if s == nil || s.bus == nil {
		return
	}
	s.bus.mu.Lock()
	if _, ok := s.bus.subs[s]; ok {
		// Remove ourselves from the bus and close both channels while
		// holding the write lock. Publish fans out under a read lock, so
		// once delete() has run no publisher can still be mid-send on
		// s.Events — closing it here cannot race a "send on closed
		// channel". The `ok` guard makes repeated calls a safe no-op.
		//
		// Closing s.Events (not just s.done) honours the documented
		// contract: consumers ranging over Events detect cleanup via a
		// closed channel. The previous implementation deferred the close
		// to a goroutine guarded by `<-s.done`, which — because s.done
		// was already closed two lines above — always took the "already
		// closed" branch and never closed Events at all.
		delete(s.bus.subs, s)
		close(s.done)
		close(s.Events)
	}
	s.bus.mu.Unlock()
}

// Done returns a channel that is closed when the subscription is
// unsubscribed (either by the caller or by the bus shutting down).
// Useful for `select { case <-sub.Done(): ... }` in SSE loops.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// SubscriberCount returns the current number of active subscribers.
// Primarily for tests and operator diagnostics.
func (b *EventBus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// droppedEvents is a process-wide counter of events dropped due to slow
// subscribers. Exposed via DroppedEvents() for operator diagnostics.
var droppedEvents atomic.Uint64

// DroppedEvents returns the cumulative count of events dropped because a
// subscriber's channel buffer was full.
func DroppedEvents() uint64 { return droppedEvents.Load() }

// ResetDroppedEvents is a test helper.
func ResetDroppedEvents() { droppedEvents.Store(0) }
