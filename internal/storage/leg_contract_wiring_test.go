package storage

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// conformancePackage is the import path whose Run entrypoints each leg wires.
const conformancePackage = "github.com/steveyegge/beads/backend/conformance"

// neverSatisfiedTags are build tags nothing ever sets. A file behind one is in
// no build, so what it names is not wiring.
var neverSatisfiedTags = map[string]bool{"ignore": true, "never": true}

// importerOneAccessorWaiverReason records why the Importer contract runs on one
// leg. publicops.Importer has exactly one accessor anywhere — uow.ImporterSource
// — and it is the only capability source in internal/storage/uow with no
// storage.DoltStorage counterpart: on both store legs `bd import` still runs the
// raw CreateIssuesWithFullOptions seam. Wiring the other two is a new interface
// method plus a new body at each backend, not a test change. The waiver stops
// the moment either leg grows the accessor.
const importerOneAccessorWaiverReason = "Importer has one accessor (uow.ImporterSource); " +
	"the store legs run bd import through the raw seam and implement no Importer role"

// The waivers of the three legs registered here. Each is registered beside its
// leg rather than written into one literal, so a leg registered elsewhere
// brings its waivers the same way; see unwiredContractEntrypoints.
func init() {
	registerContractLegWaivers("dolt", map[string]string{
		"RunImporterRejectsAStaleRowAndNamesIt":           importerOneAccessorWaiverReason,
		"RunImporterReportsTheAbsentTargetItDroppedOnce":  importerOneAccessorWaiverReason,
		"RunImporterWiresTheCrossPlaneEdgeBetweenItsRows": importerOneAccessorWaiverReason,
		"RunImporterReportsTheCycleEdgeItDropped":         importerOneAccessorWaiverReason,
		"RunBootstrapperRecordsExactlyOneHistoryEntry":    bootstrapSplitWaiverReason,
	})
	registerContractLegWaivers("embeddeddolt", map[string]string{
		"RunImporterRejectsAStaleRowAndNamesIt":           importerOneAccessorWaiverReason,
		"RunImporterReportsTheAbsentTargetItDroppedOnce":  importerOneAccessorWaiverReason,
		"RunImporterWiresTheCrossPlaneEdgeBetweenItsRows": importerOneAccessorWaiverReason,
		"RunImporterReportsTheCycleEdgeItDropped":         importerOneAccessorWaiverReason,
		"RunBootstrapperRecordsExactlyOneHistoryEntry":    bootstrapSplitWaiverReason,
	})
	registerContractLegWaivers("uow", map[string]string{
		"RunBootstrapperRecordsNoHistoryEntryOfItsOwn":                   bootstrapSplitWaiverReason,
		"RunIssueOperationsCreateReverseNonBlockingStagesConcreteTables": stagingWaiverReason,
		"RunIssueOperationsCreateParentChildRecomputesWaitsForClosure":   stagingWaiverReason,
	})
	registerContractLegWaivers("http", map[string]string{
		"RunCycleDetectorIncludeTracksFindsTheMoleculeRootShape":         cycleTracksWireGapWaiverReason,
		"RunCycleDetectorIncludeTracksIgnoresAPureTracksLoop":            cycleTracksWireGapWaiverReason,
		"RunCycleDetectorIncludeTracksWalksEachEdgeInItsStoredDirection": cycleTracksWireGapWaiverReason,

		"RunImporterRejectsAStaleRowAndNamesIt":           importerOneAccessorWaiverReason,
		"RunImporterReportsTheAbsentTargetItDroppedOnce":  importerOneAccessorWaiverReason,
		"RunImporterWiresTheCrossPlaneEdgeBetweenItsRows": importerOneAccessorWaiverReason,
		"RunImporterReportsTheCycleEdgeItDropped":         importerOneAccessorWaiverReason,

		"RunADestructiveOperationEnumeratesAffectedAddressesFirst":     retentionEpochNoRoleWaiverReason,
		"RunAHoldPreventsRemovalAndReportsInRetainedBounds":            retentionEpochNoRoleWaiverReason,
		"RunAStoreThatRemovesStateStillAnswersGoneDurably":             retentionEpochNoRoleWaiverReason,
		"RunAStoreWithNoLineageKnowledgeAnswersUnknownNotGone":         retentionEpochNoRoleWaiverReason,
		"RunAnAddressNeverResolvesToADifferentState":                   retentionEpochNoRoleWaiverReason,
		"RunAnEpochBumpIsTriggeredOnlyByRestoreReinitOrSchemeChange":   retentionEpochNoRoleWaiverReason,
		"RunEpochBumpVoidsOnlyAddressesOfVersionsNoLongerServed":       retentionEpochNoRoleWaiverReason,
		"RunErasureMintsACorrectedVersionRatherThanEditingInPlace":     retentionEpochNoRoleWaiverReason,
		"RunEveryRetentionAnswerNamesItsProducingStore":                retentionEpochNoRoleWaiverReason,
		"RunForcingAHeldRemovalRecordsWhoWhenWhy":                      retentionEpochNoRoleWaiverReason,
		"RunGoneIsDistinguishableFromUnknown":                          retentionEpochNoRoleWaiverReason,
		"RunRemovalLeavesTheAddressAbleToAnswer":                       retentionEpochNoRoleWaiverReason,
		"RunRemovalNeverReassignsASurvivingAddress":                    retentionEpochNoRoleWaiverReason,
		"RunRemovalReasonIsRetentionErasureOrReorganizationDistinctly": retentionEpochNoRoleWaiverReason,
		"RunRemovalReportsTheSurvivingRetainedWindow":                  retentionEpochNoRoleWaiverReason,

		"RunAdvisoryLeaseAloneDoesNotPreventTheInvariantViolation":    crossRecordInvariantNoRoleWaiverReason,
		"RunCrossRecordInvariantSurvivesTwoPassingPerRecordGuards":    crossRecordInvariantNoRoleWaiverReason,
		"RunInvariantRefusalNamesTheViolatedInvariant":                crossRecordInvariantNoRoleWaiverReason,
		"RunLeaseAcquisitionRecordsNoHistoryEntry":                    crossRecordInvariantNoRoleWaiverReason,
		"RunMaximumEndpointMultiplicityIsAStoreInvariantNotACAS":      crossRecordInvariantNoRoleWaiverReason,
		"RunMemoryKeyAliasUniquenessIsAStoreInvariantNotACAS":         crossRecordInvariantNoRoleWaiverReason,
		"RunStoreInvariantTransactionScopesExactlyTheSpanningRecords": crossRecordInvariantNoRoleWaiverReason,
	})
}

