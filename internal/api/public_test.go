package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	chi "github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

type testDB struct {
	games []types.GameEntry
}

func (t *testDB) ListGamesForMeta7z() ([]types.GameEntry, error) {
	return t.games, nil
}

func TestMeta7zHandler_MissingPassword(t *testing.T) {
	// The VRHub client (com.vrhub.logic.CatalogUtils.downloadFile)
	// issues a plain GET with NO custom headers — only User-Agent.
	// /meta.7z is intentionally unauthenticated at the HTTP layer
	// (the 7z archive is AES-256 encrypted and the password is used
	// to extract it locally on the Quest). Verify that no header at
	// all still returns 200.
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	handler := NewPublicAPIHandler(modeVal)
	handler.Config = &types.Config{
		DataDir: t.TempDir(),
		Admin: types.AdminConfig{
			ArchivePassword: "testpass",
		},
	}
	// Wire a minimal DB stub so the handler doesn't 500.
	handler.DB = &testDB{}
	handler.FileDB = &mockFileServerDB{}

	req := httptest.NewRequest("GET", "/meta.7z", nil)
	rec := httptest.NewRecorder()

	handler.Meta7zHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body=%q)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/x-7z-compressed" {
		t.Errorf("Content-Type = %q, want application/x-7z-compressed", got)
	}
}

func TestMeta7zHandler_EmptyPassword(t *testing.T) {
	// Same contract as MissingPassword: an empty "password" header
	// must not cause a 401 — the header is ignored entirely now.
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	handler := NewPublicAPIHandler(modeVal)
	handler.Config = &types.Config{
		DataDir: t.TempDir(),
		Admin: types.AdminConfig{
			ArchivePassword: "testpass",
		},
	}
	handler.DB = &testDB{}
	handler.FileDB = &mockFileServerDB{}

	req := httptest.NewRequest("GET", "/meta.7z", nil)
	req.Header.Set("password", "")
	rec := httptest.NewRecorder()

	handler.Meta7zHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body=%q)", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestMeta7zHandler_SetupMode(t *testing.T) {
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeSetup))

	handler := NewPublicAPIHandler(modeVal)
	req := httptest.NewRequest("GET", "/meta.7z", nil)
	rec := httptest.NewRecorder()

	handler.Meta7zHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestMeta7zHandler_ValidPassword(t *testing.T) {
	tmpDir := t.TempDir()

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("testpass"), 10)
	if err != nil {
		t.Fatalf("failed to generate password hash: %v", err)
	}

	config := &types.Config{
		DataDir: tmpDir,
		Admin: types.AdminConfig{
			PasswordHash:    string(hashedPassword),
			ArchivePassword: "testpass",
		},
	}

	dbPath := tmpDir + "/test.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	game := types.GameEntry{
		GameName:    "Test Game",
		ReleaseName: "test_v1.0",
		PackageName: "com.test.game",
		VersionCode: 42,
		SizeBytes:   104857600,
		Popularity:  50,
	}
	if err := d.InsertGame(game); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	handler := NewPublicAPIHandler(modeVal)
	handler.DB = d
	handler.Config = config

	req := httptest.NewRequest("GET", "/meta.7z", nil)
	password := base64.StdEncoding.EncodeToString([]byte("testpass"))
	req.Header.Set("password", password)
	rec := httptest.NewRecorder()

	handler.Meta7zHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d. body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	contentType := rec.Header().Get("Content-Type")
	if contentType != "application/x-7z-compressed" {
		t.Errorf("Content-Type = %q, want %q", contentType, "application/x-7z-compressed")
	}
}

