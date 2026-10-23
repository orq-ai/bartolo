package main

import (
	"cmp"
	"embed"
	"encoding/json"
	"fmt"
	"go/format"
	"io/ioutil"
	"log"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"unicode"

	survey "github.com/AlecAivazis/survey/v2"
	surveycore "github.com/AlecAivazis/survey/v2/core"
	surveyterminal "github.com/AlecAivazis/survey/v2/terminal"
	"github.com/getkin/kin-openapi/openapi3"
	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/orq-ai/bartolo/shorthand"
	"github.com/spf13/cobra"
	yamlv3 "gopkg.in/yaml.v3"
)

//go:embed templates/*
var templateFS embed.FS

const projectConfigFilename = ".bartolo.json"

// bartoloVersion is the module version this binary was installed with, not a
// constant anyone has to remember to bump. `go install ...@vX.Y.Z` records the
// version in the build info, so the git tag is the single source of truth and
// the reported version cannot drift away from it the way v0.4.4 did (tagged
// v0.4.4, reported 0.4.3).
var bartoloVersion = resolveVersion()

// devel is the sentinel for a binary with no recorded module version: `go build`
// has none to record, so there is no honest number to print. resolveVersionFrom
// appends the VCS revision when the build carried one.
const devel = "devel"

func resolveVersion() string {
	return resolveVersionFrom(debug.ReadBuildInfo())
}

// resolveVersionFrom is resolveVersion with the one untestable call lifted out,
// so the branching below is covered by tests rather than by the build method
// that happens to produce the binary running them.
func resolveVersionFrom(info *debug.BuildInfo, ok bool) string {
	if !ok {
		return devel
	}
	version := normalizeVersion(info.Main.Version)
	if version != devel {
		return version
	}
	// A dev build has no version, but it does have a commit, and this string is
	// written into every generated .bartolo.json. "devel" alone cannot tell you
	// which checkout produced a project; "devel+1a2b3c4" can.
	return devel + buildRevision(info.Settings)
}

// normalizeVersion turns a module version recorded in the build info into the
// string bartolo reports. Go writes "(devel)" for a plain `go build`, and an
// empty string when the binary was not built as a module at all.
func normalizeVersion(raw string) string {
	if raw == "" || raw == "(devel)" {
		return devel
	}
	return strings.TrimPrefix(raw, "v")
}

// buildRevision returns "+<short sha>", or "" when the build carried no VCS
// stamp — `go build` outside a repository, or with -buildvcs=false.
func buildRevision(settings []debug.BuildSetting) string {
	for _, setting := range settings {
		if setting.Key != "vcs.revision" || setting.Value == "" {
			continue
		}
		revision := setting.Value
		if len(revision) > 7 {
			revision = revision[:7]
		}
		return "+" + revision
	}
	return ""
}

// OpenAPI vendor extensions are resolved through the helpers in extensions.go,
// which walk the canonical x-bartolo-* namespace and its legacy x-cli-*/x-mcp-*
// aliases. See extensions.go for the full key map and precedence rules.

// Param describes an OpenAPI parameter (path, query, header, etc)
type Param struct {
	Name        string
	CLIName     string
	GoName      string
	Description string
	In          string
	Required    bool
	Type        string
	TypeNil     string
	Style       string
	Explode     bool
	Enum        []string
	Format      string
}

// Operation describes an OpenAPI operation (GET/POST/PUT/PATCH/DELETE)
type Operation struct {
	HandlerName string
	GoName      string
	// PublicPrefix is the API-level Go prefix on this operation's generated
	// function. It lives here so shared template blocks can name the function
	// without a file-scoped variable, which a `define` cannot see.
	PublicPrefix   string
	Use            string
	Aliases        []string
	Short          string
	Long           string
	Method         string
	CanHaveBody    bool
	ReturnType     string
	Path           string
	AllParams      []*Param
	RequiredParams []*Param
	OptionalParams []*Param
	MediaType      string
	Examples       []string
	BodyExample    string
	BodyFields     []*BodyField
	Hidden         bool
	NeedsResponse  bool
	IsList         bool
	ListFields     []string
	Waiters        []*WaiterParams
	Group          *CommandGroup
	HelpSection    string
	CommandPath    string
	LeafName       string
	MCP            *MCPTool
	// CLIExcluded marks an operation that must not generate a CLI command
	// (x-bartolo-cli-ignore) while still entering the IR so it can be exposed
	// as an MCP tool and have its generated client func emitted.
	CLIExcluded bool
}

// BodyField describes a generated request-body flag for a simple top-level
// request property.
type BodyField struct {
	Name        string
	CLIName     string
	GoName      string
	Description string
	Type        string
	Enum        []string
}

// CommandGroup describes a high-level product noun such as `files`.
type CommandGroup struct {
	Name        string
	CLIName     string
	GoName      string
	Short       string
	Long        string
	Aliases     []string
	Hidden      bool
	HelpSection string
	Operations  []*Operation
	Description string
}

// cliVisibleOperations returns the operations that generate a CLI command,
// dropping any marked CLIExcluded (x-bartolo-cli-ignore). CLI-excluded ops still
// exist in the IR so they can be exposed as MCP tools.
func cliVisibleOperations(ops []*Operation) []*Operation {
	out := make([]*Operation, 0, len(ops))
	for _, op := range ops {
		if op.CLIExcluded {
			continue
		}
		out = append(out, op)
	}
	return out
}

// HasCLICommands reports whether the group has at least one operation that
// generates a CLI command. Groups whose operations are all CLI-excluded emit no
// group command file and are not registered.
func (g *CommandGroup) HasCLICommands() bool {
	for _, op := range g.Operations {
		if !op.CLIExcluded {
			return true
		}
	}
	return false
}

// Waiter describes a special command that blocks until a condition has been
// met, after which it exits.
type Waiter struct {
	CLIName     string
	GoName      string
	Use         string
	Aliases     []string
	Short       string
	Long        string
	Delay       int
	Attempts    int
	OperationID string `json:"operationId"`
	Operation   *Operation
	Matchers    []*Matcher
	After       map[string]map[string]string
}

// Matcher describes a condition to match for a waiter.
type Matcher struct {
	Select   string
	Test     string
	Expected json.RawMessage
	State    string
}

// WaiterParams links a waiter with param selector querires to perform wait
// operations after a command has run.
type WaiterParams struct {
	Waiter *Waiter
	Args   []string
	Params map[string]string
}

// Server describes an OpenAPI server endpoint
type Server struct {
	Description string
	URL         string
	// TODO: handle server parameters
}

// Imports describe optional imports based on features in use.
type Imports struct {
	Fmt     bool
	Strings bool
	Time    bool
	Url     bool
}

// ProjectConfig describes local generator metadata written by `init`.
type ProjectConfig struct {
	AppName             string `json:"app_name"`
	AppVersion          string `json:"app_version,omitempty"`
	ModulePath          string `json:"module_path,omitempty"`
	BartoloReplacePath  string `json:"bartolo_replace_path,omitempty"`
	BartoloVersion      string `json:"bartolo_version,omitempty"`
	EnvPrefix           string `json:"env_prefix"`
	SerializationFormat string `json:"serialization_format,omitempty"`
	// LegacyDefaultOutputFormat carries the pre-table meaning of the field so a
	// project written by an older bartolo does not silently change what it
	// serializes to. Written back under the current name on the next sync.
	LegacyDefaultOutputFormat string `json:"default_output_format,omitempty"`
	APIKeyEnvVar              string `json:"api_key_env_var,omitempty"`
	LastSpecPath              string `json:"last_spec_path,omitempty"`
	// Targets records which surfaces the project generates: "cli" and/or "mcp".
	// When absent (legacy configs), both surfaces are generated. See
	// defaultTargets / effectiveTargets.
	Targets []string `json:"targets,omitempty"`
}

// defaultTargets is the surface set used when a project does not record an
// explicit choice: both the CLI commands and the MCP server.
func defaultTargets() []string {
	return []string{targetCLI, targetMCP}
}

const (
	targetCLI = "cli"
	targetMCP = "mcp"
)

// normalizeTargets validates and de-duplicates a list of generation targets,
// preserving order. Members must be "cli" or "mcp" and the result must be
// non-empty.
func normalizeTargets(parts []string) ([]string, error) {
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p != targetCLI && p != targetMCP {
			return nil, fmt.Errorf("invalid target %q (valid targets: %s, %s)", p, targetCLI, targetMCP)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("targets cannot be empty (valid targets: %s, %s)", targetCLI, targetMCP)
	}
	return out, nil
}

// parseTargetsFlag parses the scripted `--targets` comma list. An empty value
// selects the default (both surfaces).
func parseTargetsFlag(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultTargets(), nil
	}
	return normalizeTargets(strings.Split(raw, ","))
}

// effectiveTargets returns the recorded targets, defaulting to both surfaces
// for a nil config or a config without an explicit targets list.
func (c *ProjectConfig) effectiveTargets() []string {
	if c == nil || len(c.Targets) == 0 {
		return defaultTargets()
	}
	return c.Targets
}

// TargetCLI reports whether the CLI command surface should be generated.
func (c *ProjectConfig) TargetCLI() bool {
	return containsString(c.effectiveTargets(), targetCLI)
}

// TargetMCP reports whether the MCP server surface should be generated.
func (c *ProjectConfig) TargetMCP() bool {
	return containsString(c.effectiveTargets(), targetMCP)
}

// AuthDoc describes auth setup to show in a generated README.
type AuthDoc struct {
	Enabled         bool
	Kind            string
	EnvPrefix       string
	EnvVars         []string
	ProfileCommand  string
	Summary         string
	ProfileRequired bool
	// Schemes documents each recognized security scheme (populated for
	// multi-scheme and OAuth2 specs). When empty, the single-scheme fields
	// above drive the README auth section.
	Schemes []*AuthDocScheme
	// ManualNotes lists schemes that could not be auto-wired and need manual
	// setup (openIdConnect, http basic, etc.).
	ManualNotes []string
}

// AuthDocScheme documents a single security scheme in a generated README.
type AuthDocScheme struct {
	Name     string
	Kind     string
	TokenURL string
	Default  bool
}

// READMEExample is a copy-pasteable command example for a generated CLI.
type READMEExample struct {
	Title       string
	Command     string
	Description string
}

type selectOption struct {
	Value       string
	Label       string
	Description string
}

// OpenAPI describes an API
type OpenAPI struct {
	Imports      Imports
	Name         string
	GoName       string
	PublicGoName string
	Title        string
	Description  string
	Servers      []*Server
	Groups       []*CommandGroup
	Operations   []*Operation
	Waiters      []*Waiter
	AuthInit     string
	// NeedsAPIKeyImport / NeedsOAuthImport tell the client template which auth
	// runtime packages the emitted AuthInit lines actually reference.
	NeedsAPIKeyImport bool
	NeedsOAuthImport  bool
	// AuthSchemes captures the recognized security schemes emitted into
	// AuthInit so README generation can document each one.
	AuthSchemes []*AuthScheme
	AuthDoc     *AuthDoc
	CommandName string
	Examples    []*READMEExample
	// TargetCLI / TargetMCP select which surfaces the generator emits. Both
	// default to true (see ProcessAPI) and are overridden from ProjectConfig at
	// render time. TargetMCP additionally gates HasMCPTools.
	TargetCLI bool
	TargetMCP bool
}

// AuthScheme is a recognized (or explicitly unsupported) OpenAPI security
// scheme captured during generation for README documentation.
type AuthScheme struct {
	Name      string // OpenAPI security scheme name
	Kind      string // human-readable kind, e.g. "Bearer token", "OAuth2 client credentials"
	TokenURL  string // OAuth2 token endpoint, when applicable
	Default   bool   // true for the scheme registered as the default auth type
	Supported bool   // false → documented as requiring manual setup
	Manual    string // reason/instructions when unsupported
}

// CommandsTemplateData describes a generated commands file for either the
// root command set or a specific command group.
type CommandsTemplateData struct {
	API        *OpenAPI
	Group      *CommandGroup
	Operations []*Operation
	Waiters    []*Waiter
	NeedsFmt   bool
}

// ProcessAPI returns the API description to be used with the commands template
// for a loaded and dereferenced OpenAPI 3 document.
func ProcessAPI(shortName string, api *openapi3.T) *OpenAPI {
	b := newIRBuilder(newOpenAPI(shortName, api), api.Tags)

	paths := slices.Sorted(maps.Keys(api.Paths.Map()))
	for _, path := range paths {
		b.addPath(path, api.Paths.Value(path))
	}
	b.collectGroups()

	assignMCPToolNames(b.result)
	b.addWaiters(api.Extensions)

	return b.result
}

// newOpenAPI builds the API-level part of the IR: names, description, auth,
// and servers.
func newOpenAPI(shortName string, api *openapi3.T) *OpenAPI {
	apiName := shortName
	if name := extString(api.Info.Extensions, extKeyName); name != "" {
		apiName = name
	}

	apiDescription := api.Info.Description
	if description, ok := extValue(api.Info.Extensions, extKeyDescription); ok {
		apiDescription = extStr(description)
	}

	authInit, needsAPIKey, needsOAuth, authSchemes := buildAuthInit(api)
	result := &OpenAPI{
		Name:              apiName,
		GoName:            toGoName(shortName, false),
		PublicGoName:      toGoName(shortName, true),
		Title:             api.Info.Title,
		Description:       escapeString(apiDescription),
		AuthInit:          authInit,
		NeedsAPIKeyImport: needsAPIKey,
		NeedsOAuthImport:  needsOAuth,
		AuthSchemes:       authSchemes,
		// Both surfaces default on; generateFromSpec overrides these from the
		// project's recorded targets before rendering.
		TargetCLI: true,
		TargetMCP: true,
	}

	for _, s := range api.Servers {
		result.Servers = append(result.Servers, &Server{
			Description: s.Description,
			URL:         s.URL,
		})
	}
	return result
}

// irBuilder accumulates operations and groups into an OpenAPI IR while
// ProcessAPI walks the paths.
type irBuilder struct {
	result *OpenAPI
	// operations maps operation IDs to operations, for waiters.
	operations  map[string]*Operation
	usedGoNames map[string]bool
	tagDefs     map[string]*openapi3.Tag
	groups      map[string]*CommandGroup
	groupOrder  []string
}

func newIRBuilder(result *OpenAPI, tags openapi3.Tags) *irBuilder {
	b := &irBuilder{
		result:      result,
		operations:  make(map[string]*Operation),
		usedGoNames: make(map[string]bool),
		tagDefs:     make(map[string]*openapi3.Tag),
		groups:      make(map[string]*CommandGroup),
	}
	for _, tag := range tags {
		if tag != nil {
			b.tagDefs[tag.Name] = tag
		}
	}
	return b
}

