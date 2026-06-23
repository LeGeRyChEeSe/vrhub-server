package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	chi "github.com/go-chi/chi/v5"
)

// newTestSessionStore creates a real SessionStore and registers its Stop in
// t.Cleanup so the janitor goroutine does not leak between tests. Used by
// router tests that need a non-nil sessionStore (R11-HIGH-4).
func newTestSessionStore(t *testing.T) *auth.SessionStore {
	t.Helper()
	store := auth.NewSessionStore(context.Background())
	t.Cleanup(store.Stop)
	return store
}

func setupAdminTestDB(t *testing.T) (*db.DB, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	d, err := db.Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return d, func() { d.Close() }
}

func insertTestGame(t *testing.T, d *db.DB, game types.GameEntry) {
	t.Helper()
	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert test game: %v", err)
	}
}

// newExposedToggleRouter creates a chi router with just the exposed
// toggle route for testing. M-06 (review 2026-06-11) added CSRF
// protection to HandleExposedTogglePATCH, so the router installs a
// session-injecting middleware; callers must set X-CSRF-Token to
// auth.CSRFTokenForSession(returnedSession.ID).
func newExposedToggleRouter(t *testing.T, d *db.DB) (*chi.Mux, *auth.Session) {
	t.Helper()
	r := chi.NewRouter()
	store := auth.NewSessionStore(context.Background())
	t.Cleanup(store.Stop)
	adminHandler := NewAdminHandler(t.TempDir(), nil, d, store, nil)
	session := adminHandler.SessionStore.Create("test-admin")
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.InjectSessionForTest(req.Context(), session)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Patch("/api/games/{releaseName}/exposed", adminHandler.HandleExposedTogglePATCH)
	return r, session
}

func TestHandleExposedTogglePATCH_Success_ToggleOff(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	game := types.GameEntry{
		ReleaseName: "com.example.game",
		GameName:    "Example Game",
		PackageName: "com.example.game",
		VersionCode: 42,
		SizeBytes:   1024,
		Hash:        "testhash",
		Exposed:     true,
	}
	insertTestGame(t, d, game)

	router, session := newExposedToggleRouter(t, d)

	body, err := json.Marshal(map[string]bool{"exposed": false})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/games/com.example.game/exposed", bytes.NewReader(body))
	w := httptest.NewRecorder()
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusOK, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	if got, _ := data["exposed"].(bool); got != false {
		t.Errorf("exposed = %v, want false", got)
	}

	if got, _ := data["message"].(string); got == "" {
		t.Error("response message is empty")
	}

	// Verify DB was updated
	updatedGame, err := d.GetGameByPackage("com.example.game")
	if err != nil {
		t.Fatalf("get game by package: %v", err)
	}
	if updatedGame.Exposed {
		t.Error("game should be hidden in database after toggle off")
	}
}

func TestHandleExposedTogglePATCH_Success_ToggleOn(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	game := types.GameEntry{
		ReleaseName: "com.example.game",
		GameName:    "Example Game",
		PackageName: "com.example.game",
		VersionCode: 42,
		SizeBytes:   1024,
		Hash:        "testhash",
		Exposed:     false,
	}
	insertTestGame(t, d, game)

	router, session := newExposedToggleRouter(t, d)

	body, err := json.Marshal(map[string]bool{"exposed": true})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/games/com.example.game/exposed", bytes.NewReader(body))
	w := httptest.NewRecorder()
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusOK, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	if got, _ := data["exposed"].(bool); got != true {
		t.Errorf("exposed = %v, want true", got)
	}

	// Verify DB was updated
	updatedGame, err := d.GetGameByPackage("com.example.game")
	if err != nil {
		t.Fatalf("get game by package: %v", err)
	}
	if !updatedGame.Exposed {
		t.Error("game should be exposed in database after toggle on")
	}
}

func TestHandleExposedTogglePATCH_GameNotFound(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	router, session := newExposedToggleRouter(t, d)

	body, err := json.Marshal(map[string]bool{"exposed": false})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/games/com.nonexistent.game/exposed", bytes.NewReader(body))
	w := httptest.NewRecorder()
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusNotFound {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusNotFound, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing error field")
	}

	if got, _ := errObj["code"].(string); got != "GAME_NOT_FOUND" {
		t.Errorf("error code = %q, want %q", got, "GAME_NOT_FOUND")
	}
}

func TestHandleExposedTogglePATCH_MissingPackageName(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	router, session := newExposedToggleRouter(t, d)

	body, err := json.Marshal(map[string]bool{"exposed": false})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/games//exposed", bytes.NewReader(body))
	w := httptest.NewRecorder()
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusBadRequest, w.Body.String())
	}
}

func TestHandleExposedTogglePATCH_InvalidBody(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	game := types.GameEntry{
		ReleaseName: "com.example.game",
		GameName:    "Example Game",
		PackageName: "com.example.game",
		VersionCode: 42,
		SizeBytes:   1024,
		Hash:        "testhash",
	}
	insertTestGame(t, d, game)

	router, session := newExposedToggleRouter(t, d)

	req := httptest.NewRequest(http.MethodPatch, "/api/games/com.example.game/exposed", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusBadRequest, w.Body.String())
	}
}

