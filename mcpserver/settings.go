package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/orq-ai/bartolo/cli"
)

// settings are the protocol-level choices a generated server makes. The
// generator sets them from the `mcp` block of .bartolo.json; cli/custom can
// override any of them through Use.
type settings struct {
	metadata         Metadata
	listTTL          *time.Duration
	protocolVersions []string
	traceContext     *bool
	serverCard       *ServerCard
}

// Metadata describes the server to clients beyond its name and version
// (SEP-973). Every field is optional.
type Metadata struct {
	// Title is a human-readable display name.
	Title string
	// Description says what the server is for.
	Description string
	// WebsiteURL links to the server's documentation or homepage.
	WebsiteURL string
	// Icons let clients show the server in a user interface.
	Icons []Icon
}

// Icon is one image a client may show for the server (SEP-973).
type Icon struct {
	// Src is an https: or data: URI.
	Src string
	// MIMEType overrides a missing or generic served type, e.g. "image/png".
	MIMEType string
	// Sizes lists the sizes the image suits, e.g. "48x48", or "any" for SVG.
	Sizes []string
}

// SetMetadata sets the server's title, description, website, and icons.
func SetMetadata(m Metadata) Option {
	return func(c *config) { c.settings.metadata = m }
}

// SetListTTL sets how long clients may cache server/discover and tools/list
// results (SEP-2549). Zero means "always refetch". The default is five
// minutes: the tool set is fixed for the life of the process.
func SetListTTL(ttl time.Duration) Option {
	return func(c *config) { c.settings.listTTL = &ttl }
}

// SetProtocolVersions narrows the protocol versions the server accepts, e.g.
// to refuse clients older than 2026-07-28. It cannot add a version the SDK does
// not implement; naming one fails serve.
func SetProtocolVersions(versions ...string) Option {
	return func(c *config) { c.settings.protocolVersions = versions }
}

// SetTraceContext turns W3C trace-context propagation on or off (SEP-414). On by
// default: a tool call's traceparent, tracestate, and baggage — from the
// request's _meta, else from its HTTP headers — are sent on the API requests it
// makes, so one trace spans the client, this server, and the API.
func SetTraceContext(enabled bool) Option {
	return func(c *config) { c.settings.traceContext = &enabled }
}

// ServerCard publishes an MCP Server Card (SEP-2127): static, public metadata
// that lets clients and registries discover the server and how to connect
// before opening a session. It is served at GET <Endpoint>/server-card when the
// server runs Streamable HTTP.
type ServerCard struct {
	// Name is the server's reverse-DNS name, "namespace/name", e.g. "ai.orq/mcp".
	Name string
	// Endpoint is the URL path clients connect to. Default "/mcp".
	Endpoint string
	// PublicURL is the endpoint's full public URL. When empty it is built from
	// each request, honoring X-Forwarded-Proto and X-Forwarded-Host.
	PublicURL string
	// Repository is the URL of the server's source code.
	Repository string
}

// PublishServerCard serves a Server Card describing the server. Its
// description comes from SetMetadata, which the card requires.
func PublishServerCard(card ServerCard) Option {
	return func(c *config) { c.settings.serverCard = &card }
}

const defaultListTTL = 5 * time.Minute

func (s settings) ttl() time.Duration {
	if s.listTTL == nil {
		return defaultListTTL
	}
	return *s.listTTL
}

func (s settings) traceContextEnabled() bool {
	return s.traceContext == nil || *s.traceContext
}

func (s settings) implementation(name, version string) *mcp.Implementation {
	impl := &mcp.Implementation{
		Name:        name,
		Version:     version,
		Title:       s.metadata.Title,
		Description: s.metadata.Description,
		WebsiteURL:  s.metadata.WebsiteURL,
	}
	for _, icon := range s.metadata.Icons {
		impl.Icons = append(impl.Icons, mcp.Icon{Source: icon.Src, MIMEType: icon.MIMEType, Sizes: icon.Sizes})
	}
	return impl
}

// CheckOptions reports options a server could not honor, such as an
// unsupported protocol version or an incomplete Server Card, without building
// one. The generator calls it so a bad .bartolo.json fails generate rather than
// the server at startup.
func CheckOptions(opts ...Option) error {
	cfg := &config{}
	for _, o := range opts {
		o(cfg)
	}
	return cfg.settings.validate()
}

