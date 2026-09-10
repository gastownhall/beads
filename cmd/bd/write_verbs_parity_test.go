//go:build cgo

// Characterization ("parity") suite for the four write verbs — bd create,
// bd update, bd close, bd reopen — pinning the observable CLI contract as it
// stands BEFORE cmd/bd is rewired onto the issue-operations facade
// (internal/storage/issueops).
//
// Everything here is a statement about what the CLI does TODAY, not about what
// it ought to do. Several pinned behaviors are known bugs; four of them have
// already been adjudicated and WILL change during the rewire:
//
//	R1  bd create --id <occupied>  -> silent full-row upsert reporting success
//	                                  [FLIPPED to a refusal by the bd create rewire]
//	R2  compound bd update         -> one store call (= one hook firing) per
//	                                  field/label/parent edit, plus phantom
//	                                  label_added/label_removed events
//	                                  [FLIPPED to one atomic op by the bd update
//	                                  rewire]
//	R3  bd update --parent         -> removes only the FIRST parent edge
//	                                  [FLIPPED to replace-all by the bd update
//	                                  rewire]
//	R4  bd reopen on a non-done,
//	    non-open status            -> prints "↻ Reopened" and reports success
//	                                  [FLIPPED to "nothing to do" by the bd
//	                                  reopen rewire]
//
// Assertions covering those four are tagged `RULING Rn`. When a rewire commit
// flips one, the assertion is updated IN THAT COMMIT with a comment naming the
// ruling. Any OTHER assertion in this file changing is a regression, not a
// refactor. All four have now landed; every remaining assertion is unchanged
// from the pre-rewire CLI and must stay that way.
//
// Harness: the commands' RunE functions are invoked in-process against a real
// storage.DoltStorage, with stdout/stderr captured and the returned error
// mapped to the exit code main.go would produce. The store is wrapped in
// parityStore, a counting decorator shaped exactly like
// storage.HookFiringStore (embed + Unwrap), so mutation-call counts stand in
// for hook-firing counts: HookFiringStore fires exactly one hook per mutating
// store call, so "N store calls" is "N hook firings".

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// ===== counting store decorator =====

// parityStore counts the mutating store calls a command makes. It mirrors
// storage.HookFiringStore's shape (embedded interface for passthrough, inner
// for the real calls, Unwrap for storage.UnwrapStore) so the counts equal the
// number of hooks production would fire for the same command.
type parityStore struct {
	storage.DoltStorage
	inner storage.DoltStorage

	mu    sync.Mutex
	calls []string
}

func newParityStore(inner storage.DoltStorage) *parityStore {
	return &parityStore{DoltStorage: inner, inner: inner}
}

func (p *parityStore) Unwrap() storage.DoltStorage { return p.inner }

func (p *parityStore) record(name string) {
	p.mu.Lock()
	p.calls = append(p.calls, name)
	p.mu.Unlock()
}

func (p *parityStore) mutations() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func (p *parityStore) reset() {
	p.mu.Lock()
	p.calls = nil
	p.mu.Unlock()
}

func (p *parityStore) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	p.record("CreateIssue")
	return p.inner.CreateIssue(ctx, issue, actor)
}

func (p *parityStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	p.record("UpdateIssue")
	return p.inner.UpdateIssue(ctx, id, updates, actor)
}

func (p *parityStore) UpdateIssueChecked(ctx context.Context, id string, updates map[string]interface{}, actor string, opts storage.UpdateIssueOptions) error {
	p.record("UpdateIssueChecked")
	return p.inner.UpdateIssueChecked(ctx, id, updates, actor, opts)
}

func (p *parityStore) UpdateIssueType(ctx context.Context, id, issueType, actor string) error {
	p.record("UpdateIssueType")
	return p.inner.UpdateIssueType(ctx, id, issueType, actor)
}

func (p *parityStore) ClaimIssue(ctx context.Context, id, actor string) error {
	p.record("ClaimIssue")
	return p.inner.ClaimIssue(ctx, id, actor)
}

func (p *parityStore) ReopenIssue(ctx context.Context, id, reason, actor string) error {
	p.record("ReopenIssue")
	return p.inner.ReopenIssue(ctx, id, reason, actor)
}

func (p *parityStore) CloseIssue(ctx context.Context, id, reason, actor, session string) error {
	p.record("CloseIssue")
	return p.inner.CloseIssue(ctx, id, reason, actor, session)
}

func (p *parityStore) CloseIssueChecked(ctx context.Context, id, actor string, opts storage.CloseIssueOptions) (storage.CloseIssueResult, error) {
	p.record("CloseIssueChecked")
	return p.inner.CloseIssueChecked(ctx, id, actor, opts)
}

func (p *parityStore) AddLabel(ctx context.Context, issueID, label, actor string) error {
	p.record("AddLabel")
	return p.inner.AddLabel(ctx, issueID, label, actor)
}

func (p *parityStore) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	p.record("RemoveLabel")
	return p.inner.RemoveLabel(ctx, issueID, label, actor)
}

func (p *parityStore) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	p.record("AddDependency")
	return p.inner.AddDependency(ctx, dep, actor)
}

func (p *parityStore) RemoveDependency(ctx context.Context, issueID, dependsOnID, actor string) error {
	p.record("RemoveDependency")
	return p.inner.RemoveDependency(ctx, issueID, dependsOnID, actor)
}

// ===== counting issue-operations facade =====

// parityOps is parityStore's counterpart on the issue-operations facade, the
// surface the write verbs move onto. It records into the SAME call list, so
// `mutations()` stays one entry per hook firing however the verb reached the
// database — a verb still on a direct store call records "UpdateIssue", a
// rewired verb records "Update".
//
// The firing rules mirror beads.hookIssueOperations exactly, which is what
// makes the counts comparable across the rewire: Create, Update and Close fire
// their completion hook on any success; Reopen fires only when it changed
// something.
//
// A facade is required here because the lifecycle accessor never routes
// through the DoltStorage methods parityStore decorates — it builds operations
// straight off the concrete store — so once a verb is rewired, store-level
// counting goes blind and every "no store mutations" assertion would pass
// vacuously.
type parityOps struct {
	inner issueops.Lifecycle
	store *parityStore
}

func (o *parityOps) Create(ctx context.Context, request issueops.CreateRequest) (issueops.CreateResult, error) {
	result, err := o.inner.Create(ctx, request)
	if err == nil {
		o.store.record("Create")
	}
	return result, err
}

func (o *parityOps) Update(ctx context.Context, request issueops.UpdateRequest) (issueops.UpdateResult, error) {
	result, err := o.inner.Update(ctx, request)
	if err == nil {
		o.store.record("Update")
	}
	return result, err
}

func (o *parityOps) Close(ctx context.Context, request issueops.CloseRequest) (issueops.CloseResult, error) {
	result, err := o.inner.Close(ctx, request)
	if err == nil {
		o.store.record("Close")
	}
	return result, err
}

func (o *parityOps) Reopen(ctx context.Context, request issueops.ReopenRequest) (issueops.ReopenResult, error) {
	result, err := o.inner.Reopen(ctx, request)
	if err == nil && result.Changed {
		o.store.record("Reopen")
	}
	return result, err
}

// ===== harness =====

type parityEnv struct {
	t        *testing.T
	store    *parityStore
	beadsDir string
}

// newParityEnv wires the package globals the write-verb RunE functions read
// (store, rootCtx, actor, jsonOutput, quietFlag, readonlyMode) at a real Dolt
// store, points BEADS_DIR at its .beads dir so SetLastTouchedID is observable,
// and restores everything on cleanup.
//
// quietFlag is set so `bd create`'s random maybeShowTip line cannot appear on
// stdout mid-assertion. It does NOT suppress the "✓ Created issue:" lines:
// those go through debug.PrintNormal, which reads the debug package's own
// quiet flag (set by main.go's PersistentPreRun, which RunE-level tests skip).
// parityOwnerEmail pins the git identity create derives its owner field from.
const parityOwnerEmail = "parity-owner@test"

