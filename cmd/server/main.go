package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/api"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/auth"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/config"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/db"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/firewall"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/game"
	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/metadata"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/monitor"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/network"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/trailers"
	"github.com/LeGeRyChEeSe/vrhub-server/internal/update"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

func main() {
	var dataDir string
	var port int

	flag.StringVar(&dataDir, "data-dir", "", "data directory path (overrides default)")
	flag.IntVar(&port, "port", 0, "server port (overrides config)")
	flag.Parse()

	// Initialize logging.
	vlog.Init()

	// Load configuration — returns nil on first-run (no config file).
	cfg, err := config.LoadOrCreate(dataDir)
	if cfg == nil {
		if err != nil {
			vlog.Get().Fatal().Err(err).Msg("Failed to load config")
		}
		fmt.Fprintln(os.Stderr, "First run detected — no config file found.")
		fmt.Fprintln(os.Stderr, "Starting setup wizard at /admin/setup")
	} else if err != nil {
		// Config file exists but failed to parse.
		vlog.Get().Fatal().Err(err).Msg("Failed to load config")
	}

	// Resolve dataDir to default if empty.
	if dataDir == "" {
		dataDir = config.GetDefaultDataDir()
	}

	// Clean up after a previously interrupted update. This must run before
	// anything that holds an exclusive lock on the binary or data dir
	// (db.Open, file watcher, etc.) so the recovery can rename `.updating`
	// back to the executable path if needed.
	if err := update.CheckPendingUpdate(dataDir, ""); err != nil {
		vlog.Get().Warn().Err(err).Msg("CheckPendingUpdate encountered an error (continuing)")
	}

	var mode types.ServerMode

	if cfg == nil {
		// First-run: no config file exists.
		mode = types.ModeSetup
		fmt.Fprintf(os.Stderr, "Setup mode: no configuration found at %s\n", dataDir)
	} else {
		// Config loaded successfully — normal mode.
		mode = cfg.Server.Mode
		if mode == "" {
			mode = types.ModeNormal
		}
		// Story 9.8: force setup mode if archive_password is missing
		// (migration path for installs created before 9.8).
		if mode == types.ModeNormal && cfg.Admin.ArchivePassword == "" {
			vlog.Get().Warn().Msg("archive password not configured — forcing setup mode")
			mode = types.ModeSetup
			cfg.Server.Mode = types.ModeSetup
		}
		vlog.Get().Info().Str("mode", string(mode)).Str("data_dir", cfg.DataDir).Msg("Server starting")
	}

	// Create atomic value for runtime mode transitions.
	modeVal := new(atomic.Value)
	modeVal.Store(string(mode))

	// Open database if config exists (normal mode).
	var gameDB *db.DB
	if cfg != nil && cfg.Database.Path != "" {
		gameDB, err = db.Open(cfg.Database.Path)
		if err != nil {
			vlog.Get().Fatal().Err(err).Str("db_path", cfg.Database.Path).Msg("Failed to open database")
		}
		defer gameDB.Close()
	}

	// Create metadata fetcher (normal mode only). The startup fetch and
	// package-source wiring happen after the game-folder scan so the DB is
	// fully populated before the first image-download pass.
	var metaFetcher *metadata.Fetcher
	if mode == types.ModeNormal && cfg != nil {
		refreshInterval := cfg.Metadata.RefreshInterval
		if refreshInterval <= 0 {
			refreshInterval = 24 * time.Hour
		}
		metaFetcher = metadata.NewFetcher(dataDir, cfg.Metadata.URL, refreshInterval)

		// Start scheduled background refresh (unbounded context, controlled by Stop()).
		go func() {
			metaFetcher.StartScheduledFetch(context.Background())
		}()
	}

	// Start update checker in background (normal mode only).
	var updateChecker *update.Checker
	if mode == types.ModeNormal && cfg != nil {
		updateCfg := update.DefaultConfig()
		if cfg.Update.Enabled {
			updateCfg.Enabled = cfg.Update.Enabled
		}
		if cfg.Update.CheckInterval > 0 {
			updateCfg.CheckInterval = cfg.Update.CheckInterval
		}
		if cfg.Update.GithubToken != "" {
			updateCfg.GithubToken = cfg.Update.GithubToken
		}
		if cfg.Update.Owner != "" {
			updateCfg.Owner = cfg.Update.Owner
		}
		if cfg.Update.Repo != "" {
			updateCfg.Repo = cfg.Update.Repo
		}
		updateCfg.AutoApply = cfg.Update.AutoApply
		updateCfg.AutoRestart = cfg.Update.AutoRestart
		updateChecker = update.NewChecker(updateCfg, update.CurrentVersion)

		// Create applicator for auto-apply (if enabled).
		var updateApplicator *update.Applicator
		if cfg.Update.AutoApply {
			applyCfg := update.DefaultApplyConfig(dataDir)
			applyCfg.AutoBackup = true
			applyCfg.AutoRestart = cfg.Update.AutoRestart
			updateApplicator = update.NewApplicator(applyCfg)
			updateChecker.SetApplicator(updateApplicator)
			vlog.Get().Info().Msg("Update auto-apply enabled")
		}

		updateChecker.Start(context.Background())
		// Register as the package-level global so the admin API can read results.
		update.SetGlobalChecker(updateChecker)
		vlog.Get().Info().Msg("Update checker started")
	}

	// Create GameManager for file watcher integration (only in normal mode).
	// Story 3.5: the watcher is owned by a WatcherManager so a settings
	// save that changes game_folders can restart it concurrency-safely.
	var watcherManager *game.WatcherManager
	if mode == types.ModeNormal && gameDB != nil {
		gameManager := game.NewGameManager(gameDB, dataDir)

		// Story 9.10 (B10): startup scan that backfills the new
		// games.apk_path column for games that pre-date the 9.10
		// migration. Runs in two phases:
		//   1. ScanAndImportMultiple walks game_folders and
		//      re-imports any APK (sets apk_path from the actual
		//      disk location). Also handles "file deleted" cleanup
		//      (marks corrupted games unexposed, deletes valid
		//      games).
		//   2. BackfillLegacyApkPaths fills apk_path for the 3
		//      legacy games that the operator manually copied to
		//      dataDir/games/{hash}/{pkgName}/ (those files are
		//      NOT inside game_folders, so phase 1 cannot find
		//      them).
		//
		// Both phases are best-effort: errors are logged at Warn
		// level and the server continues to boot. The scan has a
		// 5-minute timeout to bound startup latency for operators
		// with very large game libraries.
		if cfg != nil && len(cfg.GameFolders) > 0 {
			scanCtx, scanCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			vlog.Get().Info().
				Strs("dirs", cfg.GameFolders).
				Msg("startup: scanning game folders (backfill apk_path + sync DB with disk)")
			scanRes, scanErr := game.ScanAndImportMultiple(scanCtx, cfg.GameFolders, gameManager)
			if scanErr != nil {
				vlog.Get().Warn().Err(scanErr).Msg("startup scan: completed with errors (continuing boot)")
			} else {
				vlog.Get().Info().
					Int("files_scanned", scanRes.FilesScanned).
					Int("games_added", scanRes.GamesAdded).
					Int("games_removed", scanRes.GamesRemoved).
					Int64("total_size_bytes", scanRes.TotalSize).
					Msg("startup scan: phase 1 (game_folders walk) complete")
			}

			// Phase 2: legacy backfill. Always runs — it is a
			// cheap no-op once all games have apk_path set.
			bfUpdated, bfErr := game.BackfillLegacyApkPaths(scanCtx, gameDB, dataDir)
			scanCancel()
			if bfErr != nil {
				vlog.Get().Warn().Err(bfErr).Int("updated_so_far", bfUpdated).Msg("startup scan: phase 2 (legacy backfill) completed with errors (continuing boot)")
			} else {
				vlog.Get().Info().
					Int("updated", bfUpdated).
					Msg("startup scan: phase 2 (legacy backfill) complete")
			}
		}

		// Backfill icons extracted from APKs for games that were imported
		// before icon extraction was wired up (or whose icon was never saved).
		// Runs in the background so it doesn't delay startup.
		go gameManager.BackfillMissingIcons(context.Background())

		// Story 11.1 (Task 5): resolve trailer URLs for games that still have
		// none. Best-effort and non-blocking: runs in the background so it
		// never delays startup. The operator-override sidecar is already
		// applied at import time; this fills the remaining gaps via the
		// oculusdb best-effort step and the optional YouTube Data API.
		go func() {
			resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer resolveCancel()
			resolver := trailers.New(filepath.Join(dataDir, "metadata"))
			if n, rErr := resolver.ResolveMissing(resolveCtx, gameDB, cfg); rErr != nil {
				vlog.Get().Warn().Err(rErr).Msg("startup: trailer resolution completed with errors")
			} else if n > 0 {
				vlog.Get().Info().Int("resolved", n).Msg("startup: trailer URLs resolved")
			}
		}()

		// Wire metadata fetcher to the game DB so image downloads are scoped
		// to the operator's library, then trigger a startup fetch if overdue.
		// Done here — after the scan — so the DB is fully populated.
		if metaFetcher != nil {
			metaFetcher.SetPackageSource(func() []string {
				games, dbErr := gameDB.ListAllGamesOrderedByName()
				if dbErr != nil {
					return nil
				}
				pkgs := make([]string, 0, len(games))
				for _, g := range games {
					pkgs = append(pkgs, g.PackageName)
				}
				return pkgs
			})

			// Always enrich on startup: regenerate notes/images from the
			// already-extracted MetaMetadata cache without any network I/O.
			// This covers games added since the last tarball refresh.
			go func() {
				enrichCtx, enrichCancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer enrichCancel()
				games, dbErr := gameDB.ListAllGamesOrderedByName()
				if dbErr != nil {
					vlog.Get().Warn().Err(dbErr).Msg("startup enrich: failed to list games")
					return
				}
				pkgs := make([]string, 0, len(games))
				for _, g := range games {
					pkgs = append(pkgs, g.PackageName)
				}
				metaFetcher.EnrichGames(enrichCtx, pkgs)
			}()

			if metaFetcher.IsRefreshOverdue() {
				vlog.Get().Info().Msg("Metadata refresh overdue on startup, fetching now")
				go func() {
					fetchCtx, fetchCancel := context.WithTimeout(context.Background(), 10*time.Minute)
					defer fetchCancel()
					if err := metaFetcher.Fetch(fetchCtx); err != nil {
						vlog.Get().Warn().Err(err).Msg("Startup metadata refresh failed")
					}
				}()
			}
		}

		// Story 3.5: per-event import/removal handler. Reused across
		// watcher restarts (config change) so the logic lives in one
		// place. The watcher now targets cfg.GameFolders, not dataDir.
		watchHandler := func(event game.FileEvent) error {
			ext := filepath.Ext(event.FilePath)
			logger := vlog.Get()

			switch event.EventType {
			case game.EventAdded:
				if ext == ".apk" {
					logger.Info().Str("event", "added").Str("file", event.FilePath).Msg("detected new APK file")
					if err := gameManager.ImportAPK(event.FilePath); err != nil {
						logger.Error().Err(err).Str("event", "added").Str("file", event.FilePath).Msg("failed to import APK from watcher")
					}
				} else if ext == ".obb" {
					logger.Info().Str("event", "added").Str("file", event.FilePath).Msg("detected new OBB file, waiting for paired APK")
				}

			case game.EventRemoved:
				if ext == ".apk" {
					logger.Info().Str("event", "removed").Str("file", event.FilePath).Msg("detected removed APK file, skipping DB removal (periodic scan handles cleanup)")
				} else if ext == ".obb" {
					logger.Info().Str("event", "removed").Str("file", event.FilePath).Msg("detected removed OBB file")
				}

			case game.EventModified:
				if ext == ".apk" {
					logger.Info().Str("event", "modified").Str("file", event.FilePath).Msg("detected modified APK file, updating last_updated")
					meta, metaErr := game.ExtractAPKMetadata(event.FilePath)
					if metaErr != nil || meta.PackageName == "" {
						logger.Warn().Str("file", event.FilePath).Msg("cannot determine package name for modified file, skipping re-import")
					} else if importErr := gameManager.ImportAPK(event.FilePath); importErr != nil {
						logger.Error().Err(importErr).Str("package", meta.PackageName).Str("event", "modified").Msg("failed to update modified APK")
					}
				} else if ext == ".obb" {
					logger.Info().Str("event", "modified").Str("file", event.FilePath).Msg("detected modified OBB file, size tracking will update on next scan")
				}
			}
			return nil
		}

		// Story 3.5 (AC1/AC5): start the watcher on the configured game
		// folders, not dataDir. Only start when at least one folder is
		// configured (mirrors the startup-scan guard above). The manager
		// is always created so the settings-change restart hook works
		// even if folders are added later.
		watcherManager = game.NewWatcherManager(gameManager, watchHandler)
		if cfg != nil && len(cfg.GameFolders) > 0 {
			if startErr := watcherManager.Start(cfg.GameFolders); startErr != nil {
				vlog.Get().Warn().Err(startErr).Msg("Failed to start file watcher")
			} else {
				vlog.Get().Info().Str("platform", runtime.GOOS).Strs("folders", cfg.GameFolders).Msg("file watcher started")
			}
		}
	}

	// Story 7.6 T3: network reachability checker.
	//
	// Single background goroutine that HEADs GitHub and the
	// MetaMetadata URL every 60s and updates an in-memory
	// status snapshot. The admin API exposes it via
	// GET /admin/api/network-status (read-only, no mode-gate);
	// the admin shell shows the result as a colored badge in
	// the Michel header and the Power sidebar (T2).
	//
	// Wiring decision: instantiate ONLY in normal mode (mirrors
	// the update checker and metadata fetcher). Setup mode
	// doesn't need reachability info — the setup wizard
	// doesn't poll external services.
	var netChecker *network.Checker
	if mode == types.ModeNormal && cfg != nil {
		githubURL := network.DefaultGitHubURL
		if cfg.Update.Owner != "" && cfg.Update.Repo != "" {
			// Use the operator-configured GitHub owner/repo so the
			// checker tests the URL the update checker actually
			// polls. Falls back to the default LeGeRyChEeSe/vrhub-server
			// if the operator hasn't customized.
			githubURL = "https://api.github.com/repos/" + cfg.Update.Owner + "/" + cfg.Update.Repo + "/releases/latest"
		}
		metadataURL := cfg.Metadata.URL
		if metadataURL == "" {
			// Reuse the network package's default (the public
			// constant exposed in checker.go). The metadata
			// package keeps its own unexported copy; the two
			// URLs are identical by design (both point at the
			// same vrhub-metadata GitHub release tarball).
			metadataURL = network.DefaultMetadataURL
		}
		netChecker = network.NewChecker(githubURL, metadataURL, 60*time.Second)
		// Start in a goroutine — Start() itself is non-blocking
		// (it spawns its own loop goroutine), but doing the
		// Start() call in a goroutine here makes the wiring
		// symmetric with update.Checker.Start() (also called
		// inline above) and avoids any chance of a future
		// blocking Start() call delaying the HTTP listener.
		go func() {
			netChecker.Start(context.Background())
			vlog.Get().Info().Msg("Network checker started")
		}()
	}

	// Story 6.4 Task 3: first-run API key generation. If
	// cfg.Admin.APIKeyHash is empty, generate a fresh key, persist the
	// hash to disk, and log the plaintext ONCE so the operator can
	// save it. Subsequent boots use the persisted hash; the plaintext
	// is held in memory only after a fresh regenerate-via-settings.
	if cfg != nil && cfg.Admin.APIKeyHash == "" {
		plaintext, hash, err := auth.GenerateAPIKey()
		if err != nil {
			vlog.Get().Error().Err(err).Msg("failed to generate initial API key; API key auth will return 503 until configured")
		} else {
			cfg.Admin.APIKeyHash = hash
			cfg.Admin.APIKeyPlaintext = plaintext
			if writeErr := config.WriteConfig(cfg, dataDir); writeErr != nil {
				vlog.Get().Error().Err(writeErr).Msg("failed to persist initial API key hash; API key auth will return 503 on next boot")
			} else {
				// Banner output: surface the plaintext ONCE on the
				// server stderr so the operator can copy it. Future
				// boots won't re-emit this (the hash is now persisted).
				fmt.Fprintf(os.Stderr, "\n"+
					"╔════════════════════════════════════════════════════════════════╗\n"+
					"║  API KEY GENERATED — SAVE THIS KEY IMMEDIATELY                 ║\n"+
					"║                                                                ║\n"+
					"║  %s\n"+
					"║                                                                ║\n"+
					"║  Use it as the X-API-Key header on /admin/api/scripts/* routes. ║\n"+
					"║  It will NOT be shown again. Regenerate via admin settings     ║\n"+
					"║  page (requires admin session login) to rotate.                ║\n"+
					"╚════════════════════════════════════════════════════════════════╝\n\n", plaintext)
				vlog.Get().Info().Str("event", "api_key_first_run_generated").Str("key_hint", plaintext[:4]+"..."+plaintext[len(plaintext)-4:]).Msg("first-run API key generated; plaintext logged ONCE to stderr")
			}
		}
	}

	// Create session store for admin authentication.
	//
	// R7-CRITICAL-SESSION-INIT: the store is created unconditionally at startup so
	// the /admin/api/auth/login and /admin/api/auth/logout routes are registered.
	// In setup mode the SetupModeMiddleware redirects login attempts to /admin/setup;
	// the store's lifetime spans the setup→normal transition without requiring a
	// router rebuild. The login handler reloads config.toml from disk on each
	// request so the credentials written by the setup wizard are picked up even
	// though the in-memory cfg pointer at startup is nil. Stop() is called on
	// shutdown (line below) to cancel the janitor goroutine and wipe the map.
	sessionStore := auth.NewSessionStore(context.Background())

	// Build router based on mode. Story 6-3 R13-P2: wire the
	// live-rebind reloader and the update-checker pusher so AC2
	// (live rebind on server.host/port change) and Task 5
	// (update-checker hot-reload) actually work in production.
	// Story 7.6 T3: also pass the netChecker so the admin handler
	// can serve GET /admin/api/network-status.
	// B-02 (review 2026-06-11): create the in-process monitor bus
	// and pass it through SetupRouter. Without this, the
	// /admin/monitoring endpoint sends a single "bus not
	// configured" event and dies — the monitoring page is empty.
	monitorBus := monitor.NewEventBus()
	reloader := newLiveRebinder()
	updatePusher := newUpdateConfigPusher()
	// Story 3.5 (AC3): the watcher-restart hook fires when a settings
	// save mutates game_folders. nil when there's no watcher manager
	// (setup mode / DB unavailable).
	var gameFoldersChangedHook func([]string)
	if watcherManager != nil {
		gameFoldersChangedHook = func(folders []string) {
			if restartErr := watcherManager.Restart(folders); restartErr != nil {
				vlog.Get().Warn().Err(restartErr).Strs("folders", folders).Msg("failed to restart file watcher after game_folders change")
			}
		}
	}
	r := api.SetupRouter(modeVal, dataDir, gameDB, cfg, sessionStore, reloader, updatePusher, netChecker, monitorBus, gameFoldersChangedHook)

	// Determine listen address.
	// Live session 2026-06-08: default bind host is "0.0.0.0" (all
	// interfaces) so the Meta Quest on the LAN can reach the catalog
	// at the machine's LAN IP, while the admin shell stays reachable
	// at 127.0.0.1:port from the same machine. The wizard step 4
	// shows the real LAN IP for the VRHub client to use.
	// Determine listen address. Priority: -port flag > config > default 39457.
	resolvedPort := 39457
	if cfg != nil && cfg.Server.Port > 0 {
		resolvedPort = cfg.Server.Port
	}
	if port > 0 {
		resolvedPort = port
	}
	// Sync back so HandleClientConfigGET reports the actual listen port
	// (the -port flag overrides only resolvedPort, not cfg.Server.Port).
	if cfg != nil {
		cfg.Server.Port = resolvedPort
	}
	host := "0.0.0.0"
	if cfg != nil {
		host = cfg.Server.Host
		if host == "" {
			host = "0.0.0.0"
		}
	}
	addr := fmt.Sprintf("%s:%d", host, resolvedPort)

	// B-02: publish a "server_started" event so the monitoring feed
	// has at least one event on the wire. Subsequent publishes come
	// from middleware_monitor.go (HTTP request events) and any
	// future emitter (game watcher, scan progress, etc.).
	if mode != types.ModeSetup {
		monitorBus.Publish(monitor.Event{
			Type: "server_started",
			Data: map[string]any{
				"version":  update.CurrentVersion.String(),
				"mode":     string(mode),
				"data_dir": dataDir,
				"addr":     addr,
			},
		})
	}

	// Best-effort: open the listening port in the host firewall so
	// LAN clients (Meta Quest on Wi-Fi) can reach the server without
	// the operator having to click through the "Windows Security
	// Alert" popup. On non-Windows this is a no-op. On Windows a
	// failure here is non-fatal: the admin UI on 127.0.0.1 stays
	// reachable regardless of the firewall state, so the operator
	// can still log in and reconfigure. We log at Warn (not Error)
	// to keep the boot log clean when the operator is intentionally
	// running unelevated (e.g. dev iteration).
	if err := firewall.EnsureOpen(resolvedPort); err != nil {
		vlog.Get().Warn().Err(err).Int("port", resolvedPort).Str("rule", firewall.RuleName(resolvedPort)).Msg("firewall: could not open inbound TCP port (continuing — see admin UI for status)")
	} else {
		vlog.Get().Info().Int("port", resolvedPort).Str("rule", firewall.RuleName(resolvedPort)).Msg("firewall: inbound TCP port opened")
	}

	fmt.Fprintf(os.Stderr, "Listening on %s (mode=%s)\n", addr, string(mode))
	vlog.Get().Info().Str("addr", addr).Str("mode", string(mode)).Msg("Server starting")

	// Start HTTP server in a goroutine so we can handle signals.
	server := &http.Server{Addr: addr, Handler: r}

	// Wire the live-rebinder so Rebind() can swap the listener.
	reloader.server = &server
	reloader.router = r
	reloader.curAddr = addr
	reloader.curPort = resolvedPort

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			vlog.Get().Fatal().Err(err).Msg("Server failed")
		}
	}()

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	vlog.Get().Info().Msg("Shutting down...")

	// Stop HTTP server with timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		vlog.Get().Error().Err(err).Msg("HTTP shutdown error")
	}

	// Stop file watcher if active.
	if watcherManager != nil {
		watcherManager.Stop()
		vlog.Get().Info().Msg("file watcher stopped")
	}

	// Stop metadata fetcher with 60s timeout for in-flight operations.
	if metaFetcher != nil {
		metaFetcher.Stop()
		if !metaFetcher.Wait(60 * time.Second) {
			vlog.Get().Warn().Msg("metadata fetcher did not stop within 60s timeout")
		} else {
			vlog.Get().Info().Msg("metadata fetcher stopped")
		}
	}

	// Stop update checker.
	if updateChecker != nil {
		updateChecker.Stop()
		vlog.Get().Info().Msg("update checker stopped")
	}

	// Story 7.6 T3: stop the network reachability checker.
	// Stop() is idempotent (CAS), so calling it here is safe
	// even if the goroutine was never started.
	if netChecker != nil {
		netChecker.Stop()
		vlog.Get().Info().Msg("network checker stopped")
	}

	// Stop session store (cancels janitor goroutine).
	if sessionStore != nil {
		sessionStore.Stop()
		vlog.Get().Info().Msg("session store stopped")
	}

	// Best-effort: remove the firewall rule we created at startup.
	// Skipped on a port we never opened (e.g. the server started
	// before this code path landed and a manual rule is in place).
	// A failure here is non-fatal: a stale rule doesn't break the
	// next boot (EnsureOpen deletes+recreates on every start).
	if err := firewall.Remove(resolvedPort); err != nil {
		vlog.Get().Warn().Err(err).Int("port", resolvedPort).Msg("firewall: could not remove inbound rule on shutdown (non-fatal)")
	}

	vlog.Get().Info().Msg("Server stopped")
}

