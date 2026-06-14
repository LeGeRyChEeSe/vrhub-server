// ============================================================
// Login page JS (Story 9.5 / B5)
// ============================================================
//
// Extracted from admin.js so the dedicated /admin/login page can
// load it as a standalone script. The previous admin.js contained
// the loginFormSubmit() handler plus the setupLoginSection()
// reveal mechanism. After Story 9.5:
//   - The login form is no longer embedded in the admin shell.
//   - /admin/login serves a dedicated page that loads THIS file.
//   - admin.js no longer needs setupLoginSection() (the shell
//     doesn't have a login form to reveal).
//
// The file is intentionally self-contained: no I18N_MICHEL /
// I18N_POWER dictionaries (the page is too small to justify the
// localization overhead), no mode detection (the login page is
// mode-neutral — there is no Michel/Power distinction for the
// first time the user logs in), and no dependency on the admin
// shell's DOMContentLoaded bootstrap.

(function() {
    'use strict';

    // R10-NO-DOUBLE-CLICK-GUARD: prevent double-submit. If a fetch
    // is in flight, subsequent submits are no-ops until the in-flight
    // one completes (success or error). Mirrors the isTriggeringUpdate
    // pattern.
    var loginInFlight = false;

    // R11-HIGH-5: AbortController for the login fetch. A stalled
    // connection (server hung, network down) would otherwise leave
    // loginInFlight=true and the submit button disabled forever,
    // locking the operator out. 15 s timeout matches the
    // update-applied polling pattern.
    var loginAbortController = null;

    function showError(errorEl, msg) {
        if (errorEl) {
            errorEl.textContent = msg;
            errorEl.classList.remove('hidden');
        }
    }

    function clearError(errorEl) {
        if (errorEl) {
            errorEl.textContent = '';
            errorEl.classList.add('hidden');
        }
    }

    function loginFormSubmit() {
        if (loginInFlight) return;
        loginInFlight = true;

        var usernameEl = document.getElementById('login-username');
        var passwordEl = document.getElementById('login-password');
        var errorEl = document.getElementById('login-error');
        var submitBtn = document.getElementById('login-submit');

        if (submitBtn) submitBtn.disabled = true;

        // R7-LOGIN-ERROR-NOT-CLEARED: clear any stale error before
        // starting a new submission so the user does not see a
        // lingering message during a successful retry.
        clearError(errorEl);

        if (!usernameEl || !passwordEl) {
            loginInFlight = false;
            if (submitBtn) submitBtn.disabled = false;
            return;
        }

        var username = usernameEl.value.trim();
        var password = passwordEl.value;

        // R10-LOGIN-EMPTY-ERROR: empty-field validation uses a
        // distinct message so the user is not told "Invalid
        // credentials" when they simply forgot to fill a field.
        if (!username || !password) {
            showError(errorEl, 'Please fill in all fields');
            loginInFlight = false;
            if (submitBtn) submitBtn.disabled = false;
            return;
        }

        // R11-HIGH-5: install a 15 s timeout via AbortController. If
        // the server hangs or the network drops, we abort the
        // request and re-enable the submit button so the operator
        // can retry.
        //
        // R12-P3: feature-detect AbortController. In legacy browsers
        // without support, `new AbortController()` throws
        // ReferenceError BEFORE the fetch chain is constructed — the
        // .finally (which resets loginInFlight and re-enables the
        // button) would never run, locking the user out permanently.
        // Wrap the AbortController path in a try/catch that resets
        // the in-flight state on failure. We do NOT swallow the error
        // silently; the operator gets the same "Server error" message
        // via the normal error path, but only after the fetch chain
        // has been set up. If AbortController is missing, we fall
        // through to a no-timeout fetch (preserves the R10-pre-patch
        // behavior; better than locking the user out).
        var hasAbortController = (typeof AbortController !== "undefined");
        if (hasAbortController) {
            if (loginAbortController) {
                loginAbortController.abort();
            }
            try {
                loginAbortController = new AbortController();
            } catch (e) {
                hasAbortController = false;
            }
        }
        var loginTimer = null;
        if (hasAbortController && loginAbortController) {
            loginTimer = setTimeout(function() {
                loginAbortController.abort();
            }, 15000);
        }

        fetch('/admin/api/auth/login', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
                'Accept': 'application/json'
            },
            body: JSON.stringify({ username: username, password: password }),
            signal: hasAbortController ? loginAbortController.signal : undefined
        })
        .then(function(r) {
            if (!r.ok) {
                // R10-LOGIN-CATCH-ERRORS: differentiate 401 (bad
                // credentials) from 5xx (server error) so the user
                // knows whether to retry or wait.
                if (r.status === 401 || r.status === 400) {
                    throw new Error('invalid');
                }
                throw new Error('server');
            }
            return r.json();
        })
        .then(function(data) {
            if (data && data.data && data.data.redirect) {
                window.location.assign(data.data.redirect);
            } else {
                showError(errorEl, 'Invalid username or password');
            }
        })
        .catch(function(err) {
            if (err && err.name === 'AbortError') {
                showError(errorEl, 'Server error — please try again later');
            } else if (err && err.message === 'server') {
                showError(errorEl, 'Server error — please try again later');
            } else {
                showError(errorEl, 'Invalid username or password');
            }
        })
        .finally(function() {
            if (loginTimer) clearTimeout(loginTimer);
            loginInFlight = false;
            if (submitBtn) submitBtn.disabled = false;
        });
    }

    // Wire the form submit handler at DOMContentLoaded. The script
    // tag is at the END of the body, so the form is already in the
    // DOM by the time this runs — but DOMContentLoaded is the safe
    // hook for browsers that may reorder parsing.
    document.addEventListener('DOMContentLoaded', function() {
        var form = document.getElementById('login-form');
        if (!form) return;
        form.addEventListener('submit', function(e) {
            e.preventDefault();
            loginFormSubmit();
        });
    });
})();
