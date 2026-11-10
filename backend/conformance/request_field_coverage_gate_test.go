package conformance

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/journalops"
)

// The reasons the seeded waivers below share. Each names what a case would
// have to do to delete the entries that cite it.
const (
	waiveCountFilter = "Count shares List's filter vocabulary and RunCounter* narrows by only part of it; " +
		"a case counting under this filter would pin that Count and List agree on it"
	waiveListFilter  = "no RunReaderList* or RunQuerier* case narrows a list page by this member yet"
	waiveReadyFilter = "no RunReaderReady*, RunReadyCounter* or RunReadyClaimer* case narrows the ready queue " +
		"by this member yet"
	waiveImport = "RunImporter* imports issue rows under the default stale refusal only; stale admission, " +
		"memory records and prefix sync have no case yet"
	waiveBatchItem = "RunBatchApply* cases set this member's single-write twin (UpdateRequest or CloseRequest) " +
		"but no batch item carries it yet"
	waiveCreateEdge = "create-time edges are asserted by target, type and direction only; no RunLifecycleCreate* " +
		"or RunBatchCreator* case writes this member yet"
)

// uncoveredRequestFields waives the request fields this package knowingly has
// no contract case for, keyed "<qualifier>.<Struct>.<Field>". It is
// SHRINK-ONLY exactly as uncoveredRoleMethods is: the gate fails on an entry
// that names no real field and on an entry a case has since covered, so the
// change that writes the first setter deletes the waiver in the same diff.
//
// It was seeded from the census the day the gate landed; every entry here was
// uncovered then. A NEW entry is possible and deliberately visible: it must
// name a real field and carry a reason a reviewer reads.
var uncoveredRequestFields = map[string]string{
	"issueops.CloseItem.Session":                  waiveBatchItem,
	"issueops.CountRequest.Assignee":              waiveCountFilter,
	"issueops.CountRequest.ClosedAfter":           waiveCountFilter,
	"issueops.CountRequest.ClosedBefore":          waiveCountFilter,
	"issueops.CountRequest.CreatedAfter":          waiveCountFilter,
	"issueops.CountRequest.CreatedBefore":         waiveCountFilter,
	"issueops.CountRequest.DescContains":          waiveCountFilter,
	"issueops.CountRequest.EmptyDesc":             waiveCountFilter,
	"issueops.CountRequest.HasMetadataKey":        waiveCountFilter,
	"issueops.CountRequest.IncludeEphemeral":      waiveCountFilter,
	"issueops.CountRequest.LabelsAny":             waiveCountFilter,
	"issueops.CountRequest.MetadataFields":        waiveCountFilter,
	"issueops.CountRequest.NoAssignee":            waiveCountFilter,
	"issueops.CountRequest.NoLabels":              waiveCountFilter,
	"issueops.CountRequest.NotesContains":         waiveCountFilter,
	"issueops.CountRequest.Priority":              waiveCountFilter,
	"issueops.CountRequest.PriorityMax":           waiveCountFilter,
	"issueops.CountRequest.PriorityMin":           waiveCountFilter,
	"issueops.CountRequest.TitleContains":         waiveCountFilter,
	"issueops.CountRequest.TitleSearch":           waiveCountFilter,
	"issueops.CountRequest.UpdatedAfter":          waiveCountFilter,
	"issueops.CountRequest.UpdatedBefore":         waiveCountFilter,
	"issueops.CreateDependency.Metadata":          waiveCreateEdge,
	"issueops.CreateDependency.ThreadID":          waiveCreateEdge,
	"issueops.CreateRequest.IDPrefix":             "the local front door's config.yaml prefix (GH#4957); no fixture models a prefix that differs from the database's yet",
	"issueops.ImportBatchRequest.AllowStale":      waiveImport,
	"issueops.ImportBatchRequest.Memories":        waiveImport,
	"issueops.ImportBatchRequest.SyncIssuePrefix": waiveImport,
	"issueops.ImportMemory.Key":                   waiveImport,
	"issueops.ImportMemory.Value":                 waiveImport,
	"issueops.ListRequest.HasMetadataKey":         waiveListFilter,
	"issueops.ListRequest.IncludeComments":        "no RunReaderList* case asks a list page for its comment rows yet; RunReaderGet* covers GetRequest's",
	"issueops.ListRequest.TitleSearch":            waiveListFilter,
	"issueops.ReadyRequest.Assignee":              waiveReadyFilter,
	"issueops.ReadyRequest.LabelPattern":          waiveReadyFilter,
	"issueops.ReadyRequest.LabelRegex":            waiveReadyFilter,
	"issueops.ReadyRequest.MolType":               waiveReadyFilter,
	"issueops.ReadyRequest.Unassigned":            waiveReadyFilter,
	"issueops.UpdateItem.ForceAssigneeTransfer":   waiveBatchItem,
	"issueops.UpdateItem.ForceNotesOverwrite":     waiveBatchItem,
	"issueops.WaitsFor.Gate":                      waiveCreateEdge,
}