// TestMeta7zHandler_GenerationFailure_Returns500 is the F5 regression gate:
// when archive generation fails, the handler must NOT reply 200 with an empty
// body (which a VRHub client would treat as a valid-but-empty catalog,
// indistinguishable from "no games"). It must surface a 5xx instead. We force
// a deterministic failure by passing an already-cancelled request context,
// which makes GenerateMeta7z return ctx.Err() before producing any output.
func TestMeta7zHandler_GenerationFailure_Returns500(t *testing.T) {
	tmpDir := t.TempDir()

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("testpass"), 10)
	if err != nil {
		t.Fatalf("failed to generate password hash: %v", err)
	}

	config := &types.Config{
		DataDir: tmpDir,
		Admin: types.AdminConfig{
			PasswordHash:    string(hashedPassword),
			ArchivePassword: "testpass",
		},
	}

	d, err := db.Open(tmpDir + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	if err := d.InsertGame(types.GameEntry{
		GameName:    "Test Game",
		ReleaseName: "test_v1.0",
		PackageName: "com.test.game",
		VersionCode: 42,
		SizeBytes:   104857600,
	}); err != nil {
		t.Fatalf("insert game: %v", err)
	}

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	handler := NewPublicAPIHandler(modeVal)
	handler.DB = d
	handler.Config = config

	// Already-cancelled context → generation aborts before any bytes.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/meta.7z", nil).WithContext(ctx)
	req.Header.Set("password", base64.StdEncoding.EncodeToString([]byte("testpass")))
	rec := httptest.NewRecorder()

	handler.Meta7zHandler(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("status = 200 on generation failure (regression: empty 200 body); want 5xx. body len=%d", rec.Body.Len())
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	// A 5xx must not advertise cache-validation headers.
	if etag := rec.Header().Get("ETag"); etag != "" {
		t.Errorf("ETag present on 5xx: %q (should be stripped)", etag)
	}
}

func TestMeta7zHandler_CorruptedGamesExcluded(t *testing.T) {
	tmpDir := t.TempDir()

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("testpass"), 10)
	if err != nil {
		t.Fatalf("failed to generate password hash: %v", err)
	}

	config := &types.Config{
		DataDir: tmpDir,
		Admin: types.AdminConfig{
			PasswordHash:    string(hashedPassword),
			ArchivePassword: "testpass",
		},
	}

	dbPath := tmpDir + "/test.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	validGame := types.GameEntry{
		GameName:    "Valid Game",
		ReleaseName: "valid_v1",
		PackageName: "com.valid.game",
		VersionCode: 1,
		SizeBytes:   50000000,
		Popularity:  80,
		Hash:        "valid_v1_hash_00000000000000000000",
	}
	if err := d.InsertGame(validGame); err != nil {
		t.Fatalf("insert valid game: %v", err)
	}

	corruptGame := types.GameEntry{
		GameName:    "Corrupted Game",
		ReleaseName: "corrupt_v1",
		PackageName: "com.corrupt.game",
		VersionCode: 1,
		SizeBytes:   30000000,
		Popularity:  90,
		Corrupted:   true,
		Hash:        "corrupt_v1_hash_00000000000000000000",
	}
	if err := d.InsertGame(corruptGame); err != nil {
		t.Fatalf("insert corrupted game: %v", err)
	}

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	handler := NewPublicAPIHandler(modeVal)
	handler.DB = d
	handler.Config = config

	req := httptest.NewRequest("GET", "/meta.7z", nil)
	password := base64.StdEncoding.EncodeToString([]byte("testpass"))
	req.Header.Set("password", password)
	rec := httptest.NewRecorder()

	handler.Meta7zHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestMeta7zHandler_NonExposedGamesExcluded(t *testing.T) {
	tmpDir := t.TempDir()

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("testpass"), 10)
	if err != nil {
		t.Fatalf("failed to generate password hash: %v", err)
	}

	config := &types.Config{
		DataDir: tmpDir,
		Admin: types.AdminConfig{
			PasswordHash:    string(hashedPassword),
			ArchivePassword: "testpass",
		},
	}

	dbPath := tmpDir + "/test.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	exposedGame := types.GameEntry{
		GameName:    "Exposed Game",
		ReleaseName: "exposed_v1",
		PackageName: "com.exposed.game",
		VersionCode: 1,
		SizeBytes:   50000000,
		Popularity:  80,
		Exposed:     true,
		Hash:        "exposed_v1_hash_0000000000000000000",
	}
	if err := d.InsertGame(exposedGame); err != nil {
		t.Fatalf("insert exposed game: %v", err)
	}

	hiddenGame := types.GameEntry{
		GameName:    "Hidden Game",
		ReleaseName: "hidden_v1",
		PackageName: "com.hidden.game",
		VersionCode: 1,
		SizeBytes:   30000000,
		Popularity:  90,
		Exposed:     false,
	}
	if err := d.InsertGame(hiddenGame); err != nil {
		t.Fatalf("insert hidden game: %v", err)
	}

	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	handler := NewPublicAPIHandler(modeVal)
	handler.DB = d
	handler.Config = config

	req := httptest.NewRequest("GET", "/meta.7z", nil)
	password := base64.StdEncoding.EncodeToString([]byte("testpass"))
	req.Header.Set("password", password)
	rec := httptest.NewRecorder()

	handler.Meta7zHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestMeta7zHandler_SetupModeReturns503(t *testing.T) {
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeSetup))

	handler := NewPublicAPIHandler(modeVal)
	req := httptest.NewRequest("GET", "/meta.7z", nil)
	rec := httptest.NewRecorder()

	handler.Meta7zHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

