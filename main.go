package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"go/format"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"unicode"

	survey "github.com/AlecAivazis/survey/v2"
	surveycore "github.com/AlecAivazis/survey/v2/core"
	surveyterminal "github.com/AlecAivazis/survey/v2/terminal"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/orq-ai/bartolo/shorthand"
	"github.com/spf13/cobra"
	yamlv3 "gopkg.in/yaml.v3"
)

//go:embed templates/*
var templateFS embed.FS

const projectConfigFilename = ".bartolo.json"
const bartoloVersion = "0.2.0"

// OpenAPI Extensions
const (
	ExtAliases     = "x-cli-aliases"
	ExtDescription = "x-cli-description"
	ExtGroup       = "x-cli-group"
	ExtIgnore      = "x-cli-ignore"
	ExtHidden      = "x-cli-hidden"
	ExtName        = "x-cli-name"
	ExtWaiters     = "x-cli-waiters"
)

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
}

// Operation describes an OpenAPI operation (GET/POST/PUT/PATCH/DELETE)
type Operation struct {
	HandlerName    string
	GoName         string
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
	BodyFields     []*BodyField
	Hidden         bool
	NeedsResponse  bool
	Waiters        []*WaiterParams
	Group          *CommandGroup
	CommandPath    string
	LeafName       string
}

// BodyField describes a generated request-body flag for a simple top-level
// request property.
type BodyField struct {
	Name        string
	CLIName     string
	GoName      string
	Description string
	Type        string
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
	Operations  []*Operation
	Description string
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
}

// ProjectConfig describes local generator metadata written by `init`.
type ProjectConfig struct {
	AppName             string `json:"app_name"`
	AppVersion          string `json:"app_version,omitempty"`
	ModulePath          string `json:"module_path,omitempty"`
	BartoloReplacePath  string `json:"bartolo_replace_path,omitempty"`
	BartoloVersion      string `json:"bartolo_version,omitempty"`
	EnvPrefix           string `json:"env_prefix"`
	DefaultOutputFormat string `json:"default_output_format,omitempty"`
	APIKeyEnvVar        string `json:"api_key_env_var,omitempty"`
	LastSpecPath        string `json:"last_spec_path,omitempty"`
}

