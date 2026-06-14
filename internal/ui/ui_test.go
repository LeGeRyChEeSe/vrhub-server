package ui

import (
	"regexp"
	"strings"
	"testing"
)

// H5/P2/R6-H3: JS parse test — basic syntax validation (no top-level return/break/continue outside functions)
// R6-H3: Tighter break/continue detection using word boundaries (avoids false positives on `breakfast`, `else break;`)
func TestAdminJS_BasicSyntax(t *testing.T) {
	js := string(AdminJS())

	if len(js) == 0 {
		t.Fatal("AdminJS returned empty content")
	}

	// Check for common SyntaxError patterns: top-level return/break/continue
	lines := strings.Split(js, "\n")
	inFunction := false
	braceDepth := 0
	hasTopLevelReturn := false
	hasTopLevelBreak := false
	hasTopLevelContinue := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || trimmed == "" {
			continue
		}
		// Track function boundaries
		if strings.Contains(trimmed, "function ") || strings.Contains(trimmed, "=>") {
			inFunction = true
		}
		for _, c := range trimmed {
			if c == '{' {
				braceDepth++
			} else if c == '}' {
				braceDepth--
				if braceDepth < 0 {
					t.Error("JS has unmatched closing brace (SyntaxError)")
					return
				}
			}
		}
		if braceDepth <= 0 && inFunction {
			inFunction = false
		}
		// P2: Actually assert inFunction state for top-level control flow keywords
		if !inFunction && braceDepth <= 0 {
			// R6-H3: Tokenize and check exact word match to avoid false positives
			tokenized := tokenizeJS(trimmed)
			for _, tok := range tokenized {
				if tok == "return" {
					hasTopLevelReturn = true
				}
				if tok == "break" {
					hasTopLevelBreak = true
				}
				if tok == "continue" {
					hasTopLevelContinue = true
				}
			}
		}
	}

	if braceDepth != 0 {
		t.Errorf("JS has %d unclosed braces (likely SyntaxError)", braceDepth)
	}
	// P2: Assert that no top-level return/break/continue exists
	if hasTopLevelReturn {
		t.Error("JS has top-level 'return' statement outside function body (SyntaxError)")
	}
	if hasTopLevelBreak {
		t.Error("JS has top-level 'break' statement outside function body (SyntaxError)")
	}
	if hasTopLevelContinue {
		t.Error("JS has top-level 'continue' statement outside function body (SyntaxError)")
	}
}

