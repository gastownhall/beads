//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// exportProxiedFixture is the seeded corpus for TestProxiedServerExport. It
// deliberately spans what the export must carry (mirroring the protocol
// interchange fixture): priorities incl. P0, several types, a closed issue
// with a reason, labels, comments, both dependency orientations
// (parent-child and blocks), metadata, an assignee, an ephemeral wisp that
// default export must EXCLUDE, an infra-type (message) bead, a promoted
// no-history wisp (durable issues-table row still carrying no_history=true —
// the bd-r9uce plane-classification trap, with relations), and memories.
type exportProxiedFixture struct {
	critical string
	epic     string
	child    string
	blocker  string
	done     string
	wisp     string
	infraMsg string
	// promoted is the promoted no-history wisp: a DURABLE issues-table row
	// whose no_history flag is still set (wild pre-fix promote shape).
	// promotedFriend is durable and blocked by it, so the round-trip oracle
	// also pins that the relation survives — pre-fix, classic import
	// re-planed the row into the wisps table by its flags and dropped this
	// edge as a cross-bucket dependency.
	promoted       string
	promotedFriend string
}

func seedProxiedExportFixture(t *testing.T, bd string, p proxiedProject) exportProxiedFixture {
	t.Helper()
	var fx exportProxiedFixture

	fx.critical = bdProxiedCreateSilent(t, bd, p.dir, "P0 critical bug",
		"--type", "bug", "--priority", "0",
		"--description", "Line one\nline two — unicode: ✓",
		"--assignee", "alice",
		"--design", "Design body",
		"--acceptance", "All green",
		"--notes", "Notes body")
	// Metadata keys are deliberately the SAME length. JSON object key order
	// is canonicalized differently by the two stacks — the proxied update
	// path re-marshals metadata through a Go map (alphabetical key order),
	// while the embedded engine normalizes its JSON column to MySQL
	// binary-JSON order (key length first, then bytes) on read-back — and the
	// two orders coincide exactly when all keys share one length. This is a
	// pre-existing write-path/engine divergence visible to every read command
	// (`bd show --json` shows it too), not an export behavior; with
	// mixed-length keys the cross-mode byte-compare below would fail on it.
	bdProxiedUpdateOne(t, bd, p.dir, fx.critical, "--metadata", `{"risk":"high","comp":"auth"}`)

	fx.epic = bdProxiedCreateSilent(t, bd, p.dir, "Epic parent", "--type", "epic", "--priority", "1")
	fx.child = bdProxiedCreateSilent(t, bd, p.dir, "Child task", "--type", "task", "--priority", "2", "--parent", fx.epic)
	fx.blocker = bdProxiedCreateSilent(t, bd, p.dir, "Blocker", "--type", "chore", "--priority", "3")
	if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", fx.child, fx.blocker); err != nil {
		t.Fatalf("dep add: %v\n%s", err, out)
	}

	for _, label := range []string{"zeta", "alpha"} {
		if out, err := bdProxiedRun(t, bd, p.dir, "label", "add", fx.child, label); err != nil {
			t.Fatalf("label add %s: %v\n%s", label, err, out)
		}
	}
	for _, text := range []string{"first comment", "second comment"} {
		if out, err := bdProxiedRun(t, bd, p.dir, "comment", fx.child, text); err != nil {
			t.Fatalf("comment: %v\n%s", err, out)
		}
	}

	fx.done = bdProxiedCreateSilent(t, bd, p.dir, "Finished feature", "--type", "feature", "--priority", "4")
	if out, err := bdProxiedRun(t, bd, p.dir, "close", fx.done, "--reason", "shipped in abc123"); err != nil {
		t.Fatalf("close: %v\n%s", err, out)
	}

	// Promoted no-history wisp (bd-r9uce): a durable issues-table row still
	// carrying no_history=true, with durable-plane relations. `bd promote` is
	// not yet ported to proxied mode (bd-27bfd) — and the fixed promote now
	// clears the flag — so produce the wild pre-fix shape directly: durable
	// row, no_history flipped on. In the round trip below, classic import
	// must route it by the (absent) explicit plane marker and keep it — and
	// its inbound edge — in the durable plane.
	fx.promoted = bdProxiedCreateSilent(t, bd, p.dir, "Promoted no-history wisp", "--type", "task", "--priority", "3")
	if out, err := bdProxiedRun(t, bd, p.dir, "label", "add", fx.promoted, "keepme"); err != nil {
		t.Fatalf("label add: %v\n%s", err, out)
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "comment", fx.promoted, "survived promotion"); err != nil {
		t.Fatalf("comment: %v\n%s", err, out)
	}
	fx.promotedFriend = bdProxiedCreateSilent(t, bd, p.dir, "Promoted wisp friend", "--type", "task", "--priority", "3")
	if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", fx.promotedFriend, fx.promoted); err != nil {
		t.Fatalf("dep add: %v\n%s", err, out)
	}
	db := openProxiedDB(t, p)
	if _, err := db.Exec("UPDATE issues SET no_history = 1 WHERE id = ?", fx.promoted); err != nil {
		t.Fatalf("flip no_history: %v", err)
	}

	// Ephemeral wisp: excluded from default export, included with --all.
	fx.wisp = bdProxiedCreateSilent(t, bd, p.dir, "Ephemeral heartbeat", "--ephemeral", "--wisp-type", "heartbeat")
	// Relations on the wisp: the --all export must carry them, which is what
	// pins the UOW plane partition (only wisp ids reach the wisp readers,
	// wy-237yfi) against the classic-mode oracle.
	if out, err := bdProxiedRun(t, bd, p.dir, "label", "add", fx.wisp, "wisp-label"); err != nil {
		t.Fatalf("label add on wisp: %v\n%s", err, out)
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "comment", fx.wisp, "wisp comment"); err != nil {
		t.Fatalf("comment on wisp: %v\n%s", err, out)
	}

	// Infra-typed bead (message): auto-routed to the ephemeral plane and an
	// infra type, so default export must exclude it and --all include it.
	fx.infraMsg = bdProxiedCreateSilent(t, bd, p.dir, "Infra message", "--type", "message")

	// Memories: `bd remember` is not yet ported to proxied-server mode, so
	// seed the kv.memory.* config rows directly (same table, same rows).
	for key, value := range map[string]string{
		"kv.memory.zebra":    "last memory by key",
		"kv.memory.aardvark": "first memory by key",
	} {
		if out, err := bdProxiedRun(t, bd, p.dir, "config", "set", key, value); err != nil {
			t.Fatalf("config set %s: %v\n%s", key, err, out)
		}
	}

	return fx
}

