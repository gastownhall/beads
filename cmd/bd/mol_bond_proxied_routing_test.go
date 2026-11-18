//go:build cgo

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// The tests below pin `bd mol bond` on a proxied-server launcher whose operands
// live in prefix-routed rigs (bd-tqdsvo). The launcher's own database is a fake
// unit-of-work provider that counts opens and commits; the rigs are real Dolt
// databases reached through routes.jsonl, as on the direct route
// (TestMolBondPrefixRoutedWriteThrough).
//
// NOTE: These tests use os.Chdir and global state; they cannot run in parallel.

// molBondProxiedFormula is the formula every fixture can bond: a root and one
// step, so a spawn writes two rows before it attaches.
const molBondProxiedFormula = "mbp-two-step"

type molBondProxiedFixture struct {
	t             *testing.T
	ctx           context.Context
	tmpDir        string
	rigBeadsDir   string
	routesPath    string
	routes        []byte
	served        *molBondServedProvider
	reopens       int
	reopenWrapper func(*RoutedResult) *RoutedResult
}

// newMolBondProxiedFixture builds a town whose routes.jsonl sends gt- to a rig
// holding gt-mol1 and gt-mol2 and zz- to another rig holding zz-other, and
// installs a served launcher database holding hq-served.
func newMolBondProxiedFixture(t *testing.T) *molBondProxiedFixture {
	t.Helper()
	initConfigForTest(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	otherBeadsDir := filepath.Join(tmpDir, "other-rig", ".beads")
	for _, dir := range []string{townBeadsDir, rigBeadsDir, otherBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigBeadsDir, "dolt"), "gt")
	seedIssue(t, ctx, rigStore, "gt-mol1")
	seedIssue(t, ctx, rigStore, "gt-mol2")
	// Release the rig store before routing reopens it.
	rigStore.Close()
	otherStore := newTestStoreIsolatedDB(t, filepath.Join(otherBeadsDir, "dolt"), "zz")
	seedIssue(t, ctx, otherStore, "zz-other")
	otherStore.Close()

	f := &molBondProxiedFixture{
		t:           t,
		ctx:         ctx,
		tmpDir:      tmpDir,
		rigBeadsDir: rigBeadsDir,
		routesPath:  filepath.Join(townBeadsDir, "routes.jsonl"),
		routes:      []byte("{\"prefix\":\"gt-\",\"path\":\"rig\"}\n{\"prefix\":\"zz-\",\"path\":\"other-rig\"}\n"),
	}
	f.writeRoutes()

	data, err := json.Marshal(&formula.Formula{
		Formula: molBondProxiedFormula,
		Version: 1,
		Type:    formula.TypeWorkflow,
		Steps:   []*formula.Step{{ID: "step1", Title: "Routed step", Type: "task"}},
	})
	if err != nil {
		t.Fatalf("marshal formula: %v", err)
	}
	f.writeFormula(molBondProxiedFormula, data)
	t.Setenv("GT_ROOT", tmpDir)

	oldDBPath, oldProvider, oldJSON, oldReopen := dbPath, uowProvider, jsonOutput, molBondReopenRoutedHome
	t.Cleanup(func() {
		dbPath, uowProvider, jsonOutput, molBondReopenRoutedHome = oldDBPath, oldProvider, oldJSON, oldReopen
	})
	dbPath = filepath.Join(townBeadsDir, "dolt")
	jsonOutput = false
	f.served = newMolBondServedProvider(&types.Issue{ID: "hq-served", Title: "Served molecule", Status: types.StatusOpen, IssueType: types.TypeTask})
	uowProvider = f.served
	molBondReopenRoutedHome = func(ctx context.Context, id string) (*RoutedResult, error) {
		f.reopens++
		rr, err := oldReopen(ctx, id)
		if err != nil || f.reopenWrapper == nil {
			return rr, err
		}
		return f.reopenWrapper(rr), nil
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	return f
}

func (f *molBondProxiedFixture) writeRoutes() {
	f.t.Helper()
	if err := os.WriteFile(f.routesPath, f.routes, 0o644); err != nil {
		f.t.Fatalf("write routes.jsonl: %v", err)
	}
}

func (f *molBondProxiedFixture) writeFormula(name string, data []byte) {
	f.t.Helper()
	dir := filepath.Join(f.tmpDir, ".beads", "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatalf("mkdir formulas: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+formula.FormulaExt), data, 0o644); err != nil {
		f.t.Fatalf("write formula %s: %v", name, err)
	}
}