// cycleTracksWireGapWaiverReason covers the one CycleDetector widening option
// the HTTP wire does not carry at all.
//
// issueops.DetectCyclesRequest.IncludeTracks is a WIDENING field consumed
// today only by `bd dep cycles` in-process. internal/httpapi/cycles.go calls
// DetectCycles with a bare issueops.DetectCyclesRequest{} — it decodes no
// query parameter for it — and internal/httpclient's httpCycleDetector reads
// the field and, finding no wire parameter to carry it, REFUSES the request
// with a typed, ledger-cited error (encode.RefusedError via refuse(), citing
// "L-cycles-tracks") rather than silently answering the narrower default
// walk. Neither side of the wire has any plumbing for this field, so the
// three IncludeTracks cases cannot be satisfied over HTTP — the client fails
// loudly instead of wiring a narrower answer, which is why these three remain
// unwired rather than run-and-skipped.
//
// No slice currently scoped (S0-S13 in beads-design/slices.json) adds a query
// parameter, spec capability token, or handshake entry for this option; S3's
// own scope lists CycleDetector only for the base walk. Closing this gap is a
// new slice — a query parameter plus spec/capability plumbing on both ends —
// not a missing test line, so the waiver stays until that slice exists rather
// than naming one of S7/S8/S9/S10/S12/S13, none of which touch cycle
// detection.
const cycleTracksWireGapWaiverReason = "issueops.DetectCyclesRequest.IncludeTracks has no wire plumbing on " +
	"either side of internal/httpapi or internal/httpclient today (cycles.go always calls DetectCycles with a " +
	"zero-value request; the client's DetectCycles refuses the request via a typed, ledger-cited error — see " +
	"\"L-cycles-tracks\" — rather than silently answering the narrower walk); no slice through S13 adds a wire " +
	"parameter for it, so closing this is a new, not-yet-numbered slice rather than a missing test line"

