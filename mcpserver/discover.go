package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// Discovery meta-tool names.
const (
	searchOperationsTool  = "search_operations"
	describeOperationTool = "describe_operation"
	callReadOperationTool = "call_read_operation"
	callOperationTool     = "call_operation"
)

const (
	defaultSearchLimit = 20
	maxSearchLimit     = 50
	// maxSummaryLength bounds an operation's description in search results.
	maxSummaryLength = 200
)

// DiscoverTools keeps the generated tools that KeepTools leaves out reachable
// instead of dropping them. Four listed meta-tools let a model find any
// operation (search_operations), read its input schema (describe_operation),
// and run it (call_read_operation for read-only operations, call_operation for
// any). A large API stays fully usable while the tool list, which the model
// reads on every turn, stays short. The --groups and --read-only filters apply
// to what the meta-tools can reach.
func DiscoverTools() Option {
	return func(c *config) { c.discover = true }
}

// operationCatalog is the set of operations the meta-tools search and call.
type operationCatalog struct {
	ops []ToolDef // sorted by name

	mu       sync.Mutex
	resolved map[string]*jsonschema.Resolved
}

func newOperationCatalog(defs []ToolDef) *operationCatalog {
	ops := slices.Clone(defs)
	sort.Slice(ops, func(i, j int) bool { return ops[i].Name < ops[j].Name })
	return &operationCatalog{ops: ops, resolved: map[string]*jsonschema.Resolved{}}
}

func (c *operationCatalog) find(name string) (ToolDef, bool) {
	i, ok := slices.BinarySearchFunc(c.ops, name, func(d ToolDef, name string) int { return strings.Compare(d.Name, name) })
	if !ok {
		return ToolDef{}, false
	}
	return c.ops[i], true
}

// schema resolves an operation's input schema on first use, so a large catalog
// costs nothing at startup.
func (c *operationCatalog) schema(op ToolDef) (*jsonschema.Resolved, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.resolved[op.Name]; ok {
		return r, nil
	}
	raw := op.InputSchema
	if len(raw) == 0 {
		raw = emptyObjectSchema
	}
	r, err := resolveInputSchema(raw)
	if err != nil {
		return nil, err
	}
	c.resolved[op.Name] = r
	return r, nil
}

func (c *operationCatalog) groups() []string {
	var groups []string
	for _, op := range c.ops {
		if op.Group != "" && !slices.Contains(groups, op.Group) {
			groups = append(groups, op.Group)
		}
	}
	sort.Strings(groups)
	return groups
}

// discoveryTools builds the meta-tools over catalog.
func discoveryTools(defs []ToolDef) []ToolDef {
	c := newOperationCatalog(defs)
	scope := fmt.Sprintf("this API's %d operations", len(c.ops))
	if groups := c.groups(); len(groups) > 0 {
		scope += " (groups: " + strings.Join(groups, ", ") + ")"
	}
	return []ToolDef{
		{
			Name:  searchOperationsTool,
			Title: "Search API operations",
			Description: "Search " + scope + ". Only the most common operations are listed as tools; " +
				"the rest are found here. Matches query words against operation names, titles, and " +
				"descriptions. With no query or group, lists the groups. Then call describe_operation " +
				"for an operation's arguments.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` +
				`"query":{"type":"string","description":"Words to match, e.g. \"list datasets\"."},` +
				`"group":{"type":"string","description":"Only operations in this group."},` +
				`"limit":{"type":"integer","minimum":1,"maximum":50,"default":20}},` +
				`"additionalProperties":false}`),
			ReadOnly:   true,
			Idempotent: true,
			Handler:    c.search,
		},
		{
			Name:  describeOperationTool,
			Title: "Describe an API operation",
			Description: "Return an operation's description, annotations, and the JSON Schema of the " +
				"arguments to pass to call_read_operation or call_operation.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` +
				`"name":{"type":"string","description":"Operation name from search_operations."}},` +
				`"required":["name"],"additionalProperties":false}`),
			ReadOnly:   true,
			Idempotent: true,
			Handler:    c.describe,
		},
		{
			Name:  callReadOperationTool,
			Title: "Call a read-only API operation",
			Description: "Run a read-only operation (read_only: true in search_operations) with " +
				"arguments matching its describe_operation schema. Refuses any other operation.",
			InputSchema: callOperationSchema,
			ReadOnly:    true,
			Handler:     c.callReadOnly,
		},
		{
			Name:  callOperationTool,
			Title: "Call an API operation",
			Description: "Run any operation with arguments matching its describe_operation schema. " +
				"Prefer call_read_operation for read-only operations.",
			InputSchema: callOperationSchema,
			Destructive: true,
			Handler:     c.call,
		},
	}
}

var callOperationSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"name":{"type":"string","description":"Operation name from search_operations."},` +
	`"arguments":{"type":"object","description":"Arguments matching the operation's input schema."}},` +
	`"required":["name"],"additionalProperties":false}`)

// operationSummary is one search result.
type operationSummary struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Group       string `json:"group,omitempty"`
	Description string `json:"description,omitempty"`
	ReadOnly    bool   `json:"read_only"`
	Destructive bool   `json:"destructive"`
}

func summarize(op ToolDef) operationSummary {
	desc := strings.TrimSpace(op.Description)
	if i := strings.Index(desc, "\n"); i >= 0 {
		desc = desc[:i]
	}
	if r := []rune(desc); len(r) > maxSummaryLength {
		desc = string(r[:maxSummaryLength]) + "…"
	}
	return operationSummary{
		Name:        op.Name,
		Title:       op.Title,
		Group:       op.Group,
		Description: desc,
		ReadOnly:    op.ReadOnly,
		Destructive: op.Destructive,
	}
}

func (c *operationCatalog) search(_ context.Context, args map[string]any) (any, error) {
	query, _ := args["query"].(string)
	group, _ := args["group"].(string)
	limit := defaultSearchLimit
	if n, ok := args["limit"].(float64); ok {
		limit = min(max(int(n), 1), maxSearchLimit)
	}

	if strings.TrimSpace(query) == "" && group == "" {
		return c.groupCounts(), nil
	}

	matches := c.match(strings.Fields(strings.ToLower(query)), group)
	total := len(matches)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	results := make([]operationSummary, len(matches))
	for i, op := range matches {
		results[i] = summarize(op)
	}
	return map[string]any{"operations": results, "total": total}, nil
}

func (c *operationCatalog) groupCounts() map[string]any {
	counts := map[string]int{}
	for _, op := range c.ops {
		counts[op.Group]++
	}
	groups := make([]map[string]any, 0, len(counts))
	for _, name := range c.groups() {
		groups = append(groups, map[string]any{"group": name, "operations": counts[name]})
	}
	return map[string]any{
		"groups": groups,
		"total":  len(c.ops),
		"hint":   "Search with a query or a group to list operations.",
	}
}

// match ranks operations by how many query words they contain, a word in the
// name counting double; ties keep name order.
func (c *operationCatalog) match(words []string, group string) []ToolDef {
	type scored struct {
		op    ToolDef
		score int
	}
	var hits []scored
	for _, op := range c.ops {
		if group != "" && op.Group != group {
			continue
		}
		score := matchScore(op, words)
		if len(words) == 0 || score > 0 {
			hits = append(hits, scored{op, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]ToolDef, len(hits))
	for i, h := range hits {
		out[i] = h.op
	}
	return out
}

func matchScore(op ToolDef, words []string) int {
	name := strings.ToLower(op.Name)
	text := strings.ToLower(op.Title + " " + op.Description + " " + op.Group)
	score := 0
	for _, w := range words {
		switch {
		case strings.Contains(name, w):
			score += 2
		case strings.Contains(text, w):
			score++
		}
	}
	return score
}

func (c *operationCatalog) describe(_ context.Context, args map[string]any) (any, error) {
	op, err := c.lookup(args)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"name":        op.Name,
		"title":       op.Title,
		"group":       op.Group,
		"description": op.Description,
		"read_only":   op.ReadOnly,
		"destructive": op.Destructive,
		"idempotent":  op.Idempotent,
		"input_schema": func() json.RawMessage {
			if len(op.InputSchema) == 0 {
				return emptyObjectSchema
			}
			return op.InputSchema
		}(),
	}
	if len(op.OutputSchema) > 0 {
		out["output_schema"] = op.OutputSchema
	}
	return out, nil
}

func (c *operationCatalog) callReadOnly(ctx context.Context, args map[string]any) (any, error) {
	op, err := c.lookup(args)
	if err != nil {
		return nil, err
	}
	if !op.ReadOnly {
		return nil, fmt.Errorf("%s is not read-only; use %s", op.Name, callOperationTool)
	}
	return c.invoke(ctx, op, args)
}

func (c *operationCatalog) call(ctx context.Context, args map[string]any) (any, error) {
	op, err := c.lookup(args)
	if err != nil {
		return nil, err
	}
	return c.invoke(ctx, op, args)
}

// invoke validates the arguments against the operation's own schema, as a
// listed tool would, before running it.
func (c *operationCatalog) invoke(ctx context.Context, op ToolDef, args map[string]any) (any, error) {
	opArgs, _ := args["arguments"].(map[string]any)
	if opArgs == nil {
		opArgs = map[string]any{}
	}
	schema, err := c.schema(op)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op.Name, err)
	}
	if err := schema.Validate(opArgs); err != nil {
		return nil, fmt.Errorf("invalid arguments for %s (see %s): %v", op.Name, describeOperationTool, err)
	}
	return op.Handler(ctx, opArgs)
}

func (c *operationCatalog) lookup(args map[string]any) (ToolDef, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return ToolDef{}, errors.New("name is required")
	}
	op, ok := c.find(name)
	if !ok {
		return ToolDef{}, fmt.Errorf("no operation named %q; use %s to find one", name, searchOperationsTool)
	}
	return op, nil
}
