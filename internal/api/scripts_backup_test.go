package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// newTestAdminHandlerForBackup builds an AdminHandler with DataDir set to a
// temp dir, plus a fully-resolved config so HandleScriptsBackupPOST can
// produce a non-trivial config.toml entry.
func newTestAdminHandlerForBackup(t *testing.T) (*AdminHandler, string) {
	t.Helper()
	dataDir := t.TempDir()
	h := &AdminHandler{DataDir: dataDir}
	h.Config = &types.Config{
		Server: types.ServerConfig{Host: "127.0.0.1", Port: 8080},
		Update: types.UpdateConfig{Enabled: true, AutoApply: false},
	}
	h.BackupSync = &sync.WaitGroup{}
	t.Cleanup(func() { h.BackupSync.Wait() })
	return h, dataDir
}

func TestHandleScriptsBackupPOST_ValidZip(t *testing.T) {
	h, _ := newTestAdminHandlerForBackup(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/scripts/backup", nil)
	rr := httptest.NewRecorder()

	h.HandleScriptsBackupPOST(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type: got %q, want application/zip", got)
	}

	body := rr.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("response body is empty (regression: 7.2 bug — was writing to w instead of zw)")
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("response is not a valid zip: %v", err)
	}

	names := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read entry %s: %v", f.Name, err)
		}
		names[f.Name] = data
	}

	if _, ok := names["config.toml"]; !ok {
		t.Error("backup missing config.toml")
	}
	if len(names["config.toml"]) == 0 {
		t.Error("config.toml entry is empty")
	}
	if _, ok := names["manifest.json"]; !ok {
		t.Error("backup missing manifest.json")
	}
	// manifest must be valid JSON
	var mf struct {
		CreatedAt string   `json:"created_at"`
		Version   string   `json:"version"`
		Contents  []string `json:"contents"`
	}
	if err := json.Unmarshal(names["manifest.json"], &mf); err != nil {
		t.Errorf("manifest.json is not valid JSON: %v", err)
	}
	if mf.Version != "1.0" {
		t.Errorf("manifest.version: got %q, want 1.0", mf.Version)
	}
}

func TestHandleScriptsBackupPOST_IncludesDB(t *testing.T) {
	h, dataDir := newTestAdminHandlerForBackup(t)
	// Plant a fake db file with known content.
	dbContent := []byte("SQLite-format-3-fake-payload-for-test")
	if err := os.WriteFile(filepath.Join(dataDir, "vrhub.db"), dbContent, 0644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/scripts/backup", nil)
	rr := httptest.NewRecorder()
	h.HandleScriptsBackupPOST(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	zr, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}

	var dbBytes []byte
	foundDB := false
	for _, f := range zr.File {
		if f.Name == "vrhub.db" {
			foundDB = true
			rc, _ := f.Open()
			dbBytes, _ = io.ReadAll(rc)
			rc.Close()
		}
	}
	if !foundDB {
		t.Fatal("backup missing vrhub.db entry (spec 7.2 AC2)")
	}
	if !bytes.Equal(dbBytes, dbContent) {
		t.Errorf("vrhub.db content mismatch:\n  got:  %q\n  want: %q", dbBytes, dbContent)
	}
}

func TestHandleScriptsBackupPOST_DBMissing_StillSucceeds(t *testing.T) {
	h, _ := newTestAdminHandlerForBackup(t)
	// No vrhub.db planted in dataDir (fresh-install case).

	req := httptest.NewRequest(http.MethodPost, "/admin/api/scripts/backup", nil)
	rr := httptest.NewRecorder()
	h.HandleScriptsBackupPOST(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (fresh-install), got %d", rr.Code)
	}

	zr, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range zr.File {
		if f.Name == "vrhub.db" {
			t.Error("vrhub.db should NOT be in backup when source file is absent (fresh-install)")
		}
	}

	// Manifest must still be present and must NOT list vrhub.db.
	var mf struct {
		Contents []string `json:"contents"`
	}
	for _, f := range zr.File {
		if f.Name == "manifest.json" {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			_ = json.Unmarshal(data, &mf)
		}
	}
	for _, c := range mf.Contents {
		if c == "vrhub.db" {
			t.Error("manifest.contents must not list vrhub.db when file is absent")
		}
	}
}

func TestHandleScriptsBackupPOST_Filename(t *testing.T) {
	h, _ := newTestAdminHandlerForBackup(t)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/scripts/backup", nil)
	rr := httptest.NewRecorder()
	h.HandleScriptsBackupPOST(rr, req)

	cd := rr.Header().Get("Content-Disposition")
	if cd == "" {
		t.Fatal("missing Content-Disposition header")
	}
	re := regexp.MustCompile(`vrhub-server-backup-\d{8}-\d{6}\.zip`)
	if !re.MatchString(cd) {
		t.Errorf("Content-Disposition does not match expected filename pattern (got: %q)", cd)
	}
}
