package report

import (
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
)

// HTMLReporter formats coverage data as HTML
type HTMLReporter struct {
	// BaseDir, when non-empty, is the directory relative source paths are
	// resolved against before falling back to the process CWD. It is used by
	// readSourceFileAsString to locate .sql sources referenced in coverage
	// positions when the report command runs from a different directory than
	// `pgcov run` was invoked from.
	BaseDir string
}

// NewHTMLReporter creates a new HTML reporter
func NewHTMLReporter() *HTMLReporter {
	return &HTMLReporter{}
}

// SetBaseDir configures the base directory used to resolve relative source
// paths when reading files from disk. Passing an empty string clears it and
// falls back to the process CWD.
func (r *HTMLReporter) SetBaseDir(dir string) {
	r.BaseDir = dir
}

// positionRange represents a byte range with coverage info
type positionRange struct {
	startPos int
	length   int
	hitCount int
}

// Format formats coverage data as HTML and writes to the writer
func (r *HTMLReporter) Format(cov *coverage.Coverage, writer io.Writer) error {
	files := cov.GetFiles()

	// Write HTML header
	if err := r.writeHeader(cov, files, writer); err != nil {
		return err
	}

	// Write file details with source code
	for i, file := range files {
		if err := r.writeFileDetailWithSource(file, cov, writer, i); err != nil {
			return err
		}
	}

	// Write HTML footer
	if err := r.writeFooter(writer); err != nil {
		return err
	}

	return nil
}

// writeHeader writes the HTML document header with CSS
func (r *HTMLReporter) writeHeader(cov *coverage.Coverage, files []string, writer io.Writer) error {
	_, err := fmt.Fprintf(writer, `<!DOCTYPE html>
<html>
	<head>
		<meta http-equiv="Content-Type" content="text/html; charset=utf-8">
		<title>pgcov: Coverage Report</title>
		<style>
			body {
				background: black;
				color: rgb(80, 80, 80);
			}
			body, pre, #legend span {
				font-family: Menlo, monospace;
				font-weight: bold;
			}
			#topbar {
				background: black;
				position: fixed;
				top: 0; left: 0; right: 0;
				height: 42px;
				border-bottom: 1px solid rgb(80, 80, 80);
			}
			#content {
				margin-top: 50px;
			}
			#nav, #legend {
				float: left;
				margin-left: 10px;
			}
			#legend {
				margin-top: 12px;
			}
			#nav {
				margin-top: 10px;
			}
			#legend span {
				margin: 0 5px;
			}
			.cov0 { color: rgb(192, 0, 0) }
			.cov1 { color: rgb(128, 128, 128) }
			.cov2 { color: rgb(116, 140, 131) }
			.cov3 { color: rgb(104, 152, 134) }
			.cov4 { color: rgb(92, 164, 137) }
			.cov5 { color: rgb(80, 176, 140) }
			.cov6 { color: rgb(68, 188, 143) }
			.cov7 { color: rgb(56, 200, 146) }
			.cov8 { color: rgb(44, 212, 149) }
			.cov9 { color: rgb(32, 224, 152) }
			.cov10 { color: rgb(20, 236, 155) }
		</style>
	</head>
	<body>
		<div id="topbar">
			<div id="nav">
				<select id="files">
`)
	if err != nil {
		return err
	}

	// Write file options with coverage percentages
	for i, file := range files {
		percent := cov.PositionCoveragePercent(file)
		_, err = fmt.Fprintf(writer, `				<option value="file%d">%s (%.1f%%%%)</option>
`, i, html.EscapeString(file), percent)
		if err != nil {
			return err
		}
	}

	// Write legend
	_, err = writer.Write([]byte(`				</select>
			</div>
			<div id="legend">
				<span>not tracked</span>
				<span class="cov0">not covered</span>
				<span class="cov8">covered</span>
			</div>
		</div>
		<div id="content">
`))
	return err
}

// writeFileDetailWithSource writes detailed coverage for a single file with actual source code
func (r *HTMLReporter) writeFileDetailWithSource(file string, cov *coverage.Coverage, writer io.Writer, fileIndex int) error {
	posHits := cov.Positions[file]

	// Write file pre tag with ID and hidden by default
	displayStyle := "display: none"
	if fileIndex == 0 {
		displayStyle = "" // Show first file by default
	}

	_, err := fmt.Fprintf(writer, `		<pre class="file" id="file%d" style="%s">`, fileIndex, displayStyle)
	if err != nil {
		return err
	}

	// Read the source file from disk
	sourceText, err := r.readSourceFileAsString(file)
	if err != nil {
		// If we can't read the file, show error
		_, err = fmt.Fprintf(writer, `// Error reading source file: %s
`, html.EscapeString(err.Error()))
		if err != nil {
			return err
		}
	} else {
		// Parse position hits into ranges sorted by position
		ranges := r.parsePositionRanges(posHits)

		// Render source with position-based highlighting
		if err := r.renderSourceWithPositions(sourceText, ranges, writer); err != nil {
			return err
		}
	}

	// Close pre tag
	_, err = writer.Write([]byte("</pre>\n\t\t\n\t\t"))
	return err
}

// parsePositionRanges converts position hits map to sorted, non-overlapping ranges
func (r *HTMLReporter) parsePositionRanges(posHits coverage.PositionHits) []positionRange {
	var ranges []positionRange

	for posKey, hitCount := range posHits {
		startPos, length, err := coverage.ParsePositionKey(posKey)
		if err != nil {
			continue
		}
		ranges = append(ranges, positionRange{
			startPos: startPos,
			length:   length,
			hitCount: hitCount,
		})
	}

	// Sort by start position
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].startPos < ranges[j].startPos
	})

	// Merge overlapping ranges - keep only non-overlapping portions
	// When ranges overlap, prefer the one that starts first
	return r.resolveOverlappingRanges(ranges)
}