// pathScope is what a path item contributes to each of its operations.
type pathScope struct {
	path string
	item *openapi3.PathItem
	// hidden is the path-level x-bartolo-hidden default.
	hidden bool
	// cliExcluded is the path-level x-bartolo-cli-ignore: every operation under
	// the path stays out of the CLI while still allowing MCP tools.
	cliExcluded bool
}

// addPath adds the operations under one path, in sorted method order.
func (b *irBuilder) addPath(path string, item *openapi3.PathItem) {
	if sharedIgnored(item.Extensions) {
		// Ignore this path everywhere (CLI and MCP).
		return
	}

	scope := pathScope{path: path, item: item, cliExcluded: cliIgnored(item.Extensions)}
	if hidden, ok := extValue(item.Extensions, extKeyHidden); ok {
		mustDecodeExt(hidden, &scope.hidden)
	}

	operations := item.Operations()
	for _, method := range slices.Sorted(maps.Keys(operations)) {
		b.addOperation(scope, method, operations[method])
	}
}

// addOperation builds one operation's IR and files it under its group (or the
// root). httpMethod is the method as the path item keys it.
func (b *irBuilder) addOperation(scope pathScope, httpMethod string, operation *openapi3.Operation) {
	if sharedIgnored(operation.Extensions) {
		// Ignore this operation everywhere (CLI and MCP).
		return
	}
	path := scope.path

	name := operationName(httpMethod, path, operation)
	explicitLeafName := extString(operation.Extensions, extKeyName)
	leafName := cmp.Or(explicitLeafName, name)

	var aliases []string
	if a, ok := extValue(operation.Extensions, extKeyAliases); ok {
		mustDecodeExt(a, &aliases)
	}
	listFields, isList := classifyList(httpMethod, path, operation)

	params := getParams(scope.item, httpMethod)
	requiredParams := getRequiredParams(params)
	optionalParams := getOptionalParams(params)

	reqInfo := getRequestInfo(operation)
	renamedFlags := reserveGeneratedFlagNames(reqInfo.bodyFields, optionalParams)
	warnRenamedFlags(httpMethod, path, renamedFlags)
	description, examples := operationDescription(operation, reqInfo, renamedFlags)

	method := strings.Title(strings.ToLower(httpMethod))
	group, leafName := b.resolveGroup(path, operation, leafName, method, explicitLeafName != "")
	short := operationShort(operation.Summary, leafName)
	if args := requiredArgsHelp(requiredParams); args != "" {
		// Cobra prints Long or Short, never both, so a Long that exists
		// only for this section still has to carry the summary.
		description = cmp.Or(description, short) + args
	}

	use := usage(leafName, requiredParams)
	commandPath := use
	if group != nil {
		commandPath = group.CLIName + " " + use
	}

	o := &Operation{
		HandlerName:    commandPath,
		GoName:         b.uniqueGoName(name),
		PublicPrefix:   b.result.PublicGoName,
		Use:            use,
		Aliases:        aliases,
		Short:          short,
		Long:           escapeString(description),
		Method:         method,
		CanHaveBody:    operation.RequestBody != nil && operation.RequestBody.Value != nil,
		ReturnType:     operationReturnType(operation),
		Path:           path,
		AllParams:      params,
		RequiredParams: requiredParams,
		OptionalParams: optionalParams,
		MediaType:      reqInfo.mediaType,
		Examples:       examples,
		BodyExample:    reqInfo.exampleBody,
		BodyFields:     reqInfo.bodyFields,
		Hidden:         operationHidden(scope.hidden, operation),
		IsList:         isList,
		ListFields:     listFields,
		Group:          group,
		HelpSection:    extString(operation.Extensions, extKeyHelpSection),
		CommandPath:    commandPath,
		LeafName:       leafName,
		// The op still enters the IR (so it can become an MCP tool) but is
		// excluded from CLI command generation.
		CLIExcluded: scope.cliExcluded || cliIgnored(operation.Extensions),
	}
	o.MCP = buildMCPTool(o, operation, scope.item)

	b.register(o, operation.OperationID)
}

// register files an operation under its group (or the root), indexes it by
// operation ID, and records the imports its client func needs.
func (b *irBuilder) register(o *Operation, operationID string) {
	b.operations[operationID] = o
	if o.Group != nil {
		o.Group.Operations = append(o.Group.Operations, o)
	} else {
		b.result.Operations = append(b.result.Operations, o)
	}

	if slices.ContainsFunc(o.AllParams, func(p *Param) bool { return p.In == "path" }) {
		b.result.Imports.Strings = true
		b.result.Imports.Url = true
	}
	if slices.ContainsFunc(o.OptionalParams, func(p *Param) bool { return p.In == "query" || p.In == "header" }) {
		b.result.Imports.Fmt = true
	}
}

// resolveGroup finds the operation's command group, reusing the group already
// built for the same CLI name, and infers the leaf name within it.
func (b *irBuilder) resolveGroup(path string, operation *openapi3.Operation, leafName, method string, explicit bool) (*CommandGroup, string) {
	group := resolveCommandGroup(path, operation, b.tagDefs)
	if group == nil {
		return nil, leafName
	}
	if existing := b.groups[group.CLIName]; existing != nil {
		group = existing
	} else {
		b.groups[group.CLIName] = group
		b.groupOrder = append(b.groupOrder, group.CLIName)
	}

	leafName = inferGroupedLeafName(leafName, strings.ToLower(method), path, group.CLIName, explicit)
	if leafName == "" {
		leafName = slug(method)
	}
	return group, leafName
}

// collectGroups appends the groups in first-seen order, each with its
// operations sorted by command path.
func (b *irBuilder) collectGroups() {
	for _, key := range b.groupOrder {
		group := b.groups[key]
		sort.Slice(group.Operations, func(i, j int) bool {
			return group.Operations[i].CommandPath < group.Operations[j].CommandPath
		})
		b.result.Groups = append(b.result.Groups, group)
	}
}

// uniqueGoName returns the exported Go name for an operation, numbered when
// two operations share an operationId.
func (b *irBuilder) uniqueGoName(name string) string {
	goName := toGoName(name, true)
	for suffix := 2; b.usedGoNames[goName]; suffix++ {
		goName = toGoName(name, true) + strconv.Itoa(suffix)
	}
	b.usedGoNames[goName] = true
	return goName
}

// operationName is the operationId, or one generated from the method and path
// when it is missing.
func operationName(httpMethod, path string, operation *openapi3.Operation) string {
	if operation.OperationID != "" {
		return operation.OperationID
	}
	name := strings.ToLower(httpMethod) + strings.Replace(strings.Replace(path, "/", "-", -1), "{", "", -1)
	name = strings.Replace(name, "}", "", -1)
	return strings.Trim(name, "-")
}

// classifyList returns the declared list columns and whether the operation
// renders as a collection: marked by x-bartolo-list or x-bartolo-list-fields,
// or inferred, unless x-bartolo-list: false turns inference off.
func classifyList(httpMethod, path string, operation *openapi3.Operation) ([]string, bool) {
	var listFields []string
	if fields, ok := extValue(operation.Extensions, extKeyListFields); ok {
		mustDecodeExt(fields, &listFields)
	}

	listExt, hasOverride := extValue(operation.Extensions, extKeyList)
	override := extBool(listExt)
	if hasOverride && !override {
		if len(listFields) > 0 {
			panic(fmt.Errorf("%s %s: %s is false but %s declares columns; remove one of them",
				strings.ToUpper(httpMethod), path, "x-bartolo-list", "x-bartolo-list-fields"))
		}
		return listFields, false
	}
	return listFields, len(listFields) > 0 || override || isInferredList(httpMethod, path, operation)
}

func warnRenamedFlags(httpMethod, path string, renamed []renamedFlag) {
	for _, r := range renamed {
		kind, _ := bartolocli.ReservedFlagName(r.From)
		if kind == "" {
			kind = "another flag on this command"
		}
		log.Printf("warning: %s %s: --%s collides with %s, exposing it as --%s", httpMethod, path, r.From, kind, r.To)
	}
}

// operationDescription assembles the command's long help: the description (or
// its x-bartolo-description override), input examples, the request schema
// summary, and notes on body-field flags and renamed flags. Structured
// examples become shorthand examples instead.
func operationDescription(operation *openapi3.Operation, reqInfo requestInfo, renamed []renamedFlag) (string, []string) {
	description := operation.Description
	if d, ok := extValue(operation.Extensions, extKeyDescription); ok {
		description = extStr(d)
	}

	description, examples := appendInputExamples(description, reqInfo.examples)
	if reqInfo.summary != "" {
		description += "\n\n" + reqInfo.summary
	}
	description += bodyFieldsHelp(reqInfo.bodyFields)
	description += renamedFlagsHelp(renamed)
	return description, examples
}

// appendInputExamples turns object examples into shorthand examples and
// appends every other example to the description under an Input Example
// heading.
func appendInputExamples(description string, examples []interface{}) (string, []string) {
	var shorthandExamples []string
	wroteHeader := false
	for _, ex := range examples {
		if m, ok := ex.(map[string]interface{}); ok {
			shorthandExamples = append(shorthandExamples, shorthand.Get(m))
			continue
		}

		text, ok := ex.(string)
		if !ok {
			b, _ := json.Marshal(ex)
			text = string(b)
		}
		if !wroteHeader {
			description += "\n## Input Example\n\n"
			wroteHeader = true
		}
		description += "\n" + text + "\n"
	}
	return description, shorthandExamples
}

func bodyFieldsHelp(bodyFields []*BodyField) string {
	if len(bodyFields) == 0 {
		return ""
	}
	help := "\n\nAll top-level body fields are exposed as flags for this command. " +
		"Scalar, nullable scalar (pass `null` for JSON null), enum, repeatable list (`--field a --field b`), " +
		"and string map (`--field key=value`) fields use typed flags. " +
		"Nested objects, arrays of objects, and polymorphic unions accept a JSON string (e.g. `--field '{\"k\":1}'`)."
	// An enum or array date-time keeps its own flag type and is sent
	// as typed, so the syntax must not be promised there.
	if slices.ContainsFunc(bodyFields, func(f *BodyField) bool {
		return f != nil && (f.Type == "datetime" || f.Type == "datetime-nullable")
	}) {
		help += " Timestamp fields (`format: date-time`) also accept a bare date or a relative value such as `24h`, `7d` or `now-24h`."
	}
	return help
}

func renamedFlagsHelp(renamed []renamedFlag) string {
	if len(renamed) == 0 {
		return ""
	}
	help := "\n\nRenamed flags (the original names belong to global flags):\n"
	for _, r := range renamed {
		help += fmt.Sprintf("- `%s` is `--%s` (not `--%s`)\n", r.Field, r.To, r.From)
	}
	return help
}

// operationShort is the summary, or a display name derived from the leaf name.
func operationShort(summary, leafName string) string {
	if summary != "" {
		return summary
	}
	return cmp.Or(displayNameFromSlug(leafName), leafName)
}

// operationHidden applies an operation's x-bartolo-hidden over the path's.
func operationHidden(pathHidden bool, operation *openapi3.Operation) bool {
	hidden := pathHidden
	if h, ok := extValue(operation.Extensions, extKeyHidden); ok {
		mustDecodeExt(h, &hidden)
	}
	return hidden
}

// operationReturnType is map[string]interface{} when any 2xx response carries
// an object (by example or schema), else interface{}.
func operationReturnType(operation *openapi3.Operation) string {
	for code, ref := range operation.Responses.Map() {
		if num, err := strconv.Atoi(code); err != nil || num < 200 || num >= 300 || ref.Value == nil {
			continue
		}
		if slices.ContainsFunc(slices.Collect(maps.Values(ref.Value.Content)), mediaTypeIsObject) {
			return "map[string]interface{}"
		}
	}
	return "interface{}"
}

func mediaTypeIsObject(content *openapi3.MediaType) bool {
	if _, ok := content.Example.(map[string]interface{}); ok {
		return true
	}
	schema := content.Schema
	return schema != nil && schema.Value != nil && (schema.Value.Type.Is("object") || len(schema.Value.Properties) != 0)
}

// addWaiters wires the x-bartolo-waiters extension: each waiter command, and
// the follow-up flags on the operations it runs after. Waiters are processed in
// name order so the generated output is reproducible.
func (b *irBuilder) addWaiters(ext map[string]interface{}) {
	w, ok := extValue(ext, extKeyWaiters)
	if !ok {
		return
	}

	var waiters map[string]*Waiter
	mustDecodeExt(w, &waiters)
	for _, name := range slices.Sorted(maps.Keys(waiters)) {
		b.result.Waiters = append(b.result.Waiters, b.buildWaiter(name, waiters[name]))
	}
	if len(waiters) > 0 {
		b.result.Imports.Time = true
	}
}

func (b *irBuilder) buildWaiter(name string, waiter *Waiter) *Waiter {
	waiter.CLIName = slug(name)
	waiter.GoName = toGoName(name+"-waiter", true)
	waiter.Operation = b.operations[waiter.OperationID]
	if waiter.Operation == nil {
		panic(fmt.Errorf("Unknown waiter operation %s", waiter.OperationID))
	}
	waiter.Use = usage(name, waiter.Operation.RequiredParams)

	for _, matcher := range waiter.Matchers {
		if matcher.Test == "" {
			matcher.Test = "equal"
		}
	}

	for _, operationID := range slices.Sorted(maps.Keys(waiter.After)) {
		b.attachWaiter(name, waiter, operationID, waiter.After[operationID])
	}
	return waiter
}

// attachWaiter adds the follow-up waiter to the operation it runs after.
// selectors map that operation's parameter names to response selectors: the
// required ones become the waiter's arguments, the rest its flags.
func (b *irBuilder) attachWaiter(name string, waiter *Waiter, operationID string, selectors map[string]string) {
	op := b.operations[operationID]
	if op == nil {
		panic(fmt.Errorf("Unknown waiter operation %s", operationID))
	}

	args := waiterArgs(op, selectors)
	if len(args) > 0 {
		b.result.Imports.Fmt = true
		op.NeedsResponse = true
	}

	op.Waiters = append(op.Waiters, &WaiterParams{
		Waiter: waiter,
		Args:   args,
		Params: waiterFlags(name, op, selectors),
	})
}

// waiterArgs consumes the selectors for op's required parameters, in order.
func waiterArgs(op *Operation, selectors map[string]string) []string {
	var args []string
	for _, p := range op.RequiredParams {
		selector := selectors[p.Name]
		if selector == "" {
			panic(fmt.Errorf("Missing required parameter %s", p.Name))
		}
		delete(selectors, p.Name)
		args = append(args, selector)
	}
	return args
}