// setRigMode rewrites the gt- rig's dolt_mode, which is all a routed open
// reads to refuse a proxied-server rig, and returns the mode it replaced.
func (f *molBondProxiedFixture) setRigMode(mode string) (previous string) {
	f.t.Helper()
	cfg, err := configfile.Load(f.rigBeadsDir)
	if err != nil || cfg == nil {
		f.t.Fatalf("load rig metadata.json: cfg=%v err=%v", cfg, err)
	}
	previous, cfg.DoltMode = cfg.DoltMode, mode
	if err := cfg.Save(f.rigBeadsDir); err != nil {
		f.t.Fatalf("save rig metadata.json: %v", err)
	}
	return previous
}

// bond runs the proxied bond and returns its error, stdout and stderr. A panic
// (a fake asked for something it does not serve) is returned as an error.
func (f *molBondProxiedFixture) bond(in molBondInput) (runErr error, stdout, stderr string) {
	f.t.Helper()
	if in.bondType == "" {
		in.bondType = types.BondTypeSequential
	}
	stdout, stderr = captureMolBondOutput(f.t, func() {
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("panic: %v", r)
			}
		}()
		runErr = runMolBondProxiedServer(f.ctx, in)
	})
	return runErr, stdout, stderr
}

// captureMolBondOutput captures stdout and stderr together under one hold of
// stdioMutex (captureStdout and captureStderr each take it, so they cannot
// nest).
func captureMolBondOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	pipe := func(target **os.File) func() string {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		var buf bytes.Buffer
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(&buf, r)
			close(done)
		}()
		old := *target
		*target = w
		return func() string {
			_ = w.Close()
			*target = old
			<-done
			_ = r.Close()
			return buf.String()
		}
	}
	restoreOut := pipe(&os.Stdout)
	restoreErr := pipe(&os.Stderr)
	func() {
		defer func() {
			stderr = restoreErr()
			stdout = restoreOut()
		}()
		fn()
	}()
	return stdout, stderr
}

// routedRead opens the rig that routes id read-only, as a fresh process would.
func (f *molBondProxiedFixture) routedRead(id string) *RoutedResult {
	f.t.Helper()
	rr, err := resolveViaPrefixRoutingWithAccess(f.ctx, id, false, false)
	if err != nil {
		f.t.Fatalf("routed read of %s: %v", id, err)
	}
	f.t.Cleanup(rr.Close)
	return rr
}