// TestHandleExposedTogglePATCH_MissingExposedField is the C-15 regression
// gate: a payload that omits the "exposed" field must be rejected with
// 400 INVALID_PARAM. Before the fix, an empty body or `{}` would silently
// coerce to Exposed=false (Go zero value), unexposing the game without
// the operator's consent.
func TestHandleExposedTogglePATCH_MissingExposedField(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	game := types.GameEntry{
		ReleaseName: "com.example.game",
		GameName:    "Example Game",
		PackageName: "com.example.game",
		VersionCode: 42,
		SizeBytes:   1024,
		Hash:        "testhash",
		Exposed:     true, // start exposed
	}
	insertTestGame(t, d, game)

	router, session := newExposedToggleRouter(t, d)

	// Empty JSON object — no "exposed" field. Should be rejected.
	req := httptest.NewRequest(http.MethodPatch, "/api/games/com.example.game/exposed", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (missing 'exposed' must be rejected)\nbody: %s", w.Code, w.Body.String())
	}

	// Verify the game was NOT actually unexposed.
	retrieved, err := d.GetGameByPackage("com.example.game")
	if err != nil {
		t.Fatalf("get game after rejected request: %v", err)
	}
	if !retrieved.Exposed {
		t.Error("game was unexposed despite rejected request — silent zero-value coercion (C-15 regression)")
	}
}

// TestHandleExposedTogglePATCH_BodyTooLarge is the C-13 regression gate:
// a request body larger than maxAdminBodySize (4 KiB) must be rejected.
// The current implementation returns 400 (BODY_TOO_LARGE) rather than
// the RFC-7231-spec-prescribed 413; see the dev comment on
// maxAdminBodySize for the rationale and the test that guards
// TestHandleAuthLoginPOST_OversizedBody's 400 contract.
func TestHandleExposedTogglePATCH_BodyTooLarge(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	game := types.GameEntry{
		ReleaseName: "com.example.game",
		GameName:    "Example Game",
		PackageName: "com.example.game",
		VersionCode: 42,
		SizeBytes:   1024,
		Hash:        "testhash",
		Exposed:     true,
	}
	insertTestGame(t, d, game)

	router, session := newExposedToggleRouter(t, d)

	// Body well over the 4 KiB limit. Keep the JSON prefix valid so the
	// decoder reaches the byte-count check rather than a SyntaxError.
	largeBody := bytes.Repeat([]byte("x"), 5000)

	req := httptest.NewRequest(http.MethodPatch, "/api/games/com.example.game/exposed", bytes.NewReader(largeBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body limit must reject > 4 KiB)\nbody: %s", w.Code, w.Body.String())
	}
}

func TestHandleExposedTogglePATCH_ResponseFields(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	game := types.GameEntry{
		ReleaseName: "com.example.game",
		GameName:    "Example Game",
		PackageName: "com.example.game",
		VersionCode: 42,
		SizeBytes:   1024,
		Hash:        "testhash",
	}
	insertTestGame(t, d, game)

	router, session := newExposedToggleRouter(t, d)

	body, err := json.Marshal(map[string]bool{"exposed": false})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/games/com.example.game/exposed", bytes.NewReader(body))
	w := httptest.NewRecorder()
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusOK, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing data field")
	}

	if got, _ := data["game_id"].(float64); got == 0 {
		t.Error("game_id should be non-zero")
	}

	if got, _ := data["release_name"].(string); got != "com.example.game" {
		t.Errorf("release_name = %q, want %q", got, "com.example.game")
	}

	if got, _ := data["package_name"].(string); got != "com.example.game" {
		t.Errorf("package_name = %q, want %q", got, "com.example.game")
	}

	if got, _ := data["exposed"].(bool); got != false {
		t.Errorf("exposed = %v, want false", got)
	}

	if msg, _ := data["message"].(string); msg == "" {
		t.Error("message should not be empty")
	}
}

func TestHandleExposedTogglePATCH_IdempotentSameStatus(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	game := types.GameEntry{
		ReleaseName: "com.example.game",
		GameName:    "Example Game",
		PackageName: "com.example.game",
		VersionCode: 42,
		SizeBytes:   1024,
		Hash:        "testhash",
		Exposed:     false,
	}
	insertTestGame(t, d, game)

	router, session := newExposedToggleRouter(t, d)

	body, err := json.Marshal(map[string]bool{"exposed": false})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/games/com.example.game/exposed", bytes.NewReader(body))
	w := httptest.NewRecorder()
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d (should be idempotent)\nbody: %s", got, http.StatusOK, w.Body.String())
	}

	updatedGame, err := d.GetGameByPackage("com.example.game")
	if err != nil {
		t.Fatalf("get game by package: %v", err)
	}
	if updatedGame.Exposed {
		t.Error("game should remain hidden after idempotent toggle off")
	}
}

