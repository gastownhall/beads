//go:build cgo

package bdhttp_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type exportLine struct {
	Type         string   `json:"_type"`
	ID           string   `json:"id"`
	Ephemeral    bool     `json:"ephemeral"`
	NoHistory    bool     `json:"no_history"`
	WispPlane    *bool    `json:"wisp_plane"`
	Labels       []string `json:"labels"`
	CommentCount int      `json:"comment_count"`
	DepCount     int      `json:"dependency_count"`
	Dependencies []struct {
		IssueID     string `json:"issue_id"`
		DependsOnID string `json:"depends_on_id"`
		Type        string `json:"type"`
	} `json:"dependencies"`
	Comments []struct {
		Text string `json:"text"`
	} `json:"comments"`
	// Key and Value are a memory record's fields.
	Key   string `json:"key"`
	Value string `json:"value"`
}

// plane renders the explicit wisps-plane marker: "absent", "true" or "false".
func (l exportLine) plane() string {
	if l.WispPlane == nil {
		return "absent"
	}
	return strconv.FormatBool(*l.WispPlane)
}

func readExportRecords(t *testing.T, path string) []exportLine {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	var out []exportLine
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec exportLine
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse export line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func readExportLines(t *testing.T, path string) map[string]exportLine {
	t.Helper()
	out := map[string]exportLine{}
	for _, rec := range readExportRecords(t, path) {
		if rec.Type == "issue" {
			out[rec.ID] = rec
		}
	}
	return out
}

// readExportMemories returns an export's memory records, value by key.
func readExportMemories(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, rec := range readExportRecords(t, path) {
		if rec.Type == "memory" {
			out[rec.Key] = rec.Value
		}
	}
	return out
}

func wantExportMemories(t *testing.T, export string, got, want map[string]string) {
	t.Helper()
	if !maps.Equal(got, want) {
		t.Errorf("%s memories = %v, want %v", export, got, want)
	}
}

// TestE2E_ExportOverHTTP is S21. `bd export` read through the raw SearchIssues
// method and the bulk relation reads, which the http backend refuses, so gc's
// JSONL-export order failed on every remote scope. It now composes served role
// reads (issueops.ReadExport): the rows with their labels and counts, every
// row's dependency records and the comment bodies. Memories come from the
// served memory plane (listMemories): the settings enumeration withholds every
// kv.* row, so a remote export that read them there carried none.
//
// The no-history row pins the one place this route still differs from the
// local ones. No served read publishes plane membership, so the row is
// exported without the wisp_plane marker, and a current import pins it to the
// durable issues plane: the data-safe side, since a wrong marker would drop a
// durable row's relations (bd-r9uce). Serving the membership (bd-1lbvas)
// flips those assertions: the row is then marked and re-imports as a wisp.
func TestE2E_ExportOverHTTP(t *testing.T) {
	bd := connectE2EWorkspace(t, "worker")

	blocked := createE2EIssue(t, bd, "blocked work")
	blocker := createE2EIssue(t, bd, "blocker")
	mustRun(t, bd, "dep", "add", blocked, blocker, "--json")
	mustRun(t, bd, "label", "add", blocked, "lane:x", "--json")
	mustRun(t, bd, "comments", "add", blocked, "an export comment")
	closed := createE2EIssue(t, bd, "closed work")
	mustRun(t, bd, "close", closed, "--json")
	wisp := firstJSONField(t, mustRun(t, bd, "create", "a wisp", "--ephemeral", "--json").stdout, "id")
	noHistory := firstJSONField(t, mustRun(t, bd, "create", "a no-history wisp", "--no-history", "--json").stdout, "id")
	memories := map[string]string{
		"export-alpha": "remembered over http",
		"export-beta":  "a second memory",
	}
	for key, content := range memories {
		mustRun(t, bd, "remember", content, "--key", key)
	}

	dir := t.TempDir()
	all := filepath.Join(dir, "all.jsonl")
	mustRun(t, bd, "export", "--all", "-o", all)
	wantExportMemories(t, "bd export --all", readExportMemories(t, all), memories)
	got := readExportLines(t, all)
	for _, id := range []string{blocked, blocker, closed, wisp, noHistory} {
		if _, ok := got[id]; !ok {
			t.Errorf("bd export --all is missing %s; got %d issues", id, len(got))
		}
	}
	rec := got[blocked]
	if len(rec.Dependencies) != 1 || rec.Dependencies[0].DependsOnID != blocker || rec.DepCount != 1 {
		t.Errorf("%s dependencies = %+v (count %d), want one edge onto %s", blocked, rec.Dependencies, rec.DepCount, blocker)
	}
	if len(rec.Labels) != 1 || rec.Labels[0] != "lane:x" {
		t.Errorf("%s labels = %v, want [lane:x]", blocked, rec.Labels)
	}
	if rec.CommentCount != 1 || len(rec.Comments) != 1 || rec.Comments[0].Text != "an export comment" {
		t.Errorf("%s comments = %+v (count %d), want the one comment", blocked, rec.Comments, rec.CommentCount)
	}
	if !got[wisp].Ephemeral {
		t.Errorf("%s exported without ephemeral: %+v", wisp, got[wisp])
	}
	if rec := got[noHistory]; !rec.NoHistory || rec.plane() != "absent" {
		t.Errorf("%s exported as no_history=%v wisp_plane=%s, want no_history with no marker", noHistory, rec.NoHistory, rec.plane())
	}

	// The default export keeps every status and the no-history row, and drops
	// the true wisp.
	def := filepath.Join(dir, "default.jsonl")
	mustRun(t, bd, "export", "-o", def)
	gotDef := readExportLines(t, def)
	if _, ok := gotDef[wisp]; ok {
		t.Errorf("bd export (default) exported the wisp %s", wisp)
	}
	for _, id := range []string{blocked, blocker, closed, noHistory} {
		if _, ok := gotDef[id]; !ok {
			t.Errorf("bd export (default) is missing %s", id)
		}
	}
	if rec := gotDef[noHistory]; rec.plane() != "absent" {
		t.Errorf("bd export (default) stamped %s wisp_plane=%s, want no marker", noHistory, rec.plane())
	}
	// Memories are opt-in: absent by default, present with --include-memories.
	wantExportMemories(t, "bd export (default)", readExportMemories(t, def), map[string]string{})
	withMemories := filepath.Join(dir, "memories.jsonl")
	mustRun(t, bd, "export", "--include-memories", "-o", withMemories)
	wantExportMemories(t, "bd export --include-memories", readExportMemories(t, withMemories), memories)

	// Import the remote export into a fresh local workspace and export it
	// again there. The local route stamps wisp_plane from table membership,
	// which the locally created control row proves, so the imported row's
	// missing marker means it sits in the durable issues table.
	bin := buildBD(t)
	localDir := t.TempDir()
	local := func(args ...string) bdResult {
		t.Helper()
		return runBD(t, bin, localDir, []string{"BEADS_ACTOR=worker"}, args...)
	}
	mustRun(t, local, "init", "--prefix", "e2e", "--quiet")
	mustRun(t, local, "import", "-i", all)
	control := firstJSONField(t, mustRun(t, local, "create", "a local no-history wisp", "--no-history", "--json").stdout, "id")
	back := filepath.Join(dir, "reexport.jsonl")
	mustRun(t, local, "export", "--all", "-o", back)
	gotBack := readExportLines(t, back)
	if rec, ok := gotBack[control]; !ok || rec.plane() != "true" {
		t.Fatalf("local control %s exported with wisp_plane=%s (present=%v), want true", control, rec.plane(), ok)
	}
	wantExportMemories(t, "the local re-export", readExportMemories(t, back), memories)
	if rec, ok := gotBack[noHistory]; !ok {
		t.Errorf("the local re-export is missing the imported %s", noHistory)
	} else if !rec.NoHistory || rec.plane() != "absent" {
		t.Errorf("imported %s re-exported as no_history=%v wisp_plane=%s, want no_history on the durable plane (no marker)", noHistory, rec.NoHistory, rec.plane())
	}
}
