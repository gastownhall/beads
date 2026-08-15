package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// stubLookupIssueUC answers the two lookups workapi.GetIssueOrWisp makes and
// nothing else. A miss carries the shape the real domain seam produces:
// db.issueSQLRepositoryImpl.Get returns a bare sql.ErrNoRows and
// issueUseCaseImpl.get wraps it with the id - never storage.ErrNotFound, which
// is why every one of these commands used to print the raw driver sentinel.
type stubLookupIssueUC struct {
	domain.IssueUseCase // any other method is a nil call: these paths must not reach one
	hardErr             error
}

func (s stubLookupIssueUC) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return s.result(id)
}

func (s stubLookupIssueUC) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	return s.result(id)
}

func (s stubLookupIssueUC) result(id string) (*types.Issue, error) {
	if s.hardErr != nil {
		return nil, s.hardErr
	}
	return nil, fmt.Errorf("get %s: %w", id, sql.ErrNoRows)
}

// lookupOnlyUOW serves the issue use case; the other three are nil because
// workapi.NewUOWDetailSource reads all four at construction. Calling anything
// on them panics, which is the assertion: a lookup that failed must return
// before the command touches labels, dependencies, or comments.
type lookupOnlyUOW struct {
	uow.UnitOfWork
	issues domain.IssueUseCase
}

func (u lookupOnlyUOW) Close(context.Context)                       {}
func (u lookupOnlyUOW) IssueUseCase() domain.IssueUseCase           { return u.issues }
func (u lookupOnlyUOW) LabelUseCase() domain.LabelUseCase           { return nil }
func (u lookupOnlyUOW) DependencyUseCase() domain.DependencyUseCase { return nil }
func (u lookupOnlyUOW) CommentUseCase() domain.CommentUseCase       { return nil }

type lookupOnlyProvider struct{ issues domain.IssueUseCase }

func (p lookupOnlyProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	return lookupOnlyUOW{issues: p.issues}, nil
}

func (p lookupOnlyProvider) Close(context.Context) error { return nil }

// withStubbedProxiedLookup points the proxied-server commands at a unit of
// work whose issue lookup always fails: with hardErr when one is given,
// otherwise with the domain seam's wrapped sql.ErrNoRows.
func withStubbedProxiedLookup(t *testing.T, hardErr error) {
	t.Helper()
	oldProvider, oldJSON, oldWrote := uowProvider, jsonOutput, commandDidWrite.Load()
	uowProvider = lookupOnlyProvider{issues: stubLookupIssueUC{hardErr: hardErr}}
	jsonOutput = false
	t.Cleanup(func() {
		uowProvider = oldProvider
		jsonOutput = oldJSON
		// The mutating commands set commandDidWrite before they know whether
		// anything resolved, and it drives the deferred Dolt commit in main.
		commandDidWrite.Store(oldWrote)
	})
}

// inProxiedRoute selects the proxied route for one call.
//
// The label commands no longer have a run…ProxiedServer entry point to call
// directly: their route fork moved into resolveLabelTarget and the role
// accessor, which is the point of the change, so the route has to be selected
// the way a real invocation selects it.
func inProxiedRoute(run func() error) error {
	old := proxiedServerMode
	proxiedServerMode = true
	defer func() { proxiedServerMode = old }()
	return run()
}

const (
	stubMissingID    = "bd-missing"
	stubRawNoRows    = "sql: no rows in result set"
	stubBackendError = "connection reset by peer"
)

// showViewCmd builds the command for one of `bd show`'s alternate views.
// Only the named flag is registered; gatherShowProxiedInput reads the rest off
// an empty flag set, which yields their zero values.
func showViewCmd(view string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Bool(view, true, "")
	return cmd
}

// closeReasonCmd registers the reason flags `bd close` reads before it resolves
// anything. collectCloseReasonFlags returns an error for an unregistered one,
// which would abort the command ahead of the lookup under test.
func closeReasonCmd() *cobra.Command {
	cmd := &cobra.Command{}
	for _, name := range []string{"resolution", "message", "comment"} {
		cmd.Flags().String(name, "", "")
	}
	return cmd
}

