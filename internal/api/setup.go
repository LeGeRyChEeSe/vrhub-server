package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/config"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/game"
	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/trailers"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
	"golang.org/x/crypto/bcrypt"
)

const (
	configFileName = "config.toml"
	// defaultHost is the bind address used when no host is configured.
	// Live session 2026-06-08: changed from "127.0.0.1" to "0.0.0.0"
	// so the server listens on every interface. The admin shell is
	// still reachable at 127.0.0.1:port (loopback), and the Meta
	// Quest client can now reach the catalog at the LAN IP. The
	// wizard step 4 displays the detected LAN IP for the VRHub app
	// to use; the admin is reachable at either address.
	defaultHost = "0.0.0.0"
	defaultPort = 39457
)

// getOutboundIP returns the IP address this machine uses to reach the
// public internet. Implemented by opening a UDP socket to a public
// address (no packets are actually sent — the connect() call is
// non-blocking for UDP), then reading the local socket address.
//
// Live session 2026-06-08: the wizard step 4 used to display
// "http://127.0.0.1:39457" as the Base URI for the VRHub client,
// which is unreachable from the Meta Quest on the LAN. Now we show
// the real LAN IP, e.g. "http://192.168.50.3:39457". The admin shell
// stays reachable at 127.0.0.1:port (loopback) since the server
// binds to 0.0.0.0.
//
// If no non-loopback interface is found (e.g. server running in a
// container with only loopback), returns "127.0.0.1" as a safe
// fallback. Test environments use the same fallback.
func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	ip := localAddr.IP.String()
	if ip == "" || ip == "::" || ip == "0.0.0.0" {
		return "127.0.0.1"
	}
	return ip
}

// SetupHandler serves setup-related API endpoints.
type SetupHandler struct {
	DataDir string
	ModeVal *atomic.Value
	mu      sync.Mutex

	// ConfigPropagator is called by HandleLaunchPOST after config.toml is
	// written (and after the loopback-host upgrade), and BEFORE
	// TransitionToNormal, to push the freshly-loaded config to all
	// handlers that captured the (possibly nil) cfg at router construction
	// time. Wired by SetupRouter. nil is acceptable in unit tests that
	// exercise HandleLaunchPOST in isolation.
	//
	// Story 9.1 (B1): without this propagation, PublicAPIHandler.Config and
	// UpdateHandler.UpdateConfig remain nil/empty after the setup→normal
	// transition, and the public API (GET /meta.7z) returns 500 "admin
	// password hash not configured" because meta7zHandlerWithDeps sees
	// deps.Config == nil. The operator would have to restart the server
	// to recover, violating Story 1.5's "transitions to normal mode (no
	// restart needed)" acceptance criterion.
	ConfigPropagator func(*types.Config)
}

// NewSetupHandler creates a new setup handler with an atomic mode value.
func NewSetupHandler(dataDir string, mode types.ServerMode) *SetupHandler {
	h := &SetupHandler{DataDir: dataDir}
	h.ModeVal = new(atomic.Value)
	h.ModeVal.Store(string(mode))
	return h
}

// getMode returns the current server mode from the atomic value.
func (h *SetupHandler) getMode() types.ServerMode {
	if s, ok := h.ModeVal.Load().(string); ok {
		return types.ServerMode(s)
	}
	return types.ModeSetup
}

// TransitionToNormal transitions the server from setup mode to normal mode.
func (h *SetupHandler) TransitionToNormal() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.getMode() != types.ModeSetup {
		return
	}
	h.ModeVal.Store(string(types.ModeNormal))
}

// credentialsRequest represents the JSON body for credential submission.
type credentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// scanRequest represents the JSON body for folder scanning.
type scanRequest struct {
	Folder string `json:"folder"`
}

const maxExcludedPackages = 500

// reviewRequest represents the JSON body for the review POST endpoint.
type reviewRequest struct {
	Excluded []string `json:"excluded"`
}

// archivePasswordRequest represents the JSON body for the archive password endpoint.
type archivePasswordRequest struct {
	ArchivePassword string `json:"archive_password"`
}

