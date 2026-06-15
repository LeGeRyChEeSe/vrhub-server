package ui

import (
	"embed"
	"html"
	"strings"
)

//go:embed embed/admin.css
//go:embed embed/admin.js
//go:embed embed/login.js
//go:embed embed/setup.css
//go:embed embed/setup.js
//go:embed embed/fonts/*.woff2
var StaticFS embed.FS

// Font returns the embedded WOFF2 font bytes for the given base filename
// (e.g. "andika-400.woff2"). The name is reduced to its base component so a
// caller-supplied value cannot traverse out of embed/fonts. Returns nil if
// the file does not exist. (F6: Andika is self-hosted, not loaded from the
// Google Fonts CDN, so the appliance UI renders with its brand font offline.)
func Font(name string) []byte {
	base := name
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	if base == "" || !strings.HasSuffix(base, ".woff2") {
		return nil
	}
	data, err := StaticFS.ReadFile("embed/fonts/" + base)
	if err != nil {
		return nil
	}
	return data
}

// adminHTMLTemplate is the SPA admin shell served on /admin/ and all of
// the legacy /admin/<page> routes (those are now 302-redirected to
// /admin/#/<route> by router.go — see the Story X redirect block).
//
// Story X (UI/UX refonte, live session 2026-06-10): the shell is now a
// single-page app with hash routing. Every section (dashboard, games,
// configuration, backup, API docs, monitoring, statistics) lives in
// <section data-route="..."> blocks inside <main id="app-main">. The
// router in admin.js toggles `document.body.dataset.route` on
// `hashchange`, and CSS shows the matching section (one and only one
// section is visible at a time).
//
// The header (#app-header) is a UNIFIED bar that is identical in both
// Michel and Power modes — it always carries the server status dot,
// server name, baseUri chip, archive-password chip (with reveal
// button), network badge, language selector, and logout button. The
// toggle between Michel and Power mode lives in a discrete segmented
// control (#mode-switch) at the bottom-left of the page, persistent
// in both modes.
//
// The legacy components are gone:
//   - #sidebar (Power mode sidebar)         — replaced by #section-nav
//   - #michel-header                        — replaced by #app-header
//   - #michel-main, #power-main             — replaced by #section-dashboard
//   - #login-section                        — was a placeholder; the
//     login form lives on a
//     dedicated /admin/login
//     page (Story 9.5 / B5)
//   - #settings-section                     — was a placeholder; the
//     settings form is rendered
//     inside #section-configuration
//     by the JS (no full reload)
//   - <footer class="mode-footer">          — replaced by #mode-switch
const adminHTMLTemplate = `<!DOCTYPE html>
<!-- P14: Default to French (Michel mode default) — inline script updates on load -->
<html lang="fr">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <!-- Story 1.8 follow-up (live session 2026-06-08): the real CSRF
         token is injected by HandleSettingsGET per request. The
         placeholder is replaced with the per-session HMAC token so
         admin.js can include it in X-CSRF-Token for state-changing
         requests (logout, settings PUT, API key regen, etc.). On
         unauthenticated requests (no session) the placeholder stays
         empty, which makes validateCSRF fail closed and protects
         state-changing endpoints. -->
    <meta name="csrf-token" content="__VRHUB_CSRF_TOKEN__">
    <title>VRHub Server - Admin</title>
    <link rel="stylesheet" href="/admin/static/admin.css">
</head>
<body class="mode-michel" data-route="dashboard">
<!-- Mode + lang detection: run synchronously right after body opens
     to prevent FOUC. Reads ?mode= and ?lang= from the URL first, then
     falls back to localStorage. Sets BOTH body class (mode-michel /
     mode-power) AND html[lang] (fr / en) so the page renders with the
     right colors AND the right text direction on first paint. -->
<script>(function(){var p=new URLSearchParams(location.search);var m=(p.get('mode')||'').trim().toLowerCase();var l=(p.get('lang')||'').trim().toLowerCase();if(m==='power'){document.body.classList.remove('mode-michel','mode-power');document.body.classList.add('mode-power');try{localStorage.setItem("vrhub:admin-mode","power")}catch(e){}}else if(m==='michel'){document.body.classList.remove('mode-michel','mode-power');document.body.classList.add('mode-michel');try{localStorage.setItem("vrhub:admin-mode","michel")}catch(e){}}else{var s;try{s=localStorage.getItem("vrhub:admin-mode")}catch(e){}document.body.classList.remove('mode-michel','mode-power');document.body.classList.add(s==="power"?"mode-power":"mode-michel")}if(l==='fr'||l==='en'){try{localStorage.setItem("vrhub:lang",l)}catch(e){}document.documentElement.lang=l}else{var ls;try{ls=localStorage.getItem("vrhub:lang")}catch(e){}document.documentElement.lang=(ls==='fr'||ls==='en')?ls:(document.body.classList.contains('mode-power')?'en':'fr')}})();</script>
    <!-- Update notification banner - hidden by default -->
    <div id="update-banner" class="update-banner" style="display: none;">
        <span class="update-banner-text">
            Update available: <span class="version" id="update-version">v0.0.0</span> — click to update
        </span>
        <button class="update-banner-btn" id="update-btn">Update Now</button>
    </div>

    <!-- Story X: UNIFIED HEADER (identical in Michel and Power modes).
         Layout: server status dot · server name (clickable) · baseUri
         chip (clickable) · archive password chip (with reveal) ·
         network badge · language selector · logout button. -->
    <header id="app-header">
        <span class="status-dot" id="header-status-dot" aria-hidden="true"></span>
        <span class="header-server-name" id="header-server-name" data-i18n-title="header_server_name_title" title="Click to copy baseUri">VRHub Server</span>
        <span class="header-chip header-baseuri" id="header-baseuri" role="button" tabindex="0" data-i18n-title="header_baseuri_title" title="Click to copy">—</span>
        <span class="header-chip header-password-chip">
            <span class="header-chip-label" id="header-archive-password" data-i18n="password_label">••••••••</span>
            <button type="button" id="header-archive-password-reveal" class="header-chip-reveal" aria-pressed="false" data-i18n-title="header_reveal_title" title="Show password">👁</button>
        </span>
        <span class="badge-pill badge-muted network-status-badge" id="header-network-badge" data-i18n-title="network_status_title" title="Network status: checking…" aria-label="Network status: checking">●</span>
        <button type="button" id="header-theme-toggle" class="header-theme-toggle" role="switch" aria-checked="false" data-i18n-title="header_theme_title" title="Basculer thème clair / sombre" aria-label="Toggle light/dark theme">
            <span class="theme-toggle-thumb" aria-hidden="true"></span>
        </button>
        <select id="header-lang-selector" class="header-lang-selector" data-i18n-title="header_lang_title" title="Interface language">
            <option value="fr">Français</option>
            <option value="en">English</option>
        </select>
        <button type="button" id="header-logout-btn" class="btn btn-secondary" data-i18n="logout_button">Logout</button>
    </header>

    <!-- Power mode navigation (hidden in Michel via CSS). DESIGN.md: a
         240px fixed left sidebar with category groups (collapses to a
         horizontal scrollable bar on narrow screens via the responsive
         rules). The router toggles .active on the matching
         .section-nav-link on every hashchange (class-based selector, so the
         wrapping .nav-group divs do not affect it). -->
    <nav id="section-nav" aria-label="Sections">
        <div class="nav-group">
            <span class="nav-group-label" data-i18n="nav_cat_status">Status</span>
            <a href="#/dashboard" class="section-nav-link" data-nav-route="dashboard" data-i18n="nav_dashboard">Dashboard</a>
        </div>
        <div class="nav-group">
            <span class="nav-group-label" data-i18n="nav_cat_content">Content</span>
            <a href="#/games" class="section-nav-link" data-nav-route="games" data-i18n="nav_games">Games</a>
        </div>
        <div class="nav-group">
            <span class="nav-group-label" data-i18n="nav_cat_configuration">Configuration</span>
            <a href="#/configuration" class="section-nav-link" data-nav-route="configuration" data-i18n="nav_configuration">Configuration</a>
        </div>
        <div class="nav-group">
            <span class="nav-group-label" data-i18n="nav_cat_reference">Reference</span>
            <a href="#/api-docs" class="section-nav-link" data-nav-route="api-docs" data-i18n="nav_api_docs">API Docs</a>
        </div>
        <div class="nav-group">
            <span class="nav-group-label" data-i18n="nav_cat_observability">Observability</span>
            <a href="#/monitoring" class="section-nav-link" data-nav-route="monitoring" data-i18n="nav_monitoring">Monitoring</a>
            <a href="#/stats" class="section-nav-link" data-nav-route="stats" data-i18n="nav_stats">Statistics</a>
        </div>
        <div class="nav-group">
            <span class="nav-group-label" data-i18n="nav_cat_maintenance">Maintenance</span>
            <a href="#/backup" class="section-nav-link" data-nav-route="backup" data-i18n="nav_backup">Backup</a>
            <a href="#/client-setup" class="section-nav-link" data-nav-route="client-setup" data-i18n="nav_client_setup">Client setup</a>
            <a href="#/updates" class="section-nav-link" data-nav-route="updates" data-i18n="nav_updates">Updates</a>
        </div>
    </nav>

    <!-- Story X: SPA main — every section lives here. CSS shows the
         section whose name matches body[data-route] (default
         'dashboard'). Only ONE section is visible at a time. -->
    <main id="app-main">
        <!-- Dashboard (default route). The same DOM hosts Michel and
             Power widgets; CSS hides the wrong-mode widgets based on
             body class. -->
        <section data-route="dashboard" id="section-dashboard" class="fade-in">
            <h1 data-i18n="dashboard_title">Tableau de bord</h1>
            <p data-i18n="welcome_message">Bienvenue dans l'administration de VRHub Server</p>

            <!-- Michel-mode widgets (hidden in Power via CSS) -->

            <!-- Update notification card — shown by JS when update available or restart pending -->
            <div class="card update-card tiltable hidden" id="michel-update-card">
                <div class="card-header">
                    <span class="card-title" id="michel-update-card-title" data-i18n="update_available_title">Mise à jour disponible</span>
                    <span class="badge-pill badge-muted" id="michel-installed-badge"></span>
                    <span class="badge-pill badge-update" id="michel-latest-badge"></span>
                </div>
                <div class="update-card-changelog" id="michel-update-notes"></div>
                <div class="update-card-actions" id="michel-update-actions" style="margin-top: var(--space-3); display: flex; gap: var(--space-2); flex-wrap: wrap;"></div>
            </div>

            <div class="card status-widget tiltable" id="status-widget">
                <div class="card-header">
                    <span class="card-title" data-i18n="server_status_label">État du serveur</span>
                    <span class="status-dot" id="status-dot"></span>
                </div>
                <p id="status-text" data-i18n="status_checking">Vérification...</p>
            </div>

            <div class="card config-widget tiltable" id="config-widget">
                <div class="card-header">
                    <span class="card-title" data-i18n="config_title">Configuration</span>
                </div>
                <div class="config-item">
                    <div class="config-label" data-i18n="base_uri_label">URI de base</div>
                    <div class="config-value" id="config-baseuri">—</div>
                </div>
                <div class="config-item mt-4">
                    <div class="config-label" data-i18n="password_label">Mot de passe</div>
                    <div class="config-value" id="config-password">
                        <span id="password-masked">••••••••</span>
                        <span id="password-visible" style="display: none;"></span>
                        <a href="#" id="toggle-password" class="text-muted" style="font-size: 0.75rem; margin-left: var(--space-2);" data-i18n="show_password">Afficher</a>
                    </div>
                </div>
            </div>

            <div class="card tiltable" id="game-count-widget">
                <div class="card-header">
                    <span class="card-title" data-i18n="game_count_label">Jeux détectés</span>
                    <span class="badge-pill badge-primary" id="game-count-badge">0</span>
                </div>
                <p class="text-muted" id="last-scan-text" data-i18n="last_scan_placeholder">Scanné à l'instant</p>
                <button type="button" id="dashboard-rescan-btn" class="btn btn-primary" data-i18n="rescan_btn">Rescan</button>
                <p id="dashboard-rescan-status" class="text-muted" style="margin-top: var(--space-2); min-height: 1.25rem;"></p>
            </div>

            <!-- Power-mode widgets (hidden in Michel via CSS) -->
            <div class="card config-widget tiltable" id="power-config-widget">
                <div class="card-header">
                    <span class="card-title" data-i18n="config_title">Configuration Overview</span>
                </div>
                <div class="config-grid" id="config-grid"></div>
            </div>

            <div class="card tiltable" id="power-stats-widget">
                <div class="card-header">
                    <span class="card-title" data-i18n="stats_title">Quick Stats</span>
                </div>
                <div class="config-grid" id="stats-grid"></div>
            </div>

            <!-- Michel compact client setup card -->
            <div class="card tiltable" id="michel-client-setup-card">
                <div class="card-header">
                    <span class="card-title" data-i18n="michel_client_title">Connecter un client</span>
                </div>
                <div id="michel-client-setup-content">
                    <p data-i18n="client_setup_loading">Chargement…</p>
                </div>
            </div>

            <!-- Michel changelog history card (always rendered at bottom of dashboard) -->
            <div class="card tiltable" id="michel-changelog-card">
                <div class="card-header">
                    <span class="card-title" data-i18n="changelog_title">Historique des versions</span>
                </div>
                <div id="michel-changelog-content">
                    <p data-i18n="changelog_loading">Chargement…</p>
                </div>
            </div>

            <p class="text-muted mt-4">
                <span data-i18n="current_version">Version actuelle</span>:
                <code id="current-version">{{.Version}}</code>
            </p>
        </section>

        <!-- Games (Power-only; Michel sees #section-power-required) -->
        <section data-route="games" id="section-games">
            <h1 data-i18n="games_title">Games</h1>
            <p class="text-muted" data-i18n="games_intro">Liste des jeux détectés. Cochez « Exposed » pour les inclure dans le meta.7z public.</p>
            <div class="card" style="margin-bottom: var(--space-6);">
                <button type="button" id="games-rescan-btn" class="btn btn-primary" data-i18n="rescan_btn">Rescan</button>
                <p id="games-rescan-status" class="text-muted" style="margin-top: var(--space-2); margin-bottom: 0; min-height: 1.25rem;"></p>
            </div>
            <div id="games-table"></div>
            <p id="games-empty" class="text-muted hidden" data-i18n="games_empty">Aucun jeu détecté.</p>
        </section>

        <!-- Configuration (both modes — Michel = read-only banner) -->
        <section data-route="configuration" id="section-configuration">
            <h1 data-i18n="settings_title">Paramètres</h1>
            <div id="settings-readonly-banner" class="card hidden" data-mode-only="michel">
                <p data-i18n="settings_readonly_michel">Les paramètres sont en lecture seule en mode Michel. Passez en mode Power User pour les modifier.</p>
            </div>
            <div id="settings-form">
                <p data-i18n="settings_loading">Chargement des paramètres…</p>
            </div>
        </section>

        <!-- Backup (Power-only) -->
        <section data-route="backup" id="section-backup">
            <h1 data-i18n="backup_title">Backup</h1>
            <p class="text-muted" data-i18n="backup_intro">Téléchargez une archive ZIP de votre configuration et de votre base de données, ou restaurez depuis une archive existante.</p>
            <div class="card">
                <button type="button" id="backup-download" class="btn btn-primary" data-i18n="backup_download">Download backup</button>
            </div>
            <div class="card">
                <h2 data-i18n="backup_restore_title">Restore</h2>
                <form id="backup-restore-form" enctype="multipart/form-data">
                    <input type="file" name="file" id="backup-restore-file" accept=".zip" required>
                    <button type="submit" class="btn btn-primary" data-i18n="backup_restore_submit">Restore</button>
                </form>
            </div>
        </section>

        <!-- API Docs (Power-only) -->
        <section data-route="api-docs" id="section-api-docs">
            <h1 data-i18n="docs_title">API Documentation</h1>
            <p class="text-muted" data-i18n="docs_intro">Liste de tous les endpoints de l'API admin de VRHub Server.</p>
            <div id="api-docs-catalog">
                <p data-i18n="docs_loading">Chargement…</p>
            </div>
        </section>

        <!-- Monitoring (Power-only, SSE) -->
        <section data-route="monitoring" id="section-monitoring">
            <h1 data-i18n="monitoring_title">Live Monitoring</h1>
            <p class="text-muted" data-i18n="monitoring_intro">Flux temps réel des événements du serveur.</p>
            <div id="monitoring-feed" class="card">
                <p data-i18n="monitoring_connecting">Connexion au flux…</p>
            </div>
        </section>

        <!-- Statistics (Power-only) -->
        <section data-route="stats" id="section-stats">
            <h1 data-i18n="stats_title">Usage Statistics</h1>
            <p class="text-muted" data-i18n="stats_intro">Nombre de téléchargements, bande passante et dernier téléchargement par jeu.</p>
            <div id="stats-table" class="card">
                <p data-i18n="stats_loading">Chargement…</p>
            </div>
        </section>

        <!-- Updates & Changelog (accessible in both Michel and Power modes) -->
        <section data-route="updates" id="section-updates">
            <h1 data-i18n="updates_title">Mises à jour &amp; Changelog</h1>
            <!-- Update notification card — shown by JS when update available or restart pending -->
            <div class="card update-card hidden" id="power-update-card">
                <div class="card-header">
                    <span class="card-title" id="power-update-card-title" data-i18n="update_available_title">Mise à jour disponible</span>
                    <span class="badge-pill badge-muted" id="power-installed-badge"></span>
                    <span class="badge-pill badge-update" id="power-latest-badge"></span>
                </div>
                <div class="update-card-changelog" id="power-update-notes"></div>
                <div class="update-card-actions" id="power-update-actions" style="margin-top: var(--space-3); display: flex; gap: var(--space-2); flex-wrap: wrap;"></div>
            </div>
            <!-- Changelog history -->
            <div class="card tiltable" id="power-changelog-card">
                <div class="card-header">
                    <span class="card-title" data-i18n="changelog_title">Historique des versions</span>
                </div>
                <div id="power-changelog-content">
                    <p data-i18n="changelog_loading">Chargement…</p>
                </div>
            </div>
        </section>

        <!-- M-04 (review 2026-06-11): dedicated "Client Setup" page.
             Accessible in BOTH Michel and Power modes (the only route
             besides Dashboard and Configuration with that property).
             Shows the operator everything they need to connect a
             VRHub client to this server: auto-config URL, baseUri,
             archive password, and step-by-step instructions. -->
        <section data-route="client-setup" id="section-client-setup">
            <h1 data-i18n="client_setup_title">Connecter un client VRHub</h1>
            <p class="text-muted" data-i18n="client_setup_intro">Voici les informations nécessaires pour connecter votre Meta Quest à ce serveur.</p>
            <div id="client-setup-content">
                <p data-i18n="client_setup_loading">Chargement…</p>
            </div>
        </section>

        <!-- Power-required placeholder (Michel mode, non-dashboard routes) -->
        <section data-route="power-required" id="section-power-required">
            <div class="card">
                <h2 data-i18n="power_required_title">Section réservée au mode Power User</h2>
                <p data-i18n="power_required_body">Cette section n'est accessible qu'en mode Power User. Cliquez sur le bouton ci-dessous pour basculer.</p>
                <button type="button" id="section-power-required-cta" class="btn btn-primary" data-i18n="header_switch_power">Passer en mode Power User</button>
            </div>
        </section>
    </main>

    <!-- P9: Cursor trail container (paints above .main-content background).
         Story X: trail is now visible in BOTH Michel and Power modes
         (Michel gets a reduced opacity to keep the effect subtle). -->
    <div class="cursor-trail-container" id="cursor-trail" aria-hidden="true"></div>

    <!-- Confirmation modal for update -->
    <div id="update-modal" class="modal" style="display: none;">
        <div class="modal-content">
            <h2>Updating...</h2>
            <p id="update-modal-message">Downloading and restarting... Do not close this window</p>
            <div class="progress-bar"><div class="progress-fill" id="update-progress"></div></div>
        </div>
    </div>

    <!-- Restart complete page -->
    <div id="restart-page" class="restart-page" style="display: none;">
        <div class="restart-content">
            <h2>Server Restarting</h2>
            <p>The server is restarting with the new version.</p>
            <p>This page will automatically refresh in <span id="countdown">10</span> seconds.</p>
            <button class="btn" onclick="location.reload()">Reload Now</button>
        </div>
    </div>

    <!-- Story X: Mode-switch segmented control. Fixed bottom-left,
         always visible in both modes. Clicking a segment calls
         setMode(...) and the aria-pressed state updates. -->
    <div id="mode-switch" class="mode-switch" role="group" aria-label="Interface mode">
        <button type="button" id="mode-switch-michel" class="mode-switch-seg" aria-pressed="true" data-i18n="mode_label_michel">Michel</button>
        <button type="button" id="mode-switch-power" class="mode-switch-seg" aria-pressed="false" data-i18n="mode_label_power">Power</button>
    </div>

    <script src="/admin/static/admin.js"></script>
</body>
</html>`