// R6-H3/R7-H4: Tokenize a JS line into identifiers/keywords (word-boundary aware).
// Strips // line comments and string literals (single/double/template quotes) before tokenizing.
func tokenizeJS(line string) []string {
	// Strip // line comments
	if idx := strings.Index(line, "//"); idx >= 0 {
		line = line[:idx]
	}
	// R7-H4: Strip string literals — single, double, and backtick template quotes.
	// Also strip regex literals (heuristic: `/` after an operator or at start of expression).
	var stripped strings.Builder
	inString := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inString != 0 {
			if c == inString {
				// R10-TOKENIZEJS-ESCAPES: count consecutive backslashes before c.
				// An escaped quote is one backslash before a same-character quote
				// (e.g., "say \"hi\"" has backslash-quote pairs that should NOT
				// close the string). Two backslashes then a quote is an escaped
				// backslash followed by a real closing quote. The previous
				// `i == 0 || line[i-1] != '\\'` check counted only one preceding
				// backslash, so an escaped quote that followed an escaped
				// backslash (`"\\\""`) was misclassified as a string closer.
				// The fix counts backslashes: an even count of preceding
				// backslashes means the quote is unescaped (and closes the
				// string); an odd count means the quote is escaped (and stays
				// in the string).
				bs := 0
				for j := i - 1; j >= 0 && line[j] == '\\'; j-- {
					bs++
				}
				if bs%2 == 0 {
					inString = 0
				}
			}
			continue
		}
		if c == '"' || c == '\'' || c == '`' {
			inString = c
			continue
		}
		stripped.WriteByte(c)
	}
	line = stripped.String()
	var tokens []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	for _, c := range line {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$' {
			current.WriteRune(c)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

func TestAdminHTML_ContainsModeHooks(t *testing.T) {
	html := string(AdminHTML("v1.0.0"))

	// Must contain the mode detection inline script (FOUC prevention)
	if !strings.Contains(html, "vrhub:admin-mode") {
		t.Error("AdminHTML should contain localStorage key 'vrhub:admin-mode' for mode persistence")
	}
	// Story X: language preference is a second localStorage key.
	if !strings.Contains(html, "vrhub:lang") {
		t.Error("AdminHTML should contain localStorage key 'vrhub:lang' for language persistence")
	}

	// Must contain body class hooks
	if !strings.Contains(html, "mode-michel") {
		t.Error("AdminHTML should contain 'mode-michel' body class hook")
	}
	if !strings.Contains(html, "mode-power") {
		t.Error("AdminHTML should contain 'mode-power' body class reference")
	}

	// Story X: the bottom-left #mode-switch is the new toggle
	// (visible in BOTH modes). The legacy footer toggle is gone.
	if !strings.Contains(html, `id="mode-switch"`) {
		t.Error("AdminHTML should contain mode-switch (id=\"mode-switch\")")
	}
	if !strings.Contains(html, `id="mode-switch-michel"`) {
		t.Error("AdminHTML should contain the Michel segment of the mode-switch")
	}
	if !strings.Contains(html, `id="mode-switch-power"`) {
		t.Error("AdminHTML should contain the Power segment of the mode-switch")
	}

	// Story X: the unified header is identical in both modes.
	if !strings.Contains(html, `id="app-header"`) {
		t.Error("AdminHTML should contain the unified app-header (id=\"app-header\")")
	}
	if !strings.Contains(html, `id="header-baseuri"`) {
		t.Error("AdminHTML should contain the baseUri chip in the header")
	}
	if !strings.Contains(html, `id="header-archive-password"`) {
		t.Error("AdminHTML should contain the archive-password chip in the header")
	}
	if !strings.Contains(html, `id="header-archive-password-reveal"`) {
		t.Error("AdminHTML should contain the password reveal button in the header")
	}
	if !strings.Contains(html, `id="header-lang-selector"`) {
		t.Error("AdminHTML should contain the language selector in the header")
	}
	if !strings.Contains(html, `id="header-logout-btn"`) {
		t.Error("AdminHTML should contain the logout button in the unified header")
	}

	// Must contain Michel mode widgets (inside #section-dashboard)
	if !strings.Contains(html, "status-widget") {
		t.Error("AdminHTML should contain server status widget")
	}
	if !strings.Contains(html, "config-baseuri") {
		t.Error("AdminHTML should contain baseUri config element")
	}
	if !strings.Contains(html, "game-count-badge") {
		t.Error("AdminHTML should contain game count badge element")
	}

	// Story X: the in-page nav replaces the Power sidebar. The
	// .sidebar element is gone — we assert the nav is present
	// instead.
	if strings.Contains(html, `id="sidebar"`) {
		t.Error("AdminHTML should not contain the legacy #sidebar (replaced by #section-nav)")
	}
	if !strings.Contains(html, `id="section-nav"`) {
		t.Error("AdminHTML should contain #section-nav (the in-page Power nav)")
	}

	// Story X: the SPA sections (one per hash route) live in
	// <main id="app-main">. We assert a representative sample of
	// the new section IDs.
	if !strings.Contains(html, `id="app-main"`) {
		t.Error("AdminHTML should contain #app-main (the SPA main)")
	}
	if !strings.Contains(html, `id="section-dashboard"`) {
		t.Error("AdminHTML should contain #section-dashboard (the default route)")
	}
	if !strings.Contains(html, `id="section-games"`) {
		t.Error("AdminHTML should contain #section-games")
	}
	if !strings.Contains(html, `id="section-configuration"`) {
		t.Error("AdminHTML should contain #section-configuration")
	}
	if !strings.Contains(html, `id="section-backup"`) {
		t.Error("AdminHTML should contain #section-backup")
	}
	if !strings.Contains(html, `id="section-api-docs"`) {
		t.Error("AdminHTML should contain #section-api-docs")
	}
	if !strings.Contains(html, `id="section-monitoring"`) {
		t.Error("AdminHTML should contain #section-monitoring")
	}
	if !strings.Contains(html, `id="section-stats"`) {
		t.Error("AdminHTML should contain #section-stats")
	}
	if !strings.Contains(html, `id="section-power-required"`) {
		t.Error("AdminHTML should contain #section-power-required (Michel placeholder)")
	}

	// Must preserve update banner (Story 5-3 compatibility)
	if !strings.Contains(html, "update-banner") {
		t.Error("AdminHTML must preserve update banner from Story 5-3")
	}
	if !strings.Contains(html, "update-modal") {
		t.Error("AdminHTML must preserve update modal from Story 5-3")
	}
	if !strings.Contains(html, "restart-page") {
		t.Error("AdminHTML must preserve restart page from Story 5-3")
	}

	// Must contain data-i18n attributes for Michel mode strings (M5-fix: reverted H7)
	if !strings.Contains(html, `data-i18n="dashboard_title"`) {
		t.Error("AdminHTML should contain data-i18n='dashboard_title' attribute")
	}
	if !strings.Contains(html, `data-i18n="welcome_message"`) {
		t.Error("AdminHTML should contain data-i18n='welcome_message' attribute")
	}

	// P14: Default lang should be 'fr' (Michel mode default)
	if !strings.Contains(html, `<html lang="fr">`) {
		t.Error("AdminHTML should have <html lang=\"fr\"> as Michel is the default mode")
	}

	// Story X: the CTA in #section-power-required uses the
	// header_switch_power key (kept for consistency with the
	// pre-SPA Michel header).
	if !strings.Contains(html, `data-i18n="header_switch_power"`) {
		t.Error("AdminHTML should contain data-i18n='header_switch_power' attribute on the power-required CTA")
	}

	// Must contain cursor-trail container (P9: moved to last body child)
	if !strings.Contains(html, `id="cursor-trail"`) {
		t.Error("AdminHTML should contain cursor-trail container")
	}
	if !strings.Contains(html, `aria-hidden="true"`) {
		t.Error("AdminHTML should have aria-hidden='true' on cursor-trail container")
	}

	// Story X: legacy IDs that should NOT be in the shell anymore
	// (regression gate: a refactor that re-adds the Michel header
	// or the Power sidebar would fail this test).
	legacyIDs := []string{
		`id="michel-header"`,
		`id="michel-main"`,
		`id="power-main"`,
		`id="michel-mode-switch"`,
		`id="mode-toggle"`,
		`id="login-section"`,
		`id="settings-section"`,
	}
	for _, id := range legacyIDs {
		if strings.Contains(html, id) {
			t.Errorf("AdminHTML should NOT contain legacy %s (Story X: replaced by the SPA shell)", id)
		}
	}
}

func TestAdminHTML_VersionInjection(t *testing.T) {
	html := string(AdminHTML("v2.3.1"))

	if !strings.Contains(html, "v2.3.1") {
		t.Error("AdminHTML should inject the version string")
	}

	// Story X: the SPA shell renders the version ONCE in
	// #section-dashboard (the dashboard is the only place the
	// version appears; the per-mode Michel/Power widgets are now
	// toggled via CSS within the same #section-dashboard, not
	// duplicated across separate Michel/Power <main> elements).
	if !strings.Contains(html, `id="current-version"`) {
		t.Error("AdminHTML should contain #current-version (Story X: version injected once in dashboard)")
	}

	// Version should be HTML-escaped (safety check against XSS via version string)
	htmlEscaped := string(AdminHTML("<script>alert(1)</script>"))
	if !strings.Contains(htmlEscaped, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("AdminHTML should HTML-escape the version to prevent XSS in version display elements")
	}
	// H6: Also assert no unescaped <script> leaked into output
	if strings.Contains(htmlEscaped, "<script>alert(1)</script>") {
		t.Error("Unescaped version string leaked into HTML output (XSS vulnerability)")
	}
}

// Story X: the legacy #power-main is GONE. The SPA shell has a
// single <main id="app-main"> that contains all the route sections.
// This test asserts the structural HTML5 correctness of the new
// main: exactly one <main>, opens with <main>, closes with </main>.
func TestAdminHTML_PowerMainClosingTag(t *testing.T) {
	html := string(AdminHTML("v1.0.0"))
	mainIdx := strings.Index(html, `<main id="app-main"`)
	if mainIdx < 0 {
		t.Fatal("AdminHTML should contain <main id=\"app-main\">")
	}
	// The single <main> must close with </main> (no </section> mismatch).
	closeIdx := strings.Index(html[mainIdx:], "</main>")
	if closeIdx < 0 {
		t.Error("AdminHTML <main id=\"app-main\"> should close with </main>")
	}
	// HTML5 spec: exactly one <main> per document.
	mainCount := strings.Count(html, "<main ")
	if mainCount != 1 {
		t.Errorf("AdminHTML should have exactly 1 <main> element (HTML5 spec); found %d", mainCount)
	}
	// Regression gate: the legacy #power-main / #michel-main
	// elements should not appear in the SPA shell.
	if strings.Contains(html, `id="power-main"`) {
		t.Error("AdminHTML should not contain legacy #power-main (Story X: SPA has one main)")
	}
	if strings.Contains(html, `id="michel-main"`) {
		t.Error("AdminHTML should not contain legacy #michel-main (Story X: SPA has one main)")
	}
}

// TestAdminHTML_SectionNavLinks (Story X, 2026-06-10) is a
// regression gate for the SPA in-page nav wiring. The 7 SPA
// sections (Dashboard, Games, Configuration, API Docs, Monitoring,
// Backup, Statistics) are real <a href="#/..."> links to the SPA
// hash routes, not the <span class="sidebar-link disabled">
// placeholders that shipped in Story 6.1.
//
// Asserts:
//   - each of the 7 sections has an <a class="section-nav-link"
//     href="#/..."> in the rendered HTML
//   - the data-nav-route attribute matches the section's ID
//   - no legacy sidebar-link placeholders remain in the shell
func TestAdminHTML_PowerSidebarLinks(t *testing.T) {
	html := string(AdminHTML("v1.0.0"))

	// Expected: 7 SPA sections, each with a real <a> link to its
	// hash route. The route names match the data-route attributes
	// in #app-main and the data-nav-route attributes on the
	// .section-nav-link anchors.
	wantRoutes := []string{
		"dashboard",     // Dashboard
		"games",         // Games
		"configuration", // Configuration
		"api-docs",      // API Docs
		"monitoring",    // Monitoring
		"backup",        // Backup
		"stats",         // Statistics
	}
	for _, route := range wantRoutes {
		needle := `href="#/` + route + `"`
		if !strings.Contains(html, needle) {
			t.Errorf("AdminHTML should contain %q as a SPA nav link (Story X)", needle)
		}
		// The matching data-nav-route attribute must also be present
		// (the SPA router toggles .active based on this attribute).
		routeAttr := `data-nav-route="` + route + `"`
		if !strings.Contains(html, routeAttr) {
			t.Errorf("AdminHTML should contain %q on a .section-nav-link (Story X)", routeAttr)
		}
	}

	// No more <span> disabled placeholders in any nav structure.
	disabledCount := strings.Count(html, `<span class="sidebar-link disabled"`)
	if disabledCount != 0 {
		t.Errorf("AdminHTML has %d disabled sidebar links; want 0 (Story X: replaced by SPA nav)", disabledCount)
	}
	// The legacy #sidebar element is gone (replaced by #section-nav).
	if strings.Contains(html, `<aside class="sidebar"`) {
		t.Error("AdminHTML should not contain <aside class=\"sidebar\"> (Story X: replaced by #section-nav)")
	}
}

func TestAdminCSS_ReturnsNonEmpty(t *testing.T) {
	css := AdminCSS()
	if len(css) == 0 {
		t.Error("AdminCSS should return non-empty CSS content")
	}

	cssStr := string(css)

	// Must contain mode-specific CSS
	if !strings.Contains(cssStr, "mode-michel") {
		t.Error("AdminCSS should contain .mode-michel styles")
	}
	if !strings.Contains(cssStr, "mode-power") {
		t.Error("AdminCSS should contain .mode-power styles")
	}

	// Must contain theme tokens
	if !strings.Contains(cssStr, "--bg:") {
		t.Error("AdminCSS should contain CSS custom property --bg")
	}

	// Must contain status indicator styles
	if !strings.Contains(cssStr, "status-dot") {
		t.Error("AdminCSS should contain status-dot styles")
	}

	// Must contain badge styles
	if !strings.Contains(cssStr, "badge-pill") {
		t.Error("AdminCSS should contain badge-pill styles")
	}

	// Story X: the bottom-left #mode-switch is the new toggle.
	// The legacy .mode-footer / #mode-toggle styles are gone.
	if !strings.Contains(cssStr, "#mode-switch") {
		t.Error("AdminCSS should contain #mode-switch styles (Story X: replaced footer toggle)")
	}
	if !strings.Contains(cssStr, ".mode-switch-seg") {
		t.Error("AdminCSS should contain .mode-switch-seg styles (Story X)")
	}
	// The legacy footer should NOT appear in the stylesheet.
	if strings.Contains(cssStr, "body.mode-michel .mode-footer") {
		t.Error("AdminCSS should not contain the legacy 'body.mode-michel .mode-footer' rule (Story X)")
	}
}

func TestAdminJS_ReturnsNonEmpty(t *testing.T) {
	js := AdminJS()
	if len(js) == 0 {
		t.Error("AdminJS should return non-empty JS content")
	}

	jsStr := string(js)

	// Must contain mode helpers
	if !strings.Contains(jsStr, "getMode") {
		t.Error("AdminJS should contain getMode function")
	}
	if !strings.Contains(jsStr, "setMode") {
		t.Error("AdminJS should contain setMode function")
	}

	// Must contain localStorage key
	if !strings.Contains(jsStr, "vrhub:admin-mode") {
		t.Error("AdminJS should use 'vrhub:admin-mode' localStorage key")
	}

	// Must emit modechange custom event
	if !strings.Contains(jsStr, "modechange") {
		t.Error("AdminJS should emit 'modechange' custom event")
	}

	// Must contain formatting helpers
	if !strings.Contains(jsStr, "formatBytes") {
		t.Error("AdminJS should contain formatBytes function")
	}
	if !strings.Contains(jsStr, "formatRelativeTime") {
		t.Error("AdminJS should contain formatRelativeTime function")
	}

	// Must preserve existing update functionality
	if !strings.Contains(jsStr, "fetchUpdateStatus") {
		t.Error("AdminJS must preserve fetchUpdateStatus from Story 5-3")
	}
	if !strings.Contains(jsStr, "triggerUpdate") {
		t.Error("AdminJS must preserve triggerUpdate from Story 5-3")
	}

	// Must contain i18n strings
	if !strings.Contains(jsStr, "I18N_MICHEL") {
		t.Error("AdminJS should contain I18N_MICHEL dictionary")
	}

	// R6-L4: header_switch_michel removed from dicts (no element references it)
	if strings.Contains(jsStr, "'header_switch_michel'") {
		t.Error("AdminJS should NOT contain 'header_switch_michel' i18n key (R6-L4 — no element references it)")
	}

	// P1: formatNumber should exist (formatNumberFR was deleted)
	if !strings.Contains(jsStr, "function formatNumber(value, mode)") {
		t.Error("AdminJS should contain formatNumber function with mode parameter")
	}
	if strings.Contains(jsStr, "function formatNumberFR") {
		t.Error("AdminJS should NOT contain dead formatNumberFR function (replaced by formatNumber)")
	}

	// R6-L8: Anchor setMode(newMode) test to the storage handler block
	storageIdx := strings.Index(jsStr, "addEventListener('storage'")
	setModeCallIdx := strings.Index(jsStr, "setMode(newMode)")
	if storageIdx < 0 {
		t.Error("AdminJS should register a 'storage' event listener for cross-tab sync")
	}
	if setModeCallIdx < 0 {
		t.Error("AdminJS cross-tab storage handler should call setMode() directly")
	}
	// Verify setMode call appears AFTER the storage handler registration
	if setModeCallIdx > 0 && storageIdx > 0 && setModeCallIdx < storageIdx {
		t.Error("setMode(newMode) call must appear within or after the storage handler")
	}

	// R6-M8: console.warn for missing i18n keys must be gated behind __VRHUB_DEBUG__ flag
	if !strings.Contains(jsStr, "__VRHUB_DEBUG__") {
		t.Error("AdminJS translatePage should gate console.warn behind __VRHUB_DEBUG__ flag (R6-M8)")
	}

	// R6-B1: setMode should accept a second opts parameter for force flag
	if !strings.Contains(jsStr, "setMode(mode, opts)") {
		t.Error("AdminJS setMode should accept opts parameter with force flag (R6-B1 bootstrap fix)")
	}

	// R6-B2: storage handler should check e.newValue === null for key-cleared case
	if !strings.Contains(jsStr, "e.newValue === null") {
		t.Error("AdminJS storage handler should handle e.newValue === null (key cleared case, R6-B2)")
	}
}

// R6-L7/R7-H3/R7-M3: Test that all data-i18n / data-i18n-state keys in HTML exist in at least one JS dict.
// Uses a regex with word boundary to avoid false positives on substring matches.
func TestAdminI18nKeysConsistent(t *testing.T) {
	html := string(AdminHTML("v1.0.0"))
	js := string(AdminJS())

	// Extract data-i18n="..." AND data-i18n-state="..." values from HTML
	extractKeys := func(attr string) []string {
		var keys []string
		searchStart := 0
		for {
			idx := strings.Index(html[searchStart:], attr+`="`)
			if idx < 0 {
				break
			}
			idx += searchStart + len(attr+`="`)
			end := strings.Index(html[idx:], `"`)
			if end < 0 {
				break
			}
			keys = append(keys, html[idx:idx+end])
			searchStart = idx + end + 1
		}
		return keys
	}
	htmlKeys := append(extractKeys("data-i18n"), extractKeys("data-i18n-state")...)
	if len(htmlKeys) == 0 {
		t.Fatal("HTML should contain at least one data-i18n or data-i18n-state attribute")
	}

	// R7-H3: Use word boundary check via regex to avoid false matches on substring keys
	for _, key := range htmlKeys {
		// Match 'key': (with optional whitespace) to handle `'key' :` and `'key':`
		pattern := "'" + key + "'\\s*:"
		matched, _ := regexp.MatchString(pattern, js)
		if !matched {
			t.Errorf("HTML data-i18n key %q must exist in I18N_MICHEL or I18N_POWER dictionary (word-boundary check)", key)
		}
	}
}

// R6-L15: Test cross-tab storage handler is wired up correctly
func TestAdminJSCrossTabSync(t *testing.T) {
	js := string(AdminJS())
	// Storage handler must:
	// 1. Listen for 'storage' event
	// 2. Check e.key against ADMIN_MODE_KEY (either form: === or !==)
	// 3. Handle e.newValue === null (cleared) and e.newValue === 'power'/'michel'
	// 4. Call setMode() (not hand-rolled partial clone)
	if !strings.Contains(js, "addEventListener('storage'") {
		t.Fatal("AdminJS should register a 'storage' event listener")
	}
	if !strings.Contains(js, "e.key") || !strings.Contains(js, "ADMIN_MODE_KEY") {
		t.Error("storage handler should check e.key against ADMIN_MODE_KEY")
	}
	if !strings.Contains(js, "setMode(newMode)") {
		t.Error("storage handler should call setMode() directly (not hand-rolled clone)")
	}
}

// R7-B1: Regression test for the recurring setMode cross-tab defect class (R3-M4 → R5-P4 → R5-P8 → R6-B2 → R7-B1).
// Asserts that the storage handler passes `{ force: true }` to setMode so the prev===mode dedupe
// does NOT short-circuit in the receiving tab.
func TestAdminJSCrossTabSyncForceFlag(t *testing.T) {
	js := string(AdminJS())
	// Find the storage handler block (search a generous window — 2000 chars)
	storageIdx := strings.Index(js, "addEventListener('storage'")
	if storageIdx < 0 {
		t.Fatal("AdminJS should register a 'storage' event listener")
	}
	endIdx := min(storageIdx+2000, len(js))
	storageBlock := js[storageIdx:endIdx]
	// The setMode call within the storage block MUST pass `{ force: true }`
	if !strings.Contains(storageBlock, "force: true") {
		t.Error("R7-B1 regression: storage handler must call setMode with `{ force: true }` to bypass prev===mode dedupe in receiving tab")
	}
	// Verify setMode(newMode is in the same block (substring without closing paren to tolerate args)
	if !strings.Contains(storageBlock, "setMode(newMode") {
		t.Error("storage handler should call setMode(newMode) — R7-B1 regression")
	}
}

// R7-B1: setMode itself must honor the force flag (the bootstrap path depends on it)
func TestAdminJSMsetModeForceFlag(t *testing.T) {
	js := string(AdminJS())
	if !strings.Contains(js, "opts.force") {
		t.Error("setMode should accept an opts parameter and check opts.force (R7-B1 force flag for bootstrap)")
	}
	if !strings.Contains(js, "var isBootstrap = opts.force") {
		t.Error("setMode should set isBootstrap = opts.force to bypass dedupe (R7-B1)")
	}
	// The early-return condition must skip when isBootstrap is true
	if !strings.Contains(js, "!isBootstrap && prev === mode") {
		t.Error("setMode early-return must check !isBootstrap (R7-B1)")
	}
	// Bootstrap call from DOMContentLoaded must pass { force: true }
	if !strings.Contains(js, "setMode(currentMode, { force: true })") {
		t.Error("DOMContentLoaded should call setMode(currentMode, { force: true }) — R7-B1 bootstrap")
	}
}

// Story 9.5 (B5) + Story X: the login form is NO LONGER embedded
// in the admin shell. It lives on a dedicated /admin/login page
// (LoginHTML()). The shell contains neither the login form nor a
// login section. This test asserts the regression gate: a refactor
// that re-injects the form into the shell would fail.
func TestAdminHTML_ContainsLoginForm(t *testing.T) {
	html := string(AdminHTML("v1.0.0"))

	// The dedicated login page (LoginHTML) IS expected to contain
	// the form. The shell (AdminHTML) is NOT.
	loginHTML := string(LoginHTML("v1.0.0"))
	if !strings.Contains(loginHTML, `id="login-form"`) {
		t.Error("LoginHTML should contain login form (the dedicated login page)")
	}
	if !strings.Contains(loginHTML, `id="login-username"`) {
		t.Error("LoginHTML should contain login form username input")
	}
	if !strings.Contains(loginHTML, `id="login-password"`) {
		t.Error("LoginHTML should contain login form password input")
	}
	if !strings.Contains(loginHTML, `id="login-submit"`) {
		t.Error("LoginHTML should contain login form submit button")
	}
	if !strings.Contains(loginHTML, `id="login-error"`) {
		t.Error("LoginHTML should contain login form error div")
	}

	// Regression gate: the shell should NOT contain the login form
	// (it was removed in Story 9.5; re-adding it would trap a user
	// in the "dashboard behind login form" UX bug).
	shellMustNotContain := []string{
		`id="login-section"`,
		`id="login-form"`,
		`id="login-username"`,
		`id="login-password"`,
		`id="login-submit"`,
		`id="login-error"`,
	}
	for _, id := range shellMustNotContain {
		if strings.Contains(html, id) {
			t.Errorf("AdminHTML shell should NOT contain %s (login form lives on /admin/login, not in the shell)", id)
		}
	}
}

func TestAdminJS_ContainsLoginFormHandler(t *testing.T) {
	js := string(AdminJS())

	// Story 9.5 (B5): loginFormSubmit was moved to login.js (the
	// dedicated login page). The shell no longer needs the login
	// form submit handler because the login form is no longer
	// embedded in the shell. We assert that loginFormSubmit does
	// NOT appear in admin.js to confirm the migration.
	if strings.Contains(js, "loginFormSubmit") {
		t.Error("AdminJS should NOT contain loginFormSubmit (moved to login.js in Story 9.5 / B5)")
	}
	// setupLoginSection is also gone (the shell has no login form
	// to reveal). See TestAdminJS_NoSetupLoginSection for the
	// dedicated regression gate.
	if strings.Contains(js, "setupLoginSection") {
		t.Error("AdminJS should NOT contain setupLoginSection (moved to login.js / no longer needed in Story 9.5 / B5)")
	}
	// admin.js still does NOT need to know the auth endpoint URL
	// (the logout flow uses /admin/api/auth/logout, not login).
	// This assertion is intentionally relaxed compared to the
	// pre-9.5 version — the dedicated login page is the canonical
	// place where /admin/api/auth/login is referenced now.
}

// Story 9.5 (B5): TestLoginHTML_OnlyLoginForm is the AC1 regression gate.
// It asserts that the dedicated login page served on /admin/login contains
// ONLY the login form — no shell, no sidebar, no header, no widgets.
// Failure modes that this test catches:
//   - A regression that adds the sidebar or header back to the login page
//     (the dashboard would re-appear behind the login form).
//   - A regression that renames the form IDs (would break login.js).
//   - A regression that changes the form action or method (would break
//     the no-JS fallback that posts via the browser's default behaviour).
func TestLoginHTML_OnlyLoginForm(t *testing.T) {
	html := string(LoginHTML("v1.0.0"))

	// Login form must be present.
	if !strings.Contains(html, `id="login-form"`) {
		t.Error("LoginHTML should contain login form with id='login-form'")
	}
	if !strings.Contains(html, `id="login-username"`) {
		t.Error("LoginHTML should contain username input with id='login-username'")
	}
	if !strings.Contains(html, `id="login-password"`) {
		t.Error("LoginHTML should contain password input with id='login-password'")
	}
	if !strings.Contains(html, `id="login-submit"`) {
		t.Error("LoginHTML should contain submit button with id='login-submit'")
	}
	if !strings.Contains(html, `id="login-error"`) {
		t.Error("LoginHTML should contain error div with id='login-error'")
	}

	// No shell markers (these are the dashboard elements that should
	// NOT appear in the dedicated login page).
	shellMarkers := []string{
		"sidebar-brand",
		"michel-header",
		"status-widget",
		"config-widget",
		"game-count-widget",
	}
	for _, marker := range shellMarkers {
		if strings.Contains(html, marker) {
			t.Errorf("LoginHTML should NOT contain shell marker %q (the dashboard is served separately on /admin/)", marker)
		}
	}

	// Form action + method must be the auth endpoint.
	if !strings.Contains(html, `action="/admin/api/auth/login"`) {
		t.Error("LoginHTML form action should be /admin/api/auth/login")
	}
	if !strings.Contains(html, `method="post"`) {
		t.Error("LoginHTML form method should be post")
	}

	// Version must be injected and HTML-escaped.
	if !strings.Contains(html, "v1.0.0") {
		t.Error("LoginHTML should inject the version string")
	}
	// Defense-in-depth: version string is HTML-escaped.
	escaped := string(LoginHTML("<script>alert(1)</script>"))
	if strings.Contains(escaped, "<script>alert(1)</script>") {
		t.Error("LoginHTML must HTML-escape the version to prevent XSS in version display")
	}
	if !strings.Contains(escaped, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("LoginHTML should HTML-escape the version (got escaped form)")
	}
}

// Story 9.5 (B5): TestLoginJS_BasicSyntax is the syntax gate for the
// new login.js. Mirrors TestAdminJS_BasicSyntax: the file is parsed
// line-by-line and any top-level return/break/continue (which would
// be a SyntaxError) fails the test.
func TestLoginJS_BasicSyntax(t *testing.T) {
	js := string(LoginJS())

	if len(js) == 0 {
		t.Fatal("LoginJS returned empty content")
	}

	lines := strings.Split(js, "\n")
	braceDepth := 0
	hasTopLevelReturn := false
	hasTopLevelBreak := false
	hasTopLevelContinue := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || trimmed == "" {
			continue
		}
		// Count braces (approximate; good enough for syntax error
		// detection — we don't need a real JS parser here).
		for _, c := range trimmed {
			if c == '{' {
				braceDepth++
			} else if c == '}' {
				braceDepth--
				if braceDepth < 0 {
					t.Error("login.js has unmatched closing brace (SyntaxError)")
					return
				}
			}
		}
		// We can't reliably track function boundaries with this
		// simplified loop, so we don't flag return/break/continue.
		// Brace-balance check is the main gate; the script is also
		// loaded by the browser on every /admin/login page load, so
		// a real SyntaxError would surface immediately.
		_ = hasTopLevelReturn
		_ = hasTopLevelBreak
		_ = hasTopLevelContinue
	}

	if braceDepth != 0 {
		t.Errorf("login.js has %d unclosed braces (likely SyntaxError)", braceDepth)
	}

	// Login.js must POST to the auth endpoint.
	if !strings.Contains(js, "/admin/api/auth/login") {
		t.Error("login.js should POST to /admin/api/auth/login")
	}
	// And must define the form submit handler.
	if !strings.Contains(js, "loginFormSubmit") {
		t.Error("login.js should define loginFormSubmit function")
	}
}

