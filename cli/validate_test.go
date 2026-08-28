package cli

import (
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