// TestEveryRoleRequestFieldHasAContractCase is the field-level sibling of
// TestEveryRoleMethodHasAContractCase: every exported field of every request
// struct a facade role takes must be SET by a contract case here, or be waived
// above with a reason.
//
// The method gate cannot see a field. A request struct that grows a field —
// a guard hook, a refusal flag, a timestamp mode — keeps every method
// covered, and a leg, a decorator or an out-of-tree backend that ignores the
// new field fails nothing. The only field census before this one was the
// wire's (internal/httpclient/encode's TestEncoderTableClassifiesEveryRequestField),
// which asks whether the HTTP client sends a field, not whether any backend
// is held to honoring it.
//
// Both halves are derived. The census walks the facade's role signatures, so
// a field added anywhere a request reaches — IssuePatch, an ApplyItem's
// CreateItem — reaches this test on its own; the setters are found by type,
// so UpdateRequest.ExpectedVersion is never credited for a case that sets
// CloseRequest's.
//
// Watched to fail before landing: with every setter of
// UpdateRequest.ExpectedVersion stripped from a copy of this package it names
// exactly that field; with an exported field added to issueops.DeleteRequest
// it names the new field; and with the Write branch's issueops diff applied it
// names its seven new fields — the six guard and mode fields, plus
// RowCheck.Run, which a *RowCheck reaches.
func TestEveryRoleRequestFieldHasAContractCase(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	sources := newFacadeSources(root)
	census, err := requestFieldCensus(sources)
	if err != nil {
		t.Fatalf("censusing the role request fields: %v", err)
	}
	if len(census) == 0 {
		t.Fatal("the request-field census is empty; the gate would pass vacuously")
	}
	covered, err := scanRequestFieldSetters(sources, modulePath+"/backend/conformance", filepath.Join(root, "backend", "conformance"))
	if err != nil {
		t.Fatalf("scanning contract cases: %v", err)
	}
	if len(covered) == 0 {
		t.Fatal("the setter scan credited no field at all; the type check found nothing it could read")
	}
	for _, problem := range auditRequestFieldCoverage(census, covered, uncoveredRequestFields) {
		t.Error(problem)
	}
}

// rolesNoAccessorHandsOut names, by type, the facade roles no storage accessor
// hands out — which reflection can reach no other way. It must list exactly
// those: reflectedRoleTypes refuses a facade role missing from both this table
// and the accessors, and an entry an accessor already covers.
var rolesNoAccessorHandsOut = map[string]reflect.Type{
	// A backend implements it for `bd import`; no accessor returns it.
	"issueops.Importer": reflect.TypeOf((*issueops.Importer)(nil)).Elem(),
	// A caller type-asserts for it; see facadePackages.
	"journalops.Journal": reflect.TypeOf((*journalops.Journal)(nil)).Elem(),
}

