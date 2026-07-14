//go:build e2e

package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
)

// scenario is a named sequence of bd CLI invocations. IDs are pinned with --id so
// they match across backends; only timestamps/paths need normalization.
//
// unordered marks a scenario whose JSON-array output has no contractual order: bd
// sorts by priority then created_at, and Postgres's whole-second timestamps tie where
// Dolt's sub-second ones do not, so equal-priority items created in the same second
// can legitimately differ in order. For these, array elements are sorted by id before
// comparison (matching the bts-rs oracle's multiset semantics for list output).
type scenario struct {
	name      string
	steps     [][]string
	unordered bool
}

// corpus is the backend-agnostic CLI surface exercised end-to-end. It is deliberately
// compact (the gc lifecycle + config), and grows over time; the bts-rs 523-scenario
// differential oracle (scripts/run-oracle-p.sh) remains the deep gate.
var corpus = []scenario{
	{name: "create-show", steps: [][]string{
		{"create", "First task", "--id", "cf-a1", "-t", "task", "--json"},
		{"show", "cf-a1", "--json"},
	}},
	{name: "list", unordered: true, steps: [][]string{
		{"create", "one", "--id", "cf-l1", "-t", "task"},
		{"create", "two", "--id", "cf-l2", "-t", "bug"},
		{"list", "--json"},
	}},
	{name: "dep-ready-gating", steps: [][]string{
		{"create", "root", "--id", "cf-r1", "-t", "task"},
		{"create", "blocked", "--id", "cf-r2", "-t", "task"},
		{"dep", "add", "cf-r2", "cf-r1"},
		{"ready", "--json"},
		{"close", "cf-r1"},
		{"ready", "--json"},
	}},
	{name: "close-reopen", steps: [][]string{
		{"create", "x", "--id", "cf-c1", "-t", "task"},
		{"close", "cf-c1"},
		{"show", "cf-c1", "--json"},
		{"reopen", "cf-c1"},
		{"show", "cf-c1", "--json"},
	}},
	{name: "update", steps: [][]string{
		{"create", "y", "--id", "cf-u1", "-t", "task"},
		{"update", "cf-u1", "-p", "0"},
		{"show", "cf-u1", "--json"},
	}},
	{name: "label", steps: [][]string{
		{"create", "z", "--id", "cf-b1", "-t", "task"},
		{"label", "add", "cf-b1", "urgent"},
		{"show", "cf-b1", "--json"},
	}},
	{name: "config", steps: [][]string{
		{"config", "set", "custom.foo", "bar"},
		{"config", "get", "custom.foo"},
		{"config", "unset", "custom.foo"},
		{"config", "get", "custom.foo"},
	}},
	// Deferred surfaces, present to exercise XFail classification against the
	// postgres profile's allowlist (the reference passes these).
	{name: "stats", steps: [][]string{
		{"create", "s", "--id", "cf-s1", "-t", "task"},
		{"stats", "--json"},
	}},
	// A fresh issue is never stale, so this exercises the GetStaleIssues query +
	// dialect translation and asserts the empty result matches the reference on every
	// backend. The found path (aged updated_at) is covered at the store level by the
	// conformance RunDeferredReads gate, which the CLI can't reach deterministically.
	{name: "stale", steps: [][]string{
		{"create", "st", "--id", "cf-st1", "-t", "task"},
		{"stale", "--days", "1", "--json"},
	}},
	// Promote a wisp to a durable issue (PromoteFromEphemeral) — CLI-level differential
	// for one of the backfilled writes.
	{name: "promote", steps: [][]string{
		{"create", "w", "--id", "cf-pw1", "-t", "task", "--ephemeral"},
		{"promote", "cf-pw1"},
		{"show", "cf-pw1", "--json"},
	}},
	// Rekey an issue id (UpdateIssueID); dependents follow.
	{name: "rename", steps: [][]string{
		{"create", "r1", "--id", "cf-rn1", "-t", "task"},
		{"create", "r2", "--id", "cf-rn2", "-t", "task"},
		{"dep", "add", "cf-rn2", "cf-rn1"},
		{"rename", "cf-rn1", "cf-rn9"},
		{"show", "cf-rn9", "--json"},
	}},
	// Molecule rollup over parent-child children (GetMoleculeProgress): one closed, one open.
	{name: "mol-progress", steps: [][]string{
		{"create", "epic", "--id", "cf-mo1", "-t", "epic"},
		{"create", "a", "--id", "cf-mo1a", "-t", "task", "--parent", "cf-mo1"},
		{"create", "b", "--id", "cf-mo1b", "-t", "task", "--parent", "cf-mo1"},
		{"close", "cf-mo1a"},
		{"mol", "progress", "cf-mo1", "--json"},
	}},
	{name: "update-no-history-demote", steps: [][]string{
		{"create", "d", "--id", "cf-d1", "-t", "task"},
		{"update", "cf-d1", "--no-history", "--json"},
	}},
	// --- audit-derived CLI differential scenarios ---
	// count grouping: --by-priority sends COALESCE(priority,'') (int/text) to the
	// backend; --by-label exercises the IN-subquery + synthetic "(no labels)" bucket.
	// Output {total, groups:[{group,count}]} is sorted by group name, so ordered.
	{name: "count-by-priority-label", steps: [][]string{
		{"create", "c0", "--id", "cf-cnt0", "-t", "task", "-p", "0"},
		{"create", "c1", "--id", "cf-cnt1", "-t", "task", "-p", "1"},
		{"create", "c2", "--id", "cf-cnt2", "-t", "task", "-p", "1"},
		{"count", "--by-priority", "--json"},
		{"label", "add", "cf-cnt0", "bug"},
		{"label", "add", "cf-cnt1", "urgent"},
		{"count", "--by-label", "--json"},
	}},
	// Finding: bd label add + bd label remove roundtrip (DELETE routing + ORDER BY label).
	// zeta added, alpha added, zeta removed -> show should list only the surviving label.
	{name: "audit-label-add-remove", steps: [][]string{
		{"create", "y", "--id", "cf-lr1", "-t", "task"},
		{"label", "add", "cf-lr1", "zeta"},
		{"label", "add", "cf-lr1", "alpha"},
		{"label", "remove", "cf-lr1", "zeta"},
		{"show", "cf-lr1", "--json"},
	}},

	// (bd comments roundtrip is covered at the store level; its output carries a random
	// comment UUIDv7 the CLI harness can't normalize, so it is not a CLI-differential.)
	// Case-variant config key round-trip. `config set` passes the key verbatim to
	// store.SetConfig (no ToLower) and namespaced keys bypass the recognized-key
	// allowlist, so custom.TeamName reaches the store as-is. On the embedded-Dolt
	// reference the key column is case-SENSITIVE (verified at the store layer), so
	// `config get custom.teamname` does NOT see the value set under custom.TeamName,
	// while `config get custom.TeamName` does. config set/get/unset all run on SQL
	// backends. Divergence suspect: real MySQL's case-insensitive default collation
	// would make custom.teamname resolve to the custom.TeamName row.
	{name: "config-case-variant-key", steps: [][]string{
		{"config", "set", "custom.TeamName", "example-org"},
		{"config", "get", "custom.teamname"},
		{"config", "get", "custom.TeamName"},
		{"config", "unset", "custom.TeamName"},
	}},
	// bd create --parent: children auto-mint deterministic ids via GetNextChildID,
	// exercising the CONCAT/LIKE/ON DUPLICATE KEY child-counter SQL at the CLI layer.
	// No --id on the children — the whole point is the minted cf-cp1.1 / cf-cp1.2.
	{name: "create-parent-autoid", unordered: true, steps: [][]string{
		{"create", "epic", "--id", "cf-cp1", "-t", "epic"},
		{"create", "childA", "-t", "task", "--parent", "cf-cp1", "--json"},
		{"create", "childB", "-t", "task", "--parent", "cf-cp1", "--json"},
		{"list", "--json"},
	}},
	// (bd mol wisp list order is non-contractual for equal-second ties — Dolt/PG vs
	// MySQL/SQLite differ on tie order in the nested wisps array, which the harness can't
	// normalize; ListWisps is covered as a set at the store level.)
	// All cli-differential scenarios below were driven end-to-end against the
	// embedded-Dolt reference (bd built with -tags gms_pure_go, `bd init -p cf`) and
	// produce exactly the asserted output. Init prefix is "cf", so every pinned id is cf-*.
	// NOTE: `bd create` REJECTS `--id` together with `--parent` ("cannot specify both
	// --id and --parent flags"); parent-child children get auto-generated dotted ids
	// (cf-pp.1, cf-pp.1.1), which are deterministic across backends. The two parent
	// scenarios therefore pin those dotted ids instead of custom --id values.

	// metadata JSON predicate (JSON_EXTRACT / JSON_UNQUOTE(JSON_EXTRACT(...)) + $.key path).
	// Flags live on `bd list`, not `bd search`. has-key and field=core each return
	// exactly [cf-md1]; field=nope returns []. Only the JSON predicate discriminates the
	// two same-titled rows.
	{name: "search-metadata-json", steps: [][]string{
		{"create", "mdtask one", "--id", "cf-md1", "-t", "task", "--metadata", "{\"team\":\"core\"}"},
		{"create", "mdtask two", "--id", "cf-md2", "-t", "task"},
		{"list", "--has-metadata-key", "team", "--flat", "--json"},
		{"list", "--metadata-field", "team=core", "--flat", "--json"},
		{"list", "--metadata-field", "team=nope", "--flat", "--json"},
	}},

	// bd list --ready --parent <epic>: recursive descendant CTE (WITH RECURSIVE +
	// CONCAT path + LOCATE cycle guard). Returns BOTH the child (cf-pp.1) and the
	// grandchild (cf-pp.1.1) — transitive, not one-hop. Two equal-priority same-second
	// rows, so mark unordered.
	{name: "list-ready-parent-recursive", unordered: true, steps: [][]string{
		{"create", "epic", "--id", "cf-pp", "-t", "epic"},
		{"create", "child", "-t", "task", "--parent", "cf-pp"},
		{"create", "grand", "-t", "task", "--parent", "cf-pp.1"},
		{"list", "--ready", "--parent", "cf-pp", "--flat", "--json"},
	}},

	// bd blocked --json: reads the denormalized is_blocked projection, hydrates
	// BlockedBy/BlockedByCount from live blocking deps. First call returns exactly
	// cf-bk2 (blocked_by=[cf-bk1], count 1); after closing the blocker, blocked is [].
	{name: "blocked-projection", steps: [][]string{
		{"create", "b1", "--id", "cf-bk1", "-t", "task"},
		{"create", "b2", "--id", "cf-bk2", "-t", "task"},
		{"dep", "add", "cf-bk2", "cf-bk1"},
		{"blocked", "--json"},
		{"close", "cf-bk1"},
		{"blocked", "--json"},
	}},

	// bd search --desc-contains (LOWER(description) LIKE) + default closed-exclusion.
	// Lowercase 'endpoint' matches stored 'ENDPOINT' -> [cf-sd1]; plain search hides
	// closed cf-sd3 -> []; --status all surfaces it -> [cf-sd3].
	{name: "search-desc-contains-and-status", steps: [][]string{
		{"create", "alpha", "--id", "cf-sd1", "-t", "task", "--description", "Login ENDPOINT here"},
		{"create", "gamma", "--id", "cf-sd3", "-t", "task"},
		{"close", "cf-sd3"},
		{"search", "alpha", "--desc-contains", "endpoint", "--json"},
		{"search", "gamma", "--json"},
		{"search", "gamma", "--status", "all", "--json"},
	}},

	// bd list --parent <id> --flat: non-recursive ParentID filter (deps parent-child
	// branch OR the id-LIKE CONCAT(?, '.%') shape branch). The dotted child cf-lp.1 is
	// returned; result is exactly [cf-lp.1].
	{name: "list-parent-flat-nonrecursive", steps: [][]string{
		{"create", "lp epic", "--id", "cf-lp", "-t", "epic"},
		{"create", "lp kid", "-t", "task", "--parent", "cf-lp"},
		{"list", "--parent", "cf-lp", "--flat", "--json"},
	}},

	// bd reopen on an already-open issue (RowsAffected()==0 -> AlreadyOpen, exit 0) and
	// defer/close-reason clearing. cf-ro1 already-open reopen is a no-op (status open,
	// closed_at null). cf-ro2 deferred-then-closed-then-reopened clears defer_until and
	// close_reason (status open, closed_at null, defer_until null).
	{name: "reopen-already-open-and-defer-clear", steps: [][]string{
		{"create", "r", "--id", "cf-ro1", "-t", "task"},
		{"reopen", "cf-ro1"},
		{"show", "cf-ro1", "--json"},
		{"create", "r2", "--id", "cf-ro2", "-t", "task"},
		{"update", "cf-ro2", "--defer", "+1h"},
		{"close", "cf-ro2"},
		{"reopen", "cf-ro2"},
		{"show", "cf-ro2", "--json"},
	}},

	// bd mol wisp list / gc --closed --force: wisps-table routing (Ephemeral=true) +
	// closed-wisp cascade delete. First list shows cf-wg1; gc deletes exactly cf-wg1;
	// final list --all is empty. --closed --force avoids the time-dependent age path.
	{name: "wisp-list-gc-closed", steps: [][]string{
		{"create", "w", "--id", "cf-wg1", "-t", "task", "--ephemeral"},
		{"mol", "wisp", "list", "--json"},
		{"close", "cf-wg1"},
		{"mol", "wisp", "gc", "--closed", "--force", "--json"},
		{"mol", "wisp", "list", "--all", "--json"},
	}},

	// bd update --status in_progress: auto-sets started_at (null -> now on first
	// transition). Pre-update started_at is null; post-update status=in_progress and
	// started_at renders as <TS> (normalized), proving the null->present transition.
	{name: "update-status-in-progress-started-at", steps: [][]string{
		{"create", "ip", "--id", "cf-ip1", "-t", "task"},
		{"show", "cf-ip1", "--json"},
		{"update", "cf-ip1", "--status", "in_progress"},
		{"show", "cf-ip1", "--json"},
	}},

	// bd delete --force: inbound text-reference rewrite to [deleted:ID] + inbound dep
	// cleanup across two issues in one transaction. cf-dl2.description becomes
	// "blocks [deleted:cf-dl1] heavily" and its dependency on cf-dl1 is gone.
	{name: "delete-force-reference-rewrite", steps: [][]string{
		{"create", "d1", "--id", "cf-dl1", "-t", "task"},
		{"create", "d2", "--id", "cf-dl2", "-t", "task", "--description", "blocks cf-dl1 heavily"},
		{"dep", "add", "cf-dl2", "cf-dl1"},
		{"delete", "cf-dl1", "--force"},
		{"show", "cf-dl2", "--json"},
	}},

	// bd dep tree <id> --direction both --json: bidirectional graph walk. Pinned as a
	// linear chain cf-dt3 -> cf-dt2 -> cf-dt1 so exactly one child per level and the
	// traversal order is deterministic (depth 0/1/2).
	{name: "dep-tree-both-linear", steps: [][]string{
		{"create", "t1", "--id", "cf-dt1", "-t", "task"},
		{"create", "t2", "--id", "cf-dt2", "-t", "task"},
		{"create", "t3", "--id", "cf-dt3", "-t", "task"},
		{"dep", "add", "cf-dt2", "cf-dt1"},
		{"dep", "add", "cf-dt3", "cf-dt2"},
		{"dep", "tree", "cf-dt3", "--direction", "both", "--json"},
	}},

	// bd note (append-notes write) + bd search --notes-contains (LOWER(notes) LIKE).
	// show reflects the appended notes; lowercase 'flaky' matches stored 'FLAKY' -> [cf-nt1].
	{name: "note-append-and-notes-contains", steps: [][]string{
		{"create", "n", "--id", "cf-nt1", "-t", "task"},
		{"note", "cf-nt1", "Fixed the FLAKY test"},
		{"show", "cf-nt1", "--json"},
		{"search", "n", "--notes-contains", "flaky", "--json"},
	}},
}

