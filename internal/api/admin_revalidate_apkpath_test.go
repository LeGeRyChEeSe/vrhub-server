package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	chi "github.com/go-chi/chi/v5"
)

// Revalidate/corruption-status must resolve the game file from the
// DB's apk_path (Story 9.10) before falling back to the legacy
// {DataDir}/{package}.apk lookup. A game imported via game_folders
// (apk outside DataDir) previously returned 400 FILE_NOT_FOUND and
// was wrongly marked unexposed.

func newRevalidateRouter(t *testing.T, dataDir string, d *db.DB) (*chi.Mux, *auth.Session) {
	t.Helper()
	r := chi.NewRouter()
	store := auth.NewSessionStore(context.Background())
	t.Cleanup(store.Stop)
	adminHandler := NewAdminHandler(dataDir, nil, d, store, nil)
	session := adminHandler.SessionStore.Create("test-admin")
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.InjectSessionForTest(req.Context(), session)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Post("/api/games/{releaseName}/revalidate", adminHandler.HandleRevalidatePOST)
	r.Get("/api/games/{releaseName}/corruption-status", adminHandler.HandleCorruptionStatusGET)
	return r, session
}

func writeMinimalZip(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte("<manifest/>")); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
}

func TestHandleRevalidatePOST_ApkPathOutsideDataDir_Succeeds(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	dataDir := t.TempDir()
	externalDir := t.TempDir()
	apkPath := filepath.Join(externalDir, "My Game.apk")
	writeMinimalZip(t, apkPath)

	insertTestGame(t, d, types.GameEntry{
		ReleaseName: "com.example.foldergame",
		GameName:    "Folder Game",
		PackageName: "com.example.foldergame",
		VersionCode: 7,
		SizeBytes:   1024,
		Hash:        "testhash1",
		Exposed:     true,
		ApkPath:     apkPath,
	})

	router, session := newRevalidateRouter(t, dataDir, d)

	req := httptest.NewRequest(http.MethodPost, "/api/games/com.example.foldergame/revalidate", nil)
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (apk_path outside DataDir must be resolvable)\nbody: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("response missing data field: %s", w.Body.String())
	}
	if corrupted, _ := data["corrupted"].(bool); corrupted {
		t.Errorf("corrupted = true, want false (valid zip at apk_path)")
	}

	g, err := d.GetGameByPackage("com.example.foldergame")
	if err != nil {
		t.Fatalf("get game: %v", err)
	}
	if !g.Exposed {
		t.Errorf("game marked unexposed despite valid apk_path file")
	}
	if g.ApkPath != apkPath {
		t.Errorf("apk_path changed to %q, want %q", g.ApkPath, apkPath)
	}
}

func TestHandleCorruptionStatusGET_ApkPathOutsideDataDir_ReturnsRealPath(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	dataDir := t.TempDir()
	externalDir := t.TempDir()
	apkPath := filepath.Join(externalDir, "game.apk")
	writeMinimalZip(t, apkPath)

	insertTestGame(t, d, types.GameEntry{
		ReleaseName: "com.example.status",
		GameName:    "Status Game",
		PackageName: "com.example.status",
		VersionCode: 3,
		SizeBytes:   2048,
		Hash:        "testhash2",
		Exposed:     true,
		ApkPath:     apkPath,
	})

	router, _ := newRevalidateRouter(t, dataDir, d)

	req := httptest.NewRequest(http.MethodGet, "/api/games/com.example.status/corruption-status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("response missing data field")
	}
	if fp, _ := data["file_path"].(string); fp != apkPath {
		t.Errorf("file_path = %q, want %q", fp, apkPath)
	}
}

func TestHandleRevalidatePOST_LegacyApkPathEmpty_StillResolves(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	dataDir := t.TempDir()
	legacyPath := filepath.Join(dataDir, "com.example.legacy.apk")
	writeMinimalZip(t, legacyPath)

	insertTestGame(t, d, types.GameEntry{
		ReleaseName: "com.example.legacy",
		GameName:    "Legacy Game",
		PackageName: "com.example.legacy",
		VersionCode: 1,
		SizeBytes:   512,
		Hash:        "testhash3",
		Exposed:     true,
		// ApkPath deliberately empty (pre-9.10 row)
	})

	router, session := newRevalidateRouter(t, dataDir, d)

	req := httptest.NewRequest(http.MethodPost, "/api/games/com.example.legacy/revalidate", nil)
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (legacy DataDir lookup must be preserved)\nbody: %s", w.Code, w.Body.String())
	}
}

func TestHandleRevalidatePOST_FileTrulyMissing_Stays404Unexposed(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	dataDir := t.TempDir()
	ghostPath := filepath.Join(t.TempDir(), "gone.apk")

	insertTestGame(t, d, types.GameEntry{
		ReleaseName: "com.example.ghost",
		GameName:    "Ghost Game",
		PackageName: "com.example.ghost",
		VersionCode: 9,
		SizeBytes:   128,
		Hash:        "testhash4",
		Exposed:     true,
		ApkPath:     ghostPath,
	})

	router, session := newRevalidateRouter(t, dataDir, d)

	req := httptest.NewRequest(http.MethodPost, "/api/games/com.example.ghost/revalidate", nil)
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (Fix #9 missing-file contract preserved)\nbody: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errObj, _ := resp["error"].(map[string]interface{})
	if code, _ := errObj["code"].(string); code != "FILE_NOT_FOUND" {
		t.Errorf("error code = %q, want FILE_NOT_FOUND", code)
	}

	g, err := d.GetGameByPackage("com.example.ghost")
	if err != nil {
		t.Fatalf("get game: %v", err)
	}
	if g.Exposed {
		t.Errorf("game must be marked unexposed when file is missing")
	}
}

var _ = zip.NewWriter