func TestSetupRouter_ExposedToggleRouteRegistered(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	game := types.GameEntry{
		ReleaseName: "com.example.game",
		GameName:    "Example Game",
		PackageName: "com.example.game",
		VersionCode: 42,
		SizeBytes:   1024,
		Hash:        "testhash",
	}
	insertTestGame(t, d, game)

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	// R11-HIGH-4: game routes are now ONLY mounted when a session store exists.
	// The previous test passed sessionStore=nil and gameDB=non-nil, asserting
	// the route was reachable. That wiring exposed the route without auth.
	// The new contract: a real session store is required to mount protected
	// routes, mirroring the production main.go wiring.
	sessionStore := newTestSessionStore(t)
	router := SetupRouter(modeVal, t.TempDir(), d, nil, sessionStore, nil, nil, nil, nil, nil)

	// M-06 (review 2026-06-11): this test asserts the route IS
	// registered. We intentionally do NOT attach a session cookie
	// — the auth middleware should block us with 302/401. The CSRF
	// check would also kick in IF auth passed, but auth comes first.
	body, err := json.Marshal(map[string]bool{"exposed": false})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/admin/api/games/com.example.game/exposed", bytes.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Without a valid session cookie, the middleware redirects (302) or returns
	// 401. The previous expectation (200) was a result of the unauthenticated
	// pass-through defect.
	if got := w.Code; got != http.StatusFound && got != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d or %d (route registered, auth required)\nbody: %s",
			got, http.StatusFound, http.StatusUnauthorized, w.Body.String())
	}
}

func TestHandleExposedTogglePATCH_ResponseContentType(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	game := types.GameEntry{
		ReleaseName: "com.example.game",
		GameName:    "Example Game",
		PackageName: "com.example.game",
		VersionCode: 42,
		SizeBytes:   1024,
		Hash:        "testhash",
	}
	insertTestGame(t, d, game)

	router, session := newExposedToggleRouter(t, d)

	body, err := json.Marshal(map[string]bool{"exposed": false})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/games/com.example.game/exposed", bytes.NewReader(body))
	w := httptest.NewRecorder()
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusOK, w.Body.String())
	}

	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
}

func TestHandleExposedTogglePATCH_ErrorResponseFormat(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	router, session := newExposedToggleRouter(t, d)

	body, err := json.Marshal(map[string]bool{"exposed": false})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/games/com.nonexistent/exposed", bytes.NewReader(body))
	w := httptest.NewRecorder()
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusNotFound {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusNotFound, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing error field")
	}

	if _, ok := errObj["message"]; !ok {
		t.Error("error should have message field")
	}

	if _, ok := errObj["code"]; !ok {
		t.Error("error should have code field")
	}
}

// TestAdminHandler_DBErrorCodes_Consistent is a static-analysis regression
// test for debt-triage-2026-06-06 C-17. It greps every admin*.go file in
// the package for any writeError(...) call whose error code is a
// DB_*_ERROR variant other than the canonical "DATABASE_ERROR". If a new
// variant is introduced (e.g., DB_UPDATE_ERROR, DB_DELETE_ERROR), this
// test fails immediately, forcing the author to either use the canonical
// code or update this test with explicit justification.
//
// Coverage: all admin*.go files in the same package (admin.go,
// admin_settings.go, admin_keys.go, admin_games.go, etc.). The regex
// uses (?s) + `[\s\S]*?` so it matches across newlines (a multiline
// writeError call would still be caught).
//
// Note: this is a source-grep test, not a behavioral test. It runs in
// <1ms and depends only on the package's source files being readable.
func TestAdminHandler_DBErrorCodes_Consistent(t *testing.T) {
	// Glob all admin*.go files in the current package directory.
	matches, err := filepath.Glob("admin*.go")
	if err != nil {
		t.Fatalf("glob admin*.go: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no admin*.go files found in package directory")
	}

	// (?s) flag enables dotall mode so . matches newlines.
	// [\s\S]*? is used instead of [^)]*? to allow newlines AND nested parens.
	re := regexp.MustCompile(`(?s)writeError\(\s*[\s\S]*?"(DB_[A-Z_]+_ERROR|DATABASE_ERROR)"`)

	var nonCanonical []string
	totalMatches := 0
	for _, filename := range matches {
		src, err := os.ReadFile(filename)
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}

		matchIndices := re.FindAllStringSubmatchIndex(string(src), -1)
		totalMatches += len(matchIndices)

		for _, idx := range matchIndices {
			// idx[2:4] is the submatch for the captured code (group 1)
			code := string(src[idx[2]:idx[3]])
			if code != "DATABASE_ERROR" {
				// Compute the line number from the byte offset of the code literal
				line := strings.Count(string(src[:idx[2]]), "\n") + 1
				nonCanonical = append(nonCanonical, filename+": "+code+" (line "+itoa(line)+")")
			}
		}
	}

	if totalMatches == 0 {
		t.Fatal("no DB error codes found in admin*.go files (regex may need updating if writeError signature changed)")
	}

	if len(nonCanonical) > 0 {
		t.Errorf("found non-canonical DB error codes in admin package: %v\nCanonical: DATABASE_ERROR (debt-triage-2026-06-06 C-17)", nonCanonical)
	}
}

// itoa is a tiny helper to avoid pulling in strconv for a single use.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
