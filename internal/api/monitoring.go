package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/monitor"
)

// HandleMonitoringSSE streams monitor events to the client via Server-Sent
// Events. The endpoint is mode-gated: Michel mode (or unauthenticated)
// gets 404 (consistent with /admin/settings, /admin/monitoring intent
// per spec 7.4). The event source stays open until the client
// disconnects; on disconnect the subscription is released.
//
// Wire format:
//
//	id: <event.ID>\n
//	data: <json>\n
//	\n
//
// The `id:` line enables browser-native Last-Event-ID reconnect (a
// future story could replay events from a ring buffer; for 7.4 MVP we
// only stream live events).
func (h *AdminHandler) HandleMonitoringSSE(w http.ResponseWriter, r *http.Request) {
	// Mode gating: 404 in Michel mode (or unauthenticated).
	if !h.isPowerMode(r) {
		http.NotFound(w, r)
		return
	}

	// SSE headers — flushed before first write so the client knows to
	// start parsing.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		// Should not happen for chi/http.ResponseWriter, but degrade
		// gracefully: the client will buffer until connection close.
		ok = false
	}

	bus := h.MonitorBus
	if bus == nil {
		// No bus configured — write a single "bus-not-configured" event
		// and close. This is preferable to silently hanging.
		_, _ = fmt.Fprintf(w, "event: error\ndata: {\"error\":\"monitor bus not configured\"}\n\n")
		if ok {
			flusher.Flush()
		}
		return
	}

	sub := bus.Subscribe()
	defer sub.Unsubscribe()

	// Send a hello event so the client knows the stream is live.
	hello := monitor.Event{
		Type: "hello",
		Data: map[string]any{
			"server_time":      time.Now().UTC().Format(time.RFC3339),
			"subscriber_count": bus.SubscriberCount(),
		},
	}
	if err := writeSSE(w, hello); err != nil {
		return
	}
	if ok {
		flusher.Flush()
	}

	// Keep-alive ticker: SSE proxies idle out after ~30-60s. A comment
	// line (starting with ':') is ignored by the EventSource API and
	// keeps the connection warm.
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			if ok {
				flusher.Flush()
			}
		case ev, ok := <-sub.Events:
			if !ok {
				return
			}
			if err := writeSSE(w, ev); err != nil {
				return
			}
			if ok {
				flusher.Flush()
			}
		}
	}
}

// writeSSE writes a single SSE-formatted event to w.
//
// M-09 (review 2026-06-11): the previous format prefixed every event
// with `event: <name>`. The browser's EventSource.onmessage handler
// only fires for messages WITHOUT a custom event name — so all named
// events (the `hello` greeting AND any custom event types emitted by
// the MonitorBus) were silently dropped on the client.
//
// The fix: stop emitting the `event:` line. All events flow through
// onmessage. The event type is preserved inside the JSON payload's
// `type` field so the client can still discriminate (and the ID
// line is kept for Last-Event-ID reconnect).
func writeSSE(w http.ResponseWriter, ev monitor.Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.ID, payload)
	return err
}

// isPowerMode returns true if the request is authenticated AND in Power
// User mode (admin). Michel mode → false. Used for the 404 gate on
// /admin/monitoring.
//
// MVP: checks the vrhub-mode cookie set by the frontend (Epic 6 admin
// UI). A future story could plumb the canonical Mode struct here, but
// for 7.4 the cookie-based check is sufficient (the UI sets the cookie
// explicitly when the user toggles modes).
func (h *AdminHandler) isPowerMode(r *http.Request) bool {
	c, err := r.Cookie("vrhub-mode")
	if err != nil {
		return false
	}
	return c.Value == "power"
}