func newParityEnv(t *testing.T) *parityEnv {
	t.Helper()

	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)
	initConfigForTest(t)

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	raw := newTestStore(t, filepath.Join(beadsDir, "beads.db"))
	ps := newParityStore(raw)

	savedCtx, savedJSON, savedActor := rootCtx, jsonOutput, actor
	savedQuiet, savedReadonly := quietFlag, readonlyMode
	savedNewOps := newIssueOperations
	t.Cleanup(func() {
		rootCtx, jsonOutput, actor = savedCtx, savedJSON, savedActor
		quietFlag, readonlyMode = savedQuiet, savedReadonly
		newIssueOperations = savedNewOps
	})

	// Count the facade operations the write verbs perform. The real store is
	// unwrapped first so the counted lifecycle is the concrete store's,
	// whatever parityStore's own passthrough happens to promote.
	newIssueOperations = func(target beads.Storage) (issueops.Lifecycle, error) {
		if decorated, ok := target.(storage.DoltStorage); ok {
			target = storage.UnwrapStore(decorated)
		}
		inner, err := target.IssueLifecycle()
		if err != nil {
			return nil, err
		}
		return &parityOps{inner: inner, store: ps}, nil
	}

	store = ps
	rootCtx = context.Background()
	jsonOutput = false
	actor = "parity-actor"
	quietFlag = true
	readonlyMode = false

	t.Setenv("NO_COLOR", "1")
	// getOwner reads GIT_AUTHOR_EMAIL, then falls back to git config user.email, so
	// create's owner field — and therefore its --json key set — otherwise varies with
	// the ambient git identity. Pin it: CI commonly sets GIT_AUTHOR_EMAIL, a developer
	// shell usually does not, and the suite must render the same verdict in both.
	t.Setenv("GIT_AUTHOR_EMAIL", parityOwnerEmail)
	t.Setenv("BEADS_DIR", beadsDir)

	// Pin every config key the write verbs read. config.Initialize() merges
	// whatever config.yaml files happen to exist under the test HOME, and
	// other tests in this package mutate these keys globally — an inherited
	// value would either break these tests or, worse, silently weaken them
	// (output.title-length=0 makes formatFeedbackID drop the title, which
	// would turn the exact-line assertions into vacuous id-only comparisons).
	setParityConfig(t, map[string]any{
		"issue-prefix":               "", // fall through to the store's "test"
		"output.title-length":        255,
		"create.require-description": false,
		"validation.on-create":       "",
		"validation.on-close":        "",
		"routing.mode":               "",
		"routing.default":            "",
		"routing.maintainer":         "",
		"routing.contributor":        "",
	})

	// Styling must be inert or the exact-line assertions below are comparing
	// against ANSI-wrapped text. Fail loudly rather than silently mismatch.
	if got := ui.RenderPass("✓"); got != "✓" {
		t.Fatalf("parity harness requires unstyled output; ui.RenderPass(\"✓\") = %q", got)
	}
	// Guard the pin above: the human-output assertions are only meaningful
	// while formatFeedbackID actually interpolates the title.
	if got := formatFeedbackID("x-1", "T"); got != "x-1 — T" {
		t.Fatalf("parity harness requires title interpolation; formatFeedbackID = %q", got)
	}

	env := &parityEnv{t: t, store: ps, beadsDir: beadsDir}
	env.clearLastTouched()
	return env
}

// setParityConfig overrides config keys for the duration of the test. The
// enclosing initConfigForTest already registers config.ResetForTesting as
// cleanup, which drops these along with the rest of the viper state.
func setParityConfig(t *testing.T, kv map[string]any) {
	t.Helper()
	for key, value := range kv {
		config.Set(key, value)
	}
}

func (e *parityEnv) clearLastTouched() {
	e.t.Helper()
	_ = os.Remove(filepath.Join(e.beadsDir, lastTouchedFile))
}

func (e *parityEnv) lastTouched() string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.beadsDir, lastTouchedFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// runResult is everything a shell would observe from one command invocation.
type runResult struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

// run invokes cmd.RunE directly (as the other RunE-level tests in this package
// do), capturing both streams and mapping the returned error to the exit code
// main.go's run() would produce for it.
func (e *parityEnv) run(cmd *cobra.Command, args ...string) runResult {
	e.t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		e.t.Fatalf("os.Pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		e.t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW

	var outBuf, errBuf strings.Builder
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, errR) }()

	runErr := cmd.RunE(cmd, args)

	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	wg.Wait()
	_ = outR.Close()
	_ = errR.Close()

	return runResult{
		stdout:   outBuf.String(),
		stderr:   errBuf.String(),
		exitCode: parityExitCode(runErr),
		err:      runErr,
	}
}

// parityExitCode mirrors main.go's error→exit-status mapping.
func parityExitCode(err error) int {
	if err == nil {
		return 0
	}
	if code, ok := exitCodeFromError(err); ok {
		return code
	}
	return 1
}

// setFlags sets flags on cmd and registers a cleanup that returns every flag
// on the command to its declared default. The command objects are package
// globals shared by the whole test binary, so leaking a set flag silently
// corrupts unrelated tests.
func (e *parityEnv) setFlags(cmd *cobra.Command, kv map[string]string) {
	e.t.Helper()
	e.t.Cleanup(func() { resetCommandFlagsToDefaults(cmd) })
	for name, value := range kv {
		if err := cmd.Flags().Set(name, value); err != nil {
			e.t.Fatalf("set --%s=%q: %v", name, value, err)
		}
	}
}

func resetCommandFlagsToDefaults(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		switch v := f.Value.(type) {
		case *closeReasonFlagValue:
			// Accumulating flag: Set appends, so it cannot be reset by Set.
			v.values = nil
		case pflag.SliceValue:
			_ = v.Replace(nil)
		default:
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	})
}

// seed creates an issue directly through the store (bypassing the CLI) so
// fixtures never depend on the behavior under test.
func (e *parityEnv) seed(id, title string, mutate func(*types.Issue)) *types.Issue {
	e.t.Helper()
	issue := &types.Issue{
		ID:        id,
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedBy: "parity-seed",
	}
	if mutate != nil {
		mutate(issue)
	}
	if err := e.store.inner.CreateIssue(rootCtx, issue, "parity-seed"); err != nil {
		e.t.Fatalf("seed %s: %v", id, err)
	}
	e.store.reset()
	return issue
}

func (e *parityEnv) get(id string) *types.Issue {
	e.t.Helper()
	issue, err := e.store.inner.GetIssue(rootCtx, id)
	if err != nil {
		e.t.Fatalf("GetIssue(%s): %v", id, err)
	}
	return issue
}

func (e *parityEnv) eventTypes(id string) []string {
	e.t.Helper()
	events, err := e.store.inner.GetEvents(rootCtx, id, 0)
	if err != nil {
		e.t.Fatalf("GetEvents(%s): %v", id, err)
	}
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, string(ev.EventType))
	}
	return out
}

func countOf(values []string, want string) int {
	n := 0
	for _, v := range values {
		if v == want {
			n++
		}
	}
	return n
}

// decodeJSONObject asserts stdout is exactly one pretty-printed JSON object
// and returns it as a map (key order is not preserved; use rawJSONKeyOrder for
// that).
func decodeJSONObject(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var obj map[string]any
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("stdout is not a single JSON object: %v\nstdout:\n%s", err, stdout)
	}
	if rest, _ := io.ReadAll(dec.Buffered()); strings.TrimSpace(string(rest)) != "" {
		t.Fatalf("trailing content after JSON object: %q\nstdout:\n%s", rest, stdout)
	}
	return obj
}

func decodeJSONArray(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var arr []map[string]any
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&arr); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\nstdout:\n%s", err, stdout)
	}
	return arr
}

func parityJSONKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Sorted so the failure message is stable and diffable.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func assertKeySet(t *testing.T, obj map[string]any, want []string) {
	t.Helper()
	got := parityJSONKeys(obj)
	wantSorted := append([]string(nil), want...)
	for i := 1; i < len(wantSorted); i++ {
		for j := i; j > 0 && wantSorted[j] < wantSorted[j-1]; j-- {
			wantSorted[j], wantSorted[j-1] = wantSorted[j-1], wantSorted[j]
		}
	}
	if strings.Join(got, ",") != strings.Join(wantSorted, ",") {
		t.Errorf("JSON key set mismatch\n got: %v\nwant: %v", got, wantSorted)
	}
}

// rawJSONKeyOrder returns the top-level keys of a JSON object in the order
// they appear in the byte stream. Key ORDER is part of the byte shape: an
// object built by re-marshaling a map has sorted keys, one marshaled straight
// from a struct has struct-field order.
func rawJSONKeyOrder(t *testing.T, raw string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("read JSON: %v", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		t.Fatalf("expected object, got %v", tok)
	}
	var keys []string
	depth := 0
	for dec.More() || depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("read JSON: %v", err)
		}
		if delim, ok := tok.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth < 0 {
					return keys
				}
			}
			continue
		}
		if depth == 0 {
			key, ok := tok.(string)
			if !ok {
				t.Fatalf("expected object key, got %T %v", tok, tok)
			}
			keys = append(keys, key)
			// Consume the value.
			vtok, err := dec.Token()
			if err != nil {
				t.Fatalf("read JSON value: %v", err)
			}
			if delim, ok := vtok.(json.Delim); ok && (delim == '{' || delim == '[') {
				depth++
			}
		}
	}
	return keys
}

func assertRecentUTCTimestamp(t *testing.T, label, value string) {
	t.Helper()
	if !strings.HasSuffix(value, "Z") {
		t.Errorf("%s = %q; want a UTC (Z-suffixed) timestamp", label, value)
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("%s = %q is not RFC3339Nano: %v", label, value, err)
	}
	if ts.IsZero() {
		t.Errorf("%s is the zero time", label)
	}
	if delta := time.Since(ts); delta < -time.Minute || delta > 10*time.Minute {
		t.Errorf("%s = %q is %v away from now; want a freshly stamped time", label, value, delta)
	}
}

// ===== bd create =====