// TestConformanceE2E runs every corpus scenario on the reference backend
// (dolt-embedded) and each available candidate, and asserts byte-equal normalized
// output. A candidate divergence is a genuine failure unless the scenario is on that
// profile's XFail allowlist (reported as XFAIL, never masked); an XFAIL that starts
// matching is itself flagged so the allowlist can only shrink.
func TestConformanceE2E(t *testing.T) {
	bin := buildBD(t)
	ref := Reference()
	cands := Candidates()
	if len(cands) == 0 {
		t.Skip("no candidate backends available (set BEADS_PG_TEST_URL for the postgres profile)")
	}
	// Make coverage visible: sqlite's Available() is always true, so a run with
	// BEADS_PG_TEST_URL/BEADS_MYSQL_TEST_URL unset compares only sqlite yet still
	// passes green. Log the candidate set, and honor BEADS_CONFORMANCE_REQUIRE
	// (comma-separated profile names) so CI hard-fails when an expected backend is
	// absent rather than silently narrowing coverage.
	names := make([]string, len(cands))
	for i, c := range cands {
		names[i] = c.Name
	}
	t.Logf("E2E differential candidates: %v (reference=%s)", names, ref.Name)
	if req := strings.TrimSpace(os.Getenv("BEADS_CONFORMANCE_REQUIRE")); req != "" {
		for _, want := range strings.Split(req, ",") {
			if want = strings.TrimSpace(want); want == "" {
				continue
			}
			found := false
			for _, n := range names {
				if n == want {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("BEADS_CONFORMANCE_REQUIRE lists %q but that candidate is unavailable (set its BEADS_*_TEST_URL)", want)
			}
		}
	}
	for _, sc := range corpus {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			refOut := runScenario(t, bin, ref, sc)
			for _, cand := range cands {
				diff := firstDiff(refOut, runScenario(t, bin, cand, sc))
				_, xfail := cand.XFail[sc.name]
				switch {
				case diff == "" && xfail:
					t.Errorf("[%s] %q is XFail (%s) but now MATCHES the reference — remove it from the profile's XFail",
						cand.Name, sc.name, cand.XFail[sc.name])
				case diff == "":
					// pass
				case xfail:
					t.Logf("[%s] XFAIL %q (%s)", cand.Name, sc.name, cand.XFail[sc.name])
				default:
					t.Errorf("[%s] %q diverges from the %s reference:\n%s", cand.Name, sc.name, ref.Name, diff)
				}
			}
		})
	}
}