// HandleCredentialsPOST handles POST /admin/api/setup/credentials.
func (h *SetupHandler) HandleCredentialsPOST(w http.ResponseWriter, r *http.Request) {
	var req credentialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body", "INVALID_INPUT")
		return
	}

	username := strings.TrimSpace(req.Username)
	if username == "" {
		writeError(w, http.StatusBadRequest, "Username is required", "INVALID_INPUT")
		return
	}
	if len(req.Password) < 4 {
		writeError(w, http.StatusBadRequest, "Password must be at least 4 characters", "INVALID_INPUT")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.getMode() != types.ModeSetup {
		writeError(w, http.StatusForbidden, "Server is not in setup mode", "NOT_IN_SETUP_MODE")
		return
	}

	configPath := filepath.Join(h.DataDir, configFileName)

	var cfg *types.Config
	_, statErr := os.Stat(configPath)
	if statErr == nil {
		loadedCfg, loadErr := config.Load(h.DataDir)
		if loadErr != nil {
			writeError(w, http.StatusInternalServerError, "Failed to load configuration", "CONFIG_ERROR")
			return
		}
		cfg = loadedCfg

		if cfg.Admin.PasswordHash != "" {
			writeError(w, http.StatusConflict, "Credentials already set", "CREDENTIALS_ALREADY_SET")
			return
		}
	} else if os.IsNotExist(statErr) {
		cfg = &types.Config{
			Server: types.ServerConfig{
				Host: defaultHost,
				Port: defaultPort,
				Mode: types.ModeNormal,
			},
			Database: types.DatabaseConfig{
				Path: filepath.Join(h.DataDir, "vrhub.db"),
			},
			DataDir: h.DataDir,
		}
	} else {
		writeError(w, http.StatusInternalServerError, "Failed to access configuration", "CONFIG_ERROR")
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to hash password", "HASH_ERROR")
		return
	}

	cfg.Admin.Username = username
	cfg.Admin.PasswordHash = string(hashedPassword)
	cfg.Server.Mode = types.ModeNormal

	if saveErr := config.Save(cfg, h.DataDir); saveErr != nil {
		writeError(w, http.StatusInternalServerError, "Failed to save configuration", "SAVE_ERROR")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data": map[string]string{
			"username": username,
			"message":  "Credentials created",
		},
	})
}

// HandleSetupArchivePasswordPOST handles POST /admin/api/setup/archive-password.
func (h *SetupHandler) HandleSetupArchivePasswordPOST(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req archivePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body", "INVALID_INPUT")
		return
	}

	password := strings.TrimSpace(req.ArchivePassword)
	if len(password) < 8 {
		writeError(w, http.StatusBadRequest, "Archive password must be at least 8 characters", "INVALID_INPUT")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.getMode() != types.ModeSetup {
		writeError(w, http.StatusForbidden, "Server is not in setup mode", "NOT_IN_SETUP_MODE")
		return
	}

	cfg, err := config.Load(h.DataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to load configuration", "CONFIG_ERROR")
		return
	}

	cfg.Admin.ArchivePassword = password
	if saveErr := config.Save(cfg, h.DataDir); saveErr != nil {
		writeError(w, http.StatusInternalServerError, "Failed to save configuration", "SAVE_ERROR")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data": map[string]string{
			"message": "Archive password set",
		},
	})
}

