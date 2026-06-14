package api

import (
	"net/http"
	"strings"

	"github.com/LeGeRyChEeSe/vrhub-server/internal/ui"
	"github.com/LeGeRyChEeSe/vrhub-server/pkg/types"
)

// RegisterSetupHTMLRenderer wires the /admin/setup wizard HTML
// renderer into internal/ui. Called from SetupRouter (router.go) so
// the renderer is set before any request can hit /admin/setup.
//
// Why this mirrors RegisterDocsHTMLRenderer (Story 6.6) and
// RegisterStatsHTMLRenderer (Story 7.5) and is NOT a package init():
//   - Tests that import internal/ui without internal/api would
//     otherwise see ui.AdminSetupHTML() == nil and silently get an
//     empty 200 body (the very contract being defended).
//   - Package init() ordering is implicit and surprising; SetupRouter
//     is an explicit entry point.
//
// Story 1.6.
func RegisterSetupHTMLRenderer() {
	ui.SetAdminSetupRenderer(adminSetupHTMLBytes)
}

// adminSetupHTMLBytes renders the /admin/setup wizard page. The
// page is a self-contained 4-step wizard (credentials → folder →
// review → launch) that calls the existing /admin/api/setup/*
// endpoints. The actual data flow and submission logic lives in
// internal/ui/embed/setup.js; this HTML is the static shell.
//
// Design decisions (locked in the plan, see
// _bmad-output/implementation-artifacts/stories/1-6-setup-wizard-ui.md):
//   - One HTML page, 4 <section> elements with .hidden toggling.
//     No hash routing; back/next buttons drive state.
//   - Reuses admin.css design tokens (--accent, --bg, --surface,
//     --border, --text-muted, --space-*, --radius-*) and the
//     existing .btn, .form-group, .form-input, .card, .hidden
//     classes. New wizard-specific classes live in setup.css.
//   - admin.js loads BEFORE setup.js so the __VRHUB_I18N__
//     global is exposed before setup.js consumes it.
//   - <noscript> fallback points to curl as a manual setup path.
func adminSetupHTMLBytes() []byte {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="fr">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>VRHub Server - Setup</title>
<link rel="stylesheet" href="/admin/static/admin.css">
<link rel="stylesheet" href="/admin/static/setup.css">
</head>
<body>

<noscript>
<div class="wizard-overlay" style="display:flex;align-items:center;justify-content:center;text-align:center;padding:2rem;">
<div class="card" style="max-width:480px;">
<h1>JavaScript est requis</h1>
<p>L'assistant de configuration nécessite JavaScript pour fonctionner. Vous pouvez aussi configurer le serveur manuellement via les endpoints <code>/admin/api/setup/*</code> en utilisant <code>curl</code> ou un outil similaire.</p>
<p>Endpoints disponibles&nbsp;:</p>
<ul style="text-align:left;">
<li><code>POST /admin/api/setup/credentials</code> — nom d'utilisateur + mot de passe</li>
<li><code>POST /admin/api/setup/scan</code> — dossier de jeux (APK + OBB)</li>
<li><code>GET /admin/api/setup/review</code> — liste des jeux détectés</li>
<li><code>POST /admin/api/setup/review</code> — exclusions</li>
<li><code>POST /admin/api/setup/launch</code> — lancer le serveur</li>
</ul>
</div>
</div>
</noscript>

<div class="wizard-overlay" id="wizard-overlay">
<div class="wizard-container">

<!-- Stepper: 5 dots + 4 connectors -->
<div class="wizard-stepper" role="navigation" aria-label="Étapes de configuration">
<span class="wizard-step-dot" id="stepper-dot-1" aria-label="Étape 1 : Identifiants">1</span>
<span class="wizard-step-connector" id="stepper-conn-1"></span>
<span class="wizard-step-dot" id="stepper-dot-2" aria-label="Étape 2 : Mot de passe d'archive">2</span>
<span class="wizard-step-connector" id="stepper-conn-2"></span>
<span class="wizard-step-dot" id="stepper-dot-3" aria-label="Étape 3 : Dossier">3</span>
<span class="wizard-step-connector" id="stepper-conn-3"></span>
<span class="wizard-step-dot" id="stepper-dot-4" aria-label="Étape 4 : Sélection">4</span>
<span class="wizard-step-connector" id="stepper-conn-4"></span>
<span class="wizard-step-dot" id="stepper-dot-5" aria-label="Étape 5 : Lancement">5</span>
</div>

<!-- Step 1: Credentials -->
<section id="step-1" class="wizard-section" aria-labelledby="step-1-title">
<h1 class="wizard-step-title" id="step-1-title" data-i18n="setup_step1_title">Créer les identifiants administrateur</h1>
<p class="wizard-step-subtitle" data-i18n="setup_step1_subtitle">Définissez le nom d'utilisateur et le mot de passe pour accéder à l'administration.</p>
<form id="step-1-form" novalidate>
<div class="form-group">
<label class="form-label" for="setup-username" data-i18n="setup_step1_username">Nom d'utilisateur</label>
<input class="form-input" type="text" id="setup-username" name="username" required maxlength="256" autocomplete="username" autofocus>
</div>
<div class="form-group">
<label class="form-label" for="setup-password" data-i18n="setup_step1_password">Mot de passe</label>
<input class="form-input" type="password" id="setup-password" name="password" required maxlength="72" autocomplete="new-password" minlength="4">
</div>
<div class="wizard-actions">
<button type="submit" id="step-1-submit" class="btn btn-primary" data-i18n="setup_step1_submit">Continuer</button>
</div>
</form>
</section>

<!-- Step 2: Archive password -->
<section id="step-2" class="wizard-section hidden" aria-labelledby="step-2-title">
<h1 class="wizard-step-title" id="step-2-title" data-i18n="setup_step2_title_archive">Mot de passe d'archive</h1>
<p class="wizard-step-subtitle" data-i18n="setup_step2_subtitle_archive">Définissez le mot de passe utilisé par le client VRHub pour déchiffrer le catalogue (meta.7z).</p>
<form id="step-2-form" novalidate>
<div class="form-group">
<label class="form-label" for="setup-archive-password" data-i18n="setup_step2_archive_label">Mot de passe d'archive</label>
<div style="display:flex;gap:0.5rem;">
<input class="form-input" type="password" id="setup-archive-password" name="archive_password" required minlength="8" maxlength="64" autocomplete="new-password" style="flex:1;">
<button type="button" id="step-2-toggle-password" class="btn btn-secondary" data-i18n="setup_show_password">Afficher</button>
</div>
</div>
<div class="form-group">
<label class="form-label" for="setup-archive-password-length" data-i18n="setup_step2_length_label">Longueur</label>
<input class="form-input" type="range" id="setup-archive-password-length" min="8" max="64" value="24">
<span id="setup-archive-password-length-value">24</span>
</div>
<div class="wizard-actions">
<button type="button" id="step-2-generate" class="btn btn-secondary" data-i18n="setup_step2_generate">Générer</button>
<button type="button" id="step-2-back" class="btn btn-secondary" data-i18n="setup_back">Retour</button>
<button type="submit" id="step-2-submit" class="btn btn-primary" data-i18n="setup_step2_submit_archive">Continuer</button>
</div>
</form>
</section>

<!-- Step 3: Game folder -->
<section id="step-3" class="wizard-section hidden" aria-labelledby="step-3-title">
<h1 class="wizard-step-title" id="step-3-title" data-i18n="setup_step3_title_folder">Choisir le dossier de jeux</h1>
<p class="wizard-step-subtitle" data-i18n="setup_step3_subtitle_folder">Indiquez le dossier contenant vos jeux (APK + OBB).</p>
<form id="step-3-form" novalidate>
<div class="form-group">
<label class="form-label" for="setup-folder" data-i18n="setup_step3_placeholder">Dossier</label>
<input class="form-input" type="text" id="setup-folder" name="folder" placeholder="C:\Users\...\Games" required>
</div>
<div class="wizard-actions">
<button type="button" id="step-3-back" class="btn btn-secondary" data-i18n="setup_back">Retour</button>
<button type="submit" id="step-3-submit" class="btn btn-primary" data-i18n="setup_step3_scan">Scanner le dossier</button>
</div>
<p id="step-3-summary" class="wizard-scan-progress hidden" aria-live="polite"></p>
</form>
</section>

<!-- Step 4: Review -->
<section id="step-4" class="wizard-section hidden" aria-labelledby="step-4-title">
<h1 class="wizard-step-title" id="step-4-title" data-i18n="setup_step4_title_review">Sélectionner les jeux</h1>
<p class="wizard-step-subtitle" data-i18n="setup_step4_subtitle_review">Cochez les jeux à rendre accessibles aux clients VR. Décochez pour exclure.</p>
<form id="step-4-form" novalidate>
<div id="step-4-list" class="wizard-game-list" role="list"></div>
<p id="step-4-empty" class="text-muted hidden" data-i18n="setup_step4_empty">Aucun jeu détecté.</p>
<div class="wizard-actions">
<button type="button" id="step-4-back" class="btn btn-secondary" data-i18n="setup_back">Retour</button>
<button type="submit" id="step-4-submit" class="btn btn-primary" data-i18n="setup_step4_continue">Continuer</button>
</div>
</form>
</section>

<!-- Step 5: Launch -->
<section id="step-5" class="wizard-section hidden" aria-labelledby="step-5-title">
<h1 class="wizard-step-title" id="step-5-title" data-i18n="setup_step5_title">Lancer le serveur</h1>
<p class="wizard-step-subtitle" data-i18n="setup_step5_subtitle">Vérifiez les informations ci-dessous puis lancez le serveur.</p>
<form id="step-5-form" novalidate>
<div class="form-group">
<label class="form-label" for="setup-port" data-i18n="setup_step5_port_label">Port du serveur</label>
<input class="form-input" type="number" id="setup-port" name="port" value="39457" min="1" max="65535" required>
</div>
<div class="wizard-actions">
<button type="button" id="step-5-back" class="btn btn-secondary" data-i18n="setup_back">Retour</button>
<button type="submit" id="step-5-submit" class="btn btn-primary" data-i18n="setup_step5_title">Lancer le serveur</button>
</div>
</form>
<div id="step-5-result" class="hidden" aria-live="polite"></div>
</section>

</div>
</div>

<script src="/admin/static/admin.js"></script>
<script src="/admin/static/setup.js"></script>
</body>
</html>`)
	return []byte(b.String())
}

// HandleSetupPageGET serves the HTML page shell for the setup
// wizard. Story 1.6: replaces the 51-byte inline placeholder with
// a fully-styled 4-step wizard.
//
// Behavior:
//   - Setup mode: serve the wizard HTML (200, text/html, no-store).
//   - Normal mode: redirect to / (preserves the prior placeholder
//     behavior; the placeholder also redirected to / in normal mode).
//   - Renderer not registered (test setup gap): 404, so the dev
//     can spot the misconfig instead of seeing an empty 200 body.
//
// The page reuses admin design tokens and only adds wizard-specific
// classes via /admin/static/setup.css. The wizard itself is driven
// by /admin/static/setup.js.
func (h *SetupHandler) HandleSetupPageGET(w http.ResponseWriter, r *http.Request) {
	mode := h.getMode()
	if mode != types.ModeSetup {
		// Normal mode: redirect to root (per the previous placeholder
		// behavior, see router.go lines 155-165 pre-1.6).
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	body := ui.AdminSetupHTML()
	if body == nil {
		// Renderer not registered → 404 (defense-in-depth for
		// test setups that import internal/ui without internal/api).
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
