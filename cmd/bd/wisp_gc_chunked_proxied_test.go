package main

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// The reads the age gc's selection (findAbandonedWisps) makes through a
// proxied unit of work, answered for wispPurgeProxiedProvider: nothing is
// blocked, no wisp has a dependent, and there are no custom statuses.

func (s wispPurgeProxiedIssues) GetBlockedIssues(context.Context, types.WorkFilter) ([]*types.BlockedIssue, error) {
	return nil, nil
}

func (s wispPurgeProxiedIssues) FindWispDependentsRecursive(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (wispPurgeProxiedConfig) GetCustomStatuses(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}

// wispGCProxiedSuccessKeys is every key the proxied gc's success object has
// always printed: no deleted_ids, which only the direct route prints.
var wispGCProxiedSuccessKeys = []string{
	"deleted", "deleted_count", "dependencies_removed", "events_removed",
	"labels_removed", "orphaned_issues", "references_updated", "schema_version",
}

// TestProxiedServerWispGCDeletesInChunks pins the proxied route's half of
// GH#6246 for both gc modes: every deletion unit of work carries at most 200
// selected ids (200, 200, 50 for 450), uncascaded with text references
// rewritten, each committing on its own; the successful units are reported
// in one aggregate object with the route's existing success keys; and when
// the second unit fails no third is opened, and the first unit's committed
// progress is kept and reported.
func TestProxiedServerWispGCDeletesInChunks(t *testing.T) {
	modes := []struct {
		name       string
		closedMode bool
		// keys is the mode's success key set on this route.
		keys []string
	}{
		{"closed", true, append(slices.Clone(wispGCProxiedSuccessKeys), "candidates_capped")},
		// --age 0s --all: the stub's wisps are closed.
		{"age", false, wispGCProxiedSuccessKeys},
	}
	run := func(closedMode bool) func() error {
		return func() error {
			return runWispGCProxiedServer(context.Background(), false, time.Duration(0), true, closedMode, true, nil)
		}
	}
	for _, mode := range modes {
		t.Run(mode.name+"/success", func(t *testing.T) {
			p := useWispPurgeProxied(t, 450)
			jsonOutput = true

			stdout, stderr, err := captureWispGC(t, run(mode.closedMode))
			if err != nil {
				t.Fatalf("gc: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
			units := p.deletionUnits()
			var sizes []int
			for i, u := range units {
				sizes = append(sizes, len(u.params.IDs))
				if !u.committed {
					t.Errorf("deletion unit %d did not commit", i+1)
				}
				if u.params.Cascade || !u.params.UpdateTextReferences {
					t.Errorf("deletion unit %d: cascade=%v update_text_references=%v, want false/true", i+1, u.params.Cascade, u.params.UpdateTextReferences)
				}
			}
			if want := []int{200, 200, 50}; !slices.Equal(sizes, want) {
				t.Errorf("deletion unit sizes = %v, want %v", sizes, want)
			}
			if len(p.deleted) != 450 {
				t.Errorf("%d wisps deleted, want 450", len(p.deleted))
			}
			obj := decodeSingleJSONObject(t, stdout)
			var keys []string
			for k := range obj {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			want := slices.Clone(mode.keys)
			sort.Strings(want)
			if !slices.Equal(keys, want) {
				t.Errorf("success keys = %v, want the route's existing %v", keys, want)
			}
			if got := obj["deleted_count"]; got != float64(450) {
				t.Errorf("deleted_count = %v, want 450", got)
			}
			if got, _ := obj["deleted"].([]interface{}); len(got) != 450 {
				t.Errorf("deleted names %d ids, want 450", len(got))
			}
		})

		t.Run(mode.name+"/second unit fails", func(t *testing.T) {
			p := useWispPurgeProxied(t, 450)
			jsonOutput = true
			boom := errors.New("injected deletion failure")
			calls := 0
			p.fail = func([]string) error {
				calls++
				if calls == 2 {
					return boom
				}
				return nil
			}

			stdout, stderr, err := captureWispGC(t, run(mode.closedMode))
			if err == nil || !isReportedExit(err) {
				t.Fatalf("error = %v, want an already-reported nonzero exit\nstdout:\n%s", err, stdout)
			}
			units := p.deletionUnits()
			if len(units) != 2 {
				t.Fatalf("opened %d deletion units, want 2 (none after the failed one)", len(units))
			}
			if !units[0].committed || units[1].committed {
				t.Errorf("committed = [%v %v], want only the first unit", units[0].committed, units[1].committed)
			}
			if len(p.deleted) != 200 {
				t.Errorf("%d wisps deleted, want the 200 of the first unit", len(p.deleted))
			}
			obj := decodeSingleJSONObject(t, stdout)
			if got := obj["deleted_count"]; got != float64(200) {
				t.Errorf("deleted_count = %v, want 200", got)
			}
			if got, _ := obj["deleted"].([]interface{}); len(got) != 200 {
				t.Errorf("deleted names %d ids, want the first unit's 200", len(got))
			}
			if got := obj["unconfirmed_count"]; got != float64(250) {
				t.Errorf("unconfirmed_count = %v, want 250", got)
			}
			if msg, _ := decodeSingleJSONObject(t, stderr)["error"].(string); !strings.Contains(msg, boom.Error()) {
				t.Errorf("stderr error = %q, want the cause", msg)
			}
		})
	}
}