// HandleScanPOST handles POST /admin/api/setup/scan.
func (h *SetupHandler) HandleScanPOST(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body", "INVALID_INPUT")
		return
	}

	folder := strings.TrimSpace(req.Folder)
	if folder == "" {
		writeError(w, http.StatusBadRequest, "Folder path is required", "INVALID_INPUT")
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.getMode() != types.ModeSetup {
		writeError(w, http.StatusForbidden, "Server is not in setup mode", "NOT_IN_SETUP_MODE")
		return
	}

	cleanPath := filepath.Clean(folder)
	info, err := os.Stat(cleanPath)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, "Folder does not exist", "INVALID_FOLDER")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	files, scanErr := game.ScanDirectory(cleanPath)
	if scanErr != nil {
		fmt.Fprintf(os.Stderr, "[scan] failed: %v\n", scanErr)
		writeError(w, http.StatusInternalServerError, "Scan failed", "SCAN_ERROR")
		return
	}

	var apkFiles []game.FileEntry
	var obbFiles []game.FileEntry
	for _, f := range files {
		if f.IsAPK {
			apkFiles = append(apkFiles, f)
		} else {
			obbFiles = append(obbFiles, f)
		}
	}

	pairResult := game.PairFiles(apkFiles, obbFiles)

	totalSizeBytes := int64(0)
	for _, f := range files {
		totalSizeBytes += f.Size
	}

	games := make([]types.GameScanEntry, 0, len(pairResult.Games))

	dbPath := filepath.Join(h.DataDir, "vrhub.db")
	dbConn, dbOpenErr := db.Open(dbPath)
	if dbOpenErr != nil {
		fmt.Fprintf(os.Stderr, "[scan] db open failed: %v\n", dbOpenErr)
		writeError(w, http.StatusInternalServerError, "Failed to open database", "DB_ERROR")
		return
	}
	defer dbConn.Close()

	tx, txErr := dbConn.BeginTx(ctx, nil)
	if txErr != nil {
		fmt.Fprintf(os.Stderr, "[scan] transaction begin failed: %v\n", txErr)
		writeError(w, http.StatusInternalServerError, "Failed to start database transaction", "DB_ERROR")
		return
	}

	for _, gp := range pairResult.Games {
		var obbSize int64
		for _, o := range gp.OBBFiles {
			obbSize += o.Size
		}

		entry := types.GameScanEntry{
			ReleaseName:      gp.APKMeta.PackageName,
			GameName:         gp.APKMeta.Label,
			PackageName:      gp.APKMeta.PackageName,
			VersionCode:      gp.APKMeta.VersionCode,
			SizeBytes:        gp.APKFile.Size,
			OBBSizeBytes:     obbSize,
			Corrupted:        false,
			CorruptionReason: "",
		}

		integrityResult := game.ValidateAPK(gp.APKFile.Path)
		if integrityResult.Corrupted {
			entry.Corrupted = true
			entry.CorruptionReason = integrityResult.CorruptionReason
		}

		games = append(games, entry)

		gameEntry := db.NewGameEntryFromScan(
			gp.APKMeta.PackageName,
			gp.APKMeta.VersionCode,
			gp.APKFile.Size,
			obbSize,
		)
		gameEntry.GameName = gp.APKMeta.Label
		gameEntry.Corrupted = integrityResult.Corrupted
		gameEntry.CorruptionReason = integrityResult.CorruptionReason

		// Story 9.10 (F10): serve files directly from their real location
		// inside the operator's game_folders — do NOT stage a copy into
		// {data-dir}/games/{hash}/{packageName}/. The earlier wizard copied
		// every APK + OBB into dataDir, duplicating potentially gigabytes of
		// data, while the public file server (serveFileDownload /
		// serveFileListing) already resolves the file from game.ApkPath /
		// game.OBBPath since Story 9.10. We record the real on-disk paths so
		// the server reads from the source. The scanned folder is persisted
		// to config.game_folders below, so future rescans (importer) keep the
		// same serve-from-source model. APKFile.Path / OBBFiles[*].Path are
		// absolute (the scan walks the absolute folder the operator entered).
		gameEntry.ApkPath = gp.APKFile.Path
		if len(gp.OBBFiles) > 0 {
			gameEntry.OBBPath = gp.OBBFiles[0].Path
		}

		// Story 11.1 (Task 4): pick up an operator trailer-override sidecar
		// ("{releaseName}.trailer" or "trailer.url") next to the APK during
		// the initial setup scan, mirroring the importer's rescan path.
		gameEntry.TrailerURL = trailers.ReadOverrideForDir(filepath.Dir(gp.APKFile.Path), gameEntry.ReleaseName)

		if insertErr := dbConn.InsertGameTx(tx, gameEntry); insertErr != nil {
			tx.Rollback()
			fmt.Fprintf(os.Stderr, "[scan] insert game %q failed: %v\n", gp.APKMeta.PackageName, insertErr)
			writeError(w, http.StatusInternalServerError, "Failed to save game", "DB_ERROR")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "[scan] transaction commit failed: %v\n", err)
		writeError(w, http.StatusInternalServerError, "Failed to commit database changes", "DB_ERROR")
		return
	}

	// Persist the scanned folder path so future rescans (admin UI or
	// background) can re-discover new games without re-running the wizard.
	cfg, cfgErr := config.Load(h.DataDir)
	if cfgErr == nil {
		var alreadyConfigured bool
		for _, existing := range cfg.GameFolders {
			if existing == cleanPath {
				alreadyConfigured = true
				break
			}
		}
		if !alreadyConfigured {
			cfg.GameFolders = append(cfg.GameFolders, cleanPath)
			if saveErr := config.Save(cfg, h.DataDir); saveErr != nil {
				vlog.Get().Warn().Err(saveErr).Str("dir", cleanPath).Msg("scan: failed to save game_folders to config")
			}
		}
	}

	orphanOBBs := make([]types.OrphanOBBEntry, 0, len(pairResult.OrphanOBBs))
	for _, o := range pairResult.OrphanOBBs {
		orphanOBBs = append(orphanOBBs, types.OrphanOBBEntry{
			Path: o.Path,
			Name: o.Name,
			Size: o.Size,
		})
	}

	result := types.ScanResult{
		FileCount:      len(files),
		TotalSizeBytes: totalSizeBytes,
		Games:          games,
		OrphanOBBs:     orphanOBBs,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data": result,
	})
}

