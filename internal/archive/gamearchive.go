package archive

// Game archive generation (Issue #1 — VRP / Cyberdeck compatibility).
//
// VRHub clients (the Android app and VR-CyberDeck) download a game by listing
// the directory GET /{hash}/ and pulling every file they find. They auto-detect
// the delivery format: if the listing contains *.7z* parts they merge the parts
// and extract the 7z archive locally with the configured password; otherwise
// they copy the raw .apk/.obb. This file produces the split, AES-256-encrypted
// 7z archives in the VRP/Rookie layout that both clients expect:
//
//	{releaseName}.7z.001, {releaseName}.7z.002, …   (in {dataDir}/games/{hash}/)
//
// with the internal structure:
//
//	{releaseName}/{apk basename}.apk
//	{releaseName}/{packageName}/{obb basename}.obb   (one or more OBBs)
//
// The OBB lives in a {packageName}/ subfolder because Cyberdeck pushes that
// folder verbatim to /sdcard/Android/obb/{packageName}/ on the headset.
//
// The archive is a DERIVED artifact: originals stay where the scanner found
// them (game.ApkPath / game.OBBPath are served raw as a fallback until the
// archive exists). The on-disk parts plus a small .archive.json marker are the
// source of truth — no DB column is added. The marker records a hash of the
// archive password and the game's version code so a password change or a game
// update invalidates the archive and triggers a regeneration on the next sweep.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	vlog "github.com/LeGeRyChEeSe/vrhub-server/internal/log"
)

// archiveMarkerName is the per-game sidecar that records what the on-disk parts
// were generated from, so a sweep can tell a fresh archive from a stale one.
const archiveMarkerName = ".archive.json"

// DefaultSplitSize is the per-volume size passed to 7z's -v flag when the
// operator has not configured one. 2 GiB matches the common VRP convention and
// stays under the 4 GiB FAT32 per-file limit for clients that stage to such a
// volume.
const DefaultSplitSize = "2g"

// splitSizeRe validates a 7z -v volume size: a positive integer optionally
// suffixed with a unit (b/k/m/g, case-insensitive). Mirrors what 7z accepts.
var splitSizeRe = regexp.MustCompile(`^[1-9][0-9]*[bkmgBKMG]?$`)

// partSuffixRe matches the numeric suffix of a 7z split volume (".7z.001").
var partSuffixRe = regexp.MustCompile(`\.7z\.([0-9]+)$`)

// GameArchiveSpec fully describes one game to (re)archive. All paths are
// absolute; Hash is MD5(releaseName+"\n") — the same value stored on the game
// row and used to build the public download URL.
type GameArchiveSpec struct {
	DataDir     string
	Hash        string
	ReleaseName string
	PackageName string
	APKPath     string
	OBBPaths    []string
	VersionCode int64
	Password    string
	SplitSize   string
}

// archiveMarker is the JSON body of {hash dir}/.archive.json.
type archiveMarker struct {
	Version     int    `json:"version"`
	PwHash      string `json:"pw_hash"`
	VersionCode int64  `json:"version_code"`
	Parts       int    `json:"parts"`
}

// ValidateSplitSize reports whether s is a 7z-acceptable -v volume size.
func ValidateSplitSize(s string) bool {
	return splitSizeRe.MatchString(s)
}

// NormalizeSplitSize returns s when it is a valid 7z volume size, or
// DefaultSplitSize otherwise.
func NormalizeSplitSize(s string) string {
	if ValidateSplitSize(s) {
		return s
	}
	return DefaultSplitSize
}

// GameArchiveDir returns the directory that holds a game's split parts.
func GameArchiveDir(dataDir, hash string) string {
	return filepath.Join(dataDir, "games", hash)
}

// archiveBaseName is the 7z base name (before 7z appends .001/.002/…).
func archiveBaseName(releaseName string) string {
	return releaseName + ".7z"
}

// GameArchiveParts returns the sorted basenames of the split parts present on
// disk for a game ({releaseName}.7z.001, .002, …), or an empty slice when no
// archive has been generated yet. The slice is ordered by numeric volume index.
func GameArchiveParts(dataDir, hash, releaseName string) ([]string, error) {
	dir := GameArchiveDir(dataDir, hash)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	prefix := archiveBaseName(releaseName) + "."
	type part struct {
		name string
		idx  int
	}
	var parts []part
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		m := partSuffixRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		idx, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			continue
		}
		parts = append(parts, part{name: name, idx: idx})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].idx < parts[j].idx })
	names := make([]string, len(parts))
	for i, p := range parts {
		names[i] = p.name
	}
	return names, nil
}

