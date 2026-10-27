package mcpserver

import (
	"encoding/json"
	"strconv"
)

// CoerceString renders a decoded JSON value as the plain string the generated
// client funcs expect for path/query/header parameters. JSON numbers arrive as
// float64, so a naive fmt.Sprintf("%v", ...) would emit float artifacts (e.g.
// "1e+06" or "3.0"); this uses strconv.FormatFloat with the shortest exact
// representation instead. Integers that arrive as json.Number are preserved
// verbatim. Non-scalar values fall back to their compact JSON encoding.
func CoerceString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}