// AdminHTML returns the admin HTML template with version injected.
func AdminHTML(version string) []byte {
	return []byte(strings.ReplaceAll(adminHTMLTemplate, "{{.Version}}", html.EscapeString(version)))
}

// loginHTMLTemplate is a self-contained login page rendered by LoginHTML.
//
// Story 9.5 (B5): the admin shell is a complete dashboard (header, sidebar,
// widgets) and the login form was historically embedded as a hidden div
// revealed by JS when the URL contained ?showLogin=1. This caused the user
// to see a broken dashboard behind the login form after the setup wizard
// finished. The fix: a dedicated login page that contains ONLY the login
// card, with no shell, no sidebar, no header, no widgets. The /admin/
// route is now protected by the session middleware, so authenticated
// users get the shell and unauthenticated users land on this page.
//
// The template mirrors the IDs of the previous shell-embedded form
// (#login-form, #login-username, #login-password, #login-submit,
// #login-error) so login.js can reuse the same handlers without code
// duplication. The login.js file is loaded via <script src> and provides
// the form-submit glue.
const loginHTMLTemplate = `<!DOCTYPE html>
<!-- Story 9.5 (B5): dedicated login page served on /admin/login.
     No shell, no sidebar, no widgets — only the login card.
     Authenticated users are routed to /admin/ (the protected shell)
     by the session middleware. -->
<html lang="fr">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>VRHub Server - Login</title>
    <link rel="stylesheet" href="/admin/static/admin.css">
</head>
<body class="login-page">
    <div class="login-container">
        <div class="card login-card">
            <h2 data-i18n="login_title">Login</h2>
            <form id="login-form" action="/admin/api/auth/login" method="post">
                <div class="form-group">
                    <label for="login-username" data-i18n="login_username">Username</label>
                    <input type="text" id="login-username" name="username" required maxlength="256" autocomplete="username">
                </div>
                <div class="form-group">
                    <label for="login-password" data-i18n="login_password">Password</label>
                    <input type="password" id="login-password" name="password" required maxlength="72" autocomplete="current-password">
                </div>
                <button type="submit" id="login-submit" class="btn btn-primary" data-i18n="login_submit">Sign in</button>
            </form>
            <!-- R7-LOGIN-ERROR-NO-ARIA: aria-live + role=alert for screen readers (WCAG 2.1 SC 4.1.3) -->
            <div id="login-error" class="notification notification-error hidden" role="alert" aria-live="assertive"></div>
        </div>
        <p class="text-muted login-version"><span data-i18n="current_version">Version</span>: <code>{{.Version}}</code></p>
    </div>
    <noscript>
        <p class="noscript-warning">JavaScript is required to sign in. Please enable JavaScript and reload this page.</p>
    </noscript>
    <script src="/admin/static/login.js"></script>
</body>
</html>`

