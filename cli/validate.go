package cli

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Deliberately looser than openapi3.FormatOfStringForUUIDOfRFC4122, which pins
// the version nibble to 1-5 and would warn on a UUIDv7 — a common choice for
// new APIs. For a check that only warns, the shape is the part worth asserting.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidateEnum reports whether value is one of allowed. An empty allowed list
// means the schema did not constrain the value.
func ValidateEnum(label, value string, allowed []string) error {
	if len(allowed) == 0 {
		return nil
	}

	for _, candidate := range allowed {
		if candidate == value {
			return nil
		}
	}

	return fmt.Errorf("%s: %q is not one of [%s]", label, value, strings.Join(allowed, ", "))
}

// ValidateFormat checks value against an OpenAPI string `format`. Unknown
// formats pass: the list of formats in the wild is open-ended, and a format we
// do not understand is not evidence that the value is wrong.
func ValidateFormat(label, value, format string) error {
	switch format {
	case "uuid":
		if !uuidPattern.MatchString(value) {
			return fmt.Errorf("%s: %q is not a UUID", label, value)
		}
	case "uri", "url":
		parsed, err := url.Parse(value)
		if err != nil {
			return fmt.Errorf("%s: %q is not a valid URI: %w", label, value, err)
		}
		if parsed.Scheme == "" {
			return fmt.Errorf("%s: %q is not an absolute URI", label, value)
		}
	}

	return nil
}

// CheckParam applies whichever constraints the schema declared for a parameter
// and refuses the request if the value does not meet them, as a usage error so
// the CLI exits 2. Request-body fields are checked the same way, in
// ApplyBodyFlags.
//
// A schema that is stricter than the API it describes — `format: uuid` on an
// API that issues prefixed IDs, an enum list that lags the deployed server —
// would make this reject a request the server would have accepted. That is what
// `x-cli-no-validate` is for: it drops the constraint at generation time, so
// there is no runtime branch to get wrong.
func CheckParam(label, value, format string, allowed []string) error {
	if err := ValidateEnum(label, value, allowed); err != nil {
		return NewUsageError(err)
	}

	if err := ValidateFormat(label, value, format); err != nil {
		return NewUsageError(err)
	}

	return nil
}
