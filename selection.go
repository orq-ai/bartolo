package main

import (
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
)

// CommandSelection is the `commands` block of .bartolo.json. It picks which of
// the spec's operations become CLI commands (and, by default, MCP tools), so
// one spec can produce a narrower CLI, such as a gateway-only one, without
// marking anything in the spec itself.
type CommandSelection struct {
	// Include keeps only the operations it matches. Empty means everything.
	Include *CommandSelector `json:"include,omitempty"`
	// Exclude drops what it matches, applied after Include.
	Exclude *CommandSelector `json:"exclude,omitempty"`
	// MCP is "selected" (the default) to give the MCP server the same
	// operations as the CLI, or "all" to keep it on the full spec.
	MCP string `json:"mcp,omitempty"`
}

// CommandSelector matches operations on any of four attributes. An operation
// matches when any listed value matches it.
type CommandSelector struct {
	// Tags matches an operation's OpenAPI tags, by the name used in the spec.
	Tags []string `json:"tags,omitempty"`
	// Sections matches the resolved help section title, e.g. "AI Gateway".
	Sections []string `json:"sections,omitempty"`
	// Groups matches the resolved command group, e.g. "chat".
	Groups []string `json:"groups,omitempty"`
	// Commands matches a full command path without arguments, e.g.
	// "models list", or the bare name of a root-level command.
	Commands []string `json:"commands,omitempty"`
}

const (
	selectionMCPSelected = "selected"
	selectionMCPAll      = "all"
)

func (s *CommandSelector) empty() bool {
	return s == nil || len(s.Tags)+len(s.Sections)+len(s.Groups)+len(s.Commands) == 0
}

// selectorKind is one attribute a selector matches on.
type selectorKind struct {
	name   string
	values func(*CommandSelector) []string
	of     func(*Operation) []string
}

var selectorKinds = []selectorKind{
	{"tags", func(s *CommandSelector) []string { return s.Tags }, func(o *Operation) []string { return o.Tags }},
	{"sections", func(s *CommandSelector) []string { return s.Sections }, func(o *Operation) []string { return nonEmpty(o.resolvedHelpSection()) }},
	{"groups", func(s *CommandSelector) []string { return s.Groups }, func(o *Operation) []string { return nonEmpty(o.groupName()) }},
	{"commands", func(s *CommandSelector) []string { return s.Commands }, func(o *Operation) []string { return []string{o.selectionPath()} }},
}

