//go:build cgo

package main

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The `bd search` route agreement. Every route (the direct store, the proxied
// server and a remote backend) builds ONE request (searchListRequest over
// issueops.SearchListRequest) and asks ONE library entry with it
// (issueops.Reader.List), so the same seed must answer the same flags the same
// way on all of them. A route that mapped a flag on its own, or reached a
// different read, would answer differently.
//
// The routes run in two lanes: TestEmbeddedSearchRoutesAgree (the direct
// route, embedded lane) and TestProxiedServerSearchRoutesAgree (the proxied
// and remote routes, proxied lane). Neither lane can run the other's
// workspace, so both hold their answers to the same expectation,
// searchRouteQueries' want, through checkSearchRoute. Agreeing with it on
// every route is agreeing with each other.

// searchRouteSeed is one row of the seed every route gets. Distinct priorities
// among the "routed" rows make a --limit cut the same rows on every route
// regardless of creation-time ties.
//
// The "kinded" rows are one of each kind of row a listing hides by default and
// search's scope takes back (issueops.SearchListRequest): a template, a gate,
// an infra-typed wisp and a durable row of the same infra type. They match a
// word of their own rather than "routed", so the ordered queries below still
// see one row per priority.
type searchRouteSeed struct {
	title string
	args  []string
	then  searchRouteStep
}

// searchRouteStep is what a route does to a seed row once it has created it.
type searchRouteStep int

const (
	srAsCreated searchRouteStep = iota
	srClose
	srMarkTemplate // no create flag makes a template, so this is raw SQL
	srPromote      // out of the ephemeral plane creating an infra type puts it in
)

var searchRouteSeeds = []searchRouteSeed{
	{"Routed alpha task", []string{"--type", "task", "--priority", "0", "--assignee", "alice", "--label", "lane:a"}, srAsCreated},
	{"Routed beta bug", []string{"--type", "bug", "--priority", "1", "--assignee", "bob", "--label", "lane:b"}, srAsCreated},
	{"re-ROUTED gamma", []string{"--type", "feature", "--priority", "2", "--label", "lane:a"}, srAsCreated},
	{"Routed and done", []string{"--type", "task", "--priority", "3"}, srClose},
	{"unrelated delta", []string{"--type", "task", "--priority", "4", "--label", "lane:a"}, srAsCreated},
	{"Kinded template", []string{"--type", "task"}, srMarkTemplate},
	{"Kinded gate", []string{"--type", "gate"}, srAsCreated},
	{"Kinded wisp message", []string{"--type", "message"}, srAsCreated},
	{"Kinded promoted message", []string{"--type", "message"}, srPromote},
}

const (
	srAlpha = "Routed alpha task|open"
	srBeta  = "Routed beta bug|open"
	srGamma = "re-ROUTED gamma|open"
	srDone  = "Routed and done|closed"

	skTemplate = "Kinded template|open"
	skGate     = "Kinded gate|open"
	skWisp     = "Kinded wisp message|open"
	skPromoted = "Kinded promoted message|open"
)

// searchRouteQuery is one flag set. want is the seed's answer as title|status:
// in output order when ordered, otherwise as a set.
type searchRouteQuery struct {
	name    string
	args    []string
	ordered bool // the flags fix the output order
	wireCan bool // listIssues can carry every flag
	want    []string
}

var searchRouteQueries = []searchRouteQuery{
	{"text", []string{"routed"}, false, true, []string{srAlpha, srBeta, srGamma, srDone}},
	{"status open", []string{"routed", "--status", "open"}, false, true, []string{srAlpha, srBeta, srGamma}},
	{"status all", []string{"routed", "--status", "all"}, false, true, []string{srAlpha, srBeta, srGamma, srDone}},
	{"type", []string{"routed", "--type", "bug"}, false, true, []string{srBeta}},
	{"label", []string{"routed", "--label", "lane:a"}, false, true, []string{srAlpha, srGamma}},
	{"assignee", []string{"routed", "--assignee", "alice"}, false, true, []string{srAlpha}},
	// The store's default order is priority first, so the cut keeps P0 and P1.
	{"limit", []string{"routed", "--limit", "2"}, false, true, []string{srAlpha, srBeta}},
	// The title sort is case-insensitive (workapi.SortIssues).
	{"sort title", []string{"routed", "--sort", "title"}, true, true, []string{srGamma, srAlpha, srDone, srBeta}},
	{"sort priority reverse", []string{"routed", "--sort", "priority", "--reverse"}, true, true, []string{srDone, srGamma, srBeta, srAlpha}},
	{"query flag", []string{"--query", "gamma"}, false, true, []string{srGamma}},
	{"no match", []string{"nothing-matches-this"}, false, true, nil},
	{"priority-max", []string{"routed", "--priority-max", "1"}, false, true, []string{srAlpha, srBeta}},
	// --type is expanded as `bd list` expands it before the Reader validates it.
	{"type alias", []string{"routed", "--type", "feat"}, false, true, []string{srGamma}},
	// Every kind of row comes back under no flag and under its own --type. An
	// infra type is the one that also narrows a listing to its ephemeral rows;
	// search's scope keeps the durable row of that type beside the wisp.
	{"every kind of row", []string{"kinded"}, false, true, []string{skTemplate, skGate, skWisp, skPromoted}},
	{"type message keeps both planes", []string{"kinded", "--type", "message"}, false, true, []string{skWisp, skPromoted}},
	{"type gate", []string{"kinded", "--type", "gate"}, false, true, []string{skGate}},
	{"type task keeps the template", []string{"kinded", "--type", "task"}, false, true, []string{skTemplate}},
}

// searchRouteRefusals are flag sets every route must refuse, naming the value
// it refused, rather than answer with an empty result.
var searchRouteRefusals = []struct {
	name  string
	args  []string
	value string
}{
	{"unknown type", []string{"routed", "--type", "no-such-type"}, "no-such-type"},
}

type searchRouteRow struct{ ID, Title, Status string }

// searchRouteOps is how one route writes the seed.
type searchRouteOps struct {
	create       func(title string, args []string) string
	close        func(id string)
	markTemplate func(id string)
	promote      func(id string)
}

// seedSearchRoutes creates searchRouteSeeds through one route's writes.
func seedSearchRoutes(ops searchRouteOps) {
	for _, s := range searchRouteSeeds {
		id := ops.create(s.title, s.args)
		switch s.then {
		case srClose:
			ops.close(id)
		case srMarkTemplate:
			ops.markTemplate(id)
		case srPromote:
			ops.promote(id)
		}
	}
}

// runSearchRoute runs `bd search --json args...` in dir and returns its
// stdout, stderr and exit code.
func runSearchRoute(t *testing.T, bd, dir string, env []string, args ...string) (string, string, int) {
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

func decodeSearchRoute(t *testing.T, out string) []searchRouteRow {
	t.Helper()
	s := strings.TrimSpace(out)
	start := strings.Index(s, "[")
	if start < 0 {
		return nil
	}
	var rows []searchRouteRow
	if err := json.Unmarshal([]byte(s[start:]), &rows); err != nil {
		t.Fatalf("parse search JSON: %v\n%s", err, s)
	}
	return rows
}

// searchRouteComparable projects rows onto what two databases with the same
// seed share (title and status, not ids); an unordered answer is a sorted set.
func searchRouteComparable(rows []searchRouteRow, ordered bool) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Title+"|"+r.Status)
	}
	if !ordered {
		slices.Sort(out)
	}
	return out
}

