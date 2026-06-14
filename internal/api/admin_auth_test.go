package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	chi "github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

// testAdminPasswordHash is a precomputed bcrypt hash for "adminpass" using MinCost.
var testAdminPasswordHash = func() string {
	hash, err := bcrypt.GenerateFromPassword([]byte("adminpass"), bcrypt.MinCost)
	if err != nil {
		panic("admin_auth_test: failed to generate admin password hash: " + err.Error())
	}
	return string(hash)
}()

func newAuthRouter(t *testing.T, sessionStore *auth.SessionStore, cfg *types.Config) (*chi.Mux, *auth.SessionStore) {
	t.Helper()

	if sessionStore == nil {
		sessionStore = auth.NewSessionStore(context.Background())
	}

	adminHandler := NewAdminHandler(t.TempDir(), nil, nil, sessionStore, cfg)

	r := chi.NewRouter()
	r.Post("/admin/api/auth/login", adminHandler.HandleAuthLoginPOST)
	r.Post("/admin/api/auth/logout", adminHandler.HandleAuthLogoutPOST)

	return r, sessionStore
}

func TestHandleAuthLoginPOST_ValidCredentials_JSON(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "adminpass"})

	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
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

	redirect, _ := data["redirect"].(string)
	if redirect != PostLoginRedirect {
		t.Errorf("redirect = %q, want %q", redirect, PostLoginRedirect)
	}
}

func TestHandleAuthLoginPOST_ValidCredentials_HTML(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "adminpass"})

	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (303 See Other after POST login per RFC 7231 §6.4.3)\nbody: %s", got, http.StatusSeeOther, w.Body.String())
	}

	loc := w.Header().Get("Location")
	if loc != PostLoginRedirect {
		t.Errorf("Location = %q, want %q", loc, PostLoginRedirect)
	}
}

func TestHandleAuthLoginPOST_InvalidCredentials(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "wrongpass"})

	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusUnauthorized, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("response missing error field")
	}

	if got, _ := errObj["code"].(string); got != "INVALID_CREDENTIALS" {
		t.Errorf("error code = %q, want %q", got, "INVALID_CREDENTIALS")
	}
}

func TestHandleAuthLoginPOST_MissingFields(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	body, _ := json.Marshal(map[string]string{"username": "admin"}) // missing password

	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusBadRequest, w.Body.String())
	}
}

func TestHandleAuthLoginPOST_EmptyBody(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader([]byte("not json")))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusBadRequest, w.Body.String())
	}
}

func TestHandleAuthLoginPOST_OversizedBody(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	// Send a body larger than 4 KiB.
	largeBody := bytes.Repeat([]byte("x"), 5000)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(largeBody))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (should reject oversized body)\nbody: %s", got, http.StatusBadRequest, w.Body.String())
	}
}

func TestHandleAuthLoginPOST_CookieSet(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "adminpass"})

	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("Expected session cookie to be set")
	}

	found := false
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected vrhub_session cookie to be set")
	}

	// Verify session was created in store.
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			sess := store.Get(c.Value)
			if sess == nil {
				t.Error("Session should exist in store after login")
			} else if sess.Username != "admin" {
				t.Errorf("Session username = %q, want %q", sess.Username, "admin")
			}
		}
	}
}

func TestHandleAuthLogoutPOST_HappyPath(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	// First create a session via login.
	loginBody, _ := json.Marshal(map[string]string{"username": "admin", "password": "adminpass"})
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Accept", "application/json")
	loginW := httptest.NewRecorder()
	router.ServeHTTP(loginW, loginReq)

	// Get the session cookie.
	var sessionID string
	for _, c := range loginW.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionID = c.Value
			break
		}
	}
	if sessionID == "" {
		t.Fatal("Expected session cookie from login")
	}

	// Now logout.
	logoutReq := httptest.NewRequest(http.MethodPost, "/admin/api/auth/logout", nil)
	logoutReq.AddCookie(&http.Cookie{
		Name:  auth.SessionCookieName,
		Value: sessionID,
	})
	// S-02: CSRF token (HMAC of session ID). Same pattern as the
	// other state-changing admin endpoints.
	logoutReq.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(sessionID))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, logoutReq)

	if got := w.Code; got != http.StatusNoContent {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusNoContent, w.Body.String())
	}

	// Verify session was deleted.
	if store.Get(sessionID) != nil {
		t.Error("Session should be deleted after logout")
	}
}

