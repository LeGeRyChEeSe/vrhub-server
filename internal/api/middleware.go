package api

import (
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// accessLogMiddleware logs every HTTP request handled by the server,
// including the path, method, status code, response size, duration,
// remote address and user-agent. Used to diagnose "client gets 401
// but the server doesn't" scenarios where the operator needs to
// match a 401 response in the server log with a particular client
// request (User-Agent distinguishes VRHub from a browser tab, etc.).
//
// One log line per request at Info level. The volume is fine for a
// home-lab server; for a high-traffic deployment this should be
// gated behind a -verbose flag or moved to a sampling logger.
func accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(lrw, r)
		log.Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", lrw.statusCode).
			Int64("bytes", lrw.bytesWritten).
			Dur("duration", time.Since(start)).
			Str("remote_addr", r.RemoteAddr).
			Str("user_agent", r.UserAgent()).
			Msg("http request")
	})
}

// loggingResponseWriter wraps http.ResponseWriter to capture the
// status code and number of bytes written by downstream handlers.
// The default http.ResponseWriter silently discards both pieces of
// information, but for an access log we need both.
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
	wroteHeader  bool
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	if !lrw.wroteHeader {
		lrw.statusCode = code
		lrw.wroteHeader = true
	}
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Write(b []byte) (int, error) {
	if !lrw.wroteHeader {
		// Per net/http contract, the first Write implicitly sends
		// a 200 if no WriteHeader was called.
		lrw.wroteHeader = true
	}
	n, err := lrw.ResponseWriter.Write(b)
	lrw.bytesWritten += int64(n)
	return n, err
}

// Flush delegates to the underlying ResponseWriter's Flusher. The SSE
// handler in monitoring.go (and any other streaming endpoint) does
// `flusher, ok := w.(http.Flusher)` — without this method, the type
// assertion returns ok=false because the wrapper hides the Flusher
// interface, and streaming responses never get flushed to the client
// (the browser EventSource stays in CONNECTING state forever).
// Diagnosed 2026-06-11: without this, /admin/monitoring serves 200
// only when the connection closes; live events never reach the page.
func (lrw *loggingResponseWriter) Flush() {
	if f, ok := lrw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