// IsArchivePartName reports whether name looks like a 7z split volume
// ("{anything}.7z.NNN", NNN being digits). Used by the file server to route a
// part download to the archive directory.
func IsArchivePartName(name string) bool {
	return partSuffixRe.MatchString(name) && strings.Contains(name, ".7z.")
}

// HasGameArchive reports whether at least one split part exists for the game.
func HasGameArchive(dataDir, hash, releaseName string) bool {
	parts, err := GameArchiveParts(dataDir, hash, releaseName)
	return err == nil && len(parts) > 0
}

// passwordHash returns a hex SHA-256 of the password, stored in the marker so
// a password change can be detected without keeping the cleartext on disk.
func passwordHash(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

// GameArchiveUpToDate reports whether the on-disk archive for spec matches the
// current password and version code (i.e. it does NOT need regeneration). It
// returns false when no archive exists, the marker is missing/unreadable, or
// the recorded password hash / version code differ from spec.
func GameArchiveUpToDate(spec GameArchiveSpec) bool {
	parts, err := GameArchiveParts(spec.DataDir, spec.Hash, spec.ReleaseName)
	if err != nil || len(parts) == 0 {
		return false
	}
	markerPath := filepath.Join(GameArchiveDir(spec.DataDir, spec.Hash), archiveMarkerName)
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return false
	}
	var m archiveMarker
	if jsonErr := json.Unmarshal(data, &m); jsonErr != nil {
		return false
	}
	return m.PwHash == passwordHash(spec.Password) &&
		m.VersionCode == spec.VersionCode &&
		m.Parts == len(parts)
}

// RemoveGameArchive deletes all split parts and the marker for a game. It is a
// no-op when nothing is present. Used on game deletion and before regeneration.
func RemoveGameArchive(dataDir, hash, releaseName string) error {
	dir := GameArchiveDir(dataDir, hash)
	parts, err := GameArchiveParts(dataDir, hash, releaseName)
	if err != nil {
		return err
	}
	for _, name := range parts {
		if rmErr := os.Remove(filepath.Join(dir, name)); rmErr != nil && !os.IsNotExist(rmErr) {
			return rmErr
		}
	}
	if rmErr := os.Remove(filepath.Join(dir, archiveMarkerName)); rmErr != nil && !os.IsNotExist(rmErr) {
		return rmErr
	}
	return nil
}