// LoginHTML returns the dedicated login page HTML with the version
// injected. Used by the /admin/login route (Story 9.5 B5) to serve a
// clean login page (no shell, no widgets) instead of the previous
// shell-embedded form. The page is reachable WITHOUT a session
// (otherwise the session middleware would redirect to itself and
// cause an infinite loop, see router.go R13-P3).
//
// Returns nil only if the template is empty (should never happen —
// the constant is non-empty at compile time).
func LoginHTML(version string) []byte {
	return []byte(strings.ReplaceAll(loginHTMLTemplate, "{{.Version}}", html.EscapeString(version)))
}

// adminDocsRenderer is the function that produces the API docs HTML.
// It's set at process startup by internal/api via SetAdminDocsRenderer
// (Story 6.6 Task 3.2). The indirection avoids an import cycle:
// internal/api already imports internal/ui for AdminHTML, so the
// reverse direction (ui → api) would be a cycle.
var adminDocsRenderer func() []byte

// AdminDocsHTML returns the browsable API documentation page as HTML.
//
// Story 6.6 Task 3.2: mirrors the `AdminHTML()`/`AdminCSS()`/`AdminJS()`
// "embed returns bytes" idiom so the router can serve the docs page
// via `ui.AdminDocsHTML()`. The actual rendering (catalog data
// structure + HTML builder) lives in `internal/api/api_docs.go` to
// keep the catalog in one place (single source of truth, see
// `TestEndpointCatalog_AllRoutersReachable`).
//
// Returns nil if the renderer was never registered. In production
// `internal/api` registers the renderer in an `init()` block at
// process startup, so a nil return indicates a test setup gap or a
// package wiring bug.
func AdminDocsHTML() []byte {
	if adminDocsRenderer == nil {
		return nil
	}
	return adminDocsRenderer()
}