// TestParityCreateJSONShape pins `bd create --json`: a SINGLE pretty-printed
// JSON object (not an array), emitted from the LOCAL *types.Issue the CLI
// built — never a re-read — carrying a synthesized "schema_version" key and,
// because outputJSON round-trips a non-slice through map[string]any, SORTED
// key order.
//
// Source of truth: cmd/bd/create.go:601-604 (outputJSON(issue)) over the
// struct from cmd/bd/create.go:521 (buildCreateIssue), via
// cmd/bd/output.go:68-99 (wrapWithSchemaVersion).
func TestParityCreateJSONShape(t *testing.T) {
	env := newParityEnv(t)
	jsonOutput = true

	env.setFlags(createCmd, map[string]string{
		"description": "parity body",
		"priority":    "1",
		"type":        "bug",
		"assignee":    "someone",
		"labels":      "alpha,beta",
	})

	res := env.run(createCmd, "Create json shape")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	obj := decodeJSONObject(t, res.stdout)

	// The exact key set. `dependencies` and `comments` are absent because the
	// local struct never carries them — create does not re-read.
	assertKeySet(t, obj, []string{
		"id", "title", "description", "status", "priority", "issue_type",
		"assignee", "created_at", "created_by", "updated_at", "labels",
		"owner", "schema_version",
	})
	if obj["owner"] != parityOwnerEmail {
		t.Errorf("owner = %v, want %q from the git identity", obj["owner"], parityOwnerEmail)
	}
	if _, ok := obj["dependencies"]; ok {
		t.Error("create --json emitted a `dependencies` key; today it prints a local struct that has none")
	}
	if _, ok := obj["comments"]; ok {
		t.Error("create --json emitted a `comments` key; today it prints a local struct that has none")
	}
	if obj["schema_version"] != float64(JSONSchemaVersion) {
		t.Errorf("schema_version = %v, want %d", obj["schema_version"], JSONSchemaVersion)
	}

	// Byte shape: two-space indent, trailing newline, sorted top-level keys.
	if !strings.HasPrefix(res.stdout, "{\n  \"") {
		t.Errorf("create --json is not 2-space-indented: %q", firstLine(res.stdout, 2))
	}
	if !strings.HasSuffix(res.stdout, "}\n") {
		t.Errorf("create --json does not end with \"}\\n\": %q", res.stdout[max(0, len(res.stdout)-8):])
	}
	order := rawJSONKeyOrder(t, res.stdout)
	if !isSortedStrings(order) {
		t.Errorf("create --json key order = %v; today it is alphabetically sorted (map round-trip in wrapWithSchemaVersion)", order)
	}

	// Field values come from the local struct, so the CLI-supplied values —
	// not DB-normalized ones — are what get printed.
	if obj["priority"] != float64(1) {
		t.Errorf("priority = %v, want 1", obj["priority"])
	}
	if obj["issue_type"] != "bug" {
		t.Errorf("issue_type = %v, want bug", obj["issue_type"])
	}
	if obj["status"] != string(types.StatusOpen) {
		t.Errorf("status = %v, want %q", obj["status"], types.StatusOpen)
	}
	labels, _ := obj["labels"].([]any)
	if len(labels) != 2 || labels[0] != "alpha" || labels[1] != "beta" {
		t.Errorf("labels = %v, want [alpha beta]", obj["labels"])
	}
}

// TestParityCreateJSONTimestamps pins that `bd create --json` DOES emit
// populated created_at/updated_at (and no closed_at for an open issue).
//
// They are populated by a side effect: the storage layer's
// issueops.PrepareIssueForInsert (internal/storage/issueops/create.go:474-485)
// mutates the very *types.Issue pointer the CLI later prints, stamping
// time.Now().UTC(). So the values are Go-side wall-clock UTC at
// nanosecond precision, NOT the DB's stored representation. Emitting zero
// timestamps here is the predicted failure mode of the facade rewire.
func TestParityCreateJSONTimestamps(t *testing.T) {
	env := newParityEnv(t)
	jsonOutput = true

	env.setFlags(createCmd, map[string]string{"description": "timestamps"})
	res := env.run(createCmd, "Create timestamps")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	obj := decodeJSONObject(t, res.stdout)
	createdAt, _ := obj["created_at"].(string)
	updatedAt, _ := obj["updated_at"].(string)
	if createdAt == "" || updatedAt == "" {
		t.Fatalf("created_at=%q updated_at=%q; both must be populated", createdAt, updatedAt)
	}
	assertRecentUTCTimestamp(t, "created_at", createdAt)
	assertRecentUTCTimestamp(t, "updated_at", updatedAt)
	if _, ok := obj["closed_at"]; ok {
		t.Error("create --json emitted closed_at for an open issue")
	}
}

// TestParityCreateSilent pins `bd create --silent`: stdout is exactly the new
// ID plus a newline, and nothing else. Source: cmd/bd/create.go:605-606.
func TestParityCreateSilent(t *testing.T) {
	env := newParityEnv(t)

	env.setFlags(createCmd, map[string]string{
		"silent":      "true",
		"description": "silent body",
	})

	res := env.run(createCmd, "Create silent")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	id := strings.TrimSpace(res.stdout)
	if id == "" {
		t.Fatal("no ID on stdout")
	}
	if want := id + "\n"; res.stdout != want {
		t.Errorf("stdout = %q, want %q", res.stdout, want)
	}
	env.get(id) // must exist
}

// TestParityCreateHumanOutput pins the three-line human report, byte for byte.
// Source: cmd/bd/create.go:607-613.
func TestParityCreateHumanOutput(t *testing.T) {
	env := newParityEnv(t)

	env.setFlags(createCmd, map[string]string{"description": "human body", "priority": "3"})
	title := "Create human output"
	res := env.run(createCmd, title)
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	id := env.lastTouched()
	if id == "" {
		t.Fatal("no last-touched ID recorded; cannot resolve created ID")
	}
	want := fmt.Sprintf("✓ Created issue: %s\n  Priority: P3\n  Status: open\n", formatFeedbackID(id, title))
	if res.stdout != want {
		t.Errorf("stdout mismatch\n got: %q\nwant: %q", res.stdout, want)
	}
	if res.stderr != "" {
		t.Errorf("stderr = %q, want empty on the happy path", res.stderr)
	}
	if !strings.HasPrefix(res.stdout, "✓ Created issue: ") {
		t.Errorf("create's success line no longer starts with %q", "✓ Created issue: ")
	}
}

// TestParityCreateSetsLastTouched pins cmd/bd/create.go:615 — a successful
// create records the new ID as last-touched.
func TestParityCreateSetsLastTouched(t *testing.T) {
	env := newParityEnv(t)

	env.setFlags(createCmd, map[string]string{"silent": "true", "description": "lt"})
	res := env.run(createCmd, "Create last touched")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	id := strings.TrimSpace(res.stdout)
	if got := env.lastTouched(); got != id {
		t.Errorf("last-touched = %q, want %q", got, id)
	}
}

// TestParityCreateWithDepsOmitsDependenciesKey pins the create/update JSON
// asymmetry at its sharpest: even when the create actually persists dependency
// edges, `bd create --json` shows none, because it prints the local struct.
// `bd update --json` on the same issue re-reads — and (today) still shows no
// `dependencies` key, because storage GetIssue does not hydrate dependencies
// either (internal/storage/issueops/get_issue.go:37-56). The difference that
// IS observable is the container: object vs array, sorted keys vs struct
// order, schema_version present vs absent.
func TestParityCreateWithDepsOmitsDependenciesKey(t *testing.T) {
	env := newParityEnv(t)
	target := env.seed("test-dep1", "Dependency target", nil)

	jsonOutput = true
	env.setFlags(createCmd, map[string]string{
		"description": "with deps",
		"deps":        "blocked-by:" + target.ID,
	})
	res := env.run(createCmd, "Create with deps")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	obj := decodeJSONObject(t, res.stdout)
	if _, ok := obj["dependencies"]; ok {
		t.Error("create --json emitted a `dependencies` key even though it prints a local struct")
	}
	id, _ := obj["id"].(string)
	deps, err := env.store.inner.GetDependencyRecords(rootCtx, id)
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("expected the edge to be persisted, got %d dependency records", len(deps))
	}
}

// TestParityCreateOnOccupiedIDRefuses pins RULING R1 as adopted: `bd create
// --id <occupied>` refuses. The facade's create-only guard returns
// storage.ErrAlreadyExists, the CLI maps it to exit 1 with a fixed message
// naming the alternatives, and the pre-existing row is left untouched.
//
// Before the rewire this was a full-row silent upsert that reported success
// (exit 0, "✓ Created issue:") while destroying the existing issue and
// recording no creation event — silent data loss. Source of the new behavior:
// cmd/bd/create.go's ops.Create call ->
// internal/storage/issueops/create_only_guard.go.
func TestParityCreateOnOccupiedIDRefuses(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-occ1", "Original title", func(i *types.Issue) {
		i.Description = "original description"
		i.Priority = 0
	})

	env.setFlags(createCmd, map[string]string{
		"id":          "test-occ1",
		"description": "replacement description",
		"priority":    "4",
	})
	res := env.run(createCmd, "Replacement title")

	// RULING R1: the occupied ID is refused.
	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
	}
	const wantErr = "Error: test-occ1 already exists; use bd update, or bd import for upsert semantics\n"
	if res.stderr != wantErr {
		t.Errorf("stderr = %q, want %q", res.stderr, wantErr)
	}
	if res.stdout != "" {
		t.Errorf("stdout = %q, want empty on a refused create", res.stdout)
	}

	// RULING R1: and the pre-existing row survives intact.
	after := env.get("test-occ1")
	if after.Title != "Original title" {
		t.Errorf("title = %q, want the seeded title", after.Title)
	}
	if after.Description != "original description" {
		t.Errorf("description = %q, want the seeded description", after.Description)
	}
	if after.Priority != 0 {
		t.Errorf("priority = %d, want 0", after.Priority)
	}
	if got := env.lastTouched(); got != "" {
		t.Errorf("last-touched = %q, want it untouched on a refused create", got)
	}
}

