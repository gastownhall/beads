package uow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// asOfReadHarness is this leg's Design D dispatch table (gastownhall/beads#5898
// revision 9, gastownhall/beads#6136, be-x5jqd.5): the conformance suite
// invents storeID values to select among N genuinely isolated stores, and
// isolating them is entirely this harness's job -- AsOfReadInTx itself is
// storeID-oblivious (see the package doc on
// internal/storage/issueops/asof_read.go). Each storeID gets its own fresh
// UnitOfWorkProvider, built lazily on first use (provider) and torn down via
// t.Cleanup on the TestAsOfReadContract-level *testing.T. created tracks
// which (storeID, id) pairs already have a base issues row, since mintAt's
// second and later calls for the same pair must only add a historical
// issue_versions row, never a second base create.
//
// UNLIKE the dolt leg's buildAsOfReadStore (one shared testcontainer, N
// randomly-named databases) or the embeddeddolt leg's buildAsOfReadStore (N
// fully independent on-disk databases, isolation free), this leg spawns one
// real dolt sql-server SUBPROCESS per NewDoltServerUOWProvider call unless
// that call adopts an already-running one. Booting N subprocesses -- one per
// storeID, up to seven across a full TestAsOfReadContract run -- would be
// needlessly heavy, so this harness follows the same "one shared server, many
// databases" shape the dolt leg uses: TestAsOfReadContract does the ONE
// heavyweight, Fatal-permitted setup (dolt/bd binaries, HOME, port,
// storeRootDir, server config, log path) once in its own body, before any
// t.Run, and hands the resulting coordinates to newAsOfReadHarness. provider()
// then calls NewDoltServerUOWProvider -- a pure, Fatal-free function -- against
// those SAME coordinates with a fresh, randomly-named database per storeID.
// proxy.GetCreateDatabaseProxyServerEndpoint's adoption semantics (given the
// same rootDir, a later call dials the already-running server instead of
// spawning a duplicate, regardless of which database name it carries -- see
// TestNewDoltServerUOWProvider_ConcurrentInstantiation) mean only ONE server
// process is ever spawned, no matter how many storeIDs a case invents.
//
// storeID itself is used ONLY as a map key into h.providers here -- never as
// the database name. The conformance suite's storeID strings are built from
// free-form tags (asOfReadStore in backend/conformance/
// versioned_read_contract.go: fixture.IssuePrefix + "-asof-store-" + tag,
// where tag can itself embed a Restriction's hyphenated String()), so they are
// not assumed to be safe SQL/Dolt identifiers. Generating an independent
// random per-store database name (asOfReadDatabaseName, matching the dolt
// leg's own dbSuffix approach) sidesteps that question entirely rather than
// sanitizing storeID.
//
// provider() cannot use newTestUOWProvider or newUOWRoleFixtureProvider.
// AsOfReadFixture's hook types (Resolve/MintAt/MakeUnanswerable) carry no
// *testing.T of their own, so a provider built lazily inside a hook -- itself
// invoked from inside a t.Run subtest closure, sometimes nested two deep (the
// restriction-loop case) -- has access only to whichever *testing.T this
// harness was constructed with: necessarily the OUTER TestAsOfReadContract's
// t, captured before any t.Run. Both of those helpers call testify's
// require.NoError / t.Fatalf internally, and calling FailNow/SkipNow on a
// *testing.T from a goroutine other than the one running that exact test --
// which a t.Run subtest's closure always is, relative to the outer test --
// panics with "subtest may have called FailNow on a parent test" (go's
// testing package requires FailNow/SkipNow to run only on the goroutine
// executing that specific test). provider() is a Fatalf/Skip-free path for
// exactly this reason: its failures come back as a plain error, which flows
// through the calling hook's own (..., error) return to
// conformance.RunAsOfReadXXX, which correctly Fatalfs using the SUBTEST's own
// *testing.T on the SUBTEST's own goroutine. Also unlike
// newUOWRoleFixtureProvider (one provider for a whole role suite, matching
// roleFixtureKit's frozen shape), this harness needs N independently-isolated
// providers -- roleFixtureKit is deliberately not reused here; see this
// file's own note by conformanceToAsOfRestriction on why issueops must stay
// conformance-oblivious, the same boundary that keeps this harness's own
// mapping from living centrally.
//
// See internal/storage/dolt/versioned_read_contract_test.go and
// internal/storage/embeddeddolt/versioned_read_contract_test.go for this
// leg's siblings, which this file mirrors as closely as the three backends'
// construction APIs allow.
type asOfReadHarness struct {
	t            *testing.T
	storeRootDir string
	doltBin      string
	cfgPath      string
	logPath      string
	providers    map[string]UnitOfWorkProvider
	created      map[[2]string]bool
}