// rigIssueIDs lists every issue and wisp in the rig that routes id.
func (f *molBondProxiedFixture) rigIssueIDs(id string) []string {
	f.t.Helper()
	rr := f.routedRead(id)
	defer rr.Close()
	issues, err := rr.Store.SearchIssues(f.ctx, "", types.IssueFilter{})
	if err != nil {
		f.t.Fatalf("list rig issues for %s: %v", id, err)
	}
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

// rigDependsOn reports whether issueID depends on dependsOnID with depType in
// the rig that routes issueID.
func (f *molBondProxiedFixture) rigDependsOn(issueID, dependsOnID string, depType types.DependencyType) bool {
	f.t.Helper()
	rr := f.routedRead(issueID)
	defer rr.Close()
	deps, err := rr.Store.GetDependenciesWithMetadata(f.ctx, issueID)
	if err != nil {
		f.t.Fatalf("read dependencies of %s: %v", issueID, err)
	}
	for _, dep := range deps {
		if dep.ID == dependsOnID && dep.DependencyType == depType {
			return true
		}
	}
	return false
}

// rigDependencies lists the dependency records of issueID in its rig.
func (f *molBondProxiedFixture) rigDependencies(issueID string) []*types.Dependency {
	f.t.Helper()
	rr := f.routedRead(issueID)
	defer rr.Close()
	deps, err := rr.Store.GetDependencyRecords(f.ctx, issueID)
	if err != nil {
		f.t.Fatalf("read dependency records of %s: %v", issueID, err)
	}
	return deps
}

// seedRig adds issues to the gt- rig through a writable routed open, closed
// before the test bonds.
func (f *molBondProxiedFixture) seedRig(ids ...string) {
	f.t.Helper()
	rr, err := resolveViaPrefixRoutingWithAccess(f.ctx, "gt-mol1", true, true)
	if err != nil {
		f.t.Fatalf("open the gt- rig writable: %v", err)
	}
	defer rr.Close()
	for _, id := range ids {
		seedIssue(f.t, f.ctx, rr.Store, id)
	}
}

// assertServedUnwritten fails when the served launcher database committed or
// was asked to write anything.
func (f *molBondProxiedFixture) assertServedUnwritten(context string) {
	f.t.Helper()
	if f.served.commits != nil || f.served.writes != 0 {
		f.t.Errorf("%s: served database got commits %q and %d write(s), want none", context, f.served.commits, f.served.writes)
	}
}

// molBondServedProvider is the proxied launcher's database: every unit of work
// sees its issues; writes are counted and recorded, commits by message.
type molBondServedProvider struct {
	mu      sync.Mutex
	issues  map[string]*types.Issue
	opens   int
	onOpen  func(open int)
	writes  int
	deps    []*types.Dependency
	commits []string
}

func newMolBondServedProvider(issues ...*types.Issue) *molBondServedProvider {
	p := &molBondServedProvider{issues: map[string]*types.Issue{}}
	for _, issue := range issues {
		p.issues[issue.ID] = issue
	}
	return p
}

func (p *molBondServedProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	p.mu.Lock()
	p.opens++
	open, onOpen := p.opens, p.onOpen
	p.mu.Unlock()
	if onOpen != nil {
		onOpen(open)
	}
	return molBondServedUOW{p: p}, nil
}

func (p *molBondServedProvider) Close(context.Context) error { return nil }

func (p *molBondServedProvider) lookup(id string, wisp bool) (*types.Issue, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if issue, ok := p.issues[id]; ok && issue.Ephemeral == wisp {
		return issue, nil
	}
	return nil, sql.ErrNoRows
}

// molBondServedUOW embeds the interface without implementing it: a use the
// fake does not serve panics and names itself.
type molBondServedUOW struct {
	uow.UnitOfWork
	p *molBondServedProvider
}

func (u molBondServedUOW) Close(context.Context) {}

func (u molBondServedUOW) Commit(_ context.Context, message string) error {
	u.p.mu.Lock()
	defer u.p.mu.Unlock()
	u.p.commits = append(u.p.commits, message)
	return nil
}

func (u molBondServedUOW) IssueUseCase() domain.IssueUseCase {
	return molBondServedIssues{p: u.p}
}
func (u molBondServedUOW) LabelUseCase() domain.LabelUseCase { return molBondServedLabels{p: u.p} }
func (u molBondServedUOW) DependencyUseCase() domain.DependencyUseCase {
	return molBondServedDeps{p: u.p}
}
func (u molBondServedUOW) ConfigUseCase() domain.ConfigUseCase   { return molBondServedConfig{} }
func (u molBondServedUOW) CommentUseCase() domain.CommentUseCase { return nil }

type molBondServedIssues struct {
	domain.IssueUseCase
	p *molBondServedProvider
}

func (s molBondServedIssues) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return s.p.lookup(id, false)
}

func (s molBondServedIssues) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	return s.p.lookup(id, true)
}

func (s molBondServedIssues) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) (domain.SearchPage, error) {
	var page domain.SearchPage
	for _, id := range filter.IDs {
		if issue, err := s.p.lookup(id, false); err == nil {
			page.Items = append(page.Items, issue)
		}
	}
	return page, nil
}