// TestParityCreateParentNotFoundExits1 pins a create-side pre-write refusal:
// a missing --parent aborts with exit 1 and a fixed message before any ID is
// reserved or any row written. Source: cmd/bd/create.go:461-469.
func TestParityCreateParentNotFoundExits1(t *testing.T) {
	env := newParityEnv(t)

	env.setFlags(createCmd, map[string]string{
		"parent":      "test-no-such-parent",
		"description": "orphan",
	})
	res := env.run(createCmd, "Create with missing parent")

	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1\nstderr:\n%s", res.exitCode, res.stderr)
	}
	const want = "Error: parent issue test-no-such-parent not found\n"
	if res.stderr != want {
		t.Errorf("stderr = %q, want %q", res.stderr, want)
	}
	if res.stdout != "" {
		t.Errorf("stdout = %q, want empty", res.stdout)
	}
	if got := env.store.mutations(); len(got) != 0 {
		t.Errorf("store mutations = %v, want none", got)
	}
	if got := env.lastTouched(); got != "" {
		t.Errorf("last-touched = %q, want it untouched on a failed create", got)
	}
}

// TestParityCreateMissingDepTargetIsFatal pins today's CLI dependency
// contract: a --deps target that does not exist fails the whole create, exit
// 1, with the issue rolled back — the edge is never silently dropped.
// Source: cmd/bd/create_atomic.go:35-68 (one transaction; any edge failure
// returns fatal) via cmd/bd/create.go:567-569.
func TestParityCreateMissingDepTargetIsFatal(t *testing.T) {
	env := newParityEnv(t)

	env.setFlags(createCmd, map[string]string{
		"description": "dangling dep",
		"deps":        "blocked-by:test-no-such-dep",
	})
	res := env.run(createCmd, "Create with missing dep target")

	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1 (a missing dep target must not silently succeed)\nstdout:\n%s\nstderr:\n%s",
			res.exitCode, res.stdout, res.stderr)
	}
	if !strings.HasPrefix(res.stderr, "Error: ") {
		t.Errorf("stderr = %q, want an \"Error: \" prefixed diagnosis", res.stderr)
	}
	issues, err := env.store.inner.SearchIssues(rootCtx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	for _, iss := range issues {
		if iss.Title == "Create with missing dep target" {
			t.Errorf("issue %s survived a failed dependency create; the transaction must roll back", iss.ID)
		}
	}
}

// TestParityCreateJSONErrorShape pins where a create error goes in --json
// mode: HandleErrorRespectJSON writes the error object to STDOUT (not stderr),
// with a synthesized schema_version, and exits 1.
// Source: cmd/bd/errors.go:91-97 and :57-84.
func TestParityCreateJSONErrorShape(t *testing.T) {
	env := newParityEnv(t)
	jsonOutput = true

	env.setFlags(createCmd, map[string]string{
		"description": "bad deps",
		"deps":        "not-a-valid-spec:::",
	})
	res := env.run(createCmd, "Create json error")

	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", res.exitCode, res.stdout, res.stderr)
	}
	obj := decodeJSONObject(t, res.stdout)
	assertKeySet(t, obj, []string{"error", "schema_version"})
	if obj["schema_version"] != float64(JSONSchemaVersion) {
		t.Errorf("schema_version = %v, want %d", obj["schema_version"], JSONSchemaVersion)
	}
	if msg, _ := obj["error"].(string); msg == "" {
		t.Error("error message is empty")
	}
	if res.stderr != "" {
		t.Errorf("stderr = %q; in --json mode the error belongs on stdout", res.stderr)
	}
}

// ===== bd update =====

// TestParityUpdateJSONShape pins `bd update --json`: a JSON ARRAY of re-read
// issues, in struct-field order, with NO schema_version wrapper (outputJSON
// leaves slices alone — cmd/bd/output.go:85-87).
//
// Source: cmd/bd/update.go:586 (re-fetch) and :631-635 (outputJSON).
func TestParityUpdateJSONShape(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-upd1", "Update json shape", nil)

	jsonOutput = true
	env.setFlags(updateCmd, map[string]string{"priority": "0"})
	res := env.run(updateCmd, "test-upd1")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	if !strings.HasPrefix(res.stdout, "[\n  {") {
		t.Errorf("update --json is not a 2-space-indented array: %q", firstLine(res.stdout, 2))
	}
	if !strings.HasSuffix(res.stdout, "]\n") {
		t.Errorf("update --json does not end with \"]\\n\"")
	}
	arr := decodeJSONArray(t, res.stdout)
	if len(arr) != 1 {
		t.Fatalf("array length = %d, want 1", len(arr))
	}
	if _, ok := arr[0]["schema_version"]; ok {
		t.Error("update --json array elements carry schema_version; today outputJSON leaves slices unwrapped")
	}
	if arr[0]["priority"] != float64(0) {
		t.Errorf("priority = %v, want 0 (the value written)", arr[0]["priority"])
	}

	// Struct-field order (types.Issue), not sorted: `id` then `title` — the
	// reverse of alphabetical, which is what create emits.
	inner := res.stdout[strings.Index(res.stdout, "{"):]
	order := rawJSONKeyOrder(t, inner)
	if len(order) < 2 || order[0] != "id" || order[1] != "title" {
		t.Errorf("update --json element key order starts %v; today it is types.Issue struct order (id, title, ...)", order)
	}
	if isSortedStrings(order) {
		t.Errorf("update --json element keys are sorted %v; today they follow struct order", order)
	}

	// GetIssue does not hydrate dependencies/comments, so the re-read carries
	// neither key even though it is a real re-read.
	if _, ok := arr[0]["dependencies"]; ok {
		t.Error("update --json emitted `dependencies`; today GetIssue does not hydrate them")
	}
	assertRecentUTCTimestamp(t, "created_at", arr[0]["created_at"].(string))
	assertRecentUTCTimestamp(t, "updated_at", arr[0]["updated_at"].(string))
}

// TestParityUpdateHumanOutput pins the human success line, byte for byte.
// Source: cmd/bd/update.go:597.
func TestParityUpdateHumanOutput(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-upd2", "Update human output", nil)

	env.setFlags(updateCmd, map[string]string{"status": "in_progress"})
	res := env.run(updateCmd, "test-upd2")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	want := fmt.Sprintf("✓ Updated issue: %s\n", formatFeedbackID("test-upd2", "Update human output"))
	if res.stdout != want {
		t.Errorf("stdout mismatch\n got: %q\nwant: %q", res.stdout, want)
	}
	if res.stderr != "" {
		t.Errorf("stderr = %q, want empty on the happy path", res.stderr)
	}
	if !strings.HasPrefix(res.stdout, "✓ Updated issue: ") {
		t.Errorf("update's success line no longer starts with %q", "✓ Updated issue: ")
	}
}

// TestParityUpdateSetsLastTouched pins cmd/bd/update.go:627-629 — the FIRST
// successfully updated ID becomes last-touched.
func TestParityUpdateSetsLastTouched(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-upd3", "First", nil)
	env.seed("test-upd4", "Second", nil)

	env.setFlags(updateCmd, map[string]string{"priority": "1"})
	res := env.run(updateCmd, "test-upd3", "test-upd4")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	if got := env.lastTouched(); got != "test-upd3" {
		t.Errorf("last-touched = %q, want the FIRST updated ID %q", got, "test-upd3")
	}
}

// TestParityUpdateNoUpdatesShortCircuit pins the pre-write short-circuit at
// cmd/bd/update.go:327-330: with no field flags and no --claim, update prints
// "No updates specified" on STDOUT, exits 0, touches no store, and does NOT
// record a last-touched.
func TestParityUpdateNoUpdatesShortCircuit(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-upd5", "No updates", nil)

	env.setFlags(updateCmd, nil)
	res := env.run(updateCmd, "test-upd5")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	if res.stdout != "No updates specified\n" {
		t.Errorf("stdout = %q, want %q", res.stdout, "No updates specified\n")
	}
	if got := env.store.mutations(); len(got) != 0 {
		t.Errorf("store mutations = %v, want none", got)
	}
	if got := env.lastTouched(); got != "" {
		t.Errorf("last-touched = %q, want it untouched", got)
	}
}

