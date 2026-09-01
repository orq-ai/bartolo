package cli

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateEnum(t *testing.T) {
	allowed := []string{"active", "archived"}

	assert.NoError(t, ValidateEnum("--status", "active", allowed))

	// An unconstrained schema must not reject anything.
	assert.NoError(t, ValidateEnum("--status", "anything", nil))

	// Enums are case-sensitive, as JSON Schema defines them.
	for _, value := range []string{"Active", "deleted", ""} {
		assert.Errorf(t, ValidateEnum("--status", value, allowed), "ValidateEnum(%q)", value)
	}
}

func TestValidateFormat(t *testing.T) {
	valid := []struct{ value, format string }{
		{"550e8400-e29b-41d4-a716-446655440000", "uuid"},
		{"00000000-0000-0000-0000-000000000000", "uuid"},
		{"https://example.com/hook", "uri"},
		{"s3://bucket/key", "uri"},

		// An unrecognized format is not evidence the value is wrong.
		{"anything at all", "byte"},
		{"anything at all", ""},
	}
	for _, c := range valid {
		assert.NoErrorf(t, ValidateFormat("--id", c.value, c.format), "ValidateFormat(%q, %q)", c.value, c.format)
	}

	invalid := []struct{ value, format string }{
		{"cus_1a2b3c", "uuid"},
		{"550e8400e29b41d4a716446655440000", "uuid"},
		{"", "uuid"},
		{"/relative/path", "uri"},
		{"example.com", "uri"},
	}
	for _, c := range invalid {
		assert.Errorf(t, ValidateFormat("--id", c.value, c.format), "ValidateFormat(%q, %q)", c.value, c.format)
	}
}

func TestCheckParamChecksBoth(t *testing.T) {
	err := CheckParam("--id", "cus_1", "uuid", []string{"cus_1"})
	assert.Error(t, err, "CheckParam should report a format error even when the enum matches")
}

// A schema mismatch is the user's mistake, so it exits 2 like any other bad
// input rather than 1, which means the request failed.
func TestCheckParamIsUsageError(t *testing.T) {
	var usage *UsageError
	assert.ErrorAs(t, CheckParam("--kind", "external", "", []string{"internal", "a2a"}), &usage)
	assert.NoError(t, CheckParam("--kind", "internal", "", []string{"internal", "a2a"}))
}

func TestNormalizeParamDateTime(t *testing.T) {
	pinTime(t)

	got, err := NormalizeParam("--from", "24h", "date-time", nil)
	assert.NoError(t, err)
	assert.Equal(t, "2026-08-31T12:00:00Z", got)

	_, err = NormalizeParam("--from", "-P1D", "date-time", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "--from")
	assert.Contains(t, err.Error(), "now-24h")
	// A bad value is a usage error, so the CLI exits 2 rather than 1.
	var usageErr *UsageError
	assert.True(t, errors.As(err, &usageErr), "a bad timestamp should be a usage error")
}

// NormalizeParam stands in for CheckParam at every call site, so it has to keep
// doing CheckParam's job for the formats it does not normalize.
func TestNormalizeParamPassesThroughOtherFormats(t *testing.T) {
	got, err := NormalizeParam("--id", "550e8400-e29b-41d4-a716-446655440000", "uuid", nil)
	assert.NoError(t, err)
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", got)

	_, err = NormalizeParam("--id", "not-a-uuid", "uuid", nil)
	assert.Error(t, err)

	_, err = NormalizeParam("--status", "banana", "", []string{"active", "archived"})
	assert.Error(t, err)

	got, err = NormalizeParam("--cursor", "opaque", "", nil)
	assert.NoError(t, err)
	assert.Equal(t, "opaque", got)
}