func (s molBondServedIssues) SearchIssueIDs(_ context.Context, query string, filter types.IssueFilter) ([]string, error) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	wisps := filter.Ephemeral != nil && *filter.Ephemeral
	var ids []string
	for id, issue := range s.p.issues {
		if issue.Ephemeral == wisps && strings.Contains(id, query) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (s molBondServedIssues) CreateIssue(context.Context, domain.CreateIssueParams, string) (domain.CreateIssueResult, error) {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	s.p.writes++
	return domain.CreateIssueResult{}, nil
}

type molBondServedLabels struct {
	domain.LabelUseCase
	p *molBondServedProvider
}

func (l molBondServedLabels) GetLabels(_ context.Context, id string) ([]string, error) {
	if issue, err := l.p.lookup(id, false); err == nil {
		return issue.Labels, nil
	}
	return nil, nil
}

func (l molBondServedLabels) GetWispLabels(_ context.Context, id string) ([]string, error) {
	if issue, err := l.p.lookup(id, true); err == nil {
		return issue.Labels, nil
	}
	return nil, nil
}

type molBondServedDeps struct {
	domain.DependencyUseCase
	p *molBondServedProvider
}

func (d molBondServedDeps) AddDependency(_ context.Context, dep *types.Dependency, _ string) error {
	d.p.mu.Lock()
	defer d.p.mu.Unlock()
	d.p.writes++
	d.p.deps = append(d.p.deps, dep)
	return nil
}

type molBondServedConfig struct {
	domain.ConfigUseCase
}

func (molBondServedConfig) GetConfig(_ context.Context, key string) (string, error) {
	if key == "issue_prefix" {
		return "hq", nil
	}
	return "", nil
}

func (molBondServedConfig) IsInfraTypeCtx(context.Context, types.IssueType) (bool, error) {
	return false, nil
}

// lateWriteFailingStore fails a routed bond's transaction at its first
// dependency write, after the spawn has created at least one row in it.
// Unwrap keeps its logical store identity (dependencyStoreKey) the rig's.
type lateWriteFailingStore struct {
	storage.DoltStorage
	created int
}

var errLateBondWrite = errors.New("injected late bond write failure")

func (s *lateWriteFailingStore) Unwrap() storage.DoltStorage { return s.DoltStorage }

func (s *lateWriteFailingStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	return s.DoltStorage.RunInTransaction(ctx, commitMsg, func(tx storage.Transaction) error {
		return fn(&lateWriteFailingTx{Transaction: tx, store: s})
	})
}

type lateWriteFailingTx struct {
	storage.Transaction
	store *lateWriteFailingStore
}

func (tx *lateWriteFailingTx) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if err := tx.Transaction.CreateIssue(ctx, issue, actor); err != nil {
		return err
	}
	tx.store.created++
	return nil
}

func (tx *lateWriteFailingTx) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	if tx.store.created > 0 {
		return errLateBondWrite
	}
	return tx.Transaction.AddDependency(ctx, dep, actor)
}

// TestMolBondProxiedFormulaSpawnsIntoRoutedRig: a formula bonded with a
// molecule that only a prefix-routed rig holds spawns and attaches in that rig,
// in either operand order, and leaves the served launcher database unwritten.
// It failed with "'gt-mol1' not found as issue or formula" when the proxied
// route resolved operands against the served database alone.
func TestMolBondProxiedFormulaSpawnsIntoRoutedRig(t *testing.T) {
	f := newMolBondProxiedFixture(t)

	for _, tc := range []struct {
		name       string
		argA, argB string
	}{
		{name: "formula_first", argA: molBondProxiedFormula, argB: "gt-mol1"},
		{name: "molecule_first", argA: "gt-mol1", argB: molBondProxiedFormula},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := f.rigIssueIDs("gt-mol1")
			runErr, _, stderr := f.bond(molBondInput{argA: tc.argA, argB: tc.argB})
			if runErr != nil {
				t.Fatalf("bond %s %s: %v\nstderr:\n%s", tc.argA, tc.argB, runErr, stderr)
			}
			after := f.rigIssueIDs("gt-mol1")
			if len(after) != len(before)+2 {
				t.Fatalf("rig issues %v -> %v, want the formula's root and step spawned there", before, after)
			}
			attached := false
			for _, id := range after {
				if f.rigDependsOn(id, "gt-mol1", types.DepBlocks) {
					attached = true
				}
			}
			if !attached {
				t.Errorf("no spawned rig issue blocks on gt-mol1: the spawn was not attached in the rig")
			}
			f.assertServedUnwritten("formula + routed molecule")
		})
	}
}