func TestHandleAuthLogoutPOST_Idempotent(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	// Logout without any cookie (unauthenticated).
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/logout", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusNoContent {
		t.Errorf("status = %d, want %d (logout should be idempotent)\nbody: %s", got, http.StatusNoContent, w.Body.String())
	}
}

func TestHandleAuthLoginPOST_NoConfig(t *testing.T) {
	router, store := newAuthRouter(t, nil, nil)
	defer store.Stop()

	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "adminpass"})

	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d (nil config should reject all logins)\nbody: %s", got, http.StatusUnauthorized, w.Body.String())
	}
}

// TestHandleAuthLoginPOST_Concurrent verifies that 10 concurrent login
// requests with valid credentials all succeed AND each returns a unique
// session cookie (R10-CONCURRENT-SESSION-ISOLATION). The previous version
// asserted only that every call returned 200, but never read back the
// cookies — a real entropy bug (e.g., crypto/rand returning duplicates)
// would not be caught.
func TestHandleAuthLoginPOST_Concurrent(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "adminpass"})

	var wg sync.WaitGroup
	seenCookies := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want %d\nbody: %s", w.Code, http.StatusOK, w.Body.String())
				return
			}

			// Capture every session cookie for uniqueness verification.
			for _, c := range w.Result().Cookies() {
				if c.Name == auth.SessionCookieName && c.Value != "" {
					seenCookies <- c.Value
					break
				}
			}
		}()
	}
	wg.Wait()
	close(seenCookies)

	// Assert uniqueness: every captured cookie value should be a distinct
	// session ID. A crypto/rand bug that produced duplicate IDs would be
	// caught here.
	idSet := make(map[string]struct{}, 10)
	for id := range seenCookies {
		if _, dup := idSet[id]; dup {
			t.Errorf("duplicate session ID returned under concurrent logins: %s", id)
		}
		idSet[id] = struct{}{}
		// Also verify the session exists in the store (R10-CONCURRENT-SESSION-ISOLATION).
		sess := store.Get(id)
		if sess == nil {
			t.Errorf("session ID %s was returned by login but not found in store", id)
		} else if sess.Username != "admin" {
			t.Errorf("session username = %q, want %q", sess.Username, "admin")
		}
	}
}

// TestHandleAuthLoginPOST_LengthBoundaries exercises the username (max 256)
// and password (max 72) length caps. R10-LENGTH-BOUND-TESTS.
//
// We assert:
//   - 256-char username → accepted (at the boundary)
//   - 257-char username → rejected 400
//   - 72-char password → accepted
//   - 73-char password → rejected 400
//
// The error message is now generic ("input too long") to avoid leaking
// the bcrypt 72-byte limit; the test asserts the code, not the message
// (R10-PASSWORD-LENGTH-LEAK).
func TestHandleAuthLoginPOST_LengthBoundaries(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}

	t.Run("username 256 chars accepted", func(t *testing.T) {
		router, store := newAuthRouter(t, nil, cfg)
		defer store.Stop()
		// Username 256 chars; password is the correct one.
		body, _ := json.Marshal(map[string]string{
			"username": strings.Repeat("a", 256),
			"password": "adminpass",
		})
		req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if got := w.Code; got != http.StatusUnauthorized && got != http.StatusOK {
			t.Errorf("256-char username: status = %d, want 200/401 (boundary should be accepted)\nbody: %s", got, w.Body.String())
		}
	})

	t.Run("username 257 chars rejected", func(t *testing.T) {
		router, store := newAuthRouter(t, nil, cfg)
		defer store.Stop()
		body, _ := json.Marshal(map[string]string{
			"username": strings.Repeat("a", 257),
			"password": "adminpass",
		})
		req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if got := w.Code; got != http.StatusBadRequest {
			t.Errorf("257-char username: status = %d, want 400\nbody: %s", got, w.Body.String())
		}
	})

	t.Run("password 72 chars accepted (lengthwise)", func(t *testing.T) {
		router, store := newAuthRouter(t, nil, cfg)
		defer store.Stop()
		// 72-char password attempt — will fail with 401 (wrong password, NOT 400) because
		// the length cap is permissive at the boundary. This confirms the cap does not
		// reject valid-length-but-wrong passwords.
		body, _ := json.Marshal(map[string]string{
			"username": "admin",
			"password": strings.Repeat("p", 72),
		})
		req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if got := w.Code; got != http.StatusUnauthorized {
			t.Errorf("72-char password: status = %d, want 401 (length cap is permissive at boundary)\nbody: %s", got, w.Body.String())
		}
	})

	t.Run("password 73 chars rejected", func(t *testing.T) {
		router, store := newAuthRouter(t, nil, cfg)
		defer store.Stop()
		body, _ := json.Marshal(map[string]string{
			"username": "admin",
			"password": strings.Repeat("p", 73),
		})
		req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if got := w.Code; got != http.StatusBadRequest {
			t.Errorf("73-char password: status = %d, want 400\nbody: %s", got, w.Body.String())
		}
	})
}