// waiterFlags maps the remaining selectors from OpenAPI parameter names to the
// CLI names of op's optional parameters.
func waiterFlags(name string, op *Operation, selectors map[string]string) map[string]string {
	flags := make(map[string]string)
	for param, selector := range selectors {
		i := slices.IndexFunc(op.OptionalParams, func(optional *Param) bool { return optional.Name == param })
		if i < 0 {
			panic(fmt.Errorf("Unknown parameter %s for waiter %s", param, name))
		}
		flags[op.OptionalParams[i].CLIName] = selector
	}
	return flags
}

// extBool returns the boolean value of an OpenAPI extension. A missing
// extension is false, so `x-cli-no-validate: false` means what it says rather
// than being read as mere presence. A present but non-boolean value fails
// generation like every other malformed extension: a typo that silently
// disables a flag is the failure mode this generator exists to avoid.
func extBool(i interface{}) bool {
	if i == nil {
		return false
	}
	if b, ok := i.(bool); ok {
		return b
	}

	var decoded bool
	mustDecodeExt(i, &decoded)
	return decoded
}

// extStr returns the string value of an OpenAPI extension stored as a JSON
// raw message.
func extStr(i interface{}) (decoded string) {
	mustDecodeExt(i, &decoded)
	return
}

func mustDecodeExt(input interface{}, target interface{}) {
	switch value := input.(type) {
	case json.RawMessage:
		if err := json.Unmarshal(value, target); err != nil {
			panic(err)
		}
	case []byte:
		if err := json.Unmarshal(value, target); err != nil {
			panic(err)
		}
	default:
		data, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		if err := json.Unmarshal(data, target); err != nil {
			panic(err)
		}
	}
}

func toGoName(input string, public bool) string {
	transformed := strings.Replace(input, "-", " ", -1)
	transformed = strings.Replace(transformed, "_", " ", -1)
	transformed = strings.Title(transformed)
	transformed = strings.Replace(transformed, " ", "", -1)

	if !public {
		transformed = strings.ToLower(string(transformed[0])) + transformed[1:]
	}

	return transformed
}

func escapeString(value string) string {
	transformed := strings.Replace(value, "\\", "\\\\", -1)
	transformed = strings.Replace(transformed, "\n", "\\n", -1)
	transformed = strings.Replace(transformed, "\"", "\\\"", -1)
	return transformed
}

func slug(operationID string) string {
	trimmed := strings.TrimSpace(operationID)
	if trimmed == "" {
		return ""
	}

	runes := []rune(trimmed)
	var out []rune

	for i, r := range runes {
		if r == '_' || r == ' ' || r == '/' || r == '.' || r == '-' {
			if len(out) > 0 && out[len(out)-1] != '-' {
				out = append(out, '-')
			}
			continue
		}

		if unicode.IsUpper(r) {
			if len(out) > 0 && out[len(out)-1] != '-' {
				prev := runes[i-1]
				nextStartsWord := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextStartsWord) {
					out = append(out, '-')
				}
			}
			out = append(out, unicode.ToLower(r))
			continue
		}

		out = append(out, unicode.ToLower(r))
	}

	return strings.Trim(string(out), "-")
}

// A required parameter reaches the user as a bare name on the usage line and
// nowhere else, so its description, format and enum are repeated here. The
// suffixes compose in the same order the params template applies them.
func requiredArgsHelp(params []*Param) string {
	if len(params) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n## Arguments\n")
	for _, p := range params {
		desc := strings.TrimSpace(p.Description)
		if len(p.Enum) > 0 {
			desc = strings.TrimSpace(desc + " (one of: " + strings.Join(p.Enum, ", ") + ")")
		}
		if p.Format == "date-time" {
			desc = bartolocli.WithDateTimeHelp(desc)
		}

		b.WriteString("\n- `" + slug(p.Name) + "`")
		if desc != "" {
			b.WriteString(" — " + desc)
		}
	}

	return b.String()
}

func usage(name string, requiredParams []*Param) string {
	usage := slug(name)

	for _, p := range requiredParams {
		usage += " " + slug(p.Name)
	}

	return usage
}

func normalizeSpecName(filename string) string {
	base := path.Base(filename)
	ext := strings.ToLower(path.Ext(base))
	stem := strings.TrimSuffix(base, ext)
	if ext != ".yaml" && ext != ".yml" && ext != ".json" {
		stem = base
	}

	return slug(stem)
}

func resolveCommandGroup(path string, operation *openapi3.Operation, tagDefs map[string]*openapi3.Tag) *CommandGroup {
	groupName := extString(operation.Extensions, extKeyGroup)
	var tagDef *openapi3.Tag

	if groupName == "" && len(operation.Tags) > 0 {
		groupName = operation.Tags[0]
		tagDef = tagDefs[groupName]
	} else if groupName != "" {
		tagDef = tagDefs[groupName]
	}

	if groupName == "" {
		groupName = inferGroupFromPath(path)
	}

	if groupName == "" {
		return nil
	}

	cliName := slug(groupName)
	section := ""
	short := displayNameFromSlug(cliName)
	long := ""
	var aliases []string
	hidden := false

	if tagDef != nil {
		if override := extString(tagDef.Extensions, extKeyName); override != "" {
			cliName = slug(override)
		}
		if description := extString(tagDef.Extensions, extKeyDescription); description != "" {
			long = description
		} else {
			long = tagDef.Description
		}
		if tagDef.Description != "" {
			short = tagDef.Description
		}
		if tagDef.Name != "" {
			short = tagDef.Name
		}
		if a, ok := extValue(tagDef.Extensions, extKeyAliases); ok {
			mustDecodeExt(a, &aliases)
		}
		if h, ok := extValue(tagDef.Extensions, extKeyHidden); ok {
			mustDecodeExt(h, &hidden)
		}
		section = extString(tagDef.Extensions, extKeyHelpSection)
	}

	if override := extString(operation.Extensions, extKeyGroup); override != "" {
		cliName = slug(override)
	}

	if override := extString(operation.Extensions, extKeyHelpSection); override != "" {
		section = override
	}

	return &CommandGroup{
		Name:        groupName,
		CLIName:     cliName,
		GoName:      toGoName(cliName, false),
		Short:       short,
		Long:        escapeString(long),
		Aliases:     aliases,
		Hidden:      hidden,
		HelpSection: section,
		Description: long,
	}
}

func inferGroupFromPath(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for _, segment := range segments {
		if segment == "" || strings.HasPrefix(segment, "{") {
			continue
		}
		if isVersionToken(segment) {
			continue
		}
		return segment
	}

	return ""
}

func displayNameFromSlug(value string) string {
	slugged := slug(value)
	if slugged == "" {
		return ""
	}

	return strings.Title(strings.ReplaceAll(slugged, "-", " "))
}

func trimGroupPrefix(name string, group string) string {
	leaf := slug(name)
	if leaf == "" {
		return leaf
	}

	stems := []string{slug(group), singularize(slug(group))}
	for _, stem := range stems {
		if stem == "" {
			continue
		}
		prefix := stem + "-"
		if strings.HasPrefix(leaf, prefix) {
			trimmed := strings.TrimPrefix(leaf, prefix)
			if trimmed != "" {
				return trimmed
			}
		}
		suffix := "-" + stem
		if strings.HasSuffix(leaf, suffix) {
			trimmed := strings.TrimSuffix(leaf, suffix)
			if trimmed != "" {
				return trimmed
			}
		}
	}

	return leaf
}

func inferGroupedLeafName(name string, httpMethod string, path string, group string, explicit bool) string {
	leaf := trimGroupPrefix(name, group)
	leaf = normalizeGroupedLeafName(leaf)
	leaf = trimGroupPrefix(leaf, group)
	leaf = trimGroupedNounAfterVerb(leaf, group)

	if explicit {
		return leaf
	}

	if isUsefulGroupedLeaf(leaf, httpMethod) {
		return leaf
	}

	if inferred := inferLeafFromPath(httpMethod, path, group); inferred != "" {
		return inferred
	}

	return leaf
}

func trimGroupedNounAfterVerb(leaf string, group string) string {
	parts := strings.Split(slug(leaf), "-")
	if len(parts) < 2 {
		return slug(leaf)
	}

	verbs := map[string]bool{
		"get": true, "list": true, "create": true, "update": true, "delete": true,
		"upload": true, "download": true, "search": true, "query": true, "find": true,
	}
	if !verbs[parts[0]] {
		return strings.Join(parts, "-")
	}

	groupForms := map[string]bool{
		slug(group):              true,
		singularize(slug(group)): true,
	}
	if groupForms[parts[1]] {
		parts = append(parts[:1], parts[2:]...)
	}

	return strings.Join(parts, "-")
}

func normalizeGroupedLeafName(leaf string) string {
	normalized := slug(leaf)
	if normalized == "" {
		return normalized
	}

	replacements := map[string]string{
		"get-all":  "list",
		"list-all": "list",
		"get-one":  "get",
		"find-one": "get",
		"find":     "get",
	}
	if replacement := replacements[normalized]; replacement != "" {
		return replacement
	}

	prefixReplacements := []struct {
		from string
		to   string
	}{
		{"get-all-", "list-"},
		{"list-all-", "list-"},
		{"get-one-", "get-"},
		{"find-one-", "get-"},
		{"find-", "get-"},
	}
	for _, replacement := range prefixReplacements {
		if strings.HasPrefix(normalized, replacement.from) {
			return replacement.to + strings.TrimPrefix(normalized, replacement.from)
		}
	}

	methodPrefixes := []string{"get", "post", "put", "patch", "delete", "head"}
	for _, method := range methodPrefixes {
		versionPrefix := method + "-v"
		if strings.HasPrefix(normalized, versionPrefix) {
			parts := strings.Split(normalized, "-")
			if len(parts) >= 3 && isVersionToken(parts[1]) {
				return strings.Join(parts[2:], "-")
			}
		}
	}

	if strings.HasPrefix(normalized, "v") {
		parts := strings.Split(normalized, "-")
		if len(parts) >= 2 && isVersionToken(parts[0]) {
			return strings.Join(parts[1:], "-")
		}
	}

	return normalized
}

func isUsefulGroupedLeaf(leaf string, httpMethod string) bool {
	if leaf == "" {
		return false
	}

	lowSignalLeaves := map[string]bool{
		"id": true,
	}
	if lowSignalLeaves[leaf] {
		return false
	}

	if strings.Contains(leaf, "v1-") || strings.Contains(leaf, "v2-") || strings.Contains(leaf, "v3-") {
		return false
	}

	if strings.HasPrefix(leaf, httpMethod+"-v") {
		return false
	}

	return true
}

func inferLeafFromPath(httpMethod string, rawPath string, group string) string {
	segments := strings.Split(strings.Trim(rawPath, "/"), "/")
	staticSegments := make([]string, 0, len(segments))
	item := false

	for _, segment := range segments {
		if segment == "" || isVersionToken(segment) {
			continue
		}
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			item = true
			continue
		}
		staticSegments = append(staticSegments, slug(segment))
	}

	groupIndex := -1
	groupForms := map[string]bool{
		slug(group):              true,
		singularize(slug(group)): true,
	}
	for i, segment := range staticSegments {
		if groupForms[segment] {
			groupIndex = i
			break
		}
	}

	if groupIndex >= 0 {
		staticSegments = staticSegments[groupIndex+1:]
	}

	switch httpMethod {
	case "get", "head":
		if len(staticSegments) == 0 {
			if item {
				return "get"
			}
			return "list"
		}
		last := staticSegments[len(staticSegments)-1]
		if last == "query" || last == "search" {
			return last
		}
		if item {
			return "get-" + strings.Join(staticSegments, "-")
		}
		return strings.Join(staticSegments, "-")
	case "post":
		if len(staticSegments) == 0 {
			return "create"
		}
		last := staticSegments[len(staticSegments)-1]
		if isActionSegment(last) {
			return strings.Join(staticSegments, "-")
		}
		return "create-" + singularize(strings.Join(staticSegments, "-"))
	case "put", "patch":
		if len(staticSegments) == 0 {
			return "update"
		}
		last := staticSegments[len(staticSegments)-1]
		if isActionSegment(last) {
			return strings.Join(staticSegments, "-")
		}
		return "update-" + singularize(strings.Join(staticSegments, "-"))
	case "delete":
		if len(staticSegments) == 0 {
			return "delete"
		}
		return "delete-" + singularize(strings.Join(staticSegments, "-"))
	default:
		return strings.Join(staticSegments, "-")
	}
}

func isActionSegment(segment string) bool {
	switch segment {
	case "query", "search", "invoke", "stream", "upload", "download", "duplicate", "validate", "refresh", "invalidate", "cancel", "retry":
		return true
	default:
		return false
	}
}

func isCollectionPath(rawPath string) bool {
	for _, segment := range strings.Split(strings.Trim(rawPath, "/"), "/") {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			return false
		}
	}
	return true
}

func forEachSuccessResponseContent(operation *openapi3.Operation, fn func(mediaType string, content *openapi3.MediaType) bool) bool {
	for code, response := range operation.Responses.Map() {
		status, err := strconv.Atoi(code)
		if err != nil || status < 200 || status >= 300 || response == nil || response.Value == nil {
			continue
		}

		for mediaType, content := range response.Value.Content {
			if content == nil {
				continue
			}
			if fn(mediaType, content) {
				return true
			}
		}
	}

	return false
}

func isCollectionResponse(operation *openapi3.Operation) bool {
	return forEachSuccessResponseContent(operation, func(_ string, content *openapi3.MediaType) bool {
		if _, ok := content.Example.([]interface{}); ok {
			return true
		}
		if content.Schema == nil || content.Schema.Value == nil {
			return false
		}

		schema := content.Schema.Value
		if schema.Items != nil {
			return true
		}
		// Any array property counts: wrappers are named after the resource
		// as often as they are called `data` or `items`.
		for _, property := range schema.Properties {
			if property != nil && property.Value != nil && property.Value.Items != nil {
				return true
			}
		}
		return false
	})
}

// isPaginatedCollectionResponse reports whether a 2xx JSON response is a page of
// rows: an array of objects under a conventional collection key, next to a field
// describing the collection. That last gate separates retrieval from computation,
// keeping /v2/router/embeddings out. Only immediate properties are read, so an
// allOf/oneOf envelope needs `x-cli-list` instead.
func isPaginatedCollectionResponse(operation *openapi3.Operation) bool {
	return forEachSuccessResponseContent(operation, func(mediaType string, content *openapi3.MediaType) bool {
		if !isJSONMediaType(mediaType) || content.Schema == nil || content.Schema.Value == nil {
			return false
		}

		properties := content.Schema.Value.Properties
		if !hasObjectArrayProperty(properties, bartolocli.ConventionalCollectionKeys) {
			return false
		}
		for _, key := range bartolocli.PaginationEvidenceKeys {
			if properties[key] != nil {
				return true
			}
		}
		return false
	})
}