// validate reports settings the server could not honor, before it starts.
func (s settings) validate() error {
	if err := s.metadata.validate(); err != nil {
		return err
	}
	supported := mcp.SupportedProtocolVersions()
	for _, v := range s.protocolVersions {
		if !slices.Contains(supported, v) {
			return fmt.Errorf("protocol version %q is not supported (supported: %s)", v, strings.Join(supported, ", "))
		}
	}
	if s.listTTL != nil && *s.listTTL < 0 {
		return errors.New("the list TTL cannot be negative")
	}
	if s.serverCard != nil {
		return s.validateServerCard()
	}
	return nil
}

func (m Metadata) validate() error {
	if m.WebsiteURL != "" && !isHTTPURL(m.WebsiteURL) {
		return fmt.Errorf("website URL %q must be an absolute http(s) URL", m.WebsiteURL)
	}
	for _, icon := range m.Icons {
		if !isHTTPURL(icon.Src) && !strings.HasPrefix(icon.Src, "data:image/") {
			return fmt.Errorf("icon %q must be an https: URL or a data:image/ URI", icon.Src)
		}
	}
	return nil
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

var serverCardName = regexp.MustCompile(`^[a-zA-Z0-9.-]+/[a-zA-Z0-9._-]+$`)

func (s settings) validateServerCard() error {
	card := s.serverCard
	switch {
	case len(card.Name) < 3 || len(card.Name) > 200 || !serverCardName.MatchString(card.Name):
		return fmt.Errorf(`server card name %q must be "namespace/name" (reverse-DNS), 3-200 characters`, card.Name)
	case s.metadata.Description == "" || len([]rune(s.metadata.Description)) > 100:
		return errors.New("a server card needs a description of 1-100 characters")
	case card.endpoint()[0] != '/':
		return fmt.Errorf("server card endpoint %q must be a path starting with /", card.Endpoint)
	}
	if card.PublicURL != "" && !isHTTPURL(card.PublicURL) {
		return fmt.Errorf("server card public URL %q must be an absolute http(s) URL", card.PublicURL)
	}
	if card.Repository != "" && !isHTTPURL(card.Repository) {
		return fmt.Errorf("server card repository %q must be an absolute http(s) URL", card.Repository)
	}
	return nil
}

func (c ServerCard) endpoint() string {
	if c.Endpoint == "" {
		return "/mcp"
	}
	return c.Endpoint
}

// undeclaredMethods are requests for features a generated server never
// declares. The SDK answers some of them anyway, which tells a client the
// server supports a feature its discover/initialize result says it does not
// (SEP-2575). Prompts and resources are declared instead (with none to list),
// so list requests and their caching hints (SEP-2549) behave as the spec
// expects; logging is deprecated (SEP-2577) and never emitted.
var undeclaredMethods = map[string]bool{
	"resources/subscribe":   true,
	"resources/unsubscribe": true,
	"completion/complete":   true,
	"logging/setLevel":      true,
}

// capabilities are what a generated server declares: tools, plus prompts and
// resources it has none of, which it answers with empty lists.
func capabilities() *mcp.ServerCapabilities {
	return &mcp.ServerCapabilities{
		Tools:     &mcp.ToolCapabilities{},
		Prompts:   &mcp.PromptCapabilities{},
		Resources: &mcp.ResourceCapabilities{},
	}
}

// rejectUndeclaredMethods answers undeclaredMethods with Method not found.
func rejectUndeclaredMethods(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if undeclaredMethods[method] {
			return nil, &jsonrpc.Error{
				Code:    jsonrpc.CodeMethodNotFound,
				Message: fmt.Sprintf("%s is not supported: this server offers tools only", method),
			}
		}
		return next(ctx, method, req)
	}
}

// traceKeys are the W3C trace-context fields SEP-414 carries in _meta.
var traceKeys = []string{"traceparent", "tracestate", "baggage"}

