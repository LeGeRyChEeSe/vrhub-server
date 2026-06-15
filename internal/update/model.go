package update

import (
	"errors"
	"time"
)

// ErrRestartPending is returned by DownloadAndApply when the binary was staged
// successfully but AutoRestart is false. The caller should transition the update
// state to RestartPending and wait for an explicit restart request.
var ErrRestartPending = errors.New("update: binary staged, explicit restart required")

// ReleaseInfo represents a GitHub release.
type ReleaseInfo struct {
	TagName string  `json:"tag_name"`
	Version string  `json:"version"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
	Body    string  `json:"body"`
}

// Asset represents a release asset (binary download).
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}

// CheckResult holds the result of a version check.
type CheckResult struct {
	VersionAvailable bool
	LatestVersion    string
	DownloadURL      string
	CheckedAt        time.Time
	ReleaseNotes     string
	RestartPending   bool
}

// Version represents a parsed semantic version.
type Version struct {
	Major int
	Minor int
	Patch int
}
