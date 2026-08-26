// Command next-version decides releases from conventional-commit subjects.
//
// Bartolo reports the module version it was installed with, so the tag is the
// only place a version is decided and this is the only thing that decides one.
// It also owns the judgement of what counts as a conventional commit, so the
// PR-title check and the release derivation cannot disagree about a title.
//
//	next-version                     # print the next tag, or "none"
//	next-version -force patch        # print the tag a forced bump would give
//	next-version -github-output      # key=value lines for $GITHUB_OUTPUT
//	next-version -check-title "..."  # exit non-zero if the title is not conventional
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
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

// typeBumps maps conventional-commit types to the release they warrant.
var typeBumps = map[string]bump{
	"feat":   bumpMinor,
	"fix":    bumpPatch,
	"perf":   bumpPatch,
	"revert": bumpPatch,
}

// knownTypes is every conventional-commit type a PR title may use. A type
// outside typeBumps (docs, chore, ci, refactor, test, style, build) ships in
// whatever release someone else triggers, but triggers none itself — unless it
// is marked breaking.
var knownTypes = map[string]struct{}{
	"feat": {}, "fix": {}, "perf": {}, "revert": {}, "docs": {},
	"chore": {}, "ci": {}, "refactor": {}, "test": {}, "style": {}, "build": {},
}

func main() {
	force := flag.String("force", "", "force a bump level (major|minor|patch) instead of deriving one")
	checkTitle := flag.String("check-title", "", "validate a PR title as a conventional commit and exit")
	githubOutput := flag.Bool("github-output", false, "print key=value lines for $GITHUB_OUTPUT")
	flag.Parse()

	if *checkTitle != "" {
		if _, _, ok := parseSubject(*checkTitle); !ok {
			fmt.Fprintf(os.Stderr, "::error::PR title is not a conventional commit: %q\n", *checkTitle)
			fmt.Fprintf(os.Stderr, "Use \"<type>: <description>\", e.g. \"fix: reject empty path parameters\".\n")
			fmt.Fprintf(os.Stderr, "Types: %s.\n", strings.Join(sortedTypes(), ", "))
			fmt.Fprintf(os.Stderr, "feat -> minor, fix/perf/revert -> patch, \"!\" or a BREAKING CHANGE footer -> major; the rest ship without a release.\n")
			os.Exit(1)
		}
		return
	}

	if *force != "" {
		if _, ok := bumpNames[*force]; !ok {
			fatalf("unknown force level %q: want major, minor or patch", *force)
		}
	}

	current, tagged, err := lastTag()
	if err != nil {
		fatalf("%v", err)
	}

	level := bumpNames[*force]
	if *force == "" {
		messages, err := commitsSince(current, tagged)
		if err != nil {
			fatalf("%v", err)
		}
		level = deriveBump(messages)
	}

	next, released, err := nextVersion(current, tagged, level)
	if err != nil {
		fatalf("%v", err)
	}

	if !*githubOutput {
		if !released {
			fmt.Println("none")
			return
		}
		fmt.Println(next)
		return
	}

	// tag is the ref a release must exist for: the one about to be cut, or the
	// last one, so a release that failed after its tag was pushed is created on
	// the next run instead of staying missing forever.
	tag := current
	if released {
		tag = next
	}
	fmt.Printf("released=%t\n", released)
	fmt.Printf("current=%s\n", current)
	fmt.Printf("next=%s\n", next)
	fmt.Printf("tag=%s\n", tag)
	fmt.Printf("major_bump=%t\n", released && level == bumpMajor)
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
// instead announce the break with a BREAKING CHANGE footer.
func commitBump(message string) bump {
	subject, body, _ := strings.Cut(message, "\n")

	commitType, breaking, ok := parseSubject(subject)
	if !ok {
		return bumpNone
	}
	if breaking || hasBreakingFooter(body) {
		return bumpMajor
	}
	return typeBumps[commitType]
}

// parseSubject splits a conventional-commit subject into its type and breaking
// marker, reporting false for anything that is not one. The PR-title check runs
// the same function, so CI cannot accept a title the release derivation would
// then ignore — a divergence that is invisible until a release quietly does not
// happen.
func parseSubject(subject string) (commitType string, breaking, ok bool) {
	head, description, found := strings.Cut(subject, ":")
	if !found || !strings.HasPrefix(description, " ") || strings.TrimSpace(description) == "" {
		return "", false, false
	}

	breaking = strings.HasSuffix(head, "!")
	head = strings.TrimSuffix(head, "!")

	if open := strings.Index(head, "("); open >= 0 {
		if !strings.HasSuffix(head, ")") {
			return "", false, false
		}
		scope := head[open+1 : len(head)-1]
		if scope == "" || strings.ContainsAny(scope, "()") {
			return "", false, false
		}
		head = head[:open]
	}

	commitType = strings.ToLower(head)
	if _, known := knownTypes[commitType]; !known {
		return "", false, false
	}
	return commitType, breaking, true
}

// hasBreakingFooter looks for the footer in the trailing block of the body, in
// either spelling the Conventional Commits spec allows. Footers live in the
// last blank-line-separated block; searching the whole body also fires on the
// token quoted in prose or inside a code fence, and releasing a major because a
// body mentioned the words is worse than the stricter match. A fence that is
// itself the last block would still count — rare enough to leave alone rather
// than grow a markdown parser in here.
func hasBreakingFooter(body string) bool {
	blocks := strings.Split(strings.TrimSpace(body), "\n\n")
	last := blocks[len(blocks)-1]

	for _, line := range strings.Split(last, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "BREAKING CHANGE:") || strings.HasPrefix(line, "BREAKING-CHANGE:") {
			return true
		}
	}
	return false
}