func isJSONMediaType(mediaType string) bool {
	base, _, _ := strings.Cut(mediaType, ";")
	// RFC 9110 media type tokens are case-insensitive; specs do write `Application/JSON`.
	base = strings.ToLower(strings.TrimSpace(base))
	return base == "application/json" || strings.HasSuffix(base, "+json")
}

func hasObjectArrayProperty(properties openapi3.Schemas, keys []string) bool {
	for _, key := range keys {
		property := properties[key]
		if property == nil || property.Value == nil || property.Value.Items == nil {
			continue
		}
		// `items: {type: object}` parses with a nil Properties map, so the declared type has to be consulted too.
		if items := property.Value.Items.Value; items != nil && (items.Type.Is("object") || items.Properties != nil) {
			return true
		}
	}

	return false
}

func isInferredList(method, path string, operation *openapi3.Operation) bool {
	if strings.EqualFold(method, "get") && (isCollectionPath(path) || isCollectionResponse(operation)) {
		return true
	}
	return isPaginatedCollectionResponse(operation)
}

func isVersionToken(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}
	for _, r := range segment[1:] {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func singularize(value string) string {
	switch {
	case strings.HasSuffix(value, "ies") && len(value) > 3:
		return strings.TrimSuffix(value, "ies") + "y"
	case strings.HasSuffix(value, "ses") && len(value) > 3:
		return strings.TrimSuffix(value, "es")
	case strings.HasSuffix(value, "s") && !strings.HasSuffix(value, "ss") && len(value) > 1:
		return strings.TrimSuffix(value, "s")
	default:
		return value
	}
}

func getParams(path *openapi3.PathItem, httpMethod string) []*Param {
	operation := path.Operations()[httpMethod]
	allParams := make([]*Param, 0, len(path.Parameters))

	var total openapi3.Parameters
	total = append(total, path.Parameters...)
	total = append(total, operation.Parameters...)

	for _, p := range total {
		if p.Value != nil && !sharedIgnored(p.Value.Extensions) {
			t := "string"
			tn := "\"\""
			if p.Value.Schema != nil && p.Value.Schema.Value != nil && p.Value.Schema.Value.Type != nil {
				if p.Value.Schema.Value.Type.Is("boolean") {
					t = "bool"
					tn = "false"
				} else if p.Value.Schema.Value.Type.Is("integer") {
					t = "int64"
					tn = "0"
				} else if p.Value.Schema.Value.Type.Is("number") {
					t = "float64"
					tn = "0.0"
				}
			}

			// Resolved here, not at render time, so there is nothing to skip further down.
			var enum []string
			var format string
			if t == "string" && p.Value.Schema != nil {
				noValidate := extBool(extValueOrNil(p.Value.Extensions, extKeyNoValidate))
				if !noValidate {
					enum = enumStrings(p.Value.Schema.Value)
				}
				if effective, _ := effectiveBodySchema(p.Value.Schema.Value); effective != nil {
					// date-time is not a stricter-than-API constraint — it only
					// widens input — so it survives the opt-out.
					if !noValidate || effective.Format == "date-time" {
						format = effective.Format
					}
				}
			}

			cliName := slug(p.Value.Name)
			if name := extString(p.Value.Extensions, extKeyName); name != "" {
				cliName = name
			}

			description := p.Value.Description
			if d, ok := extValue(p.Value.Extensions, extKeyDescription); ok {
				description = extStr(d)
			}
			// A parameter description reaches the user as one line of flag
			// usage or one bullet of the Arguments section, so a spec's
			// paragraph breaks would split the line or start a second bullet.
			description = strings.Join(strings.Fields(description), " ")

			allParams = append(allParams, &Param{
				Name:        p.Value.Name,
				CLIName:     cliName,
				GoName:      toGoName("param "+cliName, false),
				Description: description,
				In:          p.Value.In,
				Required:    p.Value.Required,
				Type:        t,
				TypeNil:     tn,
				Enum:        enum,
				Format:      format,
			})
		}
	}

	return allParams
}

func getRequiredParams(allParams []*Param) []*Param {
	required := make([]*Param, 0)

	for _, param := range allParams {
		if param.Required || param.In == "path" {
			required = append(required, param)
		}
	}

	return required
}

func getOptionalParams(allParams []*Param) []*Param {
	optional := make([]*Param, 0)

	for _, param := range allParams {
		if !param.Required && param.In != "path" {
			optional = append(optional, param)
		}
	}

	return optional
}

// requestInfo describes the request body of an operation for one media type.
type requestInfo struct {
	mediaType   string
	summary     string
	examples    []interface{}
	exampleBody string
	bodyFields  []*BodyField
}

func getRequestInfo(op *openapi3.Operation) requestInfo {
	mts := make(map[string]requestInfo)

	if op.RequestBody != nil && op.RequestBody.Value != nil {
		for mt, item := range op.RequestBody.Value.Content {
			var summary string
			var examples []interface{}
			var bodyFields []*BodyField

			if item.Schema != nil && item.Schema.Value != nil {
				summary = summarizeRequestSchema(mt, item.Schema.Value)
				bodyFields = getBodyFields(item.Schema.Value)
			} else {
				summary = summarizeRequestSchema(mt, nil)
			}

			if item.Example != nil {
				examples = append(examples, item.Example)
			} else if len(item.Examples) > 0 {
				// Examples is a map, so pick by sorted name rather than
				// whichever key Go's randomized iteration happens to yield —
				// otherwise regenerating from an unchanged spec produces a
				// different example every time.
				names := make([]string, 0, len(item.Examples))
				for name := range item.Examples {
					names = append(names, name)
				}
				sort.Strings(names)

				for _, name := range names {
					if ex := item.Examples[name]; ex != nil && ex.Value != nil && ex.Value.Value != nil {
						examples = append(examples, ex.Value.Value)
						break
					}
				}
			}

			mts[mt] = requestInfo{
				mediaType:   mt,
				summary:     summary,
				examples:    examples,
				exampleBody: buildExampleBody(mt, item),
				bodyFields:  bodyFields,
			}
		}
	}

	// Walk media types in a stable order for the same reason.
	mediaTypes := make([]string, 0, len(mts))
	for mt := range mts {
		mediaTypes = append(mediaTypes, mt)
	}
	sort.Strings(mediaTypes)

	// Prefer JSON.
	for _, mt := range mediaTypes {
		if strings.Contains(mt, "json") {
			return mts[mt]
		}
	}

	// Fall back to YAML next.
	for _, mt := range mediaTypes {
		if strings.Contains(mt, "yaml") {
			return mts[mt]
		}
	}

	// Last resort: return the first we find!
	for _, mt := range mediaTypes {
		return mts[mt]
	}

	return requestInfo{}
}

// renamedFlag records a generated flag that had to be renamed to avoid a
// collision, so the change can be surfaced in help text and generator output.
type renamedFlag struct {
	Field string
	From  string
	To    string
}

// reserveGeneratedFlagNames renames generated flags whose names would collide
// with a global flag, a cobra built-in, or another flag on the same command.
//
// pflag's AddFlagSet skips any flag whose name a command already uses, and
// cobra merges the root's persistent flags through it. A body field named
// `profile` therefore does not merely win the long name — it drops the global
// `--profile` from that command entirely, and a global's shorthand goes with
// it, so `-o` stops resolving on a command with an `output-format` field.
// Duplicating a request-body flag such as `--stdin` is worse still: pflag
// panics when the flag is registered twice.
//
// Only optional params and body fields become flags. Required params are
// positional arguments and keep their names. Body fields are reserved first
// because the generated command registers them first.
func reserveGeneratedFlagNames(bodyFields []*BodyField, optionalParams []*Param) []renamedFlag {
	taken := map[string]bool{}
	var renamed []renamedFlag

	claim := func(prefix, name string) string {
		resolved := bartolocli.ResolveGeneratedFlagName(prefix, name)
		if taken[resolved] {
			// Two sources want the same flag. Prefix (or further qualify) until
			// the name is free, since registering it twice would panic.
			resolved = prefix + "-" + name
			for i := 2; taken[resolved]; i++ {
				resolved = fmt.Sprintf("%s-%s-%d", prefix, name, i)
			}
		}
		taken[resolved] = true
		return resolved
	}

	// Say why a flag moved, right where the user reads the flag list. The
	// runtime adds the same note for CLIs generated before this fix; it only
	// does so when it has to rename, so the note is never duplicated here.
	note := func(kind, name, from string) string {
		if _, reserved := bartolocli.ReservedFlagName(from); reserved {
			return fmt.Sprintf(" (%s %q, renamed to keep the reserved --%s flag available)", kind, name, from)
		}
		return fmt.Sprintf(" (%s %q, renamed to avoid a duplicate --%s flag)", kind, name, from)
	}

	for _, field := range bodyFields {
		resolved := claim("body", field.CLIName)
		if resolved != field.CLIName {
			renamed = append(renamed, renamedFlag{Field: field.Name, From: field.CLIName, To: resolved})
			description := strings.TrimSpace(field.Description)
			if description == "" {
				description = field.Name
			}
			field.Description = description + note("body field", field.Name, field.CLIName)
			field.CLIName = resolved
			field.GoName = toGoName("body "+resolved, false)
		}
	}

	for _, param := range optionalParams {
		resolved := claim("param", param.CLIName)
		if resolved != param.CLIName {
			renamed = append(renamed, renamedFlag{Field: param.Name, From: param.CLIName, To: resolved})
			param.Description = strings.TrimSpace(param.Description) + note("parameter", param.Name, param.CLIName)
			param.CLIName = resolved
			param.GoName = toGoName("param "+resolved, false)
		}
	}

	return renamed
}

func getBodyFields(schema *openapi3.Schema) []*BodyField {
	schema = mergeAllOf(schema)
	if schema == nil {
		return nil
	}

	if !schema.Type.Is("object") && len(schema.Properties) == 0 {
		return nil
	}

	fields := make([]*BodyField, 0, len(schema.Properties))
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		ref := schema.Properties[name]
		if ref == nil || ref.Value == nil {
			continue
		}
		if sharedIgnored(ref.Value.Extensions) {
			continue
		}

		fieldType := bodyFieldType(ref.Value)
		if fieldType == "" {
			continue
		}

		cliName := slug(name)
		if name := extString(ref.Value.Extensions, extKeyName); name != "" {
			cliName = name
		}
		description := ref.Value.Description
		if d, ok := extValue(ref.Value.Extensions, extKeyDescription); ok {
			description = extStr(d)
		}

		fields = append(fields, &BodyField{
			Name:        name,
			CLIName:     cliName,
			GoName:      toGoName("body "+cliName, false),
			Description: description,
			Type:        fieldType,
			Enum:        bodyFieldEnum(ref.Value, fieldType),
		})
	}

	return fields
}

func bodyFieldType(schema *openapi3.Schema) string {
	if schema == nil {
		return ""
	}

	effective, nullable := effectiveBodySchema(schema)
	if effective == nil {
		// Polymorphic shapes (multi-branch oneOf/anyOf) fall back to a JSON
		// string flag so users can still drive them from the CLI. When one of
		// the branches is a plain string (e.g. `model: string | object`), use a
		// flag that accepts a bare string OR JSON, so users don't have to
		// double-quote scalar values like model IDs.
		if unionHasStringBranch(schema) {
			return "json-or-string"
		}
		return "json"
	}
	if effective.Type == nil {
		return "json"
	}

	if effective.Type.Is("array") {
		if effective.Items == nil || effective.Items.Value == nil {
			return "json"
		}
		itemEffective, _ := effectiveBodySchema(effective.Items.Value)
		itemBase := scalarType(itemEffective)
		if itemBase == "" {
			// Arrays of objects, unions, etc. → JSON string flag.
			return "json"
		}
		return itemBase + "-slice"
	}

	if effective.Type.Is("object") {
		if len(effective.Properties) > 0 {
			// Nested objects: expose as a JSON string flag rather than
			// silently dropping the field.
			return "json"
		}
		ap := effective.AdditionalProperties
		if ap.Schema != nil && ap.Schema.Value != nil {
			apEffective, _ := effectiveBodySchema(ap.Schema.Value)
			if apEffective == nil || apEffective.Type == nil || apEffective.Type.Is("string") {
				return "string-map"
			}
			return "json"
		}
		if ap.Has != nil && *ap.Has {
			return "string-map"
		}
		// Free-form object (no properties, no additionalProperties): JSON.
		return "json"
	}

	if len(effective.Enum) > 0 && effective.Type.Is("string") {
		if nullable {
			return "string-nullable"
		}
		return "enum-string"
	}

	base := scalarType(effective)
	if base == "" {
		return "json"
	}
	if base == "string" && isDateTimeSchema(effective, schema) {
		// Nullable keeps both: NormalizeDateTime always rejects the "null"
		// sentinel, so checking it first cannot shadow a timestamp.
		if nullable {
			return "datetime-nullable"
		}
		return "datetime"
	}
	if nullable {
		return base + "-nullable"
	}
	return base
}

// isDateTimeSchema takes the original too: effectiveBodySchema returns the
// branch of a single-branch anyOf/oneOf, and the wrapper may hold the `format`.
func isDateTimeSchema(effective, original *openapi3.Schema) bool {
	for _, schema := range []*openapi3.Schema{effective, original} {
		if schema != nil && schema.Format == "date-time" {
			return true
		}
	}
	return false
}

// unionHasStringBranch reports whether a multi-branch `oneOf`/`anyOf` schema has
// at least one branch that resolves to a plain string scalar. Used to decide
// whether a polymorphic body field should accept a bare string in addition to
// JSON.
func unionHasStringBranch(schema *openapi3.Schema) bool {
	if schema == nil {
		return false
	}
	branches := make([]*openapi3.SchemaRef, 0, len(schema.AnyOf)+len(schema.OneOf))
	branches = append(branches, schema.AnyOf...)
	branches = append(branches, schema.OneOf...)
	for _, br := range branches {
		if br == nil || br.Value == nil {
			continue
		}
		inner, _ := effectiveBodySchema(br.Value)
		if scalarType(inner) == "string" {
			return true
		}
	}
	return false
}