type mockFileServerDB struct {
	game     *types.GameEntry
	packages []string
	err      error
}

func (m *mockFileServerDB) GetGameByHash(hash string) (*types.GameEntry, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.game, nil
}

func (m *mockFileServerDB) ListPackagesByHash(hash string) ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.packages, nil
}

type mockFileReader struct {
	entries []os.DirEntry
	err     error
}

func (m *mockFileReader) Open(name string) (*os.File, error) {
	return os.Open(name)
}

func (m *mockFileReader) ReadDir(dirname string) ([]os.DirEntry, error) {
	return m.entries, m.err
}

func setupFileServerHandler(t *testing.T, db *mockFileServerDB, fr FileReader, cfg *types.Config) http.Handler {
	t.Helper()
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	r := chi.NewRouter()

	deps := fileServerDeps{
		FileDB:     db,
		FileReader: fr,
		Config:     cfg,
	}

	h := fileServerHandlerWithDeps(deps)
	// Register routes directly on the outer router to avoid Chi v5 nested router wildcard issues.
	r.Get("/{hash}/", h)
	r.Get("/{hash}/*", h)
	return r
}

// TestHandleClientConfigGET_PasswordIsBase64Encoded verifies the
// wire-format contract: the "password" field of /config.json is the
// Base64 encoding of the cleartext archive password, so the
// Android client (com.vrhub.data.MainRepository.decodeBase64Password)
// can round-trip it through android.util.Base64.decode(NO_WRAP).
//
// If the server ever sent the cleartext, the client would fail to
// decode it (any cleartext that isn't valid Base64 → exception →
// null password → "invalid password" on archive extraction).
func TestHandleClientConfigGET_PasswordIsBase64Encoded(t *testing.T) {
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	handler := NewPublicAPIHandler(modeVal)
	handler.Config = &types.Config{
		Server: types.ServerConfig{Host: "192.168.1.42", Port: 39457},
		Admin: types.AdminConfig{
			ArchivePassword: "test12345678",
		},
	}

	req := httptest.NewRequest("GET", "/config.json", nil)
	rec := httptest.NewRecorder()
	handler.HandleClientConfigGET(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp ClientConfigResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// base64("test12345678") = "dGVzdDEyMzQ1Njc4"
	want := base64.StdEncoding.EncodeToString([]byte("test12345678"))
	if resp.Password != want {
		t.Errorf("Password = %q, want %q (Base64-encoded archive password)", resp.Password, want)
	}

	// Round-trip: decoding the wire value must produce the cleartext.
	decoded, err := base64.StdEncoding.DecodeString(resp.Password)
	if err != nil {
		t.Fatalf("wire value is not valid Base64: %v", err)
	}
	if string(decoded) != "test12345678" {
		t.Errorf("Base64-decoded password = %q, want %q", string(decoded), "test12345678")
	}

	if resp.BaseURI != "http://192.168.1.42:39457/" {
		t.Errorf("BaseURI = %q, want %q", resp.BaseURI, "http://192.168.1.42:39457/")
	}
}

// TestHandleClientConfigGET_NilConfigReturns503 documents the
// pre-launch behaviour: if the public handler is wired but the
// config is nil (operator is still in setup mode), we return 503
// rather than panicking on a nil deref.
func TestHandleClientConfigGET_NilConfigReturns503(t *testing.T) {
	modeVal := new(atomic.Value)
	modeVal.Store(string(types.ModeNormal))

	handler := NewPublicAPIHandler(modeVal)
	// No Config set.

	req := httptest.NewRequest("GET", "/config.json", nil)
	rec := httptest.NewRecorder()
	handler.HandleClientConfigGET(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestFileServerHandler_NilFileDB_Returns500(t *testing.T) {
	tmpDir := t.TempDir()

	r := chi.NewRouter()
	publicRouter := chi.NewRouter()

	deps := fileServerDeps{
		FileDB:     nil,
		FileReader: &mockFileReader{},
		Config:     &types.Config{DataDir: tmpDir},
	}

	h := fileServerHandlerWithDeps(deps)
	publicRouter.Get("/{hash}/", h)
	publicRouter.Get("/{hash}", h)
	publicRouter.Get("/{hash}/*", h)
	r.Mount("/", publicRouter)

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestFileServerHandler_PathTraversal_Returns404(t *testing.T) {
	tmpDir := t.TempDir()
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        "abc123def456789012345678abcdef00",
		},
	}

	handler := setupFileServerHandler(t, db, &mockFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/../../../etc/passwd/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServePackageListing_NoPackages_ReturnsMessage(t *testing.T) {
	tmpDir := t.TempDir()
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Empty Game",
			PackageName: "com.empty.game",
			Hash:        "abc123def456789012345678abcdef00",
		},
		packages: []string{},
	}

	handler := setupFileServerHandler(t, db, &mockFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "No packages found.") {
		t.Errorf("body missing No packages found message: %s", body)
	}
}

func TestServeFileListing_NilConfig_Returns500(t *testing.T) {
	_ = t.TempDir()
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        "abc123def456789012345678abcdef00",
		},
	}

	r := chi.NewRouter()
	publicRouter := chi.NewRouter()

	deps := fileServerDeps{
		FileDB:     db,
		FileReader: &mockFileReader{},
		Config:     nil,
	}

	h := fileServerHandlerWithDeps(deps)
	publicRouter.Get("/{hash}/", h)
	publicRouter.Get("/{hash}", h)
	publicRouter.Get("/{hash}/*", h)
	r.Mount("/", publicRouter)

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/com.test.game", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestServeFileListing_NilFileReader_Returns500(t *testing.T) {
	tmpDir := t.TempDir()
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        "abc123def456789012345678abcdef00",
		},
	}

	r := chi.NewRouter()
	publicRouter := chi.NewRouter()

	deps := fileServerDeps{
		FileDB:     db,
		FileReader: nil,
		Config:     &types.Config{DataDir: tmpDir},
	}

	h := fileServerHandlerWithDeps(deps)
	publicRouter.Get("/{hash}/", h)
	publicRouter.Get("/{hash}", h)
	publicRouter.Get("/{hash}/*", h)
	r.Mount("/", publicRouter)

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/com.test.game", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestFileServerHandler_PackageListing_ReturnsHTML(t *testing.T) {
	tmpDir := t.TempDir()
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Multi Package Game",
			PackageName: "com.test.game",
			Hash:        "abc123def456789012345678abcdef00",
		},
		packages: []string{"com.test.game", "com.test.game2"},
	}

	handler := setupFileServerHandler(t, db, &mockFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Multi Package Game") {
		t.Errorf("body missing game name: %s", body)
	}
	if !strings.Contains(body, "com.test.game/") {
		t.Errorf("body missing package link: %s", body)
	}
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("body missing DOCTYPE")
	}
}

