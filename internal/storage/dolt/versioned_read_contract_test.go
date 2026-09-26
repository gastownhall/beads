package dolt

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// asOfReadHarness is this leg's Design D dispatch table (gastownhall/beads#5898
// revision 9, gastownhall/beads#6136, be-x5jqd.5): the conformance suite
// invents storeID values to select among N genuinely isolated stores, and
// isolating them is entirely this harness's job -- AsOfReadInTx itself is
// storeID-oblivious (see the package doc on
// internal/storage/issueops/asof_read.go). Each storeID gets its own fresh
// *DoltStore, built lazily on first use (buildAsOfReadStore) and torn down
// via t.Cleanup on the TestAsOfReadContract-level *testing.T. created tracks
// which (storeID, id) pairs already have a base issues row, since mintAt's
// second and later calls for the same pair must only add a historical
// issue_versions row, never a second base create.
//
// store() cannot use setupConcurrentTestStore. AsOfReadFixture's hook types
// (Resolve/MintAt/MakeUnanswerable) carry no *testing.T of their own, so a
// store built lazily inside a hook -- itself invoked from inside a t.Run
// subtest closure, sometimes nested two deep (the restriction-loop case
// below) -- has access only to whichever *testing.T this harness was
// constructed with: necessarily the OUTER TestAsOfReadContract's t, captured
// before any t.Run. setupConcurrentTestStore calls t.Skip/t.Fatalf
// internally, and calling FailNow/SkipNow on a *testing.T from a goroutine
// other than the one running that exact test -- which a t.Run subtest's
// closure always is, relative to the outer test -- panics with "subtest may
// have called FailNow on a parent test" (go's testing package requires
// FailNow/SkipNow to run only on the goroutine executing that specific
// test). buildAsOfReadStore below is a Fatalf/Skip-free twin of
// setupConcurrentTestStore's body for exactly this reason: its failures come
// back as a plain error, which flows through the calling hook's own
// (..., error) return to conformance.RunAsOfReadXXX, which correctly
// Fatalfs using the SUBTEST's own *testing.T on the SUBTEST's own goroutine.
// Dolt-availability skipping itself is handled once, up front in
// TestAsOfReadContract's own body via skipIfNoDolt(t) -- safe there because
// that runs before any t.Run, in the one goroutine that actually owns t.
type asOfReadHarness struct {
	t       *testing.T
	stores  map[string]*DoltStore
	created map[[2]string]bool
}

func newAsOfReadHarness(t *testing.T) *asOfReadHarness {
	return &asOfReadHarness{
		t:       t,
		stores:  map[string]*DoltStore{},
		created: map[[2]string]bool{},
	}
}

func (h *asOfReadHarness) store(ctx context.Context, storeID string) (*DoltStore, error) {
	if s, ok := h.stores[storeID]; ok {
		return s, nil
	}
	store, cleanup, err := buildAsOfReadStore(ctx)
	if err != nil {
		return nil, err
	}
	// t.Cleanup only appends to a mutex-guarded slice -- unlike Fatalf/Skip it
	// never calls runtime.Goexit, so registering it on the outer h.t from
	// inside a subtest's goroutine is safe. h.t's own cleanups run only after
	// ALL of TestAsOfReadContract's t.Run calls have returned (none of them,
	// nor their own nested t.Run calls, ever invoke t.Parallel), so every
	// store this harness builds stays open for the whole test, matching
	// created's own whole-test lifetime.
	h.t.Cleanup(cleanup)
	h.stores[storeID] = store
	return store, nil
}

