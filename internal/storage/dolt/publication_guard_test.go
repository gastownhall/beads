package dolt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// approvedDoltPublishers are the only functions in this package whose bodies
// may hold a DOLT_ADD or DOLT_COMMIT literal, each with the reason it cannot
// reproduce the GH#5752 / NEXUS#92 lost update: DOLT_ADD stages a whole
// table from the session's BEGIN-time root, so a Dolt commit minted inside an
// open SQL transaction writes every row a concurrent writer committed in the
// meantime back to its BEGIN-time value.
var approvedDoltPublishers = map[string]string{
	"DoltStore.stageAndCommit": "the one post-commit publisher: every migrated write reaches it through " +
		"publishCommittedOperation/doltAddAndCommitPostTx after its SQL COMMIT, on a pinned autocommit " +
		"connection with no open transaction; it honors deferral and the staged-set guard",
	"DoltStore.commitWorkingSet": "the explicit commit point (CommitPending, bd dolt commit): it commits " +
		"the already-committed working set on an autocommit connection, outside any SQL transaction",
	"DoltStore.concludeOpenMerge": "concludes a stopped DOLT_MERGE on the merge's own session; it publishes " +
		"the merge result, not a mutation's tables from a transaction snapshot",
	"DoltStore.CommitWithConfig": "the public explicit commit of the whole working set (-Am) on an " +
		"autocommit connection, outside any SQL transaction",
	"DoltStore.CommitAll": "the public explicit commit-all (-Am) on an autocommit connection, outside any " +
		"SQL transaction",
	"DoltStore.commitFilteredStaging": "federation's pull/merge settle commit on its own connection after " +
		"the merge ran; no mutation transaction is open on it",
}

// doltPublicationCall matches a DOLT_ADD or DOLT_COMMIT procedure call in a
// string literal, in any case. A trailing "(" keeps the dolt_commits system
// table and the DOLT_COMMIT_* names out.
var doltPublicationCall = regexp.MustCompile(`(?i)\bdolt_(add|commit)\s*\(`)

// TestNoDoltAddOrCommitInsideOpenTransaction is the structural guard for
// GH#5752: no write may stage or Dolt-commit inside its open SQL
// transaction. The in-transaction helper doltAddAndCommitInTx and the role
// deleter's and sweeper's inline copies are gone, and every surviving
// DOLT_ADD/DOLT_COMMIT literal in this package sits in an approved publisher
// that runs outside any mutation transaction. A new publisher must either
// route through publishCommittedOperation or join approvedDoltPublishers with
// its reason; an exemption whose function no longer publishes must be
// dropped.
//
// Known limit: the guard is literal-based over this package's non-test
// syntax (nested function literals count toward their enclosing function, and
// package-level constants or variables count as a publisher of their own). It
// cannot see SQL assembled indirectly — a procedure name built by
// concatenation or formatting, or a call issued from another package (schema,
// issueops, uow) — so those still need review. The UOW transaction's
// in-transaction publication (internal/storage/uow/doltserver_tx.go) is such
// an out-of-package case; its hazard is documented there.
func TestNoDoltAddOrCommitInsideOpenTransaction(t *testing.T) {
	publishers, err := scanDoltPublishersInDir(".")
	if err != nil {
		t.Fatalf("scan dolt package: %v", err)
	}
	for _, problem := range checkDoltPublishers(publishers, approvedDoltPublishers) {
		t.Error(problem)
	}

	fns, err := journalscan.ParsePackage(".")
	if err != nil {
		t.Fatalf("parse dolt package: %v", err)
	}
	const inTx = "doltAddAndCommitInTx"
	for key, f := range fns {
		if f.Name == inTx {
			t.Errorf("%s is defined again: it stages and Dolt-commits inside an open SQL transaction (GH#5752); publish with publishCommittedOperation after the commit", key)
		}
		if f.CallsAnyOf(map[string]bool{inTx: true}) {
			t.Errorf("%s calls %s: publish with publishCommittedOperation after the SQL commit instead (GH#5752)", key, inTx)
		}
	}
	// Liveness: the guard must still see the publisher it approves first.
	if _, ok := publishers["DoltStore.stageAndCommit"]; !ok {
		t.Fatalf("the scan no longer finds DoltStore.stageAndCommit's DOLT_ADD/DOLT_COMMIT: the detector is broken, so its verdict means nothing")
	}
}