var traceparentFormat = regexp.MustCompile(`^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

// maxTraceHeader bounds tracestate and baggage, which W3C keeps far smaller.
const maxTraceHeader = 8192

// traceContext returns the request's trace context: the _meta keys when it has
// a traceparent there, else the W3C headers of the HTTP request it arrived on.
// A malformed traceparent drops the whole context rather than starting a
// broken trace.
func traceContext(req *mcp.CallToolRequest) map[string]string {
	fromMeta := map[string]string{}
	if req.Params != nil {
		for _, key := range traceKeys {
			if v, ok := req.Params.GetMeta()[key].(string); ok {
				fromMeta[key] = v
			}
		}
	}
	found := fromMeta
	if found["traceparent"] == "" && req.Extra != nil {
		found = map[string]string{}
		for _, key := range traceKeys {
			found[key] = req.Extra.Header.Get(key)
		}
	}
	if !traceparentFormat.MatchString(found["traceparent"]) {
		return nil
	}
	out := map[string]string{}
	for key, value := range found {
		if value != "" && len(value) <= maxTraceHeader && !strings.ContainsAny(value, "\r\n") {
			out[key] = value
		}
	}
	return out
}

// withTraceContext puts the request's trace context on ctx for the API
// requests a tool call makes.
func withTraceContext(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	if trace := traceContext(req); trace != nil {
		return cli.WithRequestHeaders(ctx, trace)
	}
	return ctx
}

const serverCardSchema = "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json"

// serverCardHandler serves the Server Card. Cards are public by design: no
// auth, and any origin may read them.
func serverCardHandler(info serverInfo, authMode string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := json.Marshal(info.serverCard(cardEndpointURL(info.settings.serverCard, r), authMode))
		if err != nil {
			http.Error(w, "server card unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/mcp-server-card+json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		// The remote URL can come from the request's host headers; never let a
		// shared cache serve one host's card for another.
		w.Header().Set("Vary", "Host, X-Forwarded-Host, X-Forwarded-Proto")
		_, _ = w.Write(data)
	})
}

// cardEndpointURL is the public URL of the MCP endpoint the card advertises.
func cardEndpointURL(card *ServerCard, r *http.Request) string {
	if card.PublicURL != "" {
		return card.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := firstHeaderValue(r.Header.Get("X-Forwarded-Proto")); proto == "https" || proto == "http" {
		scheme = proto
	}
	host := r.Host
	if forwarded := firstHeaderValue(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host + card.endpoint()
}

func firstHeaderValue(v string) string {
	first, _, _ := strings.Cut(v, ",")
	return strings.TrimSpace(first)
}

// serverCard builds the card document (schema v1).
func (info serverInfo) serverCard(endpointURL, authMode string) map[string]any {
	s := info.settings
	versions := s.protocolVersions
	if len(versions) == 0 {
		versions = mcp.SupportedProtocolVersions()
	}
	remote := map[string]any{
		"type":                      "streamable-http",
		"url":                       endpointURL,
		"supportedProtocolVersions": versions,
	}
	if authMode == authModeForward || authMode == authModeBearer {
		remote["headers"] = []map[string]any{{
			"name":        "Authorization",
			"description": "Bearer credential for this server.",
			"isRequired":  true,
			"isSecret":    true,
			"value":       "Bearer {token}",
			"variables": map[string]any{
				"token": map[string]any{"description": "Your API key.", "isRequired": true, "isSecret": true},
			},
		}}
	}

	card := map[string]any{
		"$schema":     serverCardSchema,
		"name":        s.serverCard.Name,
		"version":     info.version,
		"description": s.metadata.Description,
		"remotes":     []any{remote},
	}
	if s.metadata.Title != "" {
		card["title"] = s.metadata.Title
	}
	if s.metadata.WebsiteURL != "" {
		card["websiteUrl"] = s.metadata.WebsiteURL
	}
	if len(s.metadata.Icons) > 0 {
		card["icons"] = s.implementation(info.name, info.version).Icons
	}
	if repo := s.serverCard.Repository; repo != "" {
		source := "git"
		if u, err := url.Parse(repo); err == nil && strings.HasSuffix(u.Host, "github.com") {
			source = "github"
		}
		card["repository"] = map[string]any{"url": repo, "source": source}
	}
	return card
}