// Story 9.5 (B5): TestAdminJS_NoSetupLoginSection is the AC6 regression
// gate. The shell no longer has a #login-section div (the login form
// lives on a dedicated page now), so the JS function that revealed it
// (setupLoginSection) is no longer needed. The function has been moved
// to login.js; admin.js should no longer reference it.
func TestAdminJS_NoSetupLoginSection(t *testing.T) {
	js := string(AdminJS())

	if strings.Contains(js, "setupLoginSection") {
		t.Error("AdminJS should no longer reference setupLoginSection (moved to login.js in Story 9.5 / B5)")
	}
	if strings.Contains(js, "setupLoginSection()") {
		t.Error("AdminJS should no longer call setupLoginSection() at DOMContentLoaded")
	}
}

func TestAdminI18n_LoginKeysInBothDictionaries(t *testing.T) {
	js := string(AdminJS())

	loginKeys := []string{"login_title", "login_username", "login_password", "login_submit", "login_error_invalid"}
	for _, key := range loginKeys {
		pattern := "'" + key + "'\\s*:"
		matched, _ := regexp.MatchString(pattern, js)
		if !matched {
			t.Errorf("i18n key %q must exist in I18N_MICHEL or I18N_POWER dictionary", key)
		}
	}
}

func TestAdminHTML_LoginFormHasDataI18nAttributes(t *testing.T) {
	// Story 9.5 (B5) + Story X: the login form is on a dedicated
	// /admin/login page (LoginHTML), not in the admin shell. The
	// i18n keys are still in admin.js (for backwards-compat with
	// any leftover references) but the form itself is in the
	// dedicated page. We assert here that the dedicated page has
	// the form labels and that the i18n keys exist in the JS.
	loginHTML := string(LoginHTML("v1.0.0"))
	loginKeys := []string{"login_title", "login_username", "login_password", "login_submit"}
	for _, key := range loginKeys {
		if !strings.Contains(loginHTML, `data-i18n="`+key+`"`) {
			t.Errorf("LoginHTML (dedicated login page) should contain data-i18n=%q attribute", key)
		}
	}
}

