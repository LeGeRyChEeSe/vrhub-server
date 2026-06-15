package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	"golang.org/x/crypto/bcrypt"
)

// Constants for session management.
const (
	SessionCookieName  = "vrhub_session"
	sessionIDBytes     = 32 // 32 bytes from crypto/rand → 64 hex chars
	sessionLifetime    = 7 * 24 * time.Hour
	janitorInterval    = 1 * time.Hour
	defaultDummyBcrypt = "$2a$10$abcdefghijklmnopqrstuuRX18.ZA1A5HmYJlYz3MZ0KvqA0R0pCu" // 60-char placeholder; replaced at startup by Authenticate when a real hash is available.
)

// Session represents an authenticated admin user session.
//
// All fields use json:"-" so accidental json.Marshal does not leak the live
// session ID, username, or expiry into a response body or log.
//
// Note: Get and Create return a COPY of the Session (see sessionCopy) rather
// than a live pointer into the store's map. This lets Touch mutate the live
// fields under the write lock without creating a data race against any
// concurrent reader holding a copy via SessionFromContext. Callers MUST NOT
// mutate a returned Session's fields; if a refresh is needed, call Touch on
// the store.
type Session struct {
	ID         string    `json:"-"`
	Username   string    `json:"-"`
	CreatedAt  time.Time `json:"-"`
	LastSeenAt time.Time `json:"-"`
	ExpiresAt  time.Time `json:"-"`
}

// sessionCopy returns a value-copy of s. Used by Get and Create so the
// returned *Session is decoupled from the store's live map entry.
func sessionCopy(s *Session) *Session {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

// SessionFromContext retrieves the Session from a request context.
// Returns (nil, false) if the context has no value or the value is not a *Session.
func SessionFromContext(ctx context.Context) (*Session, bool) {
	sess := ctx.Value(sessionContextKey{})
	if sess == nil {
		return nil, false
	}
	s, ok := sess.(*Session)
	if !ok {
		return nil, false
	}
	return s, true
}

// InjectSessionForTest puts s into ctx under the same key the
// SessionAuthMiddleware uses. TEST-ONLY — production code should let
// the middleware inject the session. Story 6-3.
func InjectSessionForTest(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, s)
}

// SessionStore provides in-memory session management with automatic expiration.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	closed   atomic.Bool

	// janitorHook, if non-nil, is invoked at the top of each janitor tick BEFORE
	// evictExpired runs. Used by tests to slow the janitor down for WaitGroup
	// timing tests. Never set in production.
	janitorHook func()
}

// NewSessionStore creates a new SessionStore and starts the background janitor goroutine.
// The janitor runs every hour to evict expired sessions. It exits cleanly when ctx is cancelled.
func NewSessionStore(ctx context.Context) *SessionStore {
	storeCtx, cancel := context.WithCancel(ctx)
	s := &SessionStore{
		sessions: make(map[string]*Session),
		cancel:   cancel,
	}

	s.wg.Add(1)
	go s.janitor(storeCtx)
	return s
}

// Stop closes the store: it marks the store closed (rejecting new Create calls),
// cancels the janitor goroutine, waits for the goroutine to exit, then wipes the
// session map. Calling Stop on an already-closed store is a no-op.
func (s *SessionStore) Stop() {
	if !s.closed.CompareAndSwap(false, true) {
		// Already closed — make Stop idempotent.
		return
	}
	s.cancel()
	s.wg.Wait()
	s.mu.Lock()
	s.sessions = make(map[string]*Session)
	s.mu.Unlock()
}

// IsClosed returns true if the store has been stopped.
func (s *SessionStore) IsClosed() bool {
	return s.closed.Load()
}

