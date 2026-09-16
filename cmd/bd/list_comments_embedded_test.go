//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestEmbeddedListComments drives `bd list --json` end to end, which is the
// entry point be-73x is about: the town-wide duplicate/prior-art search this
// repo's consumers run is a `bd list --json` piped to jq, and until this
// change it could not see a comment body at all.
//
// IT RUNS THE REAL COMMAND ON PURPOSE. internal/workapi's unit tests cover the
// same hydration against a fake source with rows the test builds itself, and a
// fixture a test constructs proves what the test constructed. The rows here
// are produced by `bd create` and `bd comment` and read back by `bd list`, so
// a hydration wired to the wrong reader, a flag that never reaches the
// request, or a marshaling tag that drops the field all fail here and none of
// them fail upstairs.
func TestEmbeddedListComments(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "lc")

	// The phrase lives ONLY in a comment. That is the whole point: title,
	// description and notes are the fields a listing already carried, so a
	// term found in any of them would pass with or without this change.
	const commentOnlyPhrase = "zzzcommentonlyphrasezzz"

	commented := bdCreate(t, bd, dir, "Row with comments", "--type", "task")
	bare := bdCreate(t, bd, dir, "Row with no comments", "--type", "task")

	if out, err := bdRunWithFlockRetry(t, bd, dir, "comment", commented.ID, "ROOT CAUSE: "+commentOnlyPhrase); err != nil {
		t.Fatalf("bd comment: %v\n%s", err, out)
	}
	if out, err := bdRunWithFlockRetry(t, bd, dir, "comment", commented.ID, "second comment"); err != nil {
		t.Fatalf("bd comment: %v\n%s", err, out)
	}

	rowsByID := func(t *testing.T, args ...string) map[string]map[string]any {
		t.Helper()
		out, err := bdRunWithFlockRetry(t, bd, dir, append([]string{"list", "--json", "--status", "all"}, args...)...)
		if err != nil {
			t.Fatalf("bd list --json %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		s := string(out)
		start := strings.Index(s, "[")
		if start < 0 {
			t.Fatalf("no JSON array in list output:\n%s", s)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(s[start:]), &rows); err != nil {
			t.Fatalf("parse list JSON: %v\n%s", err, s[start:])
		}
		byID := make(map[string]map[string]any, len(rows))
		for _, r := range rows {
			id, _ := r["id"].(string)
			byID[id] = r
		}
		// A denominator, so a vacuous pass is visible: every assertion below
		// reads a specific row, and a page that lost the rows would otherwise
		// look like a page that merely lacks the field.
		if len(byID) < 2 {
			t.Fatalf("list returned %d rows, want at least the 2 seeded", len(byID))
		}
		return byID
	}

	t.Run("default_marks_the_hole_instead_of_hiding_it", func(t *testing.T) {
		byID := rowsByID(t)

		row := byID[commented.ID]
		if _, present := row["comments"]; present {
			t.Error("default listing carried a comments field; comment bodies are opt-in")
		}
		if omitted, _ := row["comments_omitted"].(bool); !omitted {
			t.Errorf("comments_omitted = %v on a row with comments, want true: an absent comments field must not read as none", row["comments_omitted"])
		}
		if count, _ := row["comment_count"].(float64); count != 2 {
			t.Errorf("comment_count = %v, want 2", row["comment_count"])
		}

		// The control. A marker on every row would carry no information.
		if _, present := byID[bare.ID]["comments_omitted"]; present {
			t.Errorf("comments_omitted present on a row with no comments: a true empty stays plain omission")
		}
	})

	t.Run("include_comments_makes_the_bodies_searchable", func(t *testing.T) {
		byID := rowsByID(t, "--include-comments")

		row := byID[commented.ID]
		comments, _ := row["comments"].([]any)
		if len(comments) != 2 {
			t.Fatalf("hydrated %d comments, want 2: %v", len(comments), row["comments"])
		}
		// Assert on the TEXT, not the length. The defect was that the bodies
		// were unreachable, and a count cannot tell a populated slice from a
		// slice of empty ones.
		var joined strings.Builder
		for _, c := range comments {
			m, _ := c.(map[string]any)
			text, _ := m["text"].(string)
			joined.WriteString(text)
		}
		if !strings.Contains(joined.String(), commentOnlyPhrase) {
			t.Errorf("hydrated comment text %q does not contain %q", joined.String(), commentOnlyPhrase)
		}
		if _, present := row["comments_omitted"]; present {
			t.Error("comments_omitted present beside populated comments: the two fields must read as one answer")
		}
		if _, present := byID[bare.ID]["comments"]; present {
			t.Error("a row with no comments carried a comments field")
		}
	})

	// The acceptance test as an agent actually runs it: grep the serialized
	// page for a phrase that exists only in a comment. This is the assertion
	// that fails on today's binary, and it fails with a plausible non-zero
	// hit count elsewhere in the page rather than with an error, which is why
	// it is worth pinning rather than trusting the field checks above.
	t.Run("a_content_search_over_the_page_finds_a_comment_only_phrase", func(t *testing.T) {
		withoutFlag, err := bdRunWithFlockRetry(t, bd, dir, "list", "--json", "--status", "all")
		if err != nil {
			t.Fatalf("bd list --json: %v\n%s", err, withoutFlag)
		}
		if strings.Contains(string(withoutFlag), commentOnlyPhrase) {
			t.Error("default listing leaked comment text; bodies are supposed to be opt-in")
		}

		withFlag, err := bdRunWithFlockRetry(t, bd, dir, "list", "--json", "--status", "all", "--include-comments")
		if err != nil {
			t.Fatalf("bd list --json --include-comments: %v\n%s", err, withFlag)
		}
		if !strings.Contains(string(withFlag), commentOnlyPhrase) {
			t.Errorf("a search over the whole page missed a phrase that exists only in a comment; this is be-73x")
		}
	})

	t.Run("text_output_is_untouched", func(t *testing.T) {
		plain := bdList(t, bd, dir, "--status", "all")
		withFlag := bdList(t, bd, dir, "--status", "all", "--include-comments")
		if plain != withFlag {
			t.Errorf("--include-comments changed text output.\nwithout:\n%s\nwith:\n%s", plain, withFlag)
		}
		if strings.Contains(withFlag, commentOnlyPhrase) {
			t.Error("text listing printed a comment body")
		}
	})
}
