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

// TestWriteChildRenumberResult is the CLI half of bd-28sb25 AC-3: a
// renumbering pull names the commit that holds its renames and lists them.
func TestWriteChildRenumberResult(t *testing.T) {
	var buf bytes.Buffer
	writeChildRenumberResult(&buf, &storage.ChildRenumberReport{
		RenameCommit: "abc123",
		Renames: []storage.ChildRenumber{
			{From: "bd-p.71", To: "bd-p.74"},
			{From: "bd-p.72", To: "bd-p.75"},
		},
	})
	want := "Renumbered local children (commit abc123):\n  bd-p.71 -> bd-p.74\n  bd-p.72 -> bd-p.75\n"
	if buf.String() != want {
		t.Errorf("result =\n%s\nwant\n%s", buf.String(), want)
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
}