// mergeAllOf flattens an `allOf`, `oneOf`, or `anyOf` request-body composition
// into a synthetic object schema whose Properties are the union of every
// branch (host's own properties win on conflict). For `allOf` the Required
// list is the union of all branches; for `oneOf`/`anyOf` it is the
// intersection (a field is only universally required if every branch
// requires it). When the same string-enum property appears in several
// `oneOf`/`anyOf` branches with different values — the discriminated-union
// `type`/`strategy` pattern — the merged property carries the union of all
// branch enums instead of just the first branch's values. Returns the schema
// unchanged when there is nothing to merge.
func mergeAllOf(schema *openapi3.Schema) *openapi3.Schema {
	if schema == nil || (len(schema.AllOf) == 0 && len(schema.OneOf) == 0 && len(schema.AnyOf) == 0) {
		return schema
	}

	m := &schemaMerger{
		merged: &openapi3.Schema{
			Type:       &openapi3.Types{"object"},
			Properties: openapi3.Schemas{},
		},
		protected: map[string]bool{},
	}

	// Host's own properties win on conflict.
	m.collect(schema)
	required := nameSet(schema.Required)

	// `allOf` branches: union properties + union required.
	for _, branch := range schema.AllOf {
		if flat := flattenBranch(branch); flat != nil {
			m.collect(flat)
			for _, name := range flat.Required {
				required[name] = true
			}
		}
	}

	// `oneOf`/`anyOf` branches: union properties + intersection of required
	// (only fields required by every branch are universally required).
	union := append(append(openapi3.SchemaRefs{}, schema.OneOf...), schema.AnyOf...)
	for name := range m.collectUnion(union) {
		required[name] = true
	}

	merged := m.merged
	if len(required) > 0 {
		merged.Required = slices.Sorted(maps.Keys(required))
	}
	if schema.Description != "" {
		merged.Description = schema.Description
	}
	if schema.Extensions != nil {
		merged.Extensions = schema.Extensions
	}

	return merged
}

// schemaMerger accumulates the flattened object schema for mergeAllOf.
type schemaMerger struct {
	merged *openapi3.Schema
	// protected holds properties collected from the host schema or an `allOf`
	// branch: authoritative constraints that union branches never widen.
	protected map[string]bool
}

// collect adds src's properties that are not already present (first wins) and
// protects them.
func (m *schemaMerger) collect(src *openapi3.Schema) {
	for name, ref := range src.Properties {
		if _, exists := m.merged.Properties[name]; !exists {
			m.merged.Properties[name] = ref
			m.protected[name] = true
		}
	}
	m.collectAdditionalProperties(src)
}

// collectUnion adds the `oneOf`/`anyOf` branches and returns the properties
// every branch requires. A value only has to satisfy one branch, so when the
// same property collides across branches its string enums are unioned rather
// than first-wins. Pure null branches are skipped so they don't wipe out the
// required set.
func (m *schemaMerger) collectUnion(branches openapi3.SchemaRefs) map[string]bool {
	var required map[string]bool
	for _, branch := range branches {
		flat := flattenBranch(branch)
		if flat == nil || flat.Type.Is("null") {
			continue
		}
		m.collectBranch(flat)
		required = intersectNames(required, flat.Required)
	}
	return required
}

// collectBranch is collect for a union branch: a new property is added
// unprotected, and an unprotected collision is widened by unionEnumSchema.
func (m *schemaMerger) collectBranch(src *openapi3.Schema) {
	for name, ref := range src.Properties {
		existing, exists := m.merged.Properties[name]
		switch {
		case !exists:
			m.merged.Properties[name] = ref
		case !m.protected[name]:
			if widened := unionEnumSchema(existing, ref); widened != nil {
				m.merged.Properties[name] = widened
			}
		}
	}
	m.collectAdditionalProperties(src)
}

// collectAdditionalProperties keeps the first additionalProperties schema and
// the first explicit allow/deny seen.
func (m *schemaMerger) collectAdditionalProperties(src *openapi3.Schema) {
	ap := &m.merged.AdditionalProperties
	if ap.Schema == nil {
		ap.Schema = src.AdditionalProperties.Schema
	}
	if ap.Has == nil {
		ap.Has = src.AdditionalProperties.Has
	}
}

// flattenBranch merges one composition branch, or returns nil for an empty one.
func flattenBranch(branch *openapi3.SchemaRef) *openapi3.Schema {
	if branch == nil || branch.Value == nil {
		return nil
	}
	return mergeAllOf(branch.Value)
}

func nameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// intersectNames narrows set to the names also in names. A nil set (no branch
// seen yet) starts from names.
func intersectNames(set map[string]bool, names []string) map[string]bool {
	branch := nameSet(names)
	if set == nil {
		return branch
	}
	for name := range set {
		if !branch[name] {
			delete(set, name)
		}
	}
	return set
}

// unionEnumSchema resolves a property collision between two `oneOf`/`anyOf`
// branches. When both sides are string schemas and the existing one carries
// an enum, it returns a replacement ref: the union of both enums when the
// incoming side is also enumerated, or a plain string (enum dropped) when the
// incoming branch accepts any string — enforcing a partial enum would reject
// values some branch allows. Returns nil when the existing ref should be kept
// as-is. The replacement wraps a copy; the source schemas are shared `$ref`
// components and are never mutated.
func unionEnumSchema(existing, incoming *openapi3.SchemaRef) *openapi3.SchemaRef {
	if existing == nil || incoming == nil {
		return nil
	}
	exEff, exNullable := effectiveBodySchema(existing.Value)
	inEff, inNullable := effectiveBodySchema(incoming.Value)
	if exEff == nil || inEff == nil || !exEff.Type.Is("string") || !inEff.Type.Is("string") {
		return nil
	}
	if len(exEff.Enum) == 0 {
		// Already accepts any string; nothing to widen.
		return nil
	}

	clone := *exEff
	clone.Nullable = clone.Nullable || exNullable || inNullable
	clone.Description = cmp.Or(clone.Description, inEff.Description)

	if len(inEff.Enum) == 0 {
		clone.Enum = nil
		return &openapi3.SchemaRef{Value: &clone}
	}

	union := uniqueEnumValues(slices.Concat(exEff.Enum, inEff.Enum))
	if len(union) == len(exEff.Enum) {
		// No new values; keep the existing ref untouched.
		return nil
	}
	clone.Enum = union
	return &openapi3.SchemaRef{Value: &clone}
}

// uniqueEnumValues drops repeated enum values, keeping first occurrences. Values
// compare by type and printed form, so 1 and "1" stay distinct.
func uniqueEnumValues(values []interface{}) []interface{} {
	seen := map[string]bool{}
	out := make([]interface{}, 0, len(values))
	for _, raw := range values {
		key := fmt.Sprintf("%T:%v", raw, raw)
		if !seen[key] {
			seen[key] = true
			out = append(out, raw)
		}
	}
	return out
}

func bodyFieldEnum(schema *openapi3.Schema, fieldType string) []string {
	if fieldType != "enum-string" {
		return nil
	}

	return enumStrings(schema)
}

// enumStrings returns a schema's string enum values, resolving the nullability
// wrappers first so `anyOf: [{enum: [...]}, {type: null}]` is not mistaken for
// an unconstrained value. Non-string members are dropped: the generated flags
// they would constrain are strings.
func enumStrings(schema *openapi3.Schema) []string {
	effective, _ := effectiveBodySchema(schema)
	if effective == nil || len(effective.Enum) == 0 {
		return nil
	}

	values := make([]string, 0, len(effective.Enum))
	for _, raw := range effective.Enum {
		if s, ok := raw.(string); ok {
			values = append(values, s)
		}
	}

	return values
}

func scalarType(schema *openapi3.Schema) string {
	if schema == nil || schema.Type == nil {
		return ""
	}
	switch {
	case schema.Type.Is("string"):
		return "string"
	case schema.Type.Is("boolean"):
		return "bool"
	case schema.Type.Is("integer"):
		return "int64"
	case schema.Type.Is("number"):
		return "float64"
	default:
		return ""
	}
}

// effectiveBodySchema collapses the common nullability shapes (`nullable: true`,
// JSON Schema `type: [..., "null"]`, `anyOf`/`oneOf` with a single non-null
// branch) and returns the schema that actually describes the field's value
// alongside whether the field accepts null.
func effectiveBodySchema(schema *openapi3.Schema) (*openapi3.Schema, bool) {
	if schema == nil {
		return nil, false
	}

	nullable := schema.Nullable

	if schema.Type != nil && schema.Type.Includes("null") {
		nullable = true
		// JSON Schema 3.1 `type: ["integer", "null"]`: strip null so the
		// downstream Type.Is("integer") check sees a single concrete type.
		nonNull := make(openapi3.Types, 0, len(schema.Type.Slice()))
		for _, t := range schema.Type.Slice() {
			if t != "null" {
				nonNull = append(nonNull, t)
			}
		}
		clone := *schema
		clone.Type = &nonNull
		schema = &clone
	}

	branches := make([]*openapi3.SchemaRef, 0, len(schema.AnyOf)+len(schema.OneOf))
	branches = append(branches, schema.AnyOf...)
	branches = append(branches, schema.OneOf...)

	if len(branches) > 0 {
		var nonNull *openapi3.Schema
		nonNullCount := 0
		for _, br := range branches {
			if br == nil || br.Value == nil {
				continue
			}
			if br.Value.Type != nil && br.Value.Type.Is("null") {
				nullable = true
				continue
			}
			nonNull = br.Value
			nonNullCount++
		}
		if nonNullCount == 1 && nonNull != nil {
			inner, innerNullable := effectiveBodySchema(nonNull)
			if inner != nil {
				return inner, nullable || innerNullable
			}
		}
		if schema.Type == nil || len(schema.Type.Slice()) == 0 {
			return nil, nullable
		}
	}

	return schema, nullable
}

// collectSecuritySchemeNames returns every distinct security scheme name
// referenced by the API's top-level security and any operation-level security.
// `ordered` preserves the order those names first appear in the top-level
// api.Security list (used to pick the default auth type); `all` is the full set
// including operation-only references.
func collectSecuritySchemeNames(api *openapi3.T) (ordered []string, all []string) {
	if api == nil {
		return nil, nil
	}

	seenAll := make(map[string]bool)
	seenOrdered := make(map[string]bool)

	addOrdered := func(reqs openapi3.SecurityRequirements) {
		for _, req := range reqs {
			// A requirement object can list several schemes; sort its keys for
			// determinism (Go map iteration order is random).
			names := make([]string, 0, len(req))
			for name := range req {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if !seenOrdered[name] {
					seenOrdered[name] = true
					ordered = append(ordered, name)
				}
			}
		}
	}
	addAll := func(reqs openapi3.SecurityRequirements) {
		for _, req := range reqs {
			for name := range req {
				if !seenAll[name] {
					seenAll[name] = true
					all = append(all, name)
				}
			}
		}
	}

	addOrdered(api.Security)
	addAll(api.Security)
	if api.Paths != nil {
		for _, item := range api.Paths.Map() {
			for _, operation := range item.Operations() {
				if operation.Security != nil {
					addAll(*operation.Security)
				}
			}
		}
	}

	sort.Strings(all)
	return ordered, all
}

// buildAuthInit emits the runtime auth registration code injected into the
// generated client's initGeneratedRuntime(). It collects every recognized
// security scheme (top-level + operation-level), emits one typed init per
// scheme, registers a default auth type, and reports which runtime packages the
// emitted code references so the template can import them conditionally.
func buildAuthInit(api *openapi3.T) (authInit string, needsAPIKey bool, needsOAuth bool, schemes []*AuthScheme) {
	if api == nil || api.Components == nil || api.Components.SecuritySchemes == nil {
		return "", false, false, nil
	}

	ordered, all := collectSecuritySchemeNames(api)
	if len(all) == 0 {
		return "", false, false, nil
	}

	var lines []string
	var emittedNames []string

	for _, name := range all {
		schemeRef := api.Components.SecuritySchemes[name]
		if schemeRef == nil || schemeRef.Value == nil {
			continue
		}
		scheme := schemeRef.Value
		line, kind, tokenURL, pkg := authInitLine(name, scheme)
		if line == "" {
			// Unsupported scheme: record it for the README as manual setup.
			schemes = append(schemes, &AuthScheme{
				Name:      name,
				Kind:      unsupportedSchemeKind(scheme),
				Supported: false,
				Manual:    unsupportedSchemeReason(scheme),
			})
			continue
		}

		lines = append(lines, line)
		emittedNames = append(emittedNames, name)
		switch pkg {
		case "apikey":
			needsAPIKey = true
		case "oauth":
			needsOAuth = true
		}
		schemes = append(schemes, &AuthScheme{
			Name:      name,
			Kind:      kind,
			TokenURL:  tokenURL,
			Supported: true,
		})
	}

	if len(emittedNames) == 0 {
		// Only unsupported schemes were present. Keep AuthInit empty (unchanged
		// behavior) but retain the manual-setup notes for the README.
		return "", false, false, schemes
	}

	// Pick the default auth type: the first scheme in the top-level security
	// order that we actually emitted, else the first emitted scheme (already
	// sorted, since `all` is sorted).
	defaultName := ""
	for _, name := range ordered {
		if containsString(emittedNames, name) {
			defaultName = name
			break
		}
	}
	if defaultName == "" {
		defaultName = emittedNames[0]
	}

	lines = append(lines, fmt.Sprintf("bartolocli.SetDefaultAuthType(%q)", defaultName))

	for _, s := range schemes {
		if s.Name == defaultName {
			s.Default = true
		}
	}

	return strings.Join(lines, "\n"), needsAPIKey, needsOAuth, schemes
}

// authInitLine returns the runtime init call for one security scheme, a
// human-readable kind, an optional OAuth token URL, and which runtime package
// ("apikey" or "oauth") the call references. It returns an empty line for
// unsupported schemes.
func authInitLine(name string, scheme *openapi3.SecurityScheme) (line, kind, tokenURL, pkg string) {
	switch scheme.Type {
	case "apiKey":
		switch scheme.In {
		case "header":
			return fmt.Sprintf("apikey.InitWithType(%q, %q, apikey.LocationHeader)", name, scheme.Name), "API key (header)", "", "apikey"
		case "query":
			return fmt.Sprintf("apikey.InitWithType(%q, %q, apikey.LocationQuery)", name, scheme.Name), "API key (query)", "", "apikey"
		case "cookie":
			return fmt.Sprintf("apikey.InitWithType(%q, %q, apikey.LocationCookie)", name, scheme.Name), "API key (cookie)", "", "apikey"
		}
	case "http":
		if strings.EqualFold(scheme.Scheme, "bearer") {
			return fmt.Sprintf("apikey.InitBearerWithType(%q, \"Authorization\")", name), "Bearer token", "", "apikey"
		}
	case "oauth2":
		if scheme.Flows != nil {
			if flow := scheme.Flows.ClientCredentials; flow != nil {
				return fmt.Sprintf("oauth.InitClientCredentialsWithType(%q, %q%s)", name, flow.TokenURL, oauthScopesArg(flow)),
					"OAuth2 client credentials", flow.TokenURL, "oauth"
			}
			if flow := scheme.Flows.AuthorizationCode; flow != nil {
				return fmt.Sprintf("oauth.InitAuthCodeWithType(%q, \"\", %q, %q%s)", name, flow.AuthorizationURL, flow.TokenURL, oauthScopesArg(flow)),
					"OAuth2 authorization code", flow.TokenURL, "oauth"
			}
		}
	}

	return "", "", "", ""
}

