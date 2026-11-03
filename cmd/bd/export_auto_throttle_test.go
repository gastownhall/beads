//go:build cgo

package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// GH#6955, empty-export half: the empty-export branch says it must not prime
// the throttle, but it saved a fresh Timestamp anyway. On a fresh workspace
// the documented opt-in sequence (`bd config set export.auto true` …) runs
// its own post-command auto-export over an empty store, so the first real
// `bd create` landed inside a window it never benefited from and produced no
// JSONL until another write or export.interval elapsed — with no further
// write, indefinitely.

// TestMaybeAutoExport_EmptyExportDoesNotPrimeThrottle pins both halves of the
// fix: an empty pass preserves the prior (zero or already-eligible) timestamp
// while still recording the new state hash and diff anchor, so an unchanged
// empty store keeps hitting the hash shortcut and a following create exports
// immediately.
func TestMaybeAutoExport_EmptyExportDoesNotPrimeThrottle(t *testing.T) {
	t.Run("FirstCreateWithinIntervalExports", func(t *testing.T) {
		fake := newWedgeHealTestStore(t) // empty store, head-1
		beadsDir, exportPath := setupWedgeHealTest(t, fake)
		// The default-scale window: the create below lands well inside it.
		config.Set("export.interval", "1h")

		// The empty pass every fresh opted-in workspace takes first.
		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("empty maybeAutoExport: %v", err)
		}
		if _, err := os.Stat(exportPath); !os.IsNotExist(err) {
			t.Fatalf("empty export must remove the file, stat err = %v", err)
		}
		if state := loadExportAutoState(beadsDir); !state.Timestamp.IsZero() {
			t.Fatalf("empty export primed the throttle: state.Timestamp = %v, want the zero time", state.Timestamp)
		}

		// A durable create lands inside the window; it must export now.
		fake.issues = []*types.Issue{wedgeHealIssue("wh-a", "Alpha")}
		fake.headCommit = "head-2"

		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("maybeAutoExport after create: %v", err)
		}
		if got, want := strings.Join(jsonlIDs(t, exportPath), ","), "wh-a"; got != want {
			t.Fatalf("exported ids = %q, want %q — the first create must export without an explicit export", got, want)
		}
		if state := loadExportAutoState(beadsDir); state.LastDoltCommit != "head-2" || state.LastDiffAnchor != "head-2" {
			t.Errorf("state after export = %+v, want the new hash and a real commit anchor", state)
		}
	})

	t.Run("EmptyPassKeepsExpiredTimestampAndHashes", func(t *testing.T) {
		fake := newWedgeHealTestStore(t) // empty store, head-1
		beadsDir, exportPath := setupWedgeHealTest(t, fake)
		config.Set("export.interval", "1h")

		// Prior state whose timestamp is already eligible (expired), with a
		// superseded hash. The empty pass must retain the eligibility it
		// inherited rather than starting a fresh window.
		expired := time.Now().Add(-2 * time.Hour)
		saveExportAutoState(beadsDir, &exportAutoState{
			LastDoltCommit: "head-0",
			LastDiffAnchor: "head-0",
			Timestamp:      expired,
		})

		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("empty maybeAutoExport: %v", err)
		}
		state := loadExportAutoState(beadsDir)
		if !state.Timestamp.Equal(expired) {
			t.Fatalf("empty export advanced the throttle: Timestamp = %v, want the prior eligible %v", state.Timestamp, expired)
		}
		if state.LastDoltCommit != "head-1" {
			t.Errorf("LastDoltCommit = %q, want head-1 (the hash must still be recorded or the next pass reprocesses an unchanged empty store)", state.LastDoltCommit)
		}
		if state.LastDiffAnchor != "head-1" {
			t.Errorf("LastDiffAnchor = %q, want head-1 (a real commit, never a working-set value)", state.LastDiffAnchor)
		}

		// The inherited eligibility is real: a create in the same instant
		// exports.
		fake.issues = []*types.Issue{wedgeHealIssue("wh-b", "Beta")}
		fake.headCommit = "head-2"

		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("maybeAutoExport after create: %v", err)
		}
		if got, want := strings.Join(jsonlIDs(t, exportPath), ","), "wh-b"; got != want {
			t.Fatalf("exported ids = %q, want %q", got, want)
		}
	})
}