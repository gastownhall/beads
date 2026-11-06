//go:build !windows

package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

// assertHookFileShellSyntax fails when the merged hook file is not valid
// POSIX shell: user content around the managed section must never combine
// with it into a syntax error.
func assertHookFileShellSyntax(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-n", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("merged hook is not valid shell: %v\n%s\ncontent:\n%s", err, out, mustReadHookMigrationFile(t, path))
	}
}

// TestGeneratedSectionStaysScannerCompatible pins GH#6751's generator
// constraint end to end: the chained-sidecar classifier mentions the marker
// and legacy shim words only in quoted-fragment form, so every
// raw-substring scanner keeps seeing exactly one marker pair — doctor's
// migration planner must keep reading the real generated text (surrounded
// by user content) as a healthy managed section, never as legacy or broken.
func TestGeneratedSectionStaysScannerCompatible(t *testing.T) {
	for _, hookName := range managedHookNames {
		t.Run(hookName, func(t *testing.T) {
			section := generateHookSection(hookName)
			if got := strings.Count(section, "BEGIN BEADS INTEGRATION"); got != 1 {
				t.Errorf("section spells the BEGIN marker %d times, want exactly the one real marker", got)
			}
			if got := strings.Count(section, "END BEADS INTEGRATION"); got != 1 {
				t.Errorf("section spells the END marker %d times, want exactly the one real marker", got)
			}
			for _, legacy := range []string{"# bd-shim", "bd-hooks-version:", "# bd (beads)"} {
				if strings.Contains(section, legacy) {
					t.Errorf("section contains the legacy marker %q verbatim; classifier mentions must stay fragment-quoted", legacy)
				}
			}

			repoDir, hooksDir := setupHookMigrationRepo(t)
			hookPath := filepath.Join(hooksDir, hookName)
			writeHookMigrationFile(t, hookPath, "#!/bin/sh\necho user-before\n"+section+"echo user-after\n")
			assertHookFileShellSyntax(t, hookPath)

			plan, err := doctor.PlanHookMigration(repoDir)
			if err != nil {
				t.Fatalf("PlanHookMigration failed: %v", err)
			}
			if plan.NeedsMigrationCount != 0 {
				t.Errorf("plan wants %d hooks migrated; a fresh managed section must need none\n%+v", plan.NeedsMigrationCount, plan.Hooks)
			}
			for _, hook := range plan.Hooks {
				if hook.Name != hookName {
					continue
				}
				// The state strings are doctor's wire vocabulary
				// (HookMigrationHookPlan JSON), reasserted here so a doctor
				// rename cannot silently strand the generator.
				if hook.MarkerState != "valid" {
					t.Errorf("marker state = %q, want %q (raw marker counts drifted)", hook.MarkerState, "valid")
				}
				if hook.State != "marker_managed" {
					t.Errorf("state = %q, want %q", hook.State, "marker_managed")
				}
				if hook.NeedsMigration {
					t.Errorf("fresh managed section must not need migration: %+v", hook)
				}
				if hook.LegacyBDHook {
					t.Errorf("fragment-quoted classifier mentions must not read as a legacy bd hook")
				}
			}
		})
	}
}

// TestManagedSectionReinstallAndRemovalPreserveUserContent pins the write
// path the chained-sidecar section rides on: re-injecting the section
// (what a same-version `bd hooks install` refresh does) replaces only the
// managed text, removal strips exactly the section, and a hook with a
// dropped marker — which doctor must flag — is repaired by re-injection.
func TestManagedSectionReinstallAndRemovalPreserveUserContent(t *testing.T) {
	section := generateHookSection("pre-commit")
	hook := "#!/bin/sh\necho user-before\n" + section + "echo user-after\n"
	writeAndCheck := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "pre-commit")
		writeHookMigrationFile(t, path, content)
		assertHookFileShellSyntax(t, path)
		return mustReadHookMigrationFile(t, path)
	}

	t.Run("re-inject replaces the section in place", func(t *testing.T) {
		updated := writeAndCheck(t, injectHookSection(hook, section))
		if got := strings.Count(updated, "BEGIN BEADS INTEGRATION"); got != 1 {
			t.Errorf("re-injected hook has %d BEGIN markers, want 1:\n%s", got, updated)
		}
		if got := strings.Count(updated, "END BEADS INTEGRATION"); got != 1 {
			t.Errorf("re-injected hook has %d END markers, want 1:\n%s", got, updated)
		}
		if got := strings.Count(updated, "if command -v bd >/dev/null 2>&1; then"); got != 1 {
			t.Errorf("re-injected hook carries %d copies of the section body, want 1:\n%s", got, updated)
		}
		if got := strings.Count(updated, "echo user-before\n"); got != 1 || strings.Count(updated, "echo user-after\n") != 1 {
			t.Errorf("user content must survive a same-version reinstall exactly once:\n%s", updated)
		}
	})

	t.Run("remove strips the section and keeps user content", func(t *testing.T) {
		stripped, ok := removeHookSection(hook)
		if !ok {
			t.Fatal("removeHookSection reported no section to remove")
		}
		if strings.Contains(stripped, "BEGIN BEADS INTEGRATION") || strings.Contains(stripped, "END BEADS INTEGRATION") {
			t.Errorf("markers must be gone after removal:\n%s", stripped)
		}
		if !strings.Contains(stripped, "echo user-before\n") || !strings.Contains(stripped, "echo user-after\n") {
			t.Errorf("user content must survive removal:\n%s", stripped)
		}
		writeAndCheck(t, stripped)
	})

	for _, tc := range []struct {
		name  string
		drop  string
		count string
		// wantAfter: whether trailing user content survives the repair.
		// With the END marker dropped, everything after the orphaned BEGIN
		// is indistinguishable from stale managed text, so the repair
		// replaces it — content before the section is what must survive
		// in every case.
		wantAfter bool
	}{
		{name: "dropped END marker is flagged and repaired", drop: hookSectionEndLine() + "\n", count: "END BEADS INTEGRATION", wantAfter: false},
		{name: "dropped BEGIN marker is flagged and repaired", drop: hookSectionBeginLine() + "\n", count: "BEGIN BEADS INTEGRATION", wantAfter: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := strings.Replace(hook, tc.drop, "", 1)

			repoDir, hooksDir := setupHookMigrationRepo(t)
			hookPath := filepath.Join(hooksDir, "pre-commit")
			writeHookMigrationFile(t, hookPath, broken)
			plan, err := doctor.PlanHookMigration(repoDir)
			if err != nil {
				t.Fatalf("PlanHookMigration failed: %v", err)
			}
			var found bool
			for _, hook := range plan.Hooks {
				if hook.Name != "pre-commit" {
					continue
				}
				found = true
				if hook.MarkerState != "broken" || !hook.NeedsMigration {
					t.Fatalf("doctor must flag the dropped marker for repair, got state %q marker %q needs-migration %v", hook.State, hook.MarkerState, hook.NeedsMigration)
				}
			}
			if !found {
				t.Fatal("pre-commit missing from plan")
			}

			repaired := writeAndCheck(t, injectHookSection(broken, section))
			if got := strings.Count(repaired, tc.count); got != 1 {
				t.Errorf("re-injection must restore exactly one %s marker, got %d:\n%s", tc.count, got, repaired)
			}
			if !strings.Contains(repaired, "echo user-before\n") {
				t.Errorf("user content before the section must survive marker repair:\n%s", repaired)
			}
			if tc.wantAfter && !strings.Contains(repaired, "echo user-after\n") {
				t.Errorf("trailing user content must survive marker repair:\n%s", repaired)
			}
		})
	}
}