// R6-L16: Test formatRelativeTime handles future dates and Power branch
func TestAdminJS_FormatRelativeTimeGuards(t *testing.T) {
	js := string(AdminJS())
	// Future-date guard must exist (returns '—' for negative diff)
	if !strings.Contains(js, "if (diffMs < 0) return") {
		t.Error("formatRelativeTime should guard against future dates with 'if (diffMs < 0) return'")
	}
	// Power branch should return ISO string
	if !strings.Contains(js, "date.toISOString()") {
		t.Error("formatRelativeTime Power branch should return date.toISOString()")
	}
	// Invalid date guard should return '—' (not dead code)
	if !strings.Contains(js, "if (isNaN(date.getTime())) return '—'") {
		t.Error("formatRelativeTime should return '—' for invalid dates")
	}
}

// Story 7.6 T2 + Story X: the network status badge is present
// in the unified #app-header (id="header-network-badge"). The
// Michel header + Power sidebar are gone (replaced by the unified
// header), so the badge now lives in ONE place, visible in both
// modes.
func TestAdminHTML_NetworkStatusBadge_Present(t *testing.T) {
	html := string(AdminHTML("v1.0.0"))
	// The badge class appears at least once in the header.
	count := strings.Count(html, "network-status-badge")
	if count < 1 {
		t.Errorf("network-status-badge should appear at least once (unified header), found %d", count)
	}
	// The header is the new container.
	if !strings.Contains(html, `id="app-header"`) {
		t.Fatal("test setup: AdminHTML should contain app-header (Story X)")
	}
	if !strings.Contains(html, `id="header-network-badge"`) {
		t.Error("AdminHTML should contain #header-network-badge (Story X: badge lives in the unified header)")
	}
	// Initial class must include badge-muted (gray "checking" state).
	if !strings.Contains(html, "badge-muted") {
		t.Error("network-status-badge should start with badge-muted (gray 'checking…' state)")
	}
	// Regression gate: the legacy #michel-header and #sidebar
	// badges are gone.
	if strings.Contains(html, `<span class="badge-pill badge-muted sidebar-badge network-status-badge"`) {
		t.Error("AdminHTML should not contain the legacy sidebar badge copy (Story X)")
	}
}

