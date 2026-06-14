package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/monitor"
)

func TestMonitorMiddleware_PublishesHTTPEvent(t *testing.T) {
	bus := monitor.NewEventBus()
	sub := bus.Subscribe()
	defer sub.Unsubscribe()

	mw := MonitorMiddleware(bus)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest(http.MethodPost, "/foo", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	select {
	case ev := <-sub.Events:
		if ev.Type != "http" {
			t.Errorf("type: got %q, want http", ev.Type)
		}
		data, ok := ev.Data.(map[string]any)
		if !ok {
			t.Fatalf("data not a map: %T", ev.Data)
		}
		if data["method"] != "POST" {
			t.Errorf("method: got %v, want POST", data["method"])
		}
		if data["path"] != "/foo" {
			t.Errorf("path: got %v, want /foo", data["path"])
		}
		if data["status"] != http.StatusTeapot {
			t.Errorf("status: got %v, want %d", data["status"], http.StatusTeapot)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("did not receive event")
	}
}

func TestMonitorMiddleware_NilBus_NoOp(t *testing.T) {
	mw := MonitorMiddleware(nil)
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !called {
		t.Error("downstream handler not invoked when bus is nil")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
}

func TestMonitorMiddleware_DefaultStatusOK(t *testing.T) {
	bus := monitor.NewEventBus()
	sub := bus.Subscribe()
	defer sub.Unsubscribe()

	mw := MonitorMiddleware(bus)
	// Handler that writes body but never calls WriteHeader explicitly.
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/implicit", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	select {
	case ev := <-sub.Events:
		data := ev.Data.(map[string]any)
		if data["status"] != http.StatusOK {
			t.Errorf("implicit 200 not captured: got %v", data["status"])
		}
		if data["bytes"] != 2 {
			t.Errorf("bytes: got %v, want 2", data["bytes"])
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("did not receive event")
	}
}