func newAsOfReadHarness(t *testing.T, storeRootDir, doltBin, cfgPath, logPath string) *asOfReadHarness {
	return &asOfReadHarness{
		t:            t,
		storeRootDir: storeRootDir,
		doltBin:      doltBin,
		cfgPath:      cfgPath,
		logPath:      logPath,
		providers:    map[string]UnitOfWorkProvider{},
		created:      map[[2]string]bool{},
	}
}

// provider returns storeID's UnitOfWorkProvider, building it on first use
// against a fresh, randomly-named database on the shared server. See the
// asOfReadHarness doc comment for why this function -- and everything it
// calls -- must never call *testing.T's Fatalf/Skip.
func (h *asOfReadHarness) provider(ctx context.Context, storeID string) (UnitOfWorkProvider, error) {
	if p, ok := h.providers[storeID]; ok {
		return p, nil
	}
	database, err := asOfReadDatabaseName()
	if err != nil {
		return nil, err
	}
	provider, err := NewDoltServerUOWProvider(
		ctx,
		h.storeRootDir,
		database,
		h.logPath,
		h.cfgPath,
		proxy.BackendLocalServer,
		"root",
		"",
		h.doltBin,
		0,
		0,
		false,
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("as-of read fixture: new provider for store %q (database %q): %w", storeID, database, err)
	}
	// t.Cleanup only appends to a mutex-guarded slice -- unlike Fatalf/Skip it
	// never calls runtime.Goexit, so registering it on the outer h.t from
	// inside a subtest's goroutine is safe. h.t's own cleanups run only after
	// ALL of TestAsOfReadContract's t.Run calls have returned (none of them,
	// nor their own nested t.Run calls, ever invoke t.Parallel), so every
	// provider this harness builds stays open for the whole test. These
	// per-provider Close cleanups are registered AFTER (so, per t.Cleanup's
	// LIFO order, run BEFORE) TestAsOfReadContract's own shared
	// proxy.Shutdown cleanup, so every provider is closed while the server
	// they share is still up.
	h.t.Cleanup(func() { _ = provider.Close(context.Background()) })

	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "bd: set issue prefix", uw.ConfigUseCase().SetConfig(ctx, "issue_prefix", "asof")
	}); err != nil {
		return nil, fmt.Errorf("as-of read fixture: set issue_prefix for store %q: %w", storeID, err)
	}

	h.providers[storeID] = provider
	return provider, nil
}

// asOfReadDatabaseName mints a fresh, unique database name, independent of
// any storeID's own content -- see the asOfReadHarness doc comment for why
// storeID itself is never used as one.
func asOfReadDatabaseName() (string, error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("as-of read fixture: generate db name: %w", err)
	}
	return "asof_" + hex.EncodeToString(suffix), nil
}

// ensureCreated bare-creates id in storeID's store the first time it is
// asked for, giving RecordVersionAtInTx (called from mintAt) a base issues
// row to snapshot -- recordVersionAtInTx starts with GetIssueInTx, so it has
// nothing to read until this has run once. This goes through the real
// IssueUseCase, the same production entry point batch_creator.go and
// issue_operations.go use, unlike resolve/mintAt/makeUnanswerable below,
// which reach the shared issueops body directly (see the package doc on
// internal/storage/issueops/asof_read.go: "ALL THREE LEGS SHARE THIS BODY").
func (h *asOfReadHarness) ensureCreated(ctx context.Context, storeID, id string) error {
	key := [2]string{storeID, id}
	if h.created[key] {
		return nil
	}
	provider, err := h.provider(ctx, storeID)
	if err != nil {
		return err
	}
	issue := &types.Issue{
		ID:        id,
		Title:     "as-of read fixture issue",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: types.TypeTask,
	}
	err = RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		params := domain.CreateIssueParams{
			Issue:      issue,
			ExplicitID: issue.ID,
			CreateOnly: true,
		}
		_, err := uw.IssueUseCase().CreateIssue(ctx, params, "asof-read-fixture")
		return "as-of read fixture: bare-create " + issue.ID, err
	})
	if err != nil {
		return fmt.Errorf("as-of read fixture: bare-create %s: %w", id, err)
	}
	h.created[key] = true
	return nil
}

