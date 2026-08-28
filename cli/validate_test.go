package cli

import (
	"errors"
	"testing"
)

func TestValidateEnum(t *testing.T) {
	allowed := []string{"active", "archived"}

	if err := ValidateEnum("--status", "active", allowed); err != nil {
		t.Errorf("ValidateEnum(active) = %v, want nil", err)
	}

	// An unconstrained schema must not reject anything.
	if err := ValidateEnum("--status", "anything", nil); err != nil {
		t.Errorf("ValidateEnum with no enum = %v, want nil", err)
	}

	// Enums are case-sensitive, as JSON Schema defines them.
	for _, value := range []string{"Active", "deleted", ""} {
		if err := ValidateEnum("--status", value, allowed); err == nil {
			t.Errorf("ValidateEnum(%q) = nil, want error", value)
		}
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
		if err := ValidateFormat("--id", c.value, c.format); err != nil {
			t.Errorf("ValidateFormat(%q, %q) = %v, want nil", c.value, c.format, err)
		}
	}

	invalid := []struct{ value, format string }{
		{"cus_1a2b3c", "uuid"},
		{"550e8400e29b41d4a716446655440000", "uuid"},
		{"", "uuid"},
		{"/relative/path", "uri"},
		{"example.com", "uri"},
	}
	for _, c := range invalid {
		if err := ValidateFormat("--id", c.value, c.format); err == nil {
			t.Errorf("ValidateFormat(%q, %q) = nil, want error", c.value, c.format)
		}
	}
}

func TestCheckParamChecksBoth(t *testing.T) {
	err := CheckParam("--id", "cus_1", "uuid", []string{"cus_1"})
	if err == nil {
		t.Fatal("CheckParam = nil, want a format error even when the enum matches")
	}
}

// A schema mismatch is the user's mistake, so it exits 2 like any other bad
// input rather than 1, which means the request failed.
func TestCheckParamIsUsageError(t *testing.T) {
	var usage *UsageError
	if err := CheckParam("--kind", "external", "", []string{"internal", "a2a"}); !errors.As(err, &usage) {
		t.Errorf("CheckParam returned %#v, want a *UsageError", err)
	}

	if err := CheckParam("--kind", "internal", "", []string{"internal", "a2a"}); err != nil {
		t.Errorf("CheckParam on a matching value = %v, want nil", err)
	}
}