// TestMolBondProxiedMolMolInOneRoutedRig: two molecules that one prefix-routed
// rig holds bond in that rig (B blocks on A there); the served launcher
// database is never written.
func TestMolBondProxiedMolMolInOneRoutedRig(t *testing.T) {
	f := newMolBondProxiedFixture(t)

	runErr, _, stderr := f.bond(molBondInput{argA: "gt-mol1", argB: "gt-mol2"})
	if runErr != nil {
		t.Fatalf("bond gt-mol1 gt-mol2: %v\nstderr:\n%s", runErr, stderr)
	}
	if !f.rigDependsOn("gt-mol2", "gt-mol1", types.DepBlocks) {
		t.Errorf("gt-mol2 does not block on gt-mol1 in the rig")
	}
	f.assertServedUnwritten("routed mol + mol")
}

// TestMolBondProxiedDryRunPreviewsRoutedHome: a dry run over routed operands
// previews the bond without a writable open, a rig write or a served write.
func TestMolBondProxiedDryRunPreviewsRoutedHome(t *testing.T) {
	f := newMolBondProxiedFixture(t)

	for _, tc := range []struct {
		name       string
		argA, argB string
		want       []string
	}{
		{name: "formula_and_molecule", argA: molBondProxiedFormula, argB: "gt-mol1", want: []string{
			"Dry run: bond " + molBondProxiedFormula + " + gt-mol1",
			"A: " + molBondProxiedFormula + " (formula → will cook as proto)",
		}},
		{name: "two_molecules", argA: "gt-mol1", argB: "gt-mol2", want: []string{"Dry run: bond gt-mol1 + gt-mol2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := f.rigIssueIDs("gt-mol1")
			reopens := f.reopens
			runErr, stdout, stderr := f.bond(molBondInput{argA: tc.argA, argB: tc.argB, dryRun: true})
			if runErr != nil {
				t.Fatalf("dry-run bond %s %s: %v\nstderr:\n%s", tc.argA, tc.argB, runErr, stderr)
			}
			for _, want := range tc.want {
				if !strings.Contains(stdout, want) {
					t.Errorf("dry-run output lacks %q:\n%s", want, stdout)
				}
			}
			if f.reopens != reopens {
				t.Errorf("dry run reopened the routed home writable %d time(s)", f.reopens-reopens)
			}
			if after := f.rigIssueIDs("gt-mol1"); len(after) != len(before) {
				t.Errorf("dry run changed the rig: %v -> %v", before, after)
			}
			if deps := f.rigDependencies("gt-mol2"); len(deps) != 0 {
				t.Errorf("dry run wrote dependencies in the rig: %+v", deps)
			}
			f.assertServedUnwritten("dry run")
		})
	}
}

// TestMolBondProxiedRefusesCrossStorePairs: operands in different logical
// stores (the served database and a rig, or two rigs) are refused with the
// different-stores error before any writable open, dry run or not.
func TestMolBondProxiedRefusesCrossStorePairs(t *testing.T) {
	f := newMolBondProxiedFixture(t)

	for _, pair := range [][2]string{
		{"hq-served", "gt-mol1"},
		{"gt-mol1", "hq-served"},
		{"gt-mol1", "zz-other"},
		{"zz-other", "gt-mol1"},
	} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_%s_dry_run_%t", pair[0], pair[1], dryRun), func(t *testing.T) {
				reopens := f.reopens
				runErr, _, stderr := f.bond(molBondInput{argA: pair[0], argB: pair[1], dryRun: dryRun})
				if runErr == nil {
					t.Fatalf("bond %s %s succeeded, want the different-stores refusal", pair[0], pair[1])
				}
				if !strings.Contains(stderr, "they live in different stores/rigs") {
					t.Errorf("bond %s %s: stderr lacks the different-stores refusal:\n%s", pair[0], pair[1], stderr)
				}
				if f.reopens != reopens {
					t.Errorf("refused bond reopened a routed home writable")
				}
				for _, id := range []string{"gt-mol1", "zz-other"} {
					if deps := f.rigDependencies(id); len(deps) != 0 {
						t.Errorf("refused bond wrote dependencies on %s: %+v", id, deps)
					}
				}
				f.assertServedUnwritten("cross-store refusal")
			})
		}
	}
}