// Create creates a new session for the given username and returns it.
// Returns nil if the store is closed or session ID generation fails.
//
// The closed check and the map insertion both happen under the same mutex
// to prevent a TOCTOU race with Stop() (Stop sets closed and then wipes
// the map; without the lock, Create could insert into the orphaned map).
//
// Returns a copy of the inserted session so callers do not share the
// pointer with the store's map (R10-SESSION-POINTER-RACE).
func (s *SessionStore) Create(username string) *Session {
	id, err := generateSessionID()
	if err != nil {
		vlog.Get().Error().Err(err).Msg("session store: failed to generate session ID")
		return nil
	}

	now := time.Now()
	session := &Session{
		ID:         id,
		Username:   username,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(sessionLifetime),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Check closed under the lock so Stop's wipe cannot race with our insert.
	if s.closed.Load() {
		return nil
	}

	// S-03: cap the session count to prevent memory-exhaustion DoS
	// (R4-UNBOUNDED-SESSIONS defer). Without this, an attacker with
	// valid credentials can create millions of sessions over 7 days
	// (the session lifetime), filling the map and OOM-ing the server.
	//
	// The cap is enforced by FIFO eviction of the OLDEST non-target
	// session before inserting the new one. We pick a non-target
	// session to avoid evicting a concurrent legitimate login from
	// the same user (the attacker's session lifetime is 7 days, so
	// their victim is a long-lived operator session).
	//
	// If the cap is zero or negative, the cap is disabled (used by
	// tests that need to exercise the unbounded path or by future
	// operator-driven config).
	const maxSessions = 10000
	if maxSessions > 0 && len(s.sessions) >= maxSessions {
		evictOldest(s, id)
	}

	s.sessions[id] = session

	return sessionCopy(session)
}

// evictOldest removes the oldest non-target session from the store
// to make room for a new one. Called when the session count is at
// the cap. The target (newly-created session ID) is excluded from
// eviction so a freshly-created session is never immediately
// evicted by its own creation.
//
// Strategy: scan for the session with the smallest CreatedAt
// timestamp, delete it. O(n) scan, called only on the rare
// "cap reached" path (a single-digit number of times per attacker
// login burst) — not the hot path.
func evictOldest(s *SessionStore, targetID string) {
	var oldestID string
	var oldestTime time.Time
	first := true
	for id, sess := range s.sessions {
		if id == targetID {
			continue
		}
		if first || sess.CreatedAt.Before(oldestTime) {
			oldestID = id
			oldestTime = sess.CreatedAt
			first = false
		}
	}
	if oldestID == "" {
		// Cap reached AND every session is the target (i.e. the
		// cap is 1 or 0). Refuse to create — return without
		// inserting. The caller will see the new session as nil
		// and respond with a 500.
		return
	}
	delete(s.sessions, oldestID)
	vlog.Get().Warn().
		Str("evicted_session_id_prefix", oldestID[:8]).
		Int("session_count", len(s.sessions)).
		Msg("session cap reached: oldest non-target session evicted")
}

// Get retrieves a session by its ID. Returns nil if not found or expired.
// Expired sessions found by Get are evicted opportunistically so the map does
// not grow without bound between janitor ticks when many cookies are stale.
//
// Returns a copy of the session (R10-SESSION-POINTER-RACE) so callers do not
// share a pointer with the store's live map. Touch can therefore mutate the
// store's session under the write lock without racing against any reader
// holding a stale pointer.
func (s *SessionStore) Get(id string) *Session {
	// Fast path: read lock.
	s.mu.RLock()
	session, ok := s.sessions[id]
	if !ok {
		s.mu.RUnlock()
		return nil
	}
	// R11-MEDIUM-5: capture now once. The previous `time.Now().Before(...) ||
	// time.Now().Equal(...)` called the wall clock twice; a second-call
	// crossing the expiry boundary could incorrectly return a fresh copy
	// of an expired session.
	now := time.Now()
	if now.Before(session.ExpiresAt) || now.Equal(session.ExpiresAt) {
		s.mu.RUnlock()
		return sessionCopy(session)
	}
	s.mu.RUnlock()

	// Slow path: session is expired — upgrade to write lock and evict.
	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check under the write lock: another goroutine may have evicted or refreshed.
	session, ok = s.sessions[id]
	if !ok {
		return nil
	}
	// R11-MEDIUM-5 (cont.): reuse the same `now` so the slow-path decision
	// is consistent with the fast-path decision.
	if now.After(session.ExpiresAt) {
		delete(s.sessions, id)
		return nil
	}
	return sessionCopy(session)
}

// Touch updates the LastSeenAt and ExpiresAt of a session.
// Returns false if the session doesn't exist or is already expired (prevents
// resurrecting expired sessions). Expired sessions are evicted on miss.
func (s *SessionStore) Touch(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return false
	}

	if time.Now().After(session.ExpiresAt) {
		delete(s.sessions, id)
		return false
	}

	now := time.Now()
	session.LastSeenAt = now
	session.ExpiresAt = now.Add(sessionLifetime)
	return true
}

// Delete removes a session by its ID. Returns true if the session existed.
func (s *SessionStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
	}
	return ok
}

