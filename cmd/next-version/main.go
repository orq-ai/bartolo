// Command next-version computes the next release tag from the
// conventional-commit subjects since the last tag.
//
// Bartolo reports the module version it was installed with, so the tag is the
// only place a version is decided and this is the only thing that decides one.
// It prints the next tag ("v0.4.7") to stdout, or "none" when nothing since the
// last tag warrants a release.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// recordSep separates commit messages in the git log output. Commit bodies
// contain newlines, so a line-based split would tear them apart.
const recordSep = "\x1e"

type bump int

const (
	bumpNone bump = iota
	bumpPatch
	bumpMinor
	bumpMajor
)

var bumpNames = map[string]bump{
	"major": bumpMajor,
	"minor": bumpMinor,
	"patch": bumpPatch,
}

// typeBumps maps conventional-commit types to the release they warrant. Types
// that are absent (docs, chore, ci, refactor, test, style, build) ship in the
// next release someone else triggers, but do not trigger one themselves.
var typeBumps = map[string]bump{
	"feat":   bumpMinor,
	"fix":    bumpPatch,
	"perf":   bumpPatch,
	"revert": bumpPatch,
}

func main() {
	force := flag.String("force", "", "force a bump level (major|minor|patch) instead of deriving one")
	flag.Parse()

	if *force != "" {
		if _, ok := bumpNames[*force]; !ok {
			fatalf("unknown force level %q: want major, minor or patch", *force)
		}
	}

	current, err := lastTag()
	if err != nil {
		fatalf("%v", err)
	}

	level := bumpNames[*force]
	if *force == "" {
		messages, err := commitsSince(current)
		if err != nil {
			fatalf("%v", err)
		}
		level = deriveBump(messages)
	}

	next, ok := nextVersion(current, level)
	if !ok {
		fmt.Println("none")
		return
	}
	fmt.Println(next)
}

// deriveBump returns the largest release any of the commits warrants.
func deriveBump(messages []string) bump {
	highest := bumpNone
	for _, message := range messages {
		if level := commitBump(message); level > highest {
			highest = level
		}
	}
	return highest
}

// commitBump reads a single commit message. The subject carries the type and
// the optional "!" breaking marker ("feat(cli)!: drop --query"); the body may
// instead announce the break with a "BREAKING CHANGE:" footer. Requiring a
// known type before honouring either means a stray "wip: BREAKING CHANGE"
// note cannot release a major.
func commitBump(message string) bump {
	subject, body, _ := strings.Cut(message, "\n")

	head, _, ok := strings.Cut(subject, ":")
	if !ok {
		return bumpNone
	}
	breaking := strings.HasSuffix(head, "!")
	head = strings.TrimSuffix(head, "!")

	// Drop an optional scope: "feat(cli)" -> "feat".
	if scope := strings.Index(head, "("); scope >= 0 {
		if !strings.HasSuffix(head, ")") {
			return bumpNone
		}
		head = head[:scope]
	}

	commitType := strings.ToLower(strings.TrimSpace(head))
	if _, known := knownTypes[commitType]; !known {
		return bumpNone
	}
	if breaking || strings.Contains(body, "BREAKING CHANGE:") {
		return bumpMajor
	}
	return typeBumps[commitType]
}

// knownTypes is every conventional-commit type the PR title check accepts. A
// type outside typeBumps (docs, chore, ci, refactor, test, style, build) ships
// in whatever release someone else triggers, but triggers none itself — unless
// it is marked breaking.
var knownTypes = map[string]struct{}{
	"feat": {}, "fix": {}, "perf": {}, "revert": {}, "docs": {},
	"chore": {}, "ci": {}, "refactor": {}, "test": {}, "style": {}, "build": {},
}

// nextVersion applies a bump to a "vX.Y.Z" tag. It reports false when there is
// nothing to release.
func nextVersion(current string, level bump) (string, bool) {
	if level == bumpNone {
		return "", false
	}

	major, minor, patch, err := parseVersion(current)
	if err != nil {
		return "", false
	}

	switch level {
	case bumpMajor:
		major, minor, patch = major+1, 0, 0
	case bumpMinor:
		minor, patch = minor+1, 0
	case bumpPatch:
		patch++
	}
	return fmt.Sprintf("v%d.%d.%d", major, minor, patch), true
}

// parseVersion reads "vX.Y.Z", ignoring any prerelease or build suffix.
func parseVersion(tag string) (major, minor, patch int, err error) {
	trimmed := strings.TrimPrefix(tag, "v")
	if cut := strings.IndexAny(trimmed, "-+"); cut >= 0 {
		trimmed = trimmed[:cut]
	}

	parts := strings.Split(trimmed, ".")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("tag %q is not vX.Y.Z", tag)
	}
	out := make([]int, 3)
	for i, part := range parts {
		out[i], err = strconv.Atoi(part)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("tag %q is not vX.Y.Z", tag)
		}
	}
	return out[0], out[1], out[2], nil
}

// lastTag returns the most recent reachable tag, or v0.0.0 for a repository
// that has never been released.
func lastTag() (string, error) {
	out, err := exec.Command("git", "describe", "--tags", "--abbrev=0").Output()
	if err != nil {
		return "v0.0.0", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func commitsSince(tag string) ([]string, error) {
	revs := tag + "..HEAD"
	if tag == "v0.0.0" {
		revs = "HEAD"
	}
	out, err := exec.Command("git", "log", "--format=%B"+recordSep, revs).Output()
	if err != nil {
		return nil, fmt.Errorf("reading commits since %s: %w", tag, err)
	}

	var messages []string
	for _, record := range strings.Split(string(out), recordSep) {
		if trimmed := strings.TrimSpace(record); trimmed != "" {
			messages = append(messages, trimmed)
		}
	}
	return messages, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "next-version: "+format+"\n", args...)
	os.Exit(1)
}
