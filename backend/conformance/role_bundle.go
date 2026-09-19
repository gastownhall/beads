package conformance

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// RoleContractBundle carries one fixture factory per role contract. It is the
// role tier's entry point — the SEMANTIC counterpart to RunAll's portable raw
// surface — and the reason an out-of-tree backend can run the role contracts
// at all: in-tree they are reached through 76 hand-written wiring files across
// the three backends, which no external module can call.
//
// A nil factory means "this backend does not serve that role's accessor". The
// contract SKIPS loudly, naming the accessor, and that same accessor must then
// appear on the allowlist the backend drives RunUnsupportedContract with. A
// backend serving half the roles therefore proves exactly those and is held to
// refusing the rest; there is no capability declaration beyond that, because
// capability is role plus accessor already
// (engdocs/ADDING_AN_ISSUEOPS_ROLE.md).
//
// A non-nil factory must return a usable fixture. Returning nil is a broken
// fixture rather than a narrow backend, so RunRoleContracts FAILS there instead
// of skipping.
//
// # How often each factory is called
//
// Per field, and the difference is load-bearing rather than incidental. Most
// fields are called ONCE and the whole contract runs against the one fixture:
// that is the unit-of-work leg's economy, where every fixture costs a Dolt
// sql-server boot and IssuePrefix is what keeps the cases from claiming each
// other's rows (internal/storage/uow, newUOWRoleFixtureProvider). The two
// issue-operations fields are called once PER CASE, for the reasons on those
// fields.
//
// # Ordering and isolation
//
// THE BUNDLE OWNS INTRA-ROLE ORDER; THE SUPPLIER OWNS INTER-ROLE ISOLATION.
//
// Intra-role: the cases run in the order their contract file declares them,
// sequentially, and never under t.Parallel. Both halves are inherited rather
// than invented. Declaration order is what the unit-of-work leg's single-
// function runners already use, so the one ordering constraint it documents —
// config a case installs early persists into later cases — holds here too.
// Sequencing is required of any backend without per-test copy-on-write
// branches: history is database-global there, so a parallel sibling corrupts
// another case's history-delta arithmetic (newUOWRoleFixtureProvider states it
// for the in-tree provider).
//
// Inter-role: the supplier's job, because no fixture hook can undo what these
// contracts deliberately do to a workspace. No in-tree leg shares one database
// across two role contracts, and a supplier that does is on its own for:
//
//   - RunWorkspaceConfigUnsetDoesNotRefuseTheProtectedKey removes issue_prefix
//     and restores it out of band through SetConfig, so a failure between the
//     two leaves the workspace with no identity for everything that follows.
//   - RunWorkspaceConfigRefusesAnUnparseableCustomStatus and
//     RunWorkspaceConfigProjectsCustomStatuses install BARE custom statuses
//     ("awaiting_review:active") and leave them installed. status.custom is
//     workspace-global, so a claimer contract sharing that workspace inherits
//     claim-eligibility vocabulary it never asked for.
//   - The bootstrapper contract UNSEEDS the workspace identity
//     (seedWorkspaceIdentity with empty strings) as the precondition of most of
//     its cases. Any id-minting contract sharing that workspace breaks it and
//     is broken by it.
//   - RunIssueOperationsCreateRoutesInfraTypesToWisps and
//     RunIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary write
//     BARE global type vocabulary (types.custom, types.infra), last writer wins
//     on the one key.
//   - Issue ids are unique WITHIN a role — each case suffixes the fixture's
//     IssuePrefix — and nothing guarantees that across roles sharing one
//     prefix. Scoped counts (the counter contract's, for instance) are safe
//     against their own suite's neighbors, not against arbitrary foreign rows.
//
// A fixture per role per workspace contains every one of them.
type RoleContractBundle struct {
	// Called ONCE; the whole contract runs against the one fixture.
	BatchApply           func(t *testing.T) *BatchApplyFixture
	BatchCloser          func(t *testing.T) *BatchCloserFixture
	BatchCreator         func(t *testing.T) *BatchCreatorFixture
	BlockingAnnotator    func(t *testing.T) *BlockingAnnotatorFixture
	Bootstrapper         func(t *testing.T) *BootstrapperFixture
	Claimer              func(t *testing.T) *ClaimerFixture
	Commenter            func(t *testing.T) *CommenterFixture
	Counter              func(t *testing.T) *CounterFixture
	CycleDetector        func(t *testing.T) *CycleDetectorFixture
	Deleter              func(t *testing.T) *DeleterFixture
	DependencyEditor     func(t *testing.T) *DependencyEditorFixture
	EdgeReader           func(t *testing.T) *EdgeReaderFixture
	GraphCounter         func(t *testing.T) *GraphCounterFixture
	Importer             func(t *testing.T) *ImporterFixture
	Journal              func(t *testing.T) *JournalFixture
	LifecycleCloseReopen func(t *testing.T) *LifecycleCloseReopenFixture
	LifecycleCreate      func(t *testing.T) *LifecycleCreateFixture
	LifecycleUpdate      func(t *testing.T) *LifecycleUpdateFixture
	Memories             func(t *testing.T) *MemoriesFixture
	MetadataCAS          func(t *testing.T) *MetadataCASFixture
	Querier              func(t *testing.T) *QuerierFixture
	Reader               func(t *testing.T) *ReaderFixture
	ReadyClaimer         func(t *testing.T) *ReadyClaimerFixture
	ReadyCounter         func(t *testing.T) *ReadyCounterFixture
	Relations            func(t *testing.T) *RelationsFixture
	Releaser             func(t *testing.T) *ReleaserFixture
	StatsReporter        func(t *testing.T) *StatsReporterFixture
	Sweeper              func(t *testing.T) *SweeperFixture
	TreeWalker           func(t *testing.T) *TreeWalkerFixture
	VersionReconciler    func(t *testing.T) *VersionReconcilerFixture
	WorkspaceConfig      func(t *testing.T) *WorkspaceConfigFixture

	// IssueOperations is called ONCE PER CASE. No in-tree leg has ever run two
	// of these cases against one workspace — the store legs take a fresh store
	// per case and the unit-of-work leg, alone among its role wirings, takes a
	// fresh provider per case — and the contract writes BARE workspace-global
	// type vocabulary, so shared-fixture safety here is unproven and the bundle
	// does not invent it.
	IssueOperations func(t *testing.T) *IssueOperationsStagingFixture

	// IssueOperationsStaging is called once per case and is separately
	// nil-able, because its content — not its plumbing — ties it to a versioned
	// store: the two cases read committed state with `AS OF 'HEAD'` and drive
	// Commit and the single-statement Exec UNGUARDED. The in-tree
	// unit-of-work leg supplies the same fixture type for IssueOperations and
	// still cannot run these two. Nil skips both, loudly.
	IssueOperationsStaging func(t *testing.T) *IssueOperationsStagingFixture
}