// checkSearchRoute runs every query on one route and holds each answer to its
// want. A query listIssues cannot carry (wireCan false) must refuse on a
// remote route rather than answer without the flag, and every refusal must
// refuse on every route. No query sets wireCan false today: issues.list.filters
// put the last flag search had to refuse, --priority-max, on the wire. The
// branch stays for the next flag listIssues cannot carry. It returns the
// decoded answers by query name, for a route that is compared row for row with
// another one over the same database.
func checkSearchRoute(t *testing.T, route, bd, dir string, env []string, remote bool) map[string][]searchRouteRow {
	t.Helper()
	answers := map[string][]searchRouteRow{}
	for _, q := range searchRouteQueries {
		stdout, stderr, code := runSearchRoute(t, bd, dir, env, q.args...)
		if remote && !q.wireCan {
			if code == 0 {
				t.Errorf("%s: %s bd search %v succeeded; listIssues cannot carry it, so it must refuse", q.name, route, q.args)
			}
			continue
		}
		if code != 0 {
			t.Errorf("%s: %s bd search %v failed (exit %d): %s", q.name, route, q.args, code, stderr)
			continue
		}
		rows := decodeSearchRoute(t, stdout)
		answers[q.name] = rows
		want := slices.Clone(q.want)
		if !q.ordered {
			slices.Sort(want)
		}
		if got := searchRouteComparable(rows, q.ordered); !slices.Equal(got, want) {
			t.Errorf("%s: %s bd search %v answered %v, want %v", q.name, route, q.args, got, want)
		}
	}
	for _, r := range searchRouteRefusals {
		stdout, stderr, code := runSearchRoute(t, bd, dir, env, r.args...)
		if code == 0 {
			t.Errorf("%s: %s bd search %v succeeded with %s; it must refuse", r.name, route, r.args, strings.TrimSpace(stdout))
		} else if !strings.Contains(stdout+stderr, r.value) {
			t.Errorf("%s: %s bd search %v refused (exit %d) without naming %q:\n%s%s", r.name, route, r.args, code, r.value, stdout, stderr)
		}
	}
	return answers
}
