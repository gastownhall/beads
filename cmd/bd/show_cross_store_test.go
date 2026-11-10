//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// captureShowRun captures the stdout and stderr of fn while it runs, for show
// runs whose error return is part of what is being asserted (captureStdout
// reports an error return as a test failure, and the JSON modes return one by
// design).
func captureShowRun(t *testing.T, fn func() error) (stdout, stderr string, runErr error) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	var outBuf, errBuf bytes.Buffer
	doneOut, doneErr := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = io.Copy(&outBuf, outR)
		close(doneOut)
	}()
	go func() {
		_, _ = io.Copy(&errBuf, errR)
		close(doneErr)
	}()
	os.Stdout, os.Stderr = outW, errW
	runErr = fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = outW.Close()
	_ = errW.Close()
	<-doneOut
	<-doneErr
	_ = outR.Close()
	_ = errR.Close()
	return outBuf.String(), errBuf.String(), runErr
}

// showCrossStoreTown makes the town/rig directory pair the cross-store show
// tests use: <root>/.beads and <root>/<rig>/.beads, and returns the paths.
func showCrossStoreTown(t *testing.T, rig string) (root, townBeadsDir, rigDir, rigBeadsDir string) {
	t.Helper()
	root = t.TempDir()
	townBeadsDir = filepath.Join(root, ".beads")
	rigDir = filepath.Join(root, rig)
	rigBeadsDir = filepath.Join(rigDir, ".beads")
	for _, d := range []string{townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("create %s: %v", d, err)
		}
	}
	return root, townBeadsDir, rigDir, rigBeadsDir
}