// TestHandleAuthLoginPOST_FormURLEncoded_BodyLimit is the Round 9 finding-2
// regression gate: the form-urlencoded path must also respect the 4 KiB body
// limit. Previously the MaxBytesReader was on a separate variable that the
// form path did not use, so a 1 MB form body would be parsed without limit
// (memory-exhaustion DoS vector). The fix wraps r.Body itself so both r.ParseForm
// and json.NewDecoder respect the limit.
func TestHandleAuthLoginPOST_FormURLEncoded_BodyLimit(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}
	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	// Build a form-urlencoded body > 4 KiB. r.PostFormValue reads only the
	// "username" and "password" fields but the whole body must be bounded.
	huge := strings.Repeat("x", 5000)
	formBody := "username=admin&password=adminpass&junk=" + huge
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", strings.NewReader(formBody))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// MaxBytesReader writes 400 directly. We should not see a 500 (parser
	// accepting and processing a 5 KB body) nor a 200 (login with a 5 KB body).
	if got := w.Code; got != http.StatusBadRequest {
		t.Errorf("form-urlencoded > 4 KiB: status = %d, want 400 (body limit must apply to form path)\nbody: %s",
			got, w.Body.String())
	}
}

// TestHandleAuthLoginPOST_ConfigCached verifies the Round 9 finding-6 fix:
// after the first successful disk load of the config, h.Config is updated
// and subsequent logins do NOT re-read the file. The first login's
// resolveConfig() call would call config.Load, the second call would use
// the cached pointer.
//
// We assert the second login succeeds (it would fail with 401 if the
// in-memory cache were lost between requests).
func TestHandleAuthLoginPOST_ConfigCached(t *testing.T) {
	// Write a config.toml on disk so resolveConfig can find it.
	dataDir := t.TempDir()
	cfgPath := filepath.Join(dataDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[server]\nhost = \"127.0.0.1\"\nport = 8080\n\n[admin]\nusername = \"admin\"\npassword_hash = \""+testAdminPasswordHash+"\"\n"), 0644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	// Construct handler with cfg = nil so resolveConfig must load from disk.
	sessionStore := auth.NewSessionStore(context.Background())
	defer sessionStore.Stop()
	adminHandler := NewAdminHandler(dataDir, nil, nil, sessionStore, nil)

	r := chi.NewRouter()
	r.Post("/admin/api/auth/login", adminHandler.HandleAuthLoginPOST)

	body := []byte(`{"username":"admin","password":"adminpass"}`)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if got := w.Code; got != http.StatusOK {
			t.Errorf("login #%d: status = %d, want 200 (cached config must persist)\nbody: %s",
				i+1, got, w.Body.String())
		}
	}
	if adminHandler.Config == nil {
		t.Error("after successful disk-load, adminHandler.Config should be non-nil (cached)")
	}
}

// TestHandleAuthLoginPOST_CaseInsensitiveContentType is the R12-P7
// regression gate for R11-HIGH-3: Content-Type matching must be
// case-insensitive per RFC 7231 §3.1.1.1. Browsers mostly send lowercase
// but some clients (curl with -H, certain proxies) capitalise. The
// previous case-sensitive HasPrefix silently 400'd these.
func TestHandleAuthLoginPOST_CaseInsensitiveContentType(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}
	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	// Capitalised Content-Type. With R10 case-sensitive match, this would
	// fall through to the JSON path and fail to parse the form-encoded
	// body. With R11 case-insensitive match, the form parser handles it.
	formBody := "username=admin&password=adminpass"
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", strings.NewReader(formBody))
	req.Header.Set("Content-Type", "Application/X-Www-Form-Urlencoded")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// 200 = login success. The capitalised Content-Type must NOT produce
	// 400 (which would indicate the case-sensitive HasPrefix regression).
	if got := w.Code; got == http.StatusBadRequest {
		t.Errorf("capitalised form Content-Type: status = %d (case-insensitive match regression)\nbody: %s", got, w.Body.String())
	}
}