// retentionEpochNoRoleWaiverReason covers the fifteen RetentionFixture and
// EpochFixture cases: PERMANENTLY unservable over HTTP, not merely unwired
// yet.
//
// Both fixtures are RAW STORAGE HOOKS, not a publicops role: RetentionFixture
// and EpochFixture (backend/conformance/retention_epoch_contract.go) carry no
// publicops.XXX field at all, only closures like Resolve, Remove, Hold,
// Erase, Mint, CurrentEpoch, and BumpEpoch that reach past every role
// straight at a backend's own address/epoch/retention bookkeeping — the same
// bookkeeping a storage engine keeps about itself, not a capability it was
// ever going to publish to a caller. dolt, embeddeddolt, and uow all wire
// this contract because all three ARE storage engines with that bookkeeping
// to show; the http leg is a REMOTE CLIENT of one, with no address table,
// epoch counter, or retention window of its own to report — there is nothing
// behind this door for a wire operation to open onto, now or in a later
// slice, unless some future design gives retention/epoch state its own
// publicops role and wire surface. That would be a new role, not a missing
// test line, which is why this is a named waiver rather than a ceiling
// entry.
const retentionEpochNoRoleWaiverReason = "RetentionFixture and EpochFixture carry raw address/epoch/retention " +
	"storage hooks (Resolve, Remove, Hold, Erase, Mint, CurrentEpoch, BumpEpoch, ...) with no publicops role " +
	"behind them; dolt, embeddeddolt and uow wire this contract because they ARE the storage engine this " +
	"bookkeeping belongs to, and the http leg is a remote client with no such bookkeeping of its own to report " +
	"— there is no role or wire surface for this to ever route through unless one is designed from scratch"

// crossRecordInvariantNoRoleWaiverReason covers the seven CrossRecordInvariantFixture
// cases, for the same shape of reason as retentionEpochNoRoleWaiverReason.
//
// CrossRecordInvariantFixture (backend/conformance/cross_record_invariant_contract.go)
// is likewise raw hooks with no publicops field — GuardedWrite,
// EnforceCrossRecordInvariant, AcquireAdvisoryLease,
// CountHistoryForSubject, MemoryKeyAliasWrite, GraphEdgeWrite — that assert a
// store's OWN invariant enforcement spanning more than one record at once,
// the kind of guarantee a storage engine's own transaction boundary gives,
// not a wire operation it exposes. dolt, embeddeddolt and uow wire it as the
// engines that hold that boundary; the http leg has none of its own to test.
const crossRecordInvariantNoRoleWaiverReason = "CrossRecordInvariantFixture carries raw per-record/cross-record " +
	"storage hooks (GuardedWrite, EnforceCrossRecordInvariant, AcquireAdvisoryLease, CountHistoryForSubject, " +
	"MemoryKeyAliasWrite, GraphEdgeWrite) with no publicops role behind them, asserting a storage engine's own " +
	"transaction boundary rather than something exposed over a wire; the http leg has no transaction boundary " +
	"of its own for this to test"

