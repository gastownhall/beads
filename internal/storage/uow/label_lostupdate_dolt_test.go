package uow

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestUOW_ConcurrentSetLabels_LastWriterWins reproduces GH#5657 on the
// proxied-server (uow) route: two writers replacing the SAME issue's full
// label set must serialize, leaving exactly one writer's set. A barrier
// between ApplyUpdate and Commit on the FIRST attempts only forces both to
// read the same snapshot before either commits, the interleaving the report's
// racing `bd update --set-labels` calls hit; the loser's replay then runs
// within RunTx's existing retry budget, with no caller-side retry loop.
func TestUOW_ConcurrentSetLabels_LastWriterWins(t *testing.T) {
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
	verifiedShutdownCleanup(t, storeRootDir)
	logPath := filepath.Join(t.TempDir(), "server.log")

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	provider, err := NewExternalDoltServerUOWProvider(
		ctx, storeRootDir, "beads_labelrace_test", logPath,
		configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: portInt},
		"root", "", 0, 0, false, "",
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })

	const issueID = "lr-1"
	func() {
		uw, err := provider.NewUOW(ctx)
		require.NoError(t, err)
		defer uw.Close(ctx)
		_, err = uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
			Issue: &types.Issue{
				ID: issueID, Title: "label race target", Status: types.StatusOpen,
				Priority: 2, IssueType: types.TypeTask,
			},
			ExplicitID: issueID,
		}, "seeder")
		require.NoError(t, err)
		require.NoError(t, uw.LabelUseCase().AddLabel(ctx, issueID, "state:a", "seeder"))
		require.NoError(t, uw.Commit(ctx, "seed label race target"))
	}()

	var applied, done sync.WaitGroup
	applied.Add(2)
	done.Add(2)
	var attempts atomic.Int32

	write := func(label string) error {
		first := true
		release := func() {
			if first {
				first = false
				applied.Done()
				applied.Wait()
			}
		}
		// A first attempt that fails before the barrier must not strand the
		// other writer waiting on it.
		defer release()
		return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			attempts.Add(1)
			set := []string{label}
			if _, err := uw.IssueUseCase().ApplyUpdate(ctx, issueID,
				domain.UpdateSpec{SetLabels: &set}, "writer-"+label); err != nil {
				return "", err
			}
			release()
			return "set labels " + label, nil
		})
	}

	errs := make([]error, 2)
	for i, label := range []string{"state:b", "state:c"} {
		go func() {
			defer done.Done()
			errs[i] = write(label)
		}()
	}
	done.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	uw, err := provider.NewUOW(ctx)
	require.NoError(t, err)
	defer uw.Close(ctx)
	got, err := uw.LabelUseCase().GetLabels(ctx, issueID)
	require.NoError(t, err)
	sort.Strings(got)
	require.Lenf(t, got, 1,
		"two concurrent --set-labels on one issue must leave exactly one writer's set; got the union %v", got)
	require.Contains(t, []string{"state:b", "state:c"}, got[0])
	require.GreaterOrEqualf(t, attempts.Load(), int32(3),
		"the overlapping loser must have replayed: %d attempts", attempts.Load())
}