// TestMolBondProxiedKeepsRouteErrors: a prefix route that fails to open, or
// refuses, keeps its own actionable error when no formula has the operand's
// name; a same-named formula still wins; a plain miss stays "not found".
func TestMolBondProxiedKeepsRouteErrors(t *testing.T) {
	f := newMolBondProxiedFixture(t)
	f.routes = append(f.routes, []byte("{\"prefix\":\"bad-\",\"path\":\"missing-rig\"}\n")...)
	f.writeRoutes()

	expectRefusal := func(t *testing.T, argA, argB, want string) {
		t.Helper()
		reopens := f.reopens
		runErr, _, stderr := f.bond(molBondInput{argA: argA, argB: argB})
		if runErr == nil {
			t.Fatalf("bond %s %s succeeded, want %q", argA, argB, want)
		}
		if !strings.Contains(stderr, want) {
			t.Errorf("bond %s %s: stderr lacks %q:\n%s", argA, argB, want, stderr)
		}
		if f.reopens != reopens {
			t.Errorf("failed bond reopened a routed home writable")
		}
		f.assertServedUnwritten(argA + " " + argB)
	}

	t.Run("route_failure", func(t *testing.T) {
		expectRefusal(t, "bad-x1", molBondProxiedFormula, "target rig has no dolt_database configured")
	})

	t.Run("proxied_server_rig_refusal", func(t *testing.T) {
		previous := f.setRigMode(configfile.DoltModeProxiedServer)
		defer f.setRigMode(previous)
		expectRefusal(t, molBondProxiedFormula, "gt-mol1", "will not open a proxied-server workspace")
	})

	t.Run("same_named_formula_wins", func(t *testing.T) {
		data, err := json.Marshal(&formula.Formula{
			Formula: "bad-x2",
			Version: 1,
			Type:    formula.TypeWorkflow,
			Steps:   []*formula.Step{{ID: "only", Title: "Only step", Type: "task"}},
		})
		if err != nil {
			t.Fatalf("marshal formula: %v", err)
		}
		f.writeFormula("bad-x2", data)
		runErr, stdout, stderr := f.bond(molBondInput{argA: "bad-x2", argB: molBondProxiedFormula, dryRun: true})
		if runErr != nil {
			t.Fatalf("dry-run bond bad-x2 %s: %v\nstderr:\n%s", molBondProxiedFormula, runErr, stderr)
		}
		if want := "A: bad-x2 (formula → will cook as proto)"; !strings.Contains(stdout, want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, stdout)
		}
	})

	t.Run("plain_miss", func(t *testing.T) {
		expectRefusal(t, "nope-x3", molBondProxiedFormula, "'nope-x3' not found as issue or formula")
	})

	t.Run("unreadable_routes_file", func(t *testing.T) {
		// A line past the scanner's token limit makes loadPrefixRoutes fail,
		// unlike a malformed line, which is skipped silently.
		if err := os.WriteFile(f.routesPath, []byte(strings.Repeat("a", 70000)), 0o644); err != nil {
			t.Fatalf("rewrite routes.jsonl: %v", err)
		}
		defer f.writeRoutes()
		expectRefusal(t, molBondProxiedFormula, "gt-any", "loading routes")
	})

	f.seedRig("gt-amb1", "gt-amb2")

	t.Run("ambiguous_rig_id_without_formula", func(t *testing.T) {
		before := f.rigIssueIDs("gt-amb1")
		expectRefusal(t, molBondProxiedFormula, "gt-amb", "ambiguous")
		if after := f.rigIssueIDs("gt-amb1"); len(after) != len(before) {
			t.Errorf("failed bond changed the rig's issues: %v -> %v", before, after)
		}
		if deps := f.rigDependencies("gt-amb1"); len(deps) != 0 {
			t.Errorf("failed bond left dependency rows on gt-amb1: %+v", deps)
		}
		if deps := f.rigDependencies("gt-amb2"); len(deps) != 0 {
			t.Errorf("failed bond left dependency rows on gt-amb2: %+v", deps)
		}
	})

	t.Run("formula_wins_over_route_refusal", func(t *testing.T) {
		data, err := json.Marshal(&formula.Formula{
			Formula: "gt-amb",
			Version: 1,
			Type:    formula.TypeWorkflow,
			Steps:   []*formula.Step{{ID: "only", Title: "Ambiguity shadowed by formula", Type: "task"}},
		})
		if err != nil {
			t.Fatalf("marshal formula: %v", err)
		}
		f.writeFormula("gt-amb", data)
		before := f.rigIssueIDs("gt-amb2")
		runErr, _, stderr := f.bond(molBondInput{argA: "gt-amb", argB: "gt-amb2"})
		if runErr != nil {
			t.Fatalf("bond gt-amb gt-amb2: the same-named formula did not win over the ambiguity refusal: %v\nstderr:\n%s", runErr, stderr)
		}
		after := f.rigIssueIDs("gt-amb2")
		if len(after) != len(before)+2 {
			t.Fatalf("rig issues %v -> %v, want the formula's root and step spawned there", before, after)
		}
		attached := false
		for _, id := range after {
			if f.rigDependsOn(id, "gt-amb2", types.DepBlocks) {
				attached = true
			}
		}
		if !attached {
			t.Errorf("no spawned rig issue blocks on gt-amb2: the formula did not bond into the rig")
		}
		f.assertServedUnwritten("formula over route refusal")
	})
}

