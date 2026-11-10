package mcpserver

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPMiddleware runs inside CORS and outside built-in auth; the first registered runs first.
func HTTPMiddleware(wrap func(http.Handler) http.Handler) Option {
	return func(c *config) {
		c.httpMiddleware = append(c.httpMiddleware, wrap)
	}
}

// ReceivingMiddleware runs before built-in method checks on both transports, first registered first.
func ReceivingMiddleware(middleware ...mcp.Middleware) Option {
	registered := slices.Clone(middleware)
	return func(c *config) {
		c.receivingMiddleware = append(c.receivingMiddleware, registered...)
	}
}

func validateMiddleware(httpMiddleware []func(http.Handler) http.Handler, receiving []mcp.Middleware) error {
	for i, wrap := range httpMiddleware {
		if wrap == nil {
			return fmt.Errorf("HTTP middleware %d is nil", i)
		}
	}
	for i, wrap := range receiving {
		if wrap == nil {
			return fmt.Errorf("receiving middleware %d is nil", i)
		}
	}
	return nil
}

func wrapHTTPHandler(next http.Handler, middleware []func(http.Handler) http.Handler) (http.Handler, error) {
	if err := validateMiddleware(middleware, nil); err != nil {
		return nil, err
	}
	for i := len(middleware) - 1; i >= 0; i-- {
		next = middleware[i](next)
		if next == nil {
			return nil, fmt.Errorf("HTTP middleware %d returned a nil handler", i)
		}
	}
	return next, nil
}

func installReceivingMiddleware(server *mcp.Server, middleware []mcp.Middleware) error {
	if err := validateMiddleware(nil, middleware); err != nil {
		return err
	}
	if len(middleware) == 0 {
		return nil
	}
	var setupErr error
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		wrapped := next
		for i := len(middleware) - 1; i >= 0; i-- {
			wrapped = middleware[i](wrapped)
			if wrapped == nil {
				setupErr = fmt.Errorf("receiving middleware %d returned a nil handler", i)
				return next
			}
		}
		return wrapped
	})
	return setupErr
}
