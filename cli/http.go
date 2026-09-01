package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v2"

	"github.com/alecthomas/chroma/quick"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"gopkg.in/h2non/gentleman.v2"
	"gopkg.in/h2non/gentleman.v2/context"
)

// HTTP Client Errors
var (
	ErrCannotUnmarshal = errors.New("Unable to unmarshal response")
)

func indent(value string) string {
	trimmed := strings.TrimSuffix(value, "\x1b[0m")
	trimmed = strings.TrimRight(trimmed, "\n")
	return "  ╎ " + strings.Replace(trimmed, "\n", "\n  ╎ ", -1) + "\n"
}

// getBody returns and wraps the request/response body in a new reader, which
// is useful for logging purposes.
func getBody(r io.ReadCloser) (string, io.ReadCloser, error) {
	newReader := r

	body := ""
	if r != nil {
		data, err := ioutil.ReadAll(r)
		if err != nil {
			return "", nil, err
		}

		if len(data) > 0 {
			body = "\n" + string(data) + "\n"
			newReader = ioutil.NopCloser(bytes.NewReader(data))
		}
	}

	return body, newReader, nil
}

// UserAgentMiddleware sets the user-agent header on requests.
func UserAgentMiddleware() {
	Client.UseRequest(func(ctx *context.Context, h context.Handler) {
		ctx.Request.Header.Set("User-Agent", viper.GetString("app-name")+"-cli-"+Root.Version)
		h.Next(ctx)
	})
}

// redactHeaderValue masks the value of a sensitive header so `--verbose` never
// prints an API key, bearer token, or cookie. What counts as sensitive is
// looksSensitiveKey, the same predicate the configuration dump and the profile
// listing use: the transport names this used to list separately
// (authorization, proxy-authorization, cookie, set-cookie) all match its
// "auth" and "cookie" hints.
func redactHeaderValue(key, val string) string {
	if looksSensitiveKey(key) {
		return "[REDACTED]"
	}
	return val
}

// redactURL renders a URL with the value of every sensitive query parameter masked.
// An API key can be carried in the query rather than a header (apikey.LocationQuery),
// and `--verbose` prints the request line, so the URL needs the same treatment the
// headers get.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	if u.RawQuery == "" {
		return u.String()
	}

	query := u.Query()
	redacted := false
	for name := range query {
		if !looksSensitiveKey(name) {
			continue
		}
		query.Set(name, "[REDACTED]")
		redacted = true
	}
	if !redacted {
		return u.String()
	}

	// Copy: the request is logged before it goes out, and must go out intact.
	clean := *u
	clean.RawQuery = query.Encode()
	return clean.String()
}

// redactBody masks credentials inside a request or response body so
// `--verbose` does not print what the header and URL redaction just kept out
// of the log: an OAuth token exchange carries client_secret in a form body and
// returns access_token in a JSON one, and any endpoint that mints a key
// returns it in its response.
//
// Only JSON and form-encoded bodies can be redacted, since redaction needs
// field names. Anything else is logged unchanged, so a credential in an opaque
// body -- or in a JSON document carried as a string inside another one, which
// is one value to this walk -- still reaches the log. That is the accepted
// limit of a key-based rule.
// The original text is returned untouched when nothing matched, so a body the
// user is trying to debug is not silently reformatted.
func redactBody(contentType, body string) string {
	if strings.TrimSpace(body) == "" {
		return body
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(contentType))
	}

	if mediaType == "application/x-www-form-urlencoded" {
		return redactFormBody(body)
	}

	// The content type is advisory: a server can return a JSON error body
	// labelled text/plain. Try to parse regardless, and fall back to the
	// original text when it is not JSON after all.
	return redactJSONBody(body)
}

func redactJSONBody(body string) string {
	var parsed interface{}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return body
	}

	redacted, changed := redactJSONValue("", parsed)
	if !changed {
		return body
	}

	out, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		// The value came out of encoding/json, so this should not happen; if
		// it does, dropping the body beats printing the unredacted one.
		return "[REDACTED BODY]"
	}
	return string(out)
}

// redactJSONValue walks a decoded JSON document, masking every value whose key
// looks like a credential at any depth. Array elements inherit the key of the
// array they are in: a list under "api_keys" is a list of secrets.
func redactJSONValue(key string, value interface{}) (interface{}, bool) {
	switch typed := value.(type) {
	case map[string]interface{}:
		if looksSensitiveKey(key) {
			return "[REDACTED]", true
		}
		out := make(map[string]interface{}, len(typed))
		changed := false
		for k, v := range typed {
			redacted, hit := redactJSONValue(k, v)
			out[k] = redacted
			changed = changed || hit
		}
		return out, changed
	case []interface{}:
		if looksSensitiveKey(key) {
			return "[REDACTED]", true
		}
		out := make([]interface{}, len(typed))
		changed := false
		for i, item := range typed {
			redacted, hit := redactJSONValue(key, item)
			out[i] = redacted
			changed = changed || hit
		}
		return out, changed
	default:
		if looksSensitiveKey(key) {
			return "[REDACTED]", true
		}
		return typed, false
	}
}