// proxiedLookupCommands is every proxied-server call site that resolves an id
// through workapi.GetIssueOrWisp, driven at its real entry point with an id
// that cannot resolve. Each names the exact stderr line for the two ways that
// resolution can fail; the phrasings differ per command because they are the
// phrasings those commands have always shipped.
var proxiedLookupCommands = []struct {
	name string
	run  func(ctx context.Context) error
	// wantNotFound is stderr for an id that is genuinely absent. Before the
	// lookup normalized its sentinel these read "...: get bd-missing: sql: no
	// rows in result set", except gate list, which said "not found" for every
	// failure including a dead backend.
	wantNotFound string
	// wantHardErr is stderr when the lookup fails for any other reason. This is
	// the half that used to be unreachable: gate list and every mutating
	// command below it said "not found" for a dropped connection, a stale
	// schema, anything at all.
	wantHardErr string
	// exitsZero marks the commands that report per id and keep going. --refs,
	// --children, defer, undefer and todo done print the failure and move to
	// the next id, so one bad id out of one still leaves the command at exit 0.
	// That predates these slices; the branch under test is what they print, not
	// what they return.
	exitsZero bool
}{
	{
		name: "comments",
		run: func(ctx context.Context) error {
			return runCommentsProxiedServer(&cobra.Command{}, ctx, []string{stubMissingID})
		},
		wantNotFound: "Error: issue bd-missing not found",
		wantHardErr:  "Error: resolving bd-missing: connection reset by peer",
	},
	{
		name: "edit",
		run: func(ctx context.Context) error {
			return runEditProxiedServer(&cobra.Command{}, ctx, []string{stubMissingID})
		},
		wantNotFound: "Error: issue bd-missing not found",
		wantHardErr:  "Error: resolving bd-missing: connection reset by peer",
	},
	{
		name: "label list",
		run: func(ctx context.Context) error {
			return inProxiedRoute(func() error { return runLabelList(ctx, []string{stubMissingID}) })
		},
		wantNotFound: "Error: resolving bd-missing: not found",
		wantHardErr:  "Error: resolving bd-missing: connection reset by peer",
	},
	{
		name: "state",
		run: func(ctx context.Context) error {
			return inProxiedRoute(func() error { return runState(ctx, stubMissingID, "phase") })
		},
		wantNotFound: "Error: resolving bd-missing: not found",
		wantHardErr:  "Error: resolving bd-missing: connection reset by peer",
	},
	{
		name: "gate list",
		run: func(ctx context.Context) error {
			return runGateListProxiedServer(&cobra.Command{}, ctx, []string{stubMissingID})
		},
		wantNotFound: "Error: issue not found: bd-missing",
		wantHardErr:  "Error: resolving bd-missing: connection reset by peer",
	},
	{
		name: "show",
		run: func(ctx context.Context) error {
			return runShowProxiedServer(&cobra.Command{}, ctx, []string{stubMissingID})
		},
		// show reports per id and keeps going, so its line has no "Error: "
		// prefix; the non-zero exit comes from the empty result at the end.
		wantNotFound: "Issue bd-missing not found",
		wantHardErr:  "Error fetching bd-missing: connection reset by peer",
	},
	{
		name: "show --refs",
		run: func(ctx context.Context) error {
			return runShowProxiedServer(showViewCmd("refs"), ctx, []string{stubMissingID})
		},
		wantNotFound: "Issue bd-missing not found",
		wantHardErr:  "Error resolving bd-missing: connection reset by peer",
		exitsZero:    true,
	},
	{
		name: "show --children",
		run: func(ctx context.Context) error {
			return runShowProxiedServer(showViewCmd("children"), ctx, []string{stubMissingID})
		},
		wantNotFound: "Issue bd-missing not found",
		wantHardErr:  "Error resolving bd-missing: connection reset by peer",
		exitsZero:    true,
	},
	{
		name: "show --thread",
		run: func(ctx context.Context) error {
			return runShowProxiedServer(showViewCmd("thread"), ctx, []string{stubMissingID})
		},
		wantNotFound: "Error: message bd-missing not found",
		wantHardErr:  "Error: fetching message bd-missing: connection reset by peer",
	},
	// The mutating commands below reached the same seam through
	// proxiedResolveIssueOrWisp, which returned a bare (nil, false) and so
	// could only ever be read as "not found" (bd-3fs.2). Their wantNotFound
	// lines are the ones they have always shipped; only wantHardErr is new.
	{
		name: "close",
		run: func(ctx context.Context) error {
			return runCloseProxiedServer(closeReasonCmd(), ctx, []string{stubMissingID})
		},
		wantNotFound: "Issue bd-missing not found",
		wantHardErr:  "Error resolving bd-missing: connection reset by peer",
	},
	{
		name: "reopen",
		run: func(ctx context.Context) error {
			return runReopenProxiedServer(&cobra.Command{}, ctx, []string{stubMissingID})
		},
		wantNotFound: "Issue bd-missing not found",
		wantHardErr:  "Error resolving bd-missing: connection reset by peer",
	},
	{
		name: "unclaim",
		run: func(ctx context.Context) error {
			return runUnclaimProxiedServer(ctx, []string{stubMissingID}, "", false, "")
		},
		wantNotFound: "Error resolving bd-missing: not found",
		wantHardErr:  "Error resolving bd-missing: connection reset by peer",
	},
	{
		name: "defer",
		run: func(ctx context.Context) error {
			return runDeferProxiedServer(ctx, []string{stubMissingID}, nil, "")
		},
		wantNotFound: "Error resolving bd-missing: not found",
		wantHardErr:  "Error resolving bd-missing: connection reset by peer",
		exitsZero:    true,
	},
	{
		name: "undefer",
		run: func(ctx context.Context) error {
			return runUndeferProxiedServer(ctx, []string{stubMissingID})
		},
		wantNotFound: "Error getting bd-missing: not found",
		wantHardErr:  "Error getting bd-missing: connection reset by peer",
		exitsZero:    true,
	},
	{
		// The two routes now share one body, so this reports the DIRECT route's
		// resolution message. It lost the "label added: " prefix the proxied
		// route used to add, because that prefix named a write this command
		// never attempted: resolution now happens before the role is even
		// asked for.
		name: "label add",
		run: func(ctx context.Context) error {
			return inProxiedRoute(func() error { return runLabelAdd(ctx, []string{stubMissingID, "urgent"}) })
		},
		wantNotFound: `Error: resolving issue ID "bd-missing": not found`,
		wantHardErr:  `Error: resolving issue ID "bd-missing": connection reset by peer`,
	},
	{
		// The remove half of the same body, which had no row here before
		// because it had no separately-callable proxied entry point to name.
		name: "label remove",
		run: func(ctx context.Context) error {
			return inProxiedRoute(func() error { return runLabelRemove(ctx, []string{stubMissingID, "urgent"}) })
		},
		wantNotFound: `Error: resolving issue ID "bd-missing": not found`,
		wantHardErr:  `Error: resolving issue ID "bd-missing": connection reset by peer`,
	},
	{
		name: "label propagate",
		run: func(ctx context.Context) error {
			return runLabelPropagateProxiedServer(ctx, []string{stubMissingID, "urgent"})
		},
		wantNotFound: `Error: label propagate: resolving parent "bd-missing": not found`,
		wantHardErr:  `Error: label propagate: resolving parent "bd-missing": connection reset by peer`,
	},
	{
		name: "set state",
		run: func(ctx context.Context) error {
			return inProxiedRoute(func() error { return runSetState(ctx, stubMissingID, "phase", "done", "") })
		},
		// Both routes now resolve through resolveLabelTarget, so this is the
		// direct route's message rather than the retired proxied twin's
		// "issue bd-missing not found" — the same alignment `bd label`'s
		// migration made, and for the same reason: resolution happens before
		// the role is asked for anything, so the failure is not about state.
		wantNotFound: "Error: resolving bd-missing: not found",
		wantHardErr:  "Error: resolving bd-missing: connection reset by peer",
	},
	{
		name: "todo done",
		run: func(ctx context.Context) error {
			return runTodoDoneProxiedServer(&cobra.Command{}, ctx, []string{stubMissingID})
		},
		wantNotFound: "Error: failed to close bd-missing: issue bd-missing not found",
		wantHardErr:  "Error: failed to close bd-missing: resolving bd-missing: connection reset by peer",
		exitsZero:    true,
	},
	{
		name: "assign",
		run: func(ctx context.Context) error {
			return runAssignProxiedServer(ctx, []string{stubMissingID, "someone"}, false)
		},
		wantNotFound: "Error: assign bd-missing: issue bd-missing not found",
		wantHardErr:  "Error: assign bd-missing: resolving bd-missing: connection reset by peer",
	},
	{
		name: "comment add",
		run: func(ctx context.Context) error {
			return runCommentsAddProxiedServer(ctx, stubMissingID, "tester", "hello")
		},
		wantNotFound: "Error: issue bd-missing not found",
		wantHardErr:  "Error: resolving bd-missing: connection reset by peer",
	},
	{
		name: "human respond",
		run: func(ctx context.Context) error {
			return runHumanRespondProxiedServer(ctx, stubMissingID, "Response: resp")
		},
		wantNotFound: "Error: issue not found: bd-missing",
		wantHardErr:  "Error: resolving issue ID bd-missing: connection reset by peer",
	},
	{
		name: "human dismiss",
		run: func(ctx context.Context) error {
			return runHumanDismissProxiedServer(ctx, stubMissingID, "Dismissed")
		},
		wantNotFound: "Error: issue not found: bd-missing",
		wantHardErr:  "Error: resolving issue ID bd-missing: connection reset by peer",
	},
}

