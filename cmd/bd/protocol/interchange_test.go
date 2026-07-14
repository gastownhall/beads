package protocol

// Interchange contract tests (PROPOSAL-beads-protocol-v0 §J1/§J2/§J6).
//
// These pin the JSONL interchange at the CLI level: what a conforming reader
// must tolerate (unknown fields, header records, dangling dep targets), what
// import must never do (delete), which streams it must accept (stdin), and the
// keystone round-trip invariant §J6.6 — export → import into an EMPTY store →
// export must reproduce the stream. bd-go had same-DB byte-stability tests but
// no cross-store equivalence test, which is where an import that silently drops
// or rewrites a field hides.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Interchange helpers
// ---------------------------------------------------------------------------

// exportJSONL runs `bd export -o <file>` and returns the file's bytes as a
// string. Export-to-file (not stdout) is deliberate: the summary line goes to
// stderr and the atomic-write path is the one real callers use.
func (w *workspace) exportJSONL(name string, extra ...string) string {
	w.t.Helper()
	path := filepath.Join(w.dir, name)
	args := append([]string{"export", "-o", path}, extra...)
	w.run(args...)
	data, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		w.t.Fatalf("read export %s: %v", path, err)
	}
	return string(data)
}

// importStream writes stream to a file in the workspace and imports it,
// returning bd's combined output.
func (w *workspace) importStream(name, stream string, extra ...string) string {
	w.t.Helper()
	path := filepath.Join(w.dir, name)
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		w.t.Fatalf("write %s: %v", path, err)
	}
	args := append([]string{"import", "-i", path}, extra...)
	return w.run(args...)
}

// runStdin runs bd with stream on stdin and returns stdout only (stdout is
// where --json summaries land; stderr carries human progress lines).
func (w *workspace) runStdin(stream string, args ...string) string {
	w.t.Helper()
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = w.dir
	cmd.Env = w.env()
	cmd.Stdin = strings.NewReader(stream)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		w.t.Fatalf("bd %s (stdin): %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return string(out)
}

// importJSONSummary imports a stream with --json and returns the parsed
// summary object (source/created/skipped/skipped_dependencies/...).
func (w *workspace) importJSONSummary(name, stream string) map[string]any {
	w.t.Helper()
	path := filepath.Join(w.dir, name)
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		w.t.Fatalf("write %s: %v", path, err)
	}
	cmd := exec.Command(w.bd, "import", "-i", path, "--json")
	cmd.Dir = w.dir
	cmd.Env = w.env()
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		w.t.Fatalf("bd import --json: %v\n%s", err, stderr)
	}
	items := parseJSONOutput(w.t, string(out))
	if len(items) == 0 {
		w.t.Fatalf("bd import --json produced no summary object:\n%s", out)
	}
	return items[0]
}

// listAllIDs returns the set of issue ids in the store, all statuses.
func (w *workspace) listAllIDs() map[string]bool {
	w.t.Helper()
	items := parseJSONOutput(w.t, w.run("list", "--all", "--json", "-n", "0"))
	ids := make(map[string]bool, len(items))
	for _, it := range items {
		if id, ok := it["id"].(string); ok {
			ids[id] = true
		}
	}
	return ids
}

