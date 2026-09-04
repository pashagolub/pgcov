package instrument_test

import (
	"testing"

	"github.com/cybertec-postgresql/pgcov/internal/instrument"
)

// TestParseSignalID_RoundTrip pins the format against its own generator,
// including the Windows case the parser is written for: an absolute path
// carries a drive-letter colon, so only the last two colons are separators.
func TestParseSignalID_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		file string
	}{
		{"relative path", "auth/authenticate.sql"},
		{"windows relative path", `auth\authenticate.sql`},
		{"windows absolute path with drive colon", `C:\proj\auth\authenticate.sql`},
		{"path containing colons", "weird:name:file.sql"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := instrument.FormatSignalID(tc.file, 42, 17)
			file, startPos, length, err := instrument.ParseSignalID(id)
			if err != nil {
				t.Fatalf("ParseSignalID(%q): %v", id, err)
			}
			if file != tc.file || startPos != 42 || length != 17 {
				t.Errorf("got (%q, %d, %d), want (%q, 42, 17)", file, startPos, length, tc.file)
			}
		})
	}
}

// TestParseSignalID_Invalid covers what the previous fmt.Sscanf("%d")-based
// parseNumber accepted silently: it consumed the leading digits, ignored the
// rest and reported success, so "a.sql:12abc:5" parsed as position 12.
func TestParseSignalID_Invalid(t *testing.T) {
	cases := []struct {
		name     string
		signalID string
	}{
		{"trailing garbage in start position", "a.sql:12abc:5"},
		{"trailing garbage in length", "a.sql:12:5abc"},
		{"non-numeric start position", "a.sql:xx:5"},
		{"non-numeric length", "a.sql:12:yy"},
		{"too few parts", "a.sql:12"},
		{"no colons at all", "a.sql"},
		{"empty start position", "a.sql::5"},
		{"empty length", "a.sql:12:"},
		{"negative start position", "a.sql:-1:5"},
		{"negative length", "a.sql:12:-5"},
		{"float length", "a.sql:12:5.5"},
		{"spaced length", "a.sql:12: 5"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := instrument.ParseSignalID(tc.signalID); err == nil {
				t.Errorf("ParseSignalID(%q) succeeded; malformed signal IDs must be rejected", tc.signalID)
			}
		})
	}
}