func runScenario(t *testing.T, bin string, p BackendProfile, sc scenario) []string {
	t.Helper()
	ws := &Workspace{Dir: t.TempDir()}
	if p.NewHandle != nil {
		ws.Handle = p.NewHandle()
	}
	if p.Teardown != nil {
		t.Cleanup(func() { p.Teardown(ws) })
	}
	var env []string
	if p.Env != nil {
		env = p.Env(ws)
	}
	initArgs := append([]string{"init", "-p", "cf", "--quiet"}, p.InitArgs(ws)...)
	if _, stderr, code := runBd(bin, ws.Dir, env, initArgs...); code != 0 {
		t.Fatalf("[%s] bd init failed (exit %d): %s", p.Name, code, stderr)
	}

	var results []string
	for _, step := range sc.steps {
		so, se, code := runBd(bin, ws.Dir, env, step...)
		out := normalize(so, ws)
		if sc.unordered {
			out = sortJSONArrayByID(out)
		}
		results = append(results, fmt.Sprintf("$ bd %s | exit=%d\nout: %s\nerr: %s",
			strings.Join(step, " "), code, out, normalize(se, ws)))
	}
	return results
}

// sortJSONArrayByID canonicalizes a top-level JSON array by sorting its elements on
// "id", so a non-contractual list order does not read as a divergence. Non-array or
// unparseable input is returned unchanged. Both backends are processed identically, so
// re-marshaling's formatting is irrelevant to the comparison.
func sortJSONArrayByID(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "[") {
		return s
	}
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(t), &arr); err != nil {
		return s
	}
	sort.SliceStable(arr, func(i, j int) bool { return jsonID(arr[i]) < jsonID(arr[j]) })
	out, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		return s
	}
	return string(out)
}