// AuthDoc describes auth setup to show in a generated README.
type AuthDoc struct {
	Enabled         bool
	Kind            string
	EnvVars         []string
	ProfileCommand  string
	Summary         string
	ProfileRequired bool
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
	AuthDoc      *AuthDoc
	CommandName  string
	Examples     []*READMEExample
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
	apiName := shortName
	if api.Info.Extensions[ExtName] != nil {
		apiName = extStr(api.Info.Extensions[ExtName])
	}

	apiDescription := api.Info.Description
	if api.Info.Extensions[ExtDescription] != nil {
		apiDescription = extStr(api.Info.Extensions[ExtDescription])
	}

	result := &OpenAPI{
		Name:         apiName,
		GoName:       toGoName(shortName, false),
		PublicGoName: toGoName(shortName, true),
		Title:        api.Info.Title,
		Description:  escapeString(apiDescription),
		AuthInit:     getAuthInit(api),
	}

	for _, s := range api.Servers {
		result.Servers = append(result.Servers, &Server{
			Description: s.Description,
			URL:         s.URL,
		})
	}

	// Convenience map for operation ID -> operation
	operationMap := make(map[string]*Operation)
	tagDefs := make(map[string]*openapi3.Tag)
	groupMap := make(map[string]*CommandGroup)
	groupOrder := make([]string, 0)

	for _, tag := range api.Tags {
		if tag == nil {
			continue
		}
		tagDefs[tag.Name] = tag
	}

	var keys []string
	for path := range api.Paths.Map() {
		keys = append(keys, path)
	}
	sort.Strings(keys)

	for _, path := range keys {
		item := api.Paths.Value(path)

		if item.Extensions[ExtIgnore] != nil {
			// Ignore this path.
			continue
		}

		pathHidden := false
		if item.Extensions[ExtHidden] != nil {
			mustDecodeExt(item.Extensions[ExtHidden], &pathHidden)
		}

		for method, operation := range item.Operations() {
			if operation.Extensions[ExtIgnore] != nil {
				// Ignore this operation.
				continue
			}

			name := operation.OperationID
			if name == "" {
				// Generate a name from the method and path when operationId is missing
				name = strings.ToLower(method) + strings.Replace(strings.Replace(path, "/", "-", -1), "{", "", -1)
				name = strings.Replace(name, "}", "", -1)
				name = strings.Trim(name, "-")
			}
			explicitLeafName := getPreferredStringExt(operation.Extensions, ExtName)
			leafName := explicitLeafName
			if leafName == "" {
				leafName = name
			}

			var aliases []string
			if operation.Extensions[ExtAliases] != nil {
				// We need to decode the extension value into our string slice.
				mustDecodeExt(operation.Extensions[ExtAliases], &aliases)
			}

			params := getParams(item, method)
			requiredParams := getRequiredParams(params)
			optionalParams := getOptionalParams(params)
			short := operation.Summary
			shortExplicit := short != ""

			description := operation.Description
			if operation.Extensions[ExtDescription] != nil {
				description = extStr(operation.Extensions[ExtDescription])
			}

			reqMt, reqSchema, reqExamples, bodyFields := getRequestInfo(operation)

			var examples []string
			if len(reqExamples) > 0 {
				wroteHeader := false
				for _, ex := range reqExamples {
					if _, ok := ex.(string); !ok {
						// Not a string, so it's structured data. Let's marshal it to the
						// shorthand syntax if we can.
						if m, ok := ex.(map[string]interface{}); ok {
							ex = shorthand.Get(m)
							examples = append(examples, ex.(string))
							continue
						}

						b, _ := json.Marshal(ex)

						if !wroteHeader {
							description += "\n## Input Example\n\n"
							wroteHeader = true
						}

						description += "\n" + string(b) + "\n"
						continue
					}

					if !wroteHeader {
						description += "\n## Input Example\n\n"
						wroteHeader = true
					}

					description += "\n" + ex.(string) + "\n"
				}
			}

			if reqSchema != "" {
				description += "\n\n" + reqSchema
			}
			if len(bodyFields) > 0 {
				description += "\n\nSimple top-level body fields are also exposed as flags for this command."
			}

			method := strings.Title(strings.ToLower(method))

			hidden := pathHidden
			if operation.Extensions[ExtHidden] != nil {
				mustDecodeExt(operation.Extensions[ExtHidden], &hidden)
			}

			group := resolveCommandGroup(path, operation, tagDefs)
			if group != nil {
				if existing := groupMap[group.CLIName]; existing != nil {
					group = existing
				} else {
					groupMap[group.CLIName] = group
					groupOrder = append(groupOrder, group.CLIName)
				}

				leafName = inferGroupedLeafName(leafName, strings.ToLower(method), path, group.CLIName, explicitLeafName != "")
				if leafName == "" {
					leafName = slug(method)
				}
			}

			if !shortExplicit {
				short = displayNameFromSlug(leafName)
				if short == "" {
					short = leafName
				}
			}

			use := usage(leafName, requiredParams)
			commandPath := use
			if group != nil {
				commandPath = group.CLIName + " " + use
			}

			returnType := "interface{}"
		returnTypeLoop:
			for code, ref := range operation.Responses.Map() {
				if num, err := strconv.Atoi(code); err != nil || num < 200 || num >= 300 {
					// Skip invalid responses
					continue
				}

				if ref.Value != nil {
					for _, content := range ref.Value.Content {
						if _, ok := content.Example.(map[string]interface{}); ok {
							returnType = "map[string]interface{}"
							break returnTypeLoop
						}

						if content.Schema != nil && content.Schema.Value != nil {
							if content.Schema.Value.Type.Is("object") || len(content.Schema.Value.Properties) != 0 {
								returnType = "map[string]interface{}"
								break returnTypeLoop
							}
						}
					}
				}
			}

			o := &Operation{
				HandlerName:    commandPath,
				GoName:         toGoName(name, true),
				Use:            use,
				Aliases:        aliases,
				Short:          short,
				Long:           escapeString(description),
				Method:         method,
				CanHaveBody:    operation.RequestBody != nil && operation.RequestBody.Value != nil,
				ReturnType:     returnType,
				Path:           path,
				AllParams:      params,
				RequiredParams: requiredParams,
				OptionalParams: optionalParams,
				MediaType:      reqMt,
				Examples:       examples,
				BodyFields:     bodyFields,
				Hidden:         hidden,
				Group:          group,
				CommandPath:    commandPath,
				LeafName:       leafName,
			}

			operationMap[operation.OperationID] = o
			if group != nil {
				group.Operations = append(group.Operations, o)
			} else {
				result.Operations = append(result.Operations, o)
			}

			for _, p := range params {
				if p.In == "path" {
					result.Imports.Strings = true
				}
			}

			for _, p := range optionalParams {
				if p.In == "query" || p.In == "header" {
					result.Imports.Fmt = true
				}
			}
		}
	}

	for _, key := range groupOrder {
		group := groupMap[key]
		sort.Slice(group.Operations, func(i, j int) bool {
			return group.Operations[i].CommandPath < group.Operations[j].CommandPath
		})
		result.Groups = append(result.Groups, group)
	}

	if api.Extensions[ExtWaiters] != nil {
		var waiters map[string]*Waiter

		mustDecodeExt(api.Extensions[ExtWaiters], &waiters)

		for name, waiter := range waiters {
			waiter.CLIName = slug(name)
			waiter.GoName = toGoName(name+"-waiter", true)
			waiter.Operation = operationMap[waiter.OperationID]
			waiter.Use = usage(name, waiter.Operation.RequiredParams)

			for _, matcher := range waiter.Matchers {
				if matcher.Test == "" {
					matcher.Test = "equal"
				}
			}

			for operationID, waitOpParams := range waiter.After {
				op := operationMap[operationID]
				if op == nil {
					panic(fmt.Errorf("Unknown waiter operation %s", operationID))
				}

				var args []string
				for _, p := range op.RequiredParams {
					selector := waitOpParams[p.Name]
					if selector == "" {
						panic(fmt.Errorf("Missing required parameter %s", p.Name))
					}
					delete(waitOpParams, p.Name)

					args = append(args, selector)

					result.Imports.Fmt = true
					op.NeedsResponse = true
				}

				// Transform from OpenAPI param names to CLI names
				wParams := make(map[string]string)
				for p, s := range waitOpParams {
					found := false
					for _, optional := range op.OptionalParams {
						if optional.Name == p {
							wParams[optional.CLIName] = s
							found = true
							break
						}
					}
					if !found {
						panic(fmt.Errorf("Unknown parameter %s for waiter %s", p, name))
					}
				}

				op.Waiters = append(op.Waiters, &WaiterParams{
					Waiter: waiter,
					Args:   args,
					Params: wParams,
				})
			}

			result.Waiters = append(result.Waiters, waiter)
		}

		if len(waiters) > 0 {
			result.Imports.Time = true
		}
	}

	return result
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

func getPreferredStringExt(extensions map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if extensions == nil || extensions[key] == nil {
			continue
		}
		return extStr(extensions[key])
	}

	return ""
}