func nonEmpty(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

// resolvedHelpSection is the section the operation's command is listed under
// at the top level: its group's section, or its own for a root command.
func (o *Operation) resolvedHelpSection() string {
	if o.Group != nil {
		return o.Group.HelpSection
	}
	return o.HelpSection
}

func (o *Operation) groupName() string {
	if o.Group != nil {
		return o.Group.CLIName
	}
	return ""
}

// selectionPath is the command path a `commands` selector names: the group
// and leaf name, without the positional arguments CommandPath carries.
func (o *Operation) selectionPath() string {
	if o.Group != nil {
		return o.Group.CLIName + " " + o.LeafName
	}
	return o.LeafName
}

func (s *CommandSelector) matches(op *Operation) bool {
	if s == nil {
		return false
	}
	for _, kind := range selectorKinds {
		for _, value := range kind.values(s) {
			if slices.Contains(kind.of(op), value) {
				return true
			}
		}
	}
	return false
}

func (c *CommandSelection) check() error {
	if c == nil {
		return nil
	}
	switch c.MCP {
	case "", selectionMCPSelected, selectionMCPAll:
		return nil
	default:
		return fmt.Errorf(".bartolo.json commands.mcp is %q; use %q or %q", c.MCP, selectionMCPSelected, selectionMCPAll)
	}
}

// applyCommandSelection marks every operation the selection leaves out as
// CLI-excluded, like x-bartolo-cli-ignore: its command is not generated but
// its client function is, so hand-written code calling it still compiles.
// Unless commands.mcp is "all", the MCP tool goes too. A selector value that
// matches no operation is an error, so a renamed tag, section or group cannot
// silently produce an empty CLI.
func applyCommandSelection(api *OpenAPI, selection *CommandSelection) error {
	if selection == nil || (selection.Include.empty() && selection.Exclude.empty()) {
		return nil
	}
	if err := selection.check(); err != nil {
		return err
	}

	ops := api.allOperations()
	var problems []string
	problems = append(problems, unmatchedSelectorValues("include", selection.Include, ops)...)
	problems = append(problems, unmatchedSelectorValues("exclude", selection.Exclude, ops)...)
	if len(problems) > 0 {
		return fmt.Errorf(".bartolo.json commands: %s", strings.Join(problems, "; "))
	}

	dropMCP := selection.MCP != selectionMCPAll
	for _, op := range ops {
		included := selection.Include.empty() || selection.Include.matches(op)
		if included && !selection.Exclude.matches(op) {
			continue
		}
		op.Unselected = true
		op.CLIExcluded = true
		if dropMCP && op.MCP != nil && !op.MCP.Excluded {
			op.MCP.Excluded = true
			op.MCP.ExcludedReason = "not selected by .bartolo.json commands"
		}
	}

	dropUnselectedWaiters(api)
	if !api.hasCLICommands() {
		return fmt.Errorf(".bartolo.json commands: the selection leaves no CLI commands")
	}
	return nil
}

// unmatchedSelectorValues names every value in selector that matches no
// operation, with the values that do exist when there are few enough to read.
func unmatchedSelectorValues(field string, selector *CommandSelector, ops []*Operation) []string {
	if selector.empty() {
		return nil
	}
	var problems []string
	for _, kind := range selectorKinds {
		known := map[string]bool{}
		for _, op := range ops {
			for _, value := range kind.of(op) {
				known[value] = true
			}
		}
		for _, value := range kind.values(selector) {
			if known[value] {
				continue
			}
			problem := fmt.Sprintf("%s.%s %q matches no operation", field, kind.name, value)
			if kind.name != "commands" {
				problem += " (known: " + strings.Join(sortedKeys(known), ", ") + ")"
			}
			problems = append(problems, problem)
		}
	}
	return problems
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// dropUnselectedWaiters removes the waiters whose operation the selection
// left out, and the follow-up flags other operations carry for them, since
// those flags call the waiter's command.
func dropUnselectedWaiters(api *OpenAPI) {
	dropped := map[*Waiter]bool{}
	kept := api.Waiters[:0]
	for _, waiter := range api.Waiters {
		if waiter.Operation != nil && waiter.Operation.Unselected {
			dropped[waiter] = true
			continue
		}
		kept = append(kept, waiter)
	}
	api.Waiters = kept
	if len(dropped) == 0 {
		return
	}
	for _, op := range api.allOperations() {
		op.Waiters = slices.DeleteFunc(op.Waiters, func(w *WaiterParams) bool { return dropped[w.Waiter] })
	}
}

// allOperations returns every operation in the IR, root ones first.
func (o *OpenAPI) allOperations() []*Operation {
	ops := slices.Clone(o.Operations)
	for _, group := range o.Groups {
		ops = append(ops, group.Operations...)
	}
	return ops
}

func (o *OpenAPI) hasCLICommands() bool {
	return len(o.CLIOperations()) > 0 || len(o.CLIGroups()) > 0
}

// CLIGroups returns the groups that generate a command, for the README and
// examples, which should not list a group whose commands were all left out.
func (o *OpenAPI) CLIGroups() []*CommandGroup {
	var groups []*CommandGroup
	for _, group := range o.Groups {
		if group.HasCLICommands() {
			groups = append(groups, group)
		}
	}
	return groups
}

// CLIOperations returns the root operations that generate a command.
func (o *OpenAPI) CLIOperations() []*Operation {
	return cliVisibleOperations(o.Operations)
}

// helpSectionOrder resolves the help_sections list from .bartolo.json into the
// sections the generated Register declares up front, in that order. A listed
// section no generated command uses is kept, because the consuming CLI may
// fill it with hand-written commands, but it is reported: if nothing fills it,
// it prints as an empty heading.
func helpSectionOrder(api *OpenAPI, listed []string) []string {
	used := map[string]bool{}
	for _, op := range api.CLIOperations() {
		if op.HelpSection != "" {
			used[op.HelpSection] = true
		}
	}
	for _, group := range api.CLIGroups() {
		if group.HelpSection != "" {
			used[group.HelpSection] = true
		}
	}

	var order []string
	for _, title := range listed {
		title = strings.TrimSpace(title)
		if title == "" || slices.Contains(order, title) {
			continue
		}
		if !used[title] {
			log.Printf("warning: help_sections lists %q, which no generated command uses; it stays empty unless the CLI adds commands to it", title)
		}
		order = append(order, title)
	}
	return order
}