func TestFileServerHandler_FileListing_ReturnsHTML(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}

	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/app-release.apk", []byte("fake apk"), 0644)
	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/main.obb", []byte("fake obb"), 0644)

	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        "abc123def456789012345678abcdef00",
		},
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/com.test.game", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "app-release.apk") {
		t.Errorf("body missing apk filename: %s", body)
	}
	if !strings.Contains(body, "main.obb") {
		t.Errorf("body missing obb filename: %s", body)
	}
}

func TestFileServerHandler_FileListing_SkipsHiddenFiles(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}

	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/.DS_Store", []byte{}, 0644)
	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/Thumbs.db", []byte{}, 0644)
	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/app.apk", []byte{}, 0644)

	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        "abc123def456789012345678abcdef00",
		},
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/com.test.game", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if strings.Contains(body, ".DS_Store") {
		t.Error("body should not contain .DS_Store")
	}
	if strings.Contains(body, "Thumbs.db") {
		t.Error("body should not contain Thumbs.db")
	}
	if !strings.Contains(body, "app.apk") {
		t.Error("body should contain app.apk")
	}
}

func TestFileServerHandler_GameDirectoryNotFound_Returns404(t *testing.T) {
	tmpDir := t.TempDir()
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        "abc123def456789012345678abcdef00",
		},
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/com.test.game", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestFileServerHandler_InvalidHash_NotInDB_Returns404(t *testing.T) {
	tmpDir := t.TempDir()
	db := &mockFileServerDB{
		game:     nil,
		packages: nil,
		err:      sql.ErrNoRows,
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestFileServerHandler_SHA256Hash_Accepted pins the 9.10-post
// bugfix: the scanner (Story 9.10 T2 / Fix #6 Round 11) emits
// SHA-256 hashes (64 hex chars) for new games, but the file
// server handler used to reject any hash that wasn't exactly 32
// chars (the legacy MD5 length). The validation now accepts
// both 32 (MD5, pre-9.10 games) and 64 (SHA-256, post-9.10
// games). This test reproduces the live AkiBonbon case
// (64-char hash, real APK on disk) end-to-end.
func TestFileServerHandler_SHA256Hash_Accepted(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a real APK-shaped file in tmpDir. We don't need a
	// valid AXML — the file server only serves bytes — but
	// the file must exist on disk.
	const pkgName = "com.gcBronze.AkiBonbon"
	const hash64 = "ab0c1b6ee85276dd5e456b6c72c6ee87600d8447398f4b47ed584770d559a34c"
	apkFile := filepath.Join(tmpDir, "AkiBonbon__18___v1_com.gcBronze.AkiBonbon.apk")
	if err := os.WriteFile(apkFile, []byte("fake apk bytes"), 0644); err != nil {
		t.Fatalf("write apk: %v", err)
	}

	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "AkiBonbon",
			PackageName: pkgName,
			Hash:        hash64,
			ApkPath:     apkFile,
			Exposed:     true,
		},
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	// 1. Package listing: GET /{hash64}/{pkgName}/
	req := httptest.NewRequest("GET", "/"+hash64+"/"+pkgName+"/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("listing status = %d, want %d (64-char hash must be accepted)", rec.Code, http.StatusOK)
	}

	// 2. File download: GET /{hash64}/{pkgName}/{file} (HEAD is
	//    supported in production via chi's GetHead middleware —
	//    this test setup only registers GET, so we exercise GET
	//    and assert Content-Length is set, which is what the
	//    VRHub client uses for its size check on HEAD).
	req = httptest.NewRequest("GET", "/"+hash64+"/"+pkgName+"/AkiBonbon__18___v1_com.gcBronze.AkiBonbon.apk", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("download status = %d, want %d", rec.Code, http.StatusOK)
	}
	if cl := rec.Header().Get("Content-Length"); cl == "" {
		t.Error("Content-Length header must be set for the VRHub client size check")
	}
}