// exportLines parses a JSONL stream, failing the test on any line that is not
// a JSON object — which is also the stdout-purity assertion when the stream
// came from stdout.
func exportLines(t *testing.T, stream string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for i, line := range strings.Split(stream, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d is not JSON (%v): %q", i+1, err, line)
		}
		out = append(out, m)
	}
	return out
}

func exportIssueIDs(records []map[string]any) map[string]bool {
	ids := make(map[string]bool)
	for _, r := range records {
		if r["_type"] == "issue" {
			if id, ok := r["id"].(string); ok {
				ids[id] = true
			}
		}
	}
	return ids
}

// exportIssueRecordByID returns the issue record with the given id, or nil.
func exportIssueRecordByID(records []map[string]any, id string) map[string]any {
	for _, r := range records {
		if r["_type"] == "issue" && r["id"] == id {
			return r
		}
	}
	return nil
}

// exportStrings coerces a JSON array field to []string.
func exportStrings(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// exportCommentTexts extracts the text field of each comment object in order.
func exportCommentTexts(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			if s, ok := m["text"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func exportMemoryKeys(records []map[string]any) []string {
	var keys []string
	for _, r := range records {
		if r["_type"] == "memory" {
			if k, ok := r["key"].(string); ok {
				keys = append(keys, k)
			}
		}
	}
	return keys
}

// firstStreamDiff renders the first differing line of two JSONL streams
// (cmd/bd-package analog of protocol's diffStreams, which lives in another
// test package and cannot be imported).
func firstStreamDiff(want, got string) string {
	wl := strings.Split(strings.TrimRight(want, "\n"), "\n")
	gl := strings.Split(strings.TrimRight(got, "\n"), "\n")
	var b strings.Builder
	if len(wl) != len(gl) {
		b.WriteString("line count differs: proxied=" + strconv.Itoa(len(wl)) +
			" classic=" + strconv.Itoa(len(gl)) + "\n")
	}
	n := min(len(wl), len(gl))
	for i := range n {
		if wl[i] != gl[i] {
			b.WriteString("first differing line " + strconv.Itoa(i+1) +
				":\n  proxied: " + wl[i] + "\n  classic: " + gl[i] + "\n")
			return b.String()
		}
	}
	if len(wl) > n {
		b.WriteString("only in proxied: " + wl[n] + "\n")
	}
	if len(gl) > n {
		b.WriteString("only in classic: " + gl[n] + "\n")
	}
	return b.String()
}

// runClassic runs the (embedded-mode) bd binary in dir with the classic env.
func runClassic(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	out, err := bdRunWithFlockRetry(t, bd, dir, args...)
	if err != nil {
		t.Fatalf("classic bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestProxiedServerExport(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "pxe")
	fx := seedProxiedExportFixture(t, bd, p)

	durable := []string{fx.critical, fx.epic, fx.child, fx.blocker, fx.done}

	t.Run("default_excludes_wisps_infra_memories", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "export")
		if err != nil {
			t.Fatalf("bd export: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		records := exportLines(t, stdout) // fails on any non-JSON stdout line
		ids := exportIssueIDs(records)
		for _, id := range durable {
			if !ids[id] {
				t.Errorf("default export missing durable issue %s", id)
			}
		}
		if ids[fx.wisp] {
			t.Errorf("default export must exclude ephemeral wisp %s", fx.wisp)
		}
		if ids[fx.infraMsg] {
			t.Errorf("default export must exclude infra-typed bead %s", fx.infraMsg)
		}
		if keys := exportMemoryKeys(records); len(keys) != 0 {
			t.Errorf("default export must not carry memories, got %v", keys)
		}
		if strings.Contains(stderr, "Exported") {
			t.Errorf("summary must not print without -o; stderr:\n%s", stderr)
		}
	})

	// Direct content oracle: the relational data must actually APPEAR in the
	// export. The byte-identity round trip below is blind to symmetric
	// omissions — a proxied read bug that drops a whole relation produces an
	// impoverished stream that classic faithfully imports and re-exports, so
	// bytes still match. These assertions pin the seeded content itself.
	t.Run("relations_content_present", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "export")
		if err != nil {
			t.Fatalf("bd export: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		records := exportLines(t, stdout)
		child := exportIssueRecordByID(records, fx.child)
		if child == nil {
			t.Fatalf("export missing %s", fx.child)
		}
		if labels := exportStrings(child["labels"]); len(labels) != 2 || labels[0] != "alpha" || labels[1] != "zeta" {
			t.Errorf("child labels = %v, want [alpha zeta]", labels)
		}
		if texts := exportCommentTexts(child["comments"]); len(texts) != 2 || texts[0] != "first comment" || texts[1] != "second comment" {
			t.Errorf("child comments = %v, want [first comment, second comment]", texts)
		}
		if cc, _ := child["comment_count"].(float64); int(cc) != 2 {
			t.Errorf("child comment_count = %v, want 2", child["comment_count"])
		}
		deps, _ := child["dependencies"].([]any)
		dependsOn := map[string]bool{}
		for _, d := range deps {
			if m, ok := d.(map[string]any); ok {
				if id, ok := m["depends_on_id"].(string); ok {
					dependsOn[id] = true
				}
			}
		}
		if len(deps) != 2 || !dependsOn[fx.epic] || !dependsOn[fx.blocker] {
			t.Errorf("child dependencies depend on %v, want both %s and %s", dependsOn, fx.epic, fx.blocker)
		}
		// Dependency COUNTS tally only 'blocks' edges (both stacks:
		// domain/db/dependency.go:396, classic GetDependencyCountsInTx), so
		// the parent-child edge appears in `dependencies` but not the count.
		if dc, _ := child["dependency_count"].(float64); int(dc) != 1 {
			t.Errorf("child dependency_count = %v, want 1 (blocks edge only)", child["dependency_count"])
		}
		blocker := exportIssueRecordByID(records, fx.blocker)
		if blocker == nil {
			t.Fatalf("export missing %s", fx.blocker)
		}
		if dc, _ := blocker["dependent_count"].(float64); int(dc) != 1 {
			t.Errorf("blocker dependent_count = %v, want 1", blocker["dependent_count"])
		}
	})

	// The plane-classification trap from review: a promoted no-history wisp
	// is a durable issues-table row that still carries no_history=true — the
	// wild pre-fix promote shape — and its labels/comments live in the
	// DURABLE plane. Classifying by row flags looks them up in the wisp plane
	// and silently drops them; classification must follow table membership.
	// Since bd-r9uce the shape lives IN the round-trip fixture (fx.promoted):
	// import now routes by the explicit "wisp_plane" marker instead of the
	// flags, so the cross_mode_byte_identical oracle below covers it too.
	// The export half pinned here: relations present, and NO plane marker on
	// the record (it is durable — stamping it would re-plane it on import).
	t.Run("promoted_no_history_relations", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "export")
		if err != nil {
			t.Fatalf("bd export: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		records := exportLines(t, stdout)
		rec := exportIssueRecordByID(records, fx.promoted)
		if rec == nil {
			t.Fatalf("export missing promoted no-history row %s (durable table membership must include it)", fx.promoted)
		}
		if noHist, _ := rec["no_history"].(bool); !noHist {
			t.Fatalf("promoted row lost no_history in export: %v", rec)
		}
		if _, marked := rec["wisp_plane"]; marked {
			t.Errorf("promoted row carries the wisps-plane marker; it is durable and must not be stamped: %v", rec)
		}
		if labels := exportStrings(rec["labels"]); len(labels) != 1 || labels[0] != "keepme" {
			t.Errorf("promoted labels = %v, want [keepme] (durable-plane labels dropped?)", labels)
		}
		if texts := exportCommentTexts(rec["comments"]); len(texts) != 1 || texts[0] != "survived promotion" {
			t.Errorf("promoted comments = %v, want [survived promotion] (durable-plane comments dropped?)", texts)
		}
		if cc, _ := rec["comment_count"].(float64); int(cc) != 1 {
			t.Errorf("promoted comment_count = %v, want 1", rec["comment_count"])
		}
		if dc, _ := rec["dependent_count"].(float64); int(dc) != 1 {
			t.Errorf("promoted dependent_count = %v, want 1 (inbound edge from friend)", rec["dependent_count"])
		}
		friend := exportIssueRecordByID(records, fx.promotedFriend)
		if friend == nil {
			t.Fatalf("export missing %s", fx.promotedFriend)
		}
		deps, _ := friend["dependencies"].([]any)
		if len(deps) != 1 {
			t.Fatalf("friend dependencies = %v, want the one edge onto the promoted row", friend["dependencies"])
		}
		if edge, ok := deps[0].(map[string]any); !ok || edge["depends_on_id"] != fx.promoted {
			t.Errorf("friend dependency edge = %v, want depends_on_id=%s", deps[0], fx.promoted)
		}
	})

	t.Run("dash_arg_streams_to_stdout", func(t *testing.T) {
		// `bd export --all -` streams to stdout: the literal `-` is a
		// positional no-op, NOT an output path (and `-o -` would mean a file
		// named "-"). Pinned so the port preserves the quirk.
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "export", "--all", "-")
		if err != nil {
			t.Fatalf("bd export --all -: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if len(exportLines(t, stdout)) == 0 {
			t.Fatal("expected JSONL on stdout")
		}
	})

	t.Run("all_includes_wisps_infra_memories", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "export", "--all")
		if err != nil {
			t.Fatalf("bd export --all: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		records := exportLines(t, stdout)
		ids := exportIssueIDs(records)
		for _, id := range append(append([]string{}, durable...), fx.wisp, fx.infraMsg) {
			if !ids[id] {
				t.Errorf("--all export missing %s", id)
			}
		}
		keys := exportMemoryKeys(records)
		if len(keys) != 2 || keys[0] != "aardvark" || keys[1] != "zebra" {
			t.Errorf("--all export memories = %v, want [aardvark zebra] (sorted)", keys)
		}
		// The wisp's own relations ride along: a misclassified plane would
		// drop them silently while the id above still passes.
		for _, rec := range records {
			if rec["id"] != fx.wisp {
				continue
			}
			labels, _ := rec["labels"].([]any)
			if len(labels) != 1 || labels[0] != "wisp-label" {
				t.Errorf("--all export wisp %s labels = %v, want [wisp-label]", fx.wisp, rec["labels"])
			}
			comments, _ := rec["comments"].([]any)
			if len(comments) != 1 {
				t.Errorf("--all export wisp %s comments = %v, want one", fx.wisp, rec["comments"])
			}
		}
	})

	t.Run("include_memories_after_issues_sorted", func(t *testing.T) {
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "export", "--include-memories")
		if err != nil {
			t.Fatalf("bd export --include-memories: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		records := exportLines(t, stdout)
		ids := exportIssueIDs(records)
		if ids[fx.wisp] {
			t.Errorf("--include-memories must not include the ephemeral wisp")
		}
		keys := exportMemoryKeys(records)
		if len(keys) != 2 || keys[0] != "aardvark" || keys[1] != "zebra" {
			t.Errorf("memories = %v, want [aardvark zebra] (sorted by key)", keys)
		}
		// All memory records come AFTER all issue records.
		seenMemory := false
		for _, r := range records {
			switch r["_type"] {
			case "memory":
				seenMemory = true
			case "issue":
				if seenMemory {
					t.Fatalf("issue record after memory record; memories must trail all issues")
				}
			}
		}
	})

	t.Run("output_file_atomic_summary_on_stderr", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "export.jsonl")
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "export", "-o", path, "--include-memories")
		if err != nil {
			t.Fatalf("bd export -o: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Errorf("with -o, stdout must be empty, got:\n%s", stdout)
		}
		if !strings.Contains(stderr, "Exported") || !strings.Contains(stderr, "2 memories") {
			t.Errorf("expected 'Exported N issues and 2 memories' summary on stderr, got:\n%s", stderr)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("export file not written: %v", err)
		}
	})

	// The acceptance oracle: the proxied export, imported into a fresh
	// CLASSIC (embedded) workspace and re-exported there, must reproduce the
	// proxied stream byte for byte — same records, same field encodings, same
	// ordering. Mirrors protocol's §J6.6 round-trip, across storage modes.
	t.Run("cross_mode_byte_identical", func(t *testing.T) {
		proxiedPath := filepath.Join(t.TempDir(), "proxied.jsonl")
		if _, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "export", "-o", proxiedPath, "--include-memories"); err != nil {
			t.Fatalf("proxied export: %v\n%s", err, stderr)
		}
		proxiedBytes, err := os.ReadFile(proxiedPath)
		if err != nil {
			t.Fatal(err)
		}

		classicDir, _, _ := bdInit(t, bd, "--prefix", "cls")
		incoming := filepath.Join(classicDir, "incoming.jsonl")
		if err := os.WriteFile(incoming, proxiedBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		runClassic(t, bd, classicDir, "import", "-i", incoming)

		classicPath := filepath.Join(classicDir, "classic.jsonl")
		runClassic(t, bd, classicDir, "export", "-o", classicPath, "--include-memories")
		classicBytes, err := os.ReadFile(classicPath)
		if err != nil {
			t.Fatal(err)
		}

		if string(proxiedBytes) != string(classicBytes) {
			t.Errorf("proxied and classic exports differ:\n%s",
				firstStreamDiff(string(proxiedBytes), string(classicBytes)))
		}

		// Same oracle for the default (no-memories) shape both from the
		// proxied store and from the classic store now holding the same data.
		proxiedDefault, _, err := bdProxiedRunBuffers(t, bd, p.dir, "export")
		if err != nil {
			t.Fatalf("proxied default export: %v", err)
		}
		classicDefaultPath := filepath.Join(classicDir, "classic-default.jsonl")
		runClassic(t, bd, classicDir, "export", "-o", classicDefaultPath)
		classicDefault, err := os.ReadFile(classicDefaultPath)
		if err != nil {
			t.Fatal(err)
		}
		if proxiedDefault != string(classicDefault) {
			t.Errorf("default-shape exports differ across modes:\n%s",
				firstStreamDiff(proxiedDefault, string(classicDefault)))
		}
	})
}