// janitor runs periodically to evict expired sessions.
// The goroutine is tracked by the store's WaitGroup so Stop() can wait for
// its actual exit before wiping the map. A panic inside evictExpired is
// recovered so a future map-write race or other defect does not silently
// kill the eviction loop and cause unbounded memory growth.
//
// R12-P1: the tick body is now inlined into the janitor loop (no separate
// runJanitorTick method). The previous design had a check-then-act race:
// `closed.Load()` then `wg.Add(1)` was not atomic, so a Stop interleaved
// between the two could reuse a WaitGroup whose prior Wait had already
// returned, panicking. The new design relies on the loop's single `+1`
// (registered at goroutine start); the closed check is purely an early
// return BEFORE any work is done, with no Add/Done pair to race.
//
// R11-LOW-1: doc fix — removed the misleading "Exported" wording.
func (s *SessionStore) janitor(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runJanitorTick()
		}
	}
}

// runJanitorTick executes one janitor iteration: invoke the test hook (if set)
// and run evictExpired under a panic-recovery guard. Tests drive this method
// directly to avoid waiting for the hourly ticker.
//
// R12-P1: the previous check-then-act race (closed.Load() then wg.Add(1)) is
// now eliminated. The body does NOT touch the WaitGroup — the parent
// janitor goroutine holds the single +1 from NewSessionStore, and this
// method is called from inside that goroutine. The closed check is now
// purely an early-return optimization (skips eviction work on a closed
// store) with no WaitGroup interaction to race.
func (s *SessionStore) runJanitorTick() {
	if s.closed.Load() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			vlog.Get().Error().Interface("panic", r).Msg("janitor panic recovered")
		}
	}()
	if s.janitorHook != nil {
		s.janitorHook()
	}
	s.evictExpired()
}

// evictExpired removes all expired sessions from the store.
// Two-pass design: first a read-lock pass collects expired IDs, then a brief
// write-lock pass deletes them. This minimizes the write-lock duration so
// concurrent Get/Create calls aren't starved during eviction sweeps.
func (s *SessionStore) evictExpired() {
	now := time.Now()

	s.mu.RLock()
	var expired []string
	for id, session := range s.sessions {
		if now.After(session.ExpiresAt) {
			expired = append(expired, id)
		}
	}
	s.mu.RUnlock()

	if len(expired) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range expired {
		// Re-check under the write lock: another goroutine could have refreshed the session.
		if session, ok := s.sessions[id]; ok && now.After(session.ExpiresAt) {
			delete(s.sessions, id)
		}
	}
}

// randReader is the source of random bytes for generateSessionID. Production code
// uses crypto/rand.Reader; tests can swap in a failing reader via withRandReader.
//
// randReaderMu is an RWMutex (not Mutex) so the hot read in generateSessionID
// can take RLock concurrently with other readers; only the test-only swap in
// withRandReader takes the write lock. Without the read lock, an unsynchronised
// read of the io.Reader interface (a fat pointer containing type + data) while
// a concurrent swap is in flight can tear the value, causing panic or a
// type/data mismatch that calls the wrong Reader implementation.
// R11-CRITICAL-1: randReader interface tear.
var (
	randReader   io.Reader = rand.Reader
	randReaderMu sync.RWMutex
)

// withRandReader temporarily replaces randReader for the duration of fn.
// Used exclusively by tests to exercise the generateSessionID error path.
func withRandReader(r io.Reader, fn func()) {
	randReaderMu.Lock()
	old := randReader
	randReader = r
	randReaderMu.Unlock()
	defer func() {
		randReaderMu.Lock()
		randReader = old
		randReaderMu.Unlock()
	}()
	fn()
}