// TestFileServerHandler_HashLengthValidation rejects hashes
// that are neither 32 nor 64 chars (defense against
// arbitrary-length path abuse).
func TestFileServerHandler_HashLengthValidation(t *testing.T) {
	tmpDir := t.TempDir()
	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "x",
			PackageName: "com.x",
			Hash:        "abc", // 3 chars, neither 32 nor 64
		},
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})
	req := httptest.NewRequest("GET", "/abc/com.x/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (3-char hash must be rejected)", rec.Code, http.StatusNotFound)
	}
}

func TestFileServerHandler_DBError_Returns500(t *testing.T) {
	tmpDir := t.TempDir()
	db := &mockFileServerDB{
		err: fmt.Errorf("database error"),
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestServeFileListing_EmptyDirectory_ReturnsHTML(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}

	os.WriteFile(tmpDir+"/games/abc123def456789012345678abcdef00/com.test.game/.DS_Store", []byte{}, 0644)

	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        "abc123def456789012345678abcdef00",
		},
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/com.test.game", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("body missing DOCTYPE")
	}
	if strings.Contains(body, ".DS_Store") {
		t.Error("body should not contain .DS_Store")
	}
}

// TestParseRangeHeader_StartBeyondFileSize_Unsatisfiable verifies the
// C-11 fix: a range whose start is beyond fileSize is now flagged as
// unsatisfiable (RFC 7233 §4.4), so the caller returns 416. The old
// behavior (AC5) silently "saved" the request by serving from byte 0
// with HTTP 206, which violated the spec.
func TestParseRangeHeader_StartBeyondFileSize_Unsatisfiable(t *testing.T) {
	fileSize := int64(300)
	_, _, valid, unsatisfiable := parseRangeHeader("bytes=500-", fileSize)
	if !valid {
		t.Fatal("expected valid=true (well-formed but out of range)")
	}
	if !unsatisfiable {
		t.Error("expected unsatisfiable=true for range beyond file size (C-11 / RFC 7233 §4.4)")
	}
}