// TestParityUpdateDefaultsToLastTouched pins cmd/bd/update.go:58-65: with no
// positional IDs, update targets the last-touched issue.
func TestParityUpdateDefaultsToLastTouched(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-updlt", "Default target", nil)
	SetLastTouchedID("test-updlt")

	env.setFlags(updateCmd, map[string]string{"priority": "1"})
	res := env.run(updateCmd)
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	if got := env.get("test-updlt").Priority; got != 1 {
		t.Errorf("priority = %d, want 1; bare `bd update` must target last-touched", got)
	}
}

// TestParityUpdateNoIDAndNoLastTouchedExits1 pins the other half of the same
// default-target contract (cmd/bd/update.go:59-65).
func TestParityUpdateNoIDAndNoLastTouchedExits1(t *testing.T) {
	env := newParityEnv(t)
	env.clearLastTouched()

	env.setFlags(updateCmd, map[string]string{"priority": "1"})
	res := env.run(updateCmd)

	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1\nstderr:\n%s", res.exitCode, res.stderr)
	}
	const want = "Error: no issue ID provided and no last touched issue\n"
	if res.stderr != want {
		t.Errorf("stderr = %q, want %q", res.stderr, want)
	}
}

// TestParityUpdateGuardsRequireFieldUpdate pins the second pre-write
// short-circuit (cmd/bd/update.go:788-798): --if-assignee/--if-status with
// only label/parent edits is rejected before any write, exit 1.
func TestParityUpdateGuardsRequireFieldUpdate(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-upd6", "Guard needs field", nil)

	env.setFlags(updateCmd, map[string]string{
		"if-status": "open",
		"add-label": "x",
	})
	res := env.run(updateCmd, "test-upd6")
	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%s", res.exitCode, res.stderr)
	}
	const want = "Error: --if-assignee/--if-status require at least one field update (e.g. -a, -s); label and parent edits are not covered by the guard\n"
	if res.stderr != want {
		t.Errorf("stderr = %q, want %q", res.stderr, want)
	}
	if got := env.store.mutations(); len(got) != 0 {
		t.Errorf("store mutations = %v, want none (rejected pre-write)", got)
	}
}

// TestParityUpdateGuardsRejectClaim pins the third pre-write short-circuit
// (cmd/bd/update.go:785-787): guards combined with --claim exit 1 before any
// write.
func TestParityUpdateGuardsRejectClaim(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-upd7", "Guard vs claim", nil)

	env.setFlags(updateCmd, map[string]string{
		"if-status": "open",
		"claim":     "true",
		"priority":  "1",
	})
	res := env.run(updateCmd, "test-upd7")
	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%s", res.exitCode, res.stderr)
	}
	const want = "Error: cannot combine --if-assignee/--if-status with --claim (--claim is already an atomic compare-and-set)\n"
	if res.stderr != want {
		t.Errorf("stderr = %q, want %q", res.stderr, want)
	}
	if got := env.store.mutations(); len(got) != 0 {
		t.Errorf("store mutations = %v, want none (rejected pre-write)", got)
	}
}

// TestParityUpdateGuardMismatchExits13 pins the bd-wsqvw exit contract:
// when EVERY failure is a stale --if-assignee/--if-status guard, the command
// exits ExitGuardMismatch (13), not 1 — nothing was written and retrying is
// pointless. Source: cmd/bd/update.go:665 (const), :690-731
// (reportUpdateFailures).
func TestParityUpdateGuardMismatchExits13(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-upd8", "Guard mismatch", func(i *types.Issue) {
		i.Assignee = "someone-else"
	})

	env.setFlags(updateCmd, map[string]string{
		"if-assignee": "not-the-holder",
		"priority":    "1",
	})
	res := env.run(updateCmd, "test-upd8")
	if res.exitCode != ExitGuardMismatch {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", res.exitCode, ExitGuardMismatch, res.stderr)
	}
	if !strings.Contains(res.stderr, "assignee mismatch") {
		t.Errorf("stderr lacks the machine-greppable %q sentinel:\n%s", "assignee mismatch", res.stderr)
	}
	if !strings.Contains(res.stderr, "Error: 1 of 1 issues failed to update") {
		t.Errorf("stderr lacks the per-ID failure summary:\n%s", res.stderr)
	}
	if got := env.get("test-upd8").Priority; got != 2 {
		t.Errorf("priority = %d; a refused guard must write nothing (seeded 2)", got)
	}
	if got := env.lastTouched(); got != "" {
		t.Errorf("last-touched = %q; a wholly-failed update must not record one", got)
	}
}

// TestParityUpdateMixedFailureExits1 pins the other half of the same contract:
// a batch whose failures are NOT all guard mismatches exits 1, even though
// some IDs succeeded. Source: cmd/bd/update.go:721-731.
func TestParityUpdateMixedFailureExits1(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-upd9", "Real", nil)

	env.setFlags(updateCmd, map[string]string{"priority": "1"})
	res := env.run(updateCmd, "test-upd9", "test-nonexistent-zz")
	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if !strings.Contains(res.stderr, "Error: 1 of 2 issues failed to update") {
		t.Errorf("stderr lacks the partial-batch summary:\n%s", res.stderr)
	}
	// The successful half stays applied — updates are per-ID, not atomic.
	if got := env.get("test-upd9").Priority; got != 1 {
		t.Errorf("priority = %d, want 1; successful IDs in a partial batch stay applied", got)
	}
	if got := env.lastTouched(); got != "test-upd9" {
		t.Errorf("last-touched = %q, want %q", got, "test-upd9")
	}
}

// TestParityUpdateJSONFailureReport pins the --json failure envelope: stdout
// keeps the plain array of successfully updated issues while the per-ID
// failure report is a single compact JSON line — the LAST line on stderr —
// carrying "error", "failed", "schema_version", and a per-failure
// "guard_mismatch" marker. Source: cmd/bd/update.go:690-732.
func TestParityUpdateJSONFailureReport(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-updjf", "Guard mismatch json", func(i *types.Issue) {
		i.Assignee = "someone-else"
	})

	jsonOutput = true
	env.setFlags(updateCmd, map[string]string{
		"if-assignee": "not-the-holder",
		"priority":    "1",
	})
	res := env.run(updateCmd, "test-updjf")

	if res.exitCode != ExitGuardMismatch {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", res.exitCode, ExitGuardMismatch, res.stderr)
	}
	if res.stdout != "" {
		t.Errorf("stdout = %q, want empty (no issue was updated)", res.stdout)
	}

	lines := strings.Split(strings.TrimRight(res.stderr, "\n"), "\n")
	report := lines[len(lines)-1]
	var payload struct {
		Error         string `json:"error"`
		SchemaVersion int    `json:"schema_version"`
		Failed        []struct {
			ID            string `json:"id"`
			Error         string `json:"error"`
			GuardMismatch bool   `json:"guard_mismatch"`
		} `json:"failed"`
	}
	if err := json.Unmarshal([]byte(report), &payload); err != nil {
		t.Fatalf("last stderr line is not the JSON failure report: %v\nline: %s\nstderr:\n%s", err, report, res.stderr)
	}
	if payload.Error != "1 of 1 issues failed to update" {
		t.Errorf("error = %q, want %q", payload.Error, "1 of 1 issues failed to update")
	}
	if payload.SchemaVersion != JSONSchemaVersion {
		t.Errorf("schema_version = %d, want %d", payload.SchemaVersion, JSONSchemaVersion)
	}
	if len(payload.Failed) != 1 {
		t.Fatalf("failed entries = %d, want 1", len(payload.Failed))
	}
	if payload.Failed[0].ID != "test-updjf" {
		t.Errorf("failed[0].id = %q, want %q", payload.Failed[0].ID, "test-updjf")
	}
	if !payload.Failed[0].GuardMismatch {
		t.Error("failed[0].guard_mismatch = false; a stale --if-assignee must be marked")
	}
}

// TestParityUpdateCompoundSideEffectSurface pins RULING R2 as adopted: a
// compound `bd update` is ONE atomic operation, so exactly one on_update hook
// fires for the whole command instead of one per edit group.
//
// Before the rewire this made one store call per edit group — the field
// UPDATE, then each label add/remove — firing three hooks for this command.
// Source of the new behavior: cmd/bd/update.go's single ops.Update call ->
// internal/storage/issueops/execution.go ExecuteUpdate.
//
// The label events themselves are unchanged here because both edits are real
// deltas; the no-op case is TestParityUpdateNoOpLabelEditIsSilent.
func TestParityUpdateCompoundSideEffectSurface(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-cmp1", "Compound update", func(i *types.Issue) {
		i.Labels = []string{"keep"}
	})
	before := env.eventTypes("test-cmp1")
	baseAdded := countOf(before, string(types.EventLabelAdded))
	baseRemoved := countOf(before, string(types.EventLabelRemoved))

	env.setFlags(updateCmd, map[string]string{
		"status":       "in_progress",
		"add-label":    "added",
		"remove-label": "keep",
	})
	res := env.run(updateCmd, "test-cmp1")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	// RULING R2: one atomic operation => one on_update hook firing.
	got := env.store.mutations()
	want := []string{"Update"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("operations = %v, want %v (one hook firing)", got, want)
	}

	// Real label deltas still emit their own events on top of the update.
	after := env.eventTypes("test-cmp1")
	if n := countOf(after, string(types.EventLabelAdded)) - baseAdded; n != 1 {
		t.Errorf("new label_added events = %d, want 1 (events: %v)", n, after)
	}
	if n := countOf(after, string(types.EventLabelRemoved)) - baseRemoved; n != 1 {
		t.Errorf("new label_removed events = %d, want 1 (events: %v)", n, after)
	}
	if len(after) <= len(before) {
		t.Errorf("event count did not grow: before=%d after=%d (%v)", len(before), len(after), after)
	}
}