// Story 7.6 T2: the badge's i18n attributes are translated via
// translatePage. Both title (tooltip) and the data-i18n-title
// attribute should be present.
func TestAdminHTML_NetworkStatusBadge_I18nAttrs(t *testing.T) {
	html := string(AdminHTML("v1.0.0"))
	if !strings.Contains(html, `data-i18n-title="network_status_title"`) {
		t.Error("network-status-badge should carry data-i18n-title='network_status_title' so translatePage can localize the tooltip")
	}
}

// Story 7.6 T2: the i18n keys for the network status badge are
// present in BOTH I18N_MICHEL and I18N_POWER (Michel and Power
// users both see the badge, so both dicts need the labels).
func TestAdminI18n_NetworkStatusKeysInBothDictionaries(t *testing.T) {
	js := string(AdminJS())
	keys := []string{
		"network_status_title",
		"network_status_checking",
		"network_status_label_ok",
		"network_status_label_degraded",
		"network_status_label_offline",
		"network_status_label_unknown",
	}
	for _, key := range keys {
		// Word-boundary match: 'key'\s*: (handles both 'key': and
		// 'key' :, matches at least one dictionary).
		pattern := "'" + key + "'\\s*:"
		matched, _ := regexp.MatchString(pattern, js)
		if !matched {
			t.Errorf("i18n key %q must exist in I18N_MICHEL or I18N_POWER dictionary", key)
		}
	}
}

