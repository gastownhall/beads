package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestIsNonInteractiveBootstrap tests the non-interactive detection logic for bootstrap.
func TestIsNonInteractiveBootstrap(t *testing.T) {
	origCI := os.Getenv("CI")
	origBDNI := os.Getenv("BD_NON_INTERACTIVE")
	defer func() {
		os.Setenv("CI", origCI)
		os.Setenv("BD_NON_INTERACTIVE", origBDNI)
	}()

	tests := []struct {
		name      string
		flagValue bool
		envCI     string
		envBDNI   string
		want      bool
	}{
		{
			name:      "flag true overrides everything",
			flagValue: true,
			envCI:     "",
			envBDNI:   "",
			want:      true,
		},
		{
			name:      "BD_NON_INTERACTIVE=1",
			flagValue: false,
			envCI:     "",
			envBDNI:   "1",
			want:      true,
		},
		{
			name:      "BD_NON_INTERACTIVE=true",
			flagValue: false,
			envCI:     "",
			envBDNI:   "true",
			want:      true,
		},
		{
			name:      "CI=true",
			flagValue: false,
			envCI:     "true",
			envBDNI:   "",
			want:      true,
		},
		{
			name:      "CI=1",
			flagValue: false,
			envCI:     "1",
			envBDNI:   "",
			want:      true,
		},
		{
			name:      "no flag no env falls back to terminal detection",
			flagValue: false,
			envCI:     "",
			envBDNI:   "",
			// In test environment, stdin is piped (not a TTY), so non-interactive
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("CI", tt.envCI)
			os.Setenv("BD_NON_INTERACTIVE", tt.envBDNI)

			got := isNonInteractiveBootstrap(tt.flagValue)
			if got != tt.want {
				t.Errorf("isNonInteractiveBootstrap(%v) with CI=%q BD_NON_INTERACTIVE=%q = %v, want %v",
					tt.flagValue, tt.envCI, tt.envBDNI, got, tt.want)
			}
		})
	}
}

// TestIsNonInteractiveBootstrapPrecedence tests that flag takes precedence over env vars.
func TestIsNonInteractiveBootstrapPrecedence(t *testing.T) {
	origCI := os.Getenv("CI")
	origBDNI := os.Getenv("BD_NON_INTERACTIVE")
	defer func() {
		os.Setenv("CI", origCI)
		os.Setenv("BD_NON_INTERACTIVE", origBDNI)
	}()

	// Flag true should always win
	os.Setenv("CI", "")
	os.Setenv("BD_NON_INTERACTIVE", "")
	if !isNonInteractiveBootstrap(true) {
		t.Error("flag=true should always return true regardless of env")
	}

	// BD_NON_INTERACTIVE should take precedence over CI
	os.Setenv("BD_NON_INTERACTIVE", "1")
	os.Setenv("CI", "")
	if !isNonInteractiveBootstrap(false) {
		t.Error("BD_NON_INTERACTIVE=1 should return true")
	}
}

// TestConfirmPromptNonInteractive verifies that confirmPrompt returns true
// when nonInteractive is set, without reading stdin.
func TestConfirmPromptNonInteractive(t *testing.T) {
	if !confirmPrompt("Proceed?", true) {
		t.Error("confirmPrompt should return true when nonInteractive=true")
	}
}

// bd-6dnrw.31: a remote-migrate gate refusal during the bootstrap warmup open
// must produce bootstrap-specific guidance — the gate's generic "adopt: bd
// bootstrap" remedy loops forever when the remote itself is behind.
func TestPrintBootstrapRemoteBehindGuidance(t *testing.T) {
	var buf bytes.Buffer
	printBootstrapRemoteBehindGuidance(&buf, &schema.RemoteMigrateGateError{
		CurrentVersion: 49,
		LatestVersion:  50,
		Pending:        1,
	}, "https://doltremoteapi.example.com/org/beads", "bd bootstrap")

	out := buf.String()
	for _, want := range []string{
		"needs 1 schema migration (v49 -> v50)",
		"Re-running `bd bootstrap` will NOT fix this",
		"bd migrate --force",
		"bd dolt push",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("guidance missing %q in:\n%s", want, out)
		}
	}
}

// bd-bt60zz (#6687): the clone the warmup refused keeps the database it just
// cloned, so once the migrator pushes, re-running bare `bd bootstrap` prints
// "Nothing to do." — the guidance must name the move-aside prerequisite on
// both branches, keep the designated-migrator choice, and keep warning that a
// retry before the remote changes cannot help.
func TestPrintBootstrapRemoteBehindGuidance_AdoptNeedsMoveAside(t *testing.T) {
	for _, rerun := range []string{"bd bootstrap", "bd init"} {
		var buf bytes.Buffer
		printBootstrapRemoteBehindGuidance(&buf, &schema.RemoteMigrateGateError{
			CurrentVersion: 49,
			LatestVersion:  50,
			Pending:        1,
		}, "https://doltremoteapi.example.com/org/beads", rerun)
		out := buf.String()
		for _, banned := range []string{
			"then other machines re-run `bd bootstrap` to adopt",
			"wait for it to push, then\n      re-run `bd bootstrap`",
		} {
			if strings.Contains(out, banned) {
				t.Errorf("%s: guidance presents bare bd bootstrap as the adopt (%q):\n%s", rerun, banned, out)
			}
		}
		for _, want := range []string{
			"Re-running `" + rerun + "` will NOT fix this",
			"designated migrator",
			"bd migrate --force",
			"move it aside first",
			"alone changes nothing",
			"a fresh clone",
			"run `bd migrate`",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: guidance missing %q in:\n%s", rerun, want, out)
			}
		}
	}
}

// bd-4mpy7: bd init's bootstrap path reuses the same guidance but must name
// the command the operator actually ran in the don't-bother-retrying line.
func TestPrintBootstrapRemoteBehindGuidance_InitWording(t *testing.T) {
	var buf bytes.Buffer
	printBootstrapRemoteBehindGuidance(&buf, &schema.RemoteMigrateGateError{
		CurrentVersion: 49,
		LatestVersion:  50,
		Pending:        1,
	}, "git+https://github.com/example/dotfiles.git", "bd init")

	out := buf.String()
	if !strings.Contains(out, "Re-running `bd init` will NOT fix this") {
		t.Errorf("guidance missing init-specific retry line in:\n%s", out)
	}
}