// seatShowAt runs show as if from the rig seat: dbPath, working directory and
// the command's store all point at the rig workspace.
func seatShowAt(t *testing.T, rigDir, rigDBPath string, seatStore storage.DoltStorage) context.Context {
	t.Helper()
	dbPath = rigDBPath
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(rigDir); err != nil {
		t.Fatalf("chdir %s: %v", rigDir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	oldCtx, oldJSON, oldProxied := rootCtx, jsonOutput, proxiedServerMode
	t.Cleanup(func() { rootCtx, jsonOutput, proxiedServerMode = oldCtx, oldJSON, oldProxied })
	ctx := context.Background()
	store, rootCtx, jsonOutput, proxiedServerMode = seatStore, ctx, false, false
	return ctx
}

// TestShowPrefixRoute_RoutedMissKeepsHint pins AC-2's prefix-route half: once
// a configured route sent the lookup to another store, the miss hint must
// not claim no other store was searched — the deletion/history hint is the
// honest one. A routed tier's refusal and a routing failure must not turn
// into a hint either: they answer with "Error fetching", as before.
func TestShowPrefixRoute_RoutedMissKeepsHint(t *testing.T) {
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())
	oldCtx, oldJSON, oldProxied := rootCtx, jsonOutput, proxiedServerMode
	t.Cleanup(func() { rootCtx, jsonOutput, proxiedServerMode = oldCtx, oldJSON, oldProxied })

	ctx := context.Background()
	_, townBeadsDir, rigDir, rigBeadsDir := showCrossStoreTown(t, "openclaw")

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "op")

	rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigBeadsDir, "dolt"), "hq")
	if err := rigStore.CreateIssue(ctx, &types.Issue{
		ID: "hq-pres0", Title: "Rig target", Status: types.StatusOpen,
		Priority: 0, IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create rig issue: %v", err)
	}
	if err := rigStore.Close(); err != nil {
		t.Fatalf("close rig store: %v", err)
	}

	townRoutes := `{"prefix":"hq-","path":"openclaw"}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(townRoutes), 0o644); err != nil {
		t.Fatalf("write town routes.jsonl: %v", err)
	}

	seatShowAt(t, rigDir, townDBPath, townStore)

	// A store was routed to and searched; the miss keeps the
	// deletion/history hint and never claims no other store was searched.
	const missing = "hq-gea4h"
	_, stderr, runErr := captureShowRun(t, func() error {
		return showCmd.RunE(showCmd, []string{missing})
	})
	if runErr == nil {
		t.Fatalf("bd show %s: want the not-found exit, got nil", missing)
	}
	if !strings.Contains(stderr, "Issue "+missing+" not found") {
		t.Errorf("bd show %s: stderr = %q, want the leading not-found line", missing, stderr)
	}
	if !strings.Contains(stderr, "never existed") || strings.Contains(stderr, "does not mean the issue is absent") {
		t.Errorf("bd show %s after a routed store was searched: stderr = %q\n"+
			"want the deletion/history hint, not the limited-search hint that claims no other store was searched", missing, stderr)
	}
}

// TestShowPrefixRoute_RefusalAndFailureAreNotHints pins that a routed tier's
// refusal (an id that abbreviates two issues in the routed store) and a
// routing failure (a route whose target cannot be opened) keep answering
// with "Error fetching" and no miss hint: the refusal proves the routed
// store holds what the id names, and the failure is its own diagnosis.
func TestShowPrefixRoute_RefusalAndFailureAreNotHints(t *testing.T) {
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())
	oldCtx, oldJSON, oldProxied := rootCtx, jsonOutput, proxiedServerMode
	t.Cleanup(func() { rootCtx, jsonOutput, proxiedServerMode = oldCtx, oldJSON, oldProxied })

	ctx := context.Background()
	_, townBeadsDir, rigDir, rigBeadsDir := showCrossStoreTown(t, "openclaw")

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "op")

	rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigBeadsDir, "dolt"), "hq")
	for _, id := range []string{"hq-zza1", "hq-zza2"} {
		if err := rigStore.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "Ambiguous pair " + id, Status: types.StatusOpen,
			Priority: 0, IssueType: types.TypeTask,
		}, "test"); err != nil {
			t.Fatalf("create rig issue %s: %v", id, err)
		}
	}
	if err := rigStore.Close(); err != nil {
		t.Fatalf("close rig store: %v", err)
	}

	townRoutes := `{"prefix":"hq-","path":"openclaw"}` + "\n" + `{"prefix":"gt-","path":"ghost"}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(townRoutes), 0o644); err != nil {
		t.Fatalf("write town routes.jsonl: %v", err)
	}

	seatShowAt(t, rigDir, townDBPath, townStore)

	for _, tc := range []struct {
		id   string
		want string
	}{
		{"hq-zza", "ambiguous issue ID"}, // routed refusal: two issues share the abbreviation
		{"gt-gea4h", "prefix route"},     // routing failure: the route's target cannot be opened
	} {
		_, stderr, runErr := captureShowRun(t, func() error {
			return showCmd.RunE(showCmd, []string{tc.id})
		})
		if runErr == nil {
			t.Errorf("bd show %s: want an exit error, got nil", tc.id)
		}
		if !strings.Contains(stderr, "Error fetching "+tc.id) || !strings.Contains(stderr, tc.want) {
			t.Errorf("bd show %s: stderr = %q\nwant 'Error fetching' with %q and no miss hint", tc.id, stderr, tc.want)
		}
		if strings.Contains(stderr, "Hint:") {
			t.Errorf("bd show %s: a routed refusal/failure turned into a miss hint;\nstderr: %s", tc.id, stderr)
		}
	}
}

