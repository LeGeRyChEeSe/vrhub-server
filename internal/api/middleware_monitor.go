package api

import (
	"net/http"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/monitor"
)

// responseRecorder wraps http.ResponseWriter to capture the status code
// that would otherwise be invisible after WriteHeader has been called.
// Used by MonitorMiddleware to publish "http" events.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// MonitorMiddleware wraps next so every request is published as a
// monitor event of type "http" with the captured method, path, status,
// and duration. The bus may be nil (no-op) so tests and partial
// deployments can wire the handler without the bus.
func MonitorMiddleware(bus *monitor.EventBus) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if bus == nil {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			// If the handler forgot to call WriteHeader, the implicit
			// 200 was captured by Write above; status stays 0 otherwise.
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			bus.Publish(monitor.Event{
				Type: "http",
				Data: map[string]any{
					"method":      r.Method,
					"path":        r.URL.Path,
					"status":      status,
					"duration_ms": time.Since(start).Milliseconds(),
					"bytes":       rec.bytes,
				},
			})
		})
	}
}