// generateSessionID generates a cryptographically random 32-byte hex string (64 chars).
// Returns an error if the random source fails (e.g., Linux getrandom(2) on early boot).
// Callers MUST handle the error path; do not panic on it.
func generateSessionID() (string, error) {
	b := make([]byte, sessionIDBytes)
	// R11-CRITICAL-1: take RLock so the io.Reader interface read is atomic with
	// respect to the write-locked swap in withRandReader. Without this lock a
	// concurrent test could tear the interface (type/data pointer mismatch).
	randReaderMu.RLock()
	reader := randReader
	randReaderMu.RUnlock()
	if _, err := io.ReadFull(reader, b); err != nil {
		return "", fmt.Errorf("auth.sessionStore.generate: random source failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// dummyBcryptHash is the hash used for the username-mismatch branch in Authenticate
// to keep timing constant. It is initialised on the first Authenticate call with a
// real admin hash (so the cost factor matches) and re-used thereafter. The dummy
// is re-seeded if the real hash changes (e.g. operator rotates admin password).
// The hardcoded fallback at the top of the file is used only when the real hash
// has not yet been parsed (first call or test contexts).
var (
	dummyBcryptHash      atomic.Value // string
	dummyBcryptSource    atomic.Value // string — the real hash that seeded the dummy (so re-seeds detect cost changes)
	resolveDummyBcryptMu sync.Mutex   // serializes concurrent resolves to prevent half-updated state
)

// resolveDummyBcrypt picks a dummy bcrypt hash whose cost factor matches the
// real admin hash. If the real hash is unparseable we fall back to the constant
// defaultDummyBcrypt. The dummy is computed once per unique real-hash value;
// callers reach this on cold start.
//
// The two atomic.Value stores (dummyBcryptHash and dummyBcryptSource) are
// written atomically under resolveDummyBcryptMu so concurrent Authenticate
// calls cannot observe a half-updated state (e.g. new source with old hash).
// R10-RESOLVEDUMMY-ATOMIC.
func resolveDummyBcrypt(realHash string) string {
	if current, ok := dummyBcryptHash.Load().(string); ok && current != "" {
		if prev, _ := dummyBcryptSource.Load().(string); prev == realHash {
			return current
		}
	}

	resolveDummyBcryptMu.Lock()
	defer resolveDummyBcryptMu.Unlock()
	// Re-check under the lock: another goroutine may have just seeded the dummy.
	if current, ok := dummyBcryptHash.Load().(string); ok && current != "" {
		if prev, _ := dummyBcryptSource.Load().(string); prev == realHash {
			return current
		}
	}

	cost, err := bcrypt.Cost([]byte(realHash))
	if err != nil {
		// Real hash unparseable — fall back to the constant. A subsequent
		// caller with a valid hash will overwrite this.
		dummyBcryptHash.Store(defaultDummyBcrypt)
		dummyBcryptSource.Store(realHash)
		return defaultDummyBcrypt
	}

	// Compute a fresh dummy at the real hash's cost factor so the timing is symmetric.
	hash, err := bcrypt.GenerateFromPassword([]byte("dummy-timing-constant-input"), cost)
	if err != nil {
		// Generating bcrypt at the real cost failed (extremely rare) — fall back.
		dummyBcryptHash.Store(defaultDummyBcrypt)
		dummyBcryptSource.Store(realHash)
		return defaultDummyBcrypt
	}

	dummyBcryptHash.Store(string(hash))
	dummyBcryptSource.Store(realHash)
	return string(hash)
}

// Authenticate checks if the given username and password match the admin config.
// Uses subtle.ConstantTimeCompare for username to prevent timing oracles revealing valid usernames.
//
// On any "fail-fast" branch (nil cfg, empty stored username, empty stored hash, username mismatch)
// we run ValidatePassword against a dummy bcrypt hash whose cost matches the real admin hash so
// the wall-clock time is symmetric. This closes the username-enumeration oracle that a naïve
// early-return would open.
func Authenticate(cfg *types.Config, username, password string) bool {
	// Decide which hash to compare against. On any failure-equivalent branch
	// we still incur a full bcrypt cost via the dummy.
	storedUsername := ""
	storedHash := ""
	if cfg != nil {
		storedUsername = cfg.Admin.Username
		storedHash = cfg.Admin.PasswordHash
	}

	// Pick a dummy hash whose cost matches the real one when available.
	// On empty/nil cfg we use the static fallback (the operator hasn't completed setup
	// so the timing surface is irrelevant — there is no valid login to enumerate).
	dummy := defaultDummyBcrypt
	if storedHash != "" {
		dummy = resolveDummyBcrypt(storedHash)
	}

	usernameMatch := storedUsername != "" &&
		subtle.ConstantTimeCompare([]byte(username), []byte(storedUsername)) == 1

	hashToCheck := storedHash
	if !usernameMatch || storedHash == "" {
		hashToCheck = dummy
	}

	result := ValidatePassword(hashToCheck, password)

	if !usernameMatch || storedHash == "" {
		return false
	}
	if !result {
		// If the real hash was somehow malformed we log it; a malformed real hash always returns false here.
		// We can detect this by re-running bcrypt.Cost on the hash and checking for error.
		if _, err := bcrypt.Cost([]byte(storedHash)); err != nil {
			vlog.Get().Warn().Err(err).Msg("auth.Authenticate: admin password hash is malformed; check config.toml")
		}
	}

	return result
}

// isHTTPS returns true if the incoming request was received over HTTPS, either
// directly (r.TLS != nil) or via a reverse proxy that sets the de-facto
// X-Forwarded-Proto: https header. This is the correct signal for the cookie
// Secure flag — not the request's host loopback status.
//
// Why not isLoopback(host)? On a LAN, the server's bound IP (e.g. 192.168.50.3)
// is not loopback, so an HTTP request from a phone/Quest on the same network
// looks "non-loopback" to the old isLoopback check. The previous SetSessionCookie
// logic therefore set Secure=true for ALL non-loopback requests, which made
// browsers refuse to store the cookie over plain HTTP — silently breaking
// LAN login. The Secure attribute is about the transport (HTTPS vs HTTP), not
// about which IP the request came from. Story 9.7 (B7).
//
// X-Forwarded-Proto is honored WITHOUT a proxy-trust list because the
// consequences of an attacker forging it are limited: forging it to "https"
// only sets Secure=true on a cookie the attacker cannot read (they have no
// valid session to steal). Forging it to "http" cannot downgrade an existing
// Secure cookie — the browser retains the original Secure attribute. The
// only thing an attacker can flip is the Set-Cookie Secure bit on a response
// to a request they originated, which is harmless without a session cookie
// to pair with it. If a future deployment needs strict proxy trust, wrap
// this helper with a per-request proxy-IP check.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// SetSessionCookie sets an HTTP-only session cookie. The Secure flag is bound
// to the actual transport of the incoming request (HTTPS direct, or HTTPS via
// a reverse proxy that sets X-Forwarded-Proto: https) — see isHTTPS. The
// previous implementation derived Secure from !isLoopback(host), which
// incorrectly set Secure=true for plain-HTTP requests from non-loopback IPs
// (e.g. a phone on the LAN reaching the server at http://192.168.50.3:8080)
// and silently broke login from other devices. Story 9.7 (B7).
func SetSessionCookie(w http.ResponseWriter, r *http.Request, sessionID string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		Secure:   isHTTPS(r),
	})
}

// ClearSessionCookie clears the session cookie by setting MaxAge=-1 which emits
// the Max-Age=0 header attribute (the standard "delete this cookie now" signal).
// The Secure flag mirrors the request's actual transport via isHTTPS(r) so the
// deletion matches the original Set-Cookie: a Secure cookie can only be deleted
// by a Set-Cookie with Secure=true, and a non-Secure cookie can only be deleted
// by a Set-Cookie with Secure=false. Using isHTTPS(r) on BOTH set and clear
// guarantees the parity required by RFC 6265 §5.3 step 6.
//
// R11-MEDIUM-6: also set Expires to the Unix epoch (1970-01-01). Some legacy
// browsers (IE, older mobile) only honour the Expires header for cookie
// deletion; Max-Age alone is ignored.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		Secure:   isHTTPS(r),
	})
}