// bootstrapSplitWaiverReason covers the one pair of entrypoints that is a
// RATIFIED PER-LEG SPLIT rather than a gap.
//
// The bootstrap history contracts come in two halves that contradict each other
// on purpose: the store legs assert the role records NO entry, because `bd
// init`'s own commit records it and an in-role commit would double it, and the
// unit-of-work leg asserts EXACTLY ONE, because the proxied init route has no
// other commit point and a zero there would leave the identity unversioned.
// Each leg wires the half that is true of the front door it stands behind, and
// wiring the other half would assert a number that leg is right not to produce.
//
// This is the shape a lock like this has to be able to express. Every other
// entry here says "this leg cannot run that contract"; this one says "that
// contract is another leg's promise". Both stay checked: the pair is exhaustive
// across the three legs, and if either half stopped being wired anywhere the
// leg that owns it fails.
const bootstrapSplitWaiverReason = "the bootstrap history contracts are a ratified per-leg split — the store " +
	"legs pin zero because `bd init` commits the identity itself, the unit-of-work leg pins one because the " +
	"proxied route has no other commit point; each leg wires its own half and the other half is not its promise"

// stagingWaiverReason is why the two staging contracts stop at the two
// store-backed legs.
//
// Both assert what a create COMMITS: seed rows, commit them, dirty an unrelated
// durable row in the working set, create, then read `AS OF 'HEAD'` to prove the
// create staged its own tables without sweeping the dirty row in. That needs a
// caller-held working set and a Commit hook to close it, and the unit-of-work
// provider has neither by design — every unit of work commits itself, with its
// own message, so there is no uncommitted state for a caller to leave lying
// around and no separate commit for one to be swept into. Its fixture supplies
// neither Commit nor Exec (see uow/role_fixture_kit_test.go), and the two cases
// dereference Commit unguarded rather than skipping loudly.
//
// Wiring them would mean inventing a commit boundary this backend does not
// have, to assert a property it cannot violate. That is a change to the
// backend's test surface, not a missing line, so it belongs in a change of its
// own rather than arriving behind a wiring lock.
const stagingWaiverReason = "the unit-of-work provider commits every unit of work itself, so it has no " +
	"caller-held working set to stage into and no Commit hook to close one; these two cases assert what a " +
	"create sweeps into a commit the caller opened"

// TestEveryLegWiresEveryRoleContract fails when a backend leg skips a role
// contract the conformance package exports.
//
// The contracts are shared source: writing one is worth nothing until every leg
// runs it, and nothing about adding a Run entrypoint reminds an author to wire
// it into three test files. A leg missing one is invisible — its own suite
// still passes, and the entrypoint still has two other backends behind it, so
// the silence looks exactly like coverage.
//
// It reads source rather than running anything, so it holds for the legs whose
// suites need infrastructure this test does not have: the server-backed store
// needs a live sql-server and the embedded store needs cgo, and their wiring is
// checked here either way.
func TestEveryLegWiresEveryRoleContract(t *testing.T) {
	root := repositoryRoot(t)
	entrypoints := roleContractEntrypoints(t, filepath.Join(root, "backend", "conformance"))
	if len(entrypoints) == 0 {
		t.Fatal("the conformance package exports no role contract entrypoints; this test would pass vacuously")
	}
	legs := registeredContractLegs(t)
	known := map[string]bool{}
	for _, name := range entrypoints {
		known[name] = true
	}
	registered := map[string]bool{}

	for _, leg := range legs {
		registered[leg.name] = true
		t.Run(leg.name, func(t *testing.T) {
			waived := unwiredContractEntrypoints[leg.name]
			wiring := inspectLegWiring(t, legWiringDir(root, leg), entrypoints, waived)

			// A leg that names nothing is the one way the lock can pass while
			// checking nothing: full waiver or ceiling coverage over a wiring
			// root that resolves nowhere leaves no missing contract to report
			// and no false waiver to catch. Nothing cross-checks the path — for
			// a leg outside internal/storage, not even the tripwire — so the
			// count has to be asked about directly.
			if wiring.named == 0 {
				t.Errorf("leg %s names no contract entrypoint at all under %s; check the wiring root.",
					leg.name, leg.wiringRoot)
			}
			for _, name := range wiring.falselyWaived {
				t.Errorf("%s is waived as unwired for %s but the leg runs it: "+
					"delete its entry from unwiredContractEntrypoints", name, leg.name)
			}
			if fault := adoptionFault(leg, wiring, len(entrypoints)); fault != "" {
				t.Error(fault)
			}

			for _, name := range sortedNames(waived) {
				if !known[name] {
					t.Errorf("unwiredContractEntrypoints waives %q for %s, which is no contract entrypoint", name, leg.name)
					continue
				}
				if strings.TrimSpace(waived[name]) == "" {
					t.Errorf("unwiredContractEntrypoints waives %s for %s with no reason", name, leg.name)
				}
			}
		})
	}

	for _, leg := range sortedNames(unwiredContractEntrypoints) {
		if !registered[leg] {
			t.Errorf("unwiredContractEntrypoints waives entrypoints for %q, which is no registered leg", leg)
		}
	}
	for _, duplicate := range duplicateContractLegWaivers {
		t.Errorf("%s was waived twice; the second registration was dropped rather than merged, so one of "+
			"the two reasons is not being checked", duplicate)
	}
}

