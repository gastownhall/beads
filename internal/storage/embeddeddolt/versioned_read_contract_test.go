//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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
// *EmbeddedDoltStore, built lazily on first use (buildAsOfReadStore) and torn
// down via t.Cleanup on the TestAsOfReadContract-level *testing.T. created
// tracks which (storeID, id) pairs already have a base issues row, since
// mintAt's second and later calls for the same pair must only add a
// historical issue_versions row, never a second base create.
//
// store() cannot use a *testing.T-tied fixture helper (e.g.
// newPristineEmbeddedDoltFixture): AsOfReadFixture's hook types
// (Resolve/MintAt/MakeUnanswerable) carry no *testing.T of their own, so a
// store built lazily inside a hook -- itself invoked from inside a t.Run
// subtest closure, sometimes nested two deep (the restriction-loop case
// below) -- has access only to whichever *testing.T this harness was
// constructed with: necessarily the OUTER TestAsOfReadContract's t, captured
// before any t.Run. Calling FailNow/SkipNow (t.Fatalf/t.Skip) on a *testing.T
// from a goroutine other than the one running that exact test -- which a
// t.Run subtest's closure always is, relative to the outer test -- panics
// with "subtest may have called FailNow on a parent test" (go's testing
// package requires FailNow/SkipNow to run only on the goroutine executing
// that specific test). buildAsOfReadStore below is a Fatalf/Skip-free
// construction path for exactly this reason: its failures come back as a
// plain error, which flows through the calling hook's own (..., error)
// return to conformance.RunAsOfReadXXX, which correctly Fatalfs using the
// SUBTEST's own *testing.T on the SUBTEST's own goroutine. Embedded-Dolt
// availability skipping itself is handled once, up front in
// TestAsOfReadContract's own body via skipUnlessEmbeddedDolt(t) -- safe there
// because that runs before any t.Run, in the one goroutine that actually owns
// t. See internal/storage/dolt/versioned_read_contract_test.go for this
// leg's sibling, which this file mirrors as closely as the two backends'
// store-construction APIs allow.
type asOfReadHarness struct {
	t       *testing.T
	stores  map[string]*EmbeddedDoltStore
	created map[[2]string]bool
}

func newAsOfReadHarness(t *testing.T) *asOfReadHarness {
	return &asOfReadHarness{
		t:       t,
		stores:  map[string]*EmbeddedDoltStore{},
		created: map[[2]string]bool{},
	}
}

func (h *asOfReadHarness) store(ctx context.Context, storeID string) (*EmbeddedDoltStore, error) {
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

// buildAsOfReadStore constructs one fresh, isolated *EmbeddedDoltStore: its
// own temp directory (so its own on-disk database, with nothing shared
// between stores -- unlike the dolt leg's single testcontainers server, an
// embedded store's isolation is just its own directory, so a fixed database
// name is safe to reuse across every call), issue_prefix seeded. Minus every
// t.Fatalf/t.Skip call; see the asOfReadHarness doc comment for why no call
// in this file may make one outside TestAsOfReadContract's own body.
func buildAsOfReadStore(ctx context.Context) (*EmbeddedDoltStore, func(), error) {
	tmpDir, err := os.MkdirTemp("", "embeddeddolt-asof-test-*")
	if err != nil {
		return nil, nil, fmt.Errorf("as-of read fixture: create temp dir: %w", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	store, err := Open(ctx, beadsDir, "asof", "main")
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, nil, fmt.Errorf("as-of read fixture: open embedded Dolt store: %w", err)
	}

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
	err = store.withConn(ctx, false, func(tx *sql.Tx) error {
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
	err = store.withConn(ctx, true, func(tx *sql.Tx) error {
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
	return store.withConn(ctx, true, func(tx *sql.Tx) error {
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

// skipUnlessEmbeddedDolt is this file's own copy of the embeddeddolt_test
// package's helper of the same name (create_issue_test.go): that one is
// unexported and lives in the external test package, unreachable from here.
// This harness must live in-package (see the asOfReadHarness doc comment for
// why: raw DBTX access requires the unexported withConn), so it carries its
// own copy rather than importing across the package/package_test boundary.
// Reads the same BEADS_TEST_EMBEDDED_DOLT env var so `go test
// ./internal/storage/embeddeddolt/...` gates uniformly regardless of which
// half of the package a given test file lives in.
func skipUnlessEmbeddedDolt(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
}

// TestAsOfReadContract wires this leg into the R7.1 as-of-read contract
// (gastownhall/beads#5898 revision 9, gastownhall/beads#6136) with a real
// Resolve/MintAt/MakeUnanswerable backed by one isolated *EmbeddedDoltStore
// per storeID (asOfReadHarness).
func TestAsOfReadContract(t *testing.T) {
	// Must run before any t.Run: skipUnlessEmbeddedDolt calls t.Skip
	// internally, and this is the one place in this file where that is safe
	// -- the outer test's own goroutine, before any subtest closure exists.
	// See the asOfReadHarness doc comment for why no other call in this file
	// may do the same.
	skipUnlessEmbeddedDolt(t)

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