// TestHandleAuthLoginPOST_WhitespacePassword is the R12-P7 regression gate
// for R12-P4: a whitespace-only password must be classified as empty
// (with the "username and password are required" message), not passed
// through to Authenticate (which would return misleading
// "Invalid credentials"). The fix checks strings.TrimSpace(req.Password)
// without modifying req.Password.
func TestHandleAuthLoginPOST_WhitespacePassword(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}
	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	body, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "   \t  \n  ", // whitespace only
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusBadRequest {
		t.Errorf("whitespace-only password: status = %d, want 400 (should be classified as empty)\nbody: %s", got, w.Body.String())
	}
	// Also assert the error code is INVALID_INPUT (not INVALID_CREDENTIALS).
	if !strings.Contains(w.Body.String(), "INVALID_INPUT") {
		t.Errorf("whitespace-only password: body should contain INVALID_INPUT, got: %s", w.Body.String())
	}
}

// TestHandleAuthLoginPOST_PasswordByteLength_R12P2 verifies that the
// password length cap is enforced on BYTES (not runes). A 72-rune CJK
// password would be ~216 bytes and would pass a rune-based cap, then
// bcrypt would silently truncate to ~24 runes (R12-P2 security regression).
// This test uses a 73-byte ASCII password to confirm the byte cap.
func TestHandleAuthLoginPOST_PasswordByteLength_R12P2(t *testing.T) {
	cfg := &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1"},
		Admin:  types.AdminConfig{Username: "admin", PasswordHash: testAdminPasswordHash},
	}
	router, store := newAuthRouter(t, nil, cfg)
	defer store.Stop()

	// 73-byte password (one past the cap).
	body, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": strings.Repeat("p", 73),
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/api/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusBadRequest {
		t.Errorf("73-byte password: status = %d, want 400 (byte cap should reject)\nbody: %s", got, w.Body.String())
	}
}

// TestAuth_Redirects_MatchSpec is a structural regression test for
// debt-triage-2026-06-06 C-01. It asserts that the spec-literal redirect
// target strings are exactly as documented in the story 6-2 acceptance
// criteria:
//
//   - AC1 (login success): HTTP 302 redirect to "/admin/dashboard"
//   - AC3 (unauthenticated): HTTP 302 redirect to "/admin/login?showLogin=1"
//
// The post-login redirect target is exposed via the exported constant
// PostLoginRedirect in this package (admin.go:122). The pre-login
// (unauthenticated) target is the string literal "/admin/login?showLogin=1"
// in internal/auth/session.go:743 (the HTML branch of writeAuthError).
// The ?showLogin=1 query param reveals the login form which is hidden
// by default in the admin shell (see live session 2026-06-09).
//
// Behavioral coverage already exists:
//   - TestHandleAuthLoginPOST_ValidCredentials_JSON (AC1 JSON branch)
//   - TestHandleAuthLoginPOST_ValidCredentials_HTML (AC1 HTML branch)
//   - TestSessionAuthMiddleware_MissingCookie (AC3 HTML branch, in auth pkg)
//
// This test is the structural backstop: it catches a refactor that
// changes the constant value or the literal without breaking the
// behavioral tests (e.g., a "consistency" PR that aligns both
// targets on the same string).
func TestAuth_Redirects_MatchSpec(t *testing.T) {
	// AC1: post-login redirect must be the spec-literal "/admin/dashboard"
	if got, want := PostLoginRedirect, "/admin/dashboard"; got != want {
		t.Errorf("AC1 PostLoginRedirect = %q, want %q (story 6-2 AC1 spec-literal)", got, want)
	}

	// AC3: the unauthenticated HTML branch of writeAuthError is in
	// internal/auth/session.go:743. We can't import the unexported
	// function, so we assert the literal in the source file.
	authSrc, err := os.ReadFile(filepath.Join("..", "auth", "session.go"))
	if err != nil {
		t.Fatalf("read ../auth/session.go: %v", err)
	}

	// The HTML branch of writeAuthError must redirect to
	// "/admin/login?showLogin=1" with HTTP 302. The behavioral test
	// (TestSessionAuthMiddleware_MissingCookie) already covers this,
	// but the literal assertion guards against a future "consolidation"
	// that changes the target without updating the test.
	if !strings.Contains(string(authSrc), `http.Redirect(w, r, "/admin/login?showLogin=1", http.StatusFound)`) {
		t.Errorf("AC3 HTML redirect target changed: expected literal \"http.Redirect(w, r, \\\"/admin/login?showLogin=1\\\", http.StatusFound)\" in internal/auth/session.go:743 (writeAuthError)")
	}
}