// adoptionFault reports why a leg's count of skipped contracts disagrees with
// what it promised, or "" when they agree.
//
// The comparison is EXACT against the leg's adoption ceiling, which is what
// makes the ceiling a ratchet: skipping more than it allows fails, and so does
// skipping fewer, so a tranche of wiring cannot land without lowering the
// number in the same change. A leg with no ceiling — the three here — is the
// same rule at zero: every gap has to be a named, reasoned waiver.
//
// It returns the message rather than failing so both arms can be proved against
// a fabricated leg, where the arms are reachable; against this repository only
// the agreeing case ever runs.
func adoptionFault(leg contractLeg, wiring legWiring, tier int) string {
	skipped := len(wiring.missing)
	if skipped == leg.adoptionCeiling {
		return ""
	}
	detail := fmt.Sprintf(" (%d waived)", wiring.waived)
	if len(wiring.excluded) > 0 {
		detail += fmt.Sprintf(" (ignoring %d file(s) no build includes: %s)",
			len(wiring.excluded), strings.Join(wiring.excluded, ", "))
	}
	if skipped < leg.adoptionCeiling {
		return fmt.Sprintf("%s skips %d of the %d role contract entrypoints%s but its adoption ceiling is "+
			"%d: lower the ceiling to %d. The ceiling is a ratchet — it has to fall as wiring lands, or it "+
			"stops measuring how far adoption got and starts being a budget nobody reviewed",
			leg.name, skipped, tier, detail, leg.adoptionCeiling, skipped)
	}
	if leg.adoptionCeiling == 0 {
		return fmt.Sprintf("%s names %d of the %d role contract entrypoints%s; it never names: %s",
			leg.name, wiring.named, tier, detail, summarizeNames(wiring.missing))
	}
	return fmt.Sprintf("%s skips %d of the %d role contract entrypoints%s, %d past its adoption ceiling of "+
		"%d (%s). The ceiling is a ratchet, not an exemption: wire the rest, or raise it in a reviewed "+
		"change that says why the leg still cannot. It never names: %s",
		leg.name, skipped, tier, detail, skipped-leg.adoptionCeiling, leg.adoptionCeiling, leg.adopting,
		summarizeNames(wiring.missing))
}

// summarizeNames joins names, stopping short of printing a whole tier. A leg
// part-way through adoption skips hundreds of contracts, and a failure that
// prints all of them buries the count that is the actionable part.
func summarizeNames(names []string) string {
	const most = 10
	if len(names) <= most {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:most], ", "), len(names)-most)
}

