package cli

import (
	"context"
	"maps"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	gentleman "gopkg.in/h2non/gentleman.v2"
)

// BeforeHandlerFunc is a function that runs before a command sends a request
// over the wire. It may modify the request.
type BeforeHandlerFunc func(string, *viper.Viper, *gentleman.Request)

// AfterHandlerFunc is a function that runs after a request has been sent and
// the response is unmarshalled. It may modify the response. It must return
// the response data regardless of whether it was modified.
type AfterHandlerFunc func(string, *viper.Viper, *gentleman.Response, interface{}) interface{}

var beforeRegistry = make(map[string][]BeforeHandlerFunc)
var afterRegistry = make(map[string][]AfterHandlerFunc)

// RegisterBefore registers a pre-request handler for the given command path.
// The handler may modify the request before it gets sent over the wire.
func RegisterBefore(path string, handler BeforeHandlerFunc) {
	if _, ok := beforeRegistry[path]; !ok {
		beforeRegistry[path] = make([]BeforeHandlerFunc, 0, 1)
	}

	beforeRegistry[path] = append(beforeRegistry[path], handler)
}

// RegisterAfter registers a post-request handler for the given command path.
// The handler may modify the unmarshalled response.
func RegisterAfter(path string, handler AfterHandlerFunc) {
	if _, ok := afterRegistry[path]; !ok {
		afterRegistry[path] = make([]AfterHandlerFunc, 0, 1)
	}

	afterRegistry[path] = append(afterRegistry[path], handler)
}

func commandPath(cmd *cobra.Command) string {
	parts := make([]string, 0)
	cur := cmd
	for cur != nil {
		name := strings.Split(cur.Use, " ")[0]
		parts = append([]string{name}, parts...)
		cur = cur.Parent()
	}

	return strings.Join(parts[1:], " ")
}

// HandleBefore runs any registered pre-request handlers for the given command.
func HandleBefore(path string, params *viper.Viper, r *gentleman.Request) {
	bindRequestContext(params, r)
	if handlers, ok := beforeRegistry[path]; ok {
		for _, handler := range handlers {
			handler(path, params, r)
		}
	}
}

// HandleAfter runs any regeistered post-request handlers for the given command.
func HandleAfter(path string, params *viper.Viper, resp *gentleman.Response, data interface{}) interface{} {
	tmp := data
	if handlers, ok := afterRegistry[path]; ok {
		for _, handler := range handlers {
			tmp = handler(path, params, resp, tmp)
		}
	}

	return tmp
}

// requestContextKey holds the context an operation's request is bound to. Like
// passedFlagsKey it is spelled with underscores so no parameter can collide.
const requestContextKey = "__request_context"

// SetRequestContext binds the requests a generated operation sends with params
// to ctx, so cancelling ctx aborts the in-flight HTTP call. The MCP server uses
// it to stop the upstream request when its client cancels a tool call.
func SetRequestContext(params *viper.Viper, ctx context.Context) {
	if params == nil || ctx == nil {
		return
	}
	params.Set(requestContextKey, ctx)
}

// bindRequestContext applies the context SetRequestContext stored, if any,
// and the headers WithRequestHeaders put on it.
func bindRequestContext(params *viper.Viper, r *gentleman.Request) {
	if params == nil || r == nil || r.Context == nil {
		return
	}
	ctx, ok := params.Get(requestContextKey).(context.Context)
	if !ok {
		return
	}
	r.Context.SetCancelContext(ctx)
	for name, value := range RequestHeaders(ctx) {
		if r.Context.Request.Header.Get(name) == "" {
			r.SetHeader(name, value)
		}
	}
}

// requestHeadersKey carries headers every request made under a context sends.
type requestHeadersKey struct{}

// WithRequestHeaders returns a context whose generated requests also send
// headers, without replacing one a request already sets. The MCP server uses
// it to propagate a caller's W3C trace context to the API. Bind the context
// with SetRequestContext.
func WithRequestHeaders(ctx context.Context, headers map[string]string) context.Context {
	merged := maps.Clone(RequestHeaders(ctx))
	if merged == nil {
		merged = make(map[string]string, len(headers))
	}
	maps.Copy(merged, headers)
	return context.WithValue(ctx, requestHeadersKey{}, merged)
}

// RequestHeaders returns the headers WithRequestHeaders put on ctx.
func RequestHeaders(ctx context.Context) map[string]string {
	headers, _ := ctx.Value(requestHeadersKey{}).(map[string]string)
	return headers
}

// requestCredentialKey carries the credential of the caller a request acts for.
type requestCredentialKey struct{}

// WithRequestCredential returns a context whose generated requests send
// authorization as their Authorization header in place of the configured
// profile or environment credentials, which are then never consulted. A hosted
// MCP server uses it to call the API as each of its callers. Bind the context
// with SetRequestContext.
func WithRequestCredential(ctx context.Context, authorization string) context.Context {
	return context.WithValue(ctx, requestCredentialKey{}, authorization)
}

// RequestCredential returns the caller credential WithRequestCredential put on
// ctx, for custom code that calls an API outside the generated client.
func RequestCredential(ctx context.Context) (string, bool) {
	authorization, _ := ctx.Value(requestCredentialKey{}).(string)
	return authorization, authorization != ""
}