// RunRoleContracts runs every role contract in this package against the
// fixtures b supplies, each under a subtest named for its role. Contracts whose
// factory is nil skip loudly; the rest run every one of their cases.
//
// It is the whole role tier. RunAll (the portable raw surface) and
// RunUnsupportedContract (the capability refusals) are the other two entry
// points a backend drives, and none of the three subsumes another.
func RunRoleContracts(t *testing.T, ctx context.Context, b RoleContractBundle) {
	t.Helper()
	for _, contract := range roleContractCases {
		t.Run(contract.role, func(t *testing.T) {
			contract.run(t, ctx, b)
		})
	}
}

// fixturePolicy is how often RunRoleContracts calls a bundle field's factory.
type fixturePolicy int

const (
	// oncePerRole builds one fixture and runs the contract's whole case list
	// against it. The contracts are written for it: each case namespaces what
	// it seeds with the fixture's IssuePrefix.
	oncePerRole fixturePolicy = iota
	// oncePerCase builds a fresh fixture for every case, for the contracts no
	// backend has ever shown to be safe on a shared workspace.
	oncePerCase
)

func (p fixturePolicy) String() string {
	if p == oncePerCase {
		return "once per case"
	}
	return "once per role"
}

// roleContract is one row of roleContractCases: a contract file's cases, the
// bundle field that supplies their fixture, and how often to call it.
type roleContract struct {
	// role is both the subtest name and the RoleContractBundle field name.
	// TestRoleContractCasesMatchTheBundleFields holds those two together.
	role string
	// accessors names the storage.Storage accessors a backend serves to supply
	// this fixture, and that it must otherwise refuse on its unsupported
	// allowlist. It is what the skip message points a partial backend at.
	accessors string
	policy    fixturePolicy
	// caseNames are the contract's Run functions resolved back to their source
	// names, in dispatch order. Resolved from the function values rather than
	// written out beside them, so a table row cannot name one case and run
	// another; TestRoleContractCasesCoverEveryContractCase compares these
	// against the package source.
	caseNames []string
	// supplied reports whether b carries this contract's factory.
	supplied func(b RoleContractBundle) bool
	run      func(t *testing.T, ctx context.Context, b RoleContractBundle)
}

