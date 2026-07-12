//go:build e2e

package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The portable JSONL backup path — `bd export` -> git -> `bd import` — is the only
// backup mechanism the non-Dolt backends have (Dolt-native CALL DOLT_BACKUP is
// unsupported on them, and auto-export is gated to Dolt because its change token is
// the Dolt commit hash). The manual round-trip is already lossless on every backend
// (verified live against real Postgres/MySQL); this test pins that so a regression in
// export or import cannot slip through CI.
//
// It is a self-consistency (idempotence) check, not a differential one, so it lives
// outside the `corpus`: for each backend it seeds a rich fixture, exports, restores
// into a FRESH workspace of the same backend, and re-exports. The two exports must be
// identical as a multiset of records — every field value preserved, including the
// timestamps and comment UUIDs a differential comparison would have to normalize away.

// roundTripConfig registers the custom type/status the seed uses. It is replayed into
// the restore workspace on purpose: `bd export` carries issue data only, not config,
// and a custom status is rejected on import ("invalid status: triage") unless
// status.custom is set first. This is backend-agnostic — the Dolt reference rejects it
// identically — and mirrors a real restore, where config is recovered before data.
var roundTripConfig = [][]string{
	{"config", "set", "types.custom", "spike"},
	{"config", "set", "status.custom", "triage"},
}

// roundTripSeed covers every portable field family the backup must preserve: built-in
// and custom issue types, a custom status, metadata JSON, labels, parent-child + blocks
// dependencies, comments, notes, a deferred issue (defer_until), a close_reason, and the
// null->present started_at transition. IDs are pinned so records line up run to run.
//
// Wisps are intentionally absent: they are ephemeral and excluded from the default
// backup export; ListWisps/promotion are covered by the corpus wisp scenarios and the
// in-process store suite.
var roundTripSeed = [][]string{
	{"create", "Epic parent", "--id", "rt-epic", "-t", "epic", "-p", "0", "--notes", "design notes"},
	{"create", "Task child", "--id", "rt-task", "-t", "task", "-p", "1", "-a", "alice", "-l", "backend,urgent", "-d", "the description"},
	{"create", "Bug closed", "--id", "rt-bug", "-t", "bug", "-p", "2"},
	{"create", "Spike custom", "--id", "rt-spike", "-t", "spike", "--metadata", `{"team":"core","k":1}`},
	{"create", "Deferred one", "--id", "rt-defer", "-t", "task"},
	{"dep", "add", "rt-task", "rt-epic", "--type", "parent-child"},
	{"dep", "add", "rt-task", "rt-bug", "--type", "blocks"},
	{"comment", "rt-bug", "first comment"},
	{"comment", "rt-bug", "second comment"},
	{"update", "rt-task", "--status", "in_progress"},
	{"update", "rt-spike", "--status", "triage"},
	{"update", "rt-defer", "--defer", "+720h"},
	{"close", "rt-bug", "--reason", "fixed it"},
}

// roundTripTokens must all appear in the FIRST export. Self-consistency alone can be
// fooled by a symmetric drop — a field lost on both export and import round-trips
// "losslessly" because neither side ever carries it. Asserting the export actually
// captured each field on every backend closes that gap.
var roundTripTokens = []string{
	`"issue_type":"spike"`, `"status":"triage"`, `"team":"core"`,
	`"close_reason":"fixed it"`, `design notes`, `"backend"`, `"urgent"`,
	`first comment`, `second comment`, `parent-child`, `blocks`,
	`"assignee":"alice"`, `defer_until`,
}

// TestExportImportRoundTripE2E runs the export->import->export round-trip on the Dolt
// reference and every available candidate backend, asserting the backup is both
// complete (captures the rich fixture) and lossless (re-export matches the export).
func TestExportImportRoundTripE2E(t *testing.T) {
	bin := buildBD(t)
	profiles := append([]BackendProfile{Reference()}, Candidates()...)
	for _, p := range profiles {
		p := p
		t.Run(p.Name, func(t *testing.T) {
			exp := seedAndExport(t, bin, p)
			for _, tok := range roundTripTokens {
				if !strings.Contains(exp, tok) {
					t.Errorf("[%s] export is missing %q — export dropped a seeded field", p.Name, tok)
				}
			}
			reexp := restoreAndReexport(t, bin, p, exp)
			if a, b := canonicalRecords(exp), canonicalRecords(reexp); a != b {
				t.Errorf("[%s] export->import->export is not lossless:\n%s", p.Name, firstRecordDiff(a, b))
			}
		})
	}
}