// TestDoltPublicationGuardDetectors proves the guard's two verdicts on
// fixtures: an unapproved publisher (a function literal inside a method, as
// the deleter's inline copy was) is rejected, and a stale exemption — an
// approved name whose function no longer holds a literal — is rejected too.
func TestDoltPublicationGuardDetectors(t *testing.T) {
	const fixture = `package fixture

type DoltStore struct{}

func (s *DoltStore) stageAndCommit() string { return "CALL DOLT_ADD(?)" }

func (s *DoltStore) inlinePublisher() {
	run := func() string { return "call dolt_commit('-m', ?)" }
	_ = run
}

func (s *DoltStore) readsHistory() string { return "SELECT * FROM dolt_commits" }

const pkgLevel = "CALL DOLT_COMMIT('-Am', ?)"
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := scanDoltPublishersInDir(dir)
	if err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	if got, want := publisherKeys(found), []string{"<package scope>", "DoltStore.inlinePublisher", "DoltStore.stageAndCommit"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("fixture publishers = %v, want %v", got, want)
	}

	problems := checkDoltPublishers(found, map[string]string{
		"DoltStore.stageAndCommit": "approved",
		"DoltStore.readsHistory":   "stale: it holds no publication literal",
	})
	joined := strings.Join(problems, "\n")
	for _, want := range []string{
		"unapproved publisher DoltStore.inlinePublisher",
		"unapproved publisher <package scope>",
		"stale exemption DoltStore.readsHistory",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("guard problems do not report %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "DoltStore.stageAndCommit") {
		t.Errorf("guard rejected the approved publisher:\n%s", joined)
	}
}

// scanDoltPublishersInDir maps each function (Recv.Name or Name) in dir's
// non-test Go files whose body holds a DOLT_ADD/DOLT_COMMIT call literal to
// the literals it holds. Literals outside any function are keyed
// "<package scope>".
func scanDoltPublishersInDir(dir string) (map[string][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	found := map[string][]string{}
	record := func(key string, n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && doltPublicationCall.MatchString(lit.Value) {
				found[key] = append(found[key], lit.Value)
			}
			return true
		})
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				record("<package scope>", decl)
				continue
			}
			key := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				key = journalscan.ReceiverTypeName(fn.Recv.List[0].Type) + "." + key
			}
			if fn.Body != nil {
				record(key, fn.Body)
			}
		}
	}
	return found, nil
}

// checkDoltPublishers compares the scanned publishers with the approved set
// and returns one problem per unapproved publisher and per stale exemption.
func checkDoltPublishers(found map[string][]string, approved map[string]string) []string {
	var problems []string
	for _, key := range publisherKeys(found) {
		if _, ok := approved[key]; !ok {
			problems = append(problems, "unapproved publisher "+key+" holds "+strings.Join(found[key], ", ")+
				": a DOLT_ADD/DOLT_COMMIT inside an open SQL transaction reverts concurrently committed rows (GH#5752); "+
				"publish with publishCommittedOperation after the SQL commit, or add it to approvedDoltPublishers with the reason it runs outside any transaction")
		}
	}
	for _, key := range publisherKeys(approved) {
		if _, ok := found[key]; !ok {
			problems = append(problems, "stale exemption "+key+": it no longer holds a DOLT_ADD/DOLT_COMMIT literal; drop it from approvedDoltPublishers")
		}
	}
	return problems
}

func publisherKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
