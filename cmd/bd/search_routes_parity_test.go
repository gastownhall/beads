//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestSearchRoutesAgree pins that `bd search` answers the same flags the same
// way on all three routes — the direct store, the proxied server and a remote
// backend — because all three build ONE request (searchListRequest over
// issueops.SearchListRequest) and ask ONE library entry with it
// (issueops.Reader.List). A route that mapped a flag on its own, or reached a
// different read, would answer differently here.
//
// The proxied and remote routes read the SAME database (the remote one is
// `bd serve` over the proxied workspace; bd serve needs a SQL server, so it
// cannot serve the embedded one), so their answers must be identical row for
// row, ids and order included. The direct route has its own embedded database
// with the same seed, so the other two are compared with it on the rows'
// titles and statuses.
//
// The proxied and remote legs need BEADS_TEST_PROXIED_SERVER=1 (the shared
// Dolt container). The remote leg also needs a reader that serves Query
// (S6c's externaldeps reader passthrough); without it the remote route
// refuses out loud, which is asserted, and its comparison is skipped.
func TestSearchRoutesAgree(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)

	type seed struct {
		title string
		args  []string
		close bool
	}
	// Distinct priorities among the "routed" rows so a --limit cut is the same
	// rows on every route regardless of creation-time ties.
	seeds := []seed{
		{"Routed alpha task", []string{"--type", "task", "--priority", "0", "--assignee", "alice", "--label", "lane:a"}, false},
		{"Routed beta bug", []string{"--type", "bug", "--priority", "1", "--assignee", "bob", "--label", "lane:b"}, false},
		{"re-ROUTED gamma", []string{"--type", "feature", "--priority", "2", "--label", "lane:a"}, false},
		{"Routed and done", []string{"--type", "task", "--priority", "3"}, true},
		{"unrelated delta", []string{"--type", "task", "--priority", "4", "--label", "lane:a"}, false},
	}
	queries := []struct {
		name    string
		args    []string
		ordered bool // compare in output order (the flags fix one)
		wireCan bool // listIssues can carry every flag
	}{
		{"text", []string{"routed"}, false, true},
		{"status open", []string{"routed", "--status", "open"}, false, true},
		{"status all", []string{"routed", "--status", "all"}, false, true},
		{"type", []string{"routed", "--type", "bug"}, false, true},
		{"label", []string{"routed", "--label", "lane:a"}, false, true},
		{"assignee", []string{"routed", "--assignee", "alice"}, false, true},
		{"limit", []string{"routed", "--limit", "2"}, false, true},
		{"sort title", []string{"routed", "--sort", "title"}, true, true},
		{"sort priority reverse", []string{"routed", "--sort", "priority", "--reverse"}, true, true},
		{"query flag", []string{"--query", "gamma"}, false, true},
		{"no match", []string{"nothing-matches-this"}, false, true},
		{"priority-max", []string{"routed", "--priority-max", "1"}, false, false},
	}

	type row struct{ ID, Title, Status string }
	decode := func(t *testing.T, out string) []row {
		t.Helper()
		s := strings.TrimSpace(out)
		start := strings.Index(s, "[")
		if start < 0 {
			return nil
		}
		var rows []row
		if err := json.Unmarshal([]byte(s[start:]), &rows); err != nil {
			t.Fatalf("parse search JSON: %v\n%s", err, s)
		}
		return rows
	}
	run := func(t *testing.T, dir string, env []string, args ...string) (string, string, int) {
		t.Helper()
		cmd := exec.Command(bd, append([]string{"search", "--json"}, args...)...)
		cmd.Dir = dir
		cmd.Env = env
		stdout, stderr, err := runCommandBuffers(t, cmd)
		code := 0
		if err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("running bd search: %v", err)
			}
			code = ee.ExitCode()
		}
		return stdout.String(), stderr.String(), code
	}
	// comparable projects rows onto what two databases with the same seed
	// share; unordered answers are compared as sorted sets.
	comparable := func(rows []row, ordered bool) []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.Title+"|"+r.Status)
		}
		if !ordered {
			slices.Sort(out)
		}
		return out
	}

	// --- direct ---
	directDir, _, _ := bdInit(t, bd, "--prefix", "sp")
	for _, s := range seeds {
		issue := bdCreate(t, bd, directDir, append([]string{s.title}, s.args...)...)
		if s.close {
			bdClose(t, bd, directDir, issue.ID)
		}
	}
	direct := map[string][]row{}
	for _, q := range queries {
		stdout, stderr, code := run(t, directDir, bdEnv(directDir), q.args...)
		if code != 0 {
			t.Fatalf("direct bd search %v failed (exit %d): %s", q.args, code, stderr)
		}
		direct[q.name] = decode(t, stdout)
	}
	// The seed is what the comparison is about, so pin that the direct route
	// answered the obvious cases at all.
	if got := len(direct["text"]); got != 4 {
		t.Fatalf("direct `bd search routed` returned %d rows, want the 4 routed ones (closed included): %+v", got, direct["text"])
	}

	// --- proxied, then remote over the proxied database ---
	if os.Getenv("BEADS_TEST_PROXIED_SERVER") != "1" {
		t.Skip("proxied and remote legs not run: set BEADS_TEST_PROXIED_SERVER=1 (direct leg ran)")
	}
	p := newSharedProxiedProject(t, bd, "sp")
	for _, s := range seeds {
		issue := bdProxiedCreate(t, bd, p.dir, append([]string{s.title}, s.args...)...)
		if s.close {
			bdProxiedClose(t, bd, p.dir, issue.ID)
		}
	}
	proxied := map[string][]row{}
	t.Run("proxied", func(t *testing.T) {
		for _, q := range queries {
			stdout, stderr, code := run(t, p.dir, bdProxiedEnv(p.dir), q.args...)
			if code != 0 {
				t.Errorf("proxied bd search %v failed (exit %d): %s", q.args, code, stderr)
				continue
			}
			proxied[q.name] = decode(t, stdout)
			got, want := comparable(proxied[q.name], q.ordered), comparable(direct[q.name], q.ordered)
			if !slices.Equal(got, want) {
				t.Errorf("%s: proxied answered %v, direct answered %v", q.name, got, want)
			}
		}
	})

	t.Run("remote", func(t *testing.T) {
		sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))
		remoteDir := t.TempDir()
		connect := exec.Command(bd, "connect", "http://"+sp.addr, "--json")
		connect.Dir = remoteDir
		connect.Env = bdEnv(remoteDir)
		if out, err := connect.CombinedOutput(); err != nil {
			t.Fatalf("bd connect: %v\n%s", err, out)
		}
		for _, q := range queries {
			stdout, stderr, code := run(t, remoteDir, bdEnv(remoteDir), q.args...)
			if code != 0 && strings.Contains(stderr, "SearchIssuesWithCounts") {
				t.Skip("needs S6c (externaldeps reader passthrough); the remote route refused out loud: " + strings.TrimSpace(stderr))
			}
			if !q.wireCan {
				if code == 0 {
					t.Errorf("%s: remote bd search %v succeeded; listIssues cannot carry it, so it must refuse", q.name, q.args)
				}
				continue
			}
			if code != 0 {
				t.Errorf("remote bd search %v failed (exit %d): %s", q.args, code, stderr)
				continue
			}
			// Same database as the proxied route: identical rows, ids and
			// order included.
			got := decode(t, stdout)
			if want := proxied[q.name]; !slices.Equal(got, want) {
				t.Errorf("%s: remote answered %+v, proxied answered %+v", q.name, got, want)
			}
			if gotC, wantC := comparable(got, q.ordered), comparable(direct[q.name], q.ordered); !slices.Equal(gotC, wantC) {
				t.Errorf("%s: remote answered %v, direct answered %v", q.name, gotC, wantC)
			}
		}
	})
}