// buildAsOfReadStore constructs one fresh, isolated *DoltStore -- the same
// shape setupConcurrentTestStore builds (own temp dir, own database,
// MaxOpenConns=2, issue_prefix seeded), minus every t.Fatalf/t.Skip call.
// The issue_prefix seed is NOT optional here, even though
// DoltStore.CreateIssue's single-issue path sets SkipPrefixValidation
// unconditionally (matches its own documented legacy behavior): that only
// skips validating an explicit ID's OWN prefix against the configured one.
// A separate, unconditional precondition -- issueops/helpers.go's
// resolvePrefix, "issue_prefix config is missing", wrapped in
// storage.ErrNotInitialized (bd-166) -- fails every write, including ones
// under SkipPrefixValidation, when no issue_prefix is configured at all.
// Confirmed the hard way: the first version of this function omitted the
// SetConfig call on the strength of the SkipPrefixValidation reasoning
// alone, and every MintAt in a real run failed with exactly that
// ErrNotInitialized. See the asOfReadHarness doc comment for why this
// function cannot take a *testing.T and call Fatalf/Skip the way
// setupConcurrentTestStore does.
//
// It also departs from setupConcurrentTestStore in when it releases
// testSem: setupConcurrentTestStore's callers build at most a couple of
// stores and hold the slot for the whole test via t.Cleanup, but this
// harness can lazily build many stores across one TestAsOfReadContract run
// (up to nine, across every RunAsOfReadXXX case), all held open
// simultaneously until h.t's own cleanups run at the very end. Holding one
// slot per store for the whole test against testSem's cap of 2 would make
// the third store's construction block on a slot only some OTHER test's
// completion could free -- an outright hang when this test runs alone (e.g.
// -run TestAsOfReadContract), since nothing else would ever be running to
// free one. testSem exists to bound concurrent SETUP load on the shared
// container, and this harness's own setups are already serialized (no
// subtest here calls t.Parallel), so acquiring only around New() and
// releasing immediately after -- rather than for the store's whole open
// lifetime -- still honors that purpose without the hang.
func buildAsOfReadStore(ctx context.Context) (*DoltStore, func(), error) {
	acquireTestSlot()
	defer releaseTestSlot()

	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-asof-test-*")
	if err != nil {
		return nil, nil, fmt.Errorf("as-of read fixture: create temp dir: %w", err)
	}

	dbSuffix := make([]byte, 6)
	if _, err := rand.Read(dbSuffix); err != nil {
		os.RemoveAll(tmpDir)
		return nil, nil, fmt.Errorf("as-of read fixture: generate db name: %w", err)
	}

	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        "testdb_asof_" + hex.EncodeToString(dbSuffix),
		MaxOpenConns:    2, // matches setupConcurrentTestStore: avoid overwhelming the test container
		CreateIfMissing: true,
	}

	store, err := New(ctx, cfg)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, nil, fmt.Errorf("as-of read fixture: create Dolt store: %w", err)
	}
	store.db.SetMaxIdleConns(0)
	store.db.SetConnMaxIdleTime(time.Second)

	if err := store.SetConfig(ctx, "issue_prefix", "asof"); err != nil {
		store.Close()
		os.RemoveAll(tmpDir)
		return nil, nil, fmt.Errorf("as-of read fixture: set issue_prefix: %w", err)
	}

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}
	return store, cleanup, nil
}

// ensureCreated bare-creates id in storeID's store the first time it is
// asked for, giving RecordVersionAtInTx (called from mintAt) a base issues
// row to snapshot -- recordVersionAtInTx starts with GetIssueInTx, so it has
// nothing to read until this has run once.
func (h *asOfReadHarness) ensureCreated(ctx context.Context, storeID, id string) error {
	key := [2]string{storeID, id}
	if h.created[key] {
		return nil
	}
	store, err := h.store(ctx, storeID)
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
	if err := store.CreateIssue(ctx, issue, "asof-read-fixture"); err != nil {
		return fmt.Errorf("as-of read fixture: bare-create %s: %w", id, err)
	}
	h.created[key] = true
	return nil
}