// resolveOverlappingRanges removes overlapping portions from ranges
// Each byte is assigned to only one range (the one that starts first)
func (r *HTMLReporter) resolveOverlappingRanges(ranges []positionRange) []positionRange {
	if len(ranges) == 0 {
		return ranges
	}

	var result []positionRange
	currentEnd := 0

	for _, rng := range ranges {
		// Skip ranges that are completely inside already-covered regions
		if rng.startPos+rng.length <= currentEnd {
			continue
		}

		// Adjust start if it overlaps with previous coverage
		adjustedStart := max(rng.startPos, currentEnd)

		// Calculate adjusted length
		adjustedLength := rng.startPos + rng.length - adjustedStart
		if adjustedLength > 0 {
			result = append(result, positionRange{
				startPos: adjustedStart,
				length:   adjustedLength,
				hitCount: rng.hitCount,
			})
			currentEnd = adjustedStart + adjustedLength
		}
	}

	return result
}

// renderSourceWithPositions renders source text with position-based coverage spans
func (r *HTMLReporter) renderSourceWithPositions(sourceText string, ranges []positionRange, writer io.Writer) error {
	// Convert source to bytes for position-based access
	sourceBytes := []byte(sourceText)
	sourceLen := len(sourceBytes)

	// Filter out any ranges that exceed the source length
	var validRanges []positionRange
	for _, rng := range ranges {
		if rng.startPos < sourceLen {
			validRanges = append(validRanges, rng)
		}
	}
	ranges = validRanges

	// Current position in source
	pos := 0
	rangeIdx := 0

	for pos < sourceLen {
		// Find next coverage range
		if rangeIdx < len(ranges) {
			rng := ranges[rangeIdx]

			if pos < rng.startPos {
				// Output uncovered text before range
				endPos := min(rng.startPos, sourceLen)
				uncoveredText := string(sourceBytes[pos:endPos])
				_, err := writer.Write([]byte(html.EscapeString(uncoveredText)))
				if err != nil {
					return err
				}
				pos = endPos
			} else {
				// We're at the start of a coverage range - output with span
				endPos := min(rng.startPos+rng.length, sourceLen)
				coveredText := string(sourceBytes[pos:endPos])
				covClass := r.getCoverageClass(rng.hitCount)

				_, err := fmt.Fprintf(writer, `<span class="%s" title="%d">%s</span>`,
					covClass, rng.hitCount, html.EscapeString(coveredText))
				if err != nil {
					return err
				}
				pos = endPos
				rangeIdx++
			}
		} else {
			// No more coverage ranges, output rest of file
			remainingText := string(sourceBytes[pos:])
			_, err := writer.Write([]byte(html.EscapeString(remainingText)))
			if err != nil {
				return err
			}
			break
		}
	}

	return nil
}

// readSourceFileAsString reads a source file and returns its content as string.
// When BaseDir is set on the reporter, relative source paths are resolved
// against BaseDir first, then the process CWD. Absolute paths are tried as-is
// before falling back to CWD-relative resolution.
func (r *HTMLReporter) readSourceFileAsString(filePath string) (string, error) {
	candidates := r.sourcePathCandidates(filePath)
	var lastErr error
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil {
			return string(data), nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidates generated")
	}
	return "", fmt.Errorf("cannot open file: %w", lastErr)
}

// sourcePathCandidates returns the ordered list of paths to try when reading a
// source file. Absolute paths skip base-dir resolution.
func (r *HTMLReporter) sourcePathCandidates(filePath string) []string {
	if filepath.IsAbs(filePath) {
		return []string{filePath}
	}
	var out []string
	if r.BaseDir != "" {
		out = append(out, filepath.Join(r.BaseDir, filepath.FromSlash(filePath)))
	}
	if cwd, err := os.Getwd(); err == nil {
		out = append(out, filepath.Join(cwd, filepath.FromSlash(filePath)))
	} else {
		out = append(out, filePath)
	}
	return out
}

// getCoverageClass returns the CSS class for coverage styling
func (r *HTMLReporter) getCoverageClass(hitCount int) string {
	if hitCount == 0 {
		return "cov0"
	}
	return "cov10" // Fully covered, TODO: implement gradient if needed
}

// writeFooter writes the HTML document footer with JavaScript
func (r *HTMLReporter) writeFooter(writer io.Writer) error {
	_, err := writer.Write([]byte(`	</div>
	</body>
	<script>
	(function() {
		var files = document.getElementById('files');
		var visible;
		files.addEventListener('change', onChange, false);
		function select(part) {
			if (visible)
				visible.style.display = 'none';
			visible = document.getElementById(part);
			if (!visible)
				return;
			files.value = part;
			visible.style.display = 'block';
			location.hash = part;
		}
		function onChange() {
			select(files.value);
			window.scrollTo(0, 0);
		}
		if (location.hash != "") {
			select(location.hash.substr(1));
		}
		if (!visible) {
			select("file0");
		}
	})();
	</script>
</html>
`))
	return err
}

// FormatString returns coverage data as an HTML string
func (r *HTMLReporter) FormatString(cov *coverage.Coverage) (string, error) {
	var buf strings.Builder
	if err := r.Format(cov, &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Name returns the name of this reporter
func (r *HTMLReporter) Name() string {
	return "html"
}