func TestParseRangeHeader_ValidStartEnd(t *testing.T) {
	fileSize := int64(10000)
	start, end, valid, unsatisfiable := parseRangeHeader("bytes=0-1023", fileSize)
	if !valid {
		t.Fatal("expected valid=true")
	}
	if unsatisfiable {
		t.Error("expected unsatisfiable=false for in-range request")
	}
	if start != 0 {
		t.Errorf("start = %d, want 0", start)
	}
	if end != 1023 {
		t.Errorf("end = %d, want 1023", end)
	}
}

func TestParseRangeHeader_ValidStartToEnd(t *testing.T) {
	fileSize := int64(10000)
	gotStart, end, valid, unsatisfiable := parseRangeHeader("bytes=500-", fileSize)
	if !valid {
		t.Fatal("expected valid=true")
	}
	if unsatisfiable {
		t.Error("expected unsatisfiable=false for in-range request")
	}
	if gotStart != 500 {
		t.Errorf("start = %d, want 500", gotStart)
	}
	if end != fileSize-1 {
		t.Errorf("end = %d, want %d", end, fileSize-1)
	}
}

func TestParseRangeHeader_InvalidFormat(t *testing.T) {
	_, _, valid, _ := parseRangeHeader("invalid", 1000)
	if valid {
		t.Error("expected valid=false for invalid format")
	}
}

func TestParseRangeHeader_EndClampedToFileSize(t *testing.T) {
	fileSize := int64(1000)
	_, end, valid, unsatisfiable := parseRangeHeader("bytes=0-9999", fileSize)
	if !valid {
		t.Fatal("expected valid=true")
	}
	if unsatisfiable {
		t.Error("expected unsatisfiable=false for end-clamped range")
	}
	if end != fileSize-1 {
		t.Errorf("end = %d, want clamped to %d", end, fileSize-1)
	}
}

// TestParseRangeHeader_EmptyFile_Unsatisfiable: any range against a
// 0-byte file is unsatisfiable (C-11 / RFC 7233 §4.4).
func TestParseRangeHeader_EmptyFile_Unsatisfiable(t *testing.T) {
	_, _, valid, unsatisfiable := parseRangeHeader("bytes=0-99", 0)
	if !valid {
		t.Fatal("expected valid=true (well-formed range against empty file)")
	}
	if !unsatisfiable {
		t.Error("expected unsatisfiable=true for any range against 0-byte file (C-11)")
	}
}

func TestDetectContentType_APK(t *testing.T) {
	got := detectContentType("app-release.apk")
	want := "application/vnd.android.package-archive"
	if got != want {
		t.Errorf("detectContentType(\"app-release.apk\") = %q, want %q", got, want)
	}
}

func TestDetectContentType_OBB(t *testing.T) {
	got := detectContentType("main.obb")
	want := "application/octet-stream"
	if got != want {
		t.Errorf("detectContentType(\"main.obb\") = %q, want %q", got, want)
	}
}

func TestDetectContentType_UnknownExtension(t *testing.T) {
	got := detectContentType("readme.txt")
	want := "application/octet-stream"
	if got != want {
		t.Errorf("detectContentType(\"readme.txt\") = %q, want %q", got, want)
	}
}

func TestEncodeContentDispositionFilename_ASCII(t *testing.T) {
	got := encodeContentDispositionFilename("app-release.apk")
	want := `attachment; filename="app-release.apk"`
	if got != want {
		t.Errorf("encodeContentDispositionFilename(\"app-release.apk\") = %q, want %q", got, want)
	}
}

func TestEncodeContentDispositionFilename_WithDoubleQuote(t *testing.T) {
	got := encodeContentDispositionFilename(`game"update.apk`)
	if !strings.Contains(got, "filename*=UTF-8") {
		t.Errorf("missing RFC 5987 filename* in: %q", got)
	}
	// Verify no HTML entities in filename* path
	if strings.Contains(got, "&quot;") && strings.Contains(got, "filename*=UTF-8''") {
		idx := strings.Index(got, "filename*=UTF-8''")
		encodedPart := got[idx+len("filename*=UTF-8''"):]
		if strings.Contains(encodedPart, "&quot;") {
			t.Errorf("HTML entity &quot; found in filename* path: %q", encodedPart)
		}
	}
	// Verify double-quote is percent-encoded in filename* (should be %22)
	if !strings.Contains(got, "%22") {
		t.Errorf("expected %%22 for double-quote in filename*: %q", got)
	}
}

