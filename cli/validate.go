package cli

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"
)

// Mirrors openapi3.FormatOfStringForUUIDOfRFC4122. Copied rather than imported
// so generated binaries do not link kin-openapi for one constant.
var uuidPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}|00000000-0000-0000-0000-000000000000)$`)

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

// WarnIfInvalid reports a parameter the schema says the API will reject, and
// sends the request anyway. An OpenAPI `enum` or `format` is often aspirational
// — `format: uuid` on an API that issues prefixed IDs, an enum list that lags
// the deployed server — so the server, not the schema, decides what is valid.
func WarnIfInvalid(err error) {
	if err != nil {
		log.Warn().Msg(err.Error())
	}
}
