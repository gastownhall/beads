//go:build cgo

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Exercise the CLI wiring: testing Lifecycle.Close or IssueClaimer alone misses
// that close uses BatchCloser and update --claim uses Lifecycle.Update.
func TestEmbeddedExternalMutationPolicy(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "xp")
	exerciseExternalMutationPolicy(t, crossModeEnv{mode: "embedded", bd: bd, dir: dir, env: bdEnv(dir)})
}

func TestProxiedServerExternalMutationPolicy(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "xp")
	exerciseExternalMutationPolicy(t, crossModeEnv{mode: "proxied", bd: bd, dir: p.dir, env: bdProxiedEnv(p.dir)})
}

func exerciseExternalMutationPolicy(t *testing.T, local crossModeEnv) {
	t.Helper()
	remoteDir, _, _ := bdInit(t, local.bd, "--prefix", "xr")
	remote := crossModeEnv{mode: "foreign-embedded", bd: local.bd, dir: remoteDir, env: bdEnv(remoteDir)}
	config := fmt.Sprintf("external_projects:\n  remote: %q\n", remoteDir)
	if err := os.WriteFile(filepath.Join(local.dir, ".beads", "config.local.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := remote.create(t, "Payments provider", "--labels", "export:payments")
	blocked := local.create(t, "Blocked consumer", "--priority", "0")
	local.mustRun(t, "dep", "add", blocked, "external:remote:payments")

	assertOpen := func(t *testing.T, id string) {
		t.Helper()
		got := local.show(t, id)
		if got.Status != types.StatusOpen || got.Assignee != "" {
			t.Fatalf("refused mutation changed %s: status=%s assignee=%q", id, got.Status, got.Assignee)
		}
	}
	refuse := func(t *testing.T, args ...string) {
		t.Helper()
		stdout, stderr, code := local.run(t, args...)
		if code == 0 || !strings.Contains(stderr, "external:remote:payments") {
			t.Errorf("bd %v: expected external-blocker refusal, got exit %d\n%s\n%s", args, code, stdout, stderr)
		}
	}

	t.Run("claim_is_atomic", func(t *testing.T) {
		refuse(t, "update", blocked, "--claim", "--notes", "must not be written")
		assertOpen(t, blocked)
		if got := local.show(t, blocked); got.Notes != "" {
			t.Fatalf("refused claim wrote notes: %q", got.Notes)
		}
	})
	t.Run("single_close", func(t *testing.T) {
		refuse(t, "close", blocked)
		assertOpen(t, blocked)
	})
	t.Run("mixed_batch_and_claim_next", func(t *testing.T) {
		finished := local.create(t, "Finished work")
		next := local.create(t, "Eligible next work", "--priority", "1")
		stdout, stderr, code := local.run(t, "close", blocked, finished, "--claim-next")
		// Partial success keeps the CLI's existing zero exit status and reports
		// each refused item separately on stderr.
		if code != 0 || !strings.Contains(stderr, "external:remote:payments") {
			t.Fatalf("mixed batch: exit %d\n%s\n%s", code, stdout, stderr)
		}
		assertOpen(t, blocked)
		if got := local.show(t, finished); got.Status != types.StatusClosed {
			t.Fatalf("eligible batch item was not closed: %s", got.Status)
		}
		if got := local.show(t, next); got.Status != types.StatusInProgress {
			t.Fatalf("claim-next did not choose eligible work: %s", got.Status)
		}
	})
	t.Run("force_only_waives_close", func(t *testing.T) {
		forced := local.create(t, "Forced close")
		local.mustRun(t, "dep", "add", forced, "external:remote:payments")
		local.mustRun(t, "close", forced, "--force", "--claim-next")
		assertOpen(t, blocked)
		if got := local.show(t, forced); got.Status != types.StatusClosed {
			t.Fatalf("forced close did not persist: %s", got.Status)
		}
		local.mustRun(t, "close", forced, "--claim-next")
		assertOpen(t, blocked)
	})
	t.Run("shipped_allows_claim_and_close", func(t *testing.T) {
		remote.mustRun(t, "close", provider)
		remote.mustRun(t, "ship", "payments")
		local.mustRun(t, "update", blocked, "--claim")
		local.mustRun(t, "close", blocked)
		if got := local.show(t, blocked); got.Status != types.StatusClosed {
			t.Fatalf("shipped capability did not allow closure: %s", got.Status)
		}
	})
}
