package main

import "testing"

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
		{"breaking marker", "feat!: drop the --query flag", bumpMajor},
		{"breaking marker with scope", "refactor(cli)!: rename GetBody", bumpMajor},
		{"breaking footer", "feat: rework --example\n\nBREAKING CHANGE: cli.GetBody lost a parameter", bumpMajor},
		{"release-neutral type", "docs: document the release flow", bumpNone},
		{"chore", "chore: bump dependencies", bumpNone},
		{"unconventional subject", "Add cli.ExecuteContext", bumpNone},
		{"unknown type", "wip: still thinking", bumpNone},
		{"breaking footer needs a known type", "wip: still thinking\n\nBREAKING CHANGE: everything", bumpNone},
		{"unterminated scope", "feat(cli: escape path parameters", bumpNone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commitBump(tc.message); got != tc.want {
				t.Errorf("commitBump(%q) = %v, want %v", tc.message, got, tc.want)
			}
		})
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
		current string
		level   bump
		want    string
		wantOK  bool
	}{
		{"v0.4.6", bumpPatch, "v0.4.7", true},
		{"v0.4.6", bumpMinor, "v0.5.0", true},
		{"v0.4.6", bumpMajor, "v1.0.0", true},
		{"v0.4.6", bumpNone, "", false},
		{"v0.0.0", bumpPatch, "v0.0.1", true},
		// A prerelease tag bumps off its release version, not its suffix.
		{"v1.2.3-rc.1", bumpPatch, "v1.2.4", true},
		{"not-a-version", bumpPatch, "", false},
	}

	for _, tc := range cases {
		got, ok := nextVersion(tc.current, tc.level)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("nextVersion(%q, %v) = (%q, %v), want (%q, %v)",
				tc.current, tc.level, got, ok, tc.want, tc.wantOK)
		}
	}
}