// ReadSessionCookie reads and returns the session ID from the request cookie.
// Returns an empty string and false if the cookie is missing or malformed.
// Unicode whitespace (e.g. U+00A0 NO-BREAK SPACE) is rejected as malformed —
// session IDs are ASCII hex by construction.
func ReadSessionCookie(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return "", false
	}

	id := strings.TrimSpace(cookie.Value)
	if id == "" {
		return "", false
	}

	// Validate: must be a valid hex string of exactly 64 characters (32 bytes).
	if len(id) != sessionIDBytes*2 {
		return "", false
	}
	for _, c := range id {
		// Only ASCII hex is allowed — reject any Unicode characters (including
		// NBSP-prefixed garbage) so we don't accidentally classify them as
		// "almost valid".
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return "", false
		}
	}

	return id, true
}

// sessionContextKey is a custom type for context keys to avoid collisions.
type sessionContextKey struct{}

// SessionAuthMiddleware returns a middleware that validates the session cookie.
// On miss: returns 302 redirect to /admin/?showLogin=1 for text/html clients,
// or 401 JSON for API clients (Accept-header rule matches HandleAuthLoginPOST).
//
// The `host` parameter is the resolved server host (cfg.Server.Host) so the
// middleware can match the Secure flag of the original Set-Cookie when emitting
// the cookie-clearing Set-Cookie. Without it, a Secure cookie set on HTTPS
// cannot be cleared (the browser would refuse the non-Secure deletion).
//
// When `store` is nil (test-mode wiring) the middleware passes through.
// When `store` has been Stop()ed it returns 503 — closing the store means the
// server is shutting down, so leaking auth-bypass to in-flight requests would
// be the worst possible failure mode.
//
// On every successful read it also clears any stale malformed/invalid cookie
// so the user does not get stuck in a redirect loop with a bad cookie that
// the browser keeps re-sending.
func SessionAuthMiddleware(store *SessionStore, host string) func(http.Handler) http.Handler {
	hostFn := func() string { return host }
	return SessionAuthMiddlewareWithHostFunc(store, hostFn)
}

