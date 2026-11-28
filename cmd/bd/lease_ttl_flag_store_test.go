//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestLeaseTTLFlagArmsTheLeaseInAStore drives `bd update --claim`,
// `bd ready --claim` and `bd heartbeat` in process against a Dolt server
// store; TestEmbeddedLeaseTTLFlagArmsTheLease drives the same journey against
// an embedded store. See runLeaseTTLFlagJourney for what each asserts.
func TestLeaseTTLFlagArmsTheLeaseInAStore(t *testing.T) {
	runLeaseTTLFlagJourney(t, func(beadsDir string) storage.DoltStorage {
		return newTestStoreWithPrefix(t, filepath.Join(beadsDir, "beads.db"), "ttl")
	})
}

func TestEmbeddedLeaseTTLFlagArmsTheLease(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	runLeaseTTLFlagJourney(t, func(beadsDir string) storage.DoltStorage {
		s, err := embeddeddolt.Open(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), "beads", "main")
		if err != nil {
			t.Fatalf("open embedded store: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if err := s.SetConfig(t.Context(), "issue_prefix", "ttl"); err != nil {
			t.Fatalf("set issue_prefix: %v", err)
		}
		return s
	})
}

// runLeaseTTLFlagJourney runs the three commands on the direct route against
// the store open returns and reads the lease each one left: --ttl sets the
// expiry to now plus the duration, the default is unchanged without it, a
// re-claim leaves the lease alone and says so, a heartbeat without --ttl cuts
// a longer lease back to the default (the per-call answer the --ttl help
// gives), and a claim of a wisp (a no-history issue here) arms no lease and
// says so.
func runLeaseTTLFlagJourney(t *testing.T, open func(beadsDir string) storage.DoltStorage) {
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)

	tmp := t.TempDir()
	beadsDir := filepath.Join(tmp, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Chdir(tmp)

	s := open(beadsDir)
	ctx := context.Background()
	store = s
	oldCtx, oldActor := rootCtx, actor
	rootCtx, actor = ctx, "ttl-worker"
	t.Cleanup(func() { rootCtx, actor = oldCtx, oldActor })

	createIssue := func(issue *types.Issue, labels ...string) string {
		t.Helper()
		if err := s.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create %q: %v", issue.Title, err)
		}
		for _, label := range labels {
			if err := s.AddLabel(ctx, issue.ID, label, "tester"); err != nil {
				t.Fatalf("label %s: %v", issue.ID, err)
			}
		}
		return issue.ID
	}
	create := func(title string, labels ...string) string {
		t.Helper()
		return createIssue(&types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, labels...)
	}
	// createNoHistory creates a no-history issue: a wisp, which the ready set
	// offers without --include-ephemeral.
	createNoHistory := func(title string, labels ...string) string {
		t.Helper()
		return createIssue(&types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, NoHistory: true}, labels...)
	}

	// run executes one command with flags set for that run alone, and
	// reports what it printed and the window of wall-clock time it ran in.
	run := func(cmd *cobra.Command, args []string, flags map[string]string) (string, time.Time, time.Time) {
		t.Helper()
		for name, value := range flags {
			if err := cmd.Flags().Set(name, value); err != nil {
				t.Fatalf("set --%s=%s: %v", name, value, err)
			}
		}
		defer func() {
			for name := range flags {
				f := cmd.Flags().Lookup(name)
				if sv, ok := f.Value.(interface{ Replace([]string) error }); ok {
					_ = sv.Replace(nil)
				} else {
					_ = f.Value.Set(f.DefValue)
				}
				f.Changed = false
			}
		}()
		start := time.Now().UTC()
		shown, err := runCommandForTTLTest(t, cmd, args)
		end := time.Now().UTC()
		if err != nil {
			t.Fatalf("bd %s %v %v: %v\n%s", cmd.Name(), args, flags, err, shown)
		}
		return shown, start, end
	}

	// requireExpiry asserts id's lease expires within [start+ttl, end+ttl],
	// widened by the second the DATETIME column rounds to.
	requireExpiry := func(step, id string, start, end time.Time, ttl time.Duration) time.Time {
		t.Helper()
		issue, err := s.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("%s: get %s: %v", step, id, err)
		}
		if issue.LeaseExpiresAt == nil {
			t.Fatalf("%s: %s has no lease", step, id)
		}
		got := issue.LeaseExpiresAt.UTC()
		lo, hi := start.Add(ttl).Add(-time.Second), end.Add(ttl).Add(time.Second)
		if got.Before(lo) || got.After(hi) {
			t.Errorf("%s: %s lease expires at %s, want now + %v (between %s and %s)", step, id, got, ttl, lo, hi)
		}
		return got
	}

	held := create("claimed with --ttl")
	_, start, end := run(updateCmd, []string{held}, map[string]string{"claim": "true", "ttl": "2h"})
	armed := requireExpiry("update --claim --ttl 2h", held, start, end, 2*time.Hour)

	plain := create("claimed without --ttl")
	_, start, end = run(updateCmd, []string{plain}, map[string]string{"claim": "true"})
	requireExpiry("update --claim", plain, start, end, storageissueops.DefaultLeaseTTL)

	shown, _, _ := run(updateCmd, []string{held}, map[string]string{"claim": "true", "ttl": "1h"})
	if !strings.Contains(shown, "not applied to "+held) {
		t.Errorf("a re-claim with --ttl did not say the lease was left as it was:\n%s", shown)
	}
	issue, err := s.GetIssue(ctx, held)
	if err != nil || issue.LeaseExpiresAt == nil || !issue.LeaseExpiresAt.UTC().Equal(armed) {
		t.Errorf("a re-claim moved the lease: was %s, now %+v (err %v)", armed, issue, err)
	}

	_, start, end = run(heartbeatCmd, []string{held}, map[string]string{"ttl": "4h"})
	requireExpiry("heartbeat --ttl 4h", held, start, end, 4*time.Hour)

	// The heartbeat question: the TTL is per call, so a heartbeat without
	// --ttl resets a 4h lease to now plus the default.
	_, start, end = run(heartbeatCmd, []string{held}, nil)
	requireExpiry("heartbeat without --ttl", held, start, end, storageissueops.DefaultLeaseTTL)

	ready := create("taken by ready --claim --ttl", "ttl-ready")
	_, start, end = run(readyCmd, nil, map[string]string{"claim": "true", "ttl": "3h", "label": "ttl-ready"})
	requireExpiry("ready --claim --ttl 3h", ready, start, end, 3*time.Hour)

	readyPlain := create("taken by ready --claim", "ttl-ready-plain")
	_, start, end = run(readyCmd, nil, map[string]string{"claim": "true", "label": "ttl-ready-plain"})
	requireExpiry("ready --claim", readyPlain, start, end, storageissueops.DefaultLeaseTTL)

	// A wisp is never leased, so a claim of one given --ttl says the length
	// was not applied, through bd update --claim and bd ready --claim alike,
	// and leaves no lease behind.
	requireWispNotLeased := func(step, id, shown string) {
		t.Helper()
		if !strings.Contains(shown, "not applied to "+id+": it is a wisp") {
			t.Errorf("%s: no wisp warning for %s:\n%s", step, id, shown)
		}
		issue, err := s.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("%s: get %s: %v", step, id, err)
		}
		if issue.Status != types.StatusInProgress || issue.Assignee != "ttl-worker" {
			t.Errorf("%s: %s is %s/%q, want it claimed by ttl-worker", step, id, issue.Status, issue.Assignee)
		}
		if issue.LeaseExpiresAt != nil {
			t.Errorf("%s: wisp %s has a lease expiring at %s, want none", step, id, issue.LeaseExpiresAt)
		}
	}
	wisp := createNoHistory("no-history claimed with --ttl")
	shown, _, _ = run(updateCmd, []string{wisp}, map[string]string{"claim": "true", "ttl": "2h"})
	requireWispNotLeased("update --claim --ttl 2h on a no-history issue", wisp, shown)

	readyWisp := createNoHistory("no-history taken by ready --claim --ttl", "ttl-ready-wisp")
	shown, _, _ = run(readyCmd, nil, map[string]string{"claim": "true", "ttl": "3h", "label": "ttl-ready-wisp"})
	requireWispNotLeased("ready --claim --ttl 3h on a no-history issue", readyWisp, shown)
}