// SetAdminDocsRenderer registers the function that produces the docs
// HTML. Called once at process startup (internal/api's `init()`
// block). Not safe for concurrent calls — register exactly once.
func SetAdminDocsRenderer(fn func() []byte) {
	adminDocsRenderer = fn
}

// AdminCSS returns the embedded admin CSS content.
func AdminCSS() []byte {
	data, _ := StaticFS.ReadFile("embed/admin.css")
	return data
}

// AdminJS returns the embedded admin JS content.
func AdminJS() []byte {
	data, _ := StaticFS.ReadFile("embed/admin.js")
	return data
}

// LoginJS returns the embedded login JS content. Story 9.5 (B5):
// the login form submit handler was extracted from admin.js so the
// dedicated /admin/login page can load it as a standalone script
// (admin.js is sized for the full shell and would waste bandwidth
// for the login page). The shell no longer needs the login form
// (it has its own dedicated page), so admin.js no longer embeds
// the handler.
func LoginJS() []byte {
	data, _ := StaticFS.ReadFile("embed/login.js")
	return data
}

// adminStatsRenderer produces the HTML for /admin/stats (Story 7.5).
// Set at process startup by internal/api via SetAdminStatsRenderer
// (mirrors the docs renderer pattern from Story 6.6, see
// SetAdminDocsRenderer above). The indirection avoids an import
// cycle: internal/api already imports internal/ui for AdminHTML,
// so ui → api would be a cycle.
//
// The renderer returns a self-contained HTML page (admin shell
// chrome + a <div id="stats-table"> placeholder that admin.js fills
// in after fetching /admin/api/stats). Mode-gating is enforced by
// the handler that calls AdminStatsHTML — Michel mode never reaches
// the renderer because the handler returns 404 first.
var adminStatsRenderer func() []byte

