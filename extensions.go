package main

// OpenAPI vendor-extension resolution.
//
// Bartolo's canonical namespace is x-bartolo-*. Each shared extension also has a
// legacy alias that keeps working with no runtime warning:
//
//	x-bartolo-ignore       <- x-cli-ignore
//	x-bartolo-name         <- x-cli-name
//	x-bartolo-group        <- x-cli-group
//	x-bartolo-description  <- x-cli-description
//	x-bartolo-hidden       <- x-cli-hidden
//	x-bartolo-aliases      <- x-cli-aliases
//	x-bartolo-waiters      <- x-cli-waiters
//	x-bartolo-list         <- x-cli-list
//	x-bartolo-list-fields  <- x-cli-list-fields
//	x-bartolo-help-section <- x-cli-help-section
//	x-bartolo-no-validate  <- x-cli-no-validate
//	x-bartolo-mcp-ignore   <- x-mcp-ignore
//
// Surface-scoped exclusions:
//
//	x-bartolo-cli-ignore   exclude from the CLI only (the op still becomes an MCP tool)
//	x-bartolo-mcp-ignore   exclude from MCP only (the op still becomes a CLI command)
//
// Precedence: when a canonical x-bartolo-* key and its legacy alias both appear on
// the same element, the canonical value wins. Surface-scoped exclusions are an
// ADDITIONAL restriction layered on top of the shared model; the shared
// x-bartolo-ignore always excludes everywhere regardless of any scoped value.

// Canonical short keys used with extValue/extString.
const (
	extKeyIgnore      = "ignore"
	extKeyName        = "name"
	extKeyGroup       = "group"
	extKeyDescription = "description"
	extKeyHidden      = "hidden"
	extKeyAliases     = "aliases"
	extKeyWaiters     = "waiters"
	extKeyList        = "list"
	extKeyListFields  = "list-fields"
	extKeyHelpSection = "help-section"
	extKeyNoValidate  = "no-validate"
)

// canonicalKeys returns the ordered lookup list for a canonical short key:
// the x-bartolo-<key> namespace first, then the legacy x-cli-<key> alias.
func canonicalKeys(key string) []string {
	return []string{"x-bartolo-" + key, "x-cli-" + key}
}

// extLookup returns the value stored under the first present (non-nil) key,
// preserving the given key order so callers control precedence.
func extLookup(ext map[string]interface{}, keys ...string) (interface{}, bool) {
	if ext == nil {
		return nil, false
	}
	for _, k := range keys {
		if v, ok := ext[k]; ok && v != nil {
			return v, true
		}
	}
	return nil, false
}

// extValue resolves an extension by its canonical short key (e.g. "name",
// "group"), preferring x-bartolo-<key> over the legacy x-cli-<key> alias.
func extValue(ext map[string]interface{}, key string) (interface{}, bool) {
	return extLookup(ext, canonicalKeys(key)...)
}

// extValueOrNil is extValue for callers that only need the value: an absent
// extension yields nil.
func extValueOrNil(ext map[string]interface{}, key string) interface{} {
	v, _ := extValue(ext, key)
	return v
}

// extString resolves a canonical short key to its decoded string value, or "".
func extString(ext map[string]interface{}, key string) string {
	if v, ok := extValue(ext, key); ok {
		return extStr(v)
	}
	return ""
}

// extPresent reports whether any of the given keys is present (non-nil). Used for
// presence-flag extensions (the ignore family) where the value itself is ignored,
// matching Bartolo's historical semantics.
func extPresent(ext map[string]interface{}, keys ...string) bool {
	_, ok := extLookup(ext, keys...)
	return ok
}

// sharedIgnored reports whether an element carries the shared ignore flag
// (x-bartolo-ignore or legacy x-cli-ignore). A shared-ignored element is dropped
// from the entire generated surface, CLI and MCP alike.
func sharedIgnored(ext map[string]interface{}) bool {
	return extPresent(ext, canonicalKeys(extKeyIgnore)...)
}

// cliIgnored reports whether an element carries x-bartolo-cli-ignore. The element
// is excluded from CLI command generation but still becomes an MCP tool. There is
// no legacy alias for this key (it is a new capability).
func cliIgnored(ext map[string]interface{}) bool {
	return extPresent(ext, "x-bartolo-cli-ignore")
}

// mcpIgnored reports whether an element carries the MCP-only ignore flag
// (x-bartolo-mcp-ignore or legacy x-mcp-ignore). The element is excluded from the
// MCP tool surface but still generates a CLI command.
func mcpIgnored(ext map[string]interface{}) bool {
	return extPresent(ext, "x-bartolo-mcp-ignore", "x-mcp-ignore")
}
