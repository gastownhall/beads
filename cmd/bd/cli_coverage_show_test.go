//go:build e2e

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

var cliCoverageMutex sync.Mutex

func runBDForCoverage(t *testing.T, dir string, args ...string) (stdout string, stderr string) {
	t.Helper()

	cliCoverageMutex.Lock()
	defer cliCoverageMutex.Unlock()

	// Add --no-daemon to all commands except init.
	if len(args) > 0 && args[0] != "init" {
		args = append([]string{"--no-daemon"}, args...)
	}

	oldStdout := os.Stdout
	oldStderr := os.Stderr
	oldDir, _ := os.Getwd()
	oldArgs := os.Args

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	// Ensure direct mode.
	oldNoDaemon, noDaemonWasSet := os.LookupEnv("BEADS_NO_DAEMON")
	os.Setenv("BEADS_NO_DAEMON", "1")
	defer func() {
		if noDaemonWasSet {
			_ = os.Setenv("BEADS_NO_DAEMON", oldNoDaemon)
		} else {
			os.Unsetenv("BEADS_NO_DAEMON")
		}
	}()

	// Mark tests explicitly.
	oldTestMode, testModeWasSet := os.LookupEnv("BEADS_TEST_MODE")
	os.Setenv("BEADS_TEST_MODE", "1")
	defer func() {
		if testModeWasSet {
			_ = os.Setenv("BEADS_TEST_MODE", oldTestMode)
		} else {
			os.Unsetenv("BEADS_TEST_MODE")
		}
	}()

	// Ensure all commands (including init) operate on the temp workspace DB.
	db := filepath.Join(dir, ".beads", "beads.db")
	beadsDir := filepath.Join(dir, ".beads")
	oldBeadsDir, beadsDirWasSet := os.LookupEnv("BEADS_DIR")
	os.Setenv("BEADS_DIR", beadsDir)
	defer func() {
		if beadsDirWasSet {
			_ = os.Setenv("BEADS_DIR", oldBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	}()

	oldDB, dbWasSet := os.LookupEnv("BEADS_DB")
	os.Setenv("BEADS_DB", db)
	defer func() {
		if dbWasSet {
			_ = os.Setenv("BEADS_DB", oldDB)
		} else {
			os.Unsetenv("BEADS_DB")
		}
	}()
	oldBDDB, bdDBWasSet := os.LookupEnv("BD_DB")
	os.Setenv("BD_DB", db)
	defer func() {
		if bdDBWasSet {
			_ = os.Setenv("BD_DB", oldBDDB)
		} else {
			os.Unsetenv("BD_DB")
		}
	}()

	// Ensure actor is set so label operations record audit fields.
	oldActor, actorWasSet := os.LookupEnv("BD_ACTOR")
	os.Setenv("BD_ACTOR", "test-user")
	defer func() {
		if actorWasSet {
			_ = os.Setenv("BD_ACTOR", oldActor)
		} else {
			os.Unsetenv("BD_ACTOR")
		}
	}()
	oldBeadsActor, beadsActorWasSet := os.LookupEnv("BEADS_ACTOR")
	os.Setenv("BEADS_ACTOR", "test-user")
	defer func() {
		if beadsActorWasSet {
			_ = os.Setenv("BEADS_ACTOR", oldBeadsActor)
		} else {
			os.Unsetenv("BEADS_ACTOR")
		}
	}()

	rootCmd.SetArgs(args)
	os.Args = append([]string{"bd"}, args...)

	err := rootCmd.Execute()

	// Close and clean up all global state to prevent contamination between tests.
	if store != nil {
		store.Close()
		store = nil
	}
	if daemonClient != nil {
		daemonClient.Close()
		daemonClient = nil
	}

	// Reset all global flags and state (keep aligned with integration cli_fast_test).
	dbPath = ""
	actor = ""
	jsonOutput = false
	noDaemon = false
	noAutoFlush = false
	noAutoImport = false
	sandboxMode = false
	noDb = false
	autoFlushEnabled = true
	storeActive = false
	flushFailureCount = 0
	lastFlushError = nil
	if flushManager != nil {
		_ = flushManager.Shutdown()
		flushManager = nil
	}
	rootCtx = nil
	rootCancel = nil

	// Give SQLite time to release file locks.
	time.Sleep(10 * time.Millisecond)

	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr
	_ = os.Chdir(oldDir)
	os.Args = oldArgs
	rootCmd.SetArgs(nil)

	var outBuf, errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, rOut)
	_, _ = io.Copy(&errBuf, rErr)
	_ = rOut.Close()
	_ = rErr.Close()

	stdout = outBuf.String()
	stderr = errBuf.String()

	if err != nil {
		t.Fatalf("bd %v failed: %v\nStdout: %s\nStderr: %s", args, err, stdout, stderr)
	}

	return stdout, stderr
}

func extractJSONPayload(s string) string {
	if i := strings.IndexAny(s, "[{"); i >= 0 {
		return s[i:]
	}
	return s
}

func parseCreatedIssueID(t *testing.T, out string) string {
	t.Helper()

	p := extractJSONPayload(out)
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(p), &m); err != nil {
		t.Fatalf("parse create JSON: %v\n%s", err, out)
	}
	id, _ := m["id"].(string)
	if id == "" {
		t.Fatalf("missing id in create output: %s", out)
	}
	return id
}

