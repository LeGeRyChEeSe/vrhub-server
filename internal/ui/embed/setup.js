// VRHub Server - Setup Wizard JS (Story 1.6)
//
// Drives the 4-step setup wizard at /admin/setup. The page calls
// the existing /admin/api/setup/* endpoints:
//
//   Step 1 (Credentials): POST /admin/api/setup/credentials
//   Step 2 (Game folder): POST /admin/api/setup/scan
//   Step 3 (Review):      POST /admin/api/setup/review
//   Step 4 (Launch):      POST /admin/api/setup/launch
//
// Design conventions (locked in the plan):
//   - One IIFE per step, controlled by goToStep(n).
//   - Reuses i18n() and escapeHtml() from admin.js (exposed via
//     window.__VRHUB_I18N__). Falls back to local helpers if the
//     global is not yet loaded (defense-in-depth).
//   - textContent for ALL user data (XSS safety).
//   - In-flight guard + submitBtn.disabled on each step.
//   - AbortController: 30s for scan, 10s default for others.

(function() {
    'use strict';

    // ----- Helpers from admin.js (exposed via window.__VRHUB_I18N__) -----
    var i18nHelpers = window.__VRHUB_I18N__ || {};
    function i18n(key, fallback) {
        if (i18nHelpers.i18n) return i18nHelpers.i18n(key, fallback);
        return fallback || key;
    }
    function escapeHtml(str) {
        if (i18nHelpers.escapeHtml) return i18nHelpers.escapeHtml(str);
        var div = document.createElement('div');
        div.textContent = str;
        return div.innerHTML;
    }

    // ----- State -----
    var state = {
        step: 1,
        scanResult: null,
        excluded: [],
        launchResult: null,
        credentials: null
    };
    var inFlight = { step1: false, step2: false, step3: false, step4: false, step5: false };

    // ----- Step navigation -----
    function goToStep(n) {
        if (n < 1 || n > 5) return;
        // Hide all sections
        for (var i = 1; i <= 5; i++) {
            var sec = document.getElementById('step-' + i);
            if (sec) sec.classList.add('hidden');
        }
        // Show target
        var target = document.getElementById('step-' + n);
        if (target) target.classList.remove('hidden');
        state.step = n;
        // Update stepper
        for (var j = 1; j <= 5; j++) {
            var dot = document.getElementById('stepper-dot-' + j);
            if (!dot) continue;
            dot.classList.remove('active', 'done');
            if (j < n) dot.classList.add('done');
            else if (j === n) dot.classList.add('active');
        }
        // Update connectors
        for (var k = 1; k <= 4; k++) {
            var conn = document.getElementById('stepper-conn-' + k);
            if (!conn) continue;
            if (k < n) conn.classList.add('active');
            else conn.classList.remove('active');
        }
        // Focus first input in the step
        var firstInput = target && target.querySelector('input, button');
        if (firstInput) firstInput.focus();
    }

    // ----- Shared helpers -----
    function showError(stepEl, message) {
        var errEl = stepEl.querySelector('.wizard-error');
        if (!errEl) {
            errEl = document.createElement('div');
            errEl.className = 'notification notification-error wizard-error';
            errEl.setAttribute('role', 'alert');
            errEl.setAttribute('aria-live', 'assertive');
            stepEl.insertBefore(errEl, stepEl.firstChild);
        }
        errEl.textContent = message;
        errEl.classList.remove('hidden');
    }
    function clearError(stepEl) {
        var errEl = stepEl.querySelector('.wizard-error');
        if (errEl) {
            errEl.classList.add('hidden');
            errEl.textContent = '';
        }
    }
    function setInFlight(stepKey, button, inFlightVal) {
        inFlight[stepKey] = inFlightVal;
        if (button) button.disabled = inFlightVal;
    }
    function fetchJSON(url, opts) {
        opts = opts || {};
        var ctrl = (typeof AbortController !== 'undefined') ? new AbortController() : null;
        var timeoutMs = opts.timeoutMs || 10000;
        var timer = null;
        if (ctrl) {
            timer = setTimeout(function() { ctrl.abort(); }, timeoutMs);
        }
        var fetchOpts = {
            method: opts.method || 'GET',
            credentials: 'same-origin'
        };
        if (ctrl) fetchOpts.signal = ctrl.signal;
        if (opts.body) {
            fetchOpts.headers = { 'Content-Type': 'application/json' };
            fetchOpts.body = JSON.stringify(opts.body);
        }
        return fetch(url, fetchOpts)
            .then(function(r) {
                if (timer) clearTimeout(timer);
                if (!r.ok) {
                    return r.json().then(function(j) {
                        var msg = (j && j.error && j.error.message) || ('HTTP ' + r.status);
                        throw new Error(msg);
                    }).catch(function() {
                        throw new Error('HTTP ' + r.status);
                    });
                }
                return r.json();
            })
            .then(function(data) { return data && data.data; })
            .catch(function(err) {
                if (timer) clearTimeout(timer);
                if (err && err.name === 'AbortError') {
                    throw new Error(i18n('setup_error_timeout', 'La requête a expiré. Veuillez réessayer.'));
                }
                throw err;
            });
    }

    // ----- Step 1: Credentials -----
    function initStep1() {
        var form = document.getElementById('step-1-form');
        if (!form) return;
        form.addEventListener('submit', function(e) {
            e.preventDefault();
            if (inFlight.step1) return;
            var stepEl = document.getElementById('step-1');
            clearError(stepEl);
            var usernameEl = document.getElementById('setup-username');
            var passwordEl = document.getElementById('setup-password');
            var username = (usernameEl && usernameEl.value || '').trim();
            var password = (passwordEl && passwordEl.value || '');
            if (!username) {
                showError(stepEl, i18n('setup_error_required', 'Ce champ est requis.'));
                return;
            }
            if (password.length < 4) {
                showError(stepEl, i18n('setup_error_short_password', 'Le mot de passe doit contenir au moins 4 caractères.'));
                return;
            }
            var submitBtn = document.getElementById('step-1-submit');
            setInFlight('step1', submitBtn, true);
            fetchJSON('/admin/api/setup/credentials', {
                method: 'POST',
                body: { username: username, password: password }
            })
            .then(function() {
                state.credentials = { username: username };
                goToStep(2);
            })
            .catch(function(err) {
                showError(stepEl, err.message || i18n('setup_error_server', 'Erreur serveur. Veuillez réessayer.'));
            })
            .finally(function() { setInFlight('step1', submitBtn, false); });
        });
    }

    // ----- Step 2: Archive password -----
    function initStep2() {
        var form = document.getElementById('step-2-form');
        var backBtn = document.getElementById('step-2-back');
        var generateBtn = document.getElementById('step-2-generate');
        var lengthSlider = document.getElementById('setup-archive-password-length');
        var lengthValue = document.getElementById('setup-archive-password-length-value');
        var passwordInput = document.getElementById('setup-archive-password');

        if (backBtn) {
            backBtn.addEventListener('click', function() { goToStep(1); });
        }
        if (lengthSlider && lengthValue) {
            lengthSlider.addEventListener('input', function() {
                lengthValue.textContent = lengthSlider.value;
            });
        }
        if (generateBtn && passwordInput) {
            generateBtn.addEventListener('click', function() {
                var len = parseInt(lengthSlider ? lengthSlider.value : 24, 10);
                var charset = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*';
                var pw = '';
                for (var i = 0; i < len; i++) {
                    pw += charset.charAt(Math.floor(Math.random() * charset.length));
                }
                passwordInput.value = pw;
            });
        }
        var toggleBtn = document.getElementById('step-2-toggle-password');
        if (toggleBtn && passwordInput) {
            toggleBtn.addEventListener('click', function() {
                if (passwordInput.type === 'password') {
                    passwordInput.type = 'text';
                    toggleBtn.textContent = i18n('setup_hide_password', 'Masquer');
                } else {
                    passwordInput.type = 'password';
                    toggleBtn.textContent = i18n('setup_show_password', 'Afficher');
                }
            });
        }
        if (!form) return;
        form.addEventListener('submit', function(e) {
            e.preventDefault();
            if (inFlight.step2) return;
            var stepEl = document.getElementById('step-2');
            clearError(stepEl);
            var password = (passwordInput && passwordInput.value || '');
            if (password.length < 8) {
                showError(stepEl, i18n('setup_error_short_archive_password', 'Le mot de passe doit contenir au moins 8 caractères.'));
                return;
            }
            var submitBtn = document.getElementById('step-2-submit');
            setInFlight('step2', submitBtn, true);
            fetchJSON('/admin/api/setup/archive-password', {
                method: 'POST',
                body: { archive_password: password }
            })
            .then(function() { goToStep(3); })
            .catch(function(err) {
                showError(stepEl, err.message || i18n('setup_error_server', 'Erreur serveur. Veuillez réessayer.'));
            })
            .finally(function() { setInFlight('step2', submitBtn, false); });
        });
    }

    // ----- Step 3: Game folder -----
    function initStep3() {
        var form = document.getElementById('step-3-form');
        var backBtn = document.getElementById('step-3-back');
        if (backBtn) {
            backBtn.addEventListener('click', function() { goToStep(2); });
        }
        if (!form) return;
        form.addEventListener('submit', function(e) {
            e.preventDefault();
            if (inFlight.step3) return;
            var stepEl = document.getElementById('step-3');
            clearError(stepEl);
            var folderEl = document.getElementById('setup-folder');
            var folder = (folderEl && folderEl.value || '').trim();
            if (!folder) {
                showError(stepEl, i18n('setup_error_required', 'Ce champ est requis.'));
                return;
            }
            var submitBtn = document.getElementById('step-3-submit');
            var summaryEl = document.getElementById('step-3-summary');
            setInFlight('step3', submitBtn, true);
            summaryEl.textContent = i18n('setup_step3_scanning', 'Scan en cours…');
            summaryEl.classList.remove('hidden');
            fetchJSON('/admin/api/setup/scan', {
                method: 'POST',
                body: { folder: folder },
                timeoutMs: 300000
            })
            .then(function(data) {
                state.scanResult = data || {};
                var games = data.games || [];
                state.excluded = games.filter(function(g) { return g.corrupted; }).map(function(g) { return g.package_name; });
                var fileCount = (data.file_count != null) ? data.file_count : games.length;
                var sizeBytes = data.total_size_bytes || 0;
                var sizeMo = sizeBytes / 1048576;
                var sizeStr = sizeMo >= 1024 ? (sizeMo / 1024).toFixed(1) + ' Go' : sizeMo.toFixed(1) + ' Mo';
                summaryEl.textContent = fileCount + ' fichier(s) trouvé(s) · ' + sizeStr;
                goToStep(4);
                renderStep4Games();
            })
            .catch(function(err) {
                summaryEl.classList.add('hidden');
                showError(stepEl, err.message || i18n('setup_error_scan_failed', 'Échec du scan du dossier.'));
            })
            .finally(function() { setInFlight('step3', submitBtn, false); });
        });
    }

    // ----- Step 4: Review -----
    function renderStep4Games() {
        var listEl = document.getElementById('step-4-list');
        var emptyEl = document.getElementById('step-4-empty');
        if (!listEl) return;
        listEl.textContent = '';
        var games = (state.scanResult && state.scanResult.games) || [];
        if (games.length === 0) {
            if (emptyEl) emptyEl.classList.remove('hidden');
            return;
        }
        if (emptyEl) emptyEl.classList.add('hidden');
        games.forEach(function(g) {
            var isExcluded = state.excluded.indexOf(g.package_name) !== -1;
            var item = document.createElement('div');
            item.className = 'wizard-game-item';

            var cb = document.createElement('input');
            cb.type = 'checkbox';
            cb.id = 'game-' + g.package_name;
            cb.checked = !isExcluded;
            cb.disabled = !!g.corrupted;
            cb.addEventListener('change', function() {
                var idx = state.excluded.indexOf(g.package_name);
                if (cb.checked && idx !== -1) state.excluded.splice(idx, 1);
                else if (!cb.checked && idx === -1) state.excluded.push(g.package_name);
            });

            var label = document.createElement('label');
            label.htmlFor = cb.id;
            label.style.flex = '1';
            label.style.cursor = cb.disabled ? 'not-allowed' : 'pointer';

            var name = document.createElement('div');
            name.className = 'wizard-game-name';
            name.textContent = g.game_name || g.package_name || '?';

            var meta = document.createElement('div');
            meta.className = 'wizard-game-meta';
            var sizeTotal = (g.size_bytes || 0) + (g.obb_size_bytes || 0);
            var sizeMoG = sizeTotal / 1048576;
            var sizeStr = sizeMoG >= 1024 ? (sizeMoG / 1024).toFixed(1) + ' Go' : sizeMoG.toFixed(1) + ' Mo';
            meta.textContent = (g.package_name || '?') + ' · v' + (g.version_code != null ? g.version_code : '?') + ' · ' + sizeStr;

            label.appendChild(name);
            label.appendChild(meta);

            item.appendChild(cb);
            item.appendChild(label);

            if (g.corrupted) {
                var cor = document.createElement('div');
                cor.className = 'wizard-game-corrupted';
                cor.textContent = i18n('setup_step4_corrupted', 'Corrompu');
                item.appendChild(cor);
            }

            listEl.appendChild(item);
        });
    }
    function initStep4() {
        var form = document.getElementById('step-4-form');
        var backBtn = document.getElementById('step-4-back');
        if (backBtn) {
            backBtn.addEventListener('click', function() { goToStep(3); });
        }
        if (!form) return;
        form.addEventListener('submit', function(e) {
            e.preventDefault();
            if (inFlight.step4) return;
            var stepEl = document.getElementById('step-4');
            clearError(stepEl);
            var submitBtn = document.getElementById('step-4-submit');
            setInFlight('step4', submitBtn, true);
            fetchJSON('/admin/api/setup/review', {
                method: 'POST',
                body: { excluded: state.excluded }
            })
            .then(function() { goToStep(5); })
            .catch(function(err) {
                showError(stepEl, err.message || i18n('setup_error_server', 'Erreur serveur. Veuillez réessayer.'));
            })
            .finally(function() { setInFlight('step4', submitBtn, false); });
        });
    }

    // ----- Step 5: Launch -----
    function initStep5() {
        var form = document.getElementById('step-5-form');
        var backBtn = document.getElementById('step-5-back');
        if (backBtn) {
            backBtn.addEventListener('click', function() { goToStep(4); });
        }
        if (!form) return;
        form.addEventListener('submit', function(e) {
            e.preventDefault();
            if (inFlight.step5) return;
            var stepEl = document.getElementById('step-5');
            clearError(stepEl);
            var portEl = document.getElementById('setup-port');
            var port = portEl ? parseInt(portEl.value, 10) : 8080;
            if (!port || port < 1 || port > 65535) {
                showError(stepEl, i18n('setup_error_invalid_port', 'Le port doit être compris entre 1 et 65535.'));
                return;
            }
            var submitBtn = document.getElementById('step-5-submit');
            setInFlight('step5', submitBtn, true);
            fetchJSON('/admin/api/setup/launch', {
                method: 'POST',
                body: { port: port }
            })
            .then(function(data) {
                state.launchResult = data;
                renderStep5Result(data);
            })
            .catch(function(err) {
                showError(stepEl, err.message || i18n('setup_error_server', 'Erreur serveur. Veuillez réessayer.'));
            })
            .finally(function() { setInFlight('step5', submitBtn, false); });
        });
    }
    function renderStep5Result(data) {
        var resultEl = document.getElementById('step-5-result');
        var submitBtn = document.getElementById('step-5-submit');
        var backBtn = document.getElementById('step-5-back');
        if (!resultEl) return;
        if (submitBtn) submitBtn.classList.add('hidden');
        if (backBtn) backBtn.classList.add('hidden');
        resultEl.textContent = '';
        resultEl.classList.remove('hidden');

        var title = document.createElement('h2');
        title.className = 'wizard-step-title';
        title.textContent = i18n('setup_step5_done_title', 'Serveur prêt !');
        resultEl.appendChild(title);

        // Recommended method for VRHub: client config URL
        var recoTitle = document.createElement('h3');
        recoTitle.className = 'wizard-step-subtitle';
        recoTitle.style.marginTop = 'var(--space-4)';
        recoTitle.textContent = i18n('client_config_recommended', 'Méthode recommandée pour VRHub');
        resultEl.appendChild(recoTitle);

        var recoBox = document.createElement('div');
        recoBox.className = 'wizard-credentials-box';
        var recoLabel = document.createElement('div');
        recoLabel.className = 'wizard-game-meta';
        recoLabel.textContent = i18n('client_config_url_label', 'URL de configuration') + ' :';
        recoBox.appendChild(recoLabel);
        var recoCode = document.createElement('code');
        var base = (data.base_uri || '').replace(/\/$/, '');
        recoCode.textContent = base + '/config.json';
        recoBox.appendChild(recoCode);
        resultEl.appendChild(recoBox);

        // Manual method for all clients
        var manualTitle = document.createElement('h3');
        manualTitle.className = 'wizard-step-subtitle';
        manualTitle.style.marginTop = 'var(--space-6)';
        manualTitle.textContent = i18n('client_config_manual', 'Configuration manuelle (tous les clients)');
        resultEl.appendChild(manualTitle);

        var box = document.createElement('div');
        box.className = 'wizard-credentials-box';

        var baseLabel = document.createElement('div');
        baseLabel.className = 'wizard-game-meta';
        baseLabel.textContent = i18n('setup_step5_base_uri', 'Base URI') + ' :';
        box.appendChild(baseLabel);

        var baseCode = document.createElement('code');
        baseCode.textContent = data.base_uri || '?';
        box.appendChild(baseCode);

        var passLabel = document.createElement('div');
        passLabel.className = 'wizard-game-meta';
        passLabel.textContent = i18n('setup_step5_password', 'Mot de passe VRHub') + ' :';
        box.appendChild(passLabel);

        var passCode = document.createElement('code');
        passCode.textContent = data.password || '?';
        box.appendChild(passCode);

        resultEl.appendChild(box);

        var instrTitle = document.createElement('p');
        instrTitle.className = 'wizard-step-subtitle';
        instrTitle.style.marginTop = 'var(--space-6)';
        instrTitle.textContent = i18n('setup_step5_instructions', 'Instructions :');
        resultEl.appendChild(instrTitle);

        var ol = document.createElement('ol');
        ol.className = 'wizard-instructions';
        (data.instructions || []).forEach(function(line) {
            var li = document.createElement('li');
            li.textContent = line;
            ol.appendChild(li);
        });
        resultEl.appendChild(ol);

        var link = document.createElement('a');
        link.href = '/admin/login?showLogin=1';
        link.className = 'btn btn-primary';
        link.style.marginTop = 'var(--space-6)';
        link.style.display = 'inline-block';
        link.style.textDecoration = 'none';
        link.textContent = i18n('setup_step5_open_admin', "Ouvrir l'admin");
        resultEl.appendChild(link);
    }

    // ----- Auto-skip on page load (Story 1.7 B1 + 9.8) -----
    //
    // The wizard asks the server which prerequisites are already
    // met BEFORE the user sees step 1, and jumps directly to the
    // appropriate step:
    //   - credentials_set=false        → step 1 (Credentials)
    //   - credentials_set=true, archive_set=false → step 2 (Archive password)
    //   - credentials_set=true, archive_set=true, count=0 → step 3 (Game folder)
    //   - credentials_set=true, archive_set=true, count>0 → step 5 (Launch)
    //
    // Note: archive_set is inferred from the server-side state
    // endpoint which returns archive_password_set ( Story 9.8).
    function autoSkipFromState() {
        fetchJSON('/admin/api/setup/state')
            .then(function(data) {
                if (!data) return;
                if (data.credentials_set && data.archive_password_set && data.game_count > 0) {
                    goToStep(5);
                } else if (data.credentials_set && data.archive_password_set) {
                    goToStep(3);
                } else if (data.credentials_set) {
                    goToStep(2);
                } else {
                    goToStep(1);
                }
            })
            .catch(function() {
                goToStep(1);
            });
    }

    // ----- Bootstrap -----
    document.addEventListener('DOMContentLoaded', function() {
        initStep1();
        initStep2();
        initStep3();
        initStep4();
        initStep5();
        // Auto-skip BEFORE initial goToStep(1) so the user lands on the
        // right step. If the state fetch hasn't returned yet, goToStep(1)
        // is the safe default; autoSkipFromState's then() will re-position
        // the wizard as soon as the response lands.
        autoSkipFromState();
        goToStep(1);
    });
})();