// legWiring is what one leg's test sources say about the role contract tier,
// with that leg's waivers already spent.
type legWiring struct {
	// named is how many contract entrypoints the leg's sources name.
	named int
	// missing are the entrypoints the leg neither names nor is waived for.
	missing []string
	// falselyWaived are entrypoints waived as unwired that the leg does run.
	falselyWaived []string
	// excluded are the files no build includes, which were not read.
	excluded []string
	// waived is how many per-entrypoint waivers the leg carries.
	waived int
}

// inspectLegWiring diffs what the leg rooted at dir names against the contract
// tier.
//
// It reports rather than fails so the mechanism can be proved against a
// fabricated leg — see TestARegisteredLegOutsideTheStorageTreeIsLockedToo,
// which is the only place a leg's wiring root outside internal/storage is
// exercised until a distribution registers one.
func inspectLegWiring(t *testing.T, dir string, entrypoints []string, waived map[string]string) legWiring {
	t.Helper()
	wired, excluded := conformanceEntrypointsWiredBy(t, dir)
	wiring := legWiring{excluded: excluded, waived: len(waived)}
	for _, name := range entrypoints {
		_, isWaived := waived[name]
		if wired[name] {
			wiring.named++
		}
		switch {
		case wired[name] && isWaived:
			wiring.falselyWaived = append(wiring.falselyWaived, name)
		case !wired[name] && !isWaived:
			wiring.missing = append(wiring.missing, name)
		}
	}
	return wiring
}

// repositoryRoot locates the module root from this file's own path.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	if root := bazeltest.OverrideRoot(); root != "" {
		return root
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

// roleContractEntrypoints reports the role tier: every exported Run function
// the conformance package declares whose final parameter is one of its own
// Fixture types.
//
// The SHAPE is what makes an entrypoint role tier, not the file it sits in. An
// earlier version of this test read the *_contract.go filenames and missed the
// two staging cases in issue_operations_staging.go — which two of the three
// legs wire and one does not, exactly the drift this test is for. A fixture
// parameter is the tier's defining shape: RunAll and the audit suites take a
// Factory and are wired once per leg rather than case by case.
func roleContractEntrypoints(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	var names []string
	for _, pkg := range pkgs {
		fixtures := fixtureTypeNames(pkg)
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Run") || !ast.IsExported(fn.Name.Name) {
					continue
				}
				if fixtures[finalParamTypeName(fn)] {
					names = append(names, fn.Name.Name)
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// fixtureTypeNames reports the package's own Fixture types.
func fixtureTypeNames(pkg *ast.Package) map[string]bool {
	names := map[string]bool{}
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !strings.HasSuffix(ts.Name.Name, "Fixture") {
					continue
				}
				names[ts.Name.Name] = true
			}
		}
	}
	return names
}

