//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestProxiedServerListComments is the second half of the be-73x coverage, and
// it exists because the fix has TWO implementations rather than one.
//
// `bd list --json` reaches issueops.Reader.List through the store-backed
// reader on the direct route and through the unit-of-work reader here. Those
// are separate bodies, and this repo has already had them answer one contract
// method differently (see the epilogue comments in both). The embedded test
// covers the first; removing the wiring from the second changes nothing there
// and everything here, so a green suite without this case would say the fix
// landed when half of it had not.
//
// The wisp-plane argument is also live only on this seam: the store-backed
// detail source routes an id to the right comment table itself and ignores the
// flag, while this one is told.
func TestProxiedServerListComments(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "plc")

	const commentOnlyPhrase = "zzzproxiedcommentphrasezzz"

	commented := bdProxiedCreate(t, bd, p.dir, "Proxied row with comments", "--type", "task")
	bare := bdProxiedCreate(t, bd, p.dir, "Proxied row with no comments", "--type", "task")

	if out, err := bdProxiedRun(t, bd, p.dir, "comment", commented.ID, "ROOT CAUSE: "+commentOnlyPhrase); err != nil {
		t.Fatalf("bd comment: %v\n%s", err, out)
	}

	rowsByID := func(t *testing.T, args ...string) map[string]map[string]any {
		t.Helper()
		full := append([]string{"list", "--json", "--status", "all"}, args...)
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, full...)
		if err != nil {
			t.Fatalf("bd list --json %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
		}
		start := strings.Index(stdout, "[")
		if start < 0 {
			t.Fatalf("no JSON array in list output:\n%s", stdout)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(stdout[start:]), &rows); err != nil {
			t.Fatalf("parse list JSON: %v\n%s", err, stdout[start:])
		}
		byID := make(map[string]map[string]any, len(rows))
		for _, r := range rows {
			id, _ := r["id"].(string)
			byID[id] = r
		}
		// The denominator, so a page that lost its rows cannot pass as a page
		// that merely lacks the field.
		if len(byID) < 2 {
			t.Fatalf("list returned %d rows, want at least the 2 seeded", len(byID))
		}
		return byID
	}

	t.Run("default_marks_the_hole", func(t *testing.T) {
		byID := rowsByID(t)
		if omitted, _ := byID[commented.ID]["comments_omitted"].(bool); !omitted {
			t.Errorf("comments_omitted = %v on a row with comments, want true", byID[commented.ID]["comments_omitted"])
		}
		if _, present := byID[commented.ID]["comments"]; present {
			t.Error("default listing carried comment bodies; they are opt-in")
		}
		if _, present := byID[bare.ID]["comments_omitted"]; present {
			t.Error("comments_omitted present on a row with no comments")
		}
	})

	t.Run("include_comments_hydrates_through_the_unit_of_work_reader", func(t *testing.T) {
		byID := rowsByID(t, "--include-comments")
		comments, _ := byID[commented.ID]["comments"].([]any)
		if len(comments) != 1 {
			t.Fatalf("hydrated %d comments, want 1: %v", len(comments), byID[commented.ID]["comments"])
		}
		m, _ := comments[0].(map[string]any)
		text, _ := m["text"].(string)
		if !strings.Contains(text, commentOnlyPhrase) {
			t.Errorf("comment text = %q, want it to contain %q", text, commentOnlyPhrase)
		}
		if _, present := byID[commented.ID]["comments_omitted"]; present {
			t.Error("comments_omitted present beside populated comments")
		}
	})

	t.Run("a_content_search_over_the_page_finds_a_comment_only_phrase", func(t *testing.T) {
		without, _, err := bdProxiedRunBuffers(t, bd, p.dir, "list", "--json", "--status", "all")
		if err != nil {
			t.Fatalf("bd list --json: %v", err)
		}
		if strings.Contains(without, commentOnlyPhrase) {
			t.Error("default listing leaked comment text")
		}
		with, _, err := bdProxiedRunBuffers(t, bd, p.dir, "list", "--json", "--status", "all", "--include-comments")
		if err != nil {
			t.Fatalf("bd list --json --include-comments: %v", err)
		}
		if !strings.Contains(with, commentOnlyPhrase) {
			t.Error("a search over the whole page missed a comment-only phrase on the proxied route; this is be-73x")
		}
	})
}