// copyGameFilesToDataDir copies an APK and its paired OBBs from their
// original scan location into {dataDir}/games/{hash}/{packageName}/,
// creating the directory tree as needed. The destination path matches
// what public.go's serveFileDownload/serveFileListing read from
// (public.go:518, public.go:654).
//
// Story 9.4 (B4) — fix for the public file server 404: the wizard
// previously INSERTed games into the DB but never copied the bytes,
// so every /{hash}/* endpoint returned 404 even though meta.7z
// advertised the games.
//
// Idempotency: if a destination file already exists AND its size
// matches the source, the copy is skipped. This handles a re-scan of
// the same folder (the file server would 200 the existing file, and
// re-copying 2.2 GB of OBBs on every wizard refresh would be a bad
// UX). If the size differs, the file is overwritten — operator-driven
// replacement of a corrupted file.
//
// Streaming: io.Copy uses a 32 KiB buffer internally, so we never load
// a full 2.2 GB OBB into memory. The whole call is bounded by disk
// bandwidth, not heap.
//
// Returns the list of files that were actually copied (used by the
// caller to clean up partial state on a copy error, so a half-copied
// directory doesn't linger on disk after a Rollback). Caller can
// ignore the slice on success.
//
// PATCH (review): the fileName and packageName are validated to
// reject path-traversal attempts (e.g. "../../etc/passwd"). This is
// defense-in-depth: the file server (public.go:435,461) already
// rejects `..` in the URL, but if a malicious APK in the scan
// directory had a packageName or filename containing `..`, the copy
// step would have written outside {data-dir}/games/. Now caught at
// the source.
func copyGameFilesToDataDir(dataDir, hash, packageName string, apkFile game.FileEntry, obbFiles []game.FileEntry) (copiedSoFar []string, retErr error) {
	// PATCH: validate path components FIRST, before any directory
	// creation. The hash is computed by db.ComputeHash from the
	// packageName, so it can only contain [0-9a-f] (md5 hex). The
	// packageName comes from the APK manifest; the filename comes
	// from filepath.Walk. Both are user-controlled, so we reject
	// anything that could escape the destination directory.
	if strings.Contains(hash, "..") || strings.Contains(packageName, "..") || filepath.IsAbs(hash) || filepath.IsAbs(packageName) {
		return nil, fmt.Errorf("refusing to copy: hash=%q or packageName=%q contains path-traversal characters", hash, packageName)
	}

	// Validate filenames BEFORE MkdirAll. The filename is the leaf
	// name only; any separator or parent reference would let the
	// copy step write outside the game directory.
	for _, f := range []game.FileEntry{apkFile} {
		if strings.Contains(f.Name, "..") || strings.ContainsAny(f.Name, "/\\") || filepath.IsAbs(f.Name) {
			return nil, fmt.Errorf("refusing to copy: filename %q contains path-traversal characters", f.Name)
		}
	}
	for _, f := range obbFiles {
		if strings.Contains(f.Name, "..") || strings.ContainsAny(f.Name, "/\\") || filepath.IsAbs(f.Name) {
			return nil, fmt.Errorf("refusing to copy: filename %q contains path-traversal characters", f.Name)
		}
	}

	destDir := filepath.Join(dataDir, "games", hash, packageName)
	if mkErr := os.MkdirAll(destDir, 0o755); mkErr != nil {
		return nil, fmt.Errorf("mkdir %q: %w", destDir, mkErr)
	}

	// Collect all files to copy: APK first, then OBBs.
	all := make([]game.FileEntry, 0, 1+len(obbFiles))
	all = append(all, apkFile)
	all = append(all, obbFiles...)

	for _, f := range all {
		// PATCH: filenames are pre-validated above (before MkdirAll)
		// so we know f.Name is a safe leaf. The destination is the
		// basename of f.Name inside destDir; no separator or parent
		// reference can sneak through.
		destPath := filepath.Join(destDir, f.Name)
		if same, statErr := isAlreadyCopied(destPath, f.Size); statErr == nil && same {
			// Idempotent skip: file already in place with matching size.
			vlog.Get().Debug().
				Str("src", f.Path).
				Str("dest", destPath).
				Int64("size", f.Size).
				Msg("scan: file already present at destination, skipping copy")
			continue
		}

		if err := copyOneFile(f.Path, destPath); err != nil {
			return copiedSoFar, fmt.Errorf("copy %q -> %q: %w", f.Path, destPath, err)
		}
		// PATCH: track the file we just wrote so the caller can
		// remove it on a subsequent error (partial-copy cleanup).
		copiedSoFar = append(copiedSoFar, destPath)
		vlog.Get().Info().
			Str("src", f.Path).
			Str("dest", destPath).
			Int64("size", f.Size).
			Msg("scan: copied game file to data dir")
	}

	return copiedSoFar, nil
}