func (h *asOfReadHarness) resolve(ctx context.Context, storeID, id string, selector conformance.AsOfSelector) (conformance.AsOfReadResult, error) {
	provider, err := h.provider(ctx, storeID)
	if err != nil {
		return conformance.AsOfReadResult{}, err
	}
	result, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (storageissueops.AsOfReadResult, error) {
		runner, err := importStatementRunner(uw)
		if err != nil {
			return storageissueops.AsOfReadResult{}, err
		}
		return storageissueops.AsOfReadInTx(ctx, runner, storeID, id, toIssueopsSelector(selector))
	})
	if err != nil {
		return conformance.AsOfReadResult{}, err
	}
	return toConformanceResult(result), nil
}

// mintAt is the two-phase implementation MintAt's contract needs: phase one
// (ensureCreated) runs only once per (storeID, id); phase two
// (RecordVersionAtInTx) runs every call and always supplies the row mintAt
// reports the address of. RecordVersionAtInTx deliberately does not gate on
// versionedHistoryEnabled (this harness never turns that on for its stores),
// so it always mints, and reading MAX(revision) back inside the same
// transaction (read-your-writes) is how we learn which revision it picked --
// RecordVersionAtInTx itself reports no revision number.
//
// The commit message returned to RunTxResult is deliberately non-empty: an
// empty commitMsg makes RunTxResultWithin skip calling uw.Commit entirely
// (tx.go), so the write would never persist past this attempt's implicit
// rollback -- exactly the RunTxEphemeral hazard its own doc comment warns
// against for versioned tables.
func (h *asOfReadHarness) mintAt(ctx context.Context, storeID, id string, acceptedAt time.Time) (conformance.Address, error) {
	if err := h.ensureCreated(ctx, storeID, id); err != nil {
		return "", err
	}
	provider, err := h.provider(ctx, storeID)
	if err != nil {
		return "", err
	}
	revision, err := RunTxResult(ctx, provider, func(ctx context.Context, uw UnitOfWork) (int64, string, error) {
		runner, err := importStatementRunner(uw)
		if err != nil {
			return 0, "", err
		}
		if err := storageissueops.RecordVersionAtInTx(ctx, runner, id, "asof-read-fixture", acceptedAt); err != nil {
			return 0, "", err
		}
		var rev int64
		if err := runner.QueryRowContext(ctx,
			`SELECT MAX(revision) FROM issue_versions WHERE issue_id = ?`, id,
		).Scan(&rev); err != nil {
			return 0, "", err
		}
		return rev, "as-of read fixture: mint version for " + id, nil
	})
	if err != nil {
		return "", err
	}
	return conformance.Address(storageissueops.AsOfVersionAddress(id, revision)), nil
}

func (h *asOfReadHarness) makeUnanswerable(ctx context.Context, storeID, id string, restriction conformance.Restriction) error {
	asOfRestriction, ok := conformanceToAsOfRestriction[restriction]
	if !ok {
		return fmt.Errorf("as-of read fixture: unmapped restriction %v", restriction)
	}
	provider, err := h.provider(ctx, storeID)
	if err != nil {
		return err
	}
	reason := fmt.Sprintf("as-of read fixture: made unanswerable as %s", restriction)
	return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		runner, err := importStatementRunner(uw)
		if err != nil {
			return "", err
		}
		if err := storageissueops.MarkLatestVersionRemovedInTx(ctx, runner, id, asOfRestriction, reason); err != nil {
			return "", err
		}
		return "as-of read fixture: mark " + id + " removed", nil
	})
}

// conformanceToAsOfRestriction and its inverse are this leg's translation
// boundary between backend/conformance.Restriction (hyphenated String(),
// int-backed) and issueops.AsOfRestriction (underscored, string-backed --
// its values ARE the removed_restriction column's contents). issueops must
// stay conformance-oblivious and conformance must stay backend-oblivious
// (internal/storage/issueops/asof_read.go's package doc), so this mapping is
// deliberately duplicated per leg rather than shared centrally.
var conformanceToAsOfRestriction = map[conformance.Restriction]storageissueops.AsOfRestriction{
	conformance.RestrictionLive:               storageissueops.AsOfRestrictionLive,
	conformance.RestrictionGoneRetention:      storageissueops.AsOfRestrictionGoneRetention,
	conformance.RestrictionGoneErasure:        storageissueops.AsOfRestrictionGoneErasure,
	conformance.RestrictionGoneReorganization: storageissueops.AsOfRestrictionGoneReorganization,
	conformance.RestrictionUnknown:            storageissueops.AsOfRestrictionUnknown,
}

