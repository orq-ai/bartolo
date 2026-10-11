package mcpserver

import (
	"fmt"
	"slices"
	"sync"
)

// toolPatch is one PatchTool registration.
type toolPatch struct {
	name string
	fn   func(*ToolDef)
}

var (
	useMu      sync.Mutex
	globalOpts []Option
)

// Use registers MCP customizations from user-owned code (typically
// cli/custom.Register). Options are applied lazily when the mcp command runs or
// when `mcp list` prints the manifest, so Use may be called after Command has
// already constructed the cobra command (the generated register wiring).
//
// Successive Use calls append. Tests should call ResetForTest in t.Cleanup.
func Use(opts ...Option) {
	if len(opts) == 0 {
		return
	}
	useMu.Lock()
	defer useMu.Unlock()
	globalOpts = append(globalOpts, opts...)
}

// ResetForTest clears customizations registered via Use. It is intended for
// tests; production code should not need it.
func ResetForTest() {
	useMu.Lock()
	defer useMu.Unlock()
	globalOpts = nil
}

// AddTools appends extra tools to the generated set. Names that collide with a
// generated tool (after RemoveTools) fail at serve/list time.
func AddTools(tools ...ToolDef) Option {
	return func(c *config) {
		c.extra = append(c.extra, tools...)
	}
}

// RemoveTools drops generated tools by exact name. Missing names are ignored.
func RemoveTools(names ...string) Option {
	return func(c *config) {
		c.remove = append(c.remove, names...)
	}
}

// PatchTool mutates a generated (or previously added) tool by name. The name
// must exist after RemoveTools; otherwise serve/list fails.
func PatchTool(name string, fn func(*ToolDef)) Option {
	return func(c *config) {
		c.patches = append(c.patches, toolPatch{name: name, fn: fn})
	}
}

// WrapAll wraps every remaining tool handler after add/remove/patch. Wrappers
// run after JSON Schema validation.
func WrapAll(wrap func(ToolHandler) ToolHandler) Option {
	return func(c *config) {
		c.wraps = append(c.wraps, wrap)
	}
}

// snapshotUseOpts returns a copy of options registered via Use.
func snapshotUseOpts() []Option {
	useMu.Lock()
	defer useMu.Unlock()
	return slices.Clone(globalOpts)
}

// mergeConfig copies cmdCfg and applies Use() options on top.
func mergeConfig(cmdCfg *config) *config {
	runtime := &config{
		instructions: cmdCfg.instructions,
		envPrefix:    cmdCfg.envPrefix,
		extra:        slices.Clone(cmdCfg.extra),
		remove:       slices.Clone(cmdCfg.remove),
		patches:      slices.Clone(cmdCfg.patches),
		wraps:        slices.Clone(cmdCfg.wraps),
	}
	for _, o := range snapshotUseOpts() {
		o(runtime)
	}
	return runtime
}

// applyCustomizations runs RemoveTools, then PatchTool, then AddTools, then
// WrapAll. It does not apply --tools/--groups/--read-only filters.
func applyCustomizations(defs []ToolDef, cfg *config) ([]ToolDef, error) {
	removed := make(map[string]bool, len(cfg.remove))
	for _, name := range cfg.remove {
		removed[name] = true
	}

	out := make([]ToolDef, 0, len(defs)+len(cfg.extra))
	for _, d := range defs {
		if removed[d.Name] {
			continue
		}
		out = append(out, d)
	}

	index := make(map[string]int, len(out)+len(cfg.extra))
	for i, d := range out {
		index[d.Name] = i
	}

	for _, p := range cfg.patches {
		i, ok := index[p.name]
		if !ok {
			return nil, fmt.Errorf("patch tool %q: no such tool", p.name)
		}
		tool := out[i]
		p.fn(&tool)
		if tool.Name != p.name {
			delete(index, p.name)
			if j, exists := index[tool.Name]; exists && j != i {
				return nil, fmt.Errorf("patch tool %q: renamed to %q which already exists", p.name, tool.Name)
			}
			index[tool.Name] = i
		}
		out[i] = tool
	}

	for _, extra := range cfg.extra {
		if _, exists := index[extra.Name]; exists {
			return nil, fmt.Errorf("add tool %q: name already exists", extra.Name)
		}
		index[extra.Name] = len(out)
		out = append(out, extra)
	}

	if len(cfg.wraps) > 0 {
		for i := range out {
			h := out[i].Handler
			for _, wrap := range cfg.wraps {
				h = wrap(h)
			}
			out[i].Handler = h
		}
	}
	return out, nil
}

// prepareTools materializes generated tools, applies Use() customizations, then
// runtime filters. The returned config includes any Use() overrides of
// instructions / env prefix.
func prepareTools(tools func() []ToolDef, cmdCfg *config, f filterOptions) (*config, []ToolDef, error) {
	runtime := mergeConfig(cmdCfg)
	defs, err := applyCustomizations(tools(), runtime)
	if err != nil {
		return nil, nil, err
	}
	return runtime, filterTools(defs, f), nil
}
