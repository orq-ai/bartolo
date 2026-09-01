package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// timeNow lets tests pin "now".
var timeNow = time.Now

// time.ParseDuration stops at hours, so d and w are expanded before it runs.
var dayOrWeekUnit = regexp.MustCompile(`(\d+(?:\.\d+)?)([dw])`)

var dateOnlyLayouts = []string{"2006-01-02", "2006-01-02 15:04:05"}

const dateTimeHint = "use RFC 3339 (2026-08-31T17:40:00Z), a date (2026-08-31), " +
	"or a relative value such as 24h, 7d, 30m, now, now-24h"

// DateTimeFlagHelp is referenced by generated code, so body-field and parameter
// flags describe themselves the same way.
const DateTimeFlagHelp = "(RFC 3339 timestamp, or relative: 24h, 7d, now, now-24h)"

// WithDateTimeHelp appends DateTimeFlagHelp to a description the spec may not
// have supplied — parameters often have none.
func WithDateTimeHelp(description string) string {
	if description == "" {
		return DateTimeFlagHelp
	}
	return description + " " + DateTimeFlagHelp
}

// NormalizeDateTime converts a user-supplied timestamp into RFC 3339, which is
// what an OpenAPI `format: date-time` field expects on the wire.
//
// Accepted forms:
//
//   - RFC 3339, returned verbatim so numeric offsets ("+02:00") and sub-second
//     precision survive byte-for-byte.
//   - A bare date, "2026-08-31", or "2026-08-31 15:04:05", read as UTC.
//   - "now".
//   - "now-<duration>" / "now+<duration>", offset from now.
//   - A bare "<duration>", meaning that long ago: "24h", "7d", "30m", "2w", "1h30m".
//
// The grammar is the de facto Prometheus/Grafana one, not ISO 8601 durations
// ("P1D"), which are rejected. The two are disjoint — ISO always starts with an
// optionally-signed "P" — so ISO could be added later without ambiguity.
//
// "d" and "w" are fixed spans, 24h and 168h, so no calendar arithmetic and no
// timezone to pick.
func NormalizeDateTime(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", fmt.Errorf("%q is not a timestamp: %s", value, dateTimeHint)
	}

	if _, err := time.Parse(time.RFC3339, raw); err == nil {
		return raw, nil
	}

	for _, layout := range dateOnlyLayouts {
		if t, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}

	now := timeNow().UTC()

	lower := strings.ToLower(raw)
	if lower == "now" {
		return now.Format(time.RFC3339), nil
	}

	if rest, ok := strings.CutPrefix(lower, "now"); ok {
		if len(rest) > 1 && (rest[0] == '-' || rest[0] == '+') {
			d, err := parseRelativeDuration(rest[1:])
			if err != nil {
				return "", fmt.Errorf("%q is not a timestamp: %s", value, dateTimeHint)
			}
			if rest[0] == '-' {
				d = -d
			}
			return now.Add(d).Format(time.RFC3339), nil
		}
		return "", fmt.Errorf("%q is not a timestamp: %s", value, dateTimeHint)
	}

	// A bare duration means "ago". A signed one is rejected: "-24h" is ambiguous
	// against "now-24h", and pflag reads the leading "-" as a flag anyway.
	d, err := parseRelativeDuration(lower)
	if err != nil || d < 0 {
		return "", fmt.Errorf("%q is not a timestamp: %s", value, dateTimeHint)
	}
	return now.Add(-d).Format(time.RFC3339), nil
}

// NormalizeDateTimeFlag is NormalizeDateTime with the error attributed to the
// flag the value came from. Used for body fields; parameters go through
// NormalizeParam, which already has a label.
func NormalizeDateTimeFlag(flagName, value string) (string, error) {
	normalized, err := NormalizeDateTime(value)
	if err != nil {
		return "", fmt.Errorf("--%s: %w", flagName, err)
	}
	return normalized, nil
}

// parseRelativeDuration is time.ParseDuration with "d" (24h) and "w" (168h)
// units. Expanding them up front keeps compounds like "1h30m" and ParseDuration's
// unit validation for free.
func parseRelativeDuration(value string) (time.Duration, error) {
	expanded := dayOrWeekUnit.ReplaceAllStringFunc(value, func(match string) string {
		parts := dayOrWeekUnit.FindStringSubmatch(match)
		n, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return match
		}
		hours := 24.0
		if parts[2] == "w" {
			hours = 168.0
		}
		return strconv.FormatFloat(n*hours, 'f', -1, 64) + "h"
	})

	return time.ParseDuration(expanded)
}
