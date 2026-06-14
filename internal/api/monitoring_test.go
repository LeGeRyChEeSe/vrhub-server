package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/monitor"
)

func TestHandleMonitoringSSE_RequiresPowerMode(t *testing.T) {
	h := &AdminHandler{MonitorBus: monitor.NewEventBus()}

	req := httptest.NewRequest(http.MethodGet, "/admin/monitoring", nil)
	// No vrhub-mode cookie → Michel mode (or unauth) → 404
	rr := httptest.NewRecorder()
	h.HandleMonitoringSSE(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 in Michel mode, got %d", rr.Code)
	}
}

func TestHandleMonitoringSSE_PowerMode_StreamsEvents(t *testing.T) {
	bus := monitor.NewEventBus()
	h := &AdminHandler{MonitorBus: bus}

	req := httptest.NewRequest(http.MethodGet, "/admin/monitoring", nil)
	req.AddCookie(&http.Cookie{Name: "vrhub-mode", Value: "power"})

	// Use a context that cancels after a short time so the handler returns.
	ctx, cancel := context.WithTimeout(req.Context(), 500*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.HandleMonitoringSSE(rr, req)
		close(done)
	}()

	// Publish an event after a brief delay.
	time.Sleep(50 * time.Millisecond)
	bus.Publish(monitor.Event{Type: "test", Data: "hello"})

	// Wait for the handler to return.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after context cancel")
	}

	body := rr.Body.String()
	// M-09 (review 2026-06-11): writeSSE no longer prefixes with
	// `event:` — the client uses onmessage which only fires for
	// messages WITHOUT a custom event name. The event type is now
	// inside the JSON payload's `type` field. Assert both that the
	// id: line is present AND that the JSON payload carries the
	// event type.
	if !strings.Contains(body, "id:") {
		t.Errorf("expected 'id:' line in SSE body, got: %s", body)
	}
	if !strings.Contains(body, `"type":"hello"`) {
		t.Errorf("expected 'type:hello' in JSON payload, got: %s", body)
	}
	if strings.Contains(body, "\nevent:") {
		t.Errorf("'event:' prefix should NOT be present (was removed in M-09), got: %s", body)
	}
}

func TestHandleMonitoringSSE_NoBus_ReturnsErrorEvent(t *testing.T) {
	h := &AdminHandler{MonitorBus: nil} // explicitly nil

	req := httptest.NewRequest(http.MethodGet, "/admin/monitoring", nil)
	req.AddCookie(&http.Cookie{Name: "vrhub-mode", Value: "power"})

	ctx, cancel := context.WithTimeout(req.Context(), 200*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.HandleMonitoringSSE(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "monitor bus not configured") {
		t.Errorf("expected 'monitor bus not configured' event, got: %s", body)
	}
}

// Sanity: the SSE wire format we write can be parsed back line by line.
func TestWriteSSE_Format(t *testing.T) {
	rr := httptest.NewRecorder()
	ev := monitor.Event{ID: 42, Type: "ping", Data: map[string]string{"k": "v"}}
	if err := writeSSE(rr, ev); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(strings.NewReader(rr.Body.String()))
	lines := []string{}
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	// M-09 (review 2026-06-11): the `event:` line was removed so the
	// browser's EventSource.onmessage handler can fire for every
	// event. Only id: and data: lines remain. The event type lives
	// inside the JSON payload.
	wantPrefixes := []string{"id: 42", "data: {"}
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d: %v", len(lines), lines)
	}
	for i, p := range wantPrefixes {
		if !strings.HasPrefix(lines[i], p) {
			t.Errorf("line %d: got %q, want prefix %q", i, lines[i], p)
		}
	}
	// The event type ("ping") must still be in the JSON payload so
	// consumers can discriminate.
	if !strings.Contains(rr.Body.String(), `"type":"ping"`) {
		t.Errorf("expected type:ping in JSON payload, got: %s", rr.Body.String())
	}
}