// TestParityUpdateNoOpLabelEditIsSilent pins the delta-only half of RULING R2
// as adopted: adding a label the issue ALREADY has, and removing one it does
// NOT have, write no events at all.
//
// Before the rewire both no-ops emitted label_added/label_removed events
// describing changes that never happened, because the per-label store calls
// inserted their event unconditionally
// (internal/storage/issueops/labels.go). The facade diffs the label set first
// (internal/storage/issueops/aggregate.go ApplyLabelPatch).
func TestParityUpdateNoOpLabelEditIsSilent(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-cmp2", "No-op labels", nil)
	if err := env.store.inner.AddLabel(rootCtx, "test-cmp2", "present", "parity-seed"); err != nil {
		t.Fatalf("seed label: %v", err)
	}
	env.store.reset()
	baseAdded := countOf(env.eventTypes("test-cmp2"), string(types.EventLabelAdded))
	baseRemoved := countOf(env.eventTypes("test-cmp2"), string(types.EventLabelRemoved))

	env.setFlags(updateCmd, map[string]string{
		"add-label":    "present",
		"remove-label": "absent",
	})
	res := env.run(updateCmd, "test-cmp2")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	// RULING R2: neither no-op produces an event.
	after := env.eventTypes("test-cmp2")
	if n := countOf(after, string(types.EventLabelAdded)) - baseAdded; n != 0 {
		t.Errorf("label_added events = %d, want 0 (re-adding an existing label changes nothing)", n)
	}
	if n := countOf(after, string(types.EventLabelRemoved)) - baseRemoved; n != 0 {
		t.Errorf("label_removed events = %d, want 0 (removing an absent label changes nothing)", n)
	}
	// The label set itself is untouched.
	labels, err := env.store.inner.GetLabels(rootCtx, "test-cmp2")
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	if strings.Join(labels, ",") != "present" {
		t.Errorf("labels = %v, want [present]", labels)
	}
}

// TestParityUpdateBareClaimFiresUpdateHook pins the last R2 asymmetry as
// closed: a bare `--claim` is an ordinary update operation, so it fires the
// on_update hook like every other `bd update`.
//
// Before the rewire it went through ClaimIssue, a method
// storage.HookFiringStore does not decorate, so a claim was the one update
// that fired no hook at all.
func TestParityUpdateBareClaimFiresUpdateHook(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-cmp3", "Bare claim", nil)

	env.setFlags(updateCmd, map[string]string{"claim": "true"})
	res := env.run(updateCmd, "test-cmp3")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	// RULING R2: one operation, and it fires the update hook.
	got := env.store.mutations()
	if strings.Join(got, ",") != "Update" {
		t.Errorf("operations = %v, want [Update] (one hook firing)", got)
	}
	claimed := env.get("test-cmp3")
	if claimed.Assignee != actor {
		t.Errorf("assignee = %q, want %q", claimed.Assignee, actor)
	}
	if claimed.Status != types.StatusInProgress {
		t.Errorf("status = %q, want in_progress", claimed.Status)
	}
}

// TestParityUpdateParentReplacesAllEdges pins RULING R3 as adopted: `bd update
// --parent` replaces EVERY existing parent-child edge with the requested one,
// atomically, in the same operation as the rest of the update.
//
// Before the rewire it removed only the FIRST parent-child edge it found and
// then added the new one across three separate transactions, leaving a
// two-parent issue with a stale parent AND the new one — a silently corrupted
// hierarchy. Source of the new behavior:
// internal/storage/issueops/aggregate.go ApplyParentPatch.
func TestParityUpdateParentReplacesAllEdges(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-par1", "Parent one", nil)
	env.seed("test-par2", "Parent two", nil)
	env.seed("test-par3", "Parent three", nil)
	child := env.seed("test-par9", "Child", nil)

	for _, parent := range []string{"test-par1", "test-par2"} {
		dep := &types.Dependency{IssueID: child.ID, DependsOnID: parent, Type: types.DepParentChild}
		if err := env.store.inner.AddDependency(rootCtx, dep, "parity-seed"); err != nil {
			t.Fatalf("seed parent edge %s: %v", parent, err)
		}
	}
	env.store.reset()

	env.setFlags(updateCmd, map[string]string{"parent": "test-par3"})
	res := env.run(updateCmd, child.ID)
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	records, err := env.store.inner.GetDependencyRecords(rootCtx, child.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords: %v", err)
	}
	var parents []string
	for _, dep := range records {
		if dep.Type == types.DepParentChild {
			parents = append(parents, dep.DependsOnID)
		}
	}

	// RULING R3: both old edges are gone; the requested parent is the only one.
	if strings.Join(parents, ",") != "test-par3" {
		t.Errorf("parent edges after reparent = %v, want [test-par3] (replace-all)", parents)
	}

	// RULING R2/R3: the reparent rides the same single operation as the rest
	// of the update, so it is one hook firing, not two store calls.
	got := env.store.mutations()
	want := []string{"Update"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("operations = %v, want %v (one hook firing)", got, want)
	}
}

// ===== bd close =====

// TestParityCloseJSONShape pins `bd close --json`: a JSON ARRAY of re-read
// issues in struct-field order with no schema_version wrapper — the same
// container shape as update, and the opposite of create.
// Source: cmd/bd/close.go:222 (re-fetch) and :340-353 (outputJSON).
func TestParityCloseJSONShape(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-cls1", "Close json shape", nil)

	jsonOutput = true
	env.setFlags(closeCmd, map[string]string{"reason": "done here"})
	res := env.run(closeCmd, "test-cls1")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	if !strings.HasPrefix(res.stdout, "[\n  {") {
		t.Errorf("close --json is not a 2-space-indented array: %q", firstLine(res.stdout, 2))
	}
	arr := decodeJSONArray(t, res.stdout)
	if len(arr) != 1 {
		t.Fatalf("array length = %d, want 1", len(arr))
	}
	if _, ok := arr[0]["schema_version"]; ok {
		t.Error("close --json array elements carry schema_version; today slices are unwrapped")
	}
	if arr[0]["status"] != string(types.StatusClosed) {
		t.Errorf("status = %v, want closed", arr[0]["status"])
	}
	// A re-read close DOES carry closed_at and close_reason.
	closedAt, _ := arr[0]["closed_at"].(string)
	if closedAt == "" {
		t.Fatal("close --json omitted closed_at")
	}
	assertRecentUTCTimestamp(t, "closed_at", closedAt)
	if arr[0]["close_reason"] != "done here" {
		t.Errorf("close_reason = %v, want %q", arr[0]["close_reason"], "done here")
	}
}

// TestParityCloseHumanOutput pins the human success line, byte for byte, and
// the default reason ("Closed") applied when no --reason is given.
// Source: cmd/bd/close.go:229 and :453-455.
func TestParityCloseHumanOutput(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-cls2", "Close human output", nil)

	env.setFlags(closeCmd, nil)
	res := env.run(closeCmd, "test-cls2")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	want := fmt.Sprintf("✓ Closed %s: Closed\n", formatFeedbackID("test-cls2", "Close human output"))
	if res.stdout != want {
		t.Errorf("stdout mismatch\n got: %q\nwant: %q", res.stdout, want)
	}
	if res.stderr != "" {
		t.Errorf("stderr = %q, want empty on the happy path", res.stderr)
	}
	if !strings.HasPrefix(res.stdout, "✓ Closed ") {
		t.Errorf("close's success line no longer starts with %q", "✓ Closed ")
	}
}

// TestParityCloseSetsLastTouched pins cmd/bd/close.go:251-253.
func TestParityCloseSetsLastTouched(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-cls3", "Close last touched", nil)

	env.setFlags(closeCmd, nil)
	res := env.run(closeCmd, "test-cls3")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	if got := env.lastTouched(); got != "test-cls3" {
		t.Errorf("last-touched = %q, want %q", got, "test-cls3")
	}
}

// TestParityCloseDefaultsToLastTouched pins cmd/bd/close.go:53-60: with no
// positional IDs, close targets the last-touched issue.
func TestParityCloseDefaultsToLastTouched(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-clslt", "Default close target", nil)
	SetLastTouchedID("test-clslt")

	env.setFlags(closeCmd, nil)
	res := env.run(closeCmd)
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	if got := env.get("test-clslt").Status; got != types.StatusClosed {
		t.Errorf("status = %q, want closed; bare `bd close` must target last-touched", got)
	}
}

