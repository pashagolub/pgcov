package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Discover recursively finds all SQL files under rootPath.
//
// Each file's RelativePath is computed against rootPath and normalised to
// forward slashes. That path becomes the coverage-file key, so it must not
// depend on where the process happens to be running: it previously used
// filepath.Rel(cwd, path), which made the same source file key differently
// depending on the directory pgcov was invoked from, and carried OS-native
// separators that did not survive a mixed-OS CI matrix.
func Discover(rootPath string) ([]DiscoveredFile, error) {
	return discoverRelativeTo(rootPath, rootPath)
}

// discoverRelativeTo walks scanRoot but expresses every RelativePath against
// relRoot. Splitting the two lets DiscoverCoLocatedSources scan individual test
// directories while still keying every file against the run's single discovery
// root - without that, sources in different directories would collapse onto the
// same bare filename.
func discoverRelativeTo(scanRoot, relRoot string) ([]DiscoveredFile, error) {
	absScan, err := filepath.Abs(scanRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	absRel, err := filepath.Abs(relRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Check if directory exists
	info, err := os.Stat(absScan)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("directory not found: %s", absScan)
		}
		return nil, fmt.Errorf("failed to access directory: %w", err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", absScan)
	}

	var files []DiscoveredFile

	err = filepath.Walk(absScan, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Skip directories we can't access
			if os.IsPermission(err) {
				return nil
			}
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Only process .sql files
		if !strings.HasSuffix(strings.ToLower(path), ".sql") {
			return nil
		}

		// Key the file against the discovery root, not the process CWD, and
		// use forward slashes so coverage data is portable across platforms.
		relPath, err := filepath.Rel(absRel, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}
		relPath = filepath.ToSlash(relPath)

		// Classify the file
		fileType := ClassifyFile(filepath.Base(path))

		files = append(files, DiscoveredFile{
			Path:         path,
			RelativePath: relPath,
			Type:         fileType,
			ModTime:      info.ModTime(),
		})

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	return files, nil
}

// DiscoverTests finds only test files (*_test.sql) in the given directory
func DiscoverTests(rootPath string) ([]DiscoveredFile, error) {
	allFiles, err := Discover(rootPath)
	if err != nil {
		return nil, err
	}

	var testFiles []DiscoveredFile
	for _, file := range allFiles {
		if file.Type == FileTypeTest {
			testFiles = append(testFiles, file)
		}
	}

	return testFiles, nil
}

// DiscoverSources finds only source files (*.sql but not *_test.sql) in the given directory
func DiscoverSources(rootPath string) ([]DiscoveredFile, error) {
	allFiles, err := Discover(rootPath)
	if err != nil {
		return nil, err
	}

	var sourceFiles []DiscoveredFile
	for _, file := range allFiles {
		if file.Type == FileTypeSource {
			sourceFiles = append(sourceFiles, file)
		}
	}

	return sourceFiles, nil
}

// DiscoverCoLocatedSources finds source files in the same directories as test
// files. This implements the co-location strategy where tests and source code
// are kept together.
//
// root is the run's discovery root: every returned RelativePath is expressed
// against it, matching what Discover(root) would have produced. Passing the
// individual test directory instead would reduce each source to its bare
// filename, so two "functions.sql" files in different directories would share
// a coverage key.
func DiscoverCoLocatedSources(root string, testFiles []DiscoveredFile) ([]DiscoveredFile, error) {
	// Collect unique directories containing test files
	testDirs := make(map[string]bool)
	for _, test := range testFiles {
		testDirs[filepath.Dir(test.Path)] = true
	}

	// Discover all source files in those directories
	var sourceFiles []DiscoveredFile
	seenFiles := make(map[string]bool) // Avoid duplicates

	for testDir := range testDirs {
		files, err := discoverRelativeTo(testDir, root)
		if err != nil {
			return nil, fmt.Errorf("failed to discover sources in %s: %w", testDir, err)
		}

		for _, file := range files {
			if file.Type != FileTypeSource {
				continue
			}
			if !seenFiles[file.Path] {
				sourceFiles = append(sourceFiles, file)
				seenFiles[file.Path] = true
			}
		}
	}

	return sourceFiles, nil
}