// TestMolBondProxiedRefusesAChangedRoutedHome: a writable reopen that hands
// back another store than the one discovery planned on is refused before any
// write.
func TestMolBondProxiedRefusesAChangedRoutedHome(t *testing.T) {
	f := newMolBondProxiedFixture(t)
	f.reopenWrapper = func(rr *RoutedResult) *RoutedResult {
		rr.Close()
		other, err := resolveViaPrefixRoutingWithAccess(f.ctx, "zz-other", true, true)
		if err != nil {
			t.Fatalf("open the other rig: %v", err)
		}
		return other
	}

	runErr, _, stderr := f.bond(molBondInput{argA: "gt-mol1", argB: "gt-mol2"})
	if runErr == nil {
		t.Fatalf("bond onto a changed home succeeded")
	}
	if !strings.Contains(stderr, "changed between discovery and writable reopen") {
		t.Errorf("stderr lacks the changed-home refusal:\n%s", stderr)
	}
	if f.reopens != 1 {
		t.Errorf("got %d writable reopen(s), want 1", f.reopens)
	}
	if deps := f.rigDependencies("gt-mol2"); len(deps) != 0 {
		t.Errorf("refused bond wrote dependencies in the planned rig: %+v", deps)
	}
	if deps := f.rigDependencies("zz-other"); len(deps) != 0 {
		t.Errorf("refused bond wrote dependencies in the other rig: %+v", deps)
	}
	f.assertServedUnwritten("changed home")
}