// TestParityCloseNoIDAndNoLastTouchedExits1 pins the other half
// (cmd/bd/close.go:54-59).
func TestParityCloseNoIDAndNoLastTouchedExits1(t *testing.T) {
	env := newParityEnv(t)
	env.clearLastTouched()

	env.setFlags(closeCmd, nil)
	res := env.run(closeCmd)

	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1\nstderr:\n%s", res.exitCode, res.stderr)
	}
	const want = "Error: no issue ID provided and no last touched issue\n"
	if res.stderr != want {
		t.Errorf("stderr = %q, want %q", res.stderr, want)
	}
}

// TestParityClosePartialFailureExitsZero pins the close exit contract's
// permissive half (cmd/bd/close.go:377-380): as long as ONE id settled as
// closed, a batch with refused ids still exits 0. The refusal is reported on
// stderr only.
func TestParityClosePartialFailureExitsZero(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-cls4", "Closable", nil)
	env.seed("test-cls5", "Owned by another actor", func(i *types.Issue) {
		i.Assignee = "someone-else"
	})

	env.setFlags(closeCmd, nil)
	res := env.run(closeCmd, "test-cls4", "test-cls5")

	if res.exitCode != 0 {
		t.Fatalf("exit = %d, want 0 (partial failure is still success today)\nstderr:\n%s", res.exitCode, res.stderr)
	}
	wantErr := fmt.Sprintf("cannot close %s: assignee is %q, actor is %q; reclaim or use --force to override\n",
		"test-cls5", "someone-else", actor)
	if res.stderr != wantErr {
		t.Errorf("stderr = %q, want %q", res.stderr, wantErr)
	}
	if env.get("test-cls4").Status != types.StatusClosed {
		t.Error("the closable ID was not closed")
	}
	if env.get("test-cls5").Status == types.StatusClosed {
		t.Error("the refused ID must not have been closed")
	}
	// The one that DID close still drives the post-close contracts.
	if got := env.lastTouched(); got != "test-cls4" {
		t.Errorf("last-touched = %q, want %q", got, "test-cls4")
	}
}

// TestParityCloseNothingSettledExits1 pins the strict half of the same
// contract: when NO id settled as closed, close returns SilentExit() — exit 1
// with no extra stdout. Source: cmd/bd/close.go:377-380 and
// cmd/bd/errors.go:119-121.
func TestParityCloseNothingSettledExits1(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-cls6", "Owned by another actor", func(i *types.Issue) {
		i.Assignee = "someone-else"
	})

	env.setFlags(closeCmd, nil)
	res := env.run(closeCmd, "test-cls6")

	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if env.get("test-cls6").Status == types.StatusClosed {
		t.Error("the refused ID must not have been closed")
	}
	if res.stdout != "" {
		t.Errorf("stdout = %q, want empty (SilentExit prints nothing itself)", res.stdout)
	}
	if got := env.lastTouched(); got != "" {
		t.Errorf("last-touched = %q, want it untouched when nothing settled", got)
	}
}

// TestParityCloseAlreadyClosedIsIdempotentSuccess pins the re-close no-op:
// exit 0, the issue still reported on stdout, last-touched still recorded, and
// NO second close event. Source: cmd/bd/close.go:167-195.
func TestParityCloseAlreadyClosedIsIdempotentSuccess(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-cls7", "Already closed", nil)

	env.setFlags(closeCmd, nil)
	if res := env.run(closeCmd, "test-cls7"); res.exitCode != 0 {
		t.Fatalf("first close failed: exit=%d stderr=%s", res.exitCode, res.stderr)
	}
	closedEventsAfterFirst := countOf(env.eventTypes("test-cls7"), string(types.EventClosed))
	env.clearLastTouched()
	env.store.reset()

	res := env.run(closeCmd, "test-cls7")
	if res.exitCode != 0 {
		t.Fatalf("re-close exit = %d, want 0\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if !strings.HasPrefix(res.stdout, "✓ Closed ") {
		t.Errorf("re-close stdout = %q; today an already-closed re-close still reports success", res.stdout)
	}
	if got := env.lastTouched(); got != "test-cls7" {
		t.Errorf("last-touched = %q, want %q (retry-safe post-close contract)", got, "test-cls7")
	}
	if n := countOf(env.eventTypes("test-cls7"), string(types.EventClosed)); n != closedEventsAfterFirst {
		t.Errorf("closed events = %d, want %d (a re-close must add none)", n, closedEventsAfterFirst)
	}
}

// ===== bd reopen =====

// TestParityReopenJSONShape pins `bd reopen --json`: a JSON ARRAY of re-read
// issues, no schema_version — same container as update/close.
// Source: cmd/bd/reopen.go:72-76 and :101-105.
func TestParityReopenJSONShape(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-rop1", "Reopen json shape", func(i *types.Issue) {
		i.Status = types.StatusClosed
	})

	jsonOutput = true
	env.setFlags(reopenCmd, map[string]string{"reason": "back to work"})
	res := env.run(reopenCmd, "test-rop1")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	if !strings.HasPrefix(res.stdout, "[\n  {") {
		t.Errorf("reopen --json is not a 2-space-indented array: %q", firstLine(res.stdout, 2))
	}
	arr := decodeJSONArray(t, res.stdout)
	if len(arr) != 1 {
		t.Fatalf("array length = %d, want 1", len(arr))
	}
	if _, ok := arr[0]["schema_version"]; ok {
		t.Error("reopen --json array elements carry schema_version; today slices are unwrapped")
	}
	if arr[0]["status"] != string(types.StatusOpen) {
		t.Errorf("status = %v, want open", arr[0]["status"])
	}
	if _, ok := arr[0]["closed_at"]; ok {
		t.Error("reopen --json still carries closed_at; the engine clears it")
	}
}

// TestParityReopenHumanOutput pins the human line, byte for byte, including
// the ": <reason>" suffix. Source: cmd/bd/reopen.go:78-83.
func TestParityReopenHumanOutput(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-rop2", "Reopen human", func(i *types.Issue) { i.Status = types.StatusClosed })

	env.setFlags(reopenCmd, map[string]string{"reason": "needs more"})
	res := env.run(reopenCmd, "test-rop2")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	const want = "↻ Reopened test-rop2: needs more\n"
	if res.stdout != want {
		t.Errorf("stdout mismatch\n got: %q\nwant: %q", res.stdout, want)
	}
	if res.stderr != "" {
		t.Errorf("stderr = %q, want empty on the happy path", res.stderr)
	}
	if !strings.HasPrefix(res.stdout, "↻ Reopened ") {
		t.Errorf("reopen's success line no longer starts with %q", "↻ Reopened ")
	}
}

// TestParityReopenHumanOutputNoReason pins the no-reason variant: no colon
// suffix, and NO title interpolation — unlike create/update/close, reopen
// prints the bare resolved ID. Source: cmd/bd/reopen.go:82.
func TestParityReopenHumanOutputNoReason(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-rop3", "A title that must not appear", func(i *types.Issue) {
		i.Status = types.StatusClosed
	})

	env.setFlags(reopenCmd, nil)
	res := env.run(reopenCmd, "test-rop3")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	const want = "↻ Reopened test-rop3\n"
	if res.stdout != want {
		t.Errorf("stdout mismatch\n got: %q\nwant: %q", res.stdout, want)
	}
}

// TestParityReopenDoesNotSetLastTouched pins an absence: unlike create,
// update and close, `bd reopen` never calls SetLastTouchedID. Nothing in
// cmd/bd/reopen.go does.
func TestParityReopenDoesNotSetLastTouched(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-rop4", "Reopen last touched", func(i *types.Issue) { i.Status = types.StatusClosed })

	env.setFlags(reopenCmd, nil)
	res := env.run(reopenCmd, "test-rop4")
	if res.exitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	if got := env.lastTouched(); got != "" {
		t.Errorf("last-touched = %q; bd reopen does not record one today", got)
	}
}

// TestParityReopenAlreadyOpenSkipsSilently pins the pre-read short-circuit at
// cmd/bd/reopen.go:59-63: an already-open issue reports on stderr, prints
// nothing on stdout, performs no store write, and does NOT set hasError — so
// the command exits 0.
func TestParityReopenAlreadyOpenSkipsSilently(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-rop5", "Already open", nil)

	env.setFlags(reopenCmd, nil)
	res := env.run(reopenCmd, "test-rop5")

	if res.exitCode != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if res.stdout != "" {
		t.Errorf("stdout = %q, want empty", res.stdout)
	}
	if res.stderr != "test-rop5 is already open\n" {
		t.Errorf("stderr = %q, want %q", res.stderr, "test-rop5 is already open\n")
	}
	if got := env.store.mutations(); len(got) != 0 {
		t.Errorf("store mutations = %v, want none", got)
	}
}