func redactFormBody(body string) string {
	values, err := url.ParseQuery(body)
	if err != nil {
		return body
	}

	changed := false
	for name := range values {
		if !looksSensitiveKey(name) {
			continue
		}
		values.Set(name, "[REDACTED]")
		changed = true
	}
	if !changed {
		return body
	}
	return values.Encode()
}

// LogMiddleware adds verbose log info to HTTP requests.
func LogMiddleware(useColor bool) {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))

	Client.UseRequest(func(ctx *context.Context, h context.Handler) {
		l := log.With().Str("request-id", fmt.Sprintf("%x", rnd.Uint64())).Logger()
		ctx.Set("log", &l)

		h.Next(ctx)
	})

	Client.UseHandler("before dial", func(ctx *context.Context, h context.Handler) {
		ctx.Set("start", time.Now())

		log := ctx.Get("log").(*zerolog.Logger)

		// Make the request body available to downstream processors through the
		// request context as `request-body`.
		body, newReader, err := getBody(ctx.Request.Body)
		if err != nil {
			h.Error(ctx, err)
			return
		}
		ctx.Set("request-body", body)
		ctx.Request.Body = newReader

		if viper.GetBool("verbose") {
			headers := ""
			for key, val := range ctx.Request.Header {
				headers += key + ": " + redactHeaderValue(key, val[0]) + "\n"
			}

			if body != "" {
				body = "\n" + redactBody(ctx.Request.Header.Get("Content-Type"), body)
			}

			http := fmt.Sprintf("%s %s %s\n%s%s", ctx.Request.Method, redactURL(ctx.Request.URL), ctx.Request.Proto, headers, body)

			if useColor {
				sb := strings.Builder{}
				if err := quick.Highlight(&sb, http, "http", "terminal256", "cli-dark"); err != nil {
					h.Error(ctx, err)
				}
				http = sb.String()
			}

			log.Debug().Msgf("Making request:\n%s", indent(http))
		}

		h.Next(ctx)
	})

	Client.UseResponse(func(ctx *context.Context, h context.Handler) {
		l := ctx.Get("log").(*zerolog.Logger)

		if viper.GetBool("verbose") {
			headers := ""
			for key, val := range ctx.Response.Header {
				headers += key + ": " + redactHeaderValue(key, val[0]) + "\n"
			}

			body, newReader, err := getBody(ctx.Response.Body)
			if err != nil {
				h.Error(ctx, err)
				return
			}
			ctx.Response.Body = newReader

			http := fmt.Sprintf("%s %s\n%s\n%s", ctx.Response.Proto, ctx.Response.Status, headers, redactBody(ctx.Response.Header.Get("Content-Type"), body))

			if useColor {
				sb := strings.Builder{}
				if err := quick.Highlight(&sb, http, "http", "terminal256", "cli-dark"); err != nil {
					h.Error(ctx, err)
				}
				http = sb.String()
			}

			l.Debug().Msgf("Got response in %s:\n%s", time.Since(ctx.Get("start").(time.Time)), indent(http))
		}

		h.Next(ctx)
	})
}

// UnmarshalRequest body into a given structure `s`. Supports both JSON and
// YAML depending on the request's content-type header.
func UnmarshalRequest(ctx *context.Context, s interface{}) error {
	return unmarshalBody(ctx.Request.Header, []byte(ctx.GetString("request-body")), s)
}

// UnmarshalResponse into a given structure `s`. Supports both JSON and
// YAML depending on the response's content-type header.
func UnmarshalResponse(resp *gentleman.Response, s interface{}) error {
	data := resp.Bytes()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d:\n%s", resp.StatusCode, string(data))
	}

	return unmarshalBody(resp.Header, data, s)
}

func unmarshalBody(headers http.Header, data []byte, s interface{}) error {
	if len(data) == 0 {
		return nil
	}

	ct := headers.Get("content-type")
	if strings.Contains(ct, "json") || strings.Contains(ct, "javascript") {
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
	} else if strings.Contains(ct, "yaml") {
		if err := yaml.Unmarshal(data, &s); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("Not sure how to unmarshal %s", ct)
	}

	return nil
}