// liveRebinder implements api.Reloader for Story 6-3 R13-P2: it
// gracefully shuts down the current HTTP server and starts a new one
// on the requested address (the address constructed from the new
// server.host + server.port after a settings PUT). R13-P10: if the
// new listener fails to bind, the old listener is preserved (the
// caller in admin_settings.go logs the error and returns a
// rebind_status: "failed" in the response).
//
// On top of the rebind itself, this also keeps the host firewall in
// sync with the listening port: if the port changes, the rule for
// the old port is removed and a rule for the new port is added. Both
// calls are best-effort and never fail the rebind — a Windows
// non-elevated process is expected to see the warn logs and the
// operator can fix the firewall manually (the admin UI stays
// reachable on 127.0.0.1).
type liveRebinder struct {
	mu      sync.Mutex
	server  **http.Server
	router  http.Handler
	curAddr string
	curPort int
}

func newLiveRebinder() *liveRebinder {
	return &liveRebinder{}
}

// Shutdown gracefully shuts down the current HTTP server, closing its
// listener so the replacement process can bind the same address.
// Called by UpdateHandler before triggering a process restart.
func (r *liveRebinder) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	s := *r.server
	r.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.Shutdown(ctx)
}

// Rebind shuts down the current server (if any) and starts a new one
// on addr. Returns an error if the new listener fails to bind (in
// which case the old listener is left running).
func (r *liveRebinder) Rebind(addr string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.server != nil && r.curAddr == addr {
		// No-op: already on this address.
		return nil
	}

	// Start the new server first. If it binds successfully, shut down
	// the old one (so the rebind is a no-downtime operation when
	// the host/port are valid; if binding fails, the old one keeps
	// running and the operator sees the rebind_status: "failed" in
	// the response).
	newServer := &http.Server{Addr: addr, Handler: r.router}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("rebind: listen on %s: %w", addr, err)
	}
	oldServer := *r.server
	oldAddr := r.curAddr
	oldPort := r.curPort
	*r.server = newServer
	r.curAddr = addr
	// Cache the new port for the next rebind / for the shutdown
	// path. host changes (e.g. 0.0.0.0 -> 127.0.0.1) keep the same
	// firewall rule, so we only re-derive the port here.
	if _, newPort, splitErr := net.SplitHostPort(addr); splitErr == nil {
		if p, convErr := strconv.Atoi(newPort); convErr == nil {
			r.curPort = p
		}
	}

	// Start the new server in a goroutine.
	go func() {
		if err := newServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			vlog.Get().Error().Err(err).Str("addr", addr).Msg("rebound server crashed")
		}
	}()

	// Sync the firewall with the new port. EnsureOpen is idempotent
	// (delete+add) so calling it on a port that already has a rule
	// is a cheap no-op. Remove of the old port only matters when
	// the port actually changed; otherwise we skip it to avoid an
	// extra netsh call.
	if r.curPort != 0 && r.curPort != oldPort {
		if err := firewall.EnsureOpen(r.curPort); err != nil {
			vlog.Get().Warn().Err(err).Int("port", r.curPort).Msg("firewall: could not open inbound rule for rebound port (continuing — manual firewall fix may be required)")
		}
		if oldPort != 0 {
			if err := firewall.Remove(oldPort); err != nil {
				vlog.Get().Warn().Err(err).Int("port", oldPort).Msg("firewall: could not remove old inbound rule on rebind (non-fatal)")
			}
		}
	}

	// Shut down the old server (if any, and if it's on a different addr).
	// Runs in a goroutine: Rebind() is called from an HTTP handler that
	// is itself in-flight on oldServer; blocking on Shutdown() would
	// stall that handler and deadlock if the server waits for all
	// handlers to finish before returning from Shutdown().
	if oldServer != nil && oldAddr != "" && oldAddr != addr {
		go func() {
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutCancel()
			if err := oldServer.Shutdown(shutCtx); err != nil {
				vlog.Get().Warn().Err(err).Str("addr", oldAddr).Msg("graceful shutdown of old listener failed")
			}
		}()
	}

	vlog.Get().Info().Str("old_addr", oldAddr).Str("new_addr", addr).Msg("listener rebound")
	return nil
}

// updateConfigPusherImpl implements api.UpdateConfigPusher for Story 6-3
// Task 5: pushes new update-checker config to the update package
// when update.enabled or update.auto_apply change via the settings
// page. The implementation is intentionally minimal — it just
// signals the update package via a best-effort log; the actual
// update checker reads from disk on each tick so the new value
// takes effect on the next loop iteration without explicit push.
func newUpdateConfigPusher() api.UpdateConfigPusher {
	return func(cfg *types.UpdateConfig) {
		if cfg == nil {
			return
		}
		vlog.Get().Info().
			Bool("enabled", cfg.Enabled).
			Bool("auto_apply", cfg.AutoApply).
			Msg("update checker config pushed (R13-P11 hot-reload)")
	}
}