// isAlreadyCopied returns (true, nil) when destPath exists, is a
// regular file, and its size matches expectedSize. Returns
// (false, nil) when the file is missing or has a different size.
// A non-nil error is reserved for genuine stat failures.
func isAlreadyCopied(destPath string, expectedSize int64) (bool, error) {
	info, err := os.Stat(destPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	return info.Size() == expectedSize, nil
}

// copyOneFile streams src to dest via io.Copy. Uses 0o644 for the
// destination (matches the existing file-server test fixtures and is
// the standard "user-writable, world-readable" file mode).
func copyOneFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open dest: %w", err)
	}
	// Close explicitly so we catch a flush error before declaring success.
	if _, copyErr := io.Copy(out, in); copyErr != nil {
		out.Close()
		// Best-effort cleanup of a partial file: a half-written
		// destination would be served as corrupted bytes on the next
		// /{hash}/{file} request and waste 2.2 GB of disk.
		os.Remove(dest)
		return fmt.Errorf("io.Copy: %w", copyErr)
	}
	if closeErr := out.Close(); closeErr != nil {
		os.Remove(dest)
		return fmt.Errorf("close dest: %w", closeErr)
	}
	return nil
}

// HandleSetupStateGET handles GET /admin/api/setup/state. It returns the
// minimal server-side state the wizard needs to auto-skip a step on
// page refresh.
//
// Story 1.7 B1 (live session 2026-06-08): a user reported that after
// completing the credentials step and refreshing the wizard page, the
// client-side state reset to step 1 and re-submitting the same
// credentials returned 409 CREDENTIALS_ALREADY_SET, leaving the user
// stuck. The fix is to have the wizard JS call this endpoint on
// initStep1() load and goToStep() directly to the appropriate step
// based on what has already been completed.
//
// Response shape:
//
//	{"data": {"credentials_set": bool, "game_count": int}}
//
// - credentials_set: true iff config.toml exists with a non-empty Admin.PasswordHash
// - game_count: number of rows in the `games` table
//
// The endpoint is only meaningful in setup mode; in normal mode the
// SetupModeMiddleware already redirects /admin/* away, but we return
// 403 as defense-in-depth in case the route is reached via a test or
// future router wiring change.
func (h *SetupHandler) HandleSetupStateGET(w http.ResponseWriter, r *http.Request) {
	if h.getMode() != types.ModeSetup {
		writeError(w, http.StatusForbidden, "Server is not in setup mode", "NOT_IN_SETUP_MODE")
		return
	}

	// credentials_set: load config.toml, check Admin.PasswordHash.
	credentialsSet := false
	archivePasswordSet := false
	configPath := filepath.Join(h.DataDir, configFileName)
	if _, statErr := os.Stat(configPath); statErr == nil {
		if loaded, loadErr := config.Load(h.DataDir); loadErr == nil {
			if loaded.Admin.PasswordHash != "" {
				credentialsSet = true
			}
			if loaded.Admin.ArchivePassword != "" {
				archivePasswordSet = true
			}
		}
	}

	// game_count: open DB and count. Reuses the same path the launch
	// endpoint uses (HandleLaunchPOST:389-407).
	gameCount := 0
	dbPath := filepath.Join(h.DataDir, "vrhub.db")
	if d, dbErr := db.Open(dbPath); dbErr == nil {
		if c, countErr := d.CountGames(); countErr == nil {
			gameCount = c
		}
		d.Close()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data": map[string]interface{}{
			"credentials_set":      credentialsSet,
			"archive_password_set": archivePasswordSet,
			"game_count":           gameCount,
		},
	})
}

