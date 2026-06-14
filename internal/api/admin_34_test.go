package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	chi "github.com/go-chi/chi/v5"
)

func newGamesListRouter(t *testing.T, d *db.DB) *chi.Mux {
	t.Helper()
	r := chi.NewRouter()
	adminHandler := NewAdminHandler(t.TempDir(), nil, d, nil, nil)
	r.Get("/api/games", adminHandler.HandleGamesListGET)
	return r
}

func TestHandleGamesListGET_Empty(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	router := newGamesListRouter(t, d)

	req := httptest.NewRequest(http.MethodGet, "/api/games", nil)
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

	gamesArr, ok := data["games"].([]interface{})
	if !ok {
		t.Fatal("games should be an array")
	}

	count, _ := data["count"].(float64)
	if int(count) != 0 {
		t.Errorf("count = %d, want 0", int(count))
	}
	if len(gamesArr) != 0 {
		t.Errorf("games length = %d, want 0", len(gamesArr))
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", contentType, "application/json")
	}
}

func TestHandleGamesListGET_WithGames(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	game1 := types.GameEntry{
		ReleaseName: "com.example.game1",
		GameName:    "Game One",
		PackageName: "com.example.game1",
		VersionCode: 10,
		SizeBytes:   52428800,
		Hash:        "hash1",
		Exposed:     true,
		Corrupted:   false,
	}
	game2 := types.GameEntry{
		ReleaseName: "com.example.game2",
		GameName:    "Game Two",
		PackageName: "com.example.game2",
		VersionCode: 20,
		SizeBytes:   104857600,
		Hash:        "hash2",
		Exposed:     false,
		Corrupted:   false,
	}
	game3 := types.GameEntry{
		ReleaseName: "com.example.game3",
		GameName:    "Game Three",
		PackageName: "com.example.game3",
		VersionCode: 30,
		SizeBytes:   209715200,
		Hash:        "hash3",
		Exposed:     true,
		Corrupted:   true,
	}

	insertTestGame(t, d, game1)
	insertTestGame(t, d, game2)
	insertTestGame(t, d, game3)

	router := newGamesListRouter(t, d)

	req := httptest.NewRequest(http.MethodGet, "/api/games", nil)
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

	count, _ := data["count"].(float64)
	if int(count) != 3 {
		t.Errorf("count = %d, want 3", int(count))
	}

	gamesArr, ok := data["games"].([]interface{})
	if !ok {
		t.Fatal("games should be an array")
	}
	if len(gamesArr) != 3 {
		t.Fatalf("games length = %d, want 3", len(gamesArr))
	}

	gameNames := make(map[string]bool)
	for _, raw := range gamesArr {
		g := raw.(map[string]interface{})
		name := g["game_name"].(string)
		gameNames[name] = true

		status := g["status"].(string)
		corrupted := g["corrupted"].(bool)
		exposed := g["exposed"].(bool)

		expectedStatus := "ok"
		if corrupted {
			expectedStatus = "corrupted"
		} else if !exposed {
			expectedStatus = "excluded"
		}

		if status != expectedStatus {
			t.Errorf("game %s: status = %q, want %q (corrupted=%v, exposed=%v)",
				g["package_name"], status, expectedStatus, corrupted, exposed)
		}
	}

	for _, expected := range []string{"Game One", "Game Two", "Game Three"} {
		if !gameNames[expected] {
			t.Errorf("missing game %q in response", expected)
		}
	}
}

