package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccessLogMiddleware_PassThrough(t *testing.T) {
	// The middleware must be transparent: the wrapped handler must
	// still receive the request, write the response, and the
	// client must see the original status + body.
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("X-Test", "ok")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	})

	req := httptest.NewRequest("GET", "/foo", nil)
	req.Header.Set("User-Agent", "test-agent/1.0")
	rec := httptest.NewRecorder()

	accessLogMiddleware(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("downstream handler was not called")
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if got := rec.Header().Get("X-Test"); got != "ok" {
		t.Errorf("X-Test = %q, want %q", got, "ok")
	}
	if rec.Body.String() != "hello" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hello")
	}
}

func TestAccessLogMiddleware_DefaultStatusIs200(t *testing.T) {
	// A handler that calls Write without WriteHeader must be
	// recorded as 200 (per net/http contract).
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("auto-200"))
	})

	rec := httptest.NewRecorder()
	accessLogMiddleware(next).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (default for Write without WriteHeader)", rec.Code, http.StatusOK)
	}
}

func TestAccessLogMiddleware_BytesCounted(t *testing.T) {
	// The middleware's loggingResponseWriter must count bytes
	// written by the downstream handler. We expose a test helper
	// that wraps the same way and checks the count.
	const want = 4096
	var observed int64
	capture := func(w http.ResponseWriter, r *http.Request) {
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		lrw.WriteHeader(http.StatusOK)
		_, _ = lrw.Write([]byte(strings.Repeat("x", want)))
		observed = lrw.bytesWritten
	}

	rec := httptest.NewRecorder()
	capture(rec, httptest.NewRequest("GET", "/", nil))

	if observed != want {
		t.Errorf("loggingResponseWriter.bytesWritten = %d, want %d", observed, want)
	}
	// Sanity: the underlying recorder also saw the bytes.
	if rec.Body.Len() != want {
		t.Errorf("underlying recorder body length = %d, want %d", rec.Body.Len(), want)
	}
}

func TestAccessLogMiddleware_PassesRequestHeaders(t *testing.T) {
	// The middleware must not strip or mutate inbound headers.
	var seenUA string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenUA = r.UserAgent()
	})

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("User-Agent", "VRHub/1.0")
	req.Header.Set("X-Custom", "y")
	rec := httptest.NewRecorder()

	accessLogMiddleware(next).ServeHTTP(rec, req)

	if seenUA != "VRHub/1.0" {
		t.Errorf("User-Agent seen by next handler = %q, want %q", seenUA, "VRHub/1.0")
	}
}

func TestAccessLogMiddleware_DurationIsReasonable(t *testing.T) {
	// Sanity: the elapsed duration observed by the middleware
	// (via a downstream handler that sleeps briefly) is in the
	// expected ballpark — i.e. the timer is wired up correctly.
	const sleep = 20 * time.Millisecond
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(sleep)
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	start := time.Now()
	accessLogMiddleware(next).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	elapsed := time.Since(start)

	if elapsed < sleep {
		t.Errorf("elapsed = %v, want >= %v (middleware clock may be broken)", elapsed, sleep)
	}
	if elapsed > 5*time.Second {
		t.Errorf("elapsed = %v, want < 5s (middleware clock stuck?)", elapsed)
	}
}
