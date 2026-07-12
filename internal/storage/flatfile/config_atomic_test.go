package flatfile

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Oracle: sqlkit.SetConfig performs the config REPLACE and the
// SyncCustomStatusesTable sync inside ONE withWriteTx — concurrent setters
// serialize and a failure rolls both back, so the config value and the
// normalized custom-status store can never disagree.

// assertConfigStatusesConsistent checks that the stored status.custom config
// value parses to exactly the normalized custom-status store's content.
func assertConfigStatusesConsistent(t *testing.T, s *FlatFileStore) {
	t.Helper()
	ctx := context.Background()
	value, err := s.GetConfig(ctx, "status.custom")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	parsed, err := types.ParseCustomStatusConfig(value)
	if err != nil {
		t.Fatalf("stored status.custom %q does not parse: %v", value, err)
	}
	want := make([]string, len(parsed))
	for i, cs := range parsed {
		want[i] = cs.Name
	}
	sort.Strings(want)
	got, err := s.GetCustomStatuses(ctx)
	if err != nil {
		t.Fatalf("GetCustomStatuses: %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("config value %q => statuses %v, but normalized store has %v", value, want, got)
	}
}

func TestSetConfigConcurrentSettersStayConsistent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const iters = 25
	var wg sync.WaitGroup
	for _, value := range []string{"a1,a2", "b1,b2,b3"} {
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if err := s.SetConfig(ctx, "status.custom", v); err != nil {
					t.Errorf("SetConfig(%q): %v", v, err)
					return
				}
			}
		}(value)
	}
	wg.Wait()

	// Whichever write won, the KV value and the normalized file must agree —
	// on SQL both land in one tx, so an interleave can never split them.
	assertConfigStatusesConsistent(t, s)
}

func TestSetConfigPartialFailureRollsBackSync(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetConfig(ctx, "status.custom", "triage"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	// Force the second write of the pair (config KV) to fail after the
	// custom-status sync succeeded: swap the KV file for a directory.
	backup := s.configKVPath + ".bak"
	if err := os.Rename(s.configKVPath, backup); err != nil {
		t.Fatalf("rename kv: %v", err)
	}
	if err := os.Mkdir(s.configKVPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := s.SetConfig(ctx, "status.custom", "escalated"); err == nil {
		t.Fatal("SetConfig succeeded with unwritable config KV; want error")
	}
	if err := os.Remove(s.configKVPath); err != nil {
		t.Fatalf("rmdir: %v", err)
	}
	if err := os.Rename(backup, s.configKVPath); err != nil {
		t.Fatalf("restore kv: %v", err)
	}

	// SQL rolls the sync back with the failed tx: the normalized store must
	// still hold the previous value, not the half-applied new one.
	statuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		t.Fatalf("GetCustomStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0] != "triage" {
		t.Fatalf("statuses after failed SetConfig = %v, want [triage] (sync not rolled back)", statuses)
	}
	assertConfigStatusesConsistent(t, s)
}