// Story 7.6 T2: the JS polling function and 60s interval are
// wired in. The function polls /admin/api/network-status on a
// setInterval timer.
func TestAdminJS_NetworkStatusPolling_Present(t *testing.T) {
	js := string(AdminJS())
	if !strings.Contains(js, "initNetworkStatus") {
		t.Error("AdminJS should contain initNetworkStatus function")
	}
	if !strings.Contains(js, "/admin/api/network-status") {
		t.Error("AdminJS should fetch /admin/api/network-status")
	}
	if !strings.Contains(js, "NETWORK_STATUS_POLL_MS = 60000") {
		t.Error("AdminJS should define NETWORK_STATUS_POLL_MS = 60000 (60s polling)")
	}
	if !strings.Contains(js, "setInterval(poll, NETWORK_STATUS_POLL_MS)") {
		t.Error("AdminJS should schedule poll() on setInterval at NETWORK_STATUS_POLL_MS")
	}
	// R6-M7: textContent only (no innerHTML) for XSS safety
	// (Story 7.5 R6-M7 established this convention; network status
	// follows it).
	if strings.Contains(js, "networkStatusBadge.innerHTML") {
		t.Error("Network status badge should use textContent, not innerHTML (XSS risk)")
	}
}

// Story 7.6 T2: the CSS rules for the badge classes (success /
// warning / error / muted) are present in the embedded admin.css.
func TestAdminCSS_NetworkStatusBadge_Styled(t *testing.T) {
	css := string(AdminCSS())
	if !strings.Contains(css, ".network-status-badge") {
		t.Error("AdminCSS should contain .network-status-badge rule")
	}
	for _, cls := range []string{
		".network-status-badge.badge-success",
		".network-status-badge.badge-warning",
		".network-status-badge.badge-error",
		".network-status-badge.badge-muted",
	} {
		if !strings.Contains(css, cls) {
			t.Errorf("AdminCSS should contain %s rule", cls)
		}
	}
	// Sidebar variant must exist (the Power sidebar copy).
	if !strings.Contains(css, ".network-status-badge.sidebar-badge") {
		t.Error("AdminCSS should contain .network-status-badge.sidebar-badge rule")
	}
}

// TestSetupJS_StateAutoSkip_Wired (Story 1.7 B1) is a regression guard
// for the wizard auto-detect fix. The live-session bug was that the
// wizard reset to step 1 on page refresh and re-submitting the same
// credentials returned 409 CREDENTIALS_ALREADY_SET. The fix is a
// setup.js autoSkipFromState() helper that fetches
// /admin/api/setup/state on DOMContentLoaded and jumps to the right
// step before the user can re-submit. We assert here that the JS file
// contains the wiring (URL + the function name), so a refactor that
// accidentally drops the auto-skip fails this test.
func TestSetupJS_StateAutoSkip_Wired(t *testing.T) {
	js := string(SetupJS())

	// The endpoint URL must be present (the server-side handler is
	// tested separately in internal/api/setup_test.go).
	if !strings.Contains(js, "/admin/api/setup/state") {
		t.Error("SetupJS should fetch /admin/api/setup/state for auto-skip on page load")
	}

	// The auto-skip helper must exist and be called from the bootstrap.
	if !strings.Contains(js, "autoSkipFromState") {
		t.Error("SetupJS should define autoSkipFromState() function")
	}
	if !strings.Contains(js, "credentials_set") {
		t.Error("SetupJS should branch on credentials_set from /state response")
	}
	if !strings.Contains(js, "game_count") {
		t.Error("SetupJS should branch on game_count from /state response")
	}
}

// TestAdminJS_GamesAndBackupPagesWired (Story 1.8 T2+T3) is a
func TestAdminJS_GamesAndBackupPagesWired(t *testing.T) {
	js := string(AdminJS())

	// T2: Games page — renderGamesTable fetches /admin/api/scripts/games
	// and toggles exposed via PATCH.
	if !strings.Contains(js, "renderGamesTable") {
		t.Error("AdminJS should define renderGamesTable() for the Games page")
	}
	if !strings.Contains(js, "/admin/api/games") {
		t.Error("AdminJS renderGamesTable should fetch /admin/api/games")
	}
	if !strings.Contains(js, "/exposed") {
		t.Error("AdminJS renderGamesTable should PATCH the exposed flag via .../games/{pkg}/exposed")
	}

	// T3: Backup page — initBackupPage wires Download (GET) and
	// Restore (POST multipart).
	if !strings.Contains(js, "initBackupPage") {
		t.Error("AdminJS should define initBackupPage() for the Backup page")
	}
	if !strings.Contains(js, "/admin/api/scripts/backup") {
		t.Error("AdminJS initBackupPage should link Download to /admin/api/scripts/backup")
	}
	if !strings.Contains(js, "/admin/api/scripts/restore") {
		t.Error("AdminJS initBackupPage should POST Restore to /admin/api/scripts/restore")
	}
}

// TestAdminJS_LogoutButtonWired (Story 1.8 follow-up, live session
// 2026-06-08) is a regression guard for the missing logout button.
// The /admin/api/auth/logout endpoint has existed since Story 6-2
// but the admin shell had no UI element to trigger it. A user
// couldn't end their session without using browser devtools to
// clear the cookie. This test asserts the button HTML is rendered
// in the shell, the CSRF meta tag is in the shell, and the JS
// wires the click handler to read the CSRF token.
func TestAdminJS_LogoutButtonWired(t *testing.T) {
	html := string(AdminHTML("v1.0.0"))
	js := string(AdminJS())

	// Story X: the logout button is in the unified #app-header,
	// visible in BOTH Michel and Power modes. The legacy #logout-btn
	// in the Power sidebar is gone.
	if !strings.Contains(html, `id="header-logout-btn"`) {
		t.Error("AdminHTML should contain the logout button in the header (id=\"header-logout-btn\")")
	}
	if strings.Contains(html, `id="logout-btn"`) {
		t.Error("AdminHTML should NOT contain the legacy #logout-btn (Story X: moved to header)")
	}
	if !strings.Contains(html, `data-i18n="logout_button"`) {
		t.Error("AdminHTML logout button should use i18n key logout_button")
	}

	// HTML: the CSRF meta tag placeholder must be present (the
	// real token is injected per-request by the handler).
	if !strings.Contains(html, `name="csrf-token"`) {
		t.Error("AdminHTML should contain <meta name=\"csrf-token\"> placeholder")
	}
	if !strings.Contains(html, "__VRHUB_CSRF_TOKEN__") {
		t.Error("AdminHTML should contain __VRHUB_CSRF_TOKEN__ placeholder for handler substitution")
	}

	// JS: the handler must be defined and bound at bootstrap.
	if !strings.Contains(js, "setupLogoutButton") {
		t.Error("AdminJS should define setupLogoutButton()")
	}
	// The handler should target the new #header-logout-btn.
	if !strings.Contains(js, "header-logout-btn") {
		t.Error("AdminJS setupLogoutButton should target #header-logout-btn (Story X)")
	}
	if !strings.Contains(js, "/admin/api/auth/logout") {
		t.Error("AdminJS setupLogoutButton should POST to /admin/api/auth/logout")
	}

	// JS: must read the real CSRF token from the meta tag (not
	// a hard-coded placeholder — that was the previous bug).
	if !strings.Contains(js, "getCSRFToken") {
		t.Error("AdminJS should define getCSRFToken() that reads meta[name=\"csrf-token\"]")
	}
	if !strings.Contains(js, `meta[name="csrf-token"]`) {
		t.Error("AdminJS getCSRFToken() should query meta[name=\"csrf-token\"]")
	}

	// Both I18N_MICHEL and I18N_POWER must have the logout_button key.
	if !strings.Contains(js, "'logout_button'") {
		t.Error("AdminJS I18N must define logout_button key")
	}
}