// oauthScopesArg renders the trailing `, oauth.Scopes(...)` option for an OAuth
// flow, or "" when the flow declares no scopes.
func oauthScopesArg(flow *openapi3.OAuthFlow) string {
	if flow == nil || len(flow.Scopes) == 0 {
		return ""
	}
	scopes := make([]string, 0, len(flow.Scopes))
	for scope := range flow.Scopes {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	quoted := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		quoted = append(quoted, fmt.Sprintf("%q", scope))
	}
	return ", oauth.Scopes(" + strings.Join(quoted, ", ") + ")"
}

func unsupportedSchemeKind(scheme *openapi3.SecurityScheme) string {
	switch scheme.Type {
	case "openIdConnect":
		return "OpenID Connect"
	case "http":
		if strings.EqualFold(scheme.Scheme, "basic") {
			return "HTTP Basic"
		}
		return "HTTP " + scheme.Scheme
	case "oauth2":
		return "OAuth2 (unsupported flow)"
	}
	return scheme.Type
}

func unsupportedSchemeReason(scheme *openapi3.SecurityScheme) string {
	switch scheme.Type {
	case "openIdConnect":
		return "OpenID Connect discovery is not auto-wired; configure the token endpoint manually."
	case "http":
		if strings.EqualFold(scheme.Scheme, "basic") {
			return "HTTP Basic auth is not auto-wired; set credentials manually."
		}
		return "This HTTP auth scheme is not auto-wired; configure it manually."
	case "oauth2":
		return "Only clientCredentials and authorizationCode OAuth2 flows are auto-wired; configure this flow manually."
	}
	return "This security scheme is not auto-wired; configure it manually."
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func summarizeRequestSchema(mediaType string, schema *openapi3.Schema) string {
	lines := []string{
		fmt.Sprintf("Request body: `%s`. Provide it via stdin or CLI shorthand.", mediaType),
		"Run `help-input` for body syntax details.",
	}

	schema = mergeAllOf(schema)
	if schema == nil {
		return strings.Join(lines, "\n")
	}

	if len(schema.Properties) == 0 {
		summaryType := schemaTypeSummary(schema)
		if summaryType != "" {
			lines = append(lines, "", "Top-level type: `"+summaryType+"`")
		}
		if len(schema.Required) > 0 {
			lines = append(lines, "", "Required fields: "+formatRequiredFields(schema.Required))
		}
		return strings.Join(lines, "\n")
	}

	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)

	lines = append(lines, "", "Top-level fields:")
	limit := 8
	for i, name := range names {
		if i >= limit {
			lines = append(lines, fmt.Sprintf("- ... and %d more fields", len(names)-limit))
			break
		}

		required := ""
		if contains(schema.Required, name) {
			required = ", required"
		}

		fieldType := schemaTypeSummary(schema.Properties[name].Value)
		if fieldType == "" {
			fieldType = "value"
		}

		lines = append(lines, fmt.Sprintf("- `%s` (%s%s)", name, fieldType, required))
	}

	if len(schema.Required) > 0 {
		lines = append(lines, "", "Required fields: "+formatRequiredFields(schema.Required))
	}

	return strings.Join(lines, "\n")
}

func schemaTypeSummary(schema *openapi3.Schema) string {
	if schema == nil {
		return ""
	}

	if schema.Type != nil && len(schema.Type.Slice()) > 0 {
		return strings.Join(schema.Type.Slice(), " | ")
	}

	switch {
	case len(schema.AnyOf) > 0:
		return "anyOf"
	case len(schema.OneOf) > 0:
		return "oneOf"
	case len(schema.AllOf) > 0:
		return "allOf"
	case schema.Items != nil:
		return "array"
	case len(schema.Properties) > 0:
		return "object"
	default:
		return ""
	}
}

func formatRequiredFields(fields []string) string {
	if len(fields) == 0 {
		return ""
	}

	sorted := append([]string{}, fields...)
	sort.Strings(sorted)

	quoted := make([]string, 0, len(sorted))
	for _, field := range sorted {
		quoted = append(quoted, "`"+field+"`")
	}

	return strings.Join(quoted, ", ")
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

func writeFormattedFile(filename string, data []byte) {
	dir := filepath.Dir(filename)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			panic(err)
		}
	}

	formatted, errFormat := format.Source(data)
	if errFormat != nil {
		formatted = data
	}

	err := ioutil.WriteFile(filename, formatted, 0600)
	if errFormat != nil {
		panic(errFormat)
	} else if err != nil {
		panic(err)
	}
}

func writeFileIfMissing(filename string, data []byte, mode os.FileMode) {
	if _, err := os.Stat(filename); err == nil {
		return
	}

	dir := filepath.Dir(filename)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			panic(err)
		}
	}

	if err := ioutil.WriteFile(filename, data, mode); err != nil {
		panic(err)
	}
}

func writeTemplateFileIfMissing(templateName string, filename string, mode os.FileMode, data interface{}) {
	sb := renderTemplate(templateName, nil, data)
	writeFileIfMissing(filename, []byte(sb), mode)
}

func loadTemplate(name string) []byte {
	data, err := templateFS.ReadFile(name)
	if err != nil {
		panic(err)
	}

	return data
}

func renderTemplate(name string, funcs template.FuncMap, data interface{}, partials ...string) string {
	templateData := loadTemplate(name)
	tmpl := template.New(filepath.Base(name))
	if funcs != nil {
		tmpl = tmpl.Funcs(funcs)
	}

	// Same namespace, so the partials' `define` blocks are callable from the named template.
	for _, partial := range partials {
		if _, err := tmpl.Parse(string(loadTemplate(partial))); err != nil {
			panic(err)
		}
	}

	parsed, err := tmpl.Parse(string(templateData))
	if err != nil {
		panic(err)
	}

	var sb strings.Builder
	if err := parsed.Execute(&sb, data); err != nil {
		panic(err)
	}

	return sb.String()
}

func writeProjectConfig(config *ProjectConfig) {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		panic(err)
	}

	if err := ioutil.WriteFile(projectConfigFilename, data, 0600); err != nil {
		panic(err)
	}
}

func loadOpenAPIDocument(data []byte) (*openapi3.T, error) {
	loader := openapi3.NewLoader()

	// kin-openapi keeps OpenAPI 3.1 numeric exclusive bounds out of Schema.Min/Max,
	// so rewrite them to the 3.0 boolean form before loading.
	if normalized, changed, err := normalizeOpenAPI31Data(data); err == nil && changed {
		if swagger, err := loader.LoadFromData(normalized); err == nil {
			return swagger, nil
		}
	}

	return loader.LoadFromData(data)
}

func normalizeOpenAPI31Data(data []byte) ([]byte, bool, error) {
	var decoded interface{}
	if err := yamlv3.Unmarshal(data, &decoded); err != nil {
		return nil, false, err
	}

	normalized, changed := normalizeOpenAPI31Value(decoded)
	if !changed {
		return data, false, nil
	}

	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, false, err
	}

	return encoded, true, nil
}

func normalizeOpenAPI31Value(value interface{}) (interface{}, bool) {
	switch typed := value.(type) {
	case map[string]interface{}:
		changed := false
		normalized := make(map[string]interface{}, len(typed))
		for key, raw := range typed {
			next, nextChanged := normalizeOpenAPI31Value(raw)
			normalized[key] = next
			changed = changed || nextChanged
		}

		if numeric, ok := numericJSONValue(normalized["exclusiveMinimum"]); ok {
			if _, exists := normalized["minimum"]; !exists {
				normalized["minimum"] = numeric
			}
			normalized["exclusiveMinimum"] = true
			changed = true
		}

		if numeric, ok := numericJSONValue(normalized["exclusiveMaximum"]); ok {
			if _, exists := normalized["maximum"]; !exists {
				normalized["maximum"] = numeric
			}
			normalized["exclusiveMaximum"] = true
			changed = true
		}

		return normalized, changed
	case map[interface{}]interface{}:
		normalized := make(map[string]interface{}, len(typed))
		changed := false
		for key, raw := range typed {
			next, nextChanged := normalizeOpenAPI31Value(raw)
			normalized[fmt.Sprint(key)] = next
			changed = changed || nextChanged
		}

		next, nextChanged := normalizeOpenAPI31Value(normalized)
		return next, changed || nextChanged
	case []interface{}:
		changed := false
		normalized := make([]interface{}, len(typed))
		for i, raw := range typed {
			next, nextChanged := normalizeOpenAPI31Value(raw)
			normalized[i] = next
			changed = changed || nextChanged
		}
		return normalized, changed
	default:
		return value, false
	}
}

func numericJSONValue(value interface{}) (interface{}, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return typed, true
	case int16:
		return typed, true
	case int32:
		return typed, true
	case int64:
		return typed, true
	case uint:
		return typed, true
	case uint8:
		return typed, true
	case uint16:
		return typed, true
	case uint32:
		return typed, true
	case uint64:
		return typed, true
	case float32:
		return typed, true
	case float64:
		return typed, true
	default:
		return nil, false
	}
}

func loadProjectConfig() *ProjectConfig {
	data, err := ioutil.ReadFile(projectConfigFilename)
	if err != nil {
		return nil
	}

	var config ProjectConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}

	if config.SerializationFormat == "" {
		config.SerializationFormat = config.LegacyDefaultOutputFormat
	}
	config.LegacyDefaultOutputFormat = ""

	return &config
}

func getCommandName() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "my-cli"
	}

	return slug(filepath.Base(cwd))
}

func isInteractiveInput() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

func isInteractiveOutput() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

func shouldColorizeWizard() bool {
	if !isInteractiveOutput() {
		return false
	}

	term := strings.TrimSpace(os.Getenv("TERM"))
	return term != "" && term != "dumb"
}

func wizardStyle(enabled bool, code string, value string) string {
	if !enabled {
		return value
	}

	return "\033[" + code + "m" + value + "\033[0m"
}

func wizardTitle(enabled bool, value string) string {
	return wizardStyle(enabled, "1;38;5;45", value)
}

func wizardStep(enabled bool, value string) string {
	return wizardStyle(enabled, "1;38;5;81", value)
}

func wizardAccent(enabled bool, value string) string {
	return wizardStyle(enabled, "1;38;5;114", value)
}

func wizardMuted(enabled bool, value string) string {
	return wizardStyle(enabled, "38;5;244", value)
}

func wizardError(enabled bool, value string) string {
	return wizardStyle(enabled, "1;38;5;203", value)
}

func wizardAskOptions(color bool) []survey.AskOpt {
	return []survey.AskOpt{
		survey.WithIcons(func(icons *survey.IconSet) {
			icons.Question.Text = ">"
			icons.Question.Format = "cyan+b"
			icons.Help.Text = "i"
			icons.Help.Format = "yellow+b"
			icons.Error.Text = "x"
			icons.Error.Format = "red+b"
			icons.SelectFocus.Text = ">"
			icons.SelectFocus.Format = "green+b"
			icons.MarkedOption.Text = ">"
			icons.MarkedOption.Format = "green+b"
			icons.UnmarkedOption.Text = " "
			icons.UnmarkedOption.Format = "default"

			if !color {
				icons.Question.Format = "default"
				icons.Help.Format = "default"
				icons.Error.Format = "default"
				icons.SelectFocus.Format = "default"
				icons.MarkedOption.Format = "default"
			}
		}),
	}
}

func promptInterrupted(err error) bool {
	return err == surveyterminal.InterruptErr
}

// serializationFormats are the values accepted by `--serialization-format`: the
// generated CLIs' own list, minus `table`, which is a rendering choice and so
// cannot be what a table falls back to.
var serializationFormats = withoutTable(bartolocli.OutputFormats)

func withoutTable(formats []string) []string {
	kept := make([]string, 0, len(formats))
	for _, format := range formats {
		if format != "table" {
			kept = append(kept, format)
		}
	}
	return kept
}

func parseOutputFormat(value string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, format := range serializationFormats {
		if normalized == format {
			return format, true
		}
	}
	return "", false
}

func isValidEnvVarName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}

	for i, r := range value {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}

		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}

	return true
}

func promptSelect(question string, options []selectOption, defaultValue string, color bool) (string, error) {
	labels := make([]string, 0, len(options))
	valuesByLabel := make(map[string]string, len(options))
	defaultLabel := ""

	for _, option := range options {
		label := option.Label
		if label == "" {
			label = option.Value
		}
		labels = append(labels, label)
		valuesByLabel[label] = option.Value
		if option.Value == defaultValue {
			defaultLabel = label
		}
	}

	selected := defaultLabel
	prompt := &survey.Select{
		Message:  question,
		Options:  labels,
		Default:  defaultLabel,
		PageSize: len(labels),
		Description: func(value string, index int) string {
			if index < 0 || index >= len(options) {
				return ""
			}
			return options[index].Description
		},
	}

	if err := survey.AskOne(prompt, &selected, wizardAskOptions(color)...); err != nil {
		if promptInterrupted(err) {
			return "", fmt.Errorf("wizard cancelled")
		}
		return "", err
	}

	value := strings.TrimSpace(valuesByLabel[selected])
	if value == "" {
		value = defaultValue
	}
	return value, nil
}

// targetSelectOptions are the surface choices offered by the wizard and the
// legacy-config migration prompt. Values map to normalized target lists.
func targetSelectOptions() []selectOption {
	return []selectOption{
		{Value: "cli+mcp", Label: "CLI + MCP server (recommended)", Description: "Generate both the command-line surface and the MCP server."},
		{Value: "cli", Label: "CLI only", Description: "Generate only the command-line surface."},
		{Value: "mcp", Label: "MCP server only", Description: "Generate an MCP-first binary (bare invocation serves stdio)."},
	}
}

// targetsFromSelection maps a target-select value to a normalized target list.
func targetsFromSelection(value string) []string {
	switch value {
	case targetCLI:
		return []string{targetCLI}
	case targetMCP:
		return []string{targetMCP}
	default:
		return []string{targetCLI, targetMCP}
	}
}

// promptTargets asks the "What should this project generate?" question and
// returns the normalized target list.
func promptTargets(color bool) ([]string, error) {
	value, err := promptSelect("What should this project generate?", targetSelectOptions(), "cli+mcp", color)
	if err != nil {
		return nil, err
	}
	return targetsFromSelection(value), nil
}