// diffStreams renders the first differing line of two JSONL streams.
func diffStreams(t *testing.T, want, got string) string {
	t.Helper()
	wl := strings.Split(strings.TrimRight(want, "\n"), "\n")
	gl := strings.Split(strings.TrimRight(got, "\n"), "\n")
	var b strings.Builder
	if len(wl) != len(gl) {
		b.WriteString("line count differs: original=" +
			strconv.Itoa(len(wl)) + " re-export=" + strconv.Itoa(len(gl)) + "\n")
	}
	n := min(len(wl), len(gl))
	for i := range n {
		if wl[i] != gl[i] {
			b.WriteString("first differing line " + strconv.Itoa(i+1) + ":\n  original:  " + wl[i] + "\n  re-export: " + gl[i] + "\n")
			return b.String()
		}
	}
	if len(wl) > n {
		b.WriteString("only in original:  " + wl[n] + "\n")
	}
	if len(gl) > n {
		b.WriteString("only in re-export: " + gl[n] + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// §J6.6 — the keystone round-trip invariant
// ---------------------------------------------------------------------------

// TestProtocol_Interchange_RoundTripEquivalence pins §J6.6: export → import
// into an empty conforming store → export MUST reproduce the original stream.
//
// bd-go had same-DB byte-stability (export twice from one store) and per-field
// round-trip tests, but nothing asserted CROSS-STORE equivalence — so an import
// that dropped, defaulted, or rewrote a field (or a relational row) could not be
// caught. The fixture deliberately spans the fields the interchange must carry:
// P0 (the zero-value priority §J2.1), every core issue_type, a closed issue with
// close_reason, labels, inline comments, both dependency orientations
// (parent-child and blocks), metadata, assignee, and memories (§J6.4).
func TestProtocol_Interchange_RoundTripEquivalence(t *testing.T) {
	t.Parallel()
	src := newWorkspace(t)

	critical := src.create("--title", "P0 critical bug", "--type", "bug", "--priority", "0",
		"--description", "Line one\nline two — unicode: ✓", "--assignee", "alice",
		"--design", "Design body", "--acceptance", "All green", "--notes", "Notes body")
	src.run("update", critical, "--metadata", `{"component":"auth","risk":"high"}`)

	epic := src.create("--title", "Epic parent", "--type", "epic", "--priority", "1")
	child := src.create("--title", "Child task", "--type", "task", "--priority", "2", "--parent", epic)
	blocker := src.create("--title", "Blocker", "--type", "chore", "--priority", "3")
	src.run("dep", "add", child, blocker)

	src.run("label", "add", child, "zeta")
	src.run("label", "add", child, "alpha")
	src.run("comments", "add", child, "first comment")
	src.run("comments", "add", child, "second comment")

	done := src.create("--title", "Finished feature", "--type", "feature", "--priority", "4")
	src.run("close", done, "--reason", "shipped in abc123")

	src.run("remember", "--key", "zebra", "last memory by key")
	src.run("remember", "--key", "aardvark", "first memory by key")

	original := src.exportJSONL("original.jsonl", "--include-memories")
	if strings.Count(original, "\n") < 7 {
		t.Fatalf("fixture export looks too small (%d lines):\n%s", strings.Count(original, "\n"), original)
	}

	// A fresh workspace is an EMPTY conforming store with its own prefix — the
	// import must carry the foreign ids and every field verbatim.
	dst := newWorkspace(t)
	if got := dst.listAllIDs(); len(got) != 0 {
		t.Fatalf("destination store is not empty: %v", got)
	}
	dst.importStream("incoming.jsonl", original)

	reexport := dst.exportJSONL("reexport.jsonl", "--include-memories")

	if reexport != original {
		t.Errorf("§J6.6 violated: export → import → export did not reproduce the stream\n%s",
			diffStreams(t, original, reexport))
	}
}

// ---------------------------------------------------------------------------
// §J1.2 — forward compatibility (unknown fields ignored)
// ---------------------------------------------------------------------------

// TestProtocol_Interchange_UnknownFieldsTolerated pins §J1.2: readers MUST
// ignore unknown fields on any record. A future bd emitting new fields must not
// break an older reader — so a stream carrying fields this build has never heard
// of (at the top level, inside a dependency object, and inside a comment) still
// imports cleanly, with the known fields intact.
func TestProtocol_Interchange_UnknownFieldsTolerated(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	target := w.prefix + "-fut1"
	future := w.prefix + "-fut2"

	stream := strings.Join([]string{
		`{"_type":"issue","id":"` + target + `","title":"Dep target","status":"open","issue_type":"task","priority":2,` +
			`"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
		`{"_type":"issue","id":"` + future + `","title":"From the future","status":"open","issue_type":"task","priority":1,` +
			`"created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z",` +
			`"labels":["known"],` +
			`"dependencies":[{"issue_id":"` + future + `","depends_on_id":"` + target + `","type":"blocks",` +
			`"created_at":"2026-01-02T00:00:00Z","quantum_flux":"dep-level unknown"}],` +
			`"comments":[{"id":"c1","issue_id":"` + future + `","author":"bob","text":"hello",` +
			`"created_at":"2026-01-02T00:00:00Z","sentiment":"comment-level unknown"}],` +
			`"telepathy_score":42,"future_object":{"nested":["a","b"]},"future_list":[1,2,3]}`,
		"",
	}, "\n")

	w.importStream("future.jsonl", stream)

	issue := w.showJSONFull(future)
	assertField(t, issue, "title", "From the future")
	assertField(t, issue, "status", "open")
	assertFieldFloat(t, issue, "priority", 1)
	requireStringSetEqual(t, getStringSlice(issue, "labels"), []string{"known"},
		"labels survived a record carrying unknown fields")
	requireDepEdgesEqual(t, getObjectSlice(issue, "dependencies"),
		[]depEdge{{issueID: future, dependsOnID: target}},
		"dependency survived a dep object carrying an unknown field")
	requireCommentTextsEqual(t, getObjectSlice(issue, "comments"), []string{"hello"},
		"comment survived a comment object carrying an unknown field")
}

// ---------------------------------------------------------------------------
// §J1.3 — header record skipped by the `bd import` reader
// ---------------------------------------------------------------------------

// TestProtocol_Interchange_SchemaHeaderSkipped pins §J1.3: a reader MUST skip
// any record bearing `_schema`.
//
// This was a real gap, not a hypothetical: the bootstrap reader (parseJSONLFile)
// skipped the header, but the `bd import` reader loop did not — a canonical
// export with a provenance header aborted the WHOLE import with "title is
// required", because the header line fell through to the issue path and
// unmarshaled into an empty Issue. Fixed alongside this test (wy-ltox1).
func TestProtocol_Interchange_SchemaHeaderSkipped(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.prefix + "-hdr1"

	stream := `{"_schema":"beads-jsonl/1","_dolt_branch":"main","_sort":"stable-v1"}` + "\n" +
		`{"_type":"issue","id":"` + id + `","title":"After the header","status":"open","issue_type":"task",` +
		`"priority":2,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}` + "\n"

	t.Run("file_import", func(t *testing.T) {
		w.importStream("headered.jsonl", stream)
		assertField(t, w.showJSON(id), "title", "After the header")
	})

	t.Run("stdin_import", func(t *testing.T) {
		// Same reader loop, second entry point — and a header is exactly what a
		// piped canonical export carries.
		stdinID := w.prefix + "-hdr2"
		piped := strings.Replace(stream, id, stdinID, -1)
		w.runStdin(piped, "import", "-")
		assertField(t, w.showJSON(stdinID), "title", "After the header")
	})
}

// ---------------------------------------------------------------------------
// §J6.1 — import never deletes
// ---------------------------------------------------------------------------

// TestProtocol_Interchange_ImportNeverDeletes pins §J6.1: import is upsert-only.
// Absence of an id from the stream implies nothing — an import of a partial
// stream MUST NOT delete the local issues it omits. (Doc-stated in
// SYNC_CONCEPTS; no test held the line, and a "make the store match the file"
// regression would silently destroy every bead the file predates.)
func TestProtocol_Interchange_ImportNeverDeletes(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	kept := w.create("--title", "Not in the stream", "--type", "task", "--priority", "2")
	updated := w.create("--title", "In the stream", "--type", "task", "--priority", "2")

	// A stream mentioning ONLY `updated` (with a strictly newer updated_at so the
	// §J6.2 stale guard lets it land).
	stream := `{"_type":"issue","id":"` + updated + `","title":"Retitled by import","status":"open",` +
		`"issue_type":"task","priority":2,"created_at":"2026-01-01T00:00:00Z",` +
		`"updated_at":"2099-01-01T00:00:00Z"}` + "\n"
	w.importStream("partial.jsonl", stream)

	ids := w.listAllIDs()
	if !ids[kept] {
		t.Errorf("§J6.1 violated: issue %s was absent from the imported stream and no longer exists (store now: %v)", kept, ids)
	}
	assertField(t, w.showJSON(updated), "title", "Retitled by import")
	assertField(t, w.showJSON(kept), "title", "Not in the stream")
}

// ---------------------------------------------------------------------------
// §J6.3 — tolerant linking (missing dep target skipped AND reported)
// ---------------------------------------------------------------------------

// TestProtocol_Interchange_MissingDepTargetSkippedAndReported pins §J6.3: a
// dependency edge whose target id is absent MUST NOT fail the import; it is
// skipped and REPORTED. Both halves matter — silently swallowing the edge would
// let a partial sync quietly lose the graph.
func TestProtocol_Interchange_MissingDepTargetSkippedAndReported(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.prefix + "-dangl"
	missing := w.prefix + "-nope9"

	stream := `{"_type":"issue","id":"` + id + `","title":"Has a dangling dep","status":"open",` +
		`"issue_type":"task","priority":2,"created_at":"2026-01-01T00:00:00Z",` +
		`"updated_at":"2026-01-01T00:00:00Z","dependencies":[{"issue_id":"` + id + `",` +
		`"depends_on_id":"` + missing + `","type":"blocks","created_at":"2026-01-01T00:00:00Z"}]}` + "\n"

	summary := w.importJSONSummary("dangling.jsonl", stream)

	// The issue lands (import did not fail)...
	assertField(t, w.showJSON(id), "title", "Has a dangling dep")

	// ...the edge does not...
	if deps := getObjectSlice(w.showJSON(id), "dependencies"); len(deps) != 0 {
		t.Errorf("§J6.3: dependency on absent target %s should be skipped, got %v", missing, deps)
	}

	// ...and the skip is reported, naming the target.
	reported := getStringSlice(summary, "skipped_dependencies")
	if len(reported) == 0 {
		t.Fatalf("§J6.3 violated: skipped dependency was not reported in bd import --json (summary: %v)", summary)
	}
	if !strings.Contains(strings.Join(reported, "\n"), missing) {
		t.Errorf("§J6.3: skipped_dependencies does not name the missing target %s: %v", missing, reported)
	}
}

// ---------------------------------------------------------------------------
// §J6.5 — streams (stdin import, stdout export)
// ---------------------------------------------------------------------------

// TestProtocol_Interchange_StdinImport pins §J6.5: import MUST accept stdin
// (`bd import -`) — the `bd export | ssh host bd import -` pipe is the
// interchange's whole point, and it was untested.
func TestProtocol_Interchange_StdinImport(t *testing.T) {
	t.Parallel()
	src := newWorkspace(t)
	src.create("--title", "Piped issue", "--type", "task", "--priority", "1")
	stream := src.exportJSONL("piped.jsonl")

	dst := newWorkspace(t)
	dst.runStdin(stream, "import", "-")

	if got := len(dst.listAllIDs()); got != 1 {
		t.Fatalf("bd import - : expected 1 issue in the destination store, got %d", got)
	}
	if dst.exportJSONL("dst.jsonl") != stream {
		t.Errorf("§J6.5: stream imported from stdin did not re-export identically")
	}
}

// ---------------------------------------------------------------------------
// §J2.4 — epoch sanitize (GH#2488)
// ---------------------------------------------------------------------------

// TestProtocol_Interchange_EpochSanitize pins §J2.4: zero/unknown times are
// represented as the Unix epoch (1970-01-01T00:00:00Z), never as year-0001.
// A Go zero time.Time marshals to year 0001, which json.Marshal REFUSES ("year
// outside of range [0,9999]") — so without the sanitizer a single row with a
// NULL timestamp fails the entire export. The reachable CLI-level trigger is an
// import record with no timestamps (§J2.5 makes them optional).
func TestProtocol_Interchange_EpochSanitize(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.prefix + "-notime"

	// No created_at / updated_at at all.
	stream := `{"_type":"issue","id":"` + id + `","title":"Timeless","status":"open","issue_type":"task","priority":2}` + "\n"
	w.importStream("timeless.jsonl", stream)

	out := w.exportJSONL("timeless-out.jsonl")
	line := ""
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(l, `"`+id+`"`) {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("issue %s missing from export:\n%s", id, out)
	}

	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("export line is not valid JSON: %v\n%s", err, line)
	}
	for _, field := range []string{"created_at", "updated_at"} {
		ts, _ := rec[field].(string)
		if strings.HasPrefix(ts, "0001-") {
			t.Errorf("§J2.4 violated: %s exported as year-0001 (%q) — must be the Unix epoch", field, ts)
		}
		if ts == "" {
			t.Errorf("§J2.4: %s missing from the export record (§J2.1 requires it)", field)
		}
	}
}