func jsonID(raw json.RawMessage) string {
	var m struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.ID
}

var (
	reTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`)
	// A lost/zeroed timestamp (Go zero time 0001-01-01, or Unix epoch 1970-01-01)
	// is timestamp-shaped, so reTimestamp alone would fold it to <TS> and make a
	// backend that drops a timestamp byte-identical to one that preserved it. Map
	// these to a distinct token FIRST so a lost timestamp reads as a divergence.
	reZeroTS = regexp.MustCompile(`(?:0001-01-01|1970-01-01)[T ]00:00:00(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)
	// Onboarding tips are random (probabilistic, BEADS_TIP_SEED-seeded) and not part
	// of the storage contract — strip them so they never cause a false divergence.
	reTip = regexp.MustCompile(`\n*💡 Tip:[^\n]*`)
	// The beads.role config nudge (GH#2950) is written to stderr when the workspace's
	// git config has no beads.role. Whether it fires depends on the runner's git-config
	// state (it appears on CI but not on a dev box), not on the storage backend, so it
	// is cross-env noise like the tip banner rather than a storage divergence. Strip the
	// whole 3-line block so a role nudge on one backend's run never reads as a mismatch.
	reRoleWarn = regexp.MustCompile(`(?m)^warning: beads\.role not configured \(GH#2950\)\.\n(?:^  (?:Fix|Or): .*\n?)*`)
)