// TestRequestFieldCensusAgreesWithReflection checks the source census against
// the compiler, struct by struct and field by field, for EVERY facade role.
// The census is type-checked from source with only the facade's imports
// resolved, so this is what keeps a type the checker could not read from
// quietly shrinking what the gate demands.
func TestRequestFieldCensusAgreesWithReflection(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	census, err := requestFieldCensus(newFacadeSources(root))
	if err != nil {
		t.Fatalf("censusing the role request fields: %v", err)
	}
	facade, err := roleFacade()
	if err != nil {
		t.Fatalf("reading the role facade: %v", err)
	}
	accessors, err := accessorRoleTypes()
	if err != nil {
		t.Fatalf("censusing the storage accessors: %v", err)
	}
	roles, err := reflectedRoleTypes(facade, accessors, rolesNoAccessorHandsOut)
	if err != nil {
		t.Fatal(err)
	}
	reflected, err := reflectRequestFieldCensus(roles, facadePackages)
	if err != nil {
		t.Fatalf("reflecting the role request fields: %v", err)
	}
	if len(reflected) == 0 {
		t.Fatal("reflection reached no request struct; the cross-check would pass vacuously")
	}
	for _, name := range sortedKeys(reflected) {
		parsed, ok := census[name]
		if !ok {
			t.Errorf("%s is a role request struct the compiler sees but the source census never reached", name)
			continue
		}
		if strings.Join(parsed, ",") != strings.Join(reflected[name], ",") {
			t.Errorf("%s fields: source census has %v, reflection has %v", name, parsed, reflected[name])
		}
	}
	for _, name := range sortedKeys(census) {
		if _, ok := reflected[name]; !ok {
			t.Errorf("%s is in the source census but no role the compiler sees takes it", name)
		}
	}
}

// reflectedRoleTypes is every facade role as a compiled type: the accessors'
// roles plus the named ones, which together must be the facade exactly.
func reflectedRoleTypes(facade map[string][]string, accessors, named map[string]reflect.Type) (map[string]reflect.Type, error) {
	roles := map[string]reflect.Type{}
	for name, role := range accessors {
		roles[name] = role
	}
	for _, name := range sortedKeys(named) {
		if _, ok := accessors[name]; ok {
			return nil, fmt.Errorf("rolesNoAccessorHandsOut lists %s, which a storage accessor hands out: delete the entry", name)
		}
		roles[name] = named[name]
	}
	for _, name := range sortedKeys(facade) {
		if _, ok := roles[name]; !ok {
			return nil, fmt.Errorf("%s is a facade role reflection cannot reach: no storage accessor hands it out "+
				"and rolesNoAccessorHandsOut does not name it", name)
		}
	}
	for _, name := range sortedKeys(roles) {
		if _, ok := facade[name]; !ok {
			return nil, fmt.Errorf("reflection reaches %s, which the source census of facade roles never saw", name)
		}
	}
	return roles, nil
}

// auditRequestFieldCoverage is auditRoleCoverage one level down: every way the
// census, the setters and the waivers disagree. Separated from the scanning so
// fabricated inputs can prove the gate fails when it should.
func auditRequestFieldCoverage(census map[string][]string, covered map[requestField][]string, waived map[string]string) []string {
	var problems []string
	known := map[string]bool{}
	for _, structName := range sortedKeys(census) {
		for _, fieldName := range census[structName] {
			target := requestField{Struct: structName, Field: fieldName}
			known[target.String()] = true
			_, isWaived := waived[target.String()]
			switch {
			case len(covered[target]) > 0 && isWaived:
				problems = append(problems, fmt.Sprintf(
					"%s is waived as uncovered but %s sets it: delete its entry from uncoveredRequestFields",
					target, strings.Join(covered[target], ", ")))
			case len(covered[target]) == 0 && !isWaived:
				problems = append(problems, fmt.Sprintf(
					"%s has no contract case: no conformance entrypoint sets it. Write one, or waive it in uncoveredRequestFields with a reason",
					target))
			}
		}
	}
	for _, entry := range sortedKeys(waived) {
		if !known[entry] {
			problems = append(problems, fmt.Sprintf(
				"uncoveredRequestFields waives %q, which names no field of any role request", entry))
			continue
		}
		if strings.TrimSpace(waived[entry]) == "" {
			problems = append(problems, fmt.Sprintf("uncoveredRequestFields waives %s with no reason", entry))
		}
	}
	return problems
}

// The gate's own tests, each fabricating the drift it exists to catch.

func TestAuditRequestFieldCoverageReportsNothingWhenEveryFieldIsSet(t *testing.T) {
	census := map[string][]string{"issueops.CloseRequest": {"IssueID", "Reason"}}
	covered := map[requestField][]string{
		{Struct: "issueops.CloseRequest", Field: "IssueID"}: {"RunLifecycleCloseCloses"},
		{Struct: "issueops.CloseRequest", Field: "Reason"}:  {"RunLifecycleCloseKeepsTheFirstReason"},
	}
	if problems := auditRequestFieldCoverage(census, covered, nil); len(problems) != 0 {
		t.Errorf("fully covered census reported %v, want no problems", problems)
	}
}