// TestShowAutoRoute_OwnerSearchedKeepsHint pins AC-2's auto-route half, the
// gap PR #8144 left: when auto-routing's target was searched and missed, the
// foreign-prefix hint must not claim no other store was. Every kind of
// routed engagement suppresses it — a target that issues the requested
// prefix, an unrelated-prefix target, and a refusal the auto tier answers —
// and an auto-route that finds the issue shows it with no miss diagnostics
// at all.
func TestShowAutoRoute_OwnerSearchedKeepsHint(t *testing.T) {
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())
	oldCtx, oldJSON, oldProxied := rootCtx, jsonOutput, proxiedServerMode
	t.Cleanup(func() { rootCtx, jsonOutput, proxiedServerMode = oldCtx, oldJSON, oldProxied })

	ctx := context.Background()
	root, townBeadsDir, rigDir, _ := showCrossStoreTown(t, "openclaw")

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "op")
	if err := townStore.SetConfig(ctx, "routing.default", filepath.Join(root, "planning")); err != nil {
		t.Fatalf("set routing.default: %v", err)
	}

	planBeadsDir := filepath.Join(root, "planning", ".beads")
	if err := os.MkdirAll(planBeadsDir, 0o755); err != nil {
		t.Fatalf("create planning beads dir: %v", err)
	}
	planStore := newTestStoreIsolatedDB(t, filepath.Join(planBeadsDir, "dolt"), "hq")
	for _, id := range []string{"hq-pres0", "hq-zza1"} {
		if err := planStore.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "Planned " + id, Status: types.StatusOpen,
			Priority: 0, IssueType: types.TypeTask,
		}, "test"); err != nil {
			t.Fatalf("create planning issue %s: %v", id, err)
		}
	}
	if err := planStore.Close(); err != nil {
		t.Fatalf("close planning store: %v", err)
	}

	seatShowAt(t, rigDir, townDBPath, townStore)

	// A miss the auto-routed target was searched for: the deletion/history
	// hint stays, whatever prefix the target or the id carries.
	for _, id := range []string{"hq-gea4h", "foo-gea4h"} {
		_, stderr, runErr := captureShowRun(t, func() error {
			return showCmd.RunE(showCmd, []string{id})
		})
		if runErr == nil {
			t.Fatalf("bd show %s: want the not-found exit, got nil", id)
		}
		if !strings.Contains(stderr, "Issue "+id+" not found") {
			t.Errorf("bd show %s: stderr = %q, want the leading not-found line", id, stderr)
		}
		if !strings.Contains(stderr, "never existed") || strings.Contains(stderr, "does not mean the issue is absent") {
			t.Errorf("bd show %s after the auto-routed store was searched: stderr = %q\n"+
				"want the deletion/history hint, not the limited-search hint that claims no other store was searched", id, stderr)
		}
	}

	// An auto-route that finds the issue shows it, with no miss diagnostics.
	const found = "hq-pres"
	stdout, stderr, runErr := captureShowRun(t, func() error {
		return showCmd.RunE(showCmd, []string{found})
	})
	if runErr != nil {
		t.Fatalf("bd show %s: %v\nstdout: %s\nstderr: %s", found, runErr, stdout, stderr)
	}
	if !strings.Contains(stdout, "hq-pres0") {
		t.Errorf("bd show %s did not render the auto-routed issue;\nstdout: %s", found, stdout)
	}
	if strings.Contains(stderr, "not found") || strings.Contains(stderr, "Hint:") {
		t.Errorf("bd show %s printed a miss diagnostic on an auto-routed success;\nstderr: %s", found, stderr)
	}
}