// ensureConfigTargets resolves the generation targets for an existing project
// config that predates the targets field. It NEVER prompts during generate/sync
// when the config already records targets. For a legacy config (no targets):
// interactive stdin asks the surface question once and persists it; otherwise it
// defaults to both surfaces and emits a single stderr warning.
func ensureConfigTargets(config *ProjectConfig) error {
	if config == nil || len(config.Targets) > 0 {
		return nil
	}

	if isInteractiveInput() {
		color := shouldColorizeWizard()
		previousDisableColor := surveycore.DisableColor
		surveycore.DisableColor = !color
		defer func() { surveycore.DisableColor = previousDisableColor }()

		targets, err := promptTargets(color)
		if err != nil {
			return err
		}
		config.Targets = targets
		return nil
	}

	config.Targets = defaultTargets()
	fmt.Fprintln(os.Stderr, "warning: .bartolo.json has no \"targets\"; defaulting to CLI + MCP. Set \"targets\" (or run `bartolo init --targets ...`) to choose surfaces.")
	return nil
}

func printWizardHeader(color bool) {
	fmt.Println()
	fmt.Println(wizardTitle(color, "Bartolo Init Wizard"))
	fmt.Println(wizardMuted(color, "Create a new CLI scaffold with a sensible local default setup."))
	fmt.Println()
}

func printWizardStep(color bool, current int, total int, title string, hint string) {
	label := fmt.Sprintf("Step %d/%d", current, total)
	fmt.Printf("%s %s\n", wizardStep(color, label), wizardAccent(color, title))
	if hint != "" {
		fmt.Println(wizardMuted(color, hint))
	}
}

func printWizardSummary(color bool, config *ProjectConfig) {
	fmt.Println()
	fmt.Println(wizardTitle(color, "Wizard Summary"))
	fmt.Printf("%s %s\n", wizardAccent(color, "CLI name:"), config.AppName)
	if strings.TrimSpace(config.ModulePath) != "" {
		fmt.Printf("%s %s\n", wizardAccent(color, "Module path:"), config.ModulePath)
	}
	if strings.TrimSpace(config.BartoloReplacePath) != "" {
		fmt.Printf("%s %s\n", wizardAccent(color, "Local bartolo path:"), config.BartoloReplacePath)
	}
	fmt.Printf("%s %s\n", wizardAccent(color, "Env prefix:"), config.EnvPrefix)
	fmt.Printf("%s %s\n", wizardAccent(color, "API key env var:"), config.APIKeyEnvVar)
	fmt.Printf("%s %s\n", wizardAccent(color, "Serializes to:"), config.SerializationFormat)
	fmt.Printf("%s %s\n", wizardAccent(color, "Targets:"), strings.Join(config.effectiveTargets(), ", "))
	fmt.Println()
}

func promptText(question string, defaultValue string, validate func(string) error, color bool) (string, error) {
	value := defaultValue
	opts := wizardAskOptions(color)
	if validate != nil {
		opts = append(opts, survey.WithValidator(func(ans interface{}) error {
			return validate(strings.TrimSpace(fmt.Sprint(ans)))
		}))
	}

	if err := survey.AskOne(&survey.Input{
		Message: question,
		Default: defaultValue,
	}, &value, opts...); err != nil {
		if promptInterrupted(err) {
			return "", fmt.Errorf("wizard cancelled")
		}
		return "", err
	}

	return strings.TrimSpace(value), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func defaultModulePath(name string) string {
	modulePath := slug(name)
	if modulePath == "" {
		modulePath = "my-cli"
	}
	return modulePath
}

func isValidModulePath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return !strings.ContainsAny(value, " \t\r\n")
}

func resolveInitConfig(cmd *cobra.Command, args []string) (*ProjectConfig, error) {
	name := ""
	if len(args) > 0 {
		name = strings.TrimSpace(args[0])
	}

	modulePath, _ := cmd.Flags().GetString("module-path")
	modulePath = strings.TrimSpace(modulePath)

	bartoloReplacePath, _ := cmd.Flags().GetString("bartolo-path")
	bartoloReplacePath = strings.TrimSpace(firstNonEmpty(bartoloReplacePath, os.Getenv("BARTOLO_REPLACE_PATH")))

	apiKeyEnvVar, _ := cmd.Flags().GetString("api-key-env-var")
	apiKeyEnvVar = strings.TrimSpace(apiKeyEnvVar)

	serializationFormat, _ := cmd.Flags().GetString("serialization-format")
	// Reject an unknown format instead of quietly generating a TOON CLI.
	serializationFormat, ok := parseOutputFormat(serializationFormat)
	if !ok {
		rejected, _ := cmd.Flags().GetString("serialization-format")
		return nil, fmt.Errorf("--serialization-format: %q is not one of [%s]", rejected, strings.Join(serializationFormats, ", "))
	}

	targetsFlag, _ := cmd.Flags().GetString("targets")
	var targets []string

	interactive, _ := cmd.Flags().GetBool("interactive")
	if len(args) == 0 {
		interactive = true
	}

	if interactive {
		if !isInteractiveInput() {
			return nil, fmt.Errorf("init requires <app-name> when stdin is not interactive")
		}

		color := shouldColorizeWizard()
		previousDisableColor := surveycore.DisableColor
		surveycore.DisableColor = !color
		defer func() {
			surveycore.DisableColor = previousDisableColor
		}()

		printWizardHeader(color)

		defaultName := name
		if defaultName == "" {
			defaultName = getCommandName()
		}

		var err error
		printWizardStep(color, 1, 4, "CLI identity", "Pick the install name your users will actually type.")
		name, err = promptText("What is the name of your CLI?", defaultName, func(value string) error {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("CLI name cannot be empty")
			}
			return nil
		}, color)
		if err != nil {
			return nil, err
		}
		fmt.Println()

		envPrefix := strings.ToUpper(strings.ReplaceAll(slug(name), "-", "_"))
		if envPrefix == "" {
			envPrefix = strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		}
		if apiKeyEnvVar == "" {
			apiKeyEnvVar = envPrefix + "_API_KEY"
		}

		modulePath = firstNonEmpty(modulePath, defaultModulePath(name))

		printWizardStep(color, 2, 4, "Auth defaults", "You can keep the suggested API key env var or replace it with your own convention.")
		apiKeyEnvVar, err = promptText("API key env var", apiKeyEnvVar, func(value string) error {
			if !isValidEnvVarName(value) {
				return fmt.Errorf("%s", wizardError(color, "Use only letters, numbers, and underscores, and do not start with a number."))
			}
			return nil
		}, color)
		if err != nil {
			return nil, err
		}
		fmt.Println()

		printWizardStep(color, 3, 4, "Serialization format", "List commands render a table on a terminal. Choose what generated CLIs serialize to everywhere else. Use the arrow keys to move and Enter to confirm.")
		serializationFormat, err = promptSelect("Serialization format", []selectOption{
			{Value: "json", Label: "json", Description: "What a pipe, a script, or jq expects."},
			{Value: "yaml", Label: "yaml", Description: "Easy to scan in terminals."},
			{Value: "toon", Label: "toon", Description: "Compact serialization aimed at LLMs."},
		}, serializationFormat, color)
		if err != nil {
			return nil, err
		}
		fmt.Println()

		printWizardStep(color, 4, 4, "Generation targets", "Choose which surfaces to generate. Use the arrow keys to move and Enter to confirm.")
		targets, err = promptTargets(color)
		if err != nil {
			return nil, err
		}

		summaryEnvPrefix := strings.ToUpper(strings.ReplaceAll(slug(name), "-", "_"))
		if summaryEnvPrefix == "" {
			summaryEnvPrefix = strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		}
		printWizardSummary(color, &ProjectConfig{
			AppName:             name,
			AppVersion:          "0.1.0",
			ModulePath:          modulePath,
			BartoloReplacePath:  bartoloReplacePath,
			BartoloVersion:      bartoloVersion,
			EnvPrefix:           summaryEnvPrefix,
			SerializationFormat: serializationFormat,
			APIKeyEnvVar:        apiKeyEnvVar,
			Targets:             targets,
		})
	}

	if name == "" {
		return nil, fmt.Errorf("missing app name")
	}

	if targets == nil {
		var err error
		targets, err = parseTargetsFlag(targetsFlag)
		if err != nil {
			return nil, err
		}
	}

	envPrefix := strings.ToUpper(strings.ReplaceAll(slug(name), "-", "_"))
	if envPrefix == "" {
		envPrefix = strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	}

	modulePath = firstNonEmpty(modulePath, defaultModulePath(name))
	if !isValidModulePath(modulePath) {
		return nil, fmt.Errorf("invalid module path %q", modulePath)
	}

	apiKeyEnvVar = firstNonEmpty(apiKeyEnvVar, envPrefix+"_API_KEY")
	if !isValidEnvVarName(apiKeyEnvVar) {
		return nil, fmt.Errorf("invalid api key env var %q", apiKeyEnvVar)
	}

	return &ProjectConfig{
		AppName:             name,
		AppVersion:          "0.1.0",
		ModulePath:          modulePath,
		BartoloReplacePath:  bartoloReplacePath,
		BartoloVersion:      bartoloVersion,
		EnvPrefix:           envPrefix,
		SerializationFormat: serializationFormat,
		APIKeyEnvVar:        apiKeyEnvVar,
		Targets:             targets,
	}, nil
}

func enrichOpenAPIForREADME(api *OpenAPI, project *ProjectConfig) {
	if api == nil {
		return
	}

	api.CommandName = getCommandName()
	if project != nil && project.AppName != "" {
		api.CommandName = project.AppName
	}
	api.AuthDoc = getAuthDocFromProject(api, project)
	api.Examples = buildREADMEExamples(api)
}

func getAuthDocFromProject(api *OpenAPI, project *ProjectConfig) *AuthDoc {
	envPrefix := strings.ToUpper(strings.ReplaceAll(api.CommandName, "-", "_"))
	apiKeyEnvVar := ""
	if project != nil && project.EnvPrefix != "" {
		envPrefix = project.EnvPrefix
	}
	if project != nil {
		apiKeyEnvVar = strings.TrimSpace(project.APIKeyEnvVar)
	}

	doc := getAuthDocFromSpec(api, envPrefix)
	if doc != nil {
		doc.EnvPrefix = envPrefix
	}
	if doc != nil && apiKeyEnvVar != "" {
		for i, envVar := range doc.EnvVars {
			if strings.HasSuffix(envVar, "_API_KEY") {
				doc.EnvVars[i] = apiKeyEnvVar
				break
			}
		}
		doc.EnvVars = uniqueStrings(doc.EnvVars)
	}
	if doc != nil && doc.ProfileCommand != "" && api != nil {
		doc.ProfileCommand = strings.ReplaceAll(doc.ProfileCommand, "<binary>", api.CommandName)
	}
	return doc
}

func getAuthDocFromSpec(api *OpenAPI, envPrefix string) *AuthDoc {
	// Documentation is derived from the recognized security schemes captured
	// during generation (api.AuthSchemes), so OAuth2 and multi-scheme specs get
	// accurate README auth sections rather than a misleading "API key" default.
	schemes := api.AuthSchemes
	if len(schemes) == 0 {
		return &AuthDoc{
			Enabled: false,
			Summary: "This CLI does not require configured authentication metadata from the OpenAPI spec.",
		}
	}

	supported := make([]*AuthScheme, 0, len(schemes))
	var manualNotes []string
	for _, s := range schemes {
		if s.Supported {
			supported = append(supported, s)
			continue
		}
		manualNotes = append(manualNotes, fmt.Sprintf("`%s` (%s): %s", s.Name, s.Kind, s.Manual))
	}

	// No auto-wired scheme: document the manual setup only.
	if len(supported) == 0 {
		return &AuthDoc{
			Enabled:     true,
			Summary:     "This CLI's OpenAPI spec declares authentication that Bartolo could not auto-wire. Configure it manually before making API calls.",
			ManualNotes: manualNotes,
		}
	}

	// Single supported scheme with no manual notes keeps the classic, compact
	// README auth section (backward compatible with the bearer/api-key output).
	if len(supported) == 1 && len(manualNotes) == 0 {
		return singleSchemeAuthDoc(supported[0], envPrefix)
	}

	// Multiple schemes (or a mix with manual ones): document each scheme and
	// which one is the default.
	doc := &AuthDoc{
		Enabled:         true,
		ProfileCommand:  "<binary> auth setup",
		ProfileRequired: true,
		ManualNotes:     manualNotes,
		Summary:         "The generated CLI supports multiple authentication schemes. The default scheme is used when no profile selects a type explicitly.",
	}
	var envVars []string
	for _, s := range supported {
		doc.Schemes = append(doc.Schemes, &AuthDocScheme{
			Name:     s.Name,
			Kind:     s.Kind,
			TokenURL: s.TokenURL,
			Default:  s.Default,
		})
		envVars = append(envVars, schemeEnvVars(s, envPrefix)...)
	}
	doc.EnvVars = uniqueStrings(envVars)
	return doc
}

func singleSchemeAuthDoc(s *AuthScheme, envPrefix string) *AuthDoc {
	doc := &AuthDoc{
		Enabled:         true,
		Kind:            s.Kind,
		ProfileCommand:  "<binary> auth setup",
		ProfileRequired: true,
	}

	switch {
	case strings.HasPrefix(s.Kind, "OAuth2"):
		summary := "The generated CLI authenticates with " + s.Kind + " using a stored profile. Run `auth setup` to save the client ID/secret."
		if s.TokenURL != "" {
			summary += " Token endpoint: " + s.TokenURL + "."
		}
		doc.Summary = summary
	case s.Kind == "Bearer token":
		doc.EnvVars = uniqueStrings([]string{envPrefix + "_TOKEN", envPrefix + "_API_KEY", envPrefix + "_AUTHORIZATION"})
		doc.Summary = "The generated CLI supports bearer token authentication from environment variables or a stored profile."
	default:
		doc.EnvVars = uniqueStrings([]string{envPrefix + "_API_KEY"})
		doc.Summary = "The generated CLI supports API key authentication from either environment variables or a stored profile."
	}

	return doc
}

func schemeEnvVars(s *AuthScheme, envPrefix string) []string {
	switch {
	case s.Kind == "Bearer token":
		return []string{envPrefix + "_TOKEN", envPrefix + "_API_KEY", envPrefix + "_AUTHORIZATION"}
	case strings.HasPrefix(s.Kind, "API key"):
		return []string{envPrefix + "_API_KEY"}
	}
	return nil
}