// finalParamTypeName reports the package-local type name of a function's last
// parameter, or "" when it has none or names a type from elsewhere.
func finalParamTypeName(fn *ast.FuncDecl) string {
	params := fn.Type.Params
	if params == nil || len(params.List) == 0 {
		return ""
	}
	last := params.List[len(params.List)-1].Type
	if star, ok := last.(*ast.StarExpr); ok {
		last = star.X
	}
	if ident, ok := last.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// conformanceEntrypointsWiredBy reports the conformance entrypoints a leg's
// test sources name, resolved through each file's own import of the conformance
// package so a renamed import still counts. It also reports the files it
// refused to read.
//
// It counts every reference rather than only calls, because a leg is free to
// wire an entrypoint as a value: the unit-of-work leg drives five of its roles
// from a table whose rows hold `run: conformance.RunX` and call it later
// through the field. Counting calls alone read those ninety-one contracts as
// unwired.
//
// A file behind a build tag nothing sets is REFUSED: `//go:build ignore` over a
// file naming every entrypoint would otherwise satisfy this lock with source no
// build compiles. Ordinary constraints — cgo, integration — are counted,
// because a contract wired behind one is still wired.
//
// A REFERENCE IS NOT A PASS, and that gap is deliberate rather than a hole in
// this check. Several contracts (RetentionFixture, EpochFixture, BootstrapperFixture,
// VersionReconcilerFixture, ...) are written so a nil hook makes the case
// t.Skip loudly, naming the hook, rather than asserting nothing silently —
// see each fixture's own doc comment. That lets a leg wire the ENTRYPOINT
// (call conformance.RunX, satisfying this static check) while leaving some
// or all of its hooks nil, which runs for real as a skip, not a pass. This
// check cannot see past the call to know which hooks a wiring supplies, so
// it counts the reference as wired either way — a leg that did this would
// read as adopted here while its own `go test -v` output says SKIP. That
// divergence is this package's to prevent by review, the same way
// divergence_citation_gate_test.go (internal/httpclient) already holds the
// served tier's own skipKnownDivergence call sites to a named, ledger-cited
// reason instead of a bare t.Skip: a reviewer reading a wiring diff that adds
// a conformance.RunX reference should expect it to pass for real, or to carry
// a waiver in unwiredContractEntrypoints instead — not to compile a skip and
// call it adopted. As of this lock's current counts, no leg does this: every
// entrypoint a leg's sources name either has every hook that entrypoint's own
// cases need, or the gap is a named waiver here rather than a nil hook left
// standing behind a call this check would count as wired.
func conformanceEntrypointsWiredBy(t *testing.T, dir string) (map[string]bool, []string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatalf("globbing %s: %v", dir, err)
	}
	wired := map[string]bool{}
	var excluded []string
	for _, path := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		if namesNeverSatisfiedTag(parsed) {
			excluded = append(excluded, filepath.Base(path))
			continue
		}
		local := conformanceImportName(parsed)
		if local == "" {
			continue
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			selector, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkgName, ok := selector.X.(*ast.Ident); ok && pkgName.Name == local {
				wired[selector.Sel.Name] = true
			}
			return true
		})
	}
	sort.Strings(excluded)
	return wired, excluded
}

// namesNeverSatisfiedTag reports whether a file's build constraint can be
// satisfied by no build at all.
func namesNeverSatisfiedTag(file *ast.File) bool {
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if !constraint.IsGoBuild(comment.Text) {
				continue
			}
			expr, err := constraint.Parse(comment.Text)
			if err != nil {
				continue
			}
			if !satisfiable(expr) {
				return true
			}
		}
	}
	return false
}

// satisfiable reports whether some build sets tags that make expr true, taking
// the never-set tags as the only ones that cannot be turned on.
//
// It asks about satisfiability rather than evaluating with every tag set true,
// which is what an earlier draft did and got wrong: under that oracle
// `integration && !windows` reads as false, and five real integration files
// were dropped from the wiring count for having a negation in them.
func satisfiable(expr constraint.Expr) bool {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		return !neverSatisfiedTags[e.Tag]
	case *constraint.NotExpr:
		return falsifiable(e.X)
	case *constraint.AndExpr:
		return satisfiable(e.X) && satisfiable(e.Y)
	case *constraint.OrExpr:
		return satisfiable(e.X) || satisfiable(e.Y)
	}
	return true
}

// falsifiable reports whether some build leaves expr false. Any tag can be left
// unset, so a bare tag always can be.
func falsifiable(expr constraint.Expr) bool {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		return true
	case *constraint.NotExpr:
		return satisfiable(e.X)
	case *constraint.AndExpr:
		return falsifiable(e.X) || falsifiable(e.Y)
	case *constraint.OrExpr:
		return falsifiable(e.X) && falsifiable(e.Y)
	}
	return true
}

// conformanceImportName reports the name a file refers to the conformance
// package by, or "" when it does not import it.
func conformanceImportName(file *ast.File) string {
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		if path != conformancePackage {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return path[strings.LastIndexByte(path, '/')+1:]
	}
	return ""
}

// sortedNames returns a map's keys in a deterministic order.
func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