// roleCases builds one roleContract. field picks this contract's factory out of
// a bundle, and cases are its Run functions in the order the contract file
// declares them.
func roleCases[F any](
	role, accessors string,
	policy fixturePolicy,
	field func(b RoleContractBundle) func(t *testing.T) *F,
	cases ...func(t *testing.T, ctx context.Context, fixture F),
) roleContract {
	names := make([]string, len(cases))
	for i, run := range cases {
		names[i] = runFuncName(run)
	}
	return roleContract{
		role:      role,
		accessors: accessors,
		policy:    policy,
		caseNames: names,
		supplied:  func(b RoleContractBundle) bool { return field(b) != nil },
		run: func(t *testing.T, ctx context.Context, b RoleContractBundle) {
			factory := field(b)
			if factory == nil {
				t.Skipf("RoleContractBundle.%s is nil: this backend does not serve %s, "+
					"so none of the %d cases in this contract ran. Put %s on the unsupported "+
					"allowlist RunUnsupportedContract is driven with, so the refusal is proven "+
					"rather than assumed.", role, accessors, len(cases), accessors)
				return
			}
			var shared *F
			if policy == oncePerRole {
				shared = buildFixture(t, role, factory)
			}
			// Sequential, in declaration order, and never t.Parallel. See the
			// ordering note on RoleContractBundle.
			for i, run := range cases {
				t.Run(subtestName(role, names[i]), func(t *testing.T) {
					fixture := shared
					if policy == oncePerCase {
						fixture = buildFixture(t, role, factory)
					}
					run(t, ctx, *fixture)
				})
			}
		},
	}
}

func buildFixture[F any](t *testing.T, role string, factory func(t *testing.T) *F) *F {
	t.Helper()
	fixture := factory(t)
	if fixture == nil {
		t.Fatalf("RoleContractBundle.%s returned a nil fixture. A supplied factory must return "+
			"a usable one; leave the field nil to declare the role unsupported.", role)
	}
	return fixture
}

// runFuncName resolves a contract case's Run function value back to the name it
// was declared under.
func runFuncName(run any) string {
	fn := runtime.FuncForPC(reflect.ValueOf(run).Pointer())
	if fn == nil {
		return ""
	}
	name := fn.Name()
	return name[strings.LastIndexByte(name, '.')+1:]
}

// subtestName strips "Run" and, where the name carries it, the role, so
// RunClaimerRefusesAWispIDAsNotFound addresses as
// Claimer/RefusesAWispIDAsNotFound — the same name the unit-of-work leg's
// subtests use. A case whose name does not carry its role keeps the whole of
// it: Bootstrapper/InitVerifierWritesNothing names the half it belongs to,
// which is the useful part.
func subtestName(role, runName string) string {
	return strings.TrimPrefix(strings.TrimPrefix(runName, "Run"), role)
}
