package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestWriteChildRenumberHint is the CLI half of bd-28sb25 AC-2: a failed
// plain pull over colliding children shows the proposed renames and the flag
// that applies them, and says nothing when nothing collides.
func TestWriteChildRenumberHint(t *testing.T) {
	report := &storage.ChildRenumberReport{Renames: []storage.ChildRenumber{
		{From: "bd-p.71", To: "bd-p.74"},
		{From: "bd-p.72", To: "bd-p.75"},
	}}
	var buf bytes.Buffer
	writeChildRenumberHint(&buf, report, "")
	want := `
Local child issues collide with different remote children under the same ids.
Renumbering the local ones would keep the remote ids and move yours:
  bd-p.71 -> bd-p.74
  bd-p.72 -> bd-p.75

To apply this (the plan is re-checked when it runs):
  bd dolt pull --renumber-children
`
	if buf.String() != want {
		t.Errorf("hint =\n%s\nwant\n%s", buf.String(), want)
	}

	buf.Reset()
	writeChildRenumberHint(&buf, report, "peer")
	if !bytes.Contains(buf.Bytes(), []byte("  bd dolt pull peer --renumber-children\n")) {
		t.Errorf("named-remote hint = %q, want the remote in the command", buf.String())
	}

	buf.Reset()
	writeChildRenumberHint(&buf, &storage.ChildRenumberReport{}, "")
	if buf.Len() != 0 {
		t.Errorf("no collisions printed %q, want nothing", buf.String())
	}
}

// TestWriteChildRenumberResult is the CLI half of bd-28sb25 AC-3 and AC-5:
// a renumbering pull names the commit that holds its renames, lists every
// root and descendant mapping and the commits it went through, and says
// "planned, not applied" when it stopped before committing them.
func TestWriteChildRenumberResult(t *testing.T) {
	renames := []storage.ChildRenumber{
		{From: "bd-p.71", To: "bd-p.74"},
		{From: "bd-p.72", To: "bd-p.75"},
		{From: "bd-p.72.1", To: "bd-p.75.1"},
		{From: "bd-p.72.1.3", To: "bd-p.75.1.3"},
	}
	var buf bytes.Buffer
	writeChildRenumberResult(&buf, &storage.ChildRenumberReport{
		LocalHead:    "loc111",
		RemoteHead:   "rem222",
		RenameParent: "loc111",
		RenameCommit: "abc123",
		Head:         "fin333",
		Merged:       true,
		Renames:      renames,
	})
	want := "Renumbered local children (commit abc123):\n" +
		"  bd-p.71 -> bd-p.74\n" +
		"  bd-p.72 -> bd-p.75\n" +
		"  bd-p.72.1 -> bd-p.75.1 (under bd-p.72)\n" +
		"  bd-p.72.1.3 -> bd-p.75.1.3 (under bd-p.72)\n" +
		"  Local head:    loc111\n" +
		"  Remote head:   rem222\n" +
		"  Rename parent: loc111\n" +
		"  Rename commit: abc123\n" +
		"  Final head:    fin333\n"
	if buf.String() != want {
		t.Errorf("result =\n%s\nwant\n%s", buf.String(), want)
	}

	// A retry (AC-6): the kept commit's renames, its parent, and the head the
	// retry started from, which is that commit.
	buf.Reset()
	writeChildRenumberResult(&buf, &storage.ChildRenumberReport{
		LocalHead:    "abc123",
		RemoteHead:   "rem222",
		RenameParent: "loc111",
		RenameCommit: "abc123",
		Retained:     true,
		Renames:      renames[:3],
	})
	want = "Local children already renumbered by an earlier pull (commit abc123, kept):\n" +
		"  bd-p.71 -> bd-p.74\n" +
		"  bd-p.72 -> bd-p.75\n" +
		"  bd-p.72.1 -> bd-p.75.1 (under bd-p.72)\n" +
		"  Local head:    abc123\n" +
		"  Remote head:   rem222\n" +
		"  Rename parent: loc111\n" +
		"  Rename commit: abc123\n"
	if buf.String() != want {
		t.Errorf("retained =\n%s\nwant\n%s", buf.String(), want)
	}

	// Stopped before the rename commit: the same mappings, as a proposal.
	buf.Reset()
	writeChildRenumberResult(&buf, &storage.ChildRenumberReport{
		LocalHead:  "loc111",
		RemoteHead: "rem222",
		Renames:    renames[:3],
	})
	want = "Planned renames, not applied:\n" +
		"  bd-p.71 -> bd-p.74\n" +
		"  bd-p.72 -> bd-p.75\n" +
		"  bd-p.72.1 -> bd-p.75.1 (under bd-p.72)\n" +
		"  Local head:    loc111\n" +
		"  Remote head:   rem222\n"
	if buf.String() != want {
		t.Errorf("proposal =\n%s\nwant\n%s", buf.String(), want)
	}

	buf.Reset()
	writeChildRenumberResult(&buf, &storage.ChildRenumberReport{})
	if want := "No colliding children to renumber.\n"; buf.String() != want {
		t.Errorf("no collisions printed %q, want %q", buf.String(), want)
	}
}

