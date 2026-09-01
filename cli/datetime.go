package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// timeNow is the clock used to resolve relative timestamps. Tests replace it.
var timeNow = time.Now

// dayOrWeekUnit matches a `<number>d` or `<number>w` component of a duration.
// Go's time.ParseDuration deliberately stops at hours, so these are expanded to
// hours before it sees them.
var dayOrWeekUnit = regexp.MustCompile(`(\d+(?:\.\d+)?)([dw])`)

// dateOnlyLayouts are the shorthand date forms accepted in addition to RFC 3339.
// Both are interpreted as UTC.
var dateOnlyLayouts = []string{"2006-01-02", "2006-01-02 15:04:05"}

const dateTimeHint = "use RFC 3339 (2026-08-31T17:40:00Z), a date (2026-08-31), " +
	"or a relative value such as 24h, 7d, 30m, now, now-24h"

// DateTimeFlagHelp is the suffix appended to the help text of every flag that
// accepts a date-time, whether it is a body field or a query parameter. Generated
// code references it so the two stay in step.
const DateTimeFlagHelp = "(RFC 3339 timestamp, or relative: 24h, 7d, now, now-24h)"

// WithDateTimeHelp appends DateTimeFlagHelp to a flag description. The
// description is whatever the spec supplied, which for a parameter is often
// nothing at all, so the separator is only added when there is something to
// separate.
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
// The relative grammar is the de facto Prometheus/Grafana one rather than ISO 8601
// durations ("P1D", "PT30M"), because it is what people type at a terminal. The two
// are disjoint — an ISO duration always starts with an optionally-signed "P" — so
// ISO support could be added later without ambiguity.
//
// "d" and "w" are fixed spans: 1d is exactly 24h and 1w is exactly 168h. No calendar
// or DST arithmetic is attempted, which also avoids having to pick a timezone for a
// CLI talking to a UTC API.
func NormalizeDateTime(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", fmt.Errorf("%q is not a timestamp: %s", value, dateTimeHint)
	}

	// Already RFC 3339: hand it back untouched.
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
		// "now-24h" / "now+2h": the sign belongs to the offset.
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
	// against "now-24h", and a leading "-" is a flag to pflag anyway.
	d, err := parseRelativeDuration(lower)
	if err != nil || d < 0 {
		return "", fmt.Errorf("%q is not a timestamp: %s", value, dateTimeHint)
	}
	return now.Add(-d).Format(time.RFC3339), nil
}

// NormalizeDateTimeFlag is NormalizeDateTime with the error attributed to the flag
// the value came from. Generated code calls this so it needs no error-formatting
// imports of its own.
func NormalizeDateTimeFlag(flagName, value string) (string, error) {
	normalized, err := NormalizeDateTime(value)
	if err != nil {
		return "", fmt.Errorf("--%s: %w", flagName, err)
	}
	return normalized, nil
}

// parseRelativeDuration is time.ParseDuration extended with "d" (24h) and "w"
// (168h) units. Expanding those into hours up front keeps compounds like "1h30m"
// and all of ParseDuration's unit validation for free.
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