// TestMolBondProxiedServedHomeCommitsOnce: a bond whose operands the served
// database holds stays one served unit of work with one commit, and opens no
// routed home.
func TestMolBondProxiedServedHomeCommitsOnce(t *testing.T) {
	f := newMolBondProxiedFixture(t)
	for _, id := range []string{"hq-a", "hq-b"} {
		f.served.issues[id] = &types.Issue{ID: id, Title: id, Status: types.StatusOpen, IssueType: types.TypeTask}
	}

	runErr, _, stderr := f.bond(molBondInput{argA: "hq-a", argB: "hq-b"})
	if runErr != nil {
		t.Fatalf("bond hq-a hq-b: %v\nstderr:\n%s", runErr, stderr)
	}
	if want := []string{"bd: mol bond hq-a hq-b"}; fmt.Sprint(f.served.commits) != fmt.Sprint(want) {
		t.Errorf("served commits = %q, want %q", f.served.commits, want)
	}
	if len(f.served.deps) != 1 || f.served.deps[0].IssueID != "hq-b" || f.served.deps[0].DependsOnID != "hq-a" || f.served.deps[0].Type != types.DepBlocks {
		t.Errorf("served dependencies = %+v, want hq-b blocks on hq-a", f.served.deps)
	}
	if f.reopens != 0 {
		t.Errorf("served bond reopened a routed home %d time(s)", f.reopens)
	}
}

// TestMolBondProxiedServedIssueGoneFailsWithoutFormulaFallback: a served
// issue the planner resolved but that is gone when the write transaction
// re-reads it fails the bond; a formula of the same name is not cooked in
// its place.
func TestMolBondProxiedServedIssueGoneFailsWithoutFormulaFallback(t *testing.T) {
	f := newMolBondProxiedFixture(t)
	for _, id := range []string{"hq-a", "hq-gone"} {
		f.served.issues[id] = &types.Issue{ID: id, Title: id, Status: types.StatusOpen, IssueType: types.TypeTask}
	}
	data, err := json.Marshal(&formula.Formula{
		Formula: "hq-gone",
		Version: 1,
		Type:    formula.TypeWorkflow,
		Steps:   []*formula.Step{{ID: "only", Title: "Only step", Type: "task"}},
	})
	if err != nil {
		t.Fatalf("marshal formula: %v", err)
	}
	f.writeFormula("hq-gone", data)
	// Opens 1 and 2 are the planner's reads, one per operand; open 3 is the
	// write transaction, which must find hq-gone deleted.
	f.served.onOpen = func(open int) {
		if open == 3 {
			f.served.mu.Lock()
			delete(f.served.issues, "hq-gone")
			f.served.mu.Unlock()
		}
	}

	runErr, _, stderr := f.bond(molBondInput{argA: "hq-gone", argB: "hq-a"})
	if f.served.opens < 3 {
		t.Fatalf("bond opened %d served unit(s) of work, want the planner's 2 reads and the write transaction (stderr:\n%s)", f.served.opens, stderr)
	}
	if runErr == nil {
		t.Fatalf("bond with a deleted planned issue succeeded")
	}
	f.assertServedUnwritten("deleted planned issue")
}

// TestMolBondProxiedRoutedLateWriteRollsBack: a routed bond whose transaction
// fails after the spawn created rows leaves the rig as it was and the served
// database unwritten.
func TestMolBondProxiedRoutedLateWriteRollsBack(t *testing.T) {
	f := newMolBondProxiedFixture(t)
	var failing *lateWriteFailingStore
	f.reopenWrapper = func(rr *RoutedResult) *RoutedResult {
		failing = &lateWriteFailingStore{DoltStorage: rr.Store}
		rr.Store = failing
		return rr
	}

	before := f.rigIssueIDs("gt-mol1")
	runErr, _, stderr := f.bond(molBondInput{argA: molBondProxiedFormula, argB: "gt-mol1"})
	if runErr == nil {
		t.Fatalf("bond with a failing late write succeeded")
	}
	if !strings.Contains(stderr, errLateBondWrite.Error()) {
		t.Errorf("stderr lacks the injected failure:\n%s", stderr)
	}
	if failing == nil || failing.created == 0 {
		t.Fatalf("the injected failure did not come after a spawned row (created=%v)", failing)
	}
	if after := f.rigIssueIDs("gt-mol1"); len(after) != len(before) {
		t.Errorf("failed bond left rows in the rig: %v -> %v", before, after)
	}
	f.assertServedUnwritten("late write failure")
}
