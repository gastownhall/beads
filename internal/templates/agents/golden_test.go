package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this file pin the exact managed sections `bd init` and
// `bd setup <agent>` write between the BEGIN/END BEADS INTEGRATION markers of
// CLAUDE.md, AGENTS.md and the other instruction files. A change to a template
// (new guidance, a reworded rule, an added section) changes the bytes every
// user's instruction file receives and, through the marker hash, marks every
// section already installed as stale: `bd setup claude --check` then fails and
// the next `bd init` or `bd setup` rewrites the user's file. Such a change must
// therefore arrive as a reviewed golden diff, never as a side effect.
//
// Regenerate the golden files with
//
//	BD_UPDATE_GOLDEN=1 go test ./internal/templates/agents/ -run TestManagedSectionGolden
//
// and update the literal hashes in TestManagedSectionHashes by hand.

// managedSectionVariant is one managed section bd can render.
type managedSectionVariant struct {
	name    string
	profile Profile
	opts    RenderOpts
}

// managedSectionVariants lists every managed section bd renders: both
// profiles, each with and without the `bd dolt push` lines that depend on a
// configured Dolt remote, and each worded for a user-level file
// (UserLevelRenderOpts). NoPush renders the no-remote bytes
// (TestManagedSectionNoPushMatchesNoRemote), so it needs no variant of its own.
var managedSectionVariants = []managedSectionVariant{
	{name: "minimal", profile: ProfileMinimal, opts: RenderOpts{HasRemote: true}},
	{name: "minimal-no-remote", profile: ProfileMinimal, opts: RenderOpts{}},
	{name: "minimal-user", profile: ProfileMinimal, opts: UserLevelRenderOpts()},
	{name: "full", profile: ProfileFull, opts: RenderOpts{HasRemote: true}},
	{name: "full-no-remote", profile: ProfileFull, opts: RenderOpts{}},
	{name: "full-user", profile: ProfileFull, opts: UserLevelRenderOpts()},
}

// TestManagedSectionGolden pins each managed section byte for byte, markers
// included.
func TestManagedSectionGolden(t *testing.T) {
	for _, v := range managedSectionVariants {
		t.Run(v.name, func(t *testing.T) {
			assertGoldenFile(t, filepath.Join("testdata", "managed-section-"+v.name+".md"), RenderSectionWithOpts(v.profile, v.opts))
		})
	}
}

// TestManagedSectionHashes pins the marker hash of each managed section. The
// hash is what `bd setup <agent> --check` compares against an installed
// section, so changing it marks every existing install stale. It is a literal
// that BD_UPDATE_GOLDEN does not rewrite: a template change has to bump it by
// hand.
func TestManagedSectionHashes(t *testing.T) {
	want := map[string]string{
		"minimal":           "6ffdf896",
		"minimal-no-remote": "cdcea925",
		"minimal-user":      "52c58e00",
		"full":              "bacef91e",
		"full-no-remote":    "9c890b20",
		"full-user":         "0d9cad1f",
	}
	for _, v := range managedSectionVariants {
		got := CurrentHashWithOpts(v.profile, v.opts)
		if got != want[v.name] {
			t.Errorf("%s managed section hash = %q, want %q: the template changed, so every installed %s section is now stale", v.name, got, want[v.name], v.profile)
		}
		marker := "<!-- BEGIN BEADS INTEGRATION v:1 profile:" + string(v.profile) + " hash:" + want[v.name] + " -->\n"
		if section := RenderSectionWithOpts(v.profile, v.opts); !strings.HasPrefix(section, marker) {
			first, _, _ := strings.Cut(section, "\n")
			t.Errorf("%s managed section marker = %q, want %q", v.name, first, strings.TrimSuffix(marker, "\n"))
		}
	}
}

// TestManagedSectionNoPushMatchesNoRemote pins that a local-only workspace
// (no-push: true) gets the same managed section as one with no Dolt remote,
// whatever the remote state.
func TestManagedSectionNoPushMatchesNoRemote(t *testing.T) {
	for _, profile := range []Profile{ProfileMinimal, ProfileFull} {
		want := RenderSectionWithOpts(profile, RenderOpts{})
		for _, hasRemote := range []bool{false, true} {
			if got := RenderSectionWithOpts(profile, RenderOpts{HasRemote: hasRemote, NoPush: true}); got != want {
				t.Errorf("%s profile with NoPush (HasRemote=%v) differs from the no-remote section", profile, hasRemote)
			}
		}
	}
}

// assertGoldenFile compares got with the golden file at path, or rewrites the
// file when BD_UPDATE_GOLDEN=1.
func assertGoldenFile(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("BD_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		t.Logf("updated golden %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with BD_UPDATE_GOLDEN=1): %v", path, err)
	}
	if got == string(want) {
		return
	}
	t.Errorf("rendered output drifted from %s (regenerate with BD_UPDATE_GOLDEN=1 if intended)\n%s", path, firstGoldenDifference(string(want), got))
}

// firstGoldenDifference describes the first line on which want and got differ.
func firstGoldenDifference(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	line := func(lines []string, i int) string {
		if i >= len(lines) {
			return "(end of output)"
		}
		return fmt.Sprintf("%q", lines[i])
	}
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		if i >= len(wantLines) || i >= len(gotLines) || wantLines[i] != gotLines[i] {
			return fmt.Sprintf("first difference at line %d:\n  want: %s\n  got:  %s", i+1, line(wantLines, i), line(gotLines, i))
		}
	}
	return "outputs differ"
}