// SessionAuthMiddlewareWithHostFunc returns the same middleware as
// SessionAuthMiddleware but the host is resolved on EVERY request via the
// provided function. This is the correct shape when the host can change at
// runtime (e.g. a setup→normal transition where the in-memory cfg pointer
// is reloaded from disk). Capturing the host once at router construction
// time, the previous design, left a stale host in the closure and the
// Clear-Cookie Secure flag would mismatch the original Set-Cookie
// (R10-AUTHHOST-MISMATCH).
//
// The optional `pathSkip` predicate(s), if non-nil and any returns true for
// a given request, BYPASSES the session check entirely and lets the
// request reach the next handler unchanged. This is used by Story 9.3
// to let the `/admin/api/scripts/*` sub-tree authenticate via the
// X-API-Key middleware (mounted INSIDE the same protected router) when
// invoked by script clients (cron, CI) that cannot store a session
// cookie. The X-API-Key middleware on the inner sub-router enforces
// authentication, so the skip does not create an auth-free hole: a
// request without X-API-Key is rejected by that inner middleware with
// 401 API_KEY_MISSING. A request with a valid X-API-Key authenticates
// and proceeds. The skip is path-scoped (NOT host/method scoped) and
// is the MINIMAL change to honour Story 6.4's "API key alone suffices
// for /admin/api/scripts/*" contract that the previous design silently
// violated.
func SessionAuthMiddlewareWithHostFunc(store *SessionStore, hostFunc func() string, pathSkip ...func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Resolve host (if a hostFunc was provided) so the existing
			// contract is preserved: any side effects in the hostFunc (e.g.
			// router.go's hostGetter triggers a config disk-read via
			// resolveConfig) still run on every request. The result itself is
			// no longer used here as of Story 9.7 (B7): ClearSessionCookie now
			// derives the Secure flag from the request transport via isHTTPS(r),
			// not from the resolved host. Previously, the host string was passed
			// to ClearSessionCookie so it could decide !isLoopback(host) →
			// Secure=true, which incorrectly marked LAN-HTTP cookies as Secure
			// and broke login from non-loopback clients.
			if hostFunc != nil {
				_ = hostFunc()
			}
			// Story 9.3 (B3): honour the per-request path skip BEFORE
			// the store-closed fail-closed branch so a request
			// targeting the scripts sub-tree still works when the
			// session store has been closed during a graceful shutdown
			// — the inner X-API-Key middleware enforces its own
			// auth/503 contract.
			for _, skip := range pathSkip {
				if skip != nil && skip(r) {
					next.ServeHTTP(w, r)
					return
				}
			}
			if store == nil {
				next.ServeHTTP(w, r)
				return
			}
			if store.closed.Load() {
				// Fail-closed: closed store = shutting down. Refuse rather than silently bypass.
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"message":"Server shutting down","code":"SERVICE_UNAVAILABLE"}}`))
				return
			}

			id, ok := ReadSessionCookie(r)
			if !ok {
				// Cookie missing OR malformed — clear any garbage cookie before
				// redirecting so the browser stops re-sending it on each retry.
				// Story 9.7 (B7): ClearSessionCookie now derives the Secure flag
				// from the request transport via isHTTPS(r) instead of from
				// host-loopback status — so we pass `r`, not the resolved host.
				ClearSessionCookie(w, r)
				writeAuthError(w, r)
				return
			}

			session := store.Get(id)
			if session == nil {
				ClearSessionCookie(w, r)
				writeAuthError(w, r)
				return
			}

			if !store.Touch(id) {
				ClearSessionCookie(w, r)
				writeAuthError(w, r)
				return
			}

			// R11-LOW-3: the pre-Touch `session` is a stale copy (LastSeenAt /
			// ExpiresAt are pre-Touch). Re-fetch after Touch so the context
			// carries the freshly-updated timestamps. Cheap: hits the RLock
			// fast-path and returns a fresh copy.
			//
			// C-08 (debt-triage-2026-06-06) + R8-CR-2 fix: a concurrent
			// Delete() between Touch and this re-fetch would have removed
			// the session. Get returning nil here means the session is
			// gone; we MUST NOT continue with the pre-Touch `session`
			// pointer because downstream handlers (e.g. privileged admin
			// writes) would authorize a request whose cookie was revoked
			// mid-flight. The earlier "safe because next request will
			// re-validate" rationale only protected FUTURE requests, not
			// the in-flight one.
			fresh := store.Get(id)
			if fresh == nil {
				// Session was deleted between Touch and re-fetch.
				// Clear the cookie (paranoia) and fail the request.
				ClearSessionCookie(w, r)
				writeAuthError(w, r)
				return
			}
			session = fresh

			ctx := context.WithValue(r.Context(), sessionContextKey{}, session)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeAuthError writes an auth error response based on the Accept header.
// JSON clients get 401; HTML clients get 302 redirect to /admin/?showLogin=1
// (the login form is embedded in the admin shell template, revealed by ?showLogin=1).
//
// Cache-Control: no-store is set on BOTH paths (R11-MEDIUM-4) so corporate
// caches/proxies don't memoize either the 401 or the 302 redirect. Without
// it, an aggressive intermediary could cache the redirect and serve a
// stale "unauthenticated" response after the user has since logged in.
func writeAuthError(w http.ResponseWriter, r *http.Request) {
	if IsJSONRequest(r) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnauthorized)
		if _, err := w.Write([]byte(`{"error":{"message":"Authentication required","code":"UNAUTHORIZED"}}`)); err != nil {
			// Log at Debug — a write failure typically means the client hung up; not actionable.
			vlog.Get().Debug().Err(err).Msg("writeAuthError: write failed (client likely disconnected)")
		}
		return
	}
	// R11-MEDIUM-4: HTML redirect path also needs Cache-Control: no-store.
	w.Header().Set("Cache-Control", "no-store")
	// Story 6-3 (R6-AC1-AC3-REDIRECTS resolution): redirect to the
	// spec-literal /admin/login route (now registered in router.go),
	// not the 6-2 temporary /admin/?showLogin=1 (which assumed a
	// single SPA shell). The /admin/login route is the same shell
	// rendered with a "Please log in" header; the SPA-style URL
	// hash (#/login) is the Power User default. R1 DN-1 + R11-D2
	// decisions resolved.
	//
	// Live session 2026-06-09: the login form is HIDDEN by default
	// in the admin shell — it requires ?showLogin=1 to be revealed
	// (the setup wizard uses this). Without the query param, the
	// user lands on the shell with all widgets broken (no auth),
	// which is a worse UX than showing the login form. So we add
	// ?showLogin=1 to ensure the form is visible.
	http.Redirect(w, r, "/admin/login?showLogin=1", http.StatusFound)
}

// validXRequestedWith is the allowlist of accepted X-Requested-With values
// used as a JSON-classification hint by IsJSONRequest. Accepting any non-empty
// value would invite future "treat-as-JSON" misclassifications, so we pin to
// the two values the admin.js frontend can legitimately send.
var validXRequestedWith = map[string]struct{}{
	"XMLHttpRequest": {},
	"fetch":          {},
}

// IsJSONRequest returns true if the request's Accept header indicates a JSON client
// (and therefore expects a 200 JSON response with a redirect URL rather than a 302).
// It uses media-type parsing with q-value negotiation per RFC 7231 §5.3.2:
//
//   - If Accept mentions application/json (exact match) with q-value > text/html q-value → JSON.
//   - If Accept only mentions */* or application/* wildcards, fall back to the X-Requested-With
//     header (restricted allowlist of "XMLHttpRequest" and "fetch") — pin to known values so a
//     future regression cannot pivot arbitrary header values into JSON classification.
//   - If Accept is empty, only text/html, or any media-type with text/html winning → HTML.
//   - Missing Accept header is treated as HTML (browser default).
//   - q=0 means "not acceptable" per RFC 7231 §5.3.4 — such types are ignored.
//   - q-values outside [0, 1] are ignored (RFC 7231 §5.3.1).
//   - Malformed Accept parts are dropped (we cannot infer the operator's intent from garbage).
//   - Both application/json and text/html require exact media-type match — vendor extensions like
//     application/jsonp, application/json5, text/htmlfoo are NOT misclassified.
//
// This function is the single source of truth for the JSON/HTML classification used by
// both HandleAuthLoginPOST and SessionAuthMiddleware (extracted to avoid logic drift).
func IsJSONRequest(r *http.Request) bool {
	// API routes (/admin/api/*) are always JSON endpoints — redirect HTML
	// responses break fetch() callers that don't set Accept: application/json.
	if strings.HasPrefix(r.URL.Path, "/admin/api/") {
		return true
	}

	accept := r.Header.Get("Accept")
	if accept == "" {
		// No Accept header → treat as HTML (browser default).
		return false
	}

	jsonQ := -1.0
	htmlQ := -1.0
	hasWildcard := false

	for _, part := range strings.Split(accept, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			// Empty part (e.g. trailing or doubled comma) — skip.
			continue
		}
		mediaType, params, err := mime.ParseMediaType(trimmed)
		if err != nil {
			// Malformed parts are dropped: RFC 7231 §5.3.4 leaves recipient
			// behavior undefined; we choose strictest interpretation (ignore).
			continue
		}

		// Default q-value is 1.0 per RFC 7231 §5.3.4.
		q := 1.0
		if qstr, ok := params["q"]; ok {
			parsed, perr := strconv.ParseFloat(qstr, 64)
			if perr != nil || parsed < 0 || parsed > 1 {
				// Out-of-range q (per RFC 7231 §5.3.1, MUST be ignored) or
				// unparseable q — skip this media type entirely; we cannot
				// trust its preference signal.
				continue
			}
			q = parsed
		}

		// q=0 means "not acceptable" — skip this media type entirely.
		if q == 0 {
			continue
		}

		// Lowercased for case-insensitive comparison (mime.ParseMediaType
		// already lowercases the type/subtype, so this is belt-and-suspenders).
		mediaType = strings.ToLower(mediaType)

		switch {
		case mediaType == "application/json":
			if q > jsonQ {
				jsonQ = q
			}
		case mediaType == "text/html":
			if q > htmlQ {
				htmlQ = q
			}
		case mediaType == "*/*" || mediaType == "application/*":
			hasWildcard = true
		}
	}

	// JSON wins if it was seen with a strictly-higher q than HTML.
	// Ties go to HTML (browser default behavior).
	if jsonQ >= 0 && jsonQ > htmlQ {
		return true
	}
	// HTML wins if it has any q-value.
	if htmlQ >= 0 {
		return false
	}
	// JSON-only with no HTML at all wins.
	if jsonQ >= 0 {
		return true
	}
	// Only wildcards (or unrecognized types) — fall back to X-Requested-With allowlist.
	if hasWildcard {
		if xrw := r.Header.Get("X-Requested-With"); xrw != "" {
			if _, ok := validXRequestedWith[xrw]; ok {
				return true
			}
		}
	}
	// No recognized media type → default to HTML.
	return false
}

// isLoopbackWarnOnce ensures the malformed-bracket Warn log is emitted at most
// once per process. Without it, a single typo in config.toml (e.g. `Host =
// "[::1"` with a missing `]`) produces a Warn line on EVERY protected request,
// flooding the log. R11-HIGH-2.
var isLoopbackWarnOnce sync.Once