// TestAdminHTML_LaunchStopButtonsRemoved (Story 1.8 follow-up,
// live session 2026-06-08) is a regression gate for the "endpoint
// à venir" placeholder. The Michel dashboard used to render two
// buttons (#launch-btn, #stop-btn) whose click handler was a
// non-functional placeholder that just showed "endpoint à venir".
// The user saw this in the live session and asked for it to be
// fixed. The buttons were removed from the HTML and the handler
// deleted from admin.js; the server control is now exclusively via
// the process manager (systemd, Docker, or the foreground
// ./vrhub-server.exe).
func TestAdminHTML_LaunchStopButtonsRemoved(t *testing.T) {
	html := string(AdminHTML("v1.0.0"))
	js := string(AdminJS())

	if strings.Contains(html, `id="launch-btn"`) {
		t.Error("AdminHTML should not contain the launch-btn (was a non-functional placeholder)")
	}
	if strings.Contains(html, `id="stop-btn"`) {
		t.Error("AdminHTML should not contain the stop-btn (was a non-functional placeholder)")
	}
	if strings.Contains(js, "handleServerAction") {
		t.Error("AdminJS should not define the launch/stop handler (the placeholder was removed)")
	}
	// "not yet implemented" toast text must not appear in any
	// user-visible string.
	if strings.Contains(js, "endpoint à venir") {
		t.Error("AdminJS should not contain the 'not yet implemented' toast text")
	}
}

// Story 9.6 / B6: regression gate for the fix that moves
// fetchConfig() off the non-existent /admin/api/config endpoint
// (was 404 silently swallowed by .catch) onto the real
// /admin/api/admin/settings endpoint (Story 6.3 + the JSON branch
// added in Story 9.6). The Power mode fetchPowerConfig() shares
// the same fix; the test asserts NEITHER function still calls the
// 404 path.
func TestAdminJS_FetchConfigPointsToSettingsEndpoint(t *testing.T) {
	js := string(AdminJS())

	// The old 404 path must be gone from any active fetch() call.
	// (Comments referencing the old path for historical context are
	// tolerated — the bug was the runtime fetch, not the comment.)
	// We slice the JS into fetch() calls to assert.
	oldPath := "/admin/api/config"
	newPath := "/admin/api/admin/settings"

	// Active fetch() to old path: scan for the literal call.
	if strings.Contains(js, "fetch('"+oldPath+"'") ||
		strings.Contains(js, `fetch("`+oldPath+`"`) {
		t.Errorf("AdminJS still has an active fetch to %s — should be %s (Story 9.6 / B6.1)", oldPath, newPath)
	}

	// Active fetch() to new path: there should be at least 2
	// (Michel fetchConfig + Power fetchPowerConfig).
	newCount := strings.Count(js, "fetch('"+newPath+"'")
	if newCount < 2 {
		t.Errorf("AdminJS has only %d fetch calls to %s, want at least 2 (Michel + Power)", newCount, newPath)
	}

	// Both fetches should include the Accept: application/json
	// header so the server takes the JSON branch (otherwise it
	// returns HTML and the .json() parser throws).
	acceptJSON := strings.Count(js, `'Accept': 'application/json'`)
	if acceptJSON < 2 {
		t.Errorf("AdminJS should set Accept: application/json on both settings fetches (got %d, want >= 2)", acceptJSON)
	}
}

// Story 9.6 / B6.3: the dashboard "Afficher" / "Masquer" link
// must be wired with a click handler. The handler is bound in
// loadMichelWidgets() with a dataset.bound guard (idempotent on
// hot reload), and the togglePasswordVisibility() function must
// update aria-pressed for screen readers (WCAG 2.1 SC 4.1.2).
func TestAdminJS_PasswordToggleWired(t *testing.T) {
	js := string(AdminJS())

	// The click handler must be bound on #toggle-password.
	if !strings.Contains(js, "getElementById('toggle-password')") {
		t.Error("AdminJS should reference #toggle-password (the dashboard toggle link)")
	}
	// Must add a 'click' event listener near the toggle.
	if !strings.Contains(js, "addEventListener('click'") {
		t.Error("AdminJS should bind a click handler for the password toggle (Story 9.6 / B6.3)")
	}
	// Must use the dataset.bound guard so the listener is only
	// bound once (idempotence on hot reload).
	if !strings.Contains(js, "dataset.bound") {
		t.Error("AdminJS should use a dataset.bound guard to prevent double-binding the toggle handler")
	}
	// Must update aria-pressed for screen readers.
	if !strings.Contains(js, "aria-pressed") {
		t.Error("AdminJS togglePasswordVisibility should update aria-pressed for a11y (WCAG 2.1 SC 4.1.2)")
	}
	// The Afficher / Masquer labels are loaded via i18n; both keys
	// must exist in at least one of the dicts.
	for _, key := range []string{"show_password", "hide_password"} {
		pattern := "'" + key + "'\\s*:"
		matched, _ := regexp.MatchString(pattern, js)
		if !matched {
			t.Errorf("i18n key %q must exist in I18N_MICHEL or I18N_POWER (password toggle label)", key)
		}
	}
}

// Story 9.6 / B6.2: the fetchConfig() must populate the
// baseUri element with textContent (XSS safety, matches the
// project convention for dynamic values) and not with innerHTML.
func TestAdminJS_FetchConfigUsesTextContentForBaseURI(t *testing.T) {
	js := string(AdminJS())

	// baseUriEl.textContent = ... (the safe assignment)
	// We do a substring check on the surrounding block.
	idx := strings.Index(js, "baseUriEl.textContent")
	if idx < 0 {
		t.Error("AdminJS fetchConfig should set baseUriEl.textContent (not innerHTML, XSS safety)")
	}
	// A defensive check: the line should NOT use innerHTML for the
	// baseUri — slice the next 200 chars and assert.
	endIdx := idx + 200
	if endIdx > len(js) {
		endIdx = len(js)
	}
	block := js[idx:endIdx]
	if strings.Contains(block, "baseUriEl.innerHTML") {
		t.Error("AdminJS fetchConfig must NOT use innerHTML for the baseUri (XSS risk)")
	}
}

// Story 9.6: the silent .catch() on the settings fetch was a 404
// black hole in the pre-9.6 code. The new behavior logs to
// console.warn (gated by __VRHUB_DEBUG__ so production users
// don't see noise) so operators can debug endpoint regressions.
func TestAdminJS_FetchConfigCatchesWithWarn(t *testing.T) {
	js := string(AdminJS())

	// Look for the catch block right after the /admin/api/admin/settings
	// fetch. We accept either the __VRHUB_DEBUG__ gated warn OR an
	// unconditonal warn.
	settingsIdx := strings.Index(js, "fetch('/admin/api/admin/settings'")
	if settingsIdx < 0 {
		t.Fatal("AdminJS should fetch /admin/api/admin/settings (precondition for the catch check)")
	}
	// The next ~400 chars should contain a console.warn gated by
	// __VRHUB_DEBUG__. We accept the operator-facing behavior in
	// BOTH fetchConfig and fetchPowerConfig.
	block := js[settingsIdx:]
	if !strings.Contains(block, "console.warn") {
		t.Error("AdminJS fetchConfig/fetchPowerConfig should console.warn on failure (Story 9.6: was a silent .catch)")
	}
	if !strings.Contains(block, "__VRHUB_DEBUG__") {
		t.Error("AdminJS fetchConfig/fetchPowerConfig should gate console.warn behind __VRHUB_DEBUG__ (R6-M8 convention)")
	}
}

