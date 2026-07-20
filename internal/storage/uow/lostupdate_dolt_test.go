package uow

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestUOW_ConcurrentMergeOps_NoLostUpdate proves the lost-update fix against a
// REAL Dolt sql-server: concurrent writers mutating the SAME issue through the
// unit-of-work stack (the proxied-server CLI's write path) must never lose a
// write that reported success.
//
// Each writer follows the exact contract cmd/bd's applyUpdateProxiedOne now
// implements:
//
//   - metadata edits and note appends pass through ApplyUpdate as
//     merge-OPERATION keys (issueops.OpSetMetadata / OpAppendNotes), resolved
//     against the row read inside the mutation transaction — never pre-merged
//     from an earlier snapshot;
//   - the unit of work commits exactly ONCE; a serialization failure (Dolt's
//     commit-time conflict, guaranteed server-side rollback) redoes the WHOLE
//     read-merge-write in a fresh unit of work;
//   - "nothing to commit" on a fresh, unconflicted attempt would mean the
//     write silently vanished, so the writer fails the test if it appears.
//
// The old commit-only retry (uow.CommitWithRetries) re-committed the
// rolled-back session, harvested "nothing to commit", and the CLI swallowed it
// with exit 0 — the silent-loss shape this test exists to catch.
//
// Two phases, because Dolt's conflict behavior differs by column type:
//
//   - metadata is a JSON column, which Dolt three-way merges KEY-WISE at
//     commit time: distinct-key writers converge without any conflict, so the
//     metadata phase asserts convergence but cannot demand conflicts;
//   - notes is a TEXT column with no structural merge: overlapping writers
//     editing that cell MUST conflict, so the notes phase demands at least one
//     serialization failure — proof the whole-UOW redo path actually ran —
//     and still requires every append to survive.
func TestUOW_ConcurrentMergeOps_NoLostUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a Dolt container and a dbproxy subprocess; skipped in -short")
	}
	port := testutil.StartIsolatedDoltContainer(t)
	portInt, err := strconv.Atoi(port)
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	logPath := filepath.Join(t.TempDir(), "server.log")

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	provider, err := NewExternalDoltServerUOWProvider(
		ctx,
		storeRootDir,
		"beads_lostupdate_test",
		logPath,
		configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: portInt},
		"root",
		"",
		0,
		0,
	)
	require.NoError(t, err)
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })

	const issueID = "lu-1"
	func() {
		uw, err := provider.NewUOW(ctx)
		require.NoError(t, err)
		defer uw.Close(ctx)
		_, err = uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
			Issue: &types.Issue{
				ID:        issueID,
				Title:     "lost-update target",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				Notes:     "seed line",
				Metadata:  json.RawMessage(`{"seed":"yes"}`),
			},
			ExplicitID: issueID,
		}, "seeder")
		require.NoError(t, err)
		require.NoError(t, uw.Commit(ctx, "seed lost-update target"))
	}()

	// conflicts counts observed serialization failures so the assertion below
	// can prove the commit-time conflict path really fired.
	var conflicts atomic.Int64

	// writeOp mirrors applyUpdateProxiedOne: fresh UOW per attempt, merge ops
	// in spec.Fields, one commit, whole-attempt redo on serialization failure.
	//
	// syncPoint is a barrier placed between ApplyUpdate and Commit on the
	// FIRST attempt only: every writer applies its update against the same
	// base root before any of them commits, guaranteeing the transactions
	// overlap at Dolt's commit-time merge.
	writeOp := func(label string, fields map[string]any, syncPoint func()) error {
		synced := false
		syncOnce := func() {
			if !synced {
				synced = true
				syncPoint()
			}
		}
		defer syncOnce() // never leave barrier peers hanging on an early error
		const maxAttempts = 40
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			err := func() error {
				uw, err := provider.NewUOW(ctx)
				if err != nil {
					return fmt.Errorf("new uow: %w", err)
				}
				defer uw.Close(ctx)
				if _, err := uw.IssueUseCase().ApplyUpdate(ctx, issueID,
					domain.UpdateSpec{Fields: fields}, "writer-"+label); err != nil {
					return err
				}
				syncOnce()
				return uw.Commit(ctx, "write "+label)
			}()
			if err == nil {
				return nil
			}
			if IsSerializationError(err) {
				conflicts.Add(1)
				time.Sleep(time.Duration(attempt) * 10 * time.Millisecond)
				continue
			}
			if issueops.IsNothingToCommitError(err) {
				return fmt.Errorf("write %s swallowed as nothing-to-commit on a fresh attempt (silent lost update): %w", label, err)
			}
			return fmt.Errorf("write %s: %w", label, err)
		}
		return fmt.Errorf("write %s: retries exhausted on serialization failures", label)
	}

	runRound := func(round int, fieldsFor func(writer int) (string, map[string]any)) {
		t.Helper()
		const writers = 4
		errs := make([]error, writers)
		var applied, done sync.WaitGroup
		applied.Add(writers)
		done.Add(writers)
		barrier := func() {
			applied.Done()
			applied.Wait()
		}
		for i := 0; i < writers; i++ {
			i := i
			label, fields := fieldsFor(i)
			go func() {
				defer done.Done()
				errs[i] = writeOp(label, fields, barrier)
			}()
		}
		done.Wait()
		for i, err := range errs {
			require.NoErrorf(t, err, "round %d writer %d", round, i)
		}
	}

	// Phase 1: distinct metadata keys. Dolt merges the JSON column key-wise at
	// commit time, so these converge with or without observed conflicts.
	const metadataRounds = 2
	for round := 0; round < metadataRounds; round++ {
		round := round
		runRound(round, func(writer int) (string, map[string]any) {
			key := fmt.Sprintf("w%dr%d", writer, round)
			return key, map[string]any{
				issueops.OpSetMetadata: []string{key + "=1"},
			}
		})
	}

	// Phase 2: note appends. The notes TEXT cell has no structural merge, so
	// the barrier-aligned first attempts MUST collide at commit time and the
	// losers MUST redo the whole read-merge-write to preserve every line.
	notesConflictFloor := conflicts.Load()
	const notesRounds = 2
	for round := 0; round < notesRounds; round++ {
		round := round
		runRound(round, func(writer int) (string, map[string]any) {
			label := fmt.Sprintf("note-w%dr%d", writer, round)
			return label, map[string]any{
				issueops.OpAppendNotes: "appended by " + label,
			}
		})
	}
	notesConflicts := conflicts.Load() - notesConflictFloor

	t.Logf("observed %d serialization conflicts total (%d in the notes phase)",
		conflicts.Load(), notesConflicts)
	require.Positive(t, notesConflicts,
		"barrier-aligned writers on the notes TEXT cell must hit Dolt commit-time conflicts; the whole-UOW redo path was never exercised")

	// Every write reported as successful must be present, and the seeds must
	// survive.
	uw, err := provider.NewUOW(ctx)
	require.NoError(t, err)
	defer uw.Close(ctx)
	final, err := uw.IssueUseCase().GetIssue(ctx, issueID)
	require.NoError(t, err)
	require.NotNil(t, final)

	got := map[string]any{}
	require.NoError(t, json.Unmarshal(final.Metadata, &got))
	require.Equal(t, "yes", got["seed"], "seed key erased by concurrent writers: %v", got)
	var missing []string
	for round := 0; round < metadataRounds; round++ {
		for writer := 0; writer < 4; writer++ {
			key := fmt.Sprintf("w%dr%d", writer, round)
			if _, ok := got[key]; !ok {
				missing = append(missing, key)
			}
		}
	}
	require.Emptyf(t, missing, "silent lost update: %d successful metadata writes missing from final metadata %v", len(missing), got)

	require.Contains(t, final.Notes, "seed line", "seed notes erased by concurrent appends")
	for round := 0; round < notesRounds; round++ {
		for writer := 0; writer < 4; writer++ {
			line := fmt.Sprintf("appended by note-w%dr%d", writer, round)
			require.Containsf(t, final.Notes, line,
				"silent lost update: successful append %q missing from final notes %q", line, final.Notes)
		}
	}
	require.Equal(t, 1, strings.Count(final.Notes, "seed line"), "seed line duplicated: redo must not double-apply")
}
