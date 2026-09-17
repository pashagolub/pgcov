package instrument

import (
	"fmt"
	"strconv"
)

// FormatSignalID generates a signal ID for a coverage point.
// Format: {file}:{startPos}:{length}
func FormatSignalID(file string, startPos int, length int) string {
	return fmt.Sprintf("%s:%d:%d", file, startPos, length)
}

// ParseSignalID parses a signal ID into file, startPos, and length.
// Signal format: file:startPos:length
// Note: file path may contain colons on Windows (C:\path\to\file.sql), so the
// tail is anchored and only the last two colons are treated as separators.
func ParseSignalID(signalID string) (file string, startPos int, length int, err error) {
	// Find the last two colons from the end.
	colons := []int{}
	for i := len(signalID) - 1; i >= 0; i-- {
		if signalID[i] == ':' {
			colons = append(colons, i)
			if len(colons) >= 2 {
				break
			}
		}
	}

	if len(colons) < 2 {
		return "", 0, 0, fmt.Errorf("invalid signal ID format (expected 3 parts): %s", signalID)
	}

	// Format: file:startPos:length
	// colons[0] = lastColon, colons[1] = secondLast
	secondLastColon := colons[1]
	lastColon := colons[0]

	file = signalID[:secondLastColon]
	startPosStr := signalID[secondLastColon+1 : lastColon]
	lengthStr := signalID[lastColon+1:]

	startPos, err = parseNumber(startPosStr)
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid start position in signal ID %s: %w", signalID, err)
	}

	length, err = parseNumber(lengthStr)
	if err != nil {
		return "", 0, 0, fmt.Errorf("invalid length in signal ID %s: %w", signalID, err)
	}

	if startPos < 0 {
		return "", 0, 0, fmt.Errorf("start position must be non-negative, got %d", startPos)
	}
	if length < 0 {
		return "", 0, 0, fmt.Errorf("length must be non-negative, got %d", length)
	}

	return file, startPos, length, nil
}

// parseNumber parses a decimal integer, rejecting anything the string does not
// consume entirely.
//
// fmt.Sscanf("%d") was used here previously; it stops at the first byte that
// does not fit the verb and still reports success, so "12abc" parsed as 12.
// strconv.Atoi requires the whole string to be a number.
func parseNumber(s string) (int, error) {
	num, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("failed to parse number %q: %w", s, err)
	}
	return num, nil
}