// normalize removes cross-backend and cross-run noise: workspace path, schema handle,
// timestamps, and random tip banners. Pinned IDs need no normalization.
func normalize(s string, ws *Workspace) string {
	s = strings.ReplaceAll(s, ws.Dir, "<DIR>")
	if ws.Handle != "" {
		s = strings.ReplaceAll(s, ws.Handle, "<SCHEMA>")
	}
	s = reZeroTS.ReplaceAllString(s, "<ZERO-TS>")
	s = reTimestamp.ReplaceAllString(s, "<TS>")
	s = reTip.ReplaceAllString(s, "")
	s = reRoleWarn.ReplaceAllString(s, "")
	return strings.TrimRight(s, "\n")
}

func firstDiff(ref, cand []string) string {
	n := len(ref)
	if len(cand) < n {
		n = len(cand)
	}
	for i := 0; i < n; i++ {
		if ref[i] != cand[i] {
			return fmt.Sprintf("step %d:\n--- reference ---\n%s\n--- candidate ---\n%s", i, ref[i], cand[i])
		}
	}
	if len(ref) != len(cand) {
		return fmt.Sprintf("step count differs: reference=%d candidate=%d", len(ref), len(cand))
	}
	return ""
}

func runBd(bin, dir string, env []string, args ...string) (stdout, stderr string, code int) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	// Pin the tip RNG so any tip that slips past normalization is at least identical
	// across backends; the profile env may override.
	cmd.Env = append(os.Environ(), "BEADS_TIP_SEED=1")
	cmd.Env = append(cmd.Env, env...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return o.String(), e.String(), ee.ExitCode()
		}
		return o.String(), e.String(), -1
	}
	return o.String(), e.String(), 0
}

var (
	buildOnce sync.Once
	bdBin     string
	bdBinDir  string
	bdErr     error
)

// TestMain removes the shared bd binary after the whole package runs. buildBD is
// process-wide (sync.Once) and shared by every test, so its binary must outlive any
// single test — hence a package-scoped temp dir cleaned here rather than a per-test
// t.TempDir, which Go deletes when its owning test returns.
func TestMain(m *testing.M) {
	code := m.Run()
	if bdBinDir != "" {
		_ = os.RemoveAll(bdBinDir)
	}
	os.Exit(code)
}

// buildBD builds the bd binary once per test process (matching the gms_pure_go tag
// used everywhere else) and returns its absolute path.
func buildBD(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "bd-e2e")
		if err != nil {
			bdErr = fmt.Errorf("mkdir temp for bd binary: %v", err)
			return
		}
		bdBinDir = dir
		bin := filepath.Join(dir, "bd-e2e")
		cmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", bin, "./cmd/bd")
		cmd.Dir = repoRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			bdErr = fmt.Errorf("build bd: %v\n%s", err, out)
			return
		}
		bdBin = bin
	})
	if bdErr != nil {
		t.Fatal(bdErr)
	}
	return bdBin
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0) // <repo>/test/conformance/e2e_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}
