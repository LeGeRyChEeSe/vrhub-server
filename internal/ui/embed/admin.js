// VRHub Server Admin UI JavaScript

// Mode detection and persistence (Subtask 1.x)
var ADMIN_MODE_KEY = 'vrhub:admin-mode';
// Story X: language preference (independent of mode). Persisted in
// localStorage so it survives reloads. The mode change handler no
// longer touches this key — only the dedicated lang selector does.
var ADMIN_LANG_KEY = 'vrhub:lang';

function getMode() {
    var stored;
    try { stored = localStorage.getItem(ADMIN_MODE_KEY); } catch(e) {}
    // M5: Validate stored value; reject garbage/"undefined" strings
    if (stored === 'power' || stored === 'michel') return stored;
    // R6-L12: Clean up corrupt localStorage value (was deferred in R5-P20)
    try { localStorage.setItem(ADMIN_MODE_KEY, 'michel'); } catch(e) {}
    return 'michel';
}

// Story X: getLang reads the language preference. Defaults to
// 'fr' (Michel mode default) or 'en' (Power mode default) when
// no value is stored. Returns 'fr' or 'en' (never anything else).
function getLang() {
    var stored;
    try { stored = localStorage.getItem(ADMIN_LANG_KEY); } catch(e) {}
    if (stored === 'fr' || stored === 'en') return stored;
    // Default follows the current mode (Michel→fr, Power→en).
    return getMode() === 'power' ? 'en' : 'fr';
}

// Story X: setLang writes the language preference, updates
// html[lang], and triggers a page re-translation. The lang
// selector in the header is the only place that calls this.
function setLang(lang) {
    if (lang !== 'fr' && lang !== 'en') lang = 'fr';
    try { localStorage.setItem(ADMIN_LANG_KEY, lang); } catch(e) {}
    document.documentElement.lang = lang;
    // Re-translate the page so the new language takes effect.
    translatePage(lang === 'fr' ? 'michel' : 'power');
    // Notify header bits (status dot text, etc.) — re-apply the
    // status text under the new locale.
    if (typeof updateStatusText === 'function') {
        try { updateStatusText(); } catch(e) {}
    }
}

// M5/R6-B2: Cross-tab sync — listen for storage events from other tabs
window.addEventListener('storage', function(e) {
    if (e.key !== ADMIN_MODE_KEY) return;
    // R6-B2: Use e.newValue directly so setMode's prev === newMode dedupe does not short-circuit
    if (e.newValue === null) {
        // R6-B2: Key cleared in another tab → fall back to default
        // R10-CROSS-TAB-SYNC: pass force:true so the receiving tab gets a
        // full DOM update even if localStorage already has the target value
        // (the previous no-force call short-circuited the body-class update
        // when a tab reset its localStorage to 'michel' from 'power' and
        // another tab was still on 'michel' — body class never refreshed).
        setMode('michel', { force: true });
        return;
    }
    var newMode = (e.newValue === 'power' || e.newValue === 'michel') ? e.newValue : 'michel';
    // R7-B1 (and R10-CROSS-TAB-SYNC reinforcement): pass force:true to bypass
    // setMode's prev===mode dedupe — receiving tab needs full DOM update.
    setMode(newMode, { force: true });
});

function setMode(mode, opts) {
    opts = opts || {};
    // R6-B1/R7-M5: Validate mode. In debug builds, console.error on programmer error; otherwise silently coerce to 'michel'.
    if (mode !== 'power' && mode !== 'michel') {
        if (window.__VRHUB_DEBUG__) console.error('setMode: invalid mode', mode, '— falling back to michel');
        mode = 'michel';
    }
    var prev = getMode();
    // R6-M7: On bootstrap (force) or first-ever call, skip dedupe so DOM is fully primed
    var isBootstrap = opts.force || prev === null;
    if (!isBootstrap && prev === mode) return;
    var wroteOk = true;
    // R10-CATCH-OUT-OF-SCOPE: capture the catch variable so it doesn't fall
    // out of scope before the console.warn call below. ES2019+ allows this
    // in most engines, but some embedded JS engines and strict-mode parsers
    // have rules about catch-scope visibility; capturing the value into a
    // function-scope variable avoids the subtle ReferenceError surface.
    var writeErr = null;
    try { localStorage.setItem(ADMIN_MODE_KEY, mode); } catch(e) { wroteOk = false; writeErr = e; }
    if (!wroteOk && window.__VRHUB_DEBUG__) console.warn('setMode: localStorage write failed', writeErr);
    // B-01 (review 2026-06-11): also persist the mode in a cookie
    // readable by the Go API. The cookie is the source of truth for
    // `monitoring.go:isPowerMode` and `stats.go:isPowerMode` (the
    // api_docs handler additionally accepts an `X-Power-Mode: 1`
    // header which we set on every Power-mode fetch via the
    // `powerFetch` helper below). Without this cookie, the
    // /admin/monitoring, /admin/api/stats and /admin/api/docs
    // endpoints all return 404 unconditionally in Power mode.
    try { document.cookie = 'vrhub-mode=' + mode + '; path=/; max-age=31536000; SameSite=Lax'; } catch(e) { /* no-op */ }
    document.body.classList.remove('mode-michel', 'mode-power');
    document.body.classList.add(mode === 'power' ? 'mode-power' : 'mode-michel');
    // B4: Update html lang attribute for accessibility
    document.documentElement.lang = mode === 'power' ? 'en' : 'fr';
    // R6-M3: applyModeVisibility deleted — CSS rules handle visibility (.sidebar, #michel-*, #power-*, #cursor-trail)
    // B3: Load widgets for the new mode (prevents empty config-grid/stats-grid on toggle)
    if (mode === 'michel') {
        loadMichelWidgets();
    } else {
        loadPowerUserWidgets();
    }
    // Emit modechange event for other components to react
    var event = new CustomEvent('modechange', { detail: { from: prev, to: mode } });
    document.dispatchEvent(event);
}

// B-01 (review 2026-06-11): powerFetch is a thin wrapper around
// fetch() that adds the X-Power-Mode: 1 header used by
// `api_docs.go:clientOptsIntoPowerMode` (in addition to the cookie
// set by setMode). Defense in depth: if the cookie fails to set
// (e.g. third-party iframe, browser quirk), the header still gets
// the operator into Power mode for api_docs at least.
function powerFetch(url, opts) {
    opts = opts || {};
    opts.headers = Object.assign({}, opts.headers || {}, { 'X-Power-Mode': '1' });
    if (opts.credentials === undefined) opts.credentials = 'same-origin';
    return fetch(url, opts);
}

// R6-M3: applyModeVisibility deleted — CSS rules (body.mode-michel .sidebar, body.mode-power #michel-*, etc.) handle visibility

// Michel mode widget loader (Task 3)
function loadMichelWidgets() {
    // Server status widget
    fetchServerStatus();

    // Config widget — baseUri and password from /admin/api/admin/settings
    // (Story 9.6, B6 fix: the previous /admin/api/config path was a 404
    // silently swallowed by .catch; the real endpoint has existed since
    // Story 6.3 and now also returns the plaintext password after a
    // successful login so the operator can copy it into the VRHub client).
    fetchConfig();

    // Game count widget
    fetchGameCount();

    // Compact client setup card
    loadMichelClientSetupCard();

    // Password toggle — bind once per element (idempotent on hot reload).
    // The handler is wired here (Michel-only widget) so the dashboard's
    // "Afficher"/"Masquer" link actually does something. Story 9.6 / B6.3.
    var togglePassword = document.getElementById('toggle-password');
    if (togglePassword && togglePassword.dataset.bound !== '1') {
        togglePassword.dataset.bound = '1';
        // Initial aria-pressed (the password is masked on render).
        togglePassword.setAttribute('aria-pressed', 'false');
        togglePassword.addEventListener('click', function(e) {
            e.preventDefault();
            togglePasswordVisibility();
        });
    }

    // Story 1.8 follow-up (live session 2026-06-08): Launch/Stop
    // buttons were removed from the HTML (their click handler was
    // a non-functional placeholder that displayed a "not yet
    // implemented" toast). The actual start/stop REST endpoints
    // were never implemented — server control is via the process
    // manager (systemd/Docker/foreground). The event listener
    // wiring is removed too so we don't leave dangling references;
    // a future story can reintroduce both the buttons and the
    // endpoints together.
}

function fetchServerStatus() {
    var statusDot = document.getElementById('status-dot');
    var statusText = document.getElementById('status-text');
    var launchBtn = document.getElementById('launch-btn');
    var stopBtn = document.getElementById('stop-btn');
    if (!statusDot || !statusText) return;

    fetch('/admin/api/update/status', {
        headers: { 'Accept': 'application/json' },
        credentials: 'same-origin'
    })
        .then(function(r) { if (!r.ok) throw new Error(r.status); return r.json(); })
        .then(function(data) {
            // If we can reach the API, server is running
            statusDot.className = 'status-dot active';
            statusText.textContent = i18n('server_status_running', 'Serveur en marche');
            if (launchBtn) launchBtn.style.display = 'none';
            if (stopBtn) stopBtn.style.display = 'inline-flex';
        })
        .catch(function() {
            // Server unreachable — likely stopped or restarting
            statusDot.className = 'status-dot';
            statusText.textContent = i18n('server_status_stopped', 'Arrêté');
            if (launchBtn) launchBtn.style.display = 'inline-flex';
            if (stopBtn) stopBtn.style.display = 'none';
        });
}

// fetchConfig populates the Michel-mode "Configuration" widget with
// the server's baseUri and the admin password plaintext (for the
// "Afficher"/"Masquer" toggle). Story 9.6 / B6: this used to hit the
// non-existent /admin/api/config (404 swallowed silently by .catch).
// The real endpoint is /admin/api/admin/settings (Story 6.3), and as
// of this story it returns the plaintext password in the JSON
// response (audit-logged server-side at Warn level; see
// internal/api/admin_settings.go).
//
// The baseUri is read from `data.data.base_uri` when the server
// supplies it, with a fallback that builds it from
// `data.data.server.host` + `data.data.server.port`. This way the
// widget works against the pre-9.6 SanitizeConfig shape (no
// base_uri) as well as the new shape.
function fetchConfig() {
    var baseUriEl = document.getElementById('config-baseuri');
    if (!baseUriEl) return;

    fetch('/admin/api/admin/settings', {
        headers: { 'Accept': 'application/json' },
        credentials: 'same-origin'
    })
        .then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.json();
        })
        .then(function(data) {
            if (!data || !data.data) return;
            var d = data.data;

            // baseUri: prefer the precomputed field (Story 9.6),
            // fall back to host:port built from server.{host,port}.
            var baseUri = d.base_uri;
            if (!baseUri && d.server && d.server.host && d.server.port) {
                baseUri = 'http://' + d.server.host + ':' + d.server.port + '/';
            }
            // textContent (not innerHTML) — XSS safety in case a
            // future operator runs the server with a hostile
            // server.host.
            baseUriEl.textContent = baseUri || '—';

            // Password plaintext: populate #password-visible so the
            // "Afficher" toggle has something to reveal. We show the
            // archive password (for VRHub client config), not the
            // admin login password.
            var pwdVisible = document.getElementById('password-visible');
            if (pwdVisible && d.archive_password) {
                pwdVisible.textContent = d.archive_password;
            }

            // Also populate the Michel "Connecter un client" card with
            // the same data — avoids a second fetch.
            _renderClientSetupCard(d);
        })
        .catch(function(err) {
            if (window.__VRHUB_DEBUG__) {
                console.warn('fetchConfig: failed to load /admin/api/admin/settings', err);
            }
            _renderClientSetupCardError();
        });
}

// R5-P7: AbortController for fetch race on rapid Michel→Power→Michel toggles
var currentGameCountController = null;
function fetchGameCount() {
    var badge = document.getElementById('game-count-badge');
    var lastScanText = document.getElementById('last-scan-text');
    if (!badge) return;

    // R5-P7: Cancel any in-flight fetch before starting a new one
    if (currentGameCountController) currentGameCountController.abort();
    currentGameCountController = new AbortController();

    fetch('/admin/api/games', {
        signal: currentGameCountController.signal,
        headers: { 'Accept': 'application/json' },
        credentials: 'same-origin'
    })
        .then(function(r) { if (!r.ok) throw new Error(r.status); return r.json(); })
        .then(function(data) {
            var count = 0;
            if (data.data && Array.isArray(data.data)) {
                count = data.data.length;
            } else if (typeof data.data === 'object' && data.data !== null) {
                count = data.data.count ?? data.data.games?.length ?? 0;
            }
            // P1: Use mode-aware formatting instead of hardcoded French locale
            badge.textContent = formatNumber(count, getMode());
            var label = i18n('game_count_label', 'Jeux détectés');
            badge.title = count + ' ' + label;
            // R6-H4: last_scan not exposed by /admin/api/games — leave the static "Scanné à l'instant" placeholder
            // (clear only on explicit error, not on successful but empty data)
        })
        .catch(function(err) {
            // R5-P7: AbortError is expected when canceling in-flight fetch; ignore silently
            if (err && err.name === 'AbortError') return;
            // DB not ready yet — leave placeholder visible
        });
}

// togglePasswordVisibility flips the Michel dashboard's password
// row between the masked (8 bullets) and the visible (plaintext)
// representations. Story 9.6 / B6.3: the click handler existed but
// was never wired (story replay confirmed the link was a no-op).
// The handler is now bound in loadMichelWidgets() (idempotent via
// the dataset.bound guard) and this function updates aria-pressed
// alongside textContent so screen readers report the state change.
function togglePasswordVisibility() {
    var masked = document.getElementById('password-masked');
    var visible = document.getElementById('password-visible');
    var toggle = document.getElementById('toggle-password');
    if (!masked || !visible || !toggle) return;

    if (visible.style.display === 'none') {
        visible.style.display = 'inline';
        masked.style.display = 'none';
        toggle.textContent = i18n('hide_password', 'Masquer');
        // a11y: announce the state change to screen readers
        // (WCAG 2.1 SC 4.1.2 Name, Role, Value).
        toggle.setAttribute('aria-pressed', 'true');
    } else {
        visible.style.display = 'none';
        masked.style.display = 'inline';
        toggle.textContent = i18n('show_password', 'Afficher');
        toggle.setAttribute('aria-pressed', 'false');
    }
}

// Removed in Story 1.8 follow-up (live session 2026-06-08): the
// Launch/Stop buttons in the Michel dashboard were a non-functional
// placeholder whose click displayed a "not yet implemented" toast.
// The buttons have been removed from the HTML and the handler is
// gone too. A future story can reintroduce both the buttons and the
// actual start/stop REST endpoints together.

// Power User widget loader (Task 4)
function loadPowerUserWidgets() {
    fetchPowerConfig();
    fetchPowerStats();
}

// fetchPowerConfig populates the Power User "Configuration Overview"
// widget with read-only config values (full precision, English). Story
// 9.6 / B6.4: the previous /admin/api/config path was a 404
// (silently swallowed by .catch, so the widget stayed empty). The
// real endpoint is /admin/api/admin/settings (Story 6.3 + the JSON
// branch added in Story 9.6).
function fetchPowerConfig() {
    var grid = document.getElementById('config-grid');
    if (!grid) return;

    fetch('/admin/api/admin/settings', {
        headers: { 'Accept': 'application/json' },
        credentials: 'same-origin'
    })
        .then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.json();
        })
        .then(function(data) {
            if (!data.data) return;
            var config = data.data;
            var fields = [
                { key: 'base_uri', label: 'Base URI' },
                { key: 'server.port', label: 'Port' },
                { key: 'server.host', label: 'Listen Address' },
                { key: 'data_dir', label: 'Data Directory' },
                { key: 'update.enabled', label: 'Update Check Enabled' },
                { key: 'update.auto_apply', label: 'Auto Apply Updates' }
            ];

            grid.innerHTML = '';
            fields.forEach(function(f) {
                var parts = f.key.split('.');
                var val;
                // M4: Tighter null check to prevent TypeError on config.server=null
                if (parts.length === 2 && config[parts[0]] && typeof config[parts[0]] === 'object') {
                    val = config[parts[0]][parts[1]];
                } else {
                    val = config[f.key];
                }
                // M4: Allow 0 and false values (only skip undefined/null)
                if (val === undefined || val === null) return;
                var display = typeof val === 'boolean' ? (val ? 'Enabled' : 'Disabled') : String(val);
                var item = document.createElement('div');
                item.className = 'config-item';
                item.innerHTML = '<div class="config-label">' + f.label + '</div><div class="config-value">' + escapeHtml(display) + '</div>';
                grid.appendChild(item);
            });
        })
        .catch(function(err) {
            // Story 9.6: warn instead of silent (the previous code
            // swallowed 404s from /admin/api/config without any
            // operator feedback).
            if (window.__VRHUB_DEBUG__) {
                console.warn('fetchPowerConfig: failed to load /admin/api/admin/settings', err);
            }
        });
}

function fetchPowerStats() {
    var grid = document.getElementById('stats-grid');
    if (!grid) return;

    // Fetch game count for Power User (full precision)
    fetch('/admin/api/games', {
        headers: { 'Accept': 'application/json' },
        credentials: 'same-origin'
    })
        .then(function(r) { if (!r.ok) throw new Error(r.status); return r.json(); })
        .then(function(data) {
            var count = 0;
            if (data.data && Array.isArray(data.data)) {
                count = data.data.length;
            } else if (typeof data.data === 'object' && data.data !== null) {
                count = data.data.count ?? data.data.games?.length ?? 0;
            }

            var stats = [
                // R6-M1: Use mode-aware formatNumber so Power User sees English full-precision.
                // M-07 (review 2026-06-11): removed the 3 placeholder
                // stats (Exposed / Corrupted / Orphan Files) that were
                // hardcoded to '—'. Wiring them up requires new
                // endpoints; keep the Total Games card only.
                { label: 'Total Games', value: formatNumber(count, 'power') }
            ];

            grid.innerHTML = '';
            stats.forEach(function(s) {
                var item = document.createElement('div');
                item.className = 'config-item';
                item.innerHTML = '<div class="config-label">' + s.label + '</div><div class="config-value">' + escapeHtml(s.value) + '</div>';
                grid.appendChild(item);
            });
        })
        .catch(function() { /* DB not ready */ });
}