func TestAuditRequestFieldCoverageCatchesAFieldNoCaseSets(t *testing.T) {
	census := map[string][]string{"issueops.DeleteRequest": {"IDs", "InTx"}}
	covered := map[requestField][]string{
		{Struct: "issueops.DeleteRequest", Field: "IDs"}: {"RunDeleterDeletes"},
	}
	problems := auditRequestFieldCoverage(census, covered, nil)
	if len(problems) != 1 || !strings.Contains(problems[0], "issueops.DeleteRequest.InTx has no contract case") {
		t.Errorf("uncovered field reported %v, want one problem naming issueops.DeleteRequest.InTx", problems)
	}
}

func TestAuditRequestFieldCoverageAcceptsAWaivedField(t *testing.T) {
	census := map[string][]string{"issueops.ImportMemory": {"Key"}}
	waived := map[string]string{"issueops.ImportMemory.Key": "no case imports memories"}
	if problems := auditRequestFieldCoverage(census, nil, waived); len(problems) != 0 {
		t.Errorf("waived field reported %v, want no problems", problems)
	}
}

func TestAuditRequestFieldCoverageCatchesAWaiverThatIsNoLongerNeeded(t *testing.T) {
	census := map[string][]string{"issueops.UpdateRequest": {"ExpectedVersion"}}
	covered := map[requestField][]string{
		{Struct: "issueops.UpdateRequest", Field: "ExpectedVersion"}: {"RunLifecycleUpdateRefusesAStaleVersion"},
	}
	waived := map[string]string{"issueops.UpdateRequest.ExpectedVersion": "stale"}
	problems := auditRequestFieldCoverage(census, covered, waived)
	if len(problems) != 1 || !strings.Contains(problems[0], "delete its entry from uncoveredRequestFields") {
		t.Errorf("stale waiver reported %v, want one problem telling the author to delete it", problems)
	}
}

func TestAuditRequestFieldCoverageCatchesAWaiverNamingNoSuchField(t *testing.T) {
	census := map[string][]string{"issueops.CloseRequest": {"IssueID"}}
	covered := map[requestField][]string{
		{Struct: "issueops.CloseRequest", Field: "IssueID"}: {"RunLifecycleCloseCloses"},
	}
	waived := map[string]string{"issueops.CloseRequest.Renamed": "stale after a rename"}
	problems := auditRequestFieldCoverage(census, covered, waived)
	if len(problems) != 1 || !strings.Contains(problems[0], "names no field of any role request") {
		t.Errorf("waiver for a missing field reported %v, want one problem naming it", problems)
	}
}

func TestAuditRequestFieldCoverageCatchesAWaiverWithNoReason(t *testing.T) {
	census := map[string][]string{"issueops.ImportMemory": {"Key"}}
	waived := map[string]string{"issueops.ImportMemory.Key": "  "}
	problems := auditRequestFieldCoverage(census, nil, waived)
	if len(problems) != 1 || !strings.Contains(problems[0], "with no reason") {
		t.Errorf("reasonless waiver reported %v, want one problem naming it", problems)
	}
}

func TestReflectedRoleTypesMustBeTheFacadeExactly(t *testing.T) {
	reader := reflect.TypeOf((*issueops.Reader)(nil)).Elem()
	importer := reflect.TypeOf((*issueops.Importer)(nil)).Elem()
	facade := map[string][]string{"issueops.Reader": {"Get"}, "issueops.Importer": {"ImportBatch"}}
	accessors := map[string]reflect.Type{"issueops.Reader": reader}

	if _, err := reflectedRoleTypes(facade, accessors, map[string]reflect.Type{"issueops.Importer": importer}); err != nil {
		t.Errorf("an exact table was refused: %v", err)
	}
	if _, err := reflectedRoleTypes(facade, accessors, nil); err == nil || !strings.Contains(err.Error(), "issueops.Importer") {
		t.Errorf("a role neither an accessor nor the table reaches: err = %v, want one naming issueops.Importer", err)
	}
	redundant := map[string]reflect.Type{"issueops.Importer": importer, "issueops.Reader": reader}
	if _, err := reflectedRoleTypes(facade, accessors, redundant); err == nil || !strings.Contains(err.Error(), "delete the entry") {
		t.Errorf("a table entry an accessor already covers: err = %v, want a request to delete it", err)
	}
}
