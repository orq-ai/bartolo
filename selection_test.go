package main

import (
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// selectionSpec has two sectioned groups, a group whose x-cli-group names no
// tag (so its section comes from its own tag), a root command, and a waiter on
// a gateway operation that another operation follows up with.
const selectionSpec = `
openapi: 3.0.3
info:
  title: Selection API
  version: "1"
tags:
  - name: Chat
    x-cli-help-section: AI Gateway
  - name: Audio
    x-cli-help-section: AI Gateway
  - name: Agents
    x-cli-help-section: Managed agents
x-bartolo-waiters:
  chat-done:
    operationId: GetCompletion
    after:
      CancelCompletion:
        id: response#id
    matchers:
      - select: response.body#status
        expected: done
paths:
  /chat/completions:
    post:
      operationId: CreateCompletion
      tags: [Chat]
      x-cli-group: chat
      x-cli-name: create
      responses:
        "200":
          description: ok
  /chat/completions/{id}:
    get:
      operationId: GetCompletion
      tags: [Chat]
      x-cli-group: chat
      x-cli-name: get
      parameters:
        - name: id
          in: path
          required: true
          schema: {type: string}
      responses:
        "200":
          description: ok
  /chat/completions/{id}/cancel:
    post:
      operationId: CancelCompletion
      tags: [Chat]
      x-cli-group: chat
      x-cli-name: cancel
      parameters:
        - name: id
          in: path
          required: true
          schema: {type: string}
      responses:
        "200":
          description: ok
  /audio/speech:
    post:
      operationId: CreateSpeech
      tags: [Audio]
      x-cli-group: speech
      x-cli-name: create
      responses:
        "200":
          description: ok
  /agents:
    get:
      operationId: ListAgents
      tags: [Agents]
      x-cli-group: agents
      x-cli-name: list
      responses:
        "200":
          description: ok
    post:
      operationId: CreateAgent
      tags: [Agents]
      x-cli-group: agents
      x-cli-name: create
      responses:
        "200":
          description: ok
  /ping:
    get:
      operationId: Ping
      x-cli-group: ""
      x-cli-help-section: Utilities
      responses:
        "200":
          description: ok
`

func selectionAPI(t *testing.T) *OpenAPI {
	t.Helper()
	return mustProcessAPI(t, "example", loadTestSpec(t, selectionSpec))
}

// mustProcessAPI builds the IR with no .bartolo.json selection, failing the
// test on a spec error.
func mustProcessAPI(t testing.TB, shortName string, doc *openapi3.T) *OpenAPI {
	t.Helper()
	api, err := ProcessAPI(shortName, doc, nil)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	return api
}

// selectedCommands lists the command paths a selection keeps, sorted.
func selectedCommands(api *OpenAPI) string {
	var out []string
	for _, op := range api.allOperations() {
		if !op.CLIExcluded {
			out = append(out, op.selectionPath())
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func TestCommandSelectionMatchesEachAttribute(t *testing.T) {
	cases := []struct {
		name      string
		selection *CommandSelection
		want      string
	}{
		{"no selection keeps everything", nil, "agents create, agents list, chat cancel, chat create, chat get, ping ping, speech create"},
		{"section", &CommandSelection{Include: &CommandSelector{Sections: []string{"AI Gateway"}}}, "chat cancel, chat create, chat get, speech create"},
		{"tag", &CommandSelection{Include: &CommandSelector{Tags: []string{"Audio"}}}, "speech create"},
		{"group", &CommandSelection{Include: &CommandSelector{Groups: []string{"agents"}}}, "agents create, agents list"},
		{"command", &CommandSelection{Include: &CommandSelector{Commands: []string{"agents list", "ping ping"}}}, "agents list, ping ping"},
		{"union of attributes", &CommandSelection{Include: &CommandSelector{Tags: []string{"Audio"}, Groups: []string{"agents"}}}, "agents create, agents list, speech create"},
		{"exclude after include", &CommandSelection{
			Include: &CommandSelector{Sections: []string{"AI Gateway"}},
			Exclude: &CommandSelector{Commands: []string{"chat get"}},
		}, "chat cancel, chat create, speech create"},
		{"exclude alone", &CommandSelection{Exclude: &CommandSelector{Sections: []string{"Managed agents"}}}, "chat cancel, chat create, chat get, ping ping, speech create"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := selectionAPI(t)
			if err := applyCommandSelection(api, tc.selection); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if got := selectedCommands(api); got != tc.want {
				t.Fatalf("selected %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCommandSelectionRejectsSelectorsThatMatchNothing(t *testing.T) {
	api := selectionAPI(t)
	err := applyCommandSelection(api, &CommandSelection{
		Include: &CommandSelector{Sections: []string{"AI gateway"}, Commands: []string{"ocr ocr"}},
		Exclude: &CommandSelector{Groups: []string{"chats"}},
	})
	if err == nil {
		t.Fatal("expected unmatched selectors to fail generation")
	}
	for _, want := range []string{
		`include.sections "AI gateway" matches no operation (known: AI Gateway, Managed agents, Utilities)`,
		`include.commands "ocr ocr" matches no operation`,
		`exclude.groups "chats" matches no operation`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestCommandSelectionThatLeavesNothingFails(t *testing.T) {
	api := selectionAPI(t)
	err := applyCommandSelection(api, &CommandSelection{Exclude: &CommandSelector{Tags: []string{"Chat", "Audio", "Agents"}, Commands: []string{"ping ping"}}})
	if err == nil || !strings.Contains(err.Error(), "leaves no CLI commands") {
		t.Fatalf("expected an empty selection to fail, got %v", err)
	}
}

func TestCommandSelectionRejectsUnknownMCPMode(t *testing.T) {
	api := selectionAPI(t)
	err := applyCommandSelection(api, &CommandSelection{Include: &CommandSelector{Groups: []string{"chat"}}, MCP: "some"})
	if err == nil || !strings.Contains(err.Error(), `commands.mcp is "some"`) {
		t.Fatalf("expected an unknown mcp mode to fail, got %v", err)
	}
}

func TestCommandSelectionRejectsUnknownMCPModeWithoutSelectors(t *testing.T) {
	api := selectionAPI(t)
	if err := applyCommandSelection(api, &CommandSelection{MCP: "bogus"}); err == nil {
		t.Fatal("an unknown mcp mode should fail even when nothing is selected")
	}
}

// The README must not suggest a command the selection removed, including the
// raw-request example, which picks any operation's path.
func TestREADMEExamplesSkipUnselectedOperations(t *testing.T) {
	api := selectionAPI(t)
	if err := applyCommandSelection(api, &CommandSelection{Include: &CommandSelector{Groups: []string{"speech"}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if op := pickRawRequestOperation(api); op == nil || op.Path != "/audio/speech" {
		t.Fatalf("raw-request example should use a selected operation, got %+v", op)
	}

	api = selectionAPI(t)
	if err := applyCommandSelection(api, &CommandSelection{Include: &CommandSelector{Commands: []string{"agents list"}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if op := pickRawRequestOperation(api); op == nil || op.Path != "/agents" || !strings.EqualFold(op.Method, "GET") {
		t.Fatalf("raw-request example should use the selected GET /agents, got %+v", op)
	}
}

// A group whose x-cli-group names no declared tag takes its beta marker from
// its own tag, on both the short line and the long description.
func TestOwnTagFallbackMarksBetaLikeDeclaredTag(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info: {title: T, version: "1"}
tags:
  - name: Audio
    x-cli-help-section: AI Gateway
    x-bartolo-beta: true
    x-cli-hidden: true
paths:
  /audio/speech:
    post:
      operationId: CreateSpeech
      tags: [Audio]
      x-cli-group: speech
      responses: {"200": {description: ok}}
`)
	api := mustProcessAPI(t, "example", doc)
	group := api.Groups[0]
	if group.HelpSection != "AI Gateway" || !group.Hidden || !group.Beta {
		t.Fatalf("expected section, hidden and beta from the own tag, got %q hidden=%v beta=%v", group.HelpSection, group.Hidden, group.Beta)
	}
	if !strings.HasPrefix(group.Short, "Speech") || group.Short == "Speech" {
		t.Fatalf("expected the group's own name with a beta marker, got %q", group.Short)
	}
}

func TestUnselectedOperationsKeepClientButLoseMCPToolByDefault(t *testing.T) {
	api := selectionAPI(t)
	if err := applyCommandSelection(api, &CommandSelection{Include: &CommandSelector{Groups: []string{"chat"}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	agents := operationsByRoute(api)["GET /agents"]
	if !agents.CLIExcluded {
		t.Fatal("an unselected operation should be CLI-excluded")
	}
	if agents.MCP == nil || !agents.MCP.Excluded {
		t.Fatal("an unselected operation should lose its MCP tool unless commands.mcp is all")
	}
	if agents.GoName == "" {
		t.Fatal("an unselected operation keeps its client function name")
	}
	for _, group := range api.CLIGroups() {
		if group.CLIName == "agents" {
			t.Fatal("a group with no selected commands should not be listed")
		}
	}
}

func TestCommandSelectionMCPAllKeepsEveryTool(t *testing.T) {
	api := selectionAPI(t)
	if err := applyCommandSelection(api, &CommandSelection{Include: &CommandSelector{Groups: []string{"chat"}}, MCP: "all"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	agents := operationsByRoute(api)["GET /agents"]
	if !agents.CLIExcluded {
		t.Fatal("the CLI should still follow the selection")
	}
	if agents.MCP == nil || agents.MCP.Excluded {
		t.Fatal("commands.mcp all should keep the MCP tool")
	}
}

// A waiter on an unselected operation loses its `wait` command but keeps its
// client function, which the --wait-* flag on a selected operation still calls.
func TestCommandSelectionDropsOnlyTheWaitCommandOfUnselectedOperations(t *testing.T) {
	api := selectionAPI(t)
	if len(api.Waiters) != 1 || len(operationsByRoute(api)["POST /chat/completions/{id}/cancel"].Waiters) != 1 {
		t.Fatal("fixture should have one waiter followed up by CancelCompletion")
	}
	if err := applyCommandSelection(api, &CommandSelection{Exclude: &CommandSelector{Commands: []string{"chat get"}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(api.Waiters) != 1 {
		t.Fatal("the waiter's client function should stay")
	}
	if n := len(cliVisibleWaiters(api.Waiters)); n != 0 {
		t.Fatalf("the waiter's wait command should go, %d left", n)
	}
	if n := len(operationsByRoute(api)["POST /chat/completions/{id}/cancel"].Waiters); n != 1 {
		t.Fatalf("the follow-up flag on the selected operation should stay, %d left", n)
	}
}

// x-bartolo-cli-ignore and selection drop the same part of a waiter.
func TestCLIIgnoredOperationLosesWaitCommandOnly(t *testing.T) {
	doc := loadTestSpec(t, strings.Replace(selectionSpec, "      operationId: GetCompletion\n", "      operationId: GetCompletion\n      x-bartolo-cli-ignore: true\n", 1))
	api := mustProcessAPI(t, "example", doc)
	if len(api.Waiters) != 1 || len(cliVisibleWaiters(api.Waiters)) != 0 {
		t.Fatalf("expected the waiter kept with no wait command, got %d waiters, %d commands", len(api.Waiters), len(cliVisibleWaiters(api.Waiters)))
	}
}

// A grouped operation's section places its group. It no longer also puts the
// command under a heading inside the group's own help.
func TestOperationSectionPlacesGroupNotLeaf(t *testing.T) {
	api := selectionAPI(t)
	routes := operationsByRoute(api)
	if got := routes["GET /agents"].HelpSection; got != "" {
		t.Fatalf("grouped operation should carry no leaf section, got %q", got)
	}
	if got := routes["GET /ping"].HelpSection; got != "" {
		// /ping has x-cli-group "" and infers group ping from its path.
		t.Fatalf("path-grouped operation should carry no leaf section, got %q", got)
	}
	sections := map[string]string{}
	for _, group := range api.Groups {
		sections[group.CLIName] = group.HelpSection
	}
	want := map[string]string{"chat": "AI Gateway", "speech": "AI Gateway", "agents": "Managed agents", "ping": "Utilities"}
	for name, section := range want {
		if sections[name] != section {
			t.Errorf("group %s: section %q, want %q", name, sections[name], section)
		}
	}
}

// speech's x-cli-group names no declared tag, so its section comes from its
// own Audio tag, but not the tag's name.
func TestGroupFallsBackToOwnTagForSection(t *testing.T) {
	api := selectionAPI(t)
	for _, group := range api.Groups {
		if group.CLIName == "speech" && group.Short != "Speech" {
			t.Fatalf("fallback should not take the tag's name as the group's short, got %q", group.Short)
		}
	}
}

func TestOperationSectionFromAnyOperationInGroup(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info: {title: T, version: "1"}
paths:
  /a:
    get:
      operationId: ListWidgets
      x-cli-group: widgets
      responses: {"200": {description: ok}}
  /b:
    get:
      operationId: GetWidget
      x-cli-group: widgets
      x-cli-help-section: Storage
      responses: {"200": {description: ok}}
`)
	api := mustProcessAPI(t, "example", doc)
	if got := api.Groups[0].HelpSection; got != "Storage" {
		t.Fatalf("a later operation's section should still place the group, got %q", got)
	}
}

func TestConflictingOperationSectionsFail(t *testing.T) {
	doc := loadTestSpec(t, `
openapi: 3.0.3
info: {title: T, version: "1"}
paths:
  /a:
    get:
      operationId: ListWidgets
      x-cli-group: widgets
      x-cli-help-section: Storage
      responses: {"200": {description: ok}}
  /b:
    get:
      operationId: GetWidget
      x-cli-group: widgets
      x-cli-help-section: Compute
      responses: {"200": {description: ok}}
`)
	_, err := ProcessAPI("example", doc, nil)
	if err == nil {
		t.Fatal("expected disagreeing sections in one group to fail")
	}
	want := `group "widgets": GET /b sets help section "Compute" but GET /a sets "Storage"`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}

func TestHelpSectionOrderKeepsListedOrder(t *testing.T) {
	api := selectionAPI(t)
	got := helpSectionOrder(api, []string{"Utilities", "AI Gateway", " ", "Utilities", "Get started"})
	if want := "Utilities|AI Gateway|Get started"; strings.Join(got, "|") != want {
		t.Fatalf("order %q, want %q", strings.Join(got, "|"), want)
	}
}

func TestRegisterDeclaresHelpSectionsBeforeCommands(t *testing.T) {
	api := selectionAPI(t)
	api.HelpSectionOrder = []string{"AI Gateway", "Managed agents"}
	out := renderCommandTemplate("templates/generated_register.tmpl", api)
	declare := strings.Index(out, `bartolocli.DeclareHelpSections(root, "AI Gateway", "Managed agents")`)
	register := strings.Index(out, "registerRootCommands(root)")
	if declare < 0 || register < 0 || declare > register {
		t.Fatalf("expected the sections declared before any command registers:\n%s", out)
	}

	api.HelpSectionOrder = nil
	if strings.Contains(renderCommandTemplate("templates/generated_register.tmpl", api), "DeclareHelpSections") {
		t.Fatal("no help_sections should declare nothing")
	}
}

const conflictSpec = `
openapi: 3.0.3
info: {title: T, version: "1"}
paths:
  /a:
    get:
      operationId: ListWidgets
      x-cli-group: widgets
      x-cli-help-section: Storage
      responses: {"200": {description: ok}}
  /b:
    get:
      operationId: GetWidget
      x-cli-group: widgets
      x-cli-help-section: Compute
      responses: {"200": {description: ok}}
  /c:
    get:
      operationId: ListGadgets
      x-cli-group: gadgets
      responses: {"200": {description: ok}}
`

// A subset build is not blocked by a conflict in a group it leaves out.
func TestSectionConflictInUnselectedGroupDoesNotFail(t *testing.T) {
	config := &ProjectConfig{Commands: &CommandSelection{Include: &CommandSelector{Groups: []string{"gadgets"}}}}
	if _, err := ProcessAPI("example", loadTestSpec(t, conflictSpec), config); err != nil {
		t.Fatalf("a conflict outside the selection should not fail: %v", err)
	}
	config = &ProjectConfig{Commands: &CommandSelection{Include: &CommandSelector{Commands: []string{"widgets get"}}}}
	if _, err := ProcessAPI("example", loadTestSpec(t, conflictSpec), config); err == nil {
		t.Fatal("a conflict in a group that still generates a command should fail")
	}
}

// A cli-ignored operation has no command, so its section does not place or
// conflict with its group.
func TestCLIIgnoredOperationSectionIsIgnored(t *testing.T) {
	spec := strings.Replace(conflictSpec, "      x-cli-help-section: Compute\n", "      x-cli-help-section: Compute\n      x-bartolo-cli-ignore: true\n", 1)
	api := mustProcessAPI(t, "example", loadTestSpec(t, spec))
	for _, group := range api.Groups {
		if group.CLIName == "widgets" && group.HelpSection != "Storage" {
			t.Fatalf("expected the CLI-visible operation's section, got %q", group.HelpSection)
		}
	}
}

func TestProcessAPIAppliesHelpSectionsFromConfig(t *testing.T) {
	config := &ProjectConfig{HelpSections: []string{"Managed agents", "AI Gateway"}}
	api, err := ProcessAPI("example", loadTestSpec(t, selectionSpec), config)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := strings.Join(api.HelpSectionOrder, "|"); got != "Managed agents|AI Gateway" {
		t.Fatalf("order %q", got)
	}
}
