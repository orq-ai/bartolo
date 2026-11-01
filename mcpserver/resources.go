package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/orq-ai/bartolo/cli"
)

type resourceDefinition struct {
	metadata *mcp.Resource
	handler  mcp.ResourceHandler
	err      error
}

// AddResource registers a resource on the MCP server using the transport's authentication.
func AddResource(resource *mcp.Resource, handler mcp.ResourceHandler) Option {
	metadata, err := cloneResource(resource)
	return func(c *config) {
		c.resources = append(c.resources, resourceDefinition{
			metadata: metadata,
			handler:  handler,
			err:      err,
		})
	}
}

func cloneResource(resource *mcp.Resource) (*mcp.Resource, error) {
	if resource == nil {
		return nil, errors.New("resource metadata is nil")
	}
	encoded, err := json.Marshal(resource)
	if err != nil {
		return nil, fmt.Errorf("resource metadata: %w", err)
	}
	var cloned mcp.Resource
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return nil, fmt.Errorf("resource metadata: %w", err)
	}
	return &cloned, nil
}

func validateResources(resources []resourceDefinition) error {
	seen := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		if resource.err != nil {
			return resource.err
		}
		if resource.metadata == nil {
			return errors.New("resource metadata is nil")
		}
		if resource.handler == nil {
			return fmt.Errorf("resource %q has no handler", resource.metadata.URI)
		}
		if strings.TrimSpace(resource.metadata.Name) == "" {
			return fmt.Errorf("resource %q has no name", resource.metadata.URI)
		}
		uri, err := url.Parse(resource.metadata.URI)
		if err != nil || !uri.IsAbs() {
			return fmt.Errorf("resource URI %q must be absolute", resource.metadata.URI)
		}
		if _, exists := seen[resource.metadata.URI]; exists {
			return fmt.Errorf("duplicate resource URI %q", resource.metadata.URI)
		}
		seen[resource.metadata.URI] = struct{}{}
	}
	return nil
}

func resourceHandler(handler mcp.ResourceHandler, info serverInfo) mcp.ResourceHandler {
	return func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		if info.forwardCredentials {
			authorization := ""
			if request != nil && request.Extra != nil {
				authorization = request.Extra.Header.Get("Authorization")
			}
			ctx = cli.WithRequestCredential(ctx, authorization)
		}
		return handler(ctx, request)
	}
}