// TestWriteChildRenumberMentionsAndJSON is the CLI half of bd-28sb25 AC-5:
// human output lists the text that still names a renamed id and the commits,
// and --json writes the whole result as one parseable object.
func TestWriteChildRenumberMentionsAndJSON(t *testing.T) {
	report := &storage.ChildRenumberReport{
		Remote:       "origin",
		RenameCommit: "abc123",
		Head:         "def456",
		Merged:       true,
		Renames:      []storage.ChildRenumber{{From: "bd-p.71", To: "bd-p.74"}},
		Mentions: []storage.ChildIDMention{
			{IssueID: "bd-p.73", Field: "comment", CommentID: "c1", ID: "bd-p.71", RenamedTo: "bd-p.74"},
			{IssueID: "bd-p.73", Field: "notes", ID: "bd-p.71", RenamedTo: "bd-p.74"},
		},
	}
	var buf bytes.Buffer
	writeChildRenumberResult(&buf, report)
	want := "Renumbered local children (commit abc123):\n" +
		"  bd-p.71 -> bd-p.74\n" +
		"  Rename commit: abc123\n" +
		"  Final head:    def456\n" +
		"Text naming a renamed id, left as written:\n" +
		"  bd-p.73 comment c1: bd-p.71 (now bd-p.74)\n" +
		"  bd-p.73 notes: bd-p.71 (now bd-p.74)\n"
	if buf.String() != want {
		t.Errorf("result =\n%s\nwant\n%s", buf.String(), want)
	}

	for _, pullErr := range []error{nil, errors.New("merge conflicts in issues")} {
		buf.Reset()
		if err := writeChildRenumberJSON(&buf, report, pullErr); err != nil {
			t.Fatalf("writeChildRenumberJSON: %v", err)
		}
		dec := json.NewDecoder(&buf)
		var got struct {
			Renames      []storage.ChildRenumber  `json:"renames"`
			Mentions     []storage.ChildIDMention `json:"mentions"`
			RenameCommit string                   `json:"rename_commit"`
			Head         string                   `json:"head"`
			Merged       bool                     `json:"merged"`
			Error        string                   `json:"error"`
		}
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if dec.More() {
			t.Errorf("more than one JSON value on stdout")
		}
		if !reflect.DeepEqual(got.Renames, report.Renames) || !reflect.DeepEqual(got.Mentions, report.Mentions) ||
			got.RenameCommit != "abc123" || got.Head != "def456" || !got.Merged {
			t.Errorf("JSON result = %+v, want the report", got)
		}
		wantErr := ""
		if pullErr != nil {
			wantErr = pullErr.Error()
		}
		if got.Error != wantErr {
			t.Errorf("JSON error = %q, want %q", got.Error, wantErr)
		}
	}

	// A failure before the rename commit: the planned renames and the error,
	// still one object, with no commit or head to claim.
	buf.Reset()
	planned := &storage.ChildRenumberReport{
		Remote: "origin", LocalHead: "loc111", RemoteHead: "rem222",
		Renames: []storage.ChildRenumber{{From: "bd-p.71", To: "bd-p.74"}},
	}
	if err := writeChildRenumberJSON(&buf, planned, errors.New("destination occupied")); err != nil {
		t.Fatalf("writeChildRenumberJSON: %v", err)
	}
	dec := json.NewDecoder(&buf)
	var got map[string]any
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.More() {
		t.Errorf("more than one JSON value on stdout")
	}
	if _, ok := got["rename_commit"]; ok {
		t.Errorf("pre-commit failure JSON names a rename commit: %v", got)
	}
	if _, ok := got["head"]; ok {
		t.Errorf("pre-commit failure JSON names a head: %v", got)
	}
	if got["error"] != "destination occupied" || got["merged"] != false || got["local_head"] != "loc111" || got["remote_head"] != "rem222" {
		t.Errorf("pre-commit failure JSON = %v, want the plan, both heads and the error", got)
	}

	// A retry whose merge fails again: the kept commit, its renames and
	// parent, and the error, still one object.
	buf.Reset()
	retained := &storage.ChildRenumberReport{
		Remote: "origin", LocalHead: "abc123", RemoteHead: "rem222",
		RenameParent: "loc111", RenameCommit: "abc123", Retained: true,
		Renames: []storage.ChildRenumber{{From: "bd-p.71", To: "bd-p.74"}},
	}
	if err := writeChildRenumberJSON(&buf, retained, errors.New("merge conflicts in issues")); err != nil {
		t.Fatalf("writeChildRenumberJSON: %v", err)
	}
	dec = json.NewDecoder(&buf)
	got = nil
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.More() {
		t.Errorf("more than one JSON value on stdout")
	}
	if got["retained"] != true || got["rename_commit"] != "abc123" || got["rename_parent"] != "loc111" ||
		got["error"] != "merge conflicts in issues" || got["merged"] != false {
		t.Errorf("retained failure JSON = %v, want the kept commit, its parent and the error", got)
	}
	if renames, _ := got["renames"].([]any); len(renames) != 1 {
		t.Errorf("retained failure JSON renames = %v, want the kept one", got["renames"])
	}
}
