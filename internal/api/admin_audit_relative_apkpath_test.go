package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// Audit fix (2026-11), part 1: games imported via game_folders can have a
// RELATIVE games.apk_path (relative to the scan directory, which the public
// serving layer resolves against DataDir). os.Stat on such a value looks in
// the process CWD instead, so revalidate used to see a "missing" file and
// flip a perfectly downloadable game to unexposed. resolveGameFilePath must
// therefore try DataDir before the legacy search.

func TestResolveGameFilePath_RelativeApkPath_ResolvesAgainstDataDir(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	dataDir := t.TempDir()
	// The scanner stored a path relative to the scan dir (DataDir here).
	relPath := filepath.Join("games", "relative game.apk")
	writeMinimalZip(t, filepath.Join(dataDir, relPath))

	insertTestGame(t, d, types.GameEntry{
		ReleaseName: "com.example.relative",
		GameName:    "Relative Game",
		PackageName: "com.example.relative",
		VersionCode: 4,
		SizeBytes:   1024,
		Hash:        "testhashrel",
		Exposed:     true,
		ApkPath:     relPath,
	})

	h := NewAdminHandler(dataDir, nil, d, newTestSessionStore(t), nil)
	entry, err := h.DB.GetGameByPackage("com.example.relative")
	if err != nil {
		t.Fatalf("get game: %v", err)
	}

	got := h.resolveGameFilePath(entry)
	if got == "" {
		t.Fatal("resolveGameFilePath returned an empty path for a relative apk_path that exists under DataDir")
	}
	want := filepath.Join(dataDir, relPath)
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Errorf("resolved path = %q, want %q", got, want)
	}
	if _, statErr := os.Stat(got); statErr != nil {
		t.Errorf("resolved path is not stat-able: %v", statErr)
	}
}

func TestHandleRevalidatePOST_RelativeApkPath_StaysExposed(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	dataDir := t.TempDir()
	relPath := "relative game.apk"
	writeMinimalZip(t, filepath.Join(dataDir, relPath))

	insertTestGame(t, d, types.GameEntry{
		ReleaseName: "com.example.relatrevalidate",
		GameName:    "Relative Revalidate Game",
		PackageName: "com.example.relatrevalidate",
		VersionCode: 4,
		SizeBytes:   1024,
		Hash:        "testhashrel2",
		Exposed:     true,
		ApkPath:     relPath,
	})

	router, session := newRevalidateRouter(t, dataDir, d)

	req := httptest.NewRequest(http.MethodPost, "/api/games/com.example.relatrevalidate/revalidate", nil)
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(session.ID))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (relative apk_path under DataDir must resolve)\nbody: %s", w.Code, w.Body.String())
	}

	g, err := d.GetGameByPackage("com.example.relatrevalidate")
	if err != nil {
		t.Fatalf("get game: %v", err)
	}
	if !g.Exposed {
		t.Errorf("game flipped to unexposed despite a valid relative apk_path under DataDir")
	}
	if g.Corrupted {
		t.Errorf("corrupted = true, want false (valid zip at the resolved path); reason: %q", g.CorruptionReason)
	}
}

func TestHandleCorruptionStatusGET_RelativeApkPath_FoundTrueWithResolvedPath(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	dataDir := t.TempDir()
	relPath := "status relative.apk"
	writeMinimalZip(t, filepath.Join(dataDir, relPath))

	insertTestGame(t, d, types.GameEntry{
		ReleaseName: "com.example.relativestatus",
		GameName:    "Relative Status Game",
		PackageName: "com.example.relativestatus",
		VersionCode: 4,
		SizeBytes:   1024,
		Hash:        "testhashrel3",
		Exposed:     true,
		ApkPath:     relPath,
	})

	router, _ := newRevalidateRouter(t, dataDir, d)

	req := httptest.NewRequest(http.MethodGet, "/api/games/com.example.relativestatus/corruption-status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", w.Code, w.Body.String())
	}
	data := decodeCorruptionStatus(t, w)
	if found, _ := data["found"].(bool); !found {
		t.Errorf("found = false, want true (relative apk_path resolves against DataDir); reason: %v", data["reason"])
	}
	want := filepath.Clean(filepath.Join(dataDir, relPath))
	if fp, _ := data["file_path"].(string); filepath.Clean(fp) != want {
		t.Errorf("file_path = %q, want %q", fp, want)
	}
}

// Audit fix (2026-11), part 2: when the game file cannot be located at all,
// corruption-status used to answer with the DB-cached verdict only. A stored
// `corrupted: false` then read as "checked and fine" although nothing had
// been checked — the file was gone. The endpoint must flag that explicitly
// with found:false + a reason instead of returning a stale all-clear.

func TestHandleCorruptionStatusGET_MissingFile_FoundFalseWithReason(t *testing.T) {
	d, cleanup := setupAdminTestDB(t)
	defer cleanup()

	dataDir := t.TempDir()
	ghostPath := filepath.Join(t.TempDir(), "deleted.apk")

	insertTestGame(t, d, types.GameEntry{
		ReleaseName: "com.example.gone",
		GameName:    "Gone Game",
		PackageName: "com.example.gone",
		VersionCode: 2,
		SizeBytes:   4096,
		Hash:        "testhashgone",
		Exposed:     true,
		// Last DB-known verdict: "not corrupted" — now stale.
		Corrupted: false,
		ApkPath:   ghostPath,
	})

	router, _ := newRevalidateRouter(t, dataDir, d)

	req := httptest.NewRequest(http.MethodGet, "/api/games/com.example.gone/corruption-status", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (contract kept for the admin UI)\nbody: %s", w.Code, w.Body.String())
	}
	data := decodeCorruptionStatus(t, w)

	if found, _ := data["found"].(bool); found {
		t.Error("found = true, want false (no game file exists on disk)")
	}
	if reason, _ := data["reason"].(string); reason == "" {
		t.Error("reason is empty, want an explanation that the corruption fields are stale DB values")
	}
	if fp, _ := data["file_path"].(string); fp != "" {
		t.Errorf("file_path = %q, want empty (nothing was resolved)", fp)
	}
	// The stale verdict may still be reported, but only when `found` says so.
	if _, hasCorrupted := data["corrupted"]; !hasCorrupted {
		t.Error("corrupted field must keep its name/type for UI compatibility")
	}
}

func decodeCorruptionStatus(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, w.Body.String())
	}
	data, _ := resp["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("response missing data field: %s", w.Body.String())
	}
	return data
}