// HandleReviewGET handles GET /admin/setup/review.
func (h *SetupHandler) HandleReviewGET(w http.ResponseWriter, r *http.Request) {
	mode := h.getMode()

	if mode != types.ModeSetup {
		writeError(w, http.StatusForbidden, "Server is not in setup mode", "NOT_IN_SETUP_MODE")
		return
	}

	dbPath := filepath.Join(h.DataDir, "vrhub.db")
	dbConn, dbOpenErr := db.Open(dbPath)
	if dbOpenErr != nil {
		fmt.Fprintf(os.Stderr, "[review] db open failed: %v\n", dbOpenErr)
		writeError(w, http.StatusInternalServerError, "Failed to open database", "DB_ERROR")
		return
	}
	defer dbConn.Close()

	games, err := dbConn.ListAllGamesOrderedByName()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[review] list games failed: %v\n", err)
		writeError(w, http.StatusInternalServerError, "Failed to list games", "DB_ERROR")
		return
	}

	reviewEntries := make([]types.ReviewGameEntry, 0, len(games))
	for _, g := range games {
		reviewEntries = append(reviewEntries, types.ReviewGameEntry{
			ID:               g.ID,
			ReleaseName:      g.ReleaseName,
			GameName:         g.GameName,
			PackageName:      g.PackageName,
			VersionCode:      g.VersionCode,
			SizeBytes:        g.SizeBytes,
			OBBSizeBytes:     g.OBBSizeBytes,
			Corrupted:        g.Corrupted,
			CorruptionReason: g.CorruptionReason,
			Excluded:         g.Corrupted || !g.Exposed,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data": reviewEntries,
	})
}