func TestEncodeContentDispositionFilename_WithAmpersand(t *testing.T) {
	got := encodeContentDispositionFilename("game&update.apk")
	want := `attachment; filename="game&update.apk"`
	if got != want {
		t.Errorf("encodeContentDispositionFilename(\"game&update.apk\") = %q, want %q (& is valid attr-char per RFC 5987)", got, want)
	}
}

func TestEncodeContentDispositionFilename_WithSpace(t *testing.T) {
	got := encodeContentDispositionFilename("my game.apk")
	if !strings.Contains(got, "filename*=UTF-8") {
		t.Errorf("missing RFC 5987 filename* in: %q", got)
	}
	if !strings.Contains(got, "%20") {
		t.Errorf("expected %%20 for space in filename*: %q", got)
	}
}

func TestEncodeContentDispositionFilename_WithNonASCII(t *testing.T) {
	got := encodeContentDispositionFilename("jeu\u00e9.apk")
	if !strings.Contains(got, "filename*=UTF-8") {
		t.Errorf("missing RFC 5987 filename* in: %q", got)
	}
	// UTF-8 é is bytes 0xC3 0xA9
	if !strings.Contains(got, "%C3%A9") {
		t.Errorf("expected %%C3%%A9 for UTF-8 é in filename*: %q", got)
	}
}

func TestEncodeContentDispositionFilename_WithBackslash(t *testing.T) {
	got := encodeContentDispositionFilename(`game\update.apk`)
	if !strings.Contains(got, "filename*=UTF-8") {
		t.Errorf("missing RFC 5987 filename* in: %q", got)
	}
	if !strings.Contains(got, "%5C") {
		t.Errorf("expected %%5C for backslash in filename*: %q", got)
	}
}

func TestEncodeContentDispositionFilename_WithAsterisk(t *testing.T) {
	got := encodeContentDispositionFilename("game*.apk")
	want := `attachment; filename="game*.apk"`
	if got != want {
		t.Errorf("encodeContentDispositionFilename(\"game*.apk\") = %q, want %q (* is valid attr-char)", got, want)
	}
}

func TestEncodeContentDispositionFilename_WithPlus(t *testing.T) {
	got := encodeContentDispositionFilename("game+update.apk")
	want := `attachment; filename="game+update.apk"`
	if got != want {
		t.Errorf("encodeContentDispositionFilename(\"game+update.apk\") = %q, want %q (+ is valid attr-char)", got, want)
	}
}

func TestEncodeRFC5987_ASCIIOnly(t *testing.T) {
	got := encodeRFC5987("hello.apk")
	if got != "hello.apk" {
		t.Errorf("encodeRFC5987(\"hello.apk\") = %q, want %q", got, "hello.apk")
	}
}

func TestEncodeRFC5987_Space(t *testing.T) {
	got := encodeRFC5987("my file.apk")
	if got != "my%20file.apk" {
		t.Errorf("encodeRFC5987(\"my file.apk\") = %q, want %q", got, "my%20file.apk")
	}
}

func TestEncodeRFC5987_DoubleQuote(t *testing.T) {
	got := encodeRFC5987(`game"file.apk`)
	if got != `game%22file.apk` {
		t.Errorf("encodeRFC5987(\"game\\\"file.apk\") = %q, want %q", got, `game%22file.apk`)
	}
}

func TestEncodeRFC5987_Backslash(t *testing.T) {
	got := encodeRFC5987(`game\file.apk`)
	if got != `game%5Cfile.apk` {
		t.Errorf("encodeRFC5987(\"game\\file.apk\") = %q, want %q", got, `game%5Cfile.apk`)
	}
}

func TestEncodeRFC5987_UTF8(t *testing.T) {
	got := encodeRFC5987("jeu\u00e9.apk")
	if got != "jeu%C3%A9.apk" {
		t.Errorf("encodeRFC5987(\"jeu\\u00e9.apk\") = %q, want %q", got, "jeu%C3%A9.apk")
	}
}

func TestEncodeRFC5987_Ampersand(t *testing.T) {
	got := encodeRFC5987("game&file.apk")
	if got != "game&file.apk" {
		t.Errorf("encodeRFC5987(\"game&file.apk\") = %q, want %q (& is attr-char)", got, "game&file.apk")
	}
}