function escapeHtml(str) {
    var div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

// Story 7.5 T3: render the per-game stats table on /admin/stats.
// The page is a thin shell (see internal/api/stats.go); the JS does
// the fetch + render. We use textContent (not innerHTML) for every
// dynamic value to avoid XSS via game_name (an APK scan can yield
// arbitrary strings).
function renderStatsTable() {
    var container = document.getElementById('stats-table');
    if (!container) return; // not on /admin/stats

    var mode = getMode();
    powerFetch('/admin/api/stats')
        .then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.json();
        })
        .then(function(payload) {
            var stats = (payload && payload.data && Array.isArray(payload.data.stats)) ? payload.data.stats : [];
            if (stats.length === 0) {
                container.textContent = i18n('stats_no_data');
                return;
            }
            // Build a <table> via DOM APIs (no innerHTML for dynamic cells).
            var table = document.createElement('table');
            table.className = 'stats-table';

            var thead = document.createElement('thead');
            var headRow = document.createElement('tr');
            ['stats_col_game', 'stats_col_package', 'stats_col_count', 'stats_col_last', 'stats_col_bandwidth', 'stats_col_size'].forEach(function(k) {
                var th = document.createElement('th');
                th.textContent = i18n(k);
                headRow.appendChild(th);
            });
            thead.appendChild(headRow);
            table.appendChild(thead);

            var tbody = document.createElement('tbody');
            stats.forEach(function(s) {
                var tr = document.createElement('tr');

                var tdName = document.createElement('td');
                tdName.textContent = s.game_name || '';
                tr.appendChild(tdName);

                var tdPkg = document.createElement('td');
                tdPkg.textContent = s.package_name || '';
                tr.appendChild(tdPkg);

                var tdCount = document.createElement('td');
                tdCount.textContent = String(s.download_count || 0);
                tr.appendChild(tdCount);

                var tdLast = document.createElement('td');
                if (s.last_download_at && s.last_download_at > 0) {
                    var d = new Date(s.last_download_at * 1000);
                    tdLast.textContent = mode === 'power' ? d.toISOString() : formatRelativeTime(d.toISOString(), mode);
                } else {
                    tdLast.textContent = i18n('stats_never');
                }
                tr.appendChild(tdLast);

                var tdBw = document.createElement('td');
                tdBw.textContent = formatBytes(s.total_bandwidth_bytes || 0, mode);
                tr.appendChild(tdBw);

                var tdSize = document.createElement('td');
                tdSize.textContent = formatBytes(s.game_file_size || 0, mode);
                tr.appendChild(tdSize);

                tbody.appendChild(tr);
            });
            table.appendChild(tbody);

            container.textContent = '';
            container.appendChild(table);
        })
        .catch(function() {
            container.textContent = i18n('stats_failed');
        });
}

// triggerRescan calls POST /admin/api/games/rescan and updates a status element.
// It is wired to both the Michel dashboard and the Power User games page.
function triggerRescan(btn, statusEl) {
    if (!btn || !statusEl) return;
    if (btn.disabled) return;

    btn.disabled = true;
    statusEl.textContent = i18n('rescan_in_progress', 'Scan en cours…');
    statusEl.className = 'text-muted';

    fetch('/admin/api/games/rescan', {
        method: 'POST',
        credentials: 'same-origin',
        headers: {
            'Accept': 'application/json',
            'Content-Type': 'application/json',
            'X-CSRF-Token': getCSRFToken()
        }
    })
        .then(function(r) {
            if (!r.ok) {
                return r.text().then(function(body) {
                    throw new Error('HTTP ' + r.status + ' — ' + body);
                });
            }
            return r.json();
        })
        .then(function(data) {
            var summary = data && data.data;
            var added = summary ? (summary.games_added || 0) : 0;
            var scanned = summary ? (summary.files_scanned || 0) : 0;
            statusEl.textContent = i18n('rescan_done', 'Scan terminé') + ' — ' +
                i18n('rescan_files_scanned', '{{n}} fichiers scannés').replace('{{n}}', scanned) + ', ' +
                i18n('rescan_games_added', '{{n}} nouveaux jeux').replace('{{n}}', added);
            statusEl.className = 'text-success';
            // Refresh game count on Michel dashboard and games table on Power page.
            fetchGameCount();
            if (document.getElementById('games-table')) renderGamesTable();
        })
        .catch(function(err) {
            statusEl.textContent = i18n('rescan_failed', 'Échec du scan') + ': ' + err.message;
            statusEl.className = 'text-danger';
        })
        .finally(function() {
            btn.disabled = false;
        });
}

// wireRescanButtons binds the two rescan buttons (Michel dashboard + Games page).
function wireRescanButtons() {
    var dashBtn = document.getElementById('dashboard-rescan-btn');
    var dashStatus = document.getElementById('dashboard-rescan-status');
    if (dashBtn && !dashBtn.dataset.bound) {
        dashBtn.addEventListener('click', function() { triggerRescan(dashBtn, dashStatus); });
        dashBtn.dataset.bound = '1';
    }

    var gamesBtn = document.getElementById('games-rescan-btn');
    var gamesStatus = document.getElementById('games-rescan-status');
    if (gamesBtn && !gamesBtn.dataset.bound) {
        gamesBtn.addEventListener('click', function() { triggerRescan(gamesBtn, gamesStatus); });
        gamesBtn.dataset.bound = '1';
    }
}

