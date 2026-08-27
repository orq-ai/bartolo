package cli

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"
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

// ValidateParam applies whichever constraints the schema declared for a
// parameter.
func ValidateParam(label, value, format string, allowed []string) error {
	if err := ValidateEnum(label, value, allowed); err != nil {
		return err
	}

	return ValidateFormat(label, value, format)
}

// WarnIfInvalid logs a schema-validation failure as a warning instead of
// returning it. Generated commands send the request anyway: an OpenAPI `enum`
// or `format` is often aspirational — `format: uuid` on an API that issues
// prefixed IDs, an enum list that lags the deployed server — so the server, not
// the schema, decides what is valid.
func WarnIfInvalid(err error) {
	if err != nil {
		log.Warn().Msg(err.Error())
	}
}