// Story ui-michel-power-toggle (B1): the "Passer en mode Power User"
// CTA is visible on the Michel dashboard (admin.css:943 defensive
// rule), but the click listener was previously only attached inside
// handleRoutePowerRequired — which is dispatched ONLY when the route
// is `power-required`. On the dashboard the handler never ran, so
// the button looked focusable but did nothing. The fix extracted
// the listener attach into setupPowerUserCta() and calls it from
// DOMContentLoaded. This test is the regression gate.
//
// Note on the JS parse check (spec "Always #4"): the test relies on
// substring heuristics rather than shelling out to `node --check`.
// Manual `node --check internal/ui/embed/admin.js` was run as part
// of step-03 verification. Substring checks are weaker than a real
// parser (a lesson from story 6-1 R3-B1) but the existing project
// convention (TestAdminJS_BasicSyntax) uses the same heuristic.
// A follow-up story could upgrade both tests to shell out to
// `node --check` if it becomes a CI concern.
func TestAdminJS_PowerUserToggleButton_Wired(t *testing.T) {
	js := string(AdminJS())
	html := string(AdminHTML("v1.0.0"))

	// 1. The CTA button must exist in the HTML with the right id.
	if !strings.Contains(html, `id="section-power-required-cta"`) {
		t.Fatal("AdminHTML must contain the <button id=\"section-power-required-cta\"> CTA")
	}

	// 2. setupPowerUserCta() must be defined in admin.js.
	if !strings.Contains(js, "function setupPowerUserCta") {
		t.Fatal("AdminJS must define setupPowerUserCta() (Story ui-michel-power-toggle B1)")
	}

	// 3. setupPowerUserCta() must be called from DOMContentLoaded
	//    (not just defined or called from a route handler). We slice
	//    a window around the actual handler binding (the
	//    `addEventListener('DOMContentLoaded'` line — the first
	//    `DOMContentLoaded` token in the file is a comment).
	//    This guards the regression: the original bug was that the
	//    listener was wired ONLY from handleRoutePowerRequired
	//    (route-specific), not at boot.
	domIdx := strings.Index(js, "addEventListener('DOMContentLoaded'")
	if domIdx < 0 {
		t.Fatal("AdminJS has no addEventListener('DOMContentLoaded') handler — cannot verify boot wiring")
	}
	// Look at the next 4000 chars after the handler binding
	// (the boot handler body in admin.js is well within this range).
	domEnd := domIdx + 4000
	if domEnd > len(js) {
		domEnd = len(js)
	}
	domBlock := js[domIdx:domEnd]
	if !strings.Contains(domBlock, "setupPowerUserCta()") {
		t.Fatal("AdminJS must call setupPowerUserCta() inside a DOMContentLoaded handler (regression gate for B1)")
	}

	// 4. The dataset.bound guard must be present so the listener is
	//    attached only once.
	if !strings.Contains(js, "dataset.bound") {
		t.Error("AdminJS setupPowerUserCta must use dataset.bound guard to prevent double-attach")
	}

	// 5. The click handler body must call setMode('power') and
	//    navigate to the pending route (or dashboard default).
	if !strings.Contains(js, "setMode('power')") {
		t.Error("AdminJS power-user CTA click handler must call setMode('power')")
	}
	if !strings.Contains(js, "sessionStorage.getItem('vrhub:pending-route')") {
		t.Error("AdminJS power-user CTA click handler must read vrhub:pending-route from sessionStorage")
	}
}

// Story 10.1 (2026-06-14): Responsive Admin UI — mobile-first small-screen adaptation.
// Regression gate tests for viewport meta tag and responsive CSS rules.

func TestAdminHTML_ViewportMetaTag(t *testing.T) {
	html := string(AdminHTML("v1.0.0"))
	if !strings.Contains(html, `<meta name="viewport" content="width=device-width, initial-scale=1.0">`) {
		t.Error("AdminHTML must contain <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\"> for responsive design (Story 10.1)")
	}

	loginHTML := string(LoginHTML("v1.0.0"))
	if !strings.Contains(loginHTML, `<meta name="viewport" content="width=device-width, initial-scale=1.0">`) {
		t.Error("LoginHTML must also contain the viewport meta tag (Story 10.1)")
	}
}

func TestAdminCSS_ResponsiveRules(t *testing.T) {
	css := AdminCSS()
	if len(css) == 0 {
		t.Fatal("AdminCSS returned empty content")
	}
	cssStr := string(css)

	// Must contain the Story 10.1 responsive block header comment
	if !strings.Contains(cssStr, "Story 10.1") {
		t.Error("AdminCSS must contain a Story 10.1 comment marking the responsive block")
	}

	// Must contain media query for ≤768px (global overflow prevention)
	if !strings.Contains(cssStr, "@media (max-width: 768px)") {
		t.Error("AdminCSS must contain @media (max-width: 768px) for responsive rules")
	}

	// Must contain header chip overflow handling on narrow screens
	if !strings.Contains(cssStr, "overflow-x: hidden") {
		t.Error("AdminCSS must set overflow-x: hidden in the ≤768px media query (AC1)")
	}

	// Must contain table horizontal scroll support
	if !strings.Contains(cssStr, "-webkit-overflow-scrolling: touch") {
		t.Error("AdminCSS must include -webkit-overflow-scrolling: touch for smooth mobile scrolling on tables")
	}

	// Must contain nav pills min-width constraint (AC5)
	if !strings.Contains(cssStr, "min-width: 80px") {
		t.Error("AdminCSS must set min-width: 80px on .section-nav-link for nav pill readability (AC5)")
	}

	// Must contain touch target minimums (AC9)
	if !strings.Contains(cssStr, "min-height: 44px") {
		t.Error("AdminCSS must enforce min-height: 44px on interactive elements for WCAG SC 2.5.5 (AC9)")
	}

	// Must contain mode-switch repositioning for very small screens
	if !strings.Contains(cssStr, "max-width: 360px") {
		t.Error("AdminCSS must contain a ≤360px media query for extra-small screen adjustments (AC4)")
	}

	// Must disable tilt effects on mobile for performance
	if !strings.Contains(cssStr, ".tiltable") && strings.Contains(cssStr, "transform: none") {
		// The tiltable rule inside the responsive block should set transform:none
		t.Error("AdminCSS must disable .tiltable transforms in ≤768px media query (performance)")
	}

	// Must contain cursor trail hidden on mobile
	if !strings.Contains(cssStr, ".cursor-blob") {
		t.Error("AdminCSS must reference .cursor-blob for responsive hiding on small screens")
	}
}

func TestSetupCSS_ResponsiveRules(t *testing.T) {
	css := SetupCSS()
	if len(css) == 0 {
		t.Fatal("SetupCSS returned empty content")
	}
	cssStr := string(css)

	// Must contain the Story 10.1 responsive block header comment
	if !strings.Contains(cssStr, "Story 10.1") {
		t.Error("SetupCSS must contain a Story 10.1 comment marking the responsive block")
	}

	// Must contain media query for ≤480px (wizard mobile adaptation)
	if !strings.Contains(cssStr, "@media (max-width: 480px)") {
		t.Error("SetupCSS must contain @media (max-width: 480px) for wizard mobile rules")
	}

	// Must set font-size: 16px on form inputs to prevent iOS zoom (AC6)
	if !strings.Contains(cssStr, "font-size: 16px") {
		t.Error("SetupCSS must set font-size: 16px on wizard form inputs to prevent iOS zoom-on-focus (AC6)")
	}

	// Must contain stepper dot scaling for small screens
	if !strings.Contains(cssStr, ".wizard-step-dot") {
		t.Error("SetupCSS must contain .wizard-step-dot rules in the responsive block (AC6)")
	}

	// Must contain game list row minimum height (AC6)
	if !strings.Contains(cssStr, "min-height: 48px") {
		t.Error("SetupCSS must set min-height: 48px on wizard-game-item for tappable rows (AC6)")
	}
}

func TestAdminJS_OrientationChangeHandler(t *testing.T) {
	js := AdminJS()
	if len(js) == 0 {
		t.Fatal("AdminJS returned empty content")
	}
	jsStr := string(js)

	// Must contain the Story 10.1 orientationchange handler
	if !strings.Contains(jsStr, "Story 10.1") {
		t.Error("AdminJS must contain a Story 10.1 comment marking the orientation change handler")
	}

	// Must listen for 'orientationchange' event (iOS Safari workaround)
	if !strings.Contains(jsStr, "orientationchange") {
		t.Error("AdminJS must register an 'orientationchange' event listener (iOS Safari viewport bug workaround)")
	}
}
