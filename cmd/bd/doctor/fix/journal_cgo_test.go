//go:build cgo

package fix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestBeadMutatingFixJournalsIntoTheWorkspace is the guard for a claim that was
// wrong: the events-journal construction guard exempted all of bd doctor with
// the reason "workspace repairs, not bead mutations", and three repair handlers
// do mutate beads — StaleClosedIssues and PatrolPollution DELETE issues, and
// the two fresh-clone import paths CREATE them.
//
// A repair is the LAST place a silent divergence is acceptable: it runs
// unattended, often on a workspace someone is already worried about, and a
// consumer whose mirror quietly disagrees afterwards has no event to explain
// why. So a bead-mutating fix journals like any other mutation, and this proves
// it end to end against a real store rather than trusting the exemption text.
func TestBeadMutatingFixJournalsIntoTheWorkspace(t *testing.T) {
	requireFixDoltContainer(t)
	port := fixTestServerPort()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}

	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = port
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	cfg.DoltDatabase = "jrnfix_" + hex.EncodeToString(h[:6])
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	// The workspace asks for a journal, the way an operator would. Activation
	// is resolved from THIS file by the factory the handler opens through.
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
		[]byte("events-journal: true\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	ctx := context.Background()
	setup, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{CreateIfMissing: true})
	if err != nil {
		t.Fatalf("open setup store: %v", err)
	}
	if err := setup.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		_ = setup.Close()
		t.Fatalf("set issue_prefix: %v", err)
	}
	// Seeded WITHOUT the journal on, so the only rows it can hold afterwards
	// are the ones the repair wrote.
	for _, title := range []string{"Digest: mol-abc-patrol", "Session ended: cleanup check", "Normal issue should remain"} {
		if err := setup.CreateIssue(ctx, &types.Issue{
			Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "test"); err != nil {
			_ = setup.Close()
			t.Fatalf("create %q: %v", title, err)
		}
	}
	if err := setup.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}

	if err := PatrolPollution(tmpDir); err != nil {
		t.Fatalf("PatrolPollution: %v", err)
	}

	verify, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("reopen for verification: %v", err)
	}
	defer func() { _ = verify.Close() }()

	rows, err := verify.ReadEventsJournal(ctx, 0, 0)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	deletes := 0
	for _, r := range rows {
		if r.Op == "delete" {
			deletes++
		}
	}
	if deletes != 2 {
		t.Fatalf("a bead-mutating `bd doctor --fix` journaled %d delete records (of %d rows), want 2 — "+
			"the repair opened a store that was never activated, so its deletes are invisible to every consumer of this workspace",
			deletes, len(rows))
	}
}
