//go:build cgo && unix

package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedServerSDKOpenBestAvailable pins GH#8323 end to end: the Go SDK's
// OpenBestAvailable on a proxied-server workspace opens through the bd proxy
// (AC 2), starting it from the bd on PATH when none is running (AC 8), and
// the bd CLI then reads every write the SDK made. Close leaves the shared
// proxy running (AC 7), and no embedded store is created (AC 1).
func TestProxiedServerSDKOpenBestAvailable(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "sdk")
	ctx := context.Background()

	cliBlocker := bdProxiedCreate(t, bd, p.dir, "CLI-created blocker")

	// Stop the proxy bd init started, so the SDK open has to start one, and
	// can only do it through the bd on PATH.
	if err := proxy.Shutdown(p.proxyRoot); err != nil {
		t.Fatalf("proxy.Shutdown: %v", err)
	}
	if pf := readManagedProxyPidFile(t, p); pf != nil && processAlive(pf.Pid) {
		t.Fatalf("proxy pid %d still alive after Shutdown", pf.Pid)
	}
	bin := t.TempDir()
	if err := os.Symlink(bd, filepath.Join(bin, "bd")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	store, err := beads.OpenBestAvailable(ctx, p.beadsDir)
	if err != nil {
		t.Fatalf("OpenBestAvailable: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = store.Close()
		}
	})

	const actor = "sdk-test"
	newIssue := func(title string) *types.Issue {
		return &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	a := newIssue("SDK issue A")
	if err := store.CreateIssue(ctx, a, actor); err != nil {
		t.Fatalf("CreateIssue A: %v", err)
	}
	b := newIssue("SDK issue B")
	if err := store.CreateIssue(ctx, b, actor); err != nil {
		t.Fatalf("CreateIssue B: %v", err)
	}
	if a.ID == "" || b.ID == "" {
		t.Fatalf("CreateIssue did not mint ids: a=%q b=%q", a.ID, b.ID)
	}
	if got, err := store.GetIssue(ctx, a.ID); err != nil || got.Title != a.Title {
		t.Fatalf("GetIssue(%s) = %+v, %v", a.ID, got, err)
	}
	if err := store.UpdateIssue(ctx, a.ID, map[string]interface{}{"title": "SDK issue A renamed"}, actor); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	// Dependency add and remove (B), then a kept edge onto the CLI's issue.
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: a.ID, DependsOnID: b.ID, Type: types.DepBlocks}, actor); err != nil {
		t.Fatalf("AddDependency a->b: %v", err)
	}
	if err := store.RemoveDependency(ctx, a.ID, b.ID, actor); err != nil {
		t.Fatalf("RemoveDependency a->b: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: a.ID, DependsOnID: cliBlocker.ID, Type: types.DepBlocks}, actor); err != nil {
		t.Fatalf("AddDependency a->cli: %v", err)
	}

	if err := store.AddLabel(ctx, a.ID, "dropped", actor); err != nil {
		t.Fatalf("AddLabel dropped: %v", err)
	}
	if err := store.RemoveLabel(ctx, a.ID, "dropped", actor); err != nil {
		t.Fatalf("RemoveLabel dropped: %v", err)
	}
	if err := store.AddLabel(ctx, a.ID, "sdk-kept", actor); err != nil {
		t.Fatalf("AddLabel sdk-kept: %v", err)
	}
	if err := store.CloseIssue(ctx, b.ID, "done via SDK", actor, ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	committed := newIssue("SDK transaction committed")
	if err := store.RunInTransaction(ctx, "sdk: transaction commit", func(tx beads.Transaction) error {
		return tx.CreateIssue(ctx, committed, actor)
	}); err != nil {
		t.Fatalf("RunInTransaction (commit): %v", err)
	}
	rolledBack := newIssue("SDK transaction rolled back")
	errRollback := errors.New("roll back")
	if err := store.RunInTransaction(ctx, "sdk: transaction rollback", func(tx beads.Transaction) error {
		if err := tx.CreateIssue(ctx, rolledBack, actor); err != nil {
			return err
		}
		return errRollback
	}); !errors.Is(err, errRollback) {
		t.Fatalf("RunInTransaction (rollback) = %v, want the callback's error", err)
	}

	// The full create contract (GH#8323 review C1), flat and in a
	// transaction: labels and comments persisted with the issue, a NoHistory
	// issue routed to the wisp plane, and a batch's inline dependency.
	richIssue := func(title string) *types.Issue {
		issue := newIssue(title)
		issue.Labels = []string{"sdk-create-label"}
		issue.Comments = []*types.Comment{{Author: actor, Text: title + " comment"}}
		return issue
	}
	flatRich := richIssue("SDK flat rich create")
	if err := store.CreateIssue(ctx, flatRich, actor); err != nil {
		t.Fatalf("CreateIssue rich: %v", err)
	}
	if flatRich.Comments[0].ID == "" {
		t.Errorf("CreateIssue did not write the minted comment ID back onto the caller's comment")
	}
	flatNoHistory := newIssue("SDK flat no-history create")
	flatNoHistory.NoHistory = true
	if err := store.CreateIssue(ctx, flatNoHistory, actor); err != nil {
		t.Fatalf("CreateIssue NoHistory: %v", err)
	}
	flatBatchDep := newIssue("SDK flat batch with dep")
	flatBatchDep.Dependencies = []*types.Dependency{{DependsOnID: cliBlocker.ID, Type: types.DepBlocks}}
	if err := store.CreateIssues(ctx, []*types.Issue{flatBatchDep}, actor); err != nil {
		t.Fatalf("CreateIssues with dep: %v", err)
	}
	txRich := richIssue("SDK tx rich create")
	txNoHistory := newIssue("SDK tx no-history create")
	txNoHistory.NoHistory = true
	txBatchDep := newIssue("SDK tx batch with dep")
	txBatchDep.Dependencies = []*types.Dependency{{DependsOnID: cliBlocker.ID, Type: types.DepBlocks}}
	if err := store.RunInTransaction(ctx, "sdk: transaction rich creates", func(tx beads.Transaction) error {
		if err := tx.CreateIssue(ctx, txRich, actor); err != nil {
			return err
		}
		if err := tx.CreateIssue(ctx, txNoHistory, actor); err != nil {
			return err
		}
		return tx.CreateIssues(ctx, []*types.Issue{txBatchDep}, actor)
	}); err != nil {
		t.Fatalf("RunInTransaction (rich creates): %v", err)
	}

	// Rows: each issue sits on exactly the plane the Storage contract routes
	// it to.
	sqlStore, ok := store.(interface{ UnderlyingDB() *sql.DB })
	if !ok || sqlStore.UnderlyingDB() == nil {
		t.Fatalf("the proxied store exposes no UnderlyingDB pool")
	}
	db := sqlStore.UnderlyingDB()
	countIn := func(table, id string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", id).Scan(&n); err != nil {
			t.Fatalf("count %s in %s: %v", id, table, err)
		}
		return n
	}
	for _, c := range []struct {
		issue *types.Issue
		wisp  bool
	}{{flatRich, false}, {flatNoHistory, true}, {flatBatchDep, false}, {txRich, false}, {txNoHistory, true}, {txBatchDep, false}} {
		wisps, issues := countIn("wisps", c.issue.ID), countIn("issues", c.issue.ID)
		if c.wisp && (wisps != 1 || issues != 0) || !c.wisp && (wisps != 0 || issues != 1) {
			t.Errorf("%q (%s): wisps=%d issues=%d, want it only in the %s table", c.issue.Title, c.issue.ID, wisps, issues, map[bool]string{true: "wisps", false: "issues"}[c.wisp])
		}
	}

	// The flat reads answer as DoltStore's do: GetIssue carries the issue's
	// labels from either plane, and GetIssuesByIDs reads both planes with
	// their labels, collapsing a repeated id and leaving out one that names
	// no row.
	if err := store.AddLabel(ctx, flatNoHistory.ID, "sdk-wisp-label", actor); err != nil {
		t.Fatalf("AddLabel on the NoHistory issue: %v", err)
	}
	wantLabels := map[string][]string{a.ID: {"sdk-kept"}, flatNoHistory.ID: {"sdk-wisp-label"}, txNoHistory.ID: nil}
	for _, id := range []string{a.ID, flatNoHistory.ID} {
		got, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		if !slices.Equal(got.Labels, wantLabels[id]) {
			t.Errorf("GetIssue(%s).Labels = %v, want %v", id, got.Labels, wantLabels[id])
		}
	}
	batch, err := store.GetIssuesByIDs(ctx, []string{a.ID, flatNoHistory.ID, txNoHistory.ID, "sdk-no-such-id", a.ID})
	if err != nil {
		t.Fatalf("GetIssuesByIDs: %v", err)
	}
	batchLabels := map[string][]string{}
	for _, issue := range batch {
		batchLabels[issue.ID] = issue.Labels
	}
	if len(batch) != len(wantLabels) {
		t.Errorf("GetIssuesByIDs returned %d rows (%v), want %d", len(batch), batchLabels, len(wantLabels))
	}
	for id, want := range wantLabels {
		if got, ok := batchLabels[id]; !ok {
			t.Errorf("GetIssuesByIDs left out %s", id)
		} else if !slices.Equal(got, want) {
			t.Errorf("GetIssuesByIDs: %s labels = %v, want %v", id, got, want)
		}
	}

	proxyBefore := readManagedProxyPidFile(t, p)
	if proxyBefore == nil || !processAlive(proxyBefore.Pid) {
		t.Fatalf("no live proxy after the SDK open: %+v", proxyBefore)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true
	if pf := readManagedProxyPidFile(t, p); pf == nil || pf.Pid != proxyBefore.Pid || !processAlive(pf.Pid) {
		t.Fatalf("Close stopped the shared proxy: before %+v, after %+v", proxyBefore, pf)
	}
	if _, err := os.Stat(filepath.Join(p.beadsDir, "embeddeddolt")); !os.IsNotExist(err) {
		t.Fatalf("the SDK open created .beads/embeddeddolt (stat error: %v)", err)
	}

	// The CLI reads what the SDK wrote.
	shownA := bdProxiedShowDetailsFirst(t, bd, p.dir, a.ID)
	if shownA["title"] != "SDK issue A renamed" {
		t.Errorf("bd show %s title = %v, want the SDK's update", a.ID, shownA["title"])
	}
	if labels, _ := shownA["labels"].([]interface{}); len(labels) != 1 || labels[0] != "sdk-kept" {
		t.Errorf("bd show %s labels = %v, want [sdk-kept]", a.ID, shownA["labels"])
	}
	deps, _ := shownA["dependencies"].([]interface{})
	if len(deps) != 1 {
		t.Errorf("bd show %s dependencies = %v, want exactly the edge onto %s", a.ID, shownA["dependencies"], cliBlocker.ID)
	} else if dep, _ := deps[0].(map[string]interface{}); dep["id"] != cliBlocker.ID {
		t.Errorf("bd show %s dependency = %v, want %s", a.ID, deps[0], cliBlocker.ID)
	}
	if shownB := bdProxiedShowDetailsFirst(t, bd, p.dir, b.ID); shownB["status"] != string(types.StatusClosed) {
		t.Errorf("bd show %s status = %v, want closed", b.ID, shownB["status"])
	}

	for _, issue := range []*types.Issue{flatRich, txRich} {
		shown := bdProxiedShowDetailsFirst(t, bd, p.dir, issue.ID, "--include-comments")
		if labels, _ := shown["labels"].([]interface{}); len(labels) != 1 || labels[0] != "sdk-create-label" {
			t.Errorf("bd show %s labels = %v, want [sdk-create-label]", issue.ID, shown["labels"])
		}
		comments, _ := shown["comments"].([]interface{})
		if len(comments) != 1 {
			t.Errorf("bd show %s comments = %v, want the one created with the issue", issue.ID, shown["comments"])
		} else if c, _ := comments[0].(map[string]interface{}); c["text"] != issue.Title+" comment" {
			t.Errorf("bd show %s comment = %v, want %q", issue.ID, comments[0], issue.Title+" comment")
		}
	}
	for _, issue := range []*types.Issue{flatBatchDep, txBatchDep} {
		shown := bdProxiedShowDetailsFirst(t, bd, p.dir, issue.ID)
		deps, _ := shown["dependencies"].([]interface{})
		if len(deps) != 1 {
			t.Errorf("bd show %s dependencies = %v, want the inline edge onto %s", issue.ID, shown["dependencies"], cliBlocker.ID)
		} else if dep, _ := deps[0].(map[string]interface{}); dep["id"] != cliBlocker.ID {
			t.Errorf("bd show %s dependency = %v, want %s", issue.ID, deps[0], cliBlocker.ID)
		}
	}

	listed := map[string]bool{}
	for _, issue := range bdProxiedListJSON(t, bd, p, "--all", "--limit", "0") {
		listed[issue.Title] = true
	}
	for _, title := range []string{"SDK issue A renamed", "SDK issue B", committed.Title, cliBlocker.Title} {
		if !listed[title] {
			t.Errorf("bd list --all does not show %q (SDK write lost)", title)
		}
	}
	if listed[rolledBack.Title] {
		t.Errorf("bd list --all shows %q, which a rolled-back transaction created", rolledBack.Title)
	}
}
