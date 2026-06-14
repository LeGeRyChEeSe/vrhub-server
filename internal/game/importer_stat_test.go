package game

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestNoStatBeforeOpenTOCTOU is a structural regression test for C-10:
// it scans the source files of internal/game and internal/archive for the
// dangerous "os.Stat(X) ... os.Open(X)" pattern on the same path within a
// 5-line window. If a future change introduces such a pattern (e.g. a
// security-gating os.Stat followed by an os.Open of the same path), this
// test fails so the developer can refactor to "open, then check Stat via
// the returned FileInfo" or use errors.Is(err, os.ErrNotExist) directly.
//
// The test allows:
// - os.Stat of a parent directory (e.g. filepath.Dir(p))
// - os.Stat whose result variable is NOT used as a path argument later
// - os.Stat followed by readFileWithTimeout in a different function
//
// Allowed stat-use sites (current code, all benign):
//   - importer.go:65  : stat(filePath) -> size for ValidateAPK input
//   - importer.go:328 : stat(filePath) -> size for revalidation
//   - generator.go:37 : stat(filepath.Dir(p)) -> parent dir check
//   - generator.go:153/162/171 : stat(thumbnailPath) -> !IsDir() guard
//     followed by readFileWithTimeout, which handles os.ErrNotExist.
func TestNoStatBeforeOpenTOCTOU(t *testing.T) {
	files := []string{
		"importer.go",
		"../archive/generator.go",
	}
	// Match os.Stat or os.Lstat with a captured argument. Multi-line
	// backticks/quotes complicate the capture, so we only check the
	// identifier immediately following the call.
	statRe := regexp.MustCompile(`os\.(?:Stat|Lstat)\(([^)]+)\)`)
	// Match os.Open / os.ReadFile / os.OpenFile with a captured argument.
	openRe := regexp.MustCompile(`os\.(?:Open|ReadFile|OpenFile)\(([^)]+)\)`)

	for _, rel := range files {
		src, err := os.ReadFile(rel)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		lines := strings.Split(string(src), "\n")

		for i, line := range lines {
			statMatches := statRe.FindAllStringSubmatch(line, -1)
			if len(statMatches) == 0 {
				continue
			}
			for _, m := range statMatches {
				statArg := strings.TrimSpace(m[1])
				// Allow stat of a parent directory: filepath.Dir(p)
				if strings.HasPrefix(statArg, "filepath.Dir(") {
					continue
				}
				// Look for an os.Open / os.ReadFile / os.OpenFile of
				// the same argument within a 5-line forward window.
				windowEnd := i + 6
				if windowEnd > len(lines) {
					windowEnd = len(lines)
				}
				for j := i + 1; j < windowEnd; j++ {
					openMatches := openRe.FindAllStringSubmatch(lines[j], -1)
					for _, om := range openMatches {
						openArg := strings.TrimSpace(om[1])
						if openArg == statArg {
							t.Errorf("%s: line %d: os.%s(%s) followed by os.%s(%s) at line %d (dangerous TOCTOU)\n  stat line: %s\n  open line: %s",
								rel, i+1, "Stat/Lstat", statArg, "Open/ReadFile/OpenFile", openArg, j+1,
								strings.TrimSpace(line), strings.TrimSpace(lines[j]))
						}
					}
				}
			}
		}
	}
}