// AdminStatsHTML returns the /admin/stats page as HTML bytes.
// Returns nil if no renderer was registered. The handler must
// treat nil as "renderer missing" and 404 (defense-in-depth for
// test setups that import internal/ui without internal/api).
func AdminStatsHTML() []byte {
	if adminStatsRenderer == nil {
		return nil
	}
	return adminStatsRenderer()
}

// SetAdminStatsRenderer wires the function that produces the stats
// page HTML. Called once at process startup (internal/api's
// RegisterStatsHTMLRenderer). Not safe for concurrent calls —
// register exactly once.
func SetAdminStatsRenderer(fn func() []byte) {
	adminStatsRenderer = fn
}

// adminSetupRenderer produces the HTML for /admin/setup (Story 1.6).
// Set at process startup by internal/api via SetAdminSetupRenderer
// (mirrors the docs/stats renderer pattern from Story 6.6/7.5, see
// SetAdminDocsRenderer above). The indirection avoids an import
// cycle: internal/api already imports internal/ui for AdminHTML,
// so ui → api would be a cycle.
//
// The renderer returns a self-contained HTML page (wizard overlay
// with 4 step sections, stepper dots, embedded form scaffolding).
// The wizard reuses the admin design tokens (colors, spacing,
// typography) but is theme-neutral (no mode-michel/mode-power body
// class). Mode-gating is enforced by the handler that calls
// AdminSetupHTML — in normal mode, the handler redirects to /
// without invoking the renderer.
var adminSetupRenderer func() []byte

