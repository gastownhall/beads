package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/configfile"
)

// writeWorkspaceMode lays down a .beads/metadata.json recording mode and
// points BEADS_DIR at it, which is what resolveInitBeadsDir consults first.
func writeWorkspaceMode(t *testing.T, mode string) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	body := map[string]string{"dolt_mode": mode, "dolt_database": "proj"}
	blob, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), blob, 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	return beadsDir
}

// TestExistingWorkspaceDoltModeReadsRawField is the input half of #3885: the
// re-init path could not see the existing mode at all, because nothing read it.
func TestExistingWorkspaceDoltModeReadsRawField(t *testing.T) {
	for _, tc := range []struct {
		written string
		want    string
	}{
		{configfile.DoltModeServer, configfile.DoltModeServer},
		{configfile.DoltModeProxiedServer, configfile.DoltModeProxiedServer},
		{configfile.DoltModeEmbedded, configfile.DoltModeEmbedded},
		// Case and whitespace are normalized, matching configfile's own
		// IsServerMode comparison.
		{"  SERVER  ", configfile.DoltModeServer},
		// Unset must stay distinguishable from "deliberately embedded";
		// defaulting here would make every fresh init look like a mode change.
		{"", ""},
	} {
		t.Run("mode="+tc.written, func(t *testing.T) {
			writeWorkspaceMode(t, tc.written)
			if got := existingWorkspaceDoltMode(); got != tc.want {
				t.Errorf("existingWorkspaceDoltMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExistingWorkspaceDoltModeMissingWorkspace(t *testing.T) {
	t.Setenv("BEADS_DIR", filepath.Join(t.TempDir(), "nope", ".beads"))
	if got := existingWorkspaceDoltMode(); got != "" {
		t.Errorf("existingWorkspaceDoltMode() = %q on a missing workspace, want \"\"", got)
	}
}

// newModeFlagCmd mirrors the mode-bearing flags init registers.
func newModeFlagCmd() *cobra.Command {
	c := &cobra.Command{Use: "init"}
	c.Flags().Bool("server", false, "")
	c.Flags().Bool("shared-server", false, "")
	c.Flags().Bool("proxied-server", false, "")
	return c
}

// TestInitModeExplicitlyRequested is the decision half: only an explicit
// request may change an existing workspace's mode. Get this wrong in the
// permissive direction and #3885 comes straight back — a bare re-init would
// once again count as "the user asked for embedded".
func TestInitModeExplicitlyRequested(t *testing.T) {
	// Neutralize ambient signals; these tests are about the flags.
	clearModeEnv := func(t *testing.T) {
		t.Helper()
		for _, e := range []string{
			"BEADS_DOLT_SERVER_MODE",
			"BEADS_DOLT_SHARED_SERVER",
			"BEADS_DOLT_PROXIED_SERVER",
		} {
			t.Setenv(e, "")
		}
	}

	t.Run("bare invocation is not explicit", func(t *testing.T) {
		clearModeEnv(t)
		if initModeExplicitlyRequested(newModeFlagCmd()) {
			t.Error("a bare `bd init` counted as an explicit mode request; a re-init would silently rewrite the mode")
		}
	})

	for _, flag := range []string{"server", "shared-server", "proxied-server"} {
		t.Run("--"+flag+" is explicit", func(t *testing.T) {
			clearModeEnv(t)
			c := newModeFlagCmd()
			if err := c.Flags().Set(flag, "true"); err != nil {
				t.Fatalf("set --%s: %v", flag, err)
			}
			if !initModeExplicitlyRequested(c) {
				t.Errorf("--%s was not treated as an explicit mode request", flag)
			}
		})
	}

	// A flag set to its default value still counts: the user typed it.
	t.Run("--server=false is still explicit", func(t *testing.T) {
		clearModeEnv(t)
		c := newModeFlagCmd()
		if err := c.Flags().Set("server", "false"); err != nil {
			t.Fatalf("set --server=false: %v", err)
		}
		if !initModeExplicitlyRequested(c) {
			t.Error("--server=false was ignored; an explicit downgrade request must not be overridden by inheritance")
		}
	})

	for _, env := range []string{
		"BEADS_DOLT_SERVER_MODE",
		"BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_PROXIED_SERVER",
	} {
		t.Run(env+" is explicit", func(t *testing.T) {
			clearModeEnv(t)
			t.Setenv(env, "1")
			if !initModeExplicitlyRequested(newModeFlagCmd()) {
				t.Errorf("%s was not treated as an explicit mode request", env)
			}
		})
	}
}

// TestInheritWorkspaceDoltMode covers the decision the RunE inheritance block
// applies — the composition a helper-only test suite would miss (#3885 review).
// The proxied-server case is the load-bearing one: the proxied init path
// dispatches on the explicit flag before the inheritance point, so "inherit"
// there would build embedded data under proxied-server metadata. It must
// refuse instead.
func TestInheritWorkspaceDoltMode(t *testing.T) {
	t.Run("server mode is inherited", func(t *testing.T) {
		writeWorkspaceMode(t, configfile.DoltModeServer)
		inheritServer, err := inheritWorkspaceDoltMode()
		if err != nil {
			t.Fatalf("inheritWorkspaceDoltMode() error: %v", err)
		}
		if !inheritServer {
			t.Error("server-mode workspace was not inherited")
		}
	})

	t.Run("embedded mode inherits nothing", func(t *testing.T) {
		writeWorkspaceMode(t, configfile.DoltModeEmbedded)
		inheritServer, err := inheritWorkspaceDoltMode()
		if err != nil || inheritServer {
			t.Errorf("embedded workspace: got (%v, %v), want (false, nil)", inheritServer, err)
		}
	})

	t.Run("missing workspace inherits nothing", func(t *testing.T) {
		t.Setenv("BEADS_DIR", filepath.Join(t.TempDir(), "nope", ".beads"))
		inheritServer, err := inheritWorkspaceDoltMode()
		if err != nil || inheritServer {
			t.Errorf("missing workspace: got (%v, %v), want (false, nil)", inheritServer, err)
		}
	})

	t.Run("proxied-server refuses rather than silently building embedded", func(t *testing.T) {
		writeWorkspaceMode(t, configfile.DoltModeProxiedServer)
		inheritServer, err := inheritWorkspaceDoltMode()
		if inheritServer {
			t.Fatal("proxied-server workspace was inherited as server mode")
		}
		if err == nil {
			t.Fatal("proxied-server workspace did not refuse; a bare re-init would build embedded data under proxied-server metadata")
		}
		if !strings.Contains(err.Error(), "--proxied-server") {
			t.Errorf("refusal does not tell the user how to proceed: %v", err)
		}
	})
}
