package main

import (
	"sort"
	"strings"
	"testing"
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
	api := ProcessAPI("example", loadTestSpec(t, selectionSpec))
	if err := api.Err(); err != nil {
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

func TestUnselectedOperationsKeepClientButLoseMCPToolByDefault(t *testing.T) {
	api := selectionAPI(t)
	if err := applyCommandSelection(api, &CommandSelection{Include: &CommandSelector{Groups: []string{"chat"}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	agents := operationsByRoute(api)["GET /agents"]
	if !agents.CLIExcluded || !agents.Unselected {
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

func TestCommandSelectionDropsWaitersOfUnselectedOperations(t *testing.T) {
	api := selectionAPI(t)
	if len(api.Waiters) != 1 || len(operationsByRoute(api)["POST /chat/completions/{id}/cancel"].Waiters) != 1 {
		t.Fatal("fixture should have one waiter followed up by CancelCompletion")
	}
	if err := applyCommandSelection(api, &CommandSelection{Exclude: &CommandSelector{Commands: []string{"chat get"}}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(api.Waiters) != 0 {
		t.Fatal("a waiter on an unselected operation should be dropped")
	}
	if n := len(operationsByRoute(api)["POST /chat/completions/{id}/cancel"].Waiters); n != 0 {
		t.Fatalf("the follow-up flags for a dropped waiter should go too, %d left", n)
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
	api := ProcessAPI("example", doc)
	if err := api.Err(); err != nil {
		t.Fatalf("process: %v", err)
	}
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
	err := ProcessAPI("example", doc).Err()
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
