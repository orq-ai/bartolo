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

// PatchTool mutates a generated tool by name. The name must exist after
// RemoveTools; otherwise serve/list fails. Tools from AddTools are the caller's
// own and are not patched.
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
// WrapAll, and validates the result. It does not apply --tools/--groups/
// --read-only filters.
func applyCustomizations(defs []ToolDef, cfg *config) ([]ToolDef, error) {
	out := removeTools(defs, cfg.remove)
	out, err := patchTools(out, cfg.patches)
	if err != nil {
		return nil, err
	}
	out, err = addTools(out, cfg.extra)
	if err != nil {
		return nil, err
	}
	if err := validateTools(out); err != nil {
		return nil, err
	}
	return wrapTools(out, cfg.wraps), nil
}

// removeTools returns a copy of defs without the named tools.
func removeTools(defs []ToolDef, names []string) []ToolDef {
	return slices.DeleteFunc(slices.Clone(defs), func(d ToolDef) bool {
		return slices.Contains(names, d.Name)
	})
}

// patchTools applies each patch to the tool it names, in order. A patch may
// rename its tool, but not onto another tool's name.
func patchTools(defs []ToolDef, patches []toolPatch) ([]ToolDef, error) {
	for _, p := range patches {
		i := indexTool(defs, p.name)
		if i < 0 {
			return nil, fmt.Errorf("patch tool %q: no such tool", p.name)
		}
		tool := defs[i]
		p.fn(&tool)
		if j := indexTool(defs, tool.Name); j >= 0 && j != i {
			return nil, fmt.Errorf("patch tool %q: renamed to %q which already exists", p.name, tool.Name)
		}
		defs[i] = tool
	}
	return defs, nil
}

// addTools appends extra, rejecting any name already in use.
func addTools(defs, extra []ToolDef) ([]ToolDef, error) {
	for _, tool := range extra {
		if indexTool(defs, tool.Name) >= 0 {
			return nil, fmt.Errorf("add tool %q: name already exists", tool.Name)
		}
		defs = append(defs, tool)
	}
	return defs, nil
}

// wrapTools applies every WrapAll wrapper to every handler, in registration
// order (the last wrapper registered runs outermost).
func wrapTools(defs []ToolDef, wraps []func(ToolHandler) ToolHandler) []ToolDef {
	for i := range defs {
		for _, wrap := range wraps {
			defs[i].Handler = wrap(defs[i].Handler)
		}
	}
	return defs
}

// validateTools rejects a tool the server could not register: a name outside
// the MCP tool-name grammar, or no handler.
func validateTools(defs []ToolDef) error {
	for _, d := range defs {
		if !validToolName.MatchString(d.Name) {
			return fmt.Errorf("tool %q: name must match %s", d.Name, validToolName)
		}
		if d.Handler == nil {
			return fmt.Errorf("tool %q: no handler", d.Name)
		}
	}
	return nil
}

func indexTool(defs []ToolDef, name string) int {
	return slices.IndexFunc(defs, func(d ToolDef) bool { return d.Name == name })
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
