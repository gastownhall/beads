package conformance

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// uncoveredRoleMethods waives the role methods this package knowingly has no
// contract case for. It is SHRINK-ONLY in the sense that matters: the gate
// fails on an entry that names no real method and on an entry the package has
// since covered, so the PR that writes the first case for a waived method
// deletes its entry in the same change.
//
// Adding an entry stays possible, and deliberately so — a role that genuinely
// cannot be reached from the contract tier has to be recordable somewhere. What
// the gate takes away is doing it quietly: an addition must name a real method,
// carry a reason, and land in a diff a reviewer reads. It converts silence into
// an argument someone has to make.
var uncoveredRoleMethods = map[string]string{}

// TestEveryRoleMethodHasAContractCase is the exhaustiveness gate on the role
// tier: every method of every interface the public facade declares must be
// called by a contract case here, or be waived above with a reason.
//
// It exists because coverage was aspirational. Nothing failed when a role
// method landed with no contract behind it — issueops.Importer.ImportBatch has
// had none since it was written and nothing noticed. Both halves of the
// comparison are derived, so a twenty-fifth role interface, a thirty-seventh
// method, or a contract case that stops calling what it claims to all reach
// this test on their own.
func TestEveryRoleMethodHasAContractCase(t *testing.T) {
	facade, err := roleFacade()
	if err != nil {
		t.Fatalf("reading the role facade: %v", err)
	}
	if len(facade) == 0 {
		t.Fatal("the role facade census is empty; the gate would pass vacuously")
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	covered, err := scanRoleCalls(filepath.Join(root, "backend", "conformance"))
	if err != nil {
		t.Fatalf("scanning contract cases: %v", err)
	}
	for _, problem := range auditRoleCoverage(facade, covered, uncoveredRoleMethods) {
		t.Error(problem)
	}
}

// TestRoleFacadeCensusAgreesWithReflection checks the source census against the
// compiler for every role a storage accessor hands out. The census is parsed
// from source because reflection cannot enumerate a package's types; this is
// what keeps that parse honest, so a method set the parser reads wrong fails
// here rather than quietly excusing a contract.
func TestRoleFacadeCensusAgreesWithReflection(t *testing.T) {
	facade, err := roleFacade()
	if err != nil {
		t.Fatalf("reading the role facade: %v", err)
	}
	accessors, err := reflectRoleAccessors()
	if err != nil {
		t.Fatalf("censusing the storage accessors: %v", err)
	}
	if len(accessors) == 0 {
		t.Fatal("no storage accessor returns a facade role; the cross-check would pass vacuously")
	}
	for role, reflected := range accessors {
		parsed, ok := facade[role]
		if !ok {
			t.Errorf("%s is handed out by a storage accessor but the source census never saw it", role)
			continue
		}
		if strings.Join(parsed, ",") != strings.Join(reflected, ",") {
			t.Errorf("%s method set: source census has %v, reflection has %v", role, parsed, reflected)
		}
	}
}

// auditRoleCoverage reports every way the role facade, the contract cases that
// call it, and the waiver list disagree. It is separated from the scanning so
// it can be exercised against fabricated inputs, which is the only way to prove
// the gate fails when it should.
func auditRoleCoverage(facade map[string][]string, covered map[roleMethod][]string, waived map[string]string) []string {
	var problems []string
	known := map[string]bool{}
	for _, role := range sortedKeys(facade) {
		for _, method := range facade[role] {
			target := roleMethod{Role: role, Method: method}
			known[target.String()] = true
			_, isWaived := waived[target.String()]
			switch {
			case len(covered[target]) > 0 && isWaived:
				problems = append(problems, fmt.Sprintf(
					"%s is waived as uncovered but %s calls it: delete its entry from uncoveredRoleMethods",
					target, strings.Join(covered[target], ", ")))
			case len(covered[target]) == 0 && !isWaived:
				problems = append(problems, fmt.Sprintf(
					"%s has no contract case: no conformance entrypoint calls it. Write one, or waive it in uncoveredRoleMethods with a reason",
					target))
			}
		}
	}
	for _, entry := range sortedKeys(waived) {
		if !known[entry] {
			problems = append(problems, fmt.Sprintf(
				"uncoveredRoleMethods waives %q, which names no method of any facade role", entry))
			continue
		}
		if strings.TrimSpace(waived[entry]) == "" {
			problems = append(problems, fmt.Sprintf("uncoveredRoleMethods waives %s with no reason", entry))
		}
	}
	return problems
}

// sortedKeys returns a map's keys in a deterministic order, so the gate reports
// the same problems in the same order every run.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// These are the gate's own tests. A gate nobody has watched fail is a gate
// nobody knows works, so each case fabricates the drift it is meant to catch
// and asserts the report names it.

func TestAuditRoleCoverageReportsNothingWhenEveryMethodHasACase(t *testing.T) {
	facade := map[string][]string{"issueops.Reader": {"Get", "Ready"}}
	covered := map[roleMethod][]string{
		{Role: "issueops.Reader", Method: "Get"}:   {"RunReaderGetHydratesLabels"},
		{Role: "issueops.Reader", Method: "Ready"}: {"RunReaderReadyExcludesBlocked"},
	}
	if problems := auditRoleCoverage(facade, covered, nil); len(problems) != 0 {
		t.Errorf("fully covered facade reported %v, want no problems", problems)
	}
}

func TestAuditRoleCoverageCatchesAMethodNoCaseCalls(t *testing.T) {
	facade := map[string][]string{"issueops.Reader": {"Get", "Ready"}}
	covered := map[roleMethod][]string{
		{Role: "issueops.Reader", Method: "Get"}: {"RunReaderGetHydratesLabels"},
	}
	problems := auditRoleCoverage(facade, covered, nil)
	if len(problems) != 1 || !strings.Contains(problems[0], "issueops.Reader.Ready has no contract case") {
		t.Errorf("uncovered method reported %v, want one problem naming issueops.Reader.Ready", problems)
	}
}

func TestAuditRoleCoverageCatchesAWholeUncoveredRole(t *testing.T) {
	facade := map[string][]string{
		"issueops.Reader":   {"Get"},
		"issueops.Importer": {"ImportBatch"},
	}
	covered := map[roleMethod][]string{
		{Role: "issueops.Reader", Method: "Get"}: {"RunReaderGetHydratesLabels"},
	}
	problems := auditRoleCoverage(facade, covered, nil)
	if len(problems) != 1 || !strings.Contains(problems[0], "issueops.Importer.ImportBatch") {
		t.Errorf("uncovered role reported %v, want one problem naming issueops.Importer.ImportBatch", problems)
	}
}

func TestAuditRoleCoverageAcceptsAWaivedMethod(t *testing.T) {
	facade := map[string][]string{"issueops.Importer": {"ImportBatch"}}
	waived := map[string]string{"issueops.Importer.ImportBatch": "no accessor hands this role out"}
	if problems := auditRoleCoverage(facade, nil, waived); len(problems) != 0 {
		t.Errorf("waived method reported %v, want no problems", problems)
	}
}

func TestAuditRoleCoverageCatchesAWaiverThatIsNoLongerNeeded(t *testing.T) {
	facade := map[string][]string{"issueops.Importer": {"ImportBatch"}}
	covered := map[roleMethod][]string{
		{Role: "issueops.Importer", Method: "ImportBatch"}: {"RunImporterLandsOneBatch"},
	}
	waived := map[string]string{"issueops.Importer.ImportBatch": "no accessor hands this role out"}
	problems := auditRoleCoverage(facade, covered, waived)
	if len(problems) != 1 || !strings.Contains(problems[0], "delete its entry from uncoveredRoleMethods") {
		t.Errorf("stale waiver reported %v, want one problem telling the author to delete it", problems)
	}
}

func TestAuditRoleCoverageCatchesAWaiverNamingNoSuchMethod(t *testing.T) {
	facade := map[string][]string{"issueops.Reader": {"Get"}}
	covered := map[roleMethod][]string{
		{Role: "issueops.Reader", Method: "Get"}: {"RunReaderGetHydratesLabels"},
	}
	waived := map[string]string{"issueops.Reader.Renamed": "stale after a rename"}
	problems := auditRoleCoverage(facade, covered, waived)
	if len(problems) != 1 || !strings.Contains(problems[0], "names no method of any facade role") {
		t.Errorf("waiver for a missing method reported %v, want one problem naming it", problems)
	}
}

func TestAuditRoleCoverageCatchesAWaiverWithNoReason(t *testing.T) {
	facade := map[string][]string{"issueops.Importer": {"ImportBatch"}}
	waived := map[string]string{"issueops.Importer.ImportBatch": "  "}
	problems := auditRoleCoverage(facade, nil, waived)
	if len(problems) != 1 || !strings.Contains(problems[0], "with no reason") {
		t.Errorf("reasonless waiver reported %v, want one problem naming it", problems)
	}
}