// TestProxiedLookupReportsMissingIssue covers the errors.Is(storage.ErrNotFound)
// branch at every proxied call site. Of the read commands bd-hc1 migrated, all
// but one used to hand the user db.issueSQLRepositoryImpl.Get's raw
// sql.ErrNoRows for a typo'd id. The exceptions are gate list and the mutating
// commands, which already printed "not found" - for a miss and for a dead
// backend alike, which is what TestProxiedLookupReportsBackendFailure guards.
// Their lines here are unchanged, and that is the point: normalizing the
// sentinel must not move the message a user sees for a typo.
func TestProxiedLookupReportsMissingIssue(t *testing.T) {
	for _, tc := range proxiedLookupCommands {
		t.Run(tc.name, func(t *testing.T) {
			withStubbedProxiedLookup(t, nil)

			var err error
			stderr := captureStderrDuring(t, func() { err = tc.run(context.Background()) })

			if gotZero := err == nil; gotZero != tc.exitsZero {
				t.Errorf("exit zero = %v, want %v (err = %v)", gotZero, tc.exitsZero, err)
			}
			if got := strings.TrimSpace(stderr); got != tc.wantNotFound {
				t.Errorf("stderr = %q, want %q", got, tc.wantNotFound)
			}
			if strings.Contains(stderr, stubRawNoRows) {
				t.Errorf("stderr leaks the raw driver sentinel: %q", stderr)
			}
		})
	}
}