// TestParityReopenErrorExits1Silently pins the failure exit: any per-ID error
// sets hasError, and the command returns SilentExit() — exit 1 with the
// diagnosis on stderr only. Source: cmd/bd/reopen.go:50-53, :107-109.
func TestParityReopenErrorExits1Silently(t *testing.T) {
	env := newParityEnv(t)

	env.setFlags(reopenCmd, nil)
	res := env.run(reopenCmd, "test-nonexistent-zz")

	if res.exitCode != 1 {
		t.Fatalf("exit = %d, want 1\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if res.stdout != "" {
		t.Errorf("stdout = %q, want empty (SilentExit prints nothing itself)", res.stdout)
	}
	if !strings.HasPrefix(res.stderr, "Error resolving test-nonexistent-zz: ") {
		t.Errorf("stderr = %q, want the \"Error resolving <id>: \" prefix", res.stderr)
	}
}

// TestParityReopenNonDoneStatusReportsNothingToDo pins RULING R4 as adopted:
// reopening an issue whose status is neither done nor open reports that there
// was nothing to do and fires no hook.
//
// Before the rewire the engine already no-opped
// (internal/storage/issueops/reopen.go returns Changed=false for a non-done
// category) but the CLI still called ReopenIssue — firing an on_update hook —
// printed "↻ Reopened" and exited 0, so the success report was a lie about a
// status that never moved.
//
// The message goes to stderr, exactly like the already-open skip above: both
// are "nothing to do" reports, and neither issue appears in --json output.
func TestParityReopenNonDoneStatusReportsNothingToDo(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-rop6", "In progress", func(i *types.Issue) {
		i.Status = types.StatusInProgress
	})

	env.setFlags(reopenCmd, nil)
	res := env.run(reopenCmd, "test-rop6")

	// RULING R4: still exit 0 — this is not an error, just a no-op.
	if res.exitCode != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if res.stdout != "" {
		t.Errorf("stdout = %q, want empty (nothing was reopened)", res.stdout)
	}
	const wantErr = "test-rop6 is not closed (status: in_progress); nothing to do\n"
	if res.stderr != wantErr {
		t.Errorf("stderr = %q, want %q", res.stderr, wantErr)
	}
	// RULING R4: and no hook fires, because no operation reported a change.
	if got := env.store.mutations(); len(got) != 0 {
		t.Errorf("operations = %v, want none recorded (a no-op reopen fires no hook)", got)
	}
	// The status is still what it was.
	if got := env.get("test-rop6").Status; got != types.StatusInProgress {
		t.Errorf("status = %q, want in_progress (the engine no-ops)", got)
	}
}

// ===== bd update crossing into closed vs bd close =====

// closeRowParityExclusions lists the types.Issue fields a row-for-row
// comparison of `bd update -s closed` against `bd close` must skip, and why.
// Everything else has to match: the two verbs reach the same done state, so any
// other divergence is the update funnel failing to close the way close closes.
var closeRowParityExclusions = map[string]string{
	"ID":          "the two verbs act on two different issues",
	"Title":       "the two verbs act on two different issues",
	"ContentHash": "derives from ID and Title",
	"CreatedAt":   "wall-clock stamp from two different seeds",
	"UpdatedAt":   "wall-clock stamp from two different writes",
	"ClosedAt":    "wall-clock stamp; asserted non-nil on both instead",
	"RowVersion":  "freshRowLock() is regenerated per write by design",
	"CloseReason": "cmd/bd/close.go resolveCloseReasons defaults `bd close`'s " +
		"reason to \"Closed\" at the CLI layer, and `bd update` has no reason " +
		"flag; the funnel-level default is asserted separately below",
	// ga-ktn9pe.4.14 owns pin behavior in the update funnels — issueops
	// auto-clears `pinned` on a status change and domain/db does not. Comparing
	// it here would either bless that divergence or drag its fix into ga-kjkv1.
	"Pinned": "ga-ktn9pe.4.14 owns pin behavior in the update funnels",
}

// TestParityUpdateToClosedMatchesCloseRow pins ga-kjkv1's shape: a generic
// update whose status crosses into closed must land the same row `bd close`
// lands. close writes close_reason and closed_by_session on every close
// (issueops/close.go closeIssueInTx), including the empty values a caller that
// named neither produces; the funnels used to write them only when the caller
// passed the key, so the columns kept whatever the last close left.
func TestParityUpdateToClosedMatchesCloseRow(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-updcls1", "Update into closed", nil)
	env.seed("test-updcls2", "Close verb", nil)

	env.setFlags(updateCmd, map[string]string{"status": "closed"})
	if res := env.run(updateCmd, "test-updcls1"); res.exitCode != 0 {
		t.Fatalf("update into closed: exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	env.setFlags(closeCmd, nil)
	if res := env.run(closeCmd, "test-updcls2"); res.exitCode != 0 {
		t.Fatalf("close: exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}

	updated, closed := env.get("test-updcls1"), env.get("test-updcls2")
	if updated.ClosedAt == nil || closed.ClosedAt == nil {
		t.Fatalf("closed_at: update = %v, close = %v; both verbs must stamp it", updated.ClosedAt, closed.ClosedAt)
	}

	updatedValue, closedValue := reflect.ValueOf(*updated), reflect.ValueOf(*closed)
	for i := 0; i < updatedValue.NumField(); i++ {
		field := updatedValue.Type().Field(i)
		if why, skip := closeRowParityExclusions[field.Name]; skip {
			t.Logf("skipping %s: %s", field.Name, why)
			continue
		}
		got, want := updatedValue.Field(i).Interface(), closedValue.Field(i).Interface()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s after `bd update -s closed` = %#v, want %#v (the value `bd close` writes)", field.Name, got, want)
		}
	}

	// And the funnel default itself: no session flag, no session recorded.
	if updated.ClosedBySession != "" {
		t.Errorf("closed_by_session after a sessionless `bd update -s closed` = %q, want empty", updated.ClosedBySession)
	}
	if updated.CloseReason != "" {
		t.Errorf("close_reason after a reasonless `bd update -s closed` = %q, want empty", updated.CloseReason)
	}
}

// TestParityUpdateToClosedClearsPriorCloseAttribution pins the misattribution
// ga-kjkv1 fixes. The generic reopen branch never cleared closed_by_session, so
// a re-close through the funnel inherited the PREVIOUS close's session and
// `bd show` rendered the new close as that old session's work.
func TestParityUpdateToClosedClearsPriorCloseAttribution(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-updcls3", "Reclose attribution", nil)

	env.setFlags(closeCmd, map[string]string{"reason": "first pass", "session": "session-one"})
	if res := env.run(closeCmd, "test-updcls3"); res.exitCode != 0 {
		t.Fatalf("first close: exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	if got := env.get("test-updcls3"); got.ClosedBySession != "session-one" || got.CloseReason != "first pass" {
		t.Fatalf("first close recorded reason=%q session=%q, want %q/%q", got.CloseReason, got.ClosedBySession, "first pass", "session-one")
	}

	// Reopen through the generic funnel, not the reopen verb — this is the path
	// that used to leave the close attribution behind.
	env.setFlags(updateCmd, map[string]string{"status": "open"})
	if res := env.run(updateCmd, "test-updcls3"); res.exitCode != 0 {
		t.Fatalf("generic reopen: exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	reopened := env.get("test-updcls3")
	if reopened.ClosedBySession != "" {
		t.Errorf("closed_by_session after a generic reopen = %q, want empty", reopened.ClosedBySession)
	}
	if reopened.CloseReason != "" {
		t.Errorf("close_reason after a generic reopen = %q, want empty", reopened.CloseReason)
	}
	if reopened.ClosedAt != nil {
		t.Errorf("closed_at after a generic reopen = %v, want nil", reopened.ClosedAt)
	}

	// The re-close must attribute itself, not the first close.
	env.setFlags(updateCmd, map[string]string{"status": "closed"})
	if res := env.run(updateCmd, "test-updcls3"); res.exitCode != 0 {
		t.Fatalf("generic re-close: exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	reclosed := env.get("test-updcls3")
	if reclosed.ClosedBySession != "" {
		t.Errorf("closed_by_session after a sessionless generic re-close = %q, want empty (not the first close's session)", reclosed.ClosedBySession)
	}
	if reclosed.CloseReason != "" {
		t.Errorf("close_reason after a reasonless generic re-close = %q, want empty (not the first close's reason)", reclosed.CloseReason)
	}
}

// TestParityUpdateToClosedKeepsExplicitSession pins that the defaults above
// yield to an explicit key: `bd update -s closed --session` still attributes the
// close, so the CLI's own closed_by_session pass-through (cmd/bd/update.go:133,
// cmd/bd/update_input.go:58) keeps working and must stay in place.
func TestParityUpdateToClosedKeepsExplicitSession(t *testing.T) {
	env := newParityEnv(t)
	env.seed("test-updcls4", "Explicit session", nil)

	env.setFlags(updateCmd, map[string]string{"status": "closed", "session": "session-two"})
	if res := env.run(updateCmd, "test-updcls4"); res.exitCode != 0 {
		t.Fatalf("update into closed with a session: exit=%d err=%v stderr=%s", res.exitCode, res.err, res.stderr)
	}
	if got := env.get("test-updcls4").ClosedBySession; got != "session-two" {
		t.Errorf("closed_by_session = %q, want %q (the explicit key beats the default)", got, "session-two")
	}
}

// ===== small helpers =====

func firstLine(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func isSortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] < s[i-1] {
			return false
		}
	}
	return true
}