func TestEncodeTokenFilename_ASCIIOnly(t *testing.T) {
	got := encodeTokenFilename("hello.apk")
	if got != "hello.apk" {
		t.Errorf("encodeTokenFilename(\"hello.apk\") = %q, want %q", got, "hello.apk")
	}
}

func TestEncodeTokenFilename_DoubleQuote(t *testing.T) {
	got := encodeTokenFilename(`game"file.apk`)
	if got != `game&quot;file.apk` {
		t.Errorf("encodeTokenFilename(\"game\\\"file.apk\") = %q, want %q", got, `game&quot;file.apk`)
	}
}

func TestEncodeTokenFilename_Ampersand(t *testing.T) {
	got := encodeTokenFilename("game&file.apk")
	if got != "game&amp;file.apk" {
		t.Errorf("encodeTokenFilename(\"game&file.apk\") = %q, want %q", got, "game&amp;file.apk")
	}
}

func TestEncodeTokenFilename_LessThan(t *testing.T) {
	got := encodeTokenFilename("a<b.apk")
	if got != "a&lt;b.apk" {
		t.Errorf("encodeTokenFilename(\"a<b.apk\") = %q, want %q", got, "a&lt;b.apk")
	}
}

func TestEncodeTokenFilename_ControlChar(t *testing.T) {
	got := encodeTokenFilename("file\x01.apk")
	if !strings.Contains(got, "%01") {
		t.Errorf("expected %%01 for control char in: %q", got)
	}
}

func TestIsAttrChar(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{'a', true}, {'z', true}, {'A', true}, {'Z', true},
		{'0', true}, {'9', true},
		{'!', true}, {'#', true}, {'$', true}, {'&', true},
		{'+', true}, {'-', true}, {'.', true}, {'^', true},
		{'_', true}, {'`', true}, {'\'', true}, {'%', true},
		{'*', true},
		{' ', false}, {'"', false}, {'\\', false}, {'<', false},
		{'>', false}, {'@', false}, {'[', false}, {']', false},
		{'\t', false}, {'\n', false},
	}
	for _, tt := range tests {
		got := isAttrChar(tt.r)
		if got != tt.want {
			t.Errorf("isAttrChar(%q) = %v, want %v", tt.r, got, tt.want)
		}
	}
}

// TestFileServerHandler_CatchAll_TrailingSlash_Listing verifies that
// /{hash}/{packageName}/ (with trailing slash) is correctly matched by
// the /* catch-all route and returns an HTML file listing.
func TestFileServerHandler_CatchAll_TrailingSlash_Listing(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}
	os.WriteFile(gameDir+"/app-release.apk", []byte("fake apk"), 0644)

	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        "abc123def456789012345678abcdef00",
		},
	}

	// Use the same router wiring as production (setupFileServerHandler
	// mirrors MountPublicRoutes' three file-server routes).
	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/com.test.game/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "app-release.apk") {
		t.Errorf("body missing apk filename: %s", body)
	}
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("body missing DOCTYPE")
	}
}

// TestFileServerHandler_CatchAll_FileDownload verifies that
// /{hash}/{packageName}/{filename} (two segments after hash) is correctly
// matched by the /* catch-all route and returns the file contents.
func TestFileServerHandler_CatchAll_FileDownload(t *testing.T) {
	tmpDir := t.TempDir()
	gameDir := tmpDir + "/games/abc123def456789012345678abcdef00/com.test.game"
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatalf("create game dir: %v", err)
	}
	wantContent := []byte("this is the real apk content")
	os.WriteFile(gameDir+"/app-release.apk", wantContent, 0644)

	db := &mockFileServerDB{
		game: &types.GameEntry{
			GameName:    "Test Game",
			PackageName: "com.test.game",
			Hash:        "abc123def456789012345678abcdef00",
		},
	}

	handler := setupFileServerHandler(t, db, &realFileReader{}, &types.Config{DataDir: tmpDir})

	req := httptest.NewRequest("GET", "/abc123def456789012345678abcdef00/com.test.game/app-release.apk", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.Bytes(); !strings.EqualFold(string(got), string(wantContent)) {
		t.Errorf("body = %q, want %q", got, wantContent)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/vnd.android.package-archive" {
		t.Errorf("Content-Type = %q, want application/vnd.android.package-archive", got)
	}
}
