package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

// Audit fix (2026-11): the update banner in the admin dashboard offers an
// update whenever the checker reports VersionAvailable. That flag comes from
// latestVer.GreaterThan(currentVer), so a default CurrentVersion that lags
// behind the newest shipped release makes an already-up-to-date server offer
// to "update" to the version it is already running. These tests pin the
// behaviour against the production default (CurrentVersion) rather than the
// mock's own 0.1.0 baseline used by newMockChecker.

// newDefaultVersionMockChecker is newMockChecker with currentVer set to the
// production CurrentVersion default, so the comparison reflects what a real
// binary built without ldflags would do.
func newDefaultVersionMockChecker(ts *httptest.Server) *Checker {
	return &Checker{
		config:     Config{Enabled: true, Owner: "test", Repo: "test"},
		httpClient: ts.Client(),
		apiBaseURL: ts.URL + "/repos/%s/%s/releases/latest",
		currentVer: CurrentVersion,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

func TestChecker_LatestEqualsCurrentDefault_NoUpdateOffered(t *testing.T) {
	current := CurrentVersion.String()
	tag := "v" + current
	assetName := "vrhub-server-" + current + "-" + runtime.GOOS + "-" + runtime.GOARCH + ".zip"

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tag_name": tag,
			"html_url": "https://github.com/test/test/releases/tag/" + tag,
			"assets": []map[string]interface{}{
				{"name": assetName, "browser_download_url": "https://github.com/test/test/releases/download/" + tag + "/" + assetName},
			},
		})
	}))
	defer ts.Close()

	checker := newDefaultVersionMockChecker(ts)
	checker.check(context.Background())

	result := checker.GetResult()
	if result == nil {
		t.Fatal("GetResult returned nil after check")
	}
	if result.VersionAvailable {
		t.Errorf("VersionAvailable = true, want false (latest release %s == current default %s — banner must not offer an update)", tag, current)
	}
	if result.LatestVersion != current {
		t.Errorf("LatestVersion = %q, want %q", result.LatestVersion, current)
	}
	if result.DownloadURL != "" {
		t.Errorf("DownloadURL = %q, want empty (no update to offer)", result.DownloadURL)
	}
}

func TestChecker_CurrentDefaultMatchesLatestReleaseAfterBump(t *testing.T) {
	// Guard against the drift this audit found: CurrentVersion must be the
	// version the release workflow ships, so a server updated to the newest
	// tag stops advertising an update. 0.1.6 is the release documented in
	// CHANGELOG.md as shipped; the default must not lag behind it.
	if !CurrentVersion.GreaterThan(Version{Major: 0, Minor: 1, Patch: 5}) {
		t.Errorf("CurrentVersion = %s, want > 0.1.5 (the package default must track the last shipped release)", CurrentVersion.String())
	}

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tag_name": "v0.1.6",
			"html_url": "https://github.com/test/test/releases/tag/v0.1.6",
			"assets":   []map[string]interface{}{},
		})
	}))
	defer ts.Close()

	checker := newDefaultVersionMockChecker(ts)
	checker.check(context.Background())

	result := checker.GetResult()
	if result == nil {
		t.Fatal("GetResult returned nil after check")
	}
	if result.VersionAvailable {
		t.Errorf("VersionAvailable = true, want false (server already runs 0.1.6)")
	}
}