// nextVersion applies a bump to the current tag. It reports released=false when
// nothing warrants a release, and errors when the current tag cannot be parsed
// — a malformed tag must not read as a quiet "nothing to do".
func nextVersion(current string, tagged bool, level bump) (next string, released bool, err error) {
	var major, minor, patch int
	if tagged {
		major, minor, patch, err = parseVersion(current)
		if err != nil {
			return "", false, err
		}
	}
	if level == bumpNone {
		return "", false, nil
	}

	switch level {
	case bumpMajor:
		major, minor, patch = major+1, 0, 0
	case bumpMinor:
		minor, patch = minor+1, 0
	case bumpPatch:
		patch++
	}
	return fmt.Sprintf("v%d.%d.%d", major, minor, patch), true, nil
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

// lastTag returns the most recent tag reachable from HEAD, reporting false when
// the repository has no tags. Any other git failure is an error rather than a
// fallback: a swallowed one reads as "never released" and would tag v0.0.1 over
// a repository already at v0.4.6.
func lastTag() (tag string, tagged bool, err error) {
	cmd := exec.Command("git", "describe", "--tags", "--abbrev=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "No names found") || strings.Contains(stderr.String(), "No tags can describe") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("git describe: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), true, nil
}

func commitsSince(tag string, tagged bool) ([]string, error) {
	revs := "HEAD"
	if tagged {
		revs = tag + "..HEAD"
	}

	out, err := exec.Command("git", "log", "--format=%B"+recordSep, revs).Output()
	if err != nil {
		return nil, fmt.Errorf("reading commits since %s: %w", revs, err)
	}

	var messages []string
	for _, record := range strings.Split(string(out), recordSep) {
		if trimmed := strings.TrimSpace(record); trimmed != "" {
			messages = append(messages, trimmed)
		}
	}
	return messages, nil
}

func sortedTypes() []string {
	types := make([]string, 0, len(knownTypes))
	for name := range knownTypes {
		types = append(types, name)
	}
	sort.Strings(types)
	return types
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "next-version: "+format+"\n", args...)
	os.Exit(1)
}
