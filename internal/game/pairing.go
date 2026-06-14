package game

import (
	"path/filepath"
	"regexp"
	"strings"
)

// GamePair groups an APK with its paired OBB file(s).
type GamePair struct {
	APKFile   FileEntry
	OBBFiles  []FileEntry
	APKMeta   APKMetadata
	IsAPKOnly bool
}

// PairResult holds the result of pairing APK and OBB files.
type PairResult struct {
	Games      []GamePair
	OrphanOBBs []FileEntry
}

var obbPattern = regexp.MustCompile(`^(main|patch)\.(\d+)\.(.+)\.obb$`)

// PairFiles matches OBB files to APKs by package name and version code.
func PairFiles(apkFiles, obbFiles []FileEntry) PairResult {
	var games []GamePair
	var orphanOBBs []FileEntry

	for _, apk := range apkFiles {
		meta, err := ExtractAPKMetadata(apk.Path)
		if err != nil {
			continue
		}

		pair := GamePair{
			APKFile:   apk,
			OBBFiles:  nil,
			APKMeta:   meta,
			IsAPKOnly: true,
		}

		if meta.PackageName == "" {
			continue
		}

		for _, obb := range obbFiles {
			matches := obbPattern.FindStringSubmatch(filepath.Base(obb.Name))
			if matches == nil {
				continue
			}

			obbPackageName := matches[3]

			if obbPackageName == meta.PackageName && int64(parseOBBVersion(matches[2])) == meta.VersionCode {
				pair.OBBFiles = append(pair.OBBFiles, obb)
				pair.IsAPKOnly = false
			}
		}

		games = append(games, pair)
	}

	for _, obb := range obbFiles {
		matches := obbPattern.FindStringSubmatch(filepath.Base(obb.Name))
		if matches == nil {
			orphanOBBs = append(orphanOBBs, obb)
			continue
		}

		found := false
		for _, g := range games {
			if g.APKMeta.PackageName != "" {
				obbVer := parseOBBVersion(matches[2])
				if int64(obbVer) == g.APKMeta.VersionCode && matches[3] == g.APKMeta.PackageName {
					found = true
					break
				}
			}
		}
		if !found {
			orphanOBBs = append(orphanOBBs, obb)
		}
	}

	return PairResult{Games: games, OrphanOBBs: orphanOBBs}
}

func parseOBBVersion(s string) int {
	var v int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			digit := int(c - '0')
			if v > (1<<31-1-digit)/10 {
				return 1<<31 - 1
			}
			v = v*10 + digit
		} else {
			break
		}
	}
	return v
}

// ExtractOBBPackageName extracts the package name from an OBB filename.
func ExtractOBBPackageName(obbName string) (versionCode int64, packageName string, ok bool) {
	matches := obbPattern.FindStringSubmatch(obbName)
	if matches == nil {
		return 0, "", false
	}

	vc := parseOBBVersion(matches[2])
	return int64(vc), matches[3], true
}

// IsOBBFile checks if a filename is an OBB file.
func IsOBBFile(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".obb")
}
