package game

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxUncompressedSizePerEntry caps the uncompressed size of a single ZIP
// entry that the validator will accept. Story 1.7 (live session 2026-06-08)
// raised this from 100 MiB to 4 GiB after a real-world false-positive
// surfaced: native libs (`lib/arm64-v8a/libmain.so`) and DEX files in
// Meta Quest VR games routinely exceed 100 MiB when uncompressed, even
// though the compressed APK fits comfortably on disk.
//
// 4 GiB is chosen as the practical upper bound:
//   - ext4/NTFS/exFAT all cap individual file sizes at 16 TiB (or higher),
//     so 4 GiB stays well within the filesystem envelope.
//   - The ZIP64 central directory uses a uint32 for the uncompressed size
//     field's high dword, with the 4 GiB boundary being a natural
//     architectural cutover.
//   - Go's archive/zip can read multi-GiB entries without issue; the
//     limit exists only to defeat zip-bomb DoS, not to bound memory.
//
// The original 100 MiB value was an unjustified guess that produced a
// systemic false-positive for any modern Quest/VR title.
const maxUncompressedSizePerEntry uint64 = 4 * 1024 * 1024 * 1024 // 4 GiB per entry

// IntegrityResult holds the validation result for a file.
type IntegrityResult struct {
	Corrupted        bool
	CorruptionReason string
}

// ValidateAPK checks if an APK file is a valid ZIP archive with a central directory record.
func ValidateAPK(apkPath string) IntegrityResult {
	r, err := zip.OpenReader(apkPath)
	if err != nil {
		return IntegrityResult{
			Corrupted:        true,
			CorruptionReason: fmt.Sprintf("invalid ZIP archive: %v", err),
		}
	}
	defer r.Close()

	// Verify central directory by iterating files
	if len(r.File) == 0 {
		return IntegrityResult{
			Corrupted:        true,
			CorruptionReason: "empty archive (no entries)",
		}
	}

	for _, f := range r.File {
		if f == nil {
			return IntegrityResult{
				Corrupted:        true,
				CorruptionReason: "corrupted central directory record",
			}
		}
		if f.UncompressedSize64 > maxUncompressedSizePerEntry {
			return IntegrityResult{
				Corrupted:        true,
				CorruptionReason: fmt.Sprintf("entry %q uncompressed size %d exceeds limit (%d)", f.Name, f.UncompressedSize64, maxUncompressedSizePerEntry),
			}
		}
	}

	return IntegrityResult{}
}

// ValidateOBB checks if an OBB file has expected size and naming pattern compliance.
func ValidateOBB(obbPath string) IntegrityResult {
	info, err := os.Stat(obbPath)
	if err != nil {
		return IntegrityResult{
			Corrupted:        true,
			CorruptionReason: fmt.Sprintf("cannot stat file: %v", err),
		}
	}

	if info.Size() == 0 {
		return IntegrityResult{
			Corrupted:        true,
			CorruptionReason: "empty OBB file (zero size)",
		}
	}

	name := filepath.Base(obbPath)
	matches := obbPattern.FindStringSubmatch(strings.ToLower(name))
	if matches == nil {
		return IntegrityResult{
			Corrupted:        false,
			CorruptionReason: "non-standard OBB naming convention",
		}
	}

	return IntegrityResult{}
}

// ValidateFile validates any file (APK or OBB) and returns the integrity result.
// The extension comparison is case-insensitive so that .APK, .Apk, .OBB, .Obb
// are all routed to the appropriate validator. Regression fix for
// debt-triage-2026-06-06 C-03 (the variable was misnamed "lowerName" but was
// not actually lowercased, so uppercase extensions were silently dropped).
func ValidateFile(filePath string) IntegrityResult {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".apk":
		return ValidateAPK(filePath)
	case ".obb":
		return ValidateOBB(filePath)
	default:
		return IntegrityResult{}
	}
}