// Story 1.8 T2: render the games table on /admin/games.
function renderGamesTable() {
    var container = document.getElementById('games-table');
    var empty = document.getElementById('games-empty');
    if (!container) return;
    container.textContent = '';
    // M-13 (review 2026-06-11): the rows below use `mode` for
    // formatBytes / formatRelativeTime / dt.toISOString. It MUST be
    // declared in this function's scope — the previous patch left a
    // ReferenceError ("mode is not defined") that fell into the
    // .catch and showed "Failed to load games." for every visit.
    var mode = getMode();
    fetch('/admin/api/games', {
        credentials: 'same-origin',
        headers: { 'Accept': 'application/json' }
    })
        .then(function(r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
        .then(function(data) {
            var games = (data && data.data && data.data.games) || [];
            if (games.length === 0) {
                if (empty) empty.classList.remove('hidden');
                return;
            }
            if (empty) empty.classList.add('hidden');
            if (games.length === 0) {
                if (empty) empty.classList.remove('hidden');
                return;
            }
            if (empty) empty.classList.add('hidden');
            var table = document.createElement('table');
            table.className = 'stats-table';
            var thead = document.createElement('thead');
            var trh = document.createElement('tr');
            [i18n('games_col_package', 'Package'), i18n('games_col_name', 'Name'), i18n('games_col_size', 'Size'), i18n('games_col_exposed', 'Exposed'), i18n('games_col_updated', 'Last updated')].forEach(function(h) {
                var th = document.createElement('th');
                th.textContent = h;
                trh.appendChild(th);
            });
            thead.appendChild(trh);
            table.appendChild(thead);
            var tbody = document.createElement('tbody');
            games.forEach(function(g, idx) {
                var tr = document.createElement('tr');
                var tdPkg = document.createElement('td');
                tdPkg.textContent = g.package_name || g.release_name || '?';
                tr.appendChild(tdPkg);
                var tdName = document.createElement('td');
                tdName.textContent = g.game_name || '(no name)';
                tr.appendChild(tdName);
                var tdSize = document.createElement('td');
                var sz = (g.size_bytes || 0) + (g.obb_size_bytes || 0);
                // M-13 (review 2026-06-11): use formatBytes (i18n-aware)
                // instead of hardcoded "MB". Also pass the row index to
                // build a unique checkbox id for the label association.
                tdSize.textContent = formatBytes(sz, mode);
                tr.appendChild(tdSize);
                var rowIdx = idx;
                var tdExp = document.createElement('td');
                var lbl = document.createElement('label');
                // M-11 (review 2026-06-11): give the checkbox a stable
                // id and associate the label via htmlFor. Also add a
                // visually-hidden label text so screen readers
                // announce the column meaning.
                var cbId = 'g-exposed-' + rowIdx;
                var cb = document.createElement('input');
                cb.type = 'checkbox';
                cb.id = cbId;
                cb.checked = !!g.exposed;
                lbl.htmlFor = cbId;
                var lblSpan = document.createElement('span');
                lblSpan.className = 'visually-hidden';
                lblSpan.textContent = i18n('games_col_exposed');
                lbl.appendChild(cb);
                lbl.appendChild(lblSpan);
                cb.addEventListener('change', function() {
                    fetch('/admin/api/games/' + encodeURIComponent(g.package_name || g.release_name) + '/exposed', {
                        method: 'PATCH',
                        credentials: 'same-origin',
                        headers: {
                            'Accept': 'application/json',
                            'Content-Type': 'application/json',
                            'X-CSRF-Token': getCSRFToken()
                        },
                        body: JSON.stringify({ exposed: cb.checked })
                    });
                });
                tdExp.appendChild(lbl);
                tr.appendChild(tdExp);
                var tdUpd = document.createElement('td');
                // M-13 (review 2026-06-11): use formatRelativeTime /
                // toISOString depending on mode (consistent with stats
                // page). g.last_updated is an ISO datetime string from
                // the server.
                if (g.last_updated) {
                    var dt = new Date(g.last_updated);
                    if (!isNaN(dt.getTime())) {
                        tdUpd.textContent = mode === 'power' ? dt.toISOString() : formatRelativeTime(dt.toISOString(), mode);
                    } else {
                        tdUpd.textContent = g.last_updated;
                    }
                } else {
                    tdUpd.textContent = '?';
                }
                tr.appendChild(tdUpd);
                tbody.appendChild(tr);
            });
            table.appendChild(tbody);
            container.appendChild(table);
        })
        .catch(function(err) {
            // Debug (review 2026-06-11): surface the actual failure
            // instead of swallowing it. "Failed to load games." is
            // not actionable — the operator needs to know WHY.
            if (window.__VRHUB_DEBUG__) console.warn('renderGamesTable failed:', err && err.message);
            container.textContent = i18n('games_load_failed', 'Failed to load games.')
                + (err && err.message ? ' (' + err.message + ')' : '');
        });
}

// Story 1.8 T3: wire the backup page buttons.
// - "Download backup" -> GET /admin/api/scripts/backup (the API serves
//   the zip directly with Content-Disposition: attachment, so a plain
//   window.location triggers a browser download).
// - "Restore" file input -> POST /admin/api/scripts/restore as
//   multipart/form-data.
function initBackupPage() {
    var dl = document.getElementById('backup-download');
    if (dl) {
        dl.addEventListener('click', function(e) {
            e.preventDefault();
            window.location.assign('/admin/api/scripts/backup');
        });
    }
    var form = document.getElementById('backup-restore-form');
    if (form) {
        form.addEventListener('submit', function(e) {
            e.preventDefault();
            var fd = new FormData(form);
            fetch('/admin/api/scripts/restore', {
                method: 'POST',
                credentials: 'same-origin',
                body: fd
            }).then(function(r) {
                if (r.ok) {
                    alert('Restore submitted — restart the server to apply.');
                } else {
                    alert('Restore failed: HTTP ' + r.status);
                }
            });
        });
    }
}

// State
let updateStatus = {
    available: false,
    currentVersion: '',
    latestVersion: '',
    autoApply: false
};

// ============================================================
// Story X: SPA router (hash-based)
// ============================================================
//
// routeFromHash reads window.location.hash (e.g. "#/games") and:
//   1. Computes the route name (e.g. "games").
//   2. In Michel mode, Power-only routes are forced to "power-required".
//   3. Sets body[data-route] (CSS shows the matching section).
//   4. Updates .active on the matching #section-nav link.
//   5. Closes any open monitoring feed and re-opens if route is
//      "monitoring".
//   6. Fetches data for the route (games, stats, docs).
//
// The function is idempotent and is called once on DOMContentLoaded
// and on every hashchange event.
var currentRoute = null;
var monitoringSource = null;

var ROUTE_TO_HANDLER = {
    'dashboard':       handleRouteDashboard,
    'games':           handleRouteGames,
    'configuration':   handleRouteConfiguration,
    'backup':          handleRouteBackup,
    'api-docs':        handleRouteApiDocs,
    'monitoring':      handleRouteMonitoring,
    'stats':           handleRouteStats,
    'client-setup':    handleRouteClientSetup,
    'updates':         handleRouteUpdates,
    'power-required':  handleRoutePowerRequired
};

function routeFromHash(opts) {
    opts = opts || {};
    var hash = (location.hash || '').replace(/^#\//, '').trim();
    var route = hash || 'dashboard';

    // Michel mode: gate Power-only routes to the placeholder.
    if (getMode() === 'michel' && (route === 'games' || route === 'backup' ||
        route === 'api-docs' || route === 'monitoring' || route === 'stats')) {
        // M-08 (review 2026-06-11): remember the original route so
        // the CTA in #section-power-required can navigate the user
        // there after they switch to Power mode.
        try { sessionStorage.setItem('vrhub:pending-route', route); } catch(e) { /* no-op */ }
        route = 'power-required';
        if (!opts.skipHashUpdate) {
            history.replaceState({}, '', location.pathname + location.search + '#/power-required');
        }
    } else {
        // Clear the pending route on a successful navigation.
        try { sessionStorage.removeItem('vrhub:pending-route'); } catch(e) { /* no-op */ }
    }

    if (route === currentRoute && !opts.force) {
        return; // same route, no work
    }
    var prevRoute = currentRoute;
    currentRoute = route;

    // Close any open SSE feed when leaving /monitoring.
    if (prevRoute === 'monitoring' && route !== 'monitoring' && monitoringSource) {
        try { monitoringSource.close(); } catch(e) {}
        monitoringSource = null;
    }

    // Set body[data-route] — CSS shows only the matching section.
    document.body.setAttribute('data-route', route);

    // Update .active on nav links.
    var links = document.querySelectorAll('.section-nav-link');
    for (var i = 0; i < links.length; i++) {
        var l = links[i];
        if (l.getAttribute('data-nav-route') === route) {
            l.classList.add('active');
        } else {
            l.classList.remove('active');
        }
    }

    // Trigger section fade-in.
    var section = document.getElementById('section-' + route);
    if (section) {
        // Restart the CSS animation (remove + reflow + re-add the class).
        section.classList.remove('fade-in');
        void section.offsetWidth; // force reflow
        section.classList.add('fade-in');
    }

    // Dispatch to the route handler.
    var handler = ROUTE_TO_HANDLER[route];
    if (handler) {
        try { handler(); } catch(e) { if (window.__VRHUB_DEBUG__) console.error('route handler', route, e); }
    }
}

// routeTo programmatically navigates to a route (updates the hash
// AND routes). Use this from buttons/links that don't have an
// <a href> — e.g. the "Passer en mode Power User" CTA.
function routeTo(route) {
    if (location.hash === '#/' + route) {
        // Hash didn't change → fire routeFromHash anyway
        routeFromHash({ force: true });
    } else {
        location.hash = '#/' + route;
    }
}

function handleRouteDashboard() {
    // The dashboard widgets are the same DOM in both modes; CSS
    // hides the wrong-mode widgets. We re-run the per-mode loaders
    // so the data refreshes (Michel fetchConfig + fetchGameCount,
    // Power fetchPowerConfig + fetchPowerStats).
    if (getMode() === 'michel') {
        loadMichelWidgets();
    } else {
        loadPowerUserWidgets();
    }
    fetchChangelog(); // populate changelog card in both modes
}

function handleRouteUpdates() {
    fetchChangelog();
    fetchUpdateStatus(); // refresh update state when navigating to #/updates
}

// Client setup card is populated by fetchConfig() on success.
// loadMichelClientSetupCard is kept as a safety net: it runs its own
// fetch ONLY if fetchConfig() already returned early (baseUriEl missing).
// In practice baseUriEl is always present, so this is a no-op.
var _clientSetupCardRendered = false;
function loadMichelClientSetupCard() {
    // fetchConfig() will call _renderClientSetupCard() when it completes.
    // We defer 300 ms and only self-fetch if the card is still showing
    // the initial loading placeholder (meaning fetchConfig didn't run).
    setTimeout(function() {
        var container = document.getElementById('michel-client-setup-content');
        if (!container) return;
        if (_clientSetupCardRendered) return;
        // fetchConfig didn't populate the card — fetch independently.
        fetch('/admin/api/admin/settings', {
            credentials: 'same-origin',
            headers: { 'Accept': 'application/json' }
        })
        .then(function(r) { if (!r.ok) throw new Error(r.status); return r.json(); })
        .then(function(data) { _renderClientSetupCard((data && data.data) || {}); })
        .catch(function() { _renderClientSetupCardError(); });
    }, 300);
}

// Render the Michel "Connecter un client" card from already-fetched settings data.
// Called by fetchConfig() on success so there is no duplicate request.
function _renderClientSetupCard(d) {
    var container = document.getElementById('michel-client-setup-content');
    if (!container) return;
    _clientSetupCardRendered = true;

    var baseUri = (d && d.base_uri) || '';
    var configJsonURL = baseUri ? baseUri + 'config.json' : '';
    var pwd = (d && d.archive_password) || '';
    container.innerHTML = '';

    function makeRow(labelText, value, masked) {
        var wrap = document.createElement('div');
        wrap.className = 'michel-client-row';
        var lbl = document.createElement('span');
        lbl.className = 'michel-client-label text-muted';
        lbl.textContent = labelText;
        wrap.appendChild(lbl);
        var row = document.createElement('div');
        row.className = 'michel-client-value-row';
        var inp = document.createElement('input');
        inp.type = masked ? 'password' : 'text';
        inp.readOnly = true;
        inp.value = value;
        inp.className = 'form-input michel-client-input';
        row.appendChild(inp);
        if (masked) {
            var revealBtn = document.createElement('button');
            revealBtn.type = 'button';
            revealBtn.className = 'btn btn-secondary michel-client-btn';
            revealBtn.textContent = '👁';
            revealBtn.setAttribute('aria-pressed', 'false');
            revealBtn.addEventListener('click', function() {
                var shown = inp.type === 'text';
                inp.type = shown ? 'password' : 'text';
                revealBtn.setAttribute('aria-pressed', String(!shown));
            });
            row.appendChild(revealBtn);
        }
        var copyBtn = document.createElement('button');
        copyBtn.type = 'button';
        copyBtn.className = 'btn btn-primary michel-client-btn';
        copyBtn.textContent = i18n('copy_btn', 'Copy');
        copyBtn.addEventListener('click', function() {
            copyToClipboard(value,
                function() { showInlineNotification(i18n('header_copied', 'Copié')); },
                function() { showInlineNotification(i18n('header_copy_failed', 'Échec')); }
            );
        });
        row.appendChild(copyBtn);
        wrap.appendChild(row);
        return wrap;
    }

    var recTitle = document.createElement('p');
    recTitle.className = 'michel-client-section-label text-muted';
    recTitle.textContent = i18n('client_setup_recommended', 'Méthode recommandée');
    container.appendChild(recTitle);
    container.appendChild(makeRow('config.json URL', configJsonURL, false));

    var manTitle = document.createElement('p');
    manTitle.className = 'michel-client-section-label text-muted';
    manTitle.style.marginTop = 'var(--space-4)';
    manTitle.textContent = i18n('client_setup_manual', 'Méthode manuelle');
    container.appendChild(manTitle);
    container.appendChild(makeRow(i18n('client_setup_base_uri', 'URI de base'), baseUri, false));
    container.appendChild(makeRow(i18n('password_label', 'Mot de passe'), pwd, true));
}

function _renderClientSetupCardError() {
    var container = document.getElementById('michel-client-setup-content');
    if (container) {
        container.innerHTML = '<p class="text-muted">' + escapeHTML(i18n('client_setup_error', 'Impossible de charger la configuration.')) + '</p>';
    }
}

// Fetch the last releases from GitHub and render changelog cards.
async function fetchChangelog() {
    var michelContent = document.getElementById('michel-changelog-content');
    var powerContent = document.getElementById('power-changelog-content');
    try {
        var r = await fetch('/admin/api/update/changelog', {
            credentials: 'same-origin',
            headers: { 'Accept': 'application/json' }
        });
        if (!r.ok) throw new Error('HTTP ' + r.status);
        var json = await r.json();
        var releases = json.data || [];
        var html = renderChangelogHTML(releases);
        if (michelContent) michelContent.innerHTML = html;
        if (powerContent) powerContent.innerHTML = html;
    } catch(e) {
        var errHtml = '<p class="text-muted">' + i18n('changelog_error', 'Impossible de charger le changelog.') + '</p>';
        if (michelContent) michelContent.innerHTML = errHtml;
        if (powerContent) powerContent.innerHTML = errHtml;
    }
}

function renderChangelogHTML(releases) {
    if (!releases || releases.length === 0) {
        return '<p class="text-muted">' + i18n('changelog_empty', 'Aucune release trouvée.') + '</p>';
    }
    var currentVer = (updateStatus && updateStatus.currentVersion) || '';
    var latestVer = (updateStatus && updateStatus.latestVersion) || '';
    var out = '<div class="changelog-list">';
    for (var i = 0; i < releases.length; i++) {
        var rel = releases[i];
        var tag = rel.tag || '';
        var ver = rel.version || tag;
        var isInstalled = ver === currentVer || tag === currentVer || ('v' + ver) === currentVer || ('v' + currentVer) === tag;
        var isLatest = ver === latestVer || tag === latestVer || ('v' + ver) === latestVer || ('v' + latestVer) === tag;
        out += '<div class="changelog-entry">';
        out += '<div class="changelog-entry-header">';
        out += '<strong>' + escapeHTML(tag || ver) + '</strong>';
        if (isLatest) out += ' <span class="badge-pill badge-update">' + i18n('update_latest_badge', 'dernier') + '</span>';
        if (isInstalled) out += ' <span class="badge-pill badge-primary">' + i18n('update_installed_badge', 'installé') + '</span>';
        if (rel.html_url) {
            out += ' <a href="' + escapeHTML(rel.html_url) + '" target="_blank" rel="noopener noreferrer" class="text-muted" style="font-size:0.8rem;">GitHub ↗</a>';
        }
        out += '</div>';
        if (rel.body) {
            out += '<div class="changelog-entry-body text-muted" style="font-size:0.85rem;margin-top:var(--space-2);">' + renderMarkdown(rel.body) + '</div>';
        }
        out += '</div>';
    }
    out += '</div>';
    return out;
}

function escapeHTML(str) {
    return String(str)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;');
}

// Apply inline markdown transforms to already-HTML-escaped text.
// Supports **bold**, *italic*, `code`, and [text](https://...) links.
function _inlineMd(text) {
    text = text.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    text = text.replace(/\*([^*\n]+)\*/g, '<em>$1</em>');
    text = text.replace(/`([^`]+)`/g, '<code>$1</code>');
    // Only allow http/https links to prevent javascript: XSS
    text = text.replace(/\[([^\]]*)\]\((https?:\/\/[^)]*)\)/g,
        '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>');
    return text;
}

// Convert a GitHub-flavoured markdown string to safe HTML.
// Handles ATX headings (#/##/###), unordered lists (-/*), paragraphs,
// and delegates inline markup to _inlineMd().
function renderMarkdown(raw) {
    if (!raw) return '';
    var lines = raw.split('\n');
    var out = '';
    var inList = false;
    for (var i = 0; i < lines.length; i++) {
        var line = lines[i];
        var trimmed = line.trimStart ? line.trimStart() : line.replace(/^\s+/, '');
        if (/^### /.test(trimmed)) {
            if (inList) { out += '</ul>'; inList = false; }
            out += '<h5>' + _inlineMd(escapeHTML(trimmed.slice(4))) + '</h5>';
        } else if (/^## /.test(trimmed)) {
            if (inList) { out += '</ul>'; inList = false; }
            out += '<h4>' + _inlineMd(escapeHTML(trimmed.slice(3))) + '</h4>';
        } else if (/^# /.test(trimmed)) {
            if (inList) { out += '</ul>'; inList = false; }
            out += '<h3>' + _inlineMd(escapeHTML(trimmed.slice(2))) + '</h3>';
        } else if (/^[-*] /.test(trimmed)) {
            if (!inList) { out += '<ul>'; inList = true; }
            out += '<li>' + _inlineMd(escapeHTML(trimmed.slice(2))) + '</li>';
        } else if (trimmed === '') {
            if (inList) { out += '</ul>'; inList = false; }
        } else {
            if (inList) { out += '</ul>'; inList = false; }
            out += '<p>' + _inlineMd(escapeHTML(line)) + '</p>';
        }
    }
    if (inList) out += '</ul>';
    return out;
}

// Safe clipboard helper — uses modern Clipboard API when available
// (requires HTTPS or localhost), falls back to execCommand for plain-HTTP
// LAN deployments. onSuccess / onFail are zero-argument callbacks.
function copyToClipboard(text, onSuccess, onFail) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(onSuccess, onFail);
    } else {
        try {
            var ta = document.createElement('textarea');
            ta.value = text;
            ta.style.cssText = 'position:fixed;left:-9999px;top:0;opacity:0';
            document.body.appendChild(ta);
            ta.focus();
            ta.select();
            document.execCommand('copy');
            document.body.removeChild(ta);
            onSuccess();
        } catch(e) { onFail(); }
    }
}

function handleRouteGames() {
    renderGamesTable();
}

function handleRouteConfiguration() {
    var container = document.getElementById('settings-form');
    if (!container) return;
    container.textContent = i18n('settings_loading');
    fetch('/admin/api/admin/settings', { credentials: 'same-origin', headers: { 'Accept': 'application/json' } })
        .then(function(r) {
            if (r.status === 401) { window.location.href = '/admin/login'; return null; }
            if (!r.ok) throw new Error(r.status);
            return r.json();
        })
        .then(function(data) {
            if (!data) return;
            var d = (data && data.data) || {};
            container.textContent = '';
            renderSettingsForm(container, d);
        })
        .catch(function(err) {
            container.innerHTML = '<p style="color:var(--color-error,#c0392b)">' +
                i18n('settings_load_error', 'Erreur de chargement des paramètres') +
                (err ? ' (' + err.message + ')' : '') + '. ' +
                '<a href="/admin/login">Reconnectez-vous</a>.</p>';
        });
}

function renderSettingsForm(container, d) {
    var form = document.createElement('form');
    form.id = 'settings-form-el';
    form.className = 'settings-form';
    // M-03 (review 2026-06-11): Configuration is now accessible in
    // Michel mode as read-only. The HTML banner
    // #settings-readonly-banner is shown via CSS (data-mode-only).
    // We pass `disabled: true` to every input when read-only.
    var readOnly = (getMode() === 'michel');

    function makeField(id, label, type, value, opts) {
        opts = opts || {};
        var group = document.createElement('div');
        group.className = 'form-group';
        var lbl = document.createElement('label');
        lbl.className = 'form-label';
        lbl.textContent = label;
        lbl.htmlFor = id;
        group.appendChild(lbl);

        var wrapper = document.createElement('div');
        wrapper.style.display = 'flex';
        wrapper.style.gap = '0.5rem';
        wrapper.style.alignItems = 'center';

        var input;
        if (type === 'checkbox') {
            input = document.createElement('input');
            input.type = 'checkbox';
            input.checked = !!value;
        } else {
            input = document.createElement('input');
            input.type = type;
            input.value = value != null ? value : '';
            if (opts.min != null) input.min = opts.min;
            if (opts.max != null) input.max = opts.max;
        }
        input.id = id;
        input.className = 'form-input';
        input.style.flex = '1';
        // M-03: disable the input in Michel (read-only) mode.
        if (readOnly) input.disabled = true;
        wrapper.appendChild(input);

        if (opts.togglePassword) {
            var toggleBtn = document.createElement('button');
            toggleBtn.type = 'button';
            toggleBtn.className = 'btn btn-secondary';
            toggleBtn.textContent = i18n('setup_show_password', 'Afficher');
            if (readOnly) toggleBtn.disabled = true;
            toggleBtn.addEventListener('click', function() {
                if (input.type === 'password') {
                    input.type = 'text';
                    toggleBtn.textContent = i18n('setup_hide_password', 'Masquer');
                } else {
                    input.type = 'password';
                    toggleBtn.textContent = i18n('setup_show_password', 'Afficher');
                }
            });
            wrapper.appendChild(toggleBtn);
        }

        // M-03: hide the "Reset" button in Michel mode (no point resetting
        // a disabled field).
        if (!readOnly) {
            var resetBtn = document.createElement('button');
            resetBtn.type = 'button';
            resetBtn.className = 'btn btn-secondary';
            resetBtn.textContent = '↺';
            resetBtn.title = i18n('config_reset_default', 'Réinitialiser');
            resetBtn.addEventListener('click', function() {
                if (type === 'checkbox') {
                    input.checked = !!opts.defaultValue;
                } else {
                    input.value = opts.defaultValue != null ? opts.defaultValue : '';
                }
            });
            wrapper.appendChild(resetBtn);
        }

        group.appendChild(wrapper);

        // M-15 (review 2026-06-11): per-field help text. Provide
        // either a string in opts.help OR an i18n key in opts.helpKey.
        var helpText = opts.help || (opts.helpKey ? i18n(opts.helpKey) : null);
        if (helpText) {
            var helpEl = document.createElement('small');
            helpEl.className = 'form-help';
            helpEl.textContent = helpText;
            group.appendChild(helpEl);
        }
        return { group: group, input: input };
    }

    // Server host
    var hostField = makeField('cfg-host', i18n('config_listen_address'), 'text', d.server && d.server.host, { defaultValue: '', helpKey: 'config_listen_address_help' });
    form.appendChild(hostField.group);

    // Server port
    var portField = makeField('cfg-port', i18n('config_port'), 'number', d.server && d.server.port, { min: 1, max: 65535, defaultValue: 39457, helpKey: 'config_port_help' });
    form.appendChild(portField.group);

    // Archive password
    var pwdField = makeField('cfg-archive-password', i18n('config_archive_password'), 'password', d.archive_password, { togglePassword: true, defaultValue: '', helpKey: 'config_archive_password_help' });
    form.appendChild(pwdField.group);

    // Game folders
    var foldersGroup = document.createElement('div');
    foldersGroup.className = 'form-group';
    var foldersLbl = document.createElement('label');
    foldersLbl.className = 'form-label';
    foldersLbl.textContent = i18n('config_game_folders');
    foldersGroup.appendChild(foldersLbl);
    var foldersHelpEl = document.createElement('small');
    foldersHelpEl.className = 'form-help';
    foldersHelpEl.textContent = i18n('config_game_folders_help');
    foldersGroup.appendChild(foldersHelpEl);
    var foldersList = document.createElement('div');
    foldersList.id = 'cfg-folders-list';
    foldersList.style.display = 'flex';
    foldersList.style.flexDirection = 'column';
    foldersList.style.gap = '0.5rem';
    var folders = (d.game_folders && Array.isArray(d.game_folders)) ? d.game_folders : [];
    if (folders.length === 0) {
        var emptyMsg = document.createElement('p');
        emptyMsg.id = 'cfg-folders-empty';
        emptyMsg.className = 'form-help';
        var fallback = d.data_dir ? ' ' + i18n('config_no_folders_fallback', 'Répertoire de données utilisé par défaut :') + ' ' + d.data_dir : '';
        emptyMsg.textContent = i18n('config_no_folders', 'Aucun dossier configuré.') + fallback;
        foldersList.appendChild(emptyMsg);
    }
    folders.forEach(function(f) {
        var row = document.createElement('div');
        row.style.display = 'flex';
        row.style.gap = '0.5rem';
        var inp = document.createElement('input');
        inp.type = 'text';
        inp.value = f;
        inp.className = 'form-input';
        inp.style.flex = '1';
        if (readOnly) inp.setAttribute('disabled', '');
        row.appendChild(inp);
        var delBtn = document.createElement('button');
        delBtn.type = 'button';
        delBtn.className = 'btn btn-danger';
        delBtn.textContent = '×';
        delBtn.addEventListener('click', function() { row.remove(); });
        if (readOnly) delBtn.style.display = 'none';
        row.appendChild(delBtn);
        foldersList.appendChild(row);
    });
    foldersGroup.appendChild(foldersList);
    var addFolderBtn = document.createElement('button');
    addFolderBtn.type = 'button';
    addFolderBtn.className = 'btn btn-secondary';
    addFolderBtn.textContent = i18n('config_add_folder');
    if (readOnly) addFolderBtn.style.display = 'none';
    addFolderBtn.addEventListener('click', function() {
        var emptyEl = document.getElementById('cfg-folders-empty');
        if (emptyEl) emptyEl.remove();
        var row = document.createElement('div');
        row.style.display = 'flex';
        row.style.gap = '0.5rem';
        var inp = document.createElement('input');
        inp.type = 'text';
        inp.placeholder = 'C:\\Games';
        inp.className = 'form-input';
        inp.style.flex = '1';
        row.appendChild(inp);
        var delBtn = document.createElement('button');
        delBtn.type = 'button';
        delBtn.className = 'btn btn-danger';
        delBtn.textContent = '×';
        delBtn.addEventListener('click', function() { row.remove(); });
        row.appendChild(delBtn);
        foldersList.appendChild(row);
        inp.focus();
    });
    foldersGroup.appendChild(addFolderBtn);
    var resetFoldersBtn = document.createElement('button');
    resetFoldersBtn.type = 'button';
    resetFoldersBtn.className = 'btn btn-secondary';
    resetFoldersBtn.textContent = '↺ ' + i18n('config_reset_default');
    if (readOnly) resetFoldersBtn.style.display = 'none';
    resetFoldersBtn.addEventListener('click', function() {
        foldersList.textContent = '';
    });
    foldersGroup.appendChild(resetFoldersBtn);
    form.appendChild(foldersGroup);

    // Metadata section (M-01)
    var metaHeader = document.createElement('h3');
    metaHeader.textContent = i18n('config_metadata_section');
    metaHeader.style.marginTop = 'var(--space-6)';
    form.appendChild(metaHeader);

    var metaUrlField = makeField('cfg-metadata-url', i18n('config_metadata_url'), 'text', d.metadata && d.metadata.url, { defaultValue: '', helpKey: 'config_metadata_url_help' });
    form.appendChild(metaUrlField.group);
    // Convert nanoseconds to a duration string for the input.
    var metaIntervalValue = '';
    if (d.metadata && d.metadata.refresh_interval) {
        if (typeof d.metadata.refresh_interval === 'number') {
            metaIntervalValue = Math.floor(d.metadata.refresh_interval / 1e9) + 's';
        } else {
            metaIntervalValue = String(d.metadata.refresh_interval);
        }
    }
    var metaIntervalField = makeField('cfg-metadata-refresh-interval', i18n('config_metadata_refresh_interval'), 'text', metaIntervalValue, { defaultValue: '', helpKey: 'config_metadata_refresh_interval_help' });
    form.appendChild(metaIntervalField.group);

    // Update section
    var updHeader = document.createElement('h3');
    updHeader.textContent = i18n('config_update_section');
    updHeader.style.marginTop = 'var(--space-6)';
    form.appendChild(updHeader);

    // Auto-apply (update check is always enabled; only auto_apply and auto_restart are configurable)
    var autoField = makeField('cfg-auto-apply', i18n('config_auto_apply'), 'checkbox', d.update && d.update.auto_apply, { defaultValue: true, helpKey: 'config_auto_apply_help' });
    form.appendChild(autoField.group);

    // Auto-restart
    var autoRestartField = makeField('cfg-auto-restart', i18n('config_auto_restart'), 'checkbox', d.update && d.update.auto_restart, { defaultValue: false, helpKey: 'config_auto_restart_help' });
    form.appendChild(autoRestartField.group);

    // Advanced update fields (M-01)
    var updCheckValue = '';
    if (d.update && d.update.check_interval) {
        if (typeof d.update.check_interval === 'number') {
            updCheckValue = Math.floor(d.update.check_interval / 1e9) + 's';
        } else {
            updCheckValue = String(d.update.check_interval);
        }
    }
    var updCheckField = makeField('cfg-update-check-interval', i18n('config_update_check_interval'), 'text', updCheckValue, { defaultValue: '', helpKey: 'config_update_check_interval_help' });
    form.appendChild(updCheckField.group);
    var updTokenField = makeField('cfg-update-github-token', i18n('config_update_github_token'), 'password', d.update && d.update.github_token, { togglePassword: true, defaultValue: '', helpKey: 'config_update_github_token_help' });
    form.appendChild(updTokenField.group);
    var updOwnerField = makeField('cfg-update-owner', i18n('config_update_owner'), 'text', d.update && d.update.owner, { defaultValue: '' });
    form.appendChild(updOwnerField.group);
    var updRepoField = makeField('cfg-update-repo', i18n('config_update_repo'), 'text', d.update && d.update.repo, { defaultValue: '' });
    form.appendChild(updRepoField.group);

    // Save button
    var actions = document.createElement('div');
    actions.className = 'wizard-actions';
    actions.style.marginTop = 'var(--space-6)';
    var saveBtn = document.createElement('button');
    saveBtn.type = 'submit';
    if (readOnly) saveBtn.style.display = 'none';
    saveBtn.className = 'btn btn-primary';
    saveBtn.textContent = i18n('config_save', 'Sauvegarder');
    actions.appendChild(saveBtn);
    form.appendChild(actions);

    // Admin password change section
    var pwdSection = document.createElement('div');
    pwdSection.className = 'settings-password-section';
    pwdSection.style.marginTop = 'var(--space-8)';
    pwdSection.style.borderTop = '1px solid var(--border)';
    pwdSection.style.paddingTop = 'var(--space-6)';

    var pwdTitle = document.createElement('h3');
    pwdTitle.textContent = i18n('config_admin_password_change', 'Changer le mot de passe administrateur');
    pwdSection.appendChild(pwdTitle);

    var oldPwdField = makeField('cfg-old-password', i18n('config_old_password', 'Ancien mot de passe'), 'password', '', { togglePassword: true });
    pwdSection.appendChild(oldPwdField.group);

    var newPwdField = makeField('cfg-new-password', i18n('config_new_password', 'Nouveau mot de passe'), 'password', '', { togglePassword: true });
    pwdSection.appendChild(newPwdField.group);

    var confirmPwdField = makeField('cfg-confirm-password', i18n('config_confirm_password', 'Confirmer le mot de passe'), 'password', '', { togglePassword: true });
    pwdSection.appendChild(confirmPwdField.group);

    var changeBtn = document.createElement('button');
    changeBtn.type = 'button';
    changeBtn.className = 'btn btn-primary';
    changeBtn.textContent = i18n('config_admin_password_change', 'Changer le mot de passe');
    changeBtn.addEventListener('click', function() {
        var oldPwd = oldPwdField.input.value;
        var newPwd = newPwdField.input.value;
        var confirmPwd = confirmPwdField.input.value;
        if (!oldPwd || !newPwd) {
            alert(i18n('config_password_required', 'Veuillez remplir tous les champs'));
            return;
        }
        if (newPwd !== confirmPwd) {
            alert(i18n('config_password_mismatch', 'Les mots de passe ne correspondent pas'));
            return;
        }
        if (newPwd.length < 4) {
            alert(i18n('config_password_too_short', 'Le mot de passe doit contenir au moins 4 caractères'));
            return;
        }
        fetch('/admin/api/admin/change-password', {
            method: 'POST',
            credentials: 'same-origin',
            headers: {
                'Content-Type': 'application/json',
                'X-CSRF-Token': getCSRFToken()
            },
            body: JSON.stringify({ old_password: oldPwd, new_password: newPwd })
        })
        .then(function(r) {
            if (!r.ok) return r.json().then(function(err) { throw new Error(err.message || 'Error'); });
            alert(i18n('config_password_changed', 'Mot de passe changé avec succès'));
            oldPwdField.input.value = '';
            newPwdField.input.value = '';
            confirmPwdField.input.value = '';
        })
        .catch(function(err) {
            alert(i18n('config_password_change_failed', 'Échec du changement') + ': ' + err.message);
        });
    });
    pwdSection.appendChild(changeBtn);
    form.appendChild(pwdSection);

    // M-02 (review 2026-06-11): API key management section. The
    // endpoints /admin/api/admin/api-key/{reveal,regenerate} exist
    // on the server (Story 6-3) but were unreachable from the UI.
    // Power-only — Michel has no use for an API key.
    if (!readOnly) {
        var apiSection = document.createElement('div');
        apiSection.className = 'settings-api-key-section';
        apiSection.style.marginTop = 'var(--space-8)';
        apiSection.style.borderTop = '1px solid var(--border)';
        apiSection.style.paddingTop = 'var(--space-6)';

        var apiTitle = document.createElement('h3');
        apiTitle.textContent = i18n('api_key_title');
        apiSection.appendChild(apiTitle);

        var apiHelp = document.createElement('p');
        apiHelp.className = 'text-muted';
        apiHelp.style.fontSize = '0.875rem';
        apiHelp.textContent = i18n('api_key_help');
        apiSection.appendChild(apiHelp);

        var apiValueContainer = document.createElement('div');
        apiValueContainer.style.marginTop = 'var(--space-3)';
        apiValueContainer.style.display = 'flex';
        apiValueContainer.style.gap = '0.5rem';
        apiValueContainer.style.alignItems = 'center';
        var apiValueInput = document.createElement('input');
        apiValueInput.type = 'password';
        apiValueInput.readOnly = true;
        apiValueInput.placeholder = '••••••••••••••••';
        apiValueInput.className = 'form-input';
        apiValueInput.style.flex = '1';
        apiValueInput.style.fontFamily = 'monospace';
        apiValueContainer.appendChild(apiValueInput);
        var apiRevealBtn = document.createElement('button');
        apiRevealBtn.type = 'button';
        apiRevealBtn.className = 'btn btn-secondary';
        apiRevealBtn.textContent = i18n('api_key_reveal');
        apiRevealBtn.addEventListener('click', function() {
            powerFetch('/admin/api/admin/api-key', {
                headers: { 'X-CSRF-Token': getCSRFToken() }
            })
            .then(function(r) { return r.json().then(function(j) { return { ok: r.ok, status: r.status, body: j }; }); })
            .then(function(res) {
                if (res.status === 404) {
                    apiValueInput.value = '';
                    apiValueInput.placeholder = i18n('api_key_no_plaintext');
                    return;
                }
                if (!res.ok) { showInlineNotification('API error: HTTP ' + res.status); return; }
                var pwd = res.body && res.body.data && res.body.data.api_key_plaintext;
                if (!pwd) {
                    apiValueInput.placeholder = i18n('api_key_no_plaintext');
                    return;
                }
                apiValueInput.value = pwd;
                apiValueInput.type = 'text';
                copyToClipboard(pwd,
                    function() { showInlineNotification(i18n('api_key_copied', 'Clé copiée')); },
                    function() { /* clipboard unavailable — key is visible in the field */ }
                );
            })
            .catch(function(err) {
                showInlineNotification('Reveal failed: ' + (err && err.message || 'unknown'));
            });
        });
        apiValueContainer.appendChild(apiRevealBtn);
        apiSection.appendChild(apiValueContainer);

        var apiRegenBtn = document.createElement('button');
        apiRegenBtn.type = 'button';
        apiRegenBtn.className = 'btn btn-secondary';
        apiRegenBtn.style.marginTop = 'var(--space-3)';
        apiRegenBtn.textContent = i18n('api_key_regenerate');
        apiRegenBtn.addEventListener('click', function() {
            if (!window.confirm(i18n('api_key_regen_confirm'))) return;
            powerFetch('/admin/api/admin/api-key/regenerate', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'X-CSRF-Token': getCSRFToken()
                },
                body: JSON.stringify({})
            })
            .then(function(r) { return r.json().then(function(j) { return { ok: r.ok, status: r.status, body: j }; }); })
            .then(function(res) {
                if (!res.ok) { showInlineNotification('Regenerate failed: HTTP ' + res.status); return; }
                var pwd = res.body && res.body.data && res.body.data.api_key_plaintext;
                if (!pwd) { showInlineNotification('Server returned no key'); return; }
                apiValueInput.value = pwd;
                apiValueInput.type = 'text';
                alert(i18n('api_key_save_warning'));
                copyToClipboard(pwd,
                    function() { showInlineNotification(i18n('api_key_copied', 'Clé copiée')); },
                    function() { /* clipboard unavailable — key is visible in the field */ }
                );
            })
            .catch(function(err) {
                showInlineNotification('Regenerate failed: ' + (err && err.message || 'unknown'));
            });
        });
        apiSection.appendChild(apiRegenBtn);

        form.appendChild(apiSection);
    }

    // Submit handler
    form.addEventListener('submit', function(e) {
        e.preventDefault();
        var payload = {
            server: {
                host: hostField.input.value.trim(),
                port: parseInt(portField.input.value, 10)
            },
            update: {
                auto_apply: autoField.input.checked,
                auto_restart: autoRestartField.input.checked,
                check_interval: updCheckField.input.value.trim(),
                github_token: updTokenField.input.value,
                owner: updOwnerField.input.value.trim(),
                repo: updRepoField.input.value.trim()
            },
            metadata: {
                refresh_interval: metaIntervalField.input.value.trim(),
                url: metaUrlField.input.value.trim()
            }
        };
        var archivePwd = pwdField.input.value.trim();
        if (archivePwd) {
            if (archivePwd.length < 8) {
                alert(i18n('config_archive_password_too_short', 'Le mot de passe archive doit contenir au moins 8 caractères'));
                return;
            }
            payload.archive_password = archivePwd;
        }
        var folderInputs = foldersList.querySelectorAll('input');
        var folderValues = [];
        folderInputs.forEach(function(inp) {
            if (inp.value.trim()) folderValues.push(inp.value.trim());
        });
        payload.game_folders = folderValues;

        fetch('/admin/api/admin/settings', {
            method: 'PUT',
            credentials: 'same-origin',
            headers: {
                'Content-Type': 'application/json',
                'X-CSRF-Token': getCSRFToken()
            },
            body: JSON.stringify(payload)
        })
        .then(function(r) {
            if (!r.ok) return r.json().then(function(err) { throw new Error(err.message || 'Error'); });
            alert(i18n('config_saved', 'Paramètres sauvegardés'));
        })
        .catch(function(err) {
            alert(i18n('config_save_failed', 'Échec de la sauvegarde') + ': ' + err.message);
        });
    });

    container.appendChild(form);
}

function handleRouteBackup() {
    initBackupPage();
}

function handleRouteApiDocs() {
    renderDocsCatalog();
}

function handleRouteMonitoring() {
    openMonitoringFeed();
}

function handleRouteStats() {
    renderStatsTable();
}

// M-04 (review 2026-06-11): dedicated "Client Setup" page accessible
// in both Michel and Power modes. Fetches /admin/api/admin/settings
// (via powerFetch for the X-Power-Mode header so the API doesn't
// return 404 if the cookie is missing), then renders three cards:
//   1. Recommended method: the <base>/config.json URL (one-tap ingest
//      in the VRHub client).
//   2. Manual method: baseUri + archive password with copy buttons.
//   3. Step-by-step instructions.
// Plus a "Test the connection" button that hits /meta.7z to verify
// the server is reachable and serving encrypted meta.
function handleRouteClientSetup() {
    var container = document.getElementById('client-setup-content');
    if (!container) return;
    container.textContent = i18n('client_setup_loading');

    // Use powerFetch so X-Power-Mode: 1 is sent (defense in depth
    // for the cookie-based gate in stats.go / monitoring.go).
    powerFetch('/admin/api/admin/settings', { headers: { 'Accept': 'application/json' } })
        .then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.json();
        })
        .then(function(data) {
            var d = (data && data.data) || {};
            var baseUri = d.base_uri || '';
            var configJsonURL = baseUri + 'config.json';
            var pwd = d.archive_password || '';
            container.textContent = '';

            // ===== Card 1: Recommended method =====
            var recCard = document.createElement('div');
            recCard.className = 'card';
            var recH = document.createElement('h3');
            recH.textContent = i18n('client_setup_recommended');
            recCard.appendChild(recH);
            var recHelp = document.createElement('p');
            recHelp.className = 'text-muted';
            recHelp.textContent = i18n('client_setup_url_help');
            recCard.appendChild(recHelp);
            var recLabel = document.createElement('label');
            recLabel.className = 'form-label';
            recLabel.textContent = i18n('client_setup_url_label');
            recCard.appendChild(recLabel);
            var recRow = document.createElement('div');
            recRow.style.display = 'flex';
            recRow.style.gap = '0.5rem';
            recRow.style.alignItems = 'center';
            recRow.style.marginTop = '0.25rem';
            var recInput = document.createElement('input');
            recInput.type = 'text';
            recInput.readOnly = true;
            recInput.value = configJsonURL;
            recInput.className = 'form-input';
            recInput.style.flex = '1';
            recInput.style.fontFamily = 'monospace';
            recRow.appendChild(recInput);
            var recCopyBtn = document.createElement('button');
            recCopyBtn.type = 'button';
            recCopyBtn.className = 'btn btn-primary';
            recCopyBtn.textContent = i18n('copy_btn', 'Copy');
            recCopyBtn.addEventListener('click', function() {
                copyToClipboard(configJsonURL,
                    function() { showInlineNotification(i18n('header_copied', 'Copié')); },
                    function() { showInlineNotification(i18n('header_copy_failed', 'Échec')); }
                );
            });
            recRow.appendChild(recCopyBtn);
            recCard.appendChild(recRow);
            container.appendChild(recCard);

            // ===== Card 2: Manual method =====
            var manCard = document.createElement('div');
            manCard.className = 'card';
            var manH = document.createElement('h3');
            manH.textContent = i18n('client_setup_manual');
            manCard.appendChild(manH);

            var manUriLabel = document.createElement('label');
            manUriLabel.className = 'form-label';
            manUriLabel.textContent = i18n('client_setup_base_uri');
            manCard.appendChild(manUriLabel);
            var manUriRow = document.createElement('div');
            manUriRow.style.display = 'flex';
            manUriRow.style.gap = '0.5rem';
            manUriRow.style.alignItems = 'center';
            var manUriInput = document.createElement('input');
            manUriInput.type = 'text';
            manUriInput.readOnly = true;
            manUriInput.value = baseUri;
            manUriInput.className = 'form-input';
            manUriInput.style.flex = '1';
            manUriInput.style.fontFamily = 'monospace';
            manUriRow.appendChild(manUriInput);
            var manUriCopyBtn = document.createElement('button');
            manUriCopyBtn.type = 'button';
            manUriCopyBtn.className = 'btn btn-primary';
            manUriCopyBtn.textContent = i18n('copy_btn', 'Copy');
            manUriCopyBtn.addEventListener('click', function() {
                copyToClipboard(baseUri,
                    function() { showInlineNotification(i18n('header_copied', 'Copié')); },
                    function() { showInlineNotification(i18n('header_copy_failed', 'Échec')); }
                );
            });
            manUriRow.appendChild(manUriCopyBtn);
            manCard.appendChild(manUriRow);

            var manPwdLabel = document.createElement('label');
            manPwdLabel.className = 'form-label';
            manPwdLabel.style.marginTop = 'var(--space-3)';
            manPwdLabel.textContent = i18n('client_setup_password');
            manCard.appendChild(manPwdLabel);
            var manPwdRow = document.createElement('div');
            manPwdRow.style.display = 'flex';
            manPwdRow.style.gap = '0.5rem';
            manPwdRow.style.alignItems = 'center';
            var manPwdInput = document.createElement('input');
            manPwdInput.type = 'password';
            manPwdInput.readOnly = true;
            manPwdInput.value = pwd;
            manPwdInput.className = 'form-input';
            manPwdInput.style.flex = '1';
            manPwdInput.style.fontFamily = 'monospace';
            manPwdRow.appendChild(manPwdInput);
            var manPwdToggle = document.createElement('button');
            manPwdToggle.type = 'button';
            manPwdToggle.className = 'btn btn-secondary';
            manPwdToggle.textContent = i18n('setup_show_password');
            manPwdToggle.addEventListener('click', function() {
                if (manPwdInput.type === 'password') {
                    manPwdInput.type = 'text';
                    manPwdToggle.textContent = i18n('setup_hide_password');
                } else {
                    manPwdInput.type = 'password';
                    manPwdToggle.textContent = i18n('setup_show_password');
                }
            });
            manPwdRow.appendChild(manPwdToggle);
            var manPwdCopyBtn = document.createElement('button');
            manPwdCopyBtn.type = 'button';
            manPwdCopyBtn.className = 'btn btn-primary';
            manPwdCopyBtn.textContent = i18n('copy_btn', 'Copy');
            manPwdCopyBtn.addEventListener('click', function() {
                copyToClipboard(pwd,
                    function() { showInlineNotification(i18n('header_copied', 'Copié')); },
                    function() { showInlineNotification(i18n('header_copy_failed', 'Échec')); }
                );
            });
            manPwdRow.appendChild(manPwdCopyBtn);
            manCard.appendChild(manPwdRow);
            container.appendChild(manCard);

            // ===== Card 3: Test the connection =====
            var testCard = document.createElement('div');
            testCard.className = 'card';
            var testBtn = document.createElement('button');
            testBtn.type = 'button';
            testBtn.className = 'btn btn-primary';
            testBtn.textContent = i18n('client_setup_test');
            testBtn.addEventListener('click', function() {
                testBtn.disabled = true;
                testBtn.textContent = '...';
                // Probe /meta.7z — 200 = OK, anything else = problem.
                // The request runs without the session cookie so we
                // don't fail on CSRF. /meta.7z is a public endpoint.
                fetch('/meta.7z', { method: 'HEAD' })
                    .then(function(r) {
                        if (r.ok) {
                            showInlineNotification('OK: HTTP ' + r.status);
                        } else {
                            showInlineNotification('Failed: HTTP ' + r.status);
                        }
                    })
                    .catch(function(err) {
                        showInlineNotification('Failed: ' + (err && err.message || 'network error'));
                    })
                    .then(function() {
                        testBtn.disabled = false;
                        testBtn.textContent = i18n('client_setup_test');
                    });
            });
            testCard.appendChild(testBtn);
            container.appendChild(testCard);

            // ===== Card 4: Step-by-step instructions =====
            var stepsCard = document.createElement('div');
            stepsCard.className = 'card';
            var stepsList = document.createElement('ol');
            stepsList.style.paddingLeft = '1.5rem';
            [1, 2, 3, 4].forEach(function(n) {
                var li = document.createElement('li');
                li.textContent = i18n('client_setup_step' + n);
                li.style.marginTop = '0.5rem';
                stepsList.appendChild(li);
            });
            stepsCard.appendChild(stepsList);
            container.appendChild(stepsCard);
        })
        .catch(function(err) {
            container.textContent = 'Failed to load: ' + (err && err.message || 'unknown');
        });
}

// Story ui-michel-power-toggle (B1): the "Passer en mode Power User"
// CTA is visible on the Michel dashboard (admin.css:943 defensive
// rule) but the listener was previously attached only inside
// handleRoutePowerRequired — which is dispatched ONLY when the
// route is `power-required` (i.e. Michel tried to open a Power-only
// route like #/games). On the dashboard the handler never ran, so
// the button looked focusable but did nothing. Extracted here so
// the listener is wired at boot, regardless of the initial route.
// M-08 (review 2026-06-11): the CTA must not just switch to Power
// mode — it must also navigate the user to the route they were
// trying to reach. We remember the original route in sessionStorage
// before redirecting to power-required (see the dispatch table near
// the top of this file), and the CTA navigates to it after setMode.
function setupPowerUserCta() {
    var cta = document.getElementById('section-power-required-cta');
    if (!cta || cta.dataset.bound === '1') return;
    cta.dataset.bound = '1';
    cta.addEventListener('click', function() {
        var target = (function() {
            try { return sessionStorage.getItem('vrhub:pending-route'); } catch(e) { return null; }
        })() || 'dashboard';
        setMode('power');
        // Use a microtask so setMode has set the cookie before
        // the route fires its data fetch.
        setTimeout(function() { window.location.hash = '#/' + target; }, 0);
    });
}

function handleRoutePowerRequired() {
    // setupPowerUserCta is idempotent (dataset.bound guard). Calling
    // it from here too keeps the function wired if a user lands on
    // the power-required route directly (e.g. bookmarked URL) before
    // DOMContentLoaded has fired.
    setupPowerUserCta();
}

// Story X: render the API docs catalog by fetching the JSON from
// /admin/api/docs and building an accordion of endpoint cards. The
// existing standalone /admin/docs page (adminDocsHTMLBytes) used to
// do this server-side; we re-implement the catalog render in JS
// since the page is now a hash route in the SPA shell.
function renderDocsCatalog() {
    var container = document.getElementById('api-docs-catalog');
    if (!container) return;
    container.textContent = i18n('docs_loading');
    powerFetch('/admin/api/docs')
        .then(function(r) { if (!r.ok) throw new Error(r.status); return r.json(); })
        .then(function(payload) {
            var endpoints = (payload && payload.data && Array.isArray(payload.data.endpoints)) ? payload.data.endpoints : [];
            if (endpoints.length === 0) {
                container.textContent = i18n('docs_intro');
                return;
            }
            // Build the catalog using DOM APIs (no innerHTML for
            // dynamic content — XSS hardening).
            var wrap = document.createElement('div');
            wrap.className = 'api-docs-catalog';
            endpoints.forEach(function(ep) {
                var card = document.createElement('details');
                card.className = 'api-docs-card';
                var summary = document.createElement('summary');
                var method = document.createElement('span');
                method.className = 'api-docs-method method-' + String(ep.method || 'get').toLowerCase();
                method.textContent = ep.method || 'GET';
                var path = document.createElement('code');
                path.className = 'api-docs-path';
                path.textContent = ep.path || '';
                summary.appendChild(method);
                summary.appendChild(path);
                card.appendChild(summary);
                if (ep.description) {
                    var desc = document.createElement('p');
                    desc.textContent = ep.description;
                    card.appendChild(desc);
                }
                wrap.appendChild(card);
            });
            container.textContent = '';
            container.appendChild(wrap);
        })
        .catch(function() {
            container.textContent = i18n('docs_intro');
        });
}

// Story X: open a Server-Sent Events connection to /admin/monitoring
// and stream events into #monitoring-feed. Closes the previous
// source if any. (Replaces the legacy monitoring page handler.)
function openMonitoringFeed() {
    var feed = document.getElementById('monitoring-feed');
    if (!feed) return;
    if (monitoringSource) {
        try { monitoringSource.close(); } catch(e) {}
        monitoringSource = null;
    }
    feed.textContent = i18n('monitoring_connecting', 'Connexion au flux…');
    if (typeof EventSource === 'undefined') {
        feed.textContent = 'EventSource not supported in this browser.';
        return;
    }
    try {
        monitoringSource = new EventSource('/admin/monitoring');
    } catch (e) {
        feed.textContent = 'Failed to open monitoring feed.';
        return;
    }
    monitoringSource.onopen = function() {
        feed.textContent = '';
    };
    monitoringSource.onerror = function() {
        // The browser auto-reconnects; we just show a hint.
    };
    monitoringSource.onmessage = function(ev) {
        var line = document.createElement('div');
        line.className = 'monitoring-event';
        line.textContent = ev.data || '';
        feed.appendChild(line);
        // Cap the feed to the last 200 events so the DOM doesn't
        // grow unbounded.
        while (feed.children.length > 200) {
            feed.removeChild(feed.firstChild);
        }
    };
}

// Story X: header reveal — toggle the password chip between
// masked and visible. Reuses togglePasswordVisibility's
// counterpart (header chip is independent from the dashboard's
// password widget so the operator can reveal the password
// without scrolling to the dashboard).
function setupHeaderReveal() {
    var btn = document.getElementById('header-archive-password-reveal');
    var chip = document.getElementById('header-archive-password');
    if (!btn || !chip || btn.dataset.bound === '1') return;
    btn.dataset.bound = '1';
    btn.addEventListener('click', function() {
        if (chip.dataset.revealed === '1') {
            chip.dataset.revealed = '0';
            chip.textContent = chip.dataset.masked || '••••••••';
            btn.setAttribute('aria-pressed', 'false');
        } else {
            chip.dataset.revealed = '1';
            chip.textContent = chip.dataset.plain || i18n('password_label', '••••••••');
            btn.setAttribute('aria-pressed', 'true');
        }
    });
}

// Story X: lang selector — writes the new lang to localStorage,
// updates html[lang], and re-translates the page.
function setupLangSelector() {
    var sel = document.getElementById('header-lang-selector');
    if (!sel || sel.dataset.bound === '1') return;
    sel.dataset.bound = '1';
    // Sync the dropdown with the current lang on load.
    try { sel.value = getLang(); } catch(e) {}
    sel.addEventListener('change', function() {
        setLang(sel.value);
    });
}

// Theme toggle (dark default / light). Persists the choice in localStorage
// under 'vrhub-theme' and applies it via the body.theme-light class (the CSS
// token set). DESIGN.md: a 32×18 pill in the header, same placement as the
// language selector. Idempotent — skips if the toggle is absent or already
// bound (e.g. it is not present on the dedicated login page).
var THEME_STORAGE_KEY = 'vrhub-theme';
function getTheme() {
    try {
        var t = localStorage.getItem(THEME_STORAGE_KEY);
        return (t === 'light' || t === 'dark') ? t : 'dark';
    } catch (e) { return 'dark'; }
}
function applyTheme(theme) {
    var light = theme === 'light';
    document.body.classList.toggle('theme-light', light);
    var btn = document.getElementById('header-theme-toggle');
    if (btn) btn.setAttribute('aria-checked', light ? 'true' : 'false');
}
function setupThemeToggle() {
    // Apply the persisted theme on every boot, even if the toggle button is
    // missing (e.g. transient DOM), so the choice survives navigation.
    applyTheme(getTheme());
    var btn = document.getElementById('header-theme-toggle');
    if (!btn || btn.dataset.bound === '1') return;
    btn.dataset.bound = '1';
    btn.addEventListener('click', function() {
        var next = getTheme() === 'light' ? 'dark' : 'light';
        try { localStorage.setItem(THEME_STORAGE_KEY, next); } catch (e) {}
        applyTheme(next);
    });
}

// Story X: copy-on-click for #header-server-name (copies baseUri)
// and #header-baseuri. We also support keyboard activation
// (Enter / Space) for the role="button" baseUri chip.
function setupCopyOnClick() {
    function bindCopy(el, getValue) {
        if (!el || el.dataset.bound === '1') return;
        el.dataset.bound = '1';
        function copy() {
            var v = getValue();
            if (!v) return;
            var done = function() { showInlineNotification(i18n('header_copied', 'Copié')); };
            if (navigator.clipboard && navigator.clipboard.writeText) {
                navigator.clipboard.writeText(v).then(done).catch(function() {
                    fallbackCopy(v, done);
                });
            } else {
                fallbackCopy(v, done);
            }
        }
        el.addEventListener('click', copy);
        el.addEventListener('keydown', function(e) {
            if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); copy(); }
        });
    }
    function fallbackCopy(v, cb) {
        try {
            var ta = document.createElement('textarea');
            ta.value = v;
            ta.style.position = 'fixed';
            ta.style.opacity = '0';
            document.body.appendChild(ta);
            ta.focus(); ta.select();
            document.execCommand('copy');
            document.body.removeChild(ta);
            cb();
        } catch (e) {
            showInlineNotification(i18n('header_copy_failed', 'Copy failed'));
        }
    }
    bindCopy(document.getElementById('header-baseuri'),
             function() { return (document.getElementById('header-baseuri') || {}).textContent || ''; });
    bindCopy(document.getElementById('header-server-name'),
             function() { return (document.getElementById('header-baseuri') || {}).textContent || ''; });
}

// Story X: tilt cards — apply a 3D rotateX/rotateY based on the
// mouse position over the element. Magnitude is mode-dependent
// (Michel: ±1.5deg cards / ±6deg buttons, Power: same) per the
// validated mock-dashboard.html (lines 452-475) and EXPERIENCE.md
// (line 472-477). The tilt is subtle on cards (1.5deg) and more
// pronounced on buttons (6deg) because the user is explicitly
// targeting buttons. Honors prefers-reduced-motion.
//
// Story X.1: this was the second-most-visible bug after the
// dashboard-empty CSS bug — the function targeted .tilt-card (a
// class no element had) and the magnitude didn't match the mockup.
function setupTiltCards() {
    if (window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
        return; // honor the user's preference
    }
    // Story X.1: opt .btn in #app-main into the tiltable set so we
    // don't have to tag every <button> by hand in ui.go. The class
    // is the single opt-in signal consumed by the loop below.
    var appMainButtons = document.querySelectorAll('#app-main .btn');
    Array.prototype.forEach.call(appMainButtons, function(b) {
        if (!b.classList.contains('tiltable') && !b.closest('.tiltable')) b.classList.add('tiltable');
    });
    function tiltTowardCursor(el, maxTilt) {
        el.addEventListener('mousemove', function(e) {
            e.stopPropagation();
            var rect = el.getBoundingClientRect();
            var effectiveMaxTilt = maxTilt * Math.min(1, 200 / Math.max(rect.height, rect.width, 1));
            var centerX = rect.left + rect.width / 2;
            var centerY = rect.top + rect.height / 2;
            var relX = (e.clientX - centerX) / (rect.width / 2);
            var relY = (e.clientY - centerY) / (rect.height / 2);
            var rotateY = relX * effectiveMaxTilt;
            var rotateX = -relY * effectiveMaxTilt;
            el.style.transform = 'perspective(500px) rotateX(' + rotateX.toFixed(1) + 'deg) rotateY(' + rotateY.toFixed(1) + 'deg)';
        });
        el.addEventListener('mouseleave', function() {
            // Reset to the same perspective+identity matrix as
            // mousemove uses, so the card doesn't snap back through
            // a different transform path.
            el.style.transform = 'perspective(500px) rotateX(0deg) rotateY(0deg)';
        });
    }
    var tiltables = document.querySelectorAll('.tiltable');
    Array.prototype.forEach.call(tiltables, function(el) {
        var isBtn = el.tagName === 'BUTTON';
        tiltTowardCursor(el, isBtn ? 6 : 1.5);
    });
}

// Story X: populateHeader fetches /admin/api/admin/settings and
// fills the header chips (server name, baseUri, password
// plaintext, and a copy of the data in #header-archive-password).
// Called once on DOMContentLoaded.
function populateHeader() {
    fetch('/admin/api/admin/settings', {
        headers: { 'Accept': 'application/json' },
        credentials: 'same-origin'
    })
        .then(function(r) { if (!r.ok) throw new Error(r.status); return r.json(); })
        .then(function(data) {
            if (!data || !data.data) return;
            var d = data.data;
            var baseUri = d.base_uri;
            if (!baseUri && d.server && d.server.host && d.server.port) {
                baseUri = 'http://' + d.server.host + ':' + d.server.port + '/';
            }
            var nameEl = document.getElementById('header-server-name');
            var baseEl = document.getElementById('header-baseuri');
            var pwdEl  = document.getElementById('header-archive-password');
            // M-10 (review 2026-06-11): removed the read of
            // d.server_name — the field doesn't exist in the
            // /admin/api/admin/settings response. The header keeps
            // its HTML default placeholder "VRHub Server".
            if (baseEl && baseUri) baseEl.textContent = baseUri;
            // Story 9.8: populate the archive-password chip from the
            // dedicated archive_password field (not the admin password).
            if (pwdEl && d.archive_password) {
                pwdEl.dataset.masked = '••••••••';
                pwdEl.dataset.plain = d.archive_password;
                pwdEl.dataset.revealed = '0';
                pwdEl.textContent = pwdEl.dataset.masked;
            }
        })
        .catch(function() { /* no-op: header chips stay as defaults */ });
}

// DOM Elements
const updateBanner = document.getElementById('update-banner');
const updateVersion = document.getElementById('update-version');
const updateBtn = document.getElementById('update-btn');
const updateModal = document.getElementById('update-modal');
const updateModalMessage = document.getElementById('update-modal-message');
const updateProgress = document.getElementById('update-progress');
const restartPage = document.getElementById('restart-page');
const countdownSpan = document.getElementById('countdown');

// Update trigger guard — prevents double-clicks from sending duplicate POSTs.
let isTriggeringUpdate = false;

// Initialize
document.addEventListener('DOMContentLoaded', () => {
    // F7: admin.js is also loaded on the setup wizard page (it exposes the
    // __VRHUB_I18N__ global + design tokens that setup.js consumes). The
    // shell bootstrap below wires dashboard/header/mode-switch elements that
    // do not exist on the wizard, which previously threw "Cannot read
    // properties of null (reading 'addEventListener')" and could start
    // shell-only polling on the setup page. The wizard is driven entirely by
    // setup.js, so skip the shell bootstrap when the wizard overlay is
    // present. __VRHUB_I18N__ is assigned at top-level eval time, so it is
    // already available to setup.js regardless of this early return.
    if (document.getElementById('wizard-overlay')) {
        return;
    }
    // R6-B1/R6-L9: setMode uses opts.force to bypass the prev===mode dedupe on bootstrap,
    // so DOM is fully primed on first call (lang, classList, widgets, modechange dispatch).
    // Order: setMode first so widgets (Michel/Power loaders) run with correct classList; fetchUpdateStatus
    // is independent of widgets (Story 5-3 update flow runs in parallel) so order does not matter.
    var currentMode = getMode();
    setMode(currentMode, { force: true });
    // Story X: the page is now a SPA. We read the URL hash to know
    // which section to show. Default is 'dashboard' (the empty hash
    // is treated as the dashboard). Each section's render function
    // is called on hashchange; DOMContentLoaded fires the initial
    // render once.
    routeFromHash();
    window.addEventListener('hashchange', function() { routeFromHash(); });

    fetchUpdateStatus();
    setInterval(fetchUpdateStatus, 60000);
    setupEventListeners();
    wireRescanButtons();
    // Story X: setupFooterToggle replaced by setupModeSwitch (the
    // new bottom-left #mode-switch).
    setupModeSwitch();
    // Story ui-michel-power-toggle (B1): wire the "Passer en mode
    // Power User" CTA on the Michel dashboard. The defensive CSS at
    // admin.css:943 makes the section visible even when the route
    // is `dashboard`, so the listener must be attached at boot.
    setupPowerUserCta();
    setupLogoutButton();
    // Story X: new header behaviors. Each one is idempotent
    // (skips itself if the target element is missing).
    setupHeaderReveal();
    setupLangSelector();
    setupThemeToggle();
    setupCopyOnClick();
    setupTiltCards();
    populateHeader();

    // Story 9.5 (B5): the login form glue was removed from this
    // file. The login form is no longer embedded in the shell —
    // it lives on a dedicated /admin/login page that loads
    // login.js for the form-submit handler.
    // See internal/ui/embed/login.js.
    // Story 7.5 T3: renderStatsTable is invoked from handleRouteStats
    // ONLY (not at DOMContentLoaded) — the endpoint is power-only
    // and a blind call on initial load would 404 in Michel mode.
});

// Fetch update status from API
async function fetchUpdateStatus() {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), 5000);

    try {
        const response = await fetch('/admin/api/update/status', {
            signal: controller.signal,
            headers: { 'Accept': 'application/json' },
            credentials: 'same-origin'
        });
        clearTimeout(timeoutId);
        if (!response.ok) {
            return;
        }

        const data = await response.json();
        if (data.data) {
            updateStatus = {
                available: data.data.available || false,
                currentVersion: data.data.currentVersion || '',
                latestVersion: data.data.latestVersion || '',
                autoApply: data.data.autoApply || false,
                autoRestart: data.data.autoRestart || false,
                releaseNotes: data.data.releaseNotes || '',
                restartPending: data.data.restartPending || false,
                updateState: data.data.updateState || 'idle'
            };
            renderUpdateCard();
        }
    } catch (error) {
        clearTimeout(timeoutId);
    }
}

// Render update notification cards in Michel dashboard and Power #/updates section.
function renderUpdateCard() {
    var showCard = updateStatus.available || updateStatus.restartPending;

    // Legacy top banner — keep visible only when update available but not yet staged.
    if (updateBanner) {
        if (updateStatus.available && !updateStatus.restartPending) {
            if (updateVersion) updateVersion.textContent = 'v' + updateStatus.latestVersion;
            updateBanner.style.display = 'flex';
        } else {
            updateBanner.style.display = 'none';
        }
    }

    // Michel dashboard update card.
    var michelCard = document.getElementById('michel-update-card');
    if (michelCard) {
        if (showCard) {
            michelCard.classList.remove('hidden');
            _fillUpdateCard('michel', updateStatus);
        } else {
            michelCard.classList.add('hidden');
        }
    }

    // Power #/updates section update card.
    var powerCard = document.getElementById('power-update-card');
    if (powerCard) {
        if (showCard) {
            powerCard.classList.remove('hidden');
            _fillUpdateCard('power', updateStatus);
        } else {
            powerCard.classList.add('hidden');
        }
    }
}

// Populate a prefix-namespaced update card (michel- or power-).
function _fillUpdateCard(prefix, status) {
    var installedBadge = document.getElementById(prefix + '-installed-badge');
    var latestBadge = document.getElementById(prefix + '-latest-badge');
    var notes = document.getElementById(prefix + '-update-notes');
    var actions = document.getElementById(prefix + '-update-actions');
    var title = document.getElementById(prefix + '-update-card-title');

    if (installedBadge) installedBadge.textContent = status.currentVersion || '';
    if (latestBadge) latestBadge.textContent = status.latestVersion ? 'v' + status.latestVersion : '';
    if (notes) notes.innerHTML = renderMarkdown(status.releaseNotes || '');

    if (!actions) return;
    actions.innerHTML = '';

    if (status.restartPending) {
        if (title) title.textContent = i18n('update_staged_title', 'Redémarrage requis');
        var restartBtn = document.createElement('button');
        restartBtn.type = 'button';
        restartBtn.className = 'btn btn-primary';
        restartBtn.textContent = i18n('restart_now_btn', 'Redémarrer maintenant');
        restartBtn.addEventListener('click', function() { triggerRestart(false); });
        actions.appendChild(restartBtn);

        var restartAutoBtn = document.createElement('button');
        restartAutoBtn.type = 'button';
        restartAutoBtn.className = 'btn btn-secondary';
        restartAutoBtn.textContent = i18n('restart_no_ask_btn', 'Redémarrer et ne plus demander');
        restartAutoBtn.addEventListener('click', function() { triggerRestart(true); });
        actions.appendChild(restartAutoBtn);
    } else if (status.available) {
        if (title) title.textContent = i18n('update_available_title', 'Mise à jour disponible');
        var updateNowBtn = document.createElement('button');
        updateNowBtn.type = 'button';
        updateNowBtn.className = 'btn btn-primary';
        updateNowBtn.textContent = i18n('update_now_btn', 'Mettre à jour');
        updateNowBtn.addEventListener('click', function() { triggerUpdate(); });
        actions.appendChild(updateNowBtn);
    }
}

// Trigger a server restart. If setAutoRestart=true, first PUT settings
// to enable auto_restart, then POST /admin/api/update/restart.
async function triggerRestart(setAutoRestart) {
    if (setAutoRestart) {
        try {
            await fetch('/admin/api/admin/settings', {
                method: 'PUT',
                credentials: 'same-origin',
                headers: {
                    'Content-Type': 'application/json',
                    'X-CSRF-Token': getCSRFToken()
                },
                body: JSON.stringify({ update: { auto_restart: true, auto_apply: updateStatus.autoApply } })
            });
        } catch(e) { /* best-effort */ }
    }

    try {
        await fetch('/admin/api/update/restart', {
            method: 'POST',
            credentials: 'same-origin',
            headers: {
                'Accept': 'application/json',
                'X-CSRF-Token': getCSRFToken()
            }
        });
    } catch(e) { /* server went down — expected */ }

    if (restartPage) restartPage.style.display = 'flex';
    var countdown = 10;
    var countdownTimer = setInterval(function() {
        countdown--;
        if (countdownSpan) countdownSpan.textContent = countdown;
        if (countdown <= 0) {
            clearInterval(countdownTimer);
            location.reload();
        }
    }, 1000);
}

// Story 1.8 follow-up (live session 2026-06-08): logout button.
// POST /admin/api/auth/logout with the per-session CSRF token
// (rendered into a <meta name="csrf-token"> by the server at page
// load). On success the server clears the session cookie via
// Set-Cookie and returns 204; we then reload to /admin/login
// where the login form is shown.
//
// Why this works now: previously the button sent a placeholder
// X-CSRF-Token (the server returned 403), and the JS fallback tried
// to delete the HttpOnly session cookie via document.cookie (which
// is impossible — HttpOnly cookies are invisible to JS). The user
// was stuck. The proper fix is to render the real token in the HTML.
function getCSRFToken() {
    var meta = document.querySelector('meta[name="csrf-token"]');
    return meta ? (meta.getAttribute('content') || '') : '';
}

function setupLogoutButton() {
    // Story X: logout now lives in the unified header (visible in
    // BOTH Michel and Power modes). The legacy #logout-btn in the
    // Power sidebar is gone, replaced by #header-logout-btn. The
    // handler logic is unchanged.
    var btn = document.getElementById('header-logout-btn');
    if (!btn) return;
    if (btn.dataset.bound === '1') return;
    btn.dataset.bound = '1';
    btn.addEventListener('click', function(e) {
        e.preventDefault();
        var token = getCSRFToken();
        fetch('/admin/api/auth/logout', {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'X-CSRF-Token': token }
        }).then(function(r) {
            if (r.ok || r.status === 204 || r.status === 403) {
                window.location.assign('/admin/login');
                return;
            }
            window.location.assign('/admin/login');
        }).catch(function() {
            window.location.assign('/admin/login');
        });
    });
}

// Setup event listeners
function setupEventListeners() {
    // Update button click handler
    updateBtn.addEventListener('click', (e) => {
        e.preventDefault();
        e.stopPropagation();
        triggerUpdate();
    });

    // Banner click handler
    updateBanner.addEventListener('click', () => {
        if (!updateStatus.autoApply && !isTriggeringUpdate) {
            triggerUpdate();
        }
    });
}

// Trigger update flow
async function triggerUpdate() {
    // Client-side double-click guard
    if (isTriggeringUpdate) {
        return;
    }
    isTriggeringUpdate = true;

    // Show confirmation modal
    updateModal.style.display = 'flex';
    // R7-M4: Route through i18n() so the modal text is locale-aware (English default; can be French in Michel mode)
    updateModalMessage.textContent = i18n('update_downloading', 'Downloading and restarting... Do not close this window');
    updateProgress.style.width = '0%';

    try {
        const controller = new AbortController();
        const timeoutId = setTimeout(() => controller.abort(), 10000);

        // Call update apply API
        // B-04 (review 2026-06-11): the update endpoint is wrapped in
        // the CSRF middleware. Without the X-CSRF-Token header the
        // request 403s and the "Update Now" button is dead. Default
        // credentials are 'same-origin' so the session cookie is
        // included automatically.
        const response = await fetch('/admin/api/update/apply', {
            method: 'POST',
            credentials: 'same-origin',
            headers: {
                'Accept': 'application/json',
                'Content-Type': 'application/json',
                'X-CSRF-Token': getCSRFToken()
            },
            signal: controller.signal
        });

        clearTimeout(timeoutId);

        if (!response.ok) {
            throw new Error('Update failed: ' + response.status);
        }

        const data = await response.json();
        // R7-M4: i18n for the fallback message
        updateModalMessage.textContent = data.data?.message || i18n('update_started', 'Update started. Server will restart shortly.');

        // Wait a cooldown period before polling to avoid false-positive detection.
        // The goroutine needs time to start and for the server to become unavailable.
        await new Promise(resolve => setTimeout(resolve, 15000));

        // Poll for server availability to detect when update completes
        let attempts = 0;
        const maxAttempts = 60; // 10 minutes max (10s intervals)

        const pollInterval = setInterval(async () => {
            attempts++;
            try {
                const pollController = new AbortController();
                const pollTimeout = setTimeout(() => pollController.abort(), 3000);

                const pollResponse = await fetch('/admin/api/update/status', {
                    signal: pollController.signal,
                    headers: { 'Accept': 'application/json' },
                    credentials: 'same-origin'
                });
                clearTimeout(pollTimeout);

                if (pollResponse.ok) {
                    const pollData = await pollResponse.json();
                    if (pollData.data && pollData.data.updateState === 'running') {
                        // Update is still in progress on the restarted server; keep waiting.
                        updateProgress.style.width = Math.min(90, (attempts / maxAttempts) * 90) + '%';

                        // Check max attempts for running state to prevent infinite loop
                        if (attempts >= maxAttempts) {
                            clearInterval(pollInterval);
                            showRestartPage();
                        }
                        return;
                    }
                    if (pollData.data && pollData.data.updateState === 'failed') {
                        // Update failed — show error instead of restart page.
                        clearInterval(pollInterval);
                        updateModal.style.display = 'none';
                        isTriggeringUpdate = false;
                        // R7-M4: i18n for the error message
                        showInlineNotification(i18n('update_failed_state', 'Update failed: the server reported a failure state. Please check logs or try again.'));
                        return;
                    }
                }

                // Server responded and state is not "running" or "failed" — update completed.
                clearInterval(pollInterval);
                showRestartPage();

            } catch (e) {
                // Server unavailable — update is in progress.
                updateProgress.style.width = Math.min(90, (attempts / maxAttempts) * 90) + '%';

                if (attempts >= maxAttempts) {
                    clearInterval(pollInterval);
                    showRestartPage();
                }
            }
        }, 10000);

    } catch (error) {
        updateModal.style.display = 'none';
        // R7-M4: i18n for the catch-block error message (generic prefix only — actual error from exception)
        showInlineNotification(i18n('update_failed_generic', 'Update failed: ') + error.message);
        isTriggeringUpdate = false;
    }
}

// Show the restart page with countdown.
function showRestartPage() {
    updateModal.style.display = 'none';
    restartPage.style.display = 'flex';
    isTriggeringUpdate = false;

    let count = 5;
    countdownSpan.textContent = count;
    const countdownInterval = setInterval(() => {
        count--;
        countdownSpan.textContent = count;
        if (count <= 0) {
            clearInterval(countdownInterval);
            location.reload();
        }
    }, 1000);
}

// Footer mode toggle (Subtask 5.x)
//
// Story X: the .mode-footer and #mode-toggle elements are GONE.
// The mode toggle is now the bottom-left segmented control
// #mode-switch with two segments: #mode-switch-michel and
// #mode-switch-power. Both are visible in BOTH modes (Michel and
// Power users can switch back and forth from the same UI).
//
// setupModeSwitch binds a single click handler that flips the
// mode and updates the aria-pressed state. The segments are
// also kept in sync via the modechange listener (which calls
// updateModeSwitchSegments).
function setupModeSwitch() {
    function handleModeSwitch() {
        var currentMode = getMode();
        var newMode = currentMode === 'michel' ? 'power' : 'michel';
        setMode(newMode);
        // M2: Preserve other query params when clearing mode
        var params = new URLSearchParams(location.search);
        params.delete('mode');
        var qs = params.toString();
        history.replaceState({}, '', location.pathname + (qs ? '?' + qs : '') + location.hash);
    }

    var segMichel = document.getElementById('mode-switch-michel');
    var segPower  = document.getElementById('mode-switch-power');
    if (segMichel) segMichel.addEventListener('click', handleModeSwitch);
    if (segPower)  segPower.addEventListener('click', handleModeSwitch);

    // Initial aria-pressed state (in case setMode already ran).
    updateModeSwitchSegments();
}

// H3: modechange listener registered unconditionally.
// Story X: instead of updateFooterLabel (footer is gone), the
// listener now updates the aria-pressed state of the #mode-switch
// segments and re-runs the route guard (Michel can't be on a
// Power-only route). Also re-translates the page so labels in
// the new mode render with the new mode's locale.
document.addEventListener('modechange', function(e) {
    translatePage(e.detail.to);
    updateModeSwitchSegments();
    // If we just switched to Michel and we're on a Power-only
    // route, redirect to the dashboard placeholder.
    var r = (location.hash || '').replace(/^#\//, '').trim();
    // M-03 (review 2026-06-11): 'configuration' was a power-only
    // route, contradicting the HTML banner #settings-readonly-banner
    // that shows in Michel mode. Removed: configuration is now
    // accessible in Michel as a read-only view (the form disables
    // its inputs and hides the Save button when getMode() === 'michel').
    var powerOnly = (r === 'games' || r === 'api-docs' || r === 'monitoring' || r === 'backup' || r === 'stats');
    if (e.detail.to === 'michel' && powerOnly) {
        routeTo('power-required');
    } else {
        // Refresh the current route so visible widgets re-fetch.
        routeFromHash({ skipHashUpdate: true });
    }
});

// Story X: keep #mode-switch segments in sync with the current
// mode. aria-pressed="true" indicates the active mode. Idempotent.
function updateModeSwitchSegments() {
    var mode = getMode();
    var segMichel = document.getElementById('mode-switch-michel');
    var segPower  = document.getElementById('mode-switch-power');
    if (segMichel) segMichel.setAttribute('aria-pressed', mode === 'michel' ? 'true' : 'false');
    if (segPower)  segPower.setAttribute('aria-pressed',  mode === 'power'  ? 'true' : 'false');
}

// ============================================================
// Inline notification helper (M12 — replaces alert())
// ============================================================
function showInlineNotification(message) {
    var existing = document.getElementById('inline-notification');
    if (existing) existing.remove();
    var notif = document.createElement('div');
    notif.id = 'inline-notification';
    notif.className = 'inline-notification';
    notif.textContent = message;
    // M6: Compute top from banner visibility.
    // Story X: sidebar is GONE, so the left offset is just 50% in
    // both modes. The header (height ~56px) sits above the
    // notification so the top starts at 60px to clear it (and the
    // banner, if visible).
    var top = 60;
    var banner = document.getElementById('update-banner');
    if (banner && banner.style.display !== 'none') {
        top += banner.offsetHeight + 8;
    }
    notif.style.cssText = 'position:fixed;top:' + top + 'px;left:50%;transform:translateX(-50%);background:var(--surface);border:1px solid var(--border);border-radius:var(--radius-md);padding:var(--space-3) var(--space-4);z-index:9999;box-shadow:0 2px 8px rgba(0,0,0,0.3);font-size:0.875rem;color:var(--text);';
    document.body.appendChild(notif);
    setTimeout(function() { if (notif.parentNode) notif.remove(); }, 4000);
}

// ============================================================
// i18n helpers (Subtask 3.1)
// ============================================================
var I18N_MICHEL = {
    'app_title': 'VRHub Server',
    'nav_dashboard': 'Tableau de bord',
    'dashboard_title': 'Tableau de bord',
    'welcome_message': "Bienvenue dans l'administration de VRHub Server",
    'server_status_label': 'État du serveur',
    'status_checking': 'Vérification...',
    'config_title': 'Configuration',
    // R7-M2: stats_title removed — Michel mode has no stats card (Power-only)
    'base_uri_label': 'URI de base',
    'password_label': 'Mot de passe',
    'show_password': 'Afficher',
    'hide_password': 'Masquer',
    'game_count_label': 'Jeux détectés',
    'current_version': 'Version actuelle',
    'launch_server': 'Démarrer le serveur',
    'stop_server': 'Arrêter le serveur',
    'footer_power': 'Mode Power User',
    'footer_michel': 'Mode Michel',
    // R6-H4: AC1 example placeholder (static text until API exposes last_scan)
    'last_scan_placeholder': "Scanné à l'instant",
    'server_status_running': 'Serveur en marche',
    'server_status_stopped': 'Arrêté',
    'header_switch_power': 'Passer en mode Power User',
    // R7-M4: Update flow strings (Story 5-3 update polling)
    'update_downloading': 'Téléchargement et redémarrage... Ne fermez pas cette fenêtre',
    'update_started': 'Mise à jour démarrée. Le serveur va redémarrer sous peu.',
    'update_failed_state': 'Échec de la mise à jour : le serveur a signalé un état d\'erreur. Consultez les logs ou réessayez.',
    'update_failed_generic': 'Échec de la mise à jour : ',
    // Story 6-2: Login form strings (Michel mode — French)
    'login_title': 'Connexion',
    'login_username': 'Identifiant',
    'login_password': 'Mot de passe',
    'login_submit': 'Se connecter',
    'login_error_invalid': 'Identifiant ou mot de passe incorrect',
    // R10-LOGIN-EMPTY-ERROR: distinct i18n key for empty-field validation.
    'login_error_empty': 'Veuillez remplir tous les champs',
    // R10-LOGIN-CATCH-ERRORS: distinct i18n key for server errors (5xx).
    'login_error_server': 'Erreur serveur — veuillez réessayer plus tard',
    'logout_button': 'Déconnexion',
    // R13-P15: settings form (Story 6-3 Power User mode)
    'settings_title': 'Paramètres',
    'settings_loading': 'Chargement des paramètres…',
    // Story 6.6 Task 3.5: API documentation page (admin shell column headers
    // and labels; the standalone /admin/docs page is self-contained English
    // HTML, but these keys are wired here so the admin shell navigation
    // that points to /admin/docs can be translated).
    'docs_title': 'Documentation API',
    'docs_intro': "Liste de tous les endpoints de l'API admin de VRHub Server. Cliquez sur un endpoint pour voir les détails et un exemple curl.",
    'docs_method': 'Méthode',
    'docs_path': 'Chemin',
    'docs_auth': 'Auth',
    'docs_description': 'Description',
    'docs_request': 'Requête',
    'docs_response': 'Réponse',
    'docs_example_curl': 'Exemple curl',
    'docs_copy': 'Copier',
    // Story 7.5 T3: usage statistics page (Michel mode — French, used as
    // fallback by translatePage; the /admin/stats page itself returns
    // 404 in Michel mode so the JS render never fires, but the static
    // page title/intro are translated via data-i18n attrs first).
    'stats_intro': "Nombre de téléchargements, bande passante et dernier téléchargement par jeu, triés par nombre de téléchargements.",
    'stats_loading': 'Chargement…',
    'stats_no_data': 'Aucune donnée de téléchargement.',
    'stats_col_game': 'Jeu',
    'stats_col_package': 'Paquet',
    'stats_col_count': 'Téléchargements',
    'stats_col_last': 'Dernier téléchargement',
    'stats_col_bandwidth': 'Bande passante',
    'stats_col_size': 'Taille totale',
    'stats_never': 'jamais',
    'stats_failed': 'Échec du chargement des statistiques.',
    // Story 7.6: network status badge (Michel + Power). The
    // title uses a {status} placeholder filled by JS at poll
    // time (the four possible values are listed below).
    'network_status_title': "État du réseau : {status}",
    'network_status_checking': 'Vérification…',
    'network_status_label_ok': 'OK',
    'network_status_label_degraded': 'dégradé',
    'network_status_label_offline': 'hors ligne',
    'network_status_label_unknown': 'inconnu',
    'network_status_label_error': 'erreur de récupération',
    // Story 1.6: Setup wizard i18n keys (Michel-only — UX spec says
    // Michel-first; the wizard is a one-time flow with no per-user
    // mode preference). All keys consumed by internal/ui/embed/setup.js.
    'setup_step1_title': "Créer les identifiants administrateur",
    'setup_step1_subtitle': "Définissez le nom d'utilisateur et le mot de passe pour accéder à l'administration.",
    'setup_step1_username': "Nom d'utilisateur",
    'setup_step1_password': 'Mot de passe',
    'setup_step1_submit': 'Continuer',
    'setup_step2_title': 'Choisir le dossier de jeux',
    'setup_step2_subtitle': 'Indiquez le dossier contenant vos jeux (APK + OBB).',
    'setup_step2_placeholder': 'C:\\Users\\...\\Games',
    'setup_step2_scan': 'Scanner le dossier',
    'setup_step2_scanning': 'Scan en cours…',
    'setup_step3_title': 'Sélectionner les jeux',
    'setup_step3_subtitle': 'Cochez les jeux à rendre accessibles aux clients VR. Décochez pour exclure.',
    'setup_step3_empty': 'Aucun jeu détecté.',
    'setup_step3_continue': 'Continuer',
    'setup_step3_corrupted': 'Corrompu',
    'setup_step4_title': 'Lancer le serveur',
    'setup_step4_subtitle': 'Vérifiez les informations ci-dessous puis lancez le serveur.',
    'setup_step4_done_title': 'Serveur prêt !',
    'setup_step4_base_uri': 'Base URI',
    'setup_step4_password': 'Mot de passe VRHub',
    'setup_step4_instructions': 'Instructions :',
    'setup_step4_open_admin': "Ouvrir l'admin",
    'setup_error_required': 'Ce champ est requis.',
    'setup_error_short_password': 'Le mot de passe doit contenir au moins 4 caractères.',
    'setup_error_invalid_folder': "Le dossier n'existe pas ou n'est pas accessible.",
    'setup_error_scan_failed': 'Échec du scan du dossier.',
    'setup_error_timeout': 'La requête a expiré. Veuillez réessayer.',
    'setup_error_server': 'Erreur serveur. Veuillez réessayer.',
    'setup_back': 'Retour',
    'setup_next': 'Suivant',
    'setup_noscript': "JavaScript est requis pour utiliser l'assistant de configuration. Vous pouvez aussi configurer manuellement via les endpoints /admin/api/setup/* en utilisant curl.",
    // Story X: SPA shell — in-page nav links, section titles,
    // header chip tooltips, lang selector.
    'nav_games': 'Jeux',
    'nav_configuration': 'Configuration',
    'nav_api_docs': 'Documentation API',
    'nav_monitoring': 'Monitoring',
    'nav_backup': 'Sauvegarde',
    'nav_stats': 'Statistiques',
    'nav_cat_status': 'État',
    'nav_cat_content': 'Contenu',
    'nav_cat_configuration': 'Configuration',
    'nav_cat_reference': 'Référence',
    'nav_cat_observability': 'Observabilité',
    'nav_cat_maintenance': 'Maintenance',
    'games_title': 'Jeux',
    'games_intro': "Liste des jeux détectés. Cochez « Exposed » pour les inclure dans le meta.7z public.",
    'games_empty': 'Aucun jeu détecté.',
    'backup_title': 'Sauvegarde',
    'backup_intro': "Téléchargez une archive ZIP de votre configuration et de votre base de données, ou restaurez depuis une archive existante.",
    'backup_download': 'Télécharger la sauvegarde',
    'backup_restore_title': 'Restaurer',
    'backup_restore_submit': 'Restaurer',
    'monitoring_title': 'Monitoring en direct',
    'monitoring_intro': "Flux temps réel des événements du serveur.",
    'monitoring_connecting': 'Connexion au flux…',
    'docs_loading': 'Chargement…',
    // M-14 (review 2026-06-11): 'docs_intro' was defined twice with
    // different copy. The second ("Les onglets cassés...") was an
    // in-progress note that leaked to production. Use the first,
    // polished copy.
    'stats_title': 'Statistiques d\'utilisation',
    // M-14: removed "triés par nombre de téléchargements" — the JS
    // doesn't actually sort. Use server-side ordering copy instead.
    'stats_intro': "Nombre de téléchargements, bande passante et dernier téléchargement par jeu. Ordre : serveur (par téléchargements décroissants).",
    'power_required_title': 'Section réservée au mode Power User',
    'power_required_body': "Cette section n'est accessible qu'en mode Power User. Cliquez sur le bouton ci-dessous pour basculer.",
    'mode_label_michel': 'Michel',
    'mode_label_power': 'Power',
    'header_server_name_title': 'Cliquer pour copier la baseUri',
    'header_baseuri_title': 'Cliquer pour copier',
    'header_reveal_title': 'Afficher le mot de passe',
    'header_lang_title': "Langue de l'interface",
    'copy_btn': 'Copier',
    'header_copied': 'Copié',
    'header_copy_failed': 'Échec de la copie',
    'settings_readonly_michel': "Les paramètres sont en lecture seule en mode Michel. Passez en mode Power User pour les modifier.",
    'rescan_btn': 'Rescanner',
    'rescan_in_progress': 'Scan en cours…',
    'rescan_done': 'Scan terminé',
    'rescan_failed': 'Échec du scan',
    'rescan_files_scanned': '{{n}} fichiers scannés',
    'rescan_games_added': '{{n}} nouveaux jeux',
    // M-12 (review 2026-06-11): "Rescan" → "Rescanner" en FR.
    // Configuration form labels (B-05 — review 2026-06-11)
    'config_listen_address': "Adresse d'écoute",
    'config_listen_address_help': "Adresse IP ou nom d'hôte. Mettre 0.0.0.0 pour écouter sur toutes les interfaces (LAN).",
    'config_port': 'Port',
    'config_port_help': 'Port TCP du serveur HTTP. Re-bind automatique à la sauvegarde.',
    'config_archive_password': "Mot de passe de l'archive",
    'config_archive_password_help': "Chiffre meta.7z en AES-256. À copier dans le client VRHub. Min. 8 caractères.",
    'config_game_folders': 'Dossiers de jeux',
    'config_game_folders_help': "Chemins absolus vers les dossiers contenant les fichiers APK/OBB. Cliquer 'Rescanner' après modification.",
    'config_add_folder': 'Ajouter un dossier',
    'config_no_folders': 'Aucun dossier configuré.',
    'config_no_folders_fallback': 'Répertoire de données utilisé par défaut :',
    'config_auto_apply': 'Appliquer auto les MAJ',
    'config_auto_apply_help': 'Applique automatiquement les nouvelles versions dès détection.',
    'config_auto_restart': 'Redémarrage automatique',
    'config_auto_restart_help': 'Redémarre automatiquement après la mise à jour. Par défaut, un redémarrage explicite est demandé.',
    'update_available_title': 'Mise à jour disponible',
    'update_staged_title': 'Redémarrage requis',
    'update_now_btn': 'Mettre à jour',
    'restart_now_btn': 'Redémarrer maintenant',
    'restart_no_ask_btn': 'Redémarrer et ne plus demander',
    'changelog_title': 'Historique des versions',
    'changelog_loading': 'Chargement…',
    'changelog_error': 'Impossible de charger le changelog.',
    'changelog_empty': 'Aucune release trouvée.',
    'update_latest_badge': 'dernier',
    'update_installed_badge': 'installé',
    'nav_updates': 'Mises à jour',
    'updates_title': 'Mises à jour & Changelog',
    'config_metadata_section': 'Métadonnées',
    'config_metadata_url': 'URL des métadonnées',
    'config_metadata_url_help': "Source du catalogue MetaMetadata. Doit être une URL HTTPS accessible.",
    'config_metadata_refresh_interval': 'Intervalle de rafraîchissement',
    'config_metadata_refresh_interval_help': "Durée entre deux rafraîchissements (ex: '24h', '30m').",
    'config_update_section': 'Mises à jour avancées',
    'config_update_check_interval': "Intervalle de vérification",
    'config_update_check_interval_help': "Durée entre deux checks GitHub (ex: '6h', '1h').",
    'config_update_github_token': 'Token GitHub',
    'config_update_github_token_help': "Optionnel. Pour augmenter la limite de l'API GitHub (60/h → 5000/h).",
    'config_update_owner': 'Propriétaire',
    'config_update_repo': 'Dépôt',
    'config_save': 'Sauvegarder',
    'config_saved': 'Paramètres sauvegardés',
    'config_save_failed': 'Échec de la sauvegarde',
    'config_reset_default': 'Réinitialiser',
    'config_admin_password_change': 'Changer le mot de passe administrateur',
    'config_old_password': 'Ancien mot de passe',
    'config_new_password': 'Nouveau mot de passe',
    'config_confirm_password': 'Confirmer le mot de passe',
    'config_password_required': 'Veuillez remplir tous les champs',
    'config_password_mismatch': 'Les mots de passe ne correspondent pas',
    'config_password_too_short': 'Le mot de passe doit contenir au moins 4 caractères',
    'config_password_changed': 'Mot de passe changé avec succès',
    'config_password_change_failed': 'Échec du changement',
    'config_archive_password_too_short': "Le mot de passe archive doit contenir au moins 8 caractères",
    'setup_show_password': 'Afficher',
    'setup_hide_password': 'Masquer',
    // Games table column headers
    'games_col_package': 'Paquet',
    'games_col_name': 'Nom',
    'games_col_size': 'Taille',
    'games_col_exposed': 'Exposé',
    'games_col_updated': 'Dernière mise à jour',
    // Client Setup page (M-04)
    'nav_client_setup': 'Connexion client',
    'client_setup_title': 'Connecter un client VRHub',
    'client_setup_intro': "Voici les informations nécessaires pour connecter votre Meta Quest à ce serveur.",
    'client_setup_loading': 'Chargement…',
    'client_setup_recommended': 'Méthode recommandée (recommandée)',
    'client_setup_url_label': "URL de configuration automatique",
    'client_setup_url_help': "Dans l'app VRHub, ouvrez Paramètres → Serveur et collez cette URL. Le mot de passe sera ajouté automatiquement.",
    'client_setup_manual': 'Méthode manuelle',
    'client_setup_base_uri': 'Adresse du serveur',
    'client_setup_password': 'Mot de passe de l’archive',
    'client_setup_test': 'Tester la connexion',
    'client_setup_step1': '1. Sur votre Quest, ouvrez l’app VRHub',
    'client_setup_step2': '2. Allez dans Paramètres → Serveur',
    'client_setup_step3': '3. Collez l’URL de configuration OU l’adresse du serveur et le mot de passe',
    'client_setup_step4': '4. Validez. Le catalogue de jeux apparaît.',
    'client_setup_error': 'Impossible de charger la configuration.',
    'michel_client_title': 'Connecter un client',
    // API key management (M-02)
    'api_key_title': 'Clé API admin',
    'api_key_help': "Cette clé permet à vos scripts d'accéder aux endpoints protégés par X-API-Key. Elle n'est plus affichée après régénération.",
    'api_key_reveal': 'Afficher la clé',
    'api_key_regenerate': 'Régénérer',
    'api_key_no_plaintext': 'Clé non disponible. Régénérez pour en obtenir une nouvelle.',
    'api_key_copied': 'Clé copiée',
    'api_key_regen_confirm': "Régénérer la clé API ? L'ancienne clé sera immédiatement invalidée.",
    'api_key_save_warning': "Copiez cette clé MAINTENANT. Elle ne sera plus jamais affichée.",
};

var I18N_POWER = {
    'app_title': 'VRHub Server',
    'nav_dashboard': 'Dashboard',
    'dashboard_title': 'Dashboard',
    'welcome_message': 'Welcome to VRHub Server Admin — Power User mode',
    'server_status_label': 'Server Status',
    'status_checking': 'Checking...',
    'config_title': 'Configuration Overview',
    'stats_title': 'Quick Stats',
    'base_uri_label': 'Base URI',
    'password_label': 'Password',
    'show_password': 'Show',
    'hide_password': 'Hide',
    // R7-M2: game_count_label removed — Power mode doesn't have a game count card
    'current_version': 'Current version',
    'launch_server': 'Start Server',
    'stop_server': 'Stop Server',
    'server_status_running': 'Server running',
    'server_status_stopped': 'Stopped',
    // R7-M2: last_scan_placeholder removed — Power mode doesn't show last scan (Michel-only placeholder)
    'footer_power': 'Mode Power User',
    'footer_michel': 'Michel Mode',
    'header_switch_power': 'Mode Power User',
    // R7-M4: Update flow strings (English defaults — same as before for Power users)
    'update_downloading': 'Downloading and restarting... Do not close this window',
    'update_started': 'Update started. Server will restart shortly.',
    'update_failed_state': 'Update failed: the server reported a failure state. Please check logs or try again.',
    'update_failed_generic': 'Update failed: ',
    // Story 6-2: Login form strings (Power User mode — English)
    'login_title': 'Login',
    'login_username': 'Username',
    'login_password': 'Password',
    'login_submit': 'Sign In',
    'login_error_invalid': 'Invalid credentials',
    // R10-LOGIN-EMPTY-ERROR: distinct i18n key for empty-field validation.
    'login_error_empty': 'Please fill in all fields',
    // R10-LOGIN-CATCH-ERRORS: distinct i18n key for server errors (5xx).
    'login_error_server': 'Server error — please try again later',
    'logout_button': 'Logout',
    // R13-P15: settings form (Story 6-3 Power User mode)
    'settings_title': 'Settings',
    'settings_loading': 'Loading settings…',
    // Story 6.6 Task 3.5: API documentation page (English — Power User default).
    'docs_title': 'API Documentation',
    'docs_intro': 'List of all VRHub Server admin API endpoints. Click an endpoint to see details and a copy-able curl example.',
    'docs_method': 'Method',
    'docs_path': 'Path',
    'docs_auth': 'Auth',
    'docs_description': 'Description',
    'docs_request': 'Request',
    'docs_response': 'Response',
    'docs_example_curl': 'Example curl',
    'docs_copy': 'Copy',
    // Story 7.5 T3: usage statistics page (Power User mode — English).
    // Michel mode never reaches this page (server returns 404), but the
    // keys live in both dicts so the static title/intro render even
    // before the JS does the fetch.
    'stats_intro': 'Per-game download counts, bandwidth, and last-seen timestamp. Sorted by download count.',
    'stats_loading': 'Loading…',
    'stats_no_data': 'No download data yet.',
    'stats_col_game': 'Game',
    'stats_col_package': 'Package',
    'stats_col_count': 'Downloads',
    'stats_col_last': 'Last Download',
    'stats_col_bandwidth': 'Bandwidth',
    'stats_col_size': 'Total Size',
    'stats_never': 'never',
    'stats_failed': 'Failed to load statistics.',
    // Story 7.6: network status badge (English, Power User mode).
    'network_status_title': 'Network status: {status}',
    'network_status_checking': 'Checking…',
    'network_status_label_ok': 'OK',
    'network_status_label_degraded': 'degraded',
    'network_status_label_offline': 'offline',
    'network_status_label_unknown': 'unknown',
    'network_status_label_error': 'fetch error',
    // Story X: SPA shell — in-page nav links, section titles,
    // header chip tooltips, lang selector. (English, Power mode.)
    'nav_games': 'Games',
    'nav_configuration': 'Configuration',
    'nav_api_docs': 'API Docs',
    'nav_monitoring': 'Monitoring',
    'nav_backup': 'Backup',
    'nav_stats': 'Statistics',
    'nav_cat_status': 'Status',
    'nav_cat_content': 'Content',
    'nav_cat_configuration': 'Configuration',
    'nav_cat_reference': 'Reference',
    'nav_cat_observability': 'Observability',
    'nav_cat_maintenance': 'Maintenance',
    'games_title': 'Games',
    'games_intro': 'List of detected games. Tick "Exposed" to include in the public meta.7z.',
    'games_empty': 'No games detected.',
    'backup_title': 'Backup',
    'backup_intro': 'Download a ZIP archive of your configuration and database, or restore from an existing archive.',
    'backup_download': 'Download backup',
    'backup_restore_title': 'Restore',
    'backup_restore_submit': 'Restore',
    'monitoring_title': 'Live Monitoring',
    'monitoring_intro': 'Real-time stream of server events.',
    'monitoring_connecting': 'Connecting to feed…',
    'docs_loading': 'Loading…',
    // M-14: duplicate `docs_intro` was an in-progress note that leaked
    // to production. Removed (the first definition at line ~1995 wins).
    'stats_title': 'Usage Statistics',
    // M-14: removed "Sorted by download count" — JS doesn't actually
    // sort. Use server-side ordering copy instead.
    'stats_intro': 'Per-game download counts, bandwidth, and last-seen timestamp. Server-side order (most-downloaded first).',
    'power_required_title': 'Power User section required',
    'power_required_body': 'This section is only available in Power User mode. Click the button below to switch.',
    'mode_label_michel': 'Michel',
    'mode_label_power': 'Power',
    'header_server_name_title': 'Click to copy baseUri',
    'header_baseuri_title': 'Click to copy',
    'header_reveal_title': 'Show password',
    'header_lang_title': 'Interface language',
    'copy_btn': 'Copy',
    'header_copied': 'Copied',
    'header_copy_failed': 'Copy failed',
    'settings_readonly_michel': 'Settings are read-only in Michel mode. Switch to Power User to edit them.',
    'rescan_btn': 'Rescan',
    'rescan_in_progress': 'Scan in progress…',
    'rescan_done': 'Scan complete',
    'rescan_failed': 'Scan failed',
    'rescan_files_scanned': '{{n}} files scanned',
    'rescan_games_added': '{{n}} new games',
    // Configuration form labels (B-05 — review 2026-06-11)
    'config_listen_address': 'Listen address',
    'config_listen_address_help': 'IP address or hostname. Use 0.0.0.0 to listen on all interfaces (LAN).',
    'config_port': 'Port',
    'config_port_help': 'TCP port for the HTTP server. Auto re-bind on save.',
    'config_archive_password': 'Archive password',
    'config_archive_password_help': 'Encrypts meta.7z with AES-256. Paste this in the VRHub client. Min. 8 chars.',
    'config_game_folders': 'Game folders',
    'config_game_folders_help': "Absolute paths to directories containing APK/OBB files. Click 'Rescan' after editing.",
    'config_add_folder': 'Add folder',
    'config_no_folders': 'No folder configured.',
    'config_no_folders_fallback': 'Data directory used as fallback:',
    'config_auto_apply': 'Auto-apply updates',
    'config_auto_apply_help': 'Automatically apply new versions as soon as they are detected.',
    'config_auto_restart': 'Auto-restart',
    'config_auto_restart_help': 'Automatically restart after applying an update. By default, an explicit restart is required.',
    'update_available_title': 'Update Available',
    'update_staged_title': 'Restart Required',
    'update_now_btn': 'Update Now',
    'restart_now_btn': 'Restart Now',
    'restart_no_ask_btn': 'Restart & Never Ask Again',
    'changelog_title': 'Release History',
    'changelog_loading': 'Loading…',
    'changelog_error': 'Failed to load changelog.',
    'changelog_empty': 'No releases found.',
    'update_latest_badge': 'latest',
    'update_installed_badge': 'installed',
    'nav_updates': 'Updates',
    'updates_title': 'Updates & Changelog',
    'config_metadata_section': 'Metadata',
    'config_metadata_url': 'Metadata URL',
    'config_metadata_url_help': 'MetaMetadata catalog source. Must be an accessible HTTPS URL.',
    'config_metadata_refresh_interval': 'Refresh interval',
    'config_metadata_refresh_interval_help': "Time between two refreshes (e.g. '24h', '30m').",
    'config_update_section': 'Advanced update settings',
    'config_update_check_interval': 'Check interval',
    'config_update_check_interval_help': "Time between two GitHub checks (e.g. '6h', '1h').",
    'config_update_github_token': 'GitHub token',
    'config_update_github_token_help': 'Optional. Raises the GitHub API rate limit (60/h → 5000/h).',
    'config_update_owner': 'Owner',
    'config_update_repo': 'Repository',
    'config_save': 'Save',
    'config_saved': 'Settings saved',
    'config_save_failed': 'Save failed',
    'config_reset_default': 'Reset to default',
    'config_admin_password_change': 'Change admin password',
    'config_old_password': 'Old password',
    'config_new_password': 'New password',
    'config_confirm_password': 'Confirm password',
    'config_password_required': 'Please fill in all fields',
    'config_password_mismatch': 'Passwords do not match',
    'config_password_too_short': 'Password must be at least 4 characters',
    'config_password_changed': 'Password changed successfully',
    'config_password_change_failed': 'Change failed',
    'config_archive_password_too_short': 'Archive password must be at least 8 characters',
    'setup_show_password': 'Show',
    'setup_hide_password': 'Hide',
    // Games table column headers
    'games_col_package': 'Package',
    'games_col_name': 'Name',
    'games_col_size': 'Size',
    'games_col_exposed': 'Exposed',
    'games_col_updated': 'Last updated',
    // Client Setup page (M-04)
    'nav_client_setup': 'Client setup',
    'client_setup_title': 'Connect a VRHub client',
    'client_setup_intro': 'Here is the information you need to connect your Meta Quest to this server.',
    'client_setup_loading': 'Loading…',
    'client_setup_recommended': 'Recommended method',
    'client_setup_url_label': 'Auto-configuration URL',
    'client_setup_url_help': 'In the VRHub app, open Settings → Server and paste this URL. The password will be added automatically.',
    'client_setup_manual': 'Manual method',
    'client_setup_base_uri': 'Server address',
    'client_setup_password': 'Archive password',
    'client_setup_test': 'Test the connection',
    'client_setup_step1': '1. On your Quest, open the VRHub app',
    'client_setup_step2': '2. Go to Settings → Server',
    'client_setup_step3': '3. Paste the configuration URL OR the server address and password',
    'client_setup_step4': '4. Save. The game catalog appears.',
    'client_setup_error': 'Failed to load configuration.',
    'michel_client_title': 'Connect a client',
    // API key management (M-02)
    'api_key_title': 'Admin API key',
    'api_key_help': "This key lets your scripts hit the X-API-Key protected endpoints. It won't be shown again after regeneration.",
    'api_key_reveal': 'Reveal key',
    'api_key_regenerate': 'Regenerate',
    'api_key_no_plaintext': 'Key not available. Regenerate to get a new one.',
    'api_key_copied': 'Key copied',
    'api_key_regen_confirm': 'Regenerate API key? The old key will be immediately invalidated.',
    'api_key_save_warning': 'Copy this key NOW. It will never be shown again.',
};

function translatePage(mode) {
    var dict = mode === 'michel' ? I18N_MICHEL : I18N_POWER;
    var fallbackDict = mode === 'michel' ? I18N_POWER : I18N_MICHEL;
    var elements = document.querySelectorAll('[data-i18n]');
    for (var i = 0; i < elements.length; i++) {
        var key = elements[i].getAttribute('data-i18n');
        if (dict[key]) {
            elements[i].textContent = dict[key];
        } else if (fallbackDict[key]) {
            // H4: fallback to other dictionary when key missing in current mode's dict
            elements[i].textContent = fallbackDict[key];
        } else if (window.__VRHUB_DEBUG__) {
            // R6-M8: Warn only when debug flag is set; accumulate keys to avoid spam
            if (!window.__missingI18nKeys) window.__missingI18nKeys = new Set();
            if (!window.__missingI18nKeys.has(key)) {
                window.__missingI18nKeys.add(key);
                console.warn('i18n key missing in both dicts:', key);
            }
        }
    }
}

// R6-L11: i18n() is the dynamic-text helper. For STATIC text, prefer data-i18n attrs in the HTML
// template (handled by translatePage). Use i18n() only for text set after a fetch / user action.
// R7-M6: The bracket fallback `[i18n:key]` is gated behind __VRHUB_DEBUG__ to avoid leaking
// placeholder text into production.
function i18n(key, fallback) {
    var mode = getMode();
    var missing = window.__VRHUB_DEBUG__ ? ('[i18n:' + key + ']') : (fallback || key);
    if (mode === 'michel') {
        return I18N_MICHEL[key] || missing;
    }
    return I18N_POWER[key] || missing;
}

// R7-H5: setI18nText removed — was dead code. Dynamic text updates use i18n() directly + textContent.
// The data-i18n-state attribute is also removed (was unread by translatePage).

// ============================================================
// Formatting helpers (Subtask 3.5, 3.6)
// ============================================================
function formatBytes(bytes, mode) {
    if (bytes == null || !isFinite(bytes) || Number(bytes) < 0) return mode === 'power' ? '0 bytes' : '0 octets';
    var isMichel = mode === 'michel' || mode == null;
    if (!isMichel) {
        // Power User (English): full precision raw bytes, e.g. "188 743 680 bytes"
        return Math.round(bytes).toString().replace(/\B(?=(\d{3})+(?!\d))/g, '\u202f') + ' bytes';
    }
    var units = ['o', 'Ko', 'Mo', 'Go', 'To'];
    var size = Number(bytes);
    // H1/P17: Special case for 0 bytes — Michel returns "0 o", Power returns "0 octets" (no NNBSP regex needed)
    if (size === 0) return isMichel ? '0 o' : '0 octets';
    var i = 0;
    while (size >= 1024 && i < units.length - 1) {
        size /= 1024;
        i++;
    }
    return size.toFixed(1) + ' ' + units[i];
}

// R6-L6: Despite the name, this returns relative time in Michel mode and absolute ISO in Power mode.
// Kept as formatRelativeTime to avoid a rename refactor; behaviour is documented inline.
function formatRelativeTime(isoString, mode) {
    if (!isoString) return '';
    try {
        var date = new Date(isoString);
        // R6-H2: Return '—' for invalid date (was isoString || '—', dead code since guard above ensures truthy)
        if (isNaN(date.getTime())) return '—';
        var now = new Date();
        var diffMs = now - date;
        // R6-H1: Future-date guard — drop the no-op ternary; em-dash is language-neutral
        if (diffMs < 0) return '—';
        // Power User: full ISO precision (absolute time, not relative)
        if (mode === 'power') return date.toISOString();
        var diffSec = Math.floor(diffMs / 1000);
        var diffMin = Math.floor(diffSec / 60);
        var diffHr = Math.floor(diffMin / 60);
        var diffDay = Math.floor(diffHr / 24);
        // Michel mode: French relative time with "Scanné" prefix
        if (diffSec < 60) return "Scanné à l'instant";
        if (diffMin < 60) return 'Scanné il y a ' + diffMin + 'min';
        if (diffHr < 24) return 'Scanné il y a ' + diffHr + 'h';
        if (diffDay < 7) return 'Scanné il y a ' + diffDay + 'j';
        return date.toLocaleDateString('fr-FR');
    } catch(e) {
        return '—';
    }
}

// P1: formatNumberFR deleted — replaced by mode-aware formatNumber() callsite
// M11: Mode-aware number formatting
function formatNumber(value, mode) {
    var locale = mode === 'power' ? 'en-US' : 'fr-FR';
    try {
        return new Intl.NumberFormat(locale).format(value);
    } catch(e) {
        return String(value);
    }
}

// ============================================================
// Story 9.5 (B5): Login form glue was removed from admin.js.
//
// The login form is no longer embedded in the admin shell — it
// lives on a dedicated /admin/login page that loads login.js for
// the form-submit handler (see internal/ui/embed/login.js). The
// previous code is now in login.js, which is a self-contained
// IIFE with no dependency on the I18N_MICHEL/I18N_POWER
// dictionaries or the shell's
// DOMContentLoaded bootstrap. This keeps the shell's JS bundle
// smaller and avoids the dashboard-behind-login-form UX bug.
// ============================================================

// ============================================================
// Story 7.6: Network status badge polling
// ============================================================
//
// One badge per mode (Michel header + Power sidebar). Both share
// the .network-status-badge class so a single querySelectorAll
// picks up both — only one is visible at a time (CSS-driven via
// body.mode-michel / body.mode-power), so applying the same
// content/style to both is harmless and keeps the rendering
// function simple.
//
// Polling cadence is 60s, matching the server-side check interval.
// A 60s poll × 2 services × 24h = 2880 req/day from one client,
// well below GitHub's anonymous rate limit (60 req/h per IP).
//
// textContent is used everywhere (NOT innerHTML) — the status
// strings are an enum so there's no XSS surface, but we keep the
// convention from Story 7.5 (R6-M7 / renderStatsTable) for
// consistency.
var NETWORK_STATUS_POLL_MS = 60000;

function initNetworkStatus() {
    // querySelectorAll picks up both the Michel header badge and
    // the Power sidebar badge. We update both on every poll.
    var badges = document.querySelectorAll('.network-status-badge');
    if (!badges || badges.length === 0) return;

    // Map a server status string to the CSS badge class + dot glyph.
    // The 4 server-side states map to 4 client-side badge classes;
    // the "fetch error" case (network failure on this client) is
    // treated as offline so the user sees a red badge.
    function classFor(status) {
        switch (status) {
            case 'ok':       return 'badge-success';
            case 'degraded': return 'badge-warning';
            case 'offline':  return 'badge-error';
            case 'unknown':  return 'badge-muted';
            default:         return 'badge-muted';
        }
    }
    function glyphFor(status) {
        switch (status) {
            case 'ok':       return '●'; // ● filled circle (green when ok)
            case 'degraded': return '!';
            case 'offline':  return '✕'; // ✕
            case 'unknown':  return '?';
            default:         return '?';
        }
    }
    // i18n label key for the status (used in the title tooltip).
    // Returns the raw status string if no label is registered.
    function labelKeyFor(status) {
        switch (status) {
            case 'ok':       return 'network_status_label_ok';
            case 'degraded': return 'network_status_label_degraded';
            case 'offline':  return 'network_status_label_offline';
            case 'unknown':  return 'network_status_label_unknown';
            default:         return 'network_status_label_unknown';
        }
    }

    function applyToBadges(status) {
        var cls = classFor(status);
        var glyph = glyphFor(status);
        var label = i18n(labelKeyFor(status), status);
        var title = (i18n('network_status_title', 'Network status: {status}') || '')
            .replace('{status}', label);
        for (var i = 0; i < badges.length; i++) {
            var b = badges[i];
            // Replace the class list (keep badge-pill, drop the old
            // status class, add the new one). The first iteration
            // before any check landed also starts as badge-muted.
            b.className = 'badge-pill ' + cls + ' network-status-badge' +
                (b.classList.contains('sidebar-badge') ? ' sidebar-badge' : '');
            b.textContent = glyph;
            b.title = title;
            b.setAttribute('aria-label', title);
        }
    }

    function poll() {
        fetch('/admin/api/network-status', {
            credentials: 'same-origin',
            headers: { 'Accept': 'application/json' }
        })
            .then(function(r) {
                if (!r.ok) {
                    // 503 NOT_CONFIGURED (checker nil) or other server
                    // errors → mark offline so the user knows something
                    // is wrong (the badge turns red, the tooltip
                    // shows the localized error label).
                    applyToBadges('offline');
                    return null;
                }
                return r.json();
            })
            .then(function(j) {
                if (!j || !j.data) return;
                var d = j.data;
                // The server computes all_ok = (github == ok && metadata == ok).
                // When all_ok is true, the badge is green. Otherwise we
                // show the WORST of the two states (offline beats degraded
                // beats ok) so the user immediately sees there's a problem.
                var status;
                if (d.all_ok) {
                    status = 'ok';
                } else if (d.github === 'ok' && d.metadata === 'ok') {
                    // all_ok false but both report ok — server-side race
                    // (check in-flight). Treat as ok to avoid flashing red.
                    status = 'ok';
                } else if (d.github === 'offline' || d.metadata === 'offline') {
                    status = 'offline';
                } else if (d.github === 'degraded' || d.metadata === 'degraded') {
                    status = 'degraded';
                } else {
                    // both "unknown" (pre-first-check) or mixed unknown
                    status = 'unknown';
                }
                applyToBadges(status);
            })
            .catch(function() {
                // Network failure (CORS, DNS, offline) → red badge.
                applyToBadges('offline');
            });
    }

    // First poll immediately so the user sees a real status
    // (not the gray "checking…" placeholder) within ~1s of
    // page load. Subsequent polls are on the 60s timer.
    poll();
    setInterval(poll, NETWORK_STATUS_POLL_MS);
}

// Wire initNetworkStatus into the DOMContentLoaded bootstrap
// (mirrors the existing init pattern in setMode bootstrap).
if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initNetworkStatus);
} else {
    initNetworkStatus();
}

// ============================================================
// Story 1.6: Expose i18n + escapeHtml helpers to the standalone
// setup wizard (internal/ui/embed/setup.js). The setup page loads
// admin.js FIRST, then setup.js — so by the time setup.js runs,
// these helpers are on window.__VRHUB_I18N__. The defensive
// `window.__VRHUB_I18N__ || {}` check in setup.js keeps it
// usable even if admin.js fails to load (it falls back to local
// inline implementations).
// ============================================================
window.__VRHUB_I18N__ = window.__VRHUB_I18N__ || {};
window.__VRHUB_I18N__.i18n = i18n;
window.__VRHUB_I18N__.escapeHtml = escapeHtml;
if (typeof showInlineNotification === 'function') {
    window.__VRHUB_I18N__.showInlineNotification = showInlineNotification;
}

// Story 10.1: Force reflow on orientation change (iOS Safari bug
// workaround). When the device rotates, the visual viewport may not
// update CSS media queries immediately. This ensures the layout
// recalculates without scrolling the page.
(function() {
    var timer;
    window.addEventListener('orientationchange', function() {
        clearTimeout(timer);
        timer = setTimeout(function() {
            window.scrollTo(0, document.body.scrollTop || 0);
        }, 100);
    });
})();