// TestProxiedLookupReportsBackendFailure covers the other side of the same
// branch: a lookup that failed for a reason other than absence must say so.
// gate list is the regression bd-hc1 fixed here; every mutating command in the
// table is bd-3fs.2's. Those went through proxiedResolveIssueOrWisp, which
// returned a bare (nil, false) with the reason discarded, so "issue not found"
// was the only thing a caller could print - for a dropped connection, a stale
// schema, anything at all. Reverting any of those call sites fails this test.
func TestProxiedLookupReportsBackendFailure(t *testing.T) {
	for _, tc := range proxiedLookupCommands {
		t.Run(tc.name, func(t *testing.T) {
			withStubbedProxiedLookup(t, errors.New(stubBackendError))

			var err error
			stderr := captureStderrDuring(t, func() { err = tc.run(context.Background()) })

			if gotZero := err == nil; gotZero != tc.exitsZero {
				t.Errorf("exit zero = %v, want %v (err = %v)", gotZero, tc.exitsZero, err)
			}
			if got := strings.TrimSpace(stderr); got != tc.wantHardErr {
				t.Errorf("stderr = %q, want %q", got, tc.wantHardErr)
			}
			if strings.Contains(strings.ToLower(stderr), "not found") {
				t.Errorf("backend failure reported as a missing issue: %q", stderr)
			}
		})
	}
}

// TestReportIssueLookupFailure pins the helper the show paths share. It is the
// only place the distinction is drawn for refs, children, and the default
// view, all three of which print and continue rather than returning an error.
func TestReportIssueLookupFailure(t *testing.T) {
	t.Run("absent id names the issue", func(t *testing.T) {
		err := fmt.Errorf("%w: issue bd-gone", storage.ErrNotFound)
		stderr := captureStderrDuring(t, func() { reportIssueLookupFailure("fetching", "bd-gone", err) })
		if got, want := strings.TrimSpace(stderr), "Issue bd-gone not found"; got != want {
			t.Errorf("stderr = %q, want %q", got, want)
		}
	})

	t.Run("backend failure keeps the reason and the verb", func(t *testing.T) {
		stderr := captureStderrDuring(t, func() {
			reportIssueLookupFailure("resolving", "bd-gone", errors.New("i/o timeout"))
		})
		if got, want := strings.TrimSpace(stderr), "Error resolving bd-gone: i/o timeout"; got != want {
			t.Errorf("stderr = %q, want %q", got, want)
		}
	})
}

// TestUOWMolReaderLookupFailure covers the twelfth caller of the deleted
// proxiedResolveIssueOrWisp. uowMolReader is a molReader adapter rather than a
// command, so it is driven directly: the molecule commands wrap whatever it
// returns, and what they must not be handed is "step not found" for a backend
// that never answered.
func TestUOWMolReaderLookupFailure(t *testing.T) {
	newReader := func(hardErr error) uowMolReader {
		return uowMolReader{uw: lookupOnlyUOW{issues: stubLookupIssueUC{hardErr: hardErr}}}
	}

	t.Run("absent id is not found", func(t *testing.T) {
		_, err := newReader(nil).GetIssue(context.Background(), stubMissingID)
		if got, want := fmt.Sprint(err), "issue bd-missing not found"; got != want {
			t.Errorf("err = %q, want %q", got, want)
		}
	})

	t.Run("backend failure keeps the reason", func(t *testing.T) {
		_, err := newReader(errors.New(stubBackendError)).GetIssue(context.Background(), stubMissingID)
		if got, want := fmt.Sprint(err), "resolving bd-missing: "+stubBackendError; got != want {
			t.Errorf("err = %q, want %q", got, want)
		}
		if strings.Contains(strings.ToLower(fmt.Sprint(err)), "not found") {
			t.Errorf("backend failure reported as a missing issue: %v", err)
		}
	})
}
