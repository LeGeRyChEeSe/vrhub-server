package update

import "time"

// ReleaseInfo represents a GitHub release.
type ReleaseInfo struct {
	TagName string  `json:"tag_name"`
	Version string  `json:"version"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
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
}

// Version represents a parsed semantic version.
type Version struct {
	Major int
	Minor int
	Patch int
}