// RemoveGameArchiveAll deletes the entire {dataDir}/games/{hash}/ directory
// (split parts, marker, and any legacy raw files staged there). Used when a
// game is removed from the library. No-op when the directory is absent.
func RemoveGameArchiveAll(dataDir, hash string) error {
	if dataDir == "" || hash == "" {
		return nil
	}
	if err := os.RemoveAll(GameArchiveDir(dataDir, hash)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// safePathComponent rejects names that would let an archive escape its
// directory (path separators, "..", empty, leading dot for hidden files).
func safePathComponent(name string) error {
	if name == "" {
		return fmt.Errorf("empty path component")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("dot path component: %q", name)
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("path separator or null byte in %q", name)
	}
	return nil
}

// GenerateGameArchive builds (or rebuilds) the split, encrypted 7z archive for
// one game. It stages the APK + OBB(s) into the VRP-internal layout, invokes
// 7zz to produce the {releaseName}.7z.NNN volumes under {dataDir}/games/{hash}/,
// then writes the .archive.json marker. Any previous parts for the game are
// removed first so a regeneration never leaves stale volumes behind.
func GenerateGameArchive(ctx context.Context, spec GameArchiveSpec) error {
	if spec.DataDir == "" || spec.Hash == "" || spec.ReleaseName == "" || spec.PackageName == "" {
		return fmt.Errorf("game archive: incomplete spec (dataDir/hash/releaseName/packageName required)")
	}
	if spec.APKPath == "" {
		return fmt.Errorf("game archive: APK path required for %q", spec.ReleaseName)
	}
	if spec.Password == "" {
		return fmt.Errorf("game archive: archive password not configured")
	}
	if err := safePathComponent(spec.ReleaseName); err != nil {
		return fmt.Errorf("game archive: unsafe release name: %w", err)
	}
	if err := safePathComponent(spec.PackageName); err != nil {
		return fmt.Errorf("game archive: unsafe package name: %w", err)
	}

	splitSize := NormalizeSplitSize(spec.SplitSize)
	dir := GameArchiveDir(spec.DataDir, spec.Hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("game archive: mkdir %s: %w", dir, err)
	}

	// Clean slate: drop any previous parts/marker before regenerating so a
	// shrinking volume count (e.g. after a password/level change) can't leave
	// orphaned .NNN files that a client would treat as a gap.
	if err := RemoveGameArchive(spec.DataDir, spec.Hash, spec.ReleaseName); err != nil {
		return fmt.Errorf("game archive: clean previous parts: %w", err)
	}

	// Stage the inputs into {releaseName}/… under a temp dir on the dataDir
	// volume. We hardlink when possible (instant, no extra space) and fall
	// back to a copy across volumes — the common case here, since games often
	// live on a different drive than the data dir.
	staging, err := os.MkdirTemp(dir, ".staging-*")
	if err != nil {
		return fmt.Errorf("game archive: create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	releaseDir := filepath.Join(staging, spec.ReleaseName)
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		return fmt.Errorf("game archive: mkdir staging release dir: %w", err)
	}

	apkDest := filepath.Join(releaseDir, filepath.Base(spec.APKPath))
	if err := linkOrCopy(spec.APKPath, apkDest); err != nil {
		return fmt.Errorf("game archive: stage apk: %w", err)
	}

	if len(spec.OBBPaths) > 0 {
		obbDir := filepath.Join(releaseDir, spec.PackageName)
		if err := os.MkdirAll(obbDir, 0o755); err != nil {
			return fmt.Errorf("game archive: mkdir staging obb dir: %w", err)
		}
		for _, obb := range spec.OBBPaths {
			if obb == "" {
				continue
			}
			dest := filepath.Join(obbDir, filepath.Base(obb))
			if err := linkOrCopy(obb, dest); err != nil {
				return fmt.Errorf("game archive: stage obb %s: %w", obb, err)
			}
		}
	}

	sevenZipPath, err := sevenZipBinaryPath(ctx, spec.DataDir)
	if err != nil {
		return fmt.Errorf("game archive: 7z binary not available: %w", err)
	}
	// The command runs with cmd.Dir = staging; make a relative extracted-binary
	// path absolute so it still resolves (same reasoning as generator.go).
	if sevenZipPath != "" && strings.ContainsAny(sevenZipPath, `/\`) && !filepath.IsAbs(sevenZipPath) {
		if abs, absErr := filepath.Abs(sevenZipPath); absErr == nil {
			sevenZipPath = abs
		}
	}

	archivePath := filepath.Join(dir, archiveBaseName(spec.ReleaseName))
	args := []string{
		"a",
		"-p" + spec.Password,
		"-mhe=on", // encrypt headers (filenames) like meta.7z
		"-mx=1",   // fastest: APK/OBB payloads are already compressed
		"-v" + splitSize,
		"-bb0",
		"-bso0",
		"-bse1",
		"-y",
		archivePath,
		spec.ReleaseName, // relative to cmd.Dir (staging)
	}

	cmd := exec.CommandContext(ctx, sevenZipPath, args...)
	cmd.Dir = staging
	if isAndroid() {
		if libDir := GetAndroidLibDir(); libDir != "" {
			cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH="+libDir)
		}
	}

	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		// Best-effort cleanup so a half-written archive isn't served.
		_ = RemoveGameArchive(spec.DataDir, spec.Hash, spec.ReleaseName)
		return fmt.Errorf("game archive: 7z failed: %w (output: %s)", runErr, strings.TrimSpace(string(out)))
	}

	parts, err := GameArchiveParts(spec.DataDir, spec.Hash, spec.ReleaseName)
	if err != nil {
		return fmt.Errorf("game archive: list generated parts: %w", err)
	}
	if len(parts) == 0 {
		return fmt.Errorf("game archive: 7z reported success but produced no parts for %q", spec.ReleaseName)
	}

	marker := archiveMarker{
		Version:     1,
		PwHash:      passwordHash(spec.Password),
		VersionCode: spec.VersionCode,
		Parts:       len(parts),
	}
	markerData, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("game archive: marshal marker: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, archiveMarkerName), markerData, 0o644); err != nil {
		return fmt.Errorf("game archive: write marker: %w", err)
	}

	vlog.Get().Info().
		Str("release", spec.ReleaseName).
		Str("hash", spec.Hash).
		Int("parts", len(parts)).
		Str("split_size", splitSize).
		Msg("game archive: generated split 7z")
	return nil
}

// linkOrCopy hardlinks src to dst, falling back to a byte copy when the link
// fails (typically a cross-device link between the game volume and the data
// dir). dst's parent must already exist.
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