func (h *asOfReadHarness) resolve(ctx context.Context, storeID, id string, selector conformance.AsOfSelector) (conformance.AsOfReadResult, error) {
	store, err := h.store(ctx, storeID)
	if err != nil {
		return conformance.AsOfReadResult{}, err
	}
	var result issueops.AsOfReadResult
	err = store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		result, err = issueops.AsOfReadInTx(ctx, tx, storeID, id, toIssueopsSelector(selector))
		return err
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
func (h *asOfReadHarness) mintAt(ctx context.Context, storeID, id string, acceptedAt time.Time) (conformance.Address, error) {
	if err := h.ensureCreated(ctx, storeID, id); err != nil {
		return "", err
	}
	store, err := h.store(ctx, storeID)
	if err != nil {
		return "", err
	}
	var revision int64
	err = store.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := issueops.RecordVersionAtInTx(ctx, tx, id, "asof-read-fixture", acceptedAt); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx,
			`SELECT MAX(revision) FROM issue_versions WHERE issue_id = ?`, id,
		).Scan(&revision)
	})
	if err != nil {
		return "", err
	}
	return conformance.Address(issueops.AsOfVersionAddress(id, revision)), nil
}

func (h *asOfReadHarness) makeUnanswerable(ctx context.Context, storeID, id string, restriction conformance.Restriction) error {
	asOfRestriction, ok := conformanceToAsOfRestriction[restriction]
	if !ok {
		return fmt.Errorf("as-of read fixture: unmapped restriction %v", restriction)
	}
	store, err := h.store(ctx, storeID)
	if err != nil {
		return err
	}
	reason := fmt.Sprintf("as-of read fixture: made unanswerable as %s", restriction)
	return store.withWriteTx(ctx, func(tx *sql.Tx) error {
		return issueops.MarkLatestVersionRemovedInTx(ctx, tx, id, asOfRestriction, reason)
	})
}

// conformanceToAsOfRestriction and its inverse are this leg's translation
// boundary between backend/conformance.Restriction (hyphenated String(),
// int-backed) and issueops.AsOfRestriction (underscored, string-backed --
// its values ARE the removed_restriction column's contents). issueops must
// stay conformance-oblivious and conformance must stay backend-oblivious
// (internal/storage/issueops/asof_read.go's package doc), so this mapping is
// deliberately duplicated per leg rather than shared centrally.
var conformanceToAsOfRestriction = map[conformance.Restriction]issueops.AsOfRestriction{
	conformance.RestrictionLive:               issueops.AsOfRestrictionLive,
	conformance.RestrictionGoneRetention:      issueops.AsOfRestrictionGoneRetention,
	conformance.RestrictionGoneErasure:        issueops.AsOfRestrictionGoneErasure,
	conformance.RestrictionGoneReorganization: issueops.AsOfRestrictionGoneReorganization,
	conformance.RestrictionUnknown:            issueops.AsOfRestrictionUnknown,
}

var asOfRestrictionToConformance = map[issueops.AsOfRestriction]conformance.Restriction{
	issueops.AsOfRestrictionLive:               conformance.RestrictionLive,
	issueops.AsOfRestrictionGoneRetention:      conformance.RestrictionGoneRetention,
	issueops.AsOfRestrictionGoneErasure:        conformance.RestrictionGoneErasure,
	issueops.AsOfRestrictionGoneReorganization: conformance.RestrictionGoneReorganization,
	issueops.AsOfRestrictionUnknown:            conformance.RestrictionUnknown,
}

func toIssueopsSelector(selector conformance.AsOfSelector) issueops.AsOfSelector {
	out := issueops.AsOfSelector{At: selector.At}
	if selector.Version != nil {
		out.Address = string(*selector.Version)
	}
	return out
}

func toConformanceResult(result issueops.AsOfReadResult) conformance.AsOfReadResult {
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
// Resolve/MintAt/MakeUnanswerable backed by one isolated *DoltStore per
// storeID (asOfReadHarness).
func TestAsOfReadContract(t *testing.T) {
	// Must run before any t.Run: skipIfNoDolt calls t.Skip/t.Fatalf
	// internally, and this is the one place in this file where that is safe
	// -- the outer test's own goroutine, before any subtest closure exists.
	// See the asOfReadHarness doc comment for why no other call in this file
	// may do the same.
	skipIfNoDolt(t)

	ctx := context.Background()
	h := newAsOfReadHarness(t)
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
