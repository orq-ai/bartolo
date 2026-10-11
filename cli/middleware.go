package cli

import (
	"context"
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

// bindRequestContext applies the context SetRequestContext stored, if any.
func bindRequestContext(params *viper.Viper, r *gentleman.Request) {
	if params == nil || r == nil || r.Context == nil {
		return
	}
	if ctx, ok := params.Get(requestContextKey).(context.Context); ok {
		r.Context.SetCancelContext(ctx)
	}
}