// HandleLaunchPOST handles POST /admin/api/setup/launch.
func (h *SetupHandler) HandleLaunchPOST(w http.ResponseWriter, r *http.Request) {
	if h.getMode() == types.ModeNormal {
		writeError(w, http.StatusForbidden, "Server is not in setup mode", "NOT_IN_SETUP_MODE")
		return
	}

	configPath := filepath.Join(h.DataDir, configFileName)
	_, statErr := os.Stat(configPath)

	var cfg *types.Config
	if statErr == nil {
		var loadErr error
		cfg, loadErr = config.Load(h.DataDir)
		if loadErr != nil {
			writeError(w, http.StatusInternalServerError, "Failed to load configuration", "CONFIG_ERROR")
			return
		}

		if cfg.Admin.PasswordHash == "" {
			writeError(w, http.StatusConflict, "Credentials not created. Complete credential setup first.", "PREREQUISITE_NOT_MET")
			return
		}
	} else if os.IsNotExist(statErr) {
		writeError(w, http.StatusConflict, "Credentials not created. Complete credential setup first.", "PREREQUISITE_NOT_MET")
		return
	} else {
		writeError(w, http.StatusInternalServerError, "Failed to access configuration", "CONFIG_ERROR")
		return
	}

	// Read optional port override from the frontend.
	var launchReq struct {
		Port int `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&launchReq); err == nil && launchReq.Port > 0 {
		if launchReq.Port < 1 || launchReq.Port > 65535 {
			writeError(w, http.StatusBadRequest, "Port must be between 1 and 65535", "INVALID_PORT")
			return
		}
		cfg.Server.Port = launchReq.Port
		if saveErr := config.Save(cfg, h.DataDir); saveErr != nil {
			vlog.Get().Warn().Err(saveErr).Msg("failed to persist port override")
		}
	}

	dbPath := filepath.Join(h.DataDir, "vrhub.db")
	dbConn, dbOpenErr := db.Open(dbPath)
	if dbOpenErr != nil {
		fmt.Fprintf(os.Stderr, "[launch] db open failed: %v\n", dbOpenErr)
		writeError(w, http.StatusInternalServerError, "Failed to open database", "DB_ERROR")
		return
	}
	defer dbConn.Close()

	gameCount, countErr := dbConn.CountGames()
	if countErr != nil {
		fmt.Fprintf(os.Stderr, "[launch] count games failed: %v\n", countErr)
		writeError(w, http.StatusInternalServerError, "Failed to check game count", "DB_ERROR")
		return
	}

	if gameCount == 0 {
		writeError(w, http.StatusConflict, "No games found. Scan a game folder first.", "PREREQUISITE_NOT_MET")
		return
	}

	host := cfg.Server.Host
	if host == "" {
		host = defaultHost
	}
	port := cfg.Server.Port
	if port == 0 {
		port = defaultPort
	}

	// Live session 2026-06-08: a previous (pre-fix) wizard would
	// persist host="127.0.0.1" in config.toml. That binds the server
	// to loopback only, making the catalog unreachable from the Meta
	// Quest on the LAN. Upgrade any legacy loopback host to 0.0.0.0
	// (the new defaultHost) so the server listens on every interface.
	// The admin shell stays reachable at 127.0.0.1:port (loopback) and
	// the VRHub client can now reach the catalog at the LAN IP shown
	// in baseURI below.
	upgraded := false
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		host = defaultHost // 0.0.0.0
		cfg.Server.Host = host
		upgraded = true
	}
	if upgraded {
		if saveErr := config.Save(cfg, h.DataDir); saveErr != nil {
			vlog.Get().Warn().Err(saveErr).Msg("failed to persist upgraded bind host")
		}
	}

	// Story 9.1 (B1): reload config from disk so we propagate the
	// post-upgrade state (host=0.0.0.0, password_hash from step 1) to
	// every handler that captured the nil cfg at router construction.
	// The in-memory `cfg` we have here is the one we just read at the
	// top of this handler — reloading picks up the just-persisted host
	// upgrade AND guarantees we hand out a *types.Config that matches
	// exactly what's on disk (defense against in-flight changes from
	// other goroutines, which are implausible here but cheap to defend
	// against). Best-effort: if the reload fails (unlikely; we just
	// wrote the file), fall back to the in-memory cfg so the launch
	// still succeeds.
	propagatedCfg, reloadErr := config.Load(h.DataDir)
	if reloadErr != nil || propagatedCfg == nil {
		vlog.Get().Warn().Err(reloadErr).Msg("launch: post-save config reload failed, propagating in-memory cfg")
		propagatedCfg = cfg
	}

	// Propagate the freshly-loaded cfg to PublicAPIHandler.Config,
	// AdminHandler.Config (defensive — resolveConfig already handles
	// this), and UpdateHandler.UpdateConfig. The closure is wired by
	// SetupRouter; in unit tests it's nil and the call is a no-op.
	if h.ConfigPropagator != nil {
		h.ConfigPropagator(propagatedCfg)
	}

	// baseURI for the VRHub app on the Meta Quest: it MUST be the
	// machine's LAN IP, not the bind address. The bind address can be
	// 0.0.0.0 (a wildcard, not a real IP) — pointing the Quest at
	// http://0.0.0.0:39457 wouldn't work. Detect the real LAN IP and
	// show that in the wizard. If detection fails (e.g. test
	// environment with only loopback), fall back to 127.0.0.1.
	baseURIHost := getOutboundIP()
	baseURI := fmt.Sprintf("http://%s:%d/", baseURIHost, port)

	// Idempotency note: TransitionToNormal checks getMode() and returns
	// without flipping if the server is already in normal mode. The
	// ConfigPropagator above runs unconditionally — that's fine, it's
	// idempotent (just an assignment) and a defensive re-push after a
	// pathological double-launch is harmless.
	h.TransitionToNormal()

	instructions := []string{
		// F9: French to match the rest of the (Michel) wizard.
		"Ouvre l'application VRHub sur ton casque Meta Quest",
		"Va dans Paramètres > Configuration du serveur",
		"Saisis l'URI de base ci-dessus",
		"Saisis le mot de passe affiché ci-dessus",
		"Appuie sur Connecter",
	}

	result := types.LaunchResult{
		BaseURI:      baseURI,
		Password:     cfg.Admin.ArchivePassword,
		Instructions: instructions,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data": result,
	})
}

// HandleReviewPOST handles POST /admin/api/setup/review.
func (h *SetupHandler) HandleReviewPOST(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req reviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body", "INVALID_INPUT")
		return
	}

	mode := h.getMode()

	if mode != types.ModeSetup {
		writeError(w, http.StatusForbidden, "Server is not in setup mode", "NOT_IN_SETUP_MODE")
		return
	}

	if len(req.Excluded) > maxExcludedPackages {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Too many excluded packages (max %d)", maxExcludedPackages), "INVALID_INPUT")
		return
	}

	dbPath := filepath.Join(h.DataDir, "vrhub.db")
	dbConn, dbOpenErr := db.Open(dbPath)
	if dbOpenErr != nil {
		fmt.Fprintf(os.Stderr, "[review] db open failed: %v\n", dbOpenErr)
		writeError(w, http.StatusInternalServerError, "Failed to open database", "DB_ERROR")
		return
	}
	defer dbConn.Close()

	ctx := r.Context()
	tx, txErr := dbConn.BeginTx(ctx, nil)
	if txErr != nil {
		fmt.Fprintf(os.Stderr, "[review] transaction begin failed: %v\n", txErr)
		writeError(w, http.StatusInternalServerError, "Failed to start database transaction", "DB_ERROR")
		return
	}

	excludedSet := make(map[string]bool)
	for _, pkg := range req.Excluded {
		trimmed := strings.TrimSpace(pkg)
		if trimmed == "" {
			continue
		}
		excludedSet[trimmed] = true
	}

	rowsAffected, err := dbConn.UpdateGamesExposedTx(tx, excludedSet)
	if err != nil {
		tx.Rollback()
		fmt.Fprintf(os.Stderr, "[review] update games exposed failed: %v\n", err)
		writeError(w, http.StatusInternalServerError, "Failed to update game exclusions", "DB_ERROR")
		return
	}

	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "[review] transaction commit failed: %v\n", err)
		writeError(w, http.StatusInternalServerError, "Failed to commit database changes", "DB_ERROR")
		return
	}

	result := types.ReviewResult{
		UpdatedCount: int(rowsAffected),
		Message:      "Game exclusions updated",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data": result,
	})
}
