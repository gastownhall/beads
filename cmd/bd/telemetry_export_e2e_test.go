//go:build cgo && unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedTelemetryExportCarriesNoIssueContent is #8505 end to end. It runs
// the verbs that carry issue content with telemetry on and the console
// exporters selected (BD_OTEL_ENABLED=true BD_OTEL_STDOUT=true), every title,
// description, note, label, comment, search string, reason and commit message
// a unique marker, and requires that no marker appears anywhere on stderr,
// where the exporters write every span, span event, metric and the resource.
// The root span's bd.args must be the command path and the flag names alone.
//
// Before the fix, bd.args carried the command line verbatim and the storage
// spans carried search text (bd.query) and commit messages (db.commit_msg).
func TestEmbeddedTelemetryExportCarriesNoIssueContent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "otx", "--skip-hooks", "--skip-agents")

	// An inherited OTEL_* variable could disable the SDK or reshape the export.
	plain := bdEnv(dir)
	var env []string
	for _, kv := range plain {
		if !strings.HasPrefix(kv, "OTEL_") {
			env = append(env, kv)
		}
	}
	env = append(env, "BD_OTEL_ENABLED=true", "BD_OTEL_STDOUT=true")

	var markers []string
	marker := func(field string) string {
		m := "zq8505" + field + "mk"
		markers = append(markers, m)
		return m
	}
	title, desc, notes := marker("title"), marker("desc"), marker("notes")
	label1, label2 := marker("labelone"), marker("labeltwo")
	design, acceptance := marker("design"), marker("acceptance")
	newTitle, newDesc, moreNotes, label3 := marker("newtitle"), marker("newdesc"), marker("morenotes"), marker("labelthree")
	comment1, comment2, label4 := marker("commentone"), marker("commenttwo"), marker("labelfour")
	batchMessage, batchTitle, batchRetitle := marker("batchmessage"), marker("batchtitle"), marker("batchretitle")
	closeReason := marker("closereason")

	// exported runs bd with telemetry on and returns its decoded export,
	// after checking that no marker reached stderr and that the command span
	// carries wantArgs as bd.args.
	exported := func(t *testing.T, spanName, wantArgs string, args ...string) (stdout string, export []map[string]any) {
		t.Helper()
		stdout, stderr := runBDOut(t, bd, dir, env, args...)
		lower := strings.ToLower(stderr)
		for _, m := range markers {
			if strings.Contains(lower, m) {
				t.Errorf("CONTENT LEAK: bd %s exported %q:\n%s", args[0], m, excerptAround(stderr, m))
			}
		}
		export = decodeConsoleExport(stderr)
		span := findExportedSpan(export, spanName)
		if span == nil {
			t.Fatalf("bd %s: no %s span in the export:\n%s", strings.Join(args, " "), spanName, stderr)
		}
		if got := exportedSpanAttr(span, "bd.args"); got != wantArgs {
			t.Errorf("bd %s: bd.args = %q, want %q", args[0], got, wantArgs)
		}
		return stdout, export
	}

	stdout, export := exported(t, "bd.command.create", "create --acceptance --description --design --labels --notes --silent",
		"create", title, "-d", desc, "--notes", notes, "-l", label1+","+label2,
		"--design", design, "--acceptance", acceptance, "--silent")
	id := strings.TrimSpace(stdout)
	if id == "" {
		t.Fatal("bd create --silent printed no issue id")
	}
	if !exportHasMetrics(export) {
		t.Errorf("bd create exported no metrics; the check above proves nothing about them")
	}

	exported(t, "bd.command.update", "update --add-label --append-notes --description --title",
		"update", id, "--title", newTitle, "--description", newDesc, "--append-notes", moreNotes, "--add-label", label3)
	if out, _ := exported(t, "bd.command.list", "list --json --title", "list", "--json", "--title", newTitle); !strings.Contains(out, id) {
		t.Errorf("bd list --title %s did not find %s; the export it is checked against searched for nothing:\n%s", newTitle, id, out)
	}
	if out, _ := exported(t, "bd.command.search", "search --json", "search", "--json", newTitle); !strings.Contains(out, id) {
		t.Errorf("bd search %s did not find %s; the export it is checked against searched for nothing:\n%s", newTitle, id, out)
	}
	exported(t, "bd.command.comment", "comment", "comment", id, comment1)
	exported(t, "bd.command.add", "comments add", "comments", "add", id, comment2)
	exported(t, "bd.command.add", "label add", "label", "add", id, label4)
	exported(t, "bd.command.show", "show --json", "show", "--json", id)

	batchFile := filepath.Join(t.TempDir(), "ops.txt")
	ops := fmt.Sprintf("create task 2 %q\nupdate %s title=%q\n", batchTitle, id, batchRetitle)
	if err := os.WriteFile(batchFile, []byte(ops), 0o600); err != nil {
		t.Fatal(err)
	}
	exported(t, "bd.command.batch", "batch --file --message", "batch", "--message", batchMessage, "--file", batchFile)
	exported(t, "bd.command.close", "close --reason", "close", id, "--reason", closeReason)

	// The markers really are in the database: the export is clean because
	// telemetry left them out, not because the commands dropped them.
	stdout, _ = runBDOut(t, bd, dir, plain, "show", "--json", id)
	comments, _ := runBDOut(t, bd, dir, plain, "comments", id, "--json")
	stdout = strings.ToLower(stdout + comments)
	for _, m := range []string{batchRetitle, newDesc, notes, moreNotes, label1, label2, label3, label4, design, acceptance, comment1, comment2, closeReason} {
		if !strings.Contains(stdout, m) {
			t.Errorf("bd show/comments %s lacks %q, so its absence from the export proves nothing:\n%s", id, m, stdout)
		}
	}
	if stdout, _ = runBDOut(t, bd, dir, plain, "list", "--json", "--title", batchTitle); !strings.Contains(stdout, "otx-") {
		t.Errorf("bd batch did not create %q, so its absence from the export proves nothing:\n%s", batchTitle, stdout)
	}
}

