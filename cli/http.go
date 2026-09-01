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
	// An `X-Auth-Server` is exempted from masking by its name so the endpoint
	// stays diagnosable, but it can still hold a URL with a password in it.
	if namesALocation(strings.ToLower(key)) {
		if masked, ok := redactAddressValue(val).(string); ok {
			return masked
		}
	}
	return val
}

// redactURL renders a URL with its password and the value of every sensitive
// query parameter masked. An API key can be carried in the query rather than a
// header (apikey.LocationQuery), and `--verbose` prints the request line, so
// the URL needs the same treatment the headers get.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}

	// Copy unconditionally: the request is logged before it goes out and must
	// go out intact, and every exit from here has to run past the userinfo
	// strip below. An early return for the no-query case printed a
	// basic-auth password in full.
	clean := *u
	if clean.User != nil {
		clean.User = url.User(clean.User.Username())
	}

	query := clean.Query()
	if redactValues(query) {
		clean.RawQuery = query.Encode()
	}
	return clean.String()
}

// redactValues masks every sensitive entry in a set of query or form values in
// place, and reports whether it masked anything. The query string and a form
// body are the same shape, so they get the same walk.
func redactValues(values url.Values) bool {
	redacted := false
	for name, vals := range values {
		if looksSensitiveKey(name) {
			values.Set(name, "[REDACTED]")
			redacted = true
			continue
		}
		// A `redirect_uri` is exempt from masking by its name, and OAuth
		// carries one routinely -- with whatever its own query holds.
		if !namesALocation(strings.ToLower(name)) || len(vals) == 0 {
			continue
		}
		if masked, ok := redactAddressValue(vals[0]).(string); ok && masked != vals[0] {
			values.Set(name, masked)
			redacted = true
		}
	}
	return redacted
}

// redactBody masks credentials inside a request or response body so
// `--verbose` does not print what the header and URL redaction just kept out
// of the log: an OAuth token exchange carries client_secret in a form body and
// returns access_token in a JSON one, and any endpoint that mints a key
// returns it in its response.
//
// Only JSON and form-encoded bodies can be redacted, since redaction needs
// field names, and the two are recognised differently: JSON is parsed whatever
// the declared type says, because a server can return a JSON error body
// labelled text/plain, while a form body is recognised only by its exact media
// type.
//
// This fails OPEN, deliberately. Every body it cannot parse is logged
// verbatim: multipart, NDJSON, an event stream, XML, truncated or malformed
// JSON, a form body sent without its media type, and a JSON document carried
// as a string inside another one (one value to this walk). A credential in any
// of those still reaches the log. Failing closed would replace exactly the
// malformed bodies people turn `--verbose` on to look at with a placeholder,
// so the leak is the accepted cost of the flag being useful -- but it is a
// leak, and it is why `--verbose` output is not safe to paste unread.
//
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

	return redactJSONBody(body)
}

func redactJSONBody(body string) string {
	// UseNumber, and SetEscapeHTML(false) below: a body that trips redaction is
	// re-rendered from the decoded tree, and the default round trip is lossy.
	// It rounds every integer past 2^53 through float64 and escapes `&`, `<`
	// and `>`, so the dump would misreport the body it exists to help debug.
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()

	var parsed interface{}
	if err := decoder.Decode(&parsed); err != nil {
		return body
	}

	redacted, changed := redactTree("", parsed, maskRedacted)
	if !changed {
		return body
	}

	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(redacted); err != nil {
		// The value came out of encoding/json, so this should not happen; if
		// it does, dropping the body beats printing the unredacted one.
		return "[REDACTED BODY]"
	}
	return strings.TrimRight(out.String(), "\n")
}

// maskRedacted is the mask redactTree uses for wire bodies, where nothing of
// the value is worth keeping. The configuration dump passes maskHidden.
func maskRedacted(string, interface{}) interface{} {
	return "[REDACTED]"
}

func redactFormBody(body string) string {
	values, err := url.ParseQuery(body)
	if err != nil {
		return body
	}

	if !redactValues(values) {
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