func TestHandleGamesListGET_GameFields(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	game := types.GameEntry{
		ReleaseName:  "com.test.game",
		GameName:     "Test Game",
		PackageName:  "com.test.game",
		VersionCode:  99,
		SizeBytes:    12345678,
		OBBSizeBytes: 8765432,
		Hash:         "testhash",
		Exposed:      true,
		Corrupted:    false,
	}
	insertTestGame(t, d, game)

	router := newGamesListRouter(t, d)

	req := httptest.NewRequest(http.MethodGet, "/api/games", nil)
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

	gamesArr := data["games"].([]interface{})
	g := gamesArr[0].(map[string]interface{})

	if got, _ := g["game_id"].(float64); got == 0 {
		t.Error("game_id should be non-zero")
	}

	if got, _ := g["release_name"].(string); got != "com.test.game" {
		t.Errorf("release_name = %q, want %q", got, "com.test.game")
	}

	if got, _ := g["game_name"].(string); got != "Test Game" {
		t.Errorf("game_name = %q, want %q", got, "Test Game")
	}

	if got, _ := g["package_name"].(string); got != "com.test.game" {
		t.Errorf("package_name = %q, want %q", got, "com.test.game")
	}

	if got, _ := g["version_code"].(float64); int64(got) != 99 {
		t.Errorf("version_code = %d, want 99", int64(got))
	}

	if got, _ := g["size_bytes"].(float64); int64(got) != 12345678 {
		t.Errorf("size_bytes = %d, want 12345678", int64(got))
	}

	if got, _ := g["obb_size_bytes"].(float64); int64(got) != 8765432 {
		t.Errorf("obb_size_bytes = %d, want 8765432", int64(got))
	}

	if got, _ := g["exposed"].(bool); got != true {
		t.Errorf("exposed = %v, want true", got)
	}

	if got, _ := g["corrupted"].(bool); got != false {
		t.Errorf("corrupted = %v, want false", got)
	}

	if got, _ := g["status"].(string); got != "ok" {
		t.Errorf("status = %q, want %q", got, "ok")
	}

	lastUpdated := g["last_updated"].(string)
	if lastUpdated == "" {
		t.Error("last_updated should not be empty")
	}
}

func newDeleteRouter(t *testing.T, d *db.DB) *chi.Mux {
	t.Helper()
	r := chi.NewRouter()
	adminHandler := NewAdminHandler(t.TempDir(), nil, d, nil, nil)
	r.Delete("/api/games/{releaseName}", adminHandler.HandleGameDeleteDELETE)
	return r
}

func TestHandleGameDeleteDELETE_Success(t *testing.T) {
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

	router := newDeleteRouter(t, d)

	req := httptest.NewRequest(http.MethodDelete, "/api/games/com.example.game", nil)
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

	if got, _ := data["message"].(string); got == "" {
		t.Error("message should not be empty")
	}

	if got, _ := data["package_name"].(string); got != "com.example.game" {
		t.Errorf("package_name = %q, want %q", got, "com.example.game")
	}

	_, err := d.GetGameByPackage("com.example.game")
	if err == nil {
		t.Error("game should be deleted from database")
	}
}

func TestHandleGameDeleteDELETE_GameNotFound(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	router := newDeleteRouter(t, d)

	req := httptest.NewRequest(http.MethodDelete, "/api/games/com.nonexistent.game", nil)
	w := httptest.NewRecorder()
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

func TestHandleGameDeleteDELETE_MissingPackageName(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	router := newDeleteRouter(t, d)

	req := httptest.NewRequest(http.MethodDelete, "/api/games/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusNotFound {
		t.Errorf("status = %d, want %d (Chi does not match empty path segment)\nbody: %s", got, http.StatusNotFound, w.Body.String())
	}
}

func TestHandleGameDeleteDELETE_ResponseContentType(t *testing.T) {
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

	router := newDeleteRouter(t, d)

	req := httptest.NewRequest(http.MethodDelete, "/api/games/com.example.game", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusOK {
		t.Errorf("status = %d, want %d\nbody: %s", got, http.StatusOK, w.Body.String())
	}

	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
}

func TestSetupRouter_GamesListRouteRegistered(t *testing.T) {
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

	// R11-HIGH-4: game routes are only mounted when a session store exists
	// (otherwise they would be exposed without auth). Use a real session
	// store to mirror production wiring.
	sessionStore := newTestSessionStore(t)
	router := SetupRouter(modeVal, t.TempDir(), d, nil, sessionStore, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/games", nil)
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

func TestSetupRouter_GameDeleteRouteRegistered(t *testing.T) {
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

	// R11-HIGH-4: see TestSetupRouter_GamesListRouteRegistered.
	sessionStore := newTestSessionStore(t)
	router := SetupRouter(modeVal, t.TempDir(), d, nil, sessionStore, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/admin/api/games/com.example.game", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got := w.Code; got != http.StatusFound && got != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d or %d (route registered, auth required)\nbody: %s",
			got, http.StatusFound, http.StatusUnauthorized, w.Body.String())
	}
}