func resolveCommandGroup(path string, operation *openapi3.Operation, tagDefs map[string]*openapi3.Tag) *CommandGroup {
	groupName := getPreferredStringExt(operation.Extensions, ExtGroup)
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
	short := displayNameFromSlug(cliName)
	long := ""
	var aliases []string
	hidden := false

	if tagDef != nil {
		if override := getPreferredStringExt(tagDef.Extensions, ExtName); override != "" {
			cliName = slug(override)
		}
		if description := getPreferredStringExt(tagDef.Extensions, ExtDescription); description != "" {
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
		if tagDef.Extensions[ExtAliases] != nil {
			mustDecodeExt(tagDef.Extensions[ExtAliases], &aliases)
		}
		if tagDef.Extensions[ExtHidden] != nil {
			mustDecodeExt(tagDef.Extensions[ExtHidden], &hidden)
		}
	}

	if override := getPreferredStringExt(operation.Extensions, ExtGroup); override != "" {
		cliName = slug(override)
	}

	return &CommandGroup{
		Name:        groupName,
		CLIName:     cliName,
		GoName:      toGoName(cliName, false),
		Short:       short,
		Long:        escapeString(long),
		Aliases:     aliases,
		Hidden:      hidden,
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
		if p.Value != nil && p.Value.Extensions["x-cli-ignore"] == nil {
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

			cliName := slug(p.Value.Name)
			if p.Value.Extensions[ExtName] != nil {
				cliName = extStr(p.Value.Extensions[ExtName])
			}

			description := p.Value.Description
			if p.Value.Extensions[ExtDescription] != nil {
				description = extStr(p.Value.Extensions[ExtDescription])
			}

			allParams = append(allParams, &Param{
				Name:        p.Value.Name,
				CLIName:     cliName,
				GoName:      toGoName("param "+cliName, false),
				Description: description,
				In:          p.Value.In,
				Required:    p.Value.Required,
				Type:        t,
				TypeNil:     tn,
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

func getRequestInfo(op *openapi3.Operation) (string, string, []interface{}, []*BodyField) {
	type requestInfo struct {
		summary    string
		examples   []interface{}
		bodyFields []*BodyField
	}

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
			} else {
				for _, ex := range item.Examples {
					if ex.Value != nil {
						examples = append(examples, ex.Value.Value)
						break
					}
				}
			}

			mts[mt] = requestInfo{
				summary:    summary,
				examples:   examples,
				bodyFields: bodyFields,
			}
		}
	}

	// Prefer JSON.
	for mt, item := range mts {
		if strings.Contains(mt, "json") {
			return mt, item.summary, item.examples, item.bodyFields
		}
	}

	// Fall back to YAML next.
	for mt, item := range mts {
		if strings.Contains(mt, "yaml") {
			return mt, item.summary, item.examples, item.bodyFields
		}
	}

	// Last resort: return the first we find!
	for mt, item := range mts {
		return mt, item.summary, item.examples, item.bodyFields
	}

	return "", "", nil, nil
}

func getBodyFields(schema *openapi3.Schema) []*BodyField {
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
		if ref.Value.Extensions != nil && ref.Value.Extensions[ExtIgnore] != nil {
			continue
		}

		fieldType := bodyFieldType(ref.Value)
		if fieldType == "" {
			continue
		}

		cliName := slug(name)
		if ref.Value.Extensions != nil && ref.Value.Extensions[ExtName] != nil {
			cliName = extStr(ref.Value.Extensions[ExtName])
		}
		description := ref.Value.Description
		if ref.Value.Extensions != nil && ref.Value.Extensions[ExtDescription] != nil {
			description = extStr(ref.Value.Extensions[ExtDescription])
		}

		fields = append(fields, &BodyField{
			Name:        name,
			CLIName:     cliName,
			GoName:      toGoName("body "+cliName, false),
			Description: description,
			Type:        fieldType,
		})
	}

	return fields
}

func bodyFieldType(schema *openapi3.Schema) string {
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

func getAuthInit(api *openapi3.T) string {
	_, scheme := getRequiredSecurityScheme(api)
	if scheme == nil {
		return ""
	}
	switch scheme.Type {
	case "apiKey":
		switch scheme.In {
		case "header":
			return fmt.Sprintf("apikey.Init(%q, apikey.LocationHeader)", scheme.Name)
		case "query":
			return fmt.Sprintf("apikey.Init(%q, apikey.LocationQuery)", scheme.Name)
		case "cookie":
			return fmt.Sprintf("apikey.Init(%q, apikey.LocationCookie)", scheme.Name)
		}
	case "http":
		if strings.EqualFold(scheme.Scheme, "bearer") {
			return "apikey.InitBearer(\"Authorization\")"
		}
	}

	return ""
}

func getRequiredSecurityScheme(api *openapi3.T) (string, *openapi3.SecurityScheme) {
	if api == nil || api.Components == nil || api.Components.SecuritySchemes == nil {
		return "", nil
	}

	required := make(map[string]bool)
	addRequirements := func(reqs openapi3.SecurityRequirements) {
		for _, req := range reqs {
			for name := range req {
				required[name] = true
			}
		}
	}

	addRequirements(api.Security)
	for _, item := range api.Paths.Map() {
		for _, operation := range item.Operations() {
			if operation.Security != nil {
				addRequirements(*operation.Security)
			}
		}
	}

	if len(required) != 1 {
		return "", nil
	}

	var schemeName string
	for name := range required {
		schemeName = name
	}

	schemeRef := api.Components.SecuritySchemes[schemeName]
	if schemeRef == nil || schemeRef.Value == nil {
		return "", nil
	}

	return schemeName, schemeRef.Value
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

func renderTemplate(name string, funcs template.FuncMap, data interface{}) string {
	templateData := loadTemplate(name)
	tmpl := template.New(filepath.Base(name))
	if funcs != nil {
		tmpl = tmpl.Funcs(funcs)
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
	swagger, err := loader.LoadFromData(data)
	if err == nil {
		return swagger, nil
	}

	normalized, changed, normalizeErr := normalizeOpenAPI31Data(data)
	if normalizeErr != nil || !changed || bytes.Equal(normalized, data) {
		return nil, err
	}

	swagger, retryErr := loader.LoadFromData(normalized)
	if retryErr != nil {
		return nil, retryErr
	}

	return swagger, nil
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

func normalizeOutputFormat(value string) string {
	if format, ok := parseOutputFormat(value); ok {
		return format
	}
	return "json"
}

func parseOutputFormat(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "json":
		return "json", true
	case "yaml":
		return "yaml", true
	case "toon":
		return "toon", true
	default:
		return "", false
	}
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
	fmt.Printf("%s %s\n", wizardAccent(color, "Default output:"), config.DefaultOutputFormat)
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

	defaultFormat, _ := cmd.Flags().GetString("default-format")
	defaultFormat = normalizeOutputFormat(defaultFormat)

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
		printWizardStep(color, 1, 3, "CLI identity", "Pick the install name your users will actually type.")
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

		printWizardStep(color, 2, 3, "Auth defaults", "You can keep the suggested API key env var or replace it with your own convention.")
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

		printWizardStep(color, 3, 3, "Output format", "Choose the default rendering style for generated CLIs. Use the arrow keys to move and Enter to confirm.")
		defaultFormat, err = promptSelect("Default output format", []selectOption{
			{Value: "json", Label: "json", Description: "Best for agents and automation."},
			{Value: "yaml", Label: "yaml", Description: "Easy to scan in terminals."},
			{Value: "toon", Label: "toon", Description: "Human-oriented serialization of data for LLMs."},
		}, defaultFormat, color)
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
			DefaultOutputFormat: defaultFormat,
			APIKeyEnvVar:        apiKeyEnvVar,
		})
	}

	if name == "" {
		return nil, fmt.Errorf("missing app name")
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
		DefaultOutputFormat: defaultFormat,
		APIKeyEnvVar:        apiKeyEnvVar,
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
	// This wrapper exists so README generation can use the env prefix written by `init`.
	// We reconstruct from the original auth init marker rather than trying to parse it later.
	switch api.AuthInit {
	case "":
		return &AuthDoc{
			Enabled: false,
			Summary: "This CLI does not require configured authentication metadata from the OpenAPI spec.",
		}
	case "apikey.InitBearer(\"Authorization\")":
		return &AuthDoc{
			Enabled:         true,
			Kind:            "Bearer token",
			EnvVars:         uniqueStrings([]string{envPrefix + "_TOKEN", envPrefix + "_API_KEY", envPrefix + "_AUTHORIZATION"}),
			ProfileCommand:  "<binary> auth setup",
			ProfileRequired: true,
			Summary:         "The generated CLI supports bearer token authentication from environment variables or a stored profile.",
		}
	default:
		// Any other auto-generated auth init today is API-key based.
		return &AuthDoc{
			Enabled:         true,
			Kind:            "API key",
			EnvVars:         uniqueStrings([]string{envPrefix + "_API_KEY"}),
			ProfileCommand:  "<binary> auth setup",
			ProfileRequired: true,
			Summary:         "The generated CLI supports API key authentication from either environment variables or a stored profile.",
		}
	}
}

func buildREADMEExamples(api *OpenAPI) []*READMEExample {
	binary := api.CommandName
	if binary == "" {
		binary = "my-cli"
	}

	examples := []*READMEExample{
		{
			Title:       "Check setup",
			Command:     binary + " --json doctor",
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
			Description: "Write the preferred output format into the CLI config so future commands use it automatically.",
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

func commandTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"escapeStr": escapeString,
		"slug":      slug,
		"title":     strings.Title,
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

	writeFormattedFile(
		filepath.Join("cli", "generated", shortName+"_client.go"),
		[]byte(renderTemplate("templates/generated_client.tmpl", commandTemplateFuncs(), api)),
	)
	writeFormattedFile(
		filepath.Join("cli", "generated", "register.go"),
		[]byte(renderTemplate("templates/generated_register.tmpl", commandTemplateFuncs(), api)),
	)

	rootCommands := &CommandsTemplateData{
		API:        api,
		Operations: api.Operations,
		Waiters:    api.Waiters,
		NeedsFmt:   commandFileNeedsFmt(api.Operations),
	}
	writeFormattedFile(
		filepath.Join("cli", "generated", "root_commands.go"),
		[]byte(renderTemplate("templates/generated_root_commands.tmpl", commandTemplateFuncs(), rootCommands)),
	)

	for _, group := range api.Groups {
		groupData := &CommandsTemplateData{
			API:        api,
			Group:      group,
			Operations: group.Operations,
			NeedsFmt:   commandFileNeedsFmt(group.Operations),
		}
		filename := filepath.Join("cli", "generated", slug(group.CLIName)+"_commands.go")
		writeFormattedFile(
			filename,
			[]byte(renderTemplate("templates/generated_group_commands.tmpl", commandTemplateFuncs(), groupData)),
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
		"DefaultOutputFormat": config.DefaultOutputFormat,
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
	root := &cobra.Command{}

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
	initCommand.Flags().String("default-format", "json", "Default output format for generated CLIs [json, yaml, toon]")
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