func buildREADMEExamples(api *OpenAPI) []*READMEExample {
	binary := api.CommandName
	if binary == "" {
		binary = "my-cli"
	}

	examples := []*READMEExample{
		{
			Title:       "Check setup",
			Command:     binary + " -o json doctor",
			Description: "Verify config, auth source, and selected server before making API calls.",
		},
		{
			Title:       "Inspect server defaults",
			Command:     binary + " server list",
			Description: "See the generated server targets and persist a default when the spec provides multiple environments.",
		},
		{
			Title:       "Persist the default output format",
			Command:     binary + " default-format json",
			Description: "Write the preferred output format into the CLI config so future commands use it automatically. Anything other than `table` turns off the tables that list commands render on a terminal; `default-format table` turns them back on.",
		},
	}

	if len(api.Groups) > 0 {
		group := api.Groups[0]
		examples = append(examples, &READMEExample{
			Title:       "Explore a command group",
			Command:     binary + " " + group.CLIName + " --help",
			Description: "Inspect the grouped product commands synthesized from the OpenAPI tags.",
		})
		if op := pickREADMEOperation(group.Operations); op != nil {
			command := binary + " " + op.CommandPath
			description := "Replace any positional placeholders with real values from your environment."
			if len(op.RequiredParams) > 0 {
				command += " --help"
				description = "Start with command help if the operation requires resource identifiers."
			}
			examples = append(examples, &READMEExample{
				Title:       "Run a grouped command",
				Command:     command,
				Description: description,
			})
		}
	} else if op := pickREADMEOperation(api.Operations); op != nil {
		examples = append(examples, &READMEExample{
			Title:       "Inspect an operation",
			Command:     binary + " " + op.Use + " --help",
			Description: "Use command help to see flags, required args, and request body expectations.",
		})
	}

	rawPath := "/"
	if op := pickRawRequestOperation(api); op != nil {
		rawPath = op.Path
	}

	examples = append(examples, &READMEExample{
		Title:       "Use the raw escape hatch",
		Command:     binary + " request get " + rawPath,
		Description: "Call the API directly with configured auth when a high-level command is missing.",
	})

	return examples
}

func pickREADMEOperation(operations []*Operation) *Operation {
	if len(operations) == 0 {
		return nil
	}

	for _, operation := range operations {
		if len(operation.RequiredParams) == 0 {
			return operation
		}
	}

	return operations[0]
}

func pickRawRequestOperation(api *OpenAPI) *Operation {
	if api == nil {
		return nil
	}

	if len(api.Groups) > 0 {
		for _, group := range api.Groups {
			if operation := pickREADMEOperation(group.Operations); operation != nil {
				return operation
			}
		}
	}

	return pickREADMEOperation(api.Operations)
}

func writeGeneratedREADME(api *OpenAPI) {
	sb := renderTemplate("templates/readme.tmpl", nil, api)

	target := "README.generated.md"
	if _, err := os.Stat("README.md"); os.IsNotExist(err) {
		target = "README.md"
	}

	if err := ioutil.WriteFile(target, []byte(sb), 0600); err != nil {
		panic(err)
	}
}

func writeGeneratedExamples(api *OpenAPI) {
	target := filepath.Join("examples", "README.md")
	sb := renderTemplate("templates/examples_readme.tmpl", nil, api)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		panic(err)
	}
	if err := ioutil.WriteFile(target, []byte(sb), 0600); err != nil {
		panic(err)
	}
}

func writeGeneratedPackageStub() {
	const stub = `package generated

import "github.com/spf13/cobra"

// Register is the generator-owned entrypoint for OpenAPI-derived commands.
func Register(root *cobra.Command) {
	_ = root
}
`

	writeFormattedFile(filepath.Join("cli", "generated", "register.go"), []byte(stub))
}

func writeCustomPackageStub() {
	target := filepath.Join("cli", "custom", "register.go")
	if _, err := os.Stat(target); err == nil {
		return
	}

	const stub = `package custom

import "github.com/spf13/cobra"

// Register is the user-owned extension point for custom commands and hooks.
func Register(root *cobra.Command) {
	_ = root
	// Add your own commands and middleware registrations here.
	//
	// To add or patch MCP tools (survives bartolo generate), call mcpserver.Use
	// from this function. Example:
	//
	//   import "github.com/orq-ai/bartolo/mcpserver"
	//
	//   mcpserver.Use(
	//     mcpserver.AddTools(myTool),
	//     mcpserver.RemoveTools("legacy_delete"),
	//     mcpserver.PatchTool("echo_echo", func(t *mcpserver.ToolDef) {
	//         t.Description = "Custom description"
	//     }),
	//   )
}
`

	writeFormattedFile(target, []byte(stub))
}

func writeGoModIfMissing(modulePath string, bartoloReplacePath string) {
	if _, err := os.Stat("go.mod"); err == nil {
		return
	}

	if !isValidModulePath(modulePath) {
		panic(fmt.Errorf("invalid module path %q", modulePath))
	}

	data := loadTemplate("templates/go.mod.tmpl")
	tmpl, err := template.New("gomod").Parse(string(data))
	if err != nil {
		panic(err)
	}

	var sb strings.Builder
	if err := tmpl.Execute(&sb, map[string]string{
		"ModulePath":         modulePath,
		"BartoloReplacePath": bartoloReplacePath,
	}); err != nil {
		panic(err)
	}

	if err := ioutil.WriteFile("go.mod", []byte(sb.String()), 0600); err != nil {
		panic(err)
	}
}

func writeGeneratedProjectTooling(config *ProjectConfig) {
	templateData := map[string]string{
		"CommandName":  config.AppName,
		"APIKeyEnvVar": config.APIKeyEnvVar,
		"NameEnv":      config.EnvPrefix,
	}

	writeTemplateFileIfMissing("templates/generated_makefile.tmpl", "Makefile", 0600, templateData)
	writeTemplateFileIfMissing("templates/build.sh.tmpl", filepath.Join("scripts", "build.sh"), 0755, templateData)
	writeTemplateFileIfMissing("templates/install-local.sh.tmpl", filepath.Join("scripts", "install-local.sh"), 0755, templateData)
	writeTemplateFileIfMissing("templates/gitignore.tmpl", ".gitignore", 0600, templateData)
	writeTemplateFileIfMissing("templates/editorconfig.tmpl", ".editorconfig", 0600, templateData)
	writeTemplateFileIfMissing("templates/gitattributes.tmpl", ".gitattributes", 0600, templateData)
	writeTemplateFileIfMissing("templates/env.example.tmpl", ".env.example", 0600, templateData)
}

func runGoModTidy() {
	if _, err := os.Stat("go.mod"); err != nil {
		return
	}

	if _, err := exec.LookPath("go"); err != nil {
		return
	}

	cmd := exec.Command("go", "mod", "tidy")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: go mod tidy failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "Run `go mod tidy` in this directory once your module path and access are configured.")
	}
}

func resetGeneratedPackage() {
	target := filepath.Join("cli", "generated")
	if err := os.RemoveAll(target); err != nil {
		panic(err)
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		panic(err)
	}
}

// commandPartials holds the template blocks every command template shares.
const commandPartials = "templates/command_partials.tmpl"

// renderCommandTemplate renders a command template with the shared partials in
// scope.
func renderCommandTemplate(name string, data interface{}) string {
	return renderTemplate(name, commandTemplateFuncs(), data, commandPartials)
}

func commandTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"escapeStr": escapeString,
		"slug":      slug,
		"title":     strings.Title,
		"rawJSON":   rawJSONLiteral,
		"joinComma": func(values []string) string { return strings.Join(values, ", ") },
	}
}

func commandFileNeedsFmt(operations []*Operation) bool {
	for _, operation := range operations {
		if len(operation.Waiters) > 0 {
			return true
		}
	}

	return false
}

func writeGeneratedCommandFiles(api *OpenAPI, shortName string) {
	resetGeneratedPackage()

	// The client funcs are emitted for EVERY operation in the IR, including
	// CLI-excluded ops, because generated MCP tool closures call them.
	writeFormattedFile(
		filepath.Join("cli", "generated", shortName+"_client.go"),
		[]byte(renderCommandTemplate("templates/generated_client.tmpl", api)),
	)
	writeFormattedFile(
		filepath.Join("cli", "generated", "register.go"),
		[]byte(renderCommandTemplate("templates/generated_register.tmpl", api)),
	)

	// CLI command generation only runs when the CLI surface is a target. In
	// MCP-only mode no root_commands.go / group command files are emitted, and
	// the register template omits the matching register calls.
	if api.TargetCLI {
		writeGeneratedCLICommandFiles(api)
	}

	if api.HasMCPTools() {
		writeFormattedFile(
			filepath.Join("cli", "generated", "mcp_tools.go"),
			[]byte(renderCommandTemplate("templates/generated_mcp.tmpl", buildMCPTemplateData(api))),
		)
	}
}

// writeGeneratedCLICommandFiles renders root_commands.go and one file per
// command group. It only sees CLI-visible operations: x-bartolo-cli-ignore ops
// are dropped here but survive in the IR, because MCP tools call their client
// funcs.
func writeGeneratedCLICommandFiles(api *OpenAPI) {
	rootOps := cliVisibleOperations(api.Operations)
	rootCommands := &CommandsTemplateData{
		API:        api,
		Operations: rootOps,
		Waiters:    api.Waiters,
		NeedsFmt:   commandFileNeedsFmt(rootOps),
	}
	writeFormattedFile(
		filepath.Join("cli", "generated", "root_commands.go"),
		[]byte(renderCommandTemplate("templates/generated_root_commands.tmpl", rootCommands)),
	)

	for _, group := range api.Groups {
		groupOps := cliVisibleOperations(group.Operations)
		if len(groupOps) == 0 {
			// Every operation in this group is CLI-excluded; skip the group
			// command file entirely so we don't emit an empty group command.
			// The register template guards the matching register call.
			continue
		}
		groupData := &CommandsTemplateData{
			API:        api,
			Group:      group,
			Operations: groupOps,
			NeedsFmt:   commandFileNeedsFmt(groupOps),
		}
		filename := filepath.Join("cli", "generated", slug(group.CLIName)+"_commands.go")
		writeFormattedFile(
			filename,
			[]byte(renderCommandTemplate("templates/generated_group_commands.tmpl", groupData)),
		)
	}
}

func initCmd(cmd *cobra.Command, args []string) {
	config, err := resolveInitConfig(cmd, args)
	if err != nil {
		log.Fatal(err)
	}

	if err := writeProjectScaffold(config, false); err != nil {
		log.Fatal(err)
	}
	runGoModTidy()
}

func generate(cmd *cobra.Command, args []string) {
	if err := generateFromSpec(args[0]); err != nil {
		log.Fatal(err)
	}
	runGoModTidy()
}

func writeProjectScaffold(config *ProjectConfig, overwrite bool) error {
	mainPath := filepath.Join("cmd", config.AppName, "main.go")
	if !overwrite {
		if _, err := os.Stat(mainPath); err == nil {
			return fmt.Errorf("refusing to overwrite existing %s", mainPath)
		}
	}

	templateData := map[string]string{
		"Name":                config.AppName,
		"AppVersion":          firstNonEmpty(config.AppVersion, "0.1.0"),
		"NameEnv":             config.EnvPrefix,
		"ModulePath":          config.ModulePath,
		"APIKeyEnvVar":        config.APIKeyEnvVar,
		"SerializationFormat": config.SerializationFormat,
	}

	sb := renderTemplate("templates/main.tmpl", nil, templateData)

	writeFormattedFile(mainPath, []byte(sb))
	writeGeneratedPackageStub()
	writeCustomPackageStub()
	writeGoModIfMissing(config.ModulePath, config.BartoloReplacePath)
	writeGeneratedProjectTooling(config)
	writeProjectConfig(config)
	return nil
}

func generateFromSpec(specPath string) error {
	data, err := ioutil.ReadFile(specPath)
	if err != nil {
		return err
	}

	// Load the OpenAPI document.
	swagger, err := loadOpenAPIDocument(data)
	if err != nil {
		return err
	}

	shortName := normalizeSpecName(specPath)

	templateData := ProcessAPI(shortName, swagger)
	config := loadProjectConfig()
	if config != nil {
		if err := ensureConfigTargets(config); err != nil {
			return err
		}
		templateData.TargetCLI = config.TargetCLI()
		templateData.TargetMCP = config.TargetMCP()
	}
	enrichOpenAPIForREADME(templateData, config)
	writeGeneratedCommandFiles(templateData, shortName)
	writeGeneratedREADME(templateData)
	writeGeneratedExamples(templateData)
	if config != nil {
		config.LastSpecPath = specPath
		config.BartoloVersion = bartoloVersion
		writeProjectConfig(config)
	}
	return nil
}

func syncCmd(cmd *cobra.Command, args []string) {
	config := loadProjectConfig()
	if config == nil {
		log.Fatal("missing .bartolo.json; run `bartolo init` first")
	}
	config.BartoloVersion = bartoloVersion
	if config.AppVersion == "" {
		config.AppVersion = "0.1.0"
	}
	// Resolve targets once here (prompting/warning for legacy configs) and
	// persist them via writeProjectScaffold, so the generateFromSpec call below
	// reloads a config that already records targets and never re-prompts.
	if err := ensureConfigTargets(config); err != nil {
		log.Fatal(err)
	}
	if err := writeProjectScaffold(config, true); err != nil {
		log.Fatal(err)
	}

	specPath := config.LastSpecPath
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		specPath = args[0]
	}
	if specPath != "" {
		if err := generateFromSpec(specPath); err != nil {
			log.Fatal(err)
		}
	}
	runGoModTidy()
}

func main() {
	root := &cobra.Command{
		Use:     "bartolo",
		Short:   "Generate publishable CLIs from OpenAPI specs",
		Version: bartoloVersion,
	}
	root.SetVersionTemplate("bartolo {{.Version}}\n")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the bartolo version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("bartolo %s\n", bartoloVersion)
		},
	})

	initCommand := &cobra.Command{
		Use:   "init [app-name]",
		Short: "Initialize a new CLI entrypoint for your project",
		Args:  cobra.MaximumNArgs(1),
		Run:   initCmd,
	}
	initCommand.Flags().Bool("interactive", false, "Prompt for CLI settings even if app name is provided")
	initCommand.Flags().String("module-path", "", "Go module path for the generated CLI project")
	initCommand.Flags().String("bartolo-path", "", "Local path to the bartolo repo to use via go.mod replace during development")
	initCommand.Flags().String("api-key-env-var", "", "Custom API key environment variable for generated CLIs")
	initCommand.Flags().String("serialization-format", "json", fmt.Sprintf("What generated CLIs serialize to when not rendering a table [%s]", strings.Join(serializationFormats, ", ")))
	initCommand.Flags().String("targets", "", "Comma-separated generation targets [cli, mcp] (default: both)")
	root.AddCommand(initCommand)

	root.AddCommand(&cobra.Command{
		Use:   "generate <api-spec>",
		Short: "Generate API commands from an OpenAPI spec",
		Args:  cobra.ExactArgs(1),
		Run:   generate,
	})
	root.AddCommand(&cobra.Command{
		Use:     "sync [api-spec]",
		Aliases: []string{"upgrade"},
		Short:   "Refresh scaffold-owned files and optionally regenerate from the last spec",
		Args:    cobra.MaximumNArgs(1),
		Run:     syncCmd,
	})

	root.Execute()
}