// decodeConsoleExport returns the JSON objects the console exporters wrote to
// stderr: one per span (pretty-printed) and one per metrics export, each
// starting at the beginning of a line. Other stderr lines are skipped.
func decodeConsoleExport(stderr string) []map[string]any {
	var objects []map[string]any
	for pos := 0; pos < len(stderr); {
		start := strings.Index(stderr[pos:], "{")
		if start < 0 {
			break
		}
		start += pos
		if start > 0 && stderr[start-1] != '\n' {
			pos = start + 1
			continue
		}
		dec := json.NewDecoder(strings.NewReader(stderr[start:]))
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			pos = start + 1
			continue
		}
		objects = append(objects, obj)
		pos = start + int(dec.InputOffset())
	}
	return objects
}

func findExportedSpan(export []map[string]any, name string) map[string]any {
	for _, obj := range export {
		if _, isSpan := obj["SpanContext"]; isSpan && obj["Name"] == name {
			return obj
		}
	}
	return nil
}

// exportedSpanAttr returns a string attribute of an exported span, or
// "<absent>".
func exportedSpanAttr(span map[string]any, key string) string {
	attrs, _ := span["Attributes"].([]any)
	for _, a := range attrs {
		kv, _ := a.(map[string]any)
		if kv["Key"] != key {
			continue
		}
		value, _ := kv["Value"].(map[string]any)
		if s, ok := value["Value"].(string); ok {
			return s
		}
	}
	return "<absent>"
}

func exportHasMetrics(export []map[string]any) bool {
	for _, obj := range export {
		if _, ok := obj["ScopeMetrics"]; ok {
			return true
		}
	}
	return false
}

// excerptAround returns the stderr lines around the first case-insensitive
// occurrence of marker, so a failure shows which attribute carried it.
func excerptAround(stderr, marker string) string {
	lines := strings.Split(stderr, "\n")
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), marker) {
			lo, hi := max(0, i-6), min(len(lines), i+3)
			return strings.Join(lines[lo:hi], "\n")
		}
	}
	return ""
}
