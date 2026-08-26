package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCommitBump(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    bump
	}{
		{"feature", "feat: add cli.ExecuteContext", bumpMinor},
		{"fix", "fix: reject empty path parameters", bumpPatch},
		{"perf", "perf: cache the parsed schema", bumpPatch},
		{"scope", "feat(templates): escape path parameters", bumpMinor},
		{"squashed PR title", "fix: escape path parameters (#21)", bumpPatch},
		{"uppercase type", "Fix: escape path parameters", bumpPatch},
		{"colon in the description", "fix: escape this: and that", bumpPatch},
		{"breaking marker", "feat!: drop the --query flag", bumpMajor},
		{"breaking marker with scope", "refactor(cli)!: rename GetBody", bumpMajor},
		{"breaking footer", "feat: rework --example\n\nBREAKING CHANGE: cli.GetBody lost a parameter", bumpMajor},
		{"hyphenated breaking footer", "feat: rework --example\n\nBREAKING-CHANGE: cli.GetBody lost a parameter", bumpMajor},
		{"release-neutral type", "docs: document the release flow", bumpNone},
		{"chore", "chore: bump dependencies", bumpNone},
		{"unconventional subject", "Add cli.ExecuteContext", bumpNone},
		{"unknown type", "wip: still thinking", bumpNone},
		{"breaking footer needs a known type", "wip: still thinking\n\nBREAKING CHANGE: everything", bumpNone},
		{"unterminated scope", "feat(cli: escape path parameters", bumpNone},
		{"empty scope", "feat(): escape path parameters", bumpNone},
		{"nested scope", "feat(a)(b): escape path parameters", bumpNone},
		{"missing space after the colon", "feat:add a flag", bumpNone},
		{"empty description", "feat: ", bumpNone},
		{"leading whitespace", " feat: add a flag", bumpNone},
		// The bang belongs after the scope. Rejecting the reversed form is
		// correct; the test pins it so the silent downgrade to bumpNone is a
		// deliberate answer rather than an accident of the parse order.
		{"bang before the scope", "feat!(cli): drop the --query flag", bumpNone},
		// A body quoting the footer is prose, not a trailer.
		{"breaking token quoted in prose", "fix: document the release rules\n\nThe README explains that a BREAKING CHANGE: footer cuts a major.", bumpPatch},
		{"breaking token in a code fence", "fix: document the release rules\n\n```\nBREAKING CHANGE: example\n```\n\nThat is all.", bumpPatch},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commitBump(tc.message); got != tc.want {
				t.Errorf("commitBump(%q) = %v, want %v", tc.message, got, tc.want)
			}
		})
	}
}

// TestParseSubjectMatchesCommitBump guards the single-parser property: the
// PR-title check and the release derivation must agree on every subject, or CI
// accepts a title that then produces no release.
func TestParseSubjectMatchesCommitBump(t *testing.T) {
	subjects := []string{
		"feat: add a flag", "Fix: escape it", "feat(cli): add a flag",
		"feat!: drop a flag", "chore: bump deps", "docs: tidy",
		"feat(): add a flag", "feat:add a flag", " feat: add a flag",
		"Add cli.ExecuteContext", "wip: thinking", "feat(a)(b): add a flag",
		"feat: ", "feat(cli: add a flag",
	}
	for _, subject := range subjects {
		_, _, conventional := parseSubject(subject)
		releases := commitBump(subject) != bumpNone
		if !conventional && releases {
			t.Errorf("%q releases but is rejected by the title check", subject)
		}
	}
}

func TestDeriveBumpTakesTheLargest(t *testing.T) {
	messages := []string{
		"docs: tidy the readme",
		"fix: escape path parameters",
		"feat: add a release workflow",
		"chore: bump dependencies",
	}
	if got := deriveBump(messages); got != bumpMinor {
		t.Errorf("deriveBump() = %v, want %v", got, bumpMinor)
	}
	if got := deriveBump(nil); got != bumpNone {
		t.Errorf("deriveBump(nil) = %v, want %v", got, bumpNone)
	}
}

func TestNextVersion(t *testing.T) {
	cases := []struct {
		current     string
		tagged      bool
		level       bump
		want        string
		wantRelease bool
		wantErr     bool
	}{
		{"v0.4.6", true, bumpPatch, "v0.4.7", true, false},
		{"v0.4.6", true, bumpMinor, "v0.5.0", true, false},
		{"v0.4.6", true, bumpMajor, "v1.0.0", true, false},
		{"v0.4.6", true, bumpNone, "", false, false},
		// A repository with no tags at all starts from zero.
		{"", false, bumpPatch, "v0.0.1", true, false},
		{"", false, bumpNone, "", false, false},
		// A prerelease tag bumps off its release version, not its suffix.
		{"v1.2.3-rc.1", true, bumpPatch, "v1.2.4", true, false},
		// An unparseable tag is an error even when nothing warrants a release,
		// so a broken tag cannot masquerade as a quiet no-op.
		{"not-a-version", true, bumpPatch, "", false, true},
		{"not-a-version", true, bumpNone, "", false, true},
	}

	for _, tc := range cases {
		got, released, err := nextVersion(tc.current, tc.tagged, tc.level)
		if (err != nil) != tc.wantErr || got != tc.want || released != tc.wantRelease {
			t.Errorf("nextVersion(%q, %t, %v) = (%q, %t, %v), want (%q, %t, err=%t)",
				tc.current, tc.tagged, tc.level, got, released, err, tc.want, tc.wantRelease, tc.wantErr)
		}
	}
}

// TestLastTagAndCommitsSince exercises the git-shelling half against a real
// repository. The pure functions above never see what `git log --format=%B`
// actually emits, and the untagged path is the one that would tag v0.0.1 over a
// released repository if it were reached by mistake.
func TestLastTagAndCommitsSince(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "chore: first")

	// Run from the temp repository; both functions shell out to git in the
	// process working directory.
	t.Chdir(dir)

	tag, tagged, err := lastTag()
	if err != nil || tagged || tag != "" {
		t.Fatalf("lastTag() on an untagged repo = (%q, %t, %v), want (\"\", false, nil)", tag, tagged, err)
	}

	messages, err := commitsSince("", false)
	if err != nil || len(messages) != 1 {
		t.Fatalf("commitsSince(untagged) = (%v, %v), want 1 message", messages, err)
	}

	git("tag", "v0.4.6")
	git("commit", "-q", "--allow-empty", "-m", "feat: add a thing\n\nBREAKING CHANGE: it broke")

	tag, tagged, err = lastTag()
	if err != nil || !tagged || tag != "v0.4.6" {
		t.Fatalf("lastTag() = (%q, %t, %v), want (\"v0.4.6\", true, nil)", tag, tagged, err)
	}

	messages, err = commitsSince(tag, true)
	if err != nil {
		t.Fatalf("commitsSince: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("commitsSince = %d messages, want 1", len(messages))
	}
	// The body must survive the record split, or the breaking footer is lost.
	if got := deriveBump(messages); got != bumpMajor {
		t.Errorf("deriveBump(%q) = %v, want %v", messages, got, bumpMajor)
	}
}

// TestLastTagFailsLoudly pins the difference between "no tags" and "git
// failed". Swallowing the second is what would tag v0.0.1 over a released
// repository.
func TestLastTagFailsLoudly(t *testing.T) {
	t.Chdir(filepath.Join(t.TempDir()))

	if _, _, err := lastTag(); err == nil {
		t.Error("lastTag() outside a git repository returned no error")
	}
}