var asOfRestrictionToConformance = map[storageissueops.AsOfRestriction]conformance.Restriction{
	storageissueops.AsOfRestrictionLive:               conformance.RestrictionLive,
	storageissueops.AsOfRestrictionGoneRetention:      conformance.RestrictionGoneRetention,
	storageissueops.AsOfRestrictionGoneErasure:        conformance.RestrictionGoneErasure,
	storageissueops.AsOfRestrictionGoneReorganization: conformance.RestrictionGoneReorganization,
	storageissueops.AsOfRestrictionUnknown:            conformance.RestrictionUnknown,
}

func toIssueopsSelector(selector conformance.AsOfSelector) storageissueops.AsOfSelector {
	out := storageissueops.AsOfSelector{At: selector.At}
	if selector.Version != nil {
		out.Address = string(*selector.Version)
	}
	return out
}

func toConformanceResult(result storageissueops.AsOfReadResult) conformance.AsOfReadResult {
	if result.Refused {
		return conformance.AsOfReadResult{
			Refusal: &conformance.AsOfReadRefusal{
				Restriction: asOfRestrictionToConformance[result.Restriction],
				Reason:      result.Reason,
			},
		}
	}
	return conformance.AsOfReadResult{
		Address: conformance.Address(result.Address),
		State:   result.State,
	}
}

// TestAsOfReadContract wires this leg into the R7.1 as-of-read contract
// (gastownhall/beads#5898 revision 9, gastownhall/beads#6136) with a real
// Resolve/MintAt/MakeUnanswerable backed by one isolated UnitOfWorkProvider
// per storeID (asOfReadHarness).
func TestAsOfReadContract(t *testing.T) {
	// Everything through logPath below is the same one-time, Fatal-permitted
	// setup newTestUOWProvider does, run here directly instead of through
	// that helper: newTestUOWProvider both sets up AND builds one provider
	// bound to a fixed "beads" database, but this harness needs the setup
	// coordinates kept separate so provider() can build N providers, one per
	// storeID, lazily from inside subtest closures. See the asOfReadHarness
	// doc comment for why provider() must stay Fatalf/Skip-free, which is
	// exactly why this block -- the one place in this file where Fatalf/Skip
	// is safe -- must run here, before any t.Run.
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	cfgPath := writeServerConfig(t, port)
	logPath := filepath.Join(t.TempDir(), "server.log")

	ctx := context.Background()
	h := newAsOfReadHarness(t, storeRootDir, bin, cfgPath, logPath)
	fixture := conformance.AsOfReadFixture{
		IssuePrefix:      "asof",
		Resolve:          h.resolve,
		MintAt:           h.mintAt,
		MakeUnanswerable: h.makeUnanswerable,
	}

	t.Run("SelectorAcceptsEitherAnInstantOrAVersionAddress", func(t *testing.T) {
		conformance.RunAsOfReadSelectorAcceptsEitherAnInstantOrAVersionAddress(t, ctx, fixture)
	})
	t.Run("ReturnsTheLatestVersionAtOrBeforeTStrictlyExcludingLater", func(t *testing.T) {
		conformance.RunAsOfReadReturnsTheLatestVersionAtOrBeforeTStrictlyExcludingLater(t, ctx, fixture)
	})
	t.Run("ReportsTheAddressItServed", func(t *testing.T) {
		conformance.RunAsOfReadReportsTheAddressItServed(t, ctx, fixture)
	})
	t.Run("RefusesAsGoneOrUnknownNeverSubstitutingAnotherVersion", func(t *testing.T) {
		conformance.RunAsOfReadRefusesAsGoneOrUnknownNeverSubstitutingAnotherVersion(t, ctx, fixture)
	})
	t.Run("IsCompleteOnlyForTheAnsweringStoresHoldings", func(t *testing.T) {
		conformance.RunAsOfReadIsCompleteOnlyForTheAnsweringStoresHoldings(t, ctx, fixture)
	})
}