// seedAndExport inits a workspace on the backend, applies the config + seed, and
// returns its default `bd export` output.
func seedAndExport(t *testing.T, bin string, p BackendProfile) string {
	t.Helper()
	ws, env := initRoundTripWorkspace(t, bin, p)
	runSteps(t, bin, ws.Dir, env, p.Name, roundTripConfig)
	runSteps(t, bin, ws.Dir, env, p.Name, roundTripSeed)
	out := filepath.Join(ws.Dir, "export.jsonl")
	if _, stderr, code := runBd(bin, ws.Dir, env, "export", "-o", out); code != 0 {
		t.Fatalf("[%s] bd export failed (exit %d): %s", p.Name, code, stderr)
	}
	return mustRead(t, out)
}

// restoreAndReexport inits a SECOND fresh workspace on the same backend, restores the
// config and imports the exported JSONL, then returns its re-export.
func restoreAndReexport(t *testing.T, bin string, p BackendProfile, exportJSONL string) string {
	t.Helper()
	ws, env := initRoundTripWorkspace(t, bin, p)
	runSteps(t, bin, ws.Dir, env, p.Name, roundTripConfig) // config before data (see roundTripConfig)
	in := filepath.Join(ws.Dir, "restore.jsonl")
	mustWrite(t, in, exportJSONL)
	if _, stderr, code := runBd(bin, ws.Dir, env, "import", in); code != 0 {
		t.Fatalf("[%s] bd import failed (exit %d): %s", p.Name, code, stderr)
	}
	out := filepath.Join(ws.Dir, "reexport.jsonl")
	if _, stderr, code := runBd(bin, ws.Dir, env, "export", "-o", out); code != 0 {
		t.Fatalf("[%s] bd re-export failed (exit %d): %s", p.Name, code, stderr)
	}
	return mustRead(t, out)
}

// initRoundTripWorkspace mints an isolated workspace for a backend (fresh temp dir plus
// a fresh handle/schema for the server backends) and runs `bd init`. Prefix "rt" matches
// the pinned rt-* ids in the seed.
func initRoundTripWorkspace(t *testing.T, bin string, p BackendProfile) (*Workspace, []string) {
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
	initArgs := append([]string{"init", "-p", "rt", "--quiet"}, p.InitArgs(ws)...)
	if _, stderr, code := runBd(bin, ws.Dir, env, initArgs...); code != 0 {
		t.Fatalf("[%s] bd init failed (exit %d): %s", p.Name, code, stderr)
	}
	return ws, env
}

func runSteps(t *testing.T, bin, dir string, env []string, backend string, steps [][]string) {
	t.Helper()
	for _, step := range steps {
		if _, stderr, code := runBd(bin, dir, env, step...); code != 0 {
			t.Fatalf("[%s] bd %s failed (exit %d): %s", backend, strings.Join(step, " "), code, stderr)
		}
	}
}

// ephemeralExportKeys are runtime claim/lease fields that `bd export` serializes but
// `bd import` deliberately does not restore: a restored backup must not carry a dead
// worker's stale lease. They are not part of the durable backup contract, so the
// round-trip legitimately drops them on EVERY backend (the Dolt reference included), and
// canonicalRecords strips them before the fidelity comparison.
var ephemeralExportKeys = []string{"lease_expires_at", "heartbeat_at", "row_lock"}

// canonicalRecords normalizes a JSONL export for the round-trip fidelity comparison:
// it drops the ephemeral lease keys from each record and sorts the lines. Record order
// is not part of the backup contract, and each record's durable fields are still
// compared value-for-value (nested arrays are left byte-identical, which holds because
// both sides are the same backend's own export).
func canonicalRecords(s string) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		var rec map[string]json.RawMessage
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			lines = append(lines, l) // non-object line: compare verbatim
			continue
		}
		for _, k := range ephemeralExportKeys {
			delete(rec, k)
		}
		b, err := json.Marshal(rec) // map marshal emits keys in sorted order
		if err != nil {
			lines = append(lines, l)
			continue
		}
		lines = append(lines, string(b))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// firstRecordDiff reports the first differing record between two sorted exports, so a
// failure points at the exact record and field rather than dumping both files.
func firstRecordDiff(a, b string) string {
	as, bs := strings.Split(a, "\n"), strings.Split(b, "\n")
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		if as[i] != bs[i] {
			return fmt.Sprintf("first diff at record %d:\n--- export ---\n%s\n--- re-export ---\n%s", i, as[i], bs[i])
		}
	}
	if len(as) != len(bs) {
		return fmt.Sprintf("record count differs: export=%d re-export=%d", len(as), len(bs))
	}
	return "(records identical)"
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