// AdminSetupHTML returns the /admin/setup wizard page as HTML bytes.
// Returns nil if no renderer was registered. The handler must
// treat nil as "renderer missing" and 404 (defense-in-depth for
// test setups that import internal/ui without internal/api).
func AdminSetupHTML() []byte {
	if adminSetupRenderer == nil {
		return nil
	}
	return adminSetupRenderer()
}

// SetAdminSetupRenderer wires the function that produces the setup
// wizard HTML. Called once at process startup (internal/api's
// RegisterSetupHTMLRenderer). Not safe for concurrent calls —
// register exactly once.
func SetAdminSetupRenderer(fn func() []byte) {
	adminSetupRenderer = fn
}

// SetupCSS returns the embedded setup wizard CSS content.
// Story 1.6: separate file from admin.css so the wizard CSS does
// not bloat the admin shell asset. The wizard page references
// BOTH /admin/static/admin.css (for design tokens, .btn, .form-input,
// .hidden) AND /admin/static/setup.css (for .wizard-* classes).
func SetupCSS() []byte {
	data, _ := StaticFS.ReadFile("embed/setup.css")
	return data
}

// SetupJS returns the embedded setup wizard JS content.
// Story 1.6: separate file from admin.js so the wizard JS does not
// bloat the admin shell asset. The wizard page references BOTH
// /admin/static/admin.js (for design tokens, .btn, etc., and the
// __VRHUB_I18N__ global exposed by admin.js) AND
// /admin/static/setup.js (for the 4-step controller).
func SetupJS() []byte {
	data, _ := StaticFS.ReadFile("embed/setup.js")
	return data
}
