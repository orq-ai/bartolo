package cli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		// Handed back byte-for-byte.
		{"rfc3339 zulu", "2026-08-31T17:40:00Z", "2026-08-31T17:40:00Z"},
		{"rfc3339 offset", "2026-08-31T00:00:00+02:00", "2026-08-31T00:00:00+02:00"},
		{"rfc3339 subsecond", "2026-08-31T17:40:00.123456Z", "2026-08-31T17:40:00.123456Z"},

		{"date only", "2026-08-31", "2026-08-31T00:00:00Z"},
		{"date and time", "2026-08-31 14:30:00", "2026-08-31T14:30:00Z"},
		// RFC 3339 minus the zone: the likeliest near-miss, so it is read as UTC
		// rather than falling through to the duration parser.
		{"date and time t-separated", "2026-08-31T14:30:00", "2026-08-31T14:30:00Z"},

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
		// A bare duration always means "ago", so a sign is refused rather than
		// guessed at: "-24h" is ambiguous against "now-24h", and "+24h" would
		// otherwise parse as a positive duration and silently mean "24h" ago.
		{"negative bare duration", "-24h"},
		{"positive bare duration", "+24h"},

		// time.ParseDuration accepts a second sign, which would flip the one the
		// "now" branch already applied: "now--24h" would land in the future.
		{"doubled sign minus minus", "now--24h"},
		{"doubled sign plus minus", "now+-1h"},
		{"doubled sign minus plus", "now-+1h"},

		// The accepted grammar is time.ParseDuration plus "d" (24h) and "w"
		// (168h), so ISO 8601 durations fall outside it. Pinned so they are not
		// added by accident: they start with an optionally-signed "P", which the
		// grammar above never produces, so they could be added later without
		// ambiguity.
		{"iso duration signed", "-P1D"},
		{"iso duration", "P1D"},
		{"iso duration with time", "PT30M"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeDateTime(tc.input)
			require.Error(t, err)
			// The ticket requires the message to name the accepted forms.
			assert.Contains(t, err.Error(), "RFC 3339")
			assert.Contains(t, err.Error(), "24h")
			assert.Contains(t, err.Error(), "now-24h")
		})
	}
}

// The flag help and the rejection message are two wordings of one contract, so
// each accepted form has to appear in both.
func TestDateTimeHelpAndHintNameTheSameForms(t *testing.T) {
	for _, form := range []string{
		"RFC 3339", "2026-08-31", "2026-08-31 14:00:00",
		"24h", "7d", "2w", "30m", "now", "now-24h", "now+1h",
	} {
		assert.Contains(t, dateTimeHint, form)
		assert.Contains(t, DateTimeFlagHelp, form)
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
