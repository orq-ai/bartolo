package cli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinnedNow is the instant every relative case in this file resolves against.
var pinnedNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func pinTime(t *testing.T) {
	t.Helper()
	original := timeNow
	timeNow = func() time.Time { return pinnedNow }
	t.Cleanup(func() { timeNow = original })
}

func TestNormalizeDateTimeAccepts(t *testing.T) {
	pinTime(t)

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		// RFC 3339 is handed back byte-for-byte, offsets and precision intact.
		{"rfc3339 zulu", "2026-08-31T17:40:00Z", "2026-08-31T17:40:00Z"},
		{"rfc3339 offset", "2026-08-31T00:00:00+02:00", "2026-08-31T00:00:00+02:00"},
		{"rfc3339 subsecond", "2026-08-31T17:40:00.123456Z", "2026-08-31T17:40:00.123456Z"},

		{"date only", "2026-08-31", "2026-08-31T00:00:00Z"},
		{"date and time", "2026-08-31 14:30:00", "2026-08-31T14:30:00Z"},

		{"now", "now", "2026-09-01T12:00:00Z"},
		{"now minus hours", "now-24h", "2026-08-31T12:00:00Z"},
		{"now plus hours", "now+1h", "2026-09-01T13:00:00Z"},
		{"now minus days", "now-7d", "2026-08-25T12:00:00Z"},

		{"bare hours", "24h", "2026-08-31T12:00:00Z"},
		{"bare minutes", "30m", "2026-09-01T11:30:00Z"},
		{"bare days", "7d", "2026-08-25T12:00:00Z"},
		{"bare weeks", "2w", "2026-08-18T12:00:00Z"},
		{"bare compound", "1h30m", "2026-09-01T10:30:00Z"},
		{"fractional days", "0.5d", "2026-09-01T00:00:00Z"},
		{"uppercase now", "NOW", "2026-09-01T12:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeDateTime(tc.input)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestNormalizeDateTimeDayAndWeekAreFixedSpans pins the deliberate choice that a
// day is exactly 24h and a week exactly 168h, with no calendar or DST arithmetic.
func TestNormalizeDateTimeDayAndWeekAreFixedSpans(t *testing.T) {
	pinTime(t)

	for input, span := range map[string]time.Duration{
		"7d": 7 * 24 * time.Hour,
		"2w": 2 * 168 * time.Hour,
	} {
		got, err := NormalizeDateTime(input)
		require.NoError(t, err)

		parsed, err := time.Parse(time.RFC3339, got)
		require.NoError(t, err)
		assert.Equal(t, span, pinnedNow.Sub(parsed), "%s should be exactly %s before now", input, span)
	}
}

func TestNormalizeDateTimeRejects(t *testing.T) {
	pinTime(t)

	for _, tc := range []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"whitespace", "   "},
		{"dangling sign", "now-"},
		{"unknown unit", "24x"},
		{"words", "banana"},
		{"now prefix without sign", "nowish"},
		// A signed bare duration is ambiguous against "now-24h", and pflag would
		// read the leading "-" as a flag anyway.
		{"negative bare duration", "-24h"},

		// ISO 8601 durations are rejected by design: the CLI implements the
		// Prometheus-style grammar only. Adding ISO support later is unambiguous
		// (it always starts with an optionally-signed "P"), but until then these
		// must fail locally rather than reach the API.
		{"iso duration signed", "-P1D"},
		{"iso duration", "P1D"},
		{"iso duration with time", "PT30M"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeDateTime(tc.input)
			require.Error(t, err)
			// The message has to name the accepted forms, per the ticket.
			assert.Contains(t, err.Error(), "RFC 3339")
			assert.Contains(t, err.Error(), "24h")
			assert.Contains(t, err.Error(), "now-24h")
		})
	}
}

func TestNormalizeDateTimeFlagNamesTheFlag(t *testing.T) {
	pinTime(t)

	got, err := NormalizeDateTimeFlag("from", "24h")
	require.NoError(t, err)
	assert.Equal(t, "2026-08-31T12:00:00Z", got)

	_, err = NormalizeDateTimeFlag("from", "banana")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--from")
}
