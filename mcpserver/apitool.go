package mcpserver

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/orq-ai/bartolo/cli"
	"github.com/spf13/viper"
)

// APIRequest is one tool call's validated arguments, arranged the way a
// generated client func takes them: positional strings for required
// parameters, a params viper for optional ones, and the JSON request body.
type APIRequest struct {
	// Params carries the optional parameters under their CLI flag names, marked
	// as explicitly passed so zero values (false, 0, "") are still sent, and
	// bound to the tool call's context so cancelling the call aborts the
	// upstream request.
	Params *viper.Viper
	// Body is the JSON request body, or "" when the call supplies none.
	Body string

	positional map[paramKey]string
}

// paramKey identifies a parameter by location and wire name.
type paramKey struct{ in, name string }

// Arg returns the positional argument for the required parameter name in in
// (path, query, or header), or "" when the call did not supply it.
func (r *APIRequest) Arg(in, name string) string {
	return r.positional[paramKey{in, name}]
}

// APITool completes a generated tool definition with a Handler that routes the
// validated arguments per def.Routes and passes them to call, which invokes
// the generated client func. When def.WrapResult is set the decoded response
// is returned as {"result": value}, matching the generated output schema.
func APITool(def ToolDef, call func(*APIRequest) (any, error)) ToolDef {
	routes, wrap := def.Routes, def.WrapResult
	def.Handler = func(ctx context.Context, args map[string]any) (any, error) {
		req, err := newAPIRequest(ctx, args, routes)
		if err != nil {
			return nil, err
		}
		out, err := call(req)
		if err != nil || !wrap {
			return out, err
		}
		return map[string]any{"result": out}, nil
	}
	return def
}

func newAPIRequest(ctx context.Context, args map[string]any, routes []ArgRoute) (*APIRequest, error) {
	body, err := encodeBody(args, routes)
	if err != nil {
		return nil, err
	}
	req := &APIRequest{Params: viper.New(), Body: body, positional: map[paramKey]string{}}
	cli.SetRequestContext(req.Params, ctx)

	passed := make([]string, 0, len(routes))
	for _, route := range routes {
		value, ok := args[route.Prop]
		switch {
		case !ok || route.In == "body":
		case route.Required:
			req.positional[paramKey{route.In, route.WireName}] = CoerceString(paramValue(value))
		case route.Flag != "":
			req.Params.Set(route.Flag, paramValue(value))
			passed = append(passed, route.Flag)
		}
	}
	cli.MarkPassedParams(req.Params, passed...)
	return req, nil
}

// encodeBody renders the JSON request body from the body routes: a whole-body
// route's value as is, otherwise an object of the body properties given, or ""
// when the call supplies none.
func encodeBody(args map[string]any, routes []ArgRoute) (string, error) {
	fields := map[string]any{}
	for _, route := range routes {
		value, ok := args[route.Prop]
		if !ok || route.In != "body" {
			continue
		}
		if route.WireName == "" {
			return marshalString(value)
		}
		fields[route.WireName] = value
	}
	if len(fields) == 0 {
		return "", nil
	}
	return marshalString(fields)
}

func marshalString(value any) (string, error) {
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

// paramValue converts a JSON argument to what the CLI would have parsed from
// the matching flag. An array becomes the comma-separated list a flag takes,
// an object its JSON encoding; scalars pass through for viper's typed getters.
func paramValue(value any) any {
	switch v := value.(type) {
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			parts[i] = CoerceString(item)
		}
		return strings.Join(parts, ",")
	case map[string]any:
		return CoerceString(v)
	}
	return value
}
