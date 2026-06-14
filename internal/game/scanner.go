package game

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileEntry represents a discovered file during scanning.
type FileEntry struct {
	Path  string
	Name  string
	Size  int64
	IsAPK bool
}

// ScanDirectory recursively walks the directory tree and collects APK and OBB files.
func ScanDirectory(dir string) ([]FileEntry, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("stat dir %q: %w", dir, err)
	}

	var apkFiles []FileEntry
	var obbFiles []FileEntry

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible dirs
		}
		if d.IsDir() {
			return nil
		}

		name := d.Name()
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}

		entry := FileEntry{
			Path: path,
			Name: name,
			Size: info.Size(),
		}

		lowerName := strings.ToLower(filepath.Ext(name))
		switch lowerName {
		case ".apk":
			entry.IsAPK = true
			apkFiles = append(apkFiles, entry)
		case ".obb":
			obbFiles = append(obbFiles, entry)
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("walk dir %q: %w", dir, err)
	}

	allFiles := make([]FileEntry, 0, len(apkFiles)+len(obbFiles))
	allFiles = append(allFiles, apkFiles...)
	allFiles = append(allFiles, obbFiles...)

	return allFiles, nil
}