// TestShowAutoRoute_RefusalThenAutoSuccess pins the refusal ordering within
// AC-2: a prefix tier that refuses the id (it abbreviates two issues in the
// routed rig) does not win when the auto-routed store resolves it — and
// neither path prints a miss hint.
func TestShowAutoRoute_RefusalThenAutoSuccess(t *testing.T) {
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())
	oldCtx, oldJSON, oldProxied := rootCtx, jsonOutput, proxiedServerMode
	t.Cleanup(func() { rootCtx, jsonOutput, proxiedServerMode = oldCtx, oldJSON, oldProxied })

	ctx := context.Background()
	root, townBeadsDir, rigDir, rigBeadsDir := showCrossStoreTown(t, "openclaw")

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "op")
	if err := townStore.SetConfig(ctx, "routing.default", filepath.Join(root, "planning")); err != nil {
		t.Fatalf("set routing.default: %v", err)
	}

	rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigBeadsDir, "dolt"), "hq")
	for _, id := range []string{"hq-zza1", "hq-zza2"} {
		if err := rigStore.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "Ambiguous pair " + id, Status: types.StatusOpen,
			Priority: 0, IssueType: types.TypeTask,
		}, "test"); err != nil {
			t.Fatalf("create rig issue %s: %v", id, err)
		}
	}
	if err := rigStore.Close(); err != nil {
		t.Fatalf("close rig store: %v", err)
	}

	planBeadsDir := filepath.Join(root, "planning", ".beads")
	if err := os.MkdirAll(planBeadsDir, 0o755); err != nil {
		t.Fatalf("create planning beads dir: %v", err)
	}
	planStore := newTestStoreIsolatedDB(t, filepath.Join(planBeadsDir, "dolt"), "hq")
	if err := planStore.CreateIssue(ctx, &types.Issue{
		ID: "hq-zza1", Title: "Unambiguous in planning", Status: types.StatusOpen,
		Priority: 0, IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create planning issue: %v", err)
	}
	if err := planStore.Close(); err != nil {
		t.Fatalf("close planning store: %v", err)
	}

	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"),
		[]byte(`{"prefix":"hq-","path":"openclaw"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write town routes.jsonl: %v", err)
	}

	seatShowAt(t, rigDir, townDBPath, townStore)

	const want = "hq-zza"
	stdout, stderr, runErr := captureShowRun(t, func() error {
		return showCmd.RunE(showCmd, []string{want})
	})
	if runErr != nil {
		t.Fatalf("bd show %s: %v\nstdout: %s\nstderr: %s", want, runErr, stdout, stderr)
	}
	if !strings.Contains(stdout, "hq-zza1") {
		t.Errorf("bd show %s did not render the auto-routed resolution of the refused id;\nstdout: %s", want, stdout)
	}
	if strings.Contains(stderr, "not found") || strings.Contains(stderr, "Hint:") {
		t.Errorf("bd show %s printed a miss diagnostic where the auto route succeeded;\nstderr: %s", want, stderr)
	}
}

// TestShowPrefixRoute_RigSeatReachesTownStore pins the routes.jsonl recipe
// GH#5259's design documents: a rig workspace reaches the town store beside
// it with {"prefix":"hq-","path":".."} in its own routes.jsonl, and bd show
// resolves the town's issue read-only from the rig seat. The route already
// works at the base of this change; this test guards it while the miss hint
// is added around it (AC-4).
func TestShowPrefixRoute_RigSeatReachesTownStore(t *testing.T) {
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())

	ctx := context.Background()
	_, townBeadsDir, rigDir, rigBeadsDir := showCrossStoreTown(t, "openclaw")

	const townID = "hq-gea4h"
	townStore := newTestStoreIsolatedDB(t, filepath.Join(townBeadsDir, "dolt"), "hq")
	if err := townStore.CreateIssue(ctx, &types.Issue{
		ID: townID, Title: "Incident record", Status: types.StatusOpen,
		Priority: 0, IssueType: types.TypeBug,
	}, "test"); err != nil {
		t.Fatalf("create town issue: %v", err)
	}
	if err := townStore.Close(); err != nil {
		t.Fatalf("close town store: %v", err)
	}

	rigDBPath := filepath.Join(rigBeadsDir, "dolt")
	rigStore := newTestStoreIsolatedDB(t, rigDBPath, "op")

	rigRoutes := `{"prefix":"hq-","path":".."}` + "\n"
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "routes.jsonl"), []byte(rigRoutes), 0o644); err != nil {
		t.Fatalf("write rig routes.jsonl: %v", err)
	}

	seatShowAt(t, rigDir, rigDBPath, rigStore)
	stdout, stderr, runErr := captureShowRun(t, func() error {
		return showCmd.RunE(showCmd, []string{townID})
	})
	if runErr != nil {
		t.Fatalf("bd show %s from the rig seat: %v\nstdout: %s\nstderr: %s", townID, runErr, stdout, stderr)
	}
	if !strings.Contains(stdout, townID) || !strings.Contains(stdout, "Incident record") {
		t.Errorf("bd show %s from the rig seat did not render the town issue;\nstdout: %s", townID, stdout)
	}
	if strings.Contains(stderr, "not found") || strings.Contains(stderr, "Hint:") {
		t.Errorf("bd show %s from the rig seat printed a miss diagnostic on a routed success;\nstderr: %s", townID, stderr)
	}
}

// TestShowForeignPrefixMiss_JSONCompatibility pins AC-6: in --json mode the
// miss hint goes to stderr only, and every mode's stdout payload (the error
// envelope for an all-missing direct batch, the details array for a mixed
// batch, the refs/children maps) and exit behavior stay what they were.
func TestShowForeignPrefixMiss_JSONCompatibility(t *testing.T) {
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())
	oldCtx, oldJSON, oldProxied := rootCtx, jsonOutput, proxiedServerMode
	t.Cleanup(func() { rootCtx, jsonOutput, proxiedServerMode = oldCtx, oldJSON, oldProxied })

	ctx := context.Background()
	_, townBeadsDir, rigDir, _ := showCrossStoreTown(t, "openclaw")

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "op")
	const localID = "op-zz95"
	if err := townStore.CreateIssue(ctx, &types.Issue{
		ID: localID, Title: "Local sibling", Status: types.StatusOpen,
		Priority: 0, IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create local issue: %v", err)
	}

	seatShowAt(t, rigDir, townDBPath, townStore)
	jsonOutput = true

	const foreign = "hq-gea4h"

	// Direct mode, every id missing: the stdout envelope stays exactly the
	// existing one; the limited-search hint lands on stderr only.
	stdout, stderr, runErr := captureShowRun(t, func() error {
		return showCmd.RunE(showCmd, []string{foreign})
	})
	if runErr == nil {
		t.Fatalf("bd show --json %s: want the not-found exit, got nil", foreign)
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("bd show --json %s: stdout = %q, want the existing JSON error envelope: %v", foreign, stdout, err)
	}
	if msg, _ := envelope["error"].(string); msg != "no issues found matching the provided IDs" {
		t.Errorf("bd show --json %s: envelope error = %q, want the existing message untouched", foreign, msg)
	}
	if hint, _ := envelope["hint"].(string); !strings.Contains(hint, "deleted") {
		t.Errorf("bd show --json %s: envelope hint = %q, want the existing deletion/history hint untouched", foreign, hint)
	}
	if strings.Contains(stdout, "is not among this store's configured prefixes") {
		t.Errorf("bd show --json %s: the limited-search hint leaked into the stdout envelope;\nstdout: %s", foreign, stdout)
	}
	if !strings.Contains(stderr, "Issue "+foreign+" not found") || !strings.Contains(stderr, "is not among this store's configured prefixes") {
		t.Errorf("bd show --json %s: stderr = %q, want the not-found line and the limited-search hint", foreign, stderr)
	}

	// Direct mode, mixed batch: stdout is the details array of what was
	// found; the miss is explained on stderr.
	stdout, stderr, runErr = captureShowRun(t, func() error {
		return showCmd.RunE(showCmd, []string{localID, foreign})
	})
	if runErr != nil {
		t.Fatalf("bd show --json %s %s: %v\nstdout: %s\nstderr: %s", localID, foreign, runErr, stdout, stderr)
	}
	var details []map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &details); err != nil {
		t.Fatalf("bd show --json %s %s: stdout = %q, want the found issue's details array: %v", localID, foreign, stdout, err)
	}
	if len(details) != 1 || details[0]["id"] != localID {
		t.Errorf("bd show --json %s %s: stdout payload = %s, want exactly %s's details", localID, foreign, stdout, localID)
	}
	if !strings.Contains(stderr, "Issue "+foreign+" not found") || !strings.Contains(stderr, "is not among this store's configured prefixes") {
		t.Errorf("bd show --json %s %s: stderr = %q, want the miss explained on stderr", localID, foreign, stderr)
	}

	// Refs and children: the stdout maps and the success exit stay what they
	// were; only qualifying miss diagnostics on stderr gain the hint.
	for _, run := range []struct {
		name string
		fn   func() error
	}{
		{"refs", func() error { return showIssueRefs(ctx, []string{foreign}, true) }},
		{"children", func() error { return showIssueChildren(ctx, []string{foreign}, true, false) }},
	} {
		stdout, stderr, runErr = captureShowRun(t, run.fn)
		if runErr != nil {
			t.Errorf("bd show --%s --json %s: %v, want the existing success exit for an all-missing batch", run.name, foreign, runErr)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Errorf("bd show --%s --json %s: stdout = %q, want the existing JSON map: %v", run.name, foreign, stdout, err)
			continue
		}
		if len(payload) != 1 || payload["schema_version"] != float64(JSONSchemaVersion) {
			t.Errorf("bd show --%s --json %s: stdout payload = %s, want the existing schema_version-only map untouched", run.name, foreign, stdout)
		}
		if !strings.Contains(stderr, "Error resolving "+foreign) || !strings.Contains(stderr, "is not among this store's configured prefixes") {
			t.Errorf("bd show --%s --json %s: stderr = %q, want the existing resolve error plus the limited-search hint", run.name, foreign, stderr)
		}
	}
}