// isLoopback returns true if the host is a loopback address (127.x.x.x, ::1, localhost).
//
// The empty string is treated as loopback so callers that lack a resolved host
// (e.g. the middleware) get a safe default: a non-Secure cookie is accepted by
// the browser over both HTTP (local dev) and HTTPS (production behind a reverse
// proxy that strips the host). The cost is that ClearSessionCookie called with
// "" never sets Secure — acceptable because a non-Secure deletion is honored
// by browsers regardless of whether the original cookie was Secure.
//
// Uses net.SplitHostPort for unbracketed IPv4+port and bracketed IPv6+port,
// then net.ParseIP for the actual loopback check (covers 127.x.x.x, ::1,
// ::ffff:127.0.0.1, etc.). Falls back to a case-insensitive "localhost" match.
//
// The legacy `127.1` shorthand is intentionally NOT supported — net.ParseIP
// rejects it. Operators who write `127.1` in config.toml will see a
// non-loopback classification; document it in the Server.Host config comment.
func isLoopback(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		// Safe default: treat unknown as loopback so we never accidentally emit
		// Secure cookies that browsers will silently drop over HTTP.
		return true
	}

	// Strip surrounding brackets for bare IPv6 like "[::1]" (no port).
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	} else if h, _, err := net.SplitHostPort(host); err == nil {
		// "host:port" or "[ipv6]:port" — strip the port.
		host = h
	} else if strings.HasPrefix(host, "[") {
		// Malformed bracketed form (missing trailing ]) — best-effort strip
		// and log a warning so the operator can spot a typo in config.toml
		// (R10-ISLOOPBACK-MALFORMED). Without the warning a typo silently
		// flips the Secure flag and operators see a confusing "loopback
		// HTTPS" misclassification.
		//
		// R11-HIGH-2: rate-limit the warning to once per process. A single
		// typo in config.toml would otherwise produce a Warn line on EVERY
		// protected request, flooding the log.
		isLoopbackWarnOnce.Do(func() {
			vlog.Get().Warn().Str("host", host).Msg("isLoopback: malformed bracketed host — best-effort strip (warning logged once)")
		})
		host = strings.TrimPrefix(host, "[")
		if endIdx := strings.Index(host, "]"); endIdx > 0 {
			host = host[:endIdx]
		}
	}

	// Strip zone identifier if present (e.g., fe80::1%eth0 → fe80::1).
	if zoneIdx := strings.Index(host, "%"); zoneIdx > 0 {
		host = host[:zoneIdx]
	}

	// Try parsing as IP first (covers 127.x.x.x, ::1, 0.0.0.0, etc.).
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}

	// Fall back to case-insensitive hostname match for "localhost".
	return strings.EqualFold(host, "localhost")
}