func TestCoverage_ShowUpdateClose(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI coverage test in short mode")
	}

	dir := t.TempDir()
	runBDForCoverage(t, dir, "init", "--prefix", "test", "--quiet")

	out, _ := runBDForCoverage(t, dir, "create", "Show coverage issue", "-p", "1", "--json")
	id := parseCreatedIssueID(t, out)

	// Exercise update label flows (add -> set -> add/remove).
	runBDForCoverage(t, dir, "update", id, "--add-label", "old", "--json")
	runBDForCoverage(t, dir, "update", id, "--set-labels", "a,b", "--add-label", "c", "--remove-label", "a", "--json")
	runBDForCoverage(t, dir, "update", id, "--remove-label", "old", "--json")

	// Show JSON output and verify labels were applied.
	showOut, _ := runBDForCoverage(t, dir, "show", "--allow-stale", id, "--json")
	showPayload := extractJSONPayload(showOut)

	var details []map[string]interface{}
	if err := json.Unmarshal([]byte(showPayload), &details); err != nil {
		// Some commands may emit a single object; fall back to object parse.
		var single map[string]interface{}
		if err2 := json.Unmarshal([]byte(showPayload), &single); err2 != nil {
			t.Fatalf("parse show JSON: %v / %v\n%s", err, err2, showOut)
		}
		details = []map[string]interface{}{single}
	}
	if len(details) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(details))
	}
	labelsAny, ok := details[0]["labels"]
	if !ok {
		t.Fatalf("expected labels in show output: %s", showOut)
	}
	labelsBytes, _ := json.Marshal(labelsAny)
	labelsStr := string(labelsBytes)
	if !strings.Contains(labelsStr, "b") || !strings.Contains(labelsStr, "c") {
		t.Fatalf("expected labels b and c, got %s", labelsStr)
	}
	if strings.Contains(labelsStr, "a") || strings.Contains(labelsStr, "old") {
		t.Fatalf("expected labels a and old to be absent, got %s", labelsStr)
	}

	// Show text output.
	showText, _ := runBDForCoverage(t, dir, "show", "--allow-stale", id)
	if !strings.Contains(showText, "Show coverage issue") {
		t.Fatalf("expected show output to contain title, got: %s", showText)
	}

	// Multi-ID show should print both issues.
	out2, _ := runBDForCoverage(t, dir, "create", "Second issue", "-p", "2", "--json")
	id2 := parseCreatedIssueID(t, out2)
	multi, _ := runBDForCoverage(t, dir, "show", "--allow-stale", id, id2)
	if !strings.Contains(multi, "Show coverage issue") || !strings.Contains(multi, "Second issue") {
		t.Fatalf("expected multi-show output to include both titles, got: %s", multi)
	}
	if !strings.Contains(multi, "─") {
		t.Fatalf("expected multi-show output to include a separator line, got: %s", multi)
	}

	// Close and verify JSON output.
	closeOut, _ := runBDForCoverage(t, dir, "close", id, "--reason", "Done", "--json")
	closePayload := extractJSONPayload(closeOut)
	var closed []map[string]interface{}
	if err := json.Unmarshal([]byte(closePayload), &closed); err != nil {
		t.Fatalf("parse close JSON: %v\n%s", err, closeOut)
	}
	if len(closed) != 1 {
		t.Fatalf("expected 1 closed issue, got %d", len(closed))
	}
	if status, _ := closed[0]["status"].(string); status != string(types.StatusClosed) {
		t.Fatalf("expected status closed, got %q", status)
	}
}

