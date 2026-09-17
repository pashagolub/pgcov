package coverage_test

import (
	"testing"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
)

// TestParsePositionKey covers the round trip and, more importantly, the inputs
// the previous fmt.Sscanf("%d:%d") implementation accepted silently: it stopped
// at the first byte that did not fit the verb and still returned a nil error,
// so "10:20junk" parsed as (10, 20).
func TestParsePositionKey(t *testing.T) {
	valid := []struct {
		key      string
		startPos int
		length   int
	}{
		{"0:0", 0, 0},
		{"10:20", 10, 20},
		{"4096:1", 4096, 1},
	}

	for _, tc := range valid {
		t.Run("valid/"+tc.key, func(t *testing.T) {
			startPos, length, err := coverage.ParsePositionKey(tc.key)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if startPos != tc.startPos || length != tc.length {
				t.Errorf("got (%d, %d), want (%d, %d)", startPos, length, tc.startPos, tc.length)
			}
		})
	}

	invalid := []struct {
		name string
		key  string
	}{
		{"trailing garbage after length", "10:20junk"},
		{"trailing garbage after start", "10abc:20"},
		{"missing separator", "1020"},
		{"empty", ""},
		{"empty halves", ":"},
		{"missing length", "10:"},
		{"missing start", ":20"},
		{"three parts", "1:2:3"},
		{"negative start", "-1:20"},
		{"negative length", "10:-20"},
		{"leading space", " 10:20"},
		{"float", "10.5:20"},
		{"hex", "0x10:20"},
	}

	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			if _, _, err := coverage.ParsePositionKey(tc.key); err == nil {
				t.Errorf("ParsePositionKey(%q) succeeded; malformed keys must be rejected", tc.key)
			}
		})
	}
}
