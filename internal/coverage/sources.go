package coverage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SourceInfo fingerprints a source file as it was when coverage was collected.
//
// Coverage positions are byte offsets into the source. Editing a file after a
// run shifts every offset past the edit, so the stored hit counts get painted
// onto unrelated spans of the new text - a report that looks internally
// consistent and is entirely wrong. The fingerprint lets readers notice.
type SourceInfo struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// HashFile fingerprints the file at path.
func HashFile(path string) (SourceInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return SourceInfo{}, fmt.Errorf("failed to open source %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return SourceInfo{}, fmt.Errorf("failed to read source %s: %w", path, err)
	}

	return SourceInfo{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

// AddSource records the fingerprint of a source file under its coverage key.
func (c *Coverage) AddSource(file string, info SourceInfo) {
	if c.Sources == nil {
		c.Sources = make(map[string]SourceInfo)
	}
	c.Sources[file] = info
}

// ResolveBaseDir decides which directory relative coverage keys are resolved
// against, in order of precedence:
//
//  1. an explicit base dir (the --base-dir flag), which always wins;
//  2. the discovery root recorded by the run, when it still exists;
//  3. "", meaning the process working directory.
//
// Step 2 is what makes `pgcov report` work with no flags: keys are relative to
// the run's discovery root, so resolving them against the working directory
// only happens to work when the two coincide.
func (c *Coverage) ResolveBaseDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if c.Root == "" {
		return ""
	}
	if info, err := os.Stat(c.Root); err == nil && info.IsDir() {
		return c.Root
	}
	// Recorded on another machine or in a moved checkout; fall back to the CWD.
	return ""
}

// VerifySources re-hashes the recorded source files and returns one
// human-readable warning per file that has changed or gone missing.
//
// Files without a recorded fingerprint are skipped, so coverage written by an
// older build still reports cleanly. Resolution mirrors the reporters': baseDir
// first when set, then the process working directory; a file that resolves
// nowhere is reported as missing rather than silently passing.
func (c *Coverage) VerifySources(baseDir string) []string {
	if len(c.Sources) == 0 {
		return nil
	}

	var warnings []string
	for _, file := range c.GetFiles() {
		want, ok := c.Sources[file]
		if !ok {
			continue
		}

		path, found := resolveSource(file, baseDir)
		if !found {
			warnings = append(warnings, fmt.Sprintf(
				"%s: source file not found; annotated output will be incomplete", file))
			continue
		}

		got, err := HashFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		if got.SHA256 != want.SHA256 {
			warnings = append(warnings, fmt.Sprintf(
				"%s: source has changed since coverage was collected (%d bytes then, %d now); "+
					"positions are byte offsets, so this report highlights the wrong spans - re-run 'pgcov run'",
				file, want.Size, got.Size))
		}
	}
	return warnings
}

// resolveSource locates a coverage key on disk, mirroring the reporters' order:
// an absolute path as-is, then baseDir, then the working directory.
func resolveSource(file string, baseDir string) (string, bool) {
	native := filepath.FromSlash(file)

	var candidates []string
	if filepath.IsAbs(native) {
		candidates = append(candidates, native)
	} else {
		if baseDir != "" {
			candidates = append(candidates, filepath.Join(baseDir, native))
		}
		if cwd, err := os.Getwd(); err == nil {
			candidates = append(candidates, filepath.Join(cwd, native))
		}
		candidates = append(candidates, native)
	}

	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, true
		}
	}
	return "", false
}

// conflictingSources reports files whose fingerprints disagree between two
// coverage sets. Summing hit counts across different revisions of a file
// produces a result that is internally consistent and meaningless, so Merge
// refuses instead.
func conflictingSources(dst, src map[string]SourceInfo) []string {
	var conflicts []string
	for file, s := range src {
		d, ok := dst[file]
		if !ok || d.SHA256 == s.SHA256 {
			continue
		}
		conflicts = append(conflicts, file)
	}
	return conflicts
}