func TestCoverage_TemplateAndPinnedProtections(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI coverage test in short mode")
	}

	dir := t.TempDir()
	runBDForCoverage(t, dir, "init", "--prefix", "test", "--quiet")

	// Create a pinned issue and verify close requires --force.
	out, _ := runBDForCoverage(t, dir, "create", "Pinned issue", "-p", "1", "--json")
	pinnedID := parseCreatedIssueID(t, out)
	runBDForCoverage(t, dir, "update", pinnedID, "--status", string(types.StatusPinned), "--json")
	_, closeErr := runBDForCoverage(t, dir, "close", pinnedID, "--reason", "Done")
	if !strings.Contains(closeErr, "cannot close pinned issue") {
		t.Fatalf("expected pinned close to be rejected, stderr: %s", closeErr)
	}

	forceOut, _ := runBDForCoverage(t, dir, "close", pinnedID, "--force", "--reason", "Done", "--json")
	forcePayload := extractJSONPayload(forceOut)
	var closed []map[string]interface{}
	if err := json.Unmarshal([]byte(forcePayload), &closed); err != nil {
		t.Fatalf("parse close JSON: %v\n%s", err, forceOut)
	}
	if len(closed) != 1 {
		t.Fatalf("expected 1 closed issue, got %d", len(closed))
	}

	// Insert a template issue directly and verify update/close protect it.
	dbFile := filepath.Join(dir, ".beads", "beads.db")
	s, err := sqlite.New(context.Background(), dbFile)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	ctx := context.Background()
	template := &types.Issue{
		Title:      "Template issue",
		Status:     types.StatusOpen,
		Priority:   2,
		IssueType:  types.TypeTask,
		IsTemplate: true,
	}
	if err := s.CreateIssue(ctx, template, "test-user"); err != nil {
		s.Close()
		t.Fatalf("CreateIssue: %v", err)
	}
	created, err := s.GetIssue(ctx, template.ID)
	if err != nil {
		s.Close()
		t.Fatalf("GetIssue(template): %v", err)
	}
	if created == nil || !created.IsTemplate {
		s.Close()
		t.Fatalf("expected inserted issue to be IsTemplate=true, got %+v", created)
	}
	_ = s.Close()

	showOut, _ := runBDForCoverage(t, dir, "show", "--allow-stale", template.ID, "--json")
	showPayload := extractJSONPayload(showOut)
	var showDetails []map[string]interface{}
	if err := json.Unmarshal([]byte(showPayload), &showDetails); err != nil {
		t.Fatalf("parse show JSON: %v\n%s", err, showOut)
	}
	if len(showDetails) != 1 {
		t.Fatalf("expected 1 issue from show, got %d", len(showDetails))
	}
	// Re-open the DB after running the CLI to confirm is_template persisted.
	s2, err := sqlite.New(context.Background(), dbFile)
	if err != nil {
		t.Fatalf("sqlite.New (reopen): %v", err)
	}
	postShow, err := s2.GetIssue(context.Background(), template.ID)
	_ = s2.Close()
	if err != nil {
		t.Fatalf("GetIssue(template, post-show): %v", err)
	}
	if postShow == nil || !postShow.IsTemplate {
		t.Fatalf("expected template to remain IsTemplate=true post-show, got %+v", postShow)
	}
	if v, ok := showDetails[0]["is_template"]; ok {
		if b, ok := v.(bool); !ok || !b {
			t.Fatalf("expected show JSON is_template=true, got %v", v)
		}
	} else {
		t.Fatalf("expected show JSON to include is_template=true, got: %s", showOut)
	}

	_, updErr := runBDForCoverage(t, dir, "update", template.ID, "--title", "New title")
	if !strings.Contains(updErr, "cannot update template") {
		t.Fatalf("expected template update to be rejected, stderr: %s", updErr)
	}
	_, closeTemplateErr := runBDForCoverage(t, dir, "close", template.ID, "--reason", "Done")
	if !strings.Contains(closeTemplateErr, "cannot close template") {
		t.Fatalf("expected template close to be rejected, stderr: %s", closeTemplateErr)
	}
}

func TestCoverage_ShowThread(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping CLI coverage test in short mode")
	}

	dir := t.TempDir()
	runBDForCoverage(t, dir, "init", "--prefix", "test", "--quiet")

	dbFile := filepath.Join(dir, ".beads", "beads.db")
	s, err := sqlite.New(context.Background(), dbFile)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	ctx := context.Background()

	root := &types.Issue{Title: "Root message", IssueType: types.TypeMessage, Status: types.StatusOpen, Sender: "alice", Assignee: "bob"}
	reply1 := &types.Issue{Title: "Re: Root", IssueType: types.TypeMessage, Status: types.StatusOpen, Sender: "bob", Assignee: "alice"}
	reply2 := &types.Issue{Title: "Re: Re: Root", IssueType: types.TypeMessage, Status: types.StatusOpen, Sender: "alice", Assignee: "bob"}
	if err := s.CreateIssue(ctx, root, "test-user"); err != nil {
		s.Close()
		t.Fatalf("CreateIssue root: %v", err)
	}
	if err := s.CreateIssue(ctx, reply1, "test-user"); err != nil {
		s.Close()
		t.Fatalf("CreateIssue reply1: %v", err)
	}
	if err := s.CreateIssue(ctx, reply2, "test-user"); err != nil {
		s.Close()
		t.Fatalf("CreateIssue reply2: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{IssueID: reply1.ID, DependsOnID: root.ID, Type: types.DepRepliesTo, ThreadID: root.ID}, "test-user"); err != nil {
		s.Close()
		t.Fatalf("AddDependency reply1->root: %v", err)
	}
	if err := s.AddDependency(ctx, &types.Dependency{IssueID: reply2.ID, DependsOnID: reply1.ID, Type: types.DepRepliesTo, ThreadID: root.ID}, "test-user"); err != nil {
		s.Close()
		t.Fatalf("AddDependency reply2->reply1: %v", err)
	}
	_ = s.Close()

	out, _ := runBDForCoverage(t, dir, "show", "--allow-stale", reply2.ID, "--thread")
	if !strings.Contains(out, "Thread") || !strings.Contains(out, "Total: 3 messages") {
		t.Fatalf("expected thread output, got: %s", out)
	}
	if !strings.Contains(out, root.ID) || !strings.Contains(out, reply1.ID) || !strings.Contains(out, reply2.ID) {
		t.Fatalf("expected thread output to include message IDs, got: %s", out)
	}
}
