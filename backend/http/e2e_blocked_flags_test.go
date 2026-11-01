//go:build cgo

package bdhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// runBDStdin is runBD with stdin.
func runBDStdin(t *testing.T, f *remoteRolesFixture, stdin string, args ...string) bdResult {
	t.Helper()
	cmd := exec.Command(f.bin, args...)
	cmd.Dir = f.dir
	cmd.Env = minimalBDEnv(f.dir)
	cmd.Stdin = strings.NewReader(stdin)
	var o, e strings.Builder
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run bd %v: %v", args, err)
	}
	return bdResult{stdout: o.String(), stderr: e.String(), code: code}
}

type blockedFlagRow struct {
	ID        string `json:"id"`
	IsBlocked bool   `json:"is_blocked"`
}

func parseBlockedFlags(t *testing.T, out string) []blockedFlagRow {
	t.Helper()
	var rows []blockedFlagRow
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("parse bd blocked --flags --json: %v\n%s", err, out)
	}
	if rows == nil {
		t.Fatalf("bd blocked --flags --json printed null, want an array: %s", out)
	}
	return rows
}

// flagStdin is every id the fixture created, in title order, plus an id that
// names no row, a blank line and a repeat: the omitted, ignored and collapsed
// cases.
func flagStdin(f *remoteRolesFixture) string {
	titles := make([]string, 0, len(f.ids))
	for title := range f.ids {
		titles = append(titles, title)
	}
	sort.Strings(titles)
	var b strings.Builder
	for _, title := range titles {
		b.WriteString(f.ids[title] + "\n")
	}
	b.WriteString("\n  " + f.ids[titles[0]] + "  \ne2e-no-such-row\n")
	return b.String()
}

// TestE2E_BlockedFlagsOverHTTP runs `bd blocked --flags --json` (the blocked
// flag per id read from stdin, the readiness column gc projects) through the
// real `bd` binary against an in-process `bd serve` and against a local
// embedded workspace holding the same graph, and requires the same JSON once
// each generated id is mapped to its title. Over HTTP the read is
// listBlockingAnnotations with is_blocked; an older server's handshake is
// refused before the dial, naming dependencies.blocking.isBlocked.
//
// It also pins that this is the FLAG and not the listing: a row whose stored
// flag is set with no nameable blocker reads true here and is absent from
// `bd blocked --json`.
func TestE2E_BlockedFlagsOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)

	seed := func(f *remoteRolesFixture) {
		f.seed()
		blocker := f.create("blocker", "-t", "task")
		gp := f.create("gate-parent", "-t", "epic")
		kid := f.create("gate-kid", "-t", "task")
		f.mustRun(nil, "dep", "add", kid, gp, "--type", "parent-child")
		f.mustRun(nil, "dep", "add", gp, blocker)
	}

	t.Run("served equals local", func(t *testing.T) {
		addr := startE2EServer(t)
		local := newEmbeddedFixture(t, bin)
		seed(local)
		served := newHTTPFixture(t, bin, addr)
		seed(served)

		lr := runBDStdin(t, local, flagStdin(local), "blocked", "--flags", "--json")
		sr := runBDStdin(t, served, flagStdin(served), "blocked", "--flags", "--json")
		if lr.code != 0 || sr.code != 0 {
			t.Fatalf("exit local=%d served=%d\nlocal: %s %s\nserved: %s %s", lr.code, sr.code, lr.stdout, lr.stderr, sr.stdout, sr.stderr)
		}
		normalize := func(f *remoteRolesFixture, rows []blockedFlagRow) []blockedFlagRow {
			titleOf := map[string]string{}
			for title, id := range f.ids {
				titleOf[id] = title
			}
			out := make([]blockedFlagRow, 0, len(rows))
			for _, row := range rows {
				if title, ok := titleOf[row.ID]; ok {
					row.ID = title
				}
				out = append(out, row)
			}
			return out
		}
		want := normalize(local, parseBlockedFlags(t, lr.stdout))
		got := normalize(served, parseBlockedFlags(t, sr.stdout))
		if !reflect.DeepEqual(want, got) {
			t.Errorf("served bd blocked --flags --json differs from the local command:\nlocal:  %v\nserved: %v", want, got)
		}
		if len(want) != len(local.ids) {
			t.Errorf("local answered %d rows for %d ids (the unknown id omitted, the repeat collapsed): %v", len(want), len(local.ids), want)
		}
		blocked := map[string]bool{}
		for _, row := range got {
			blocked[row.ID] = row.IsBlocked
		}
		for title, wantBlocked := range map[string]bool{
			"loner": true, "child-x": true, "gate-parent": true, "gate-kid": true,
			"blocker": false, "child-b": false, "parent": false,
		} {
			if blocked[title] != wantBlocked {
				t.Errorf("%s is_blocked = %v, want %v (rows %v)", title, blocked[title], wantBlocked, got)
			}
		}
		// Byte-for-byte the same members in the same order.
		if strings.Count(lr.stdout, "\n") != strings.Count(sr.stdout, "\n") {
			t.Errorf("line counts differ:\nlocal:\n%s\nserved:\n%s", lr.stdout, sr.stdout)
		}
		// Nothing piped is an empty array.
		if r := runBDStdin(t, served, "", "blocked", "--flags", "--json"); r.code != 0 || strings.TrimSpace(r.stdout) != "[]" {
			t.Errorf("empty stdin: exit %d stdout %q, want [] and exit 0", r.code, r.stdout)
		}
		// The listing's filters are refused, not silently ignored.
		if r := runBDStdin(t, served, "x\n", "blocked", "--flags", "--json", "--label", "a"); r.code != 1 {
			t.Errorf("--flags --label exited %d, want 1: %s", r.code, r.stdout)
		}
	})

	t.Run("the flag, not the listing", func(t *testing.T) {
		addr, reference := startE2EServerWithStore(t, e2eProjectID, e2eDatabase)
		served := newHTTPFixture(t, bin, addr)
		stale := served.create("stale")
		if err := reference.RunEventsMaintenanceTx(context.Background(), func(ctx context.Context, tx issueops.DBTX) error {
			_, err := tx.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 WHERE id = ?", stale)
			return err
		}); err != nil {
			t.Fatalf("set the stale flag: %v", err)
		}
		rows := parseBlockedFlags(t, runBDStdin(t, served, stale+"\n", "blocked", "--flags", "--json").stdout)
		if len(rows) != 1 || rows[0].ID != stale || !rows[0].IsBlocked {
			t.Errorf("--flags for a stale-flagged row = %v, want [{%s true}]", rows, stale)
		}
		listing := served.mustRun(nil, "blocked", "--json").stdout
		if strings.Contains(listing, stale) {
			t.Errorf("bd blocked --json lists %s, which has no nameable blocker: %s", stale, listing)
		}
	})

	t.Run("raw is the stored column", func(t *testing.T) {
		addr, reference := startE2EServerWithStore(t, e2eProjectID, e2eDatabase)
		local := newEmbeddedFixture(t, bin)
		seed(local)
		served := newHTTPFixture(t, bin, addr)
		seed(served)

		flagsOf := func(f *remoteRolesFixture, stdin string, args ...string) map[string]bool {
			t.Helper()
			r := runBDStdin(t, f, stdin, append([]string{"blocked", "--flags", "--json"}, args...)...)
			if r.code != 0 {
				t.Fatalf("bd blocked --flags --json %v exited %d: %s %s", args, r.code, r.stdout, r.stderr)
			}
			titleOf := map[string]string{}
			for title, id := range f.ids {
				titleOf[id] = title
			}
			out := map[string]bool{}
			for _, row := range parseBlockedFlags(t, r.stdout) {
				key := row.ID
				if title, ok := titleOf[row.ID]; ok {
					key = title
				}
				out[key] = row.IsBlocked
			}
			return out
		}

		localRaw := flagsOf(local, flagStdin(local), "--raw")
		servedRaw := flagsOf(served, flagStdin(served), "--raw")
		if !reflect.DeepEqual(localRaw, servedRaw) {
			t.Errorf("served --raw differs from local --raw:\nlocal:  %v\nserved: %v", localRaw, servedRaw)
		}
		lr := runBDStdin(t, local, flagStdin(local), "blocked", "--flags", "--json", "--raw")
		sr := runBDStdin(t, served, flagStdin(served), "blocked", "--flags", "--json", "--raw")
		if strings.Count(lr.stdout, "\n") != strings.Count(sr.stdout, "\n") {
			t.Errorf("--raw line counts differ:\nlocal:\n%s\nserved:\n%s", lr.stdout, sr.stdout)
		}

		// child-x is blocked ONLY by an unsatisfied external ref: the policy
		// flag says blocked, the stored column does not.
		decorated := flagsOf(served, flagStdin(served))
		if !decorated["child-x"] || servedRaw["child-x"] || localRaw["child-x"] {
			t.Errorf("child-x (externally blocked only): --flags %v, --raw served %v local %v; want true, false, false",
				decorated["child-x"], servedRaw["child-x"], localRaw["child-x"])
		}
		// Locally blocked rows read the same either way.
		for _, title := range []string{"loner", "gate-parent", "gate-kid", "blocker"} {
			if decorated[title] != servedRaw[title] {
				t.Errorf("%s: --flags %v, --raw %v; a local blocker is the column", title, decorated[title], servedRaw[title])
			}
		}

		// A stale flag (set, no blocker) is the column, so both read true.
		stale := served.create("stale")
		if err := reference.RunEventsMaintenanceTx(context.Background(), func(ctx context.Context, tx issueops.DBTX) error {
			_, err := tx.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 WHERE id = ?", stale)
			return err
		}); err != nil {
			t.Fatalf("set the stale flag: %v", err)
		}
		if got := flagsOf(served, stale+"\n", "--raw"); !got["stale"] {
			t.Errorf("--raw for the stale-flagged row = %v, want true", got)
		}
		if got := flagsOf(served, stale+"\n"); !got["stale"] {
			t.Errorf("--flags for the stale-flagged row = %v, want true", got)
		}

		// --raw qualifies --flags only.
		if r := runBDStdin(t, served, "", "blocked", "--json", "--raw"); r.code != 1 || !strings.Contains(r.stdout+r.stderr, "--raw") {
			t.Errorf("bd blocked --raw without --flags: exit %d %s %s, want exit 1 naming --raw", r.code, r.stdout, r.stderr)
		}

		// A server that applies the policy answers the decorated flag and the
		// wire cannot ask it for the column: --raw is refused there, before
		// the annotation read, rather than answered with the policy flag.
		proxyAddr, counter := startMaskingProxy(t, addr, func(body map[string]any) {
			caps, _ := body["capabilities"].([]any)
			body["capabilities"] = append(caps, "policy.external_dependencies")
		})
		enforced := newHTTPFixture(t, bin, proxyAddr)
		counter.reset()
		r := runBDStdin(t, enforced, served.ids["child-x"]+"\n", "blocked", "--flags", "--json", "--raw")
		if r.code != 1 || !strings.Contains(r.stdout+r.stderr, "policy.external_dependencies") {
			t.Errorf("--raw against a policy server: exit %d %s %s, want exit 1 naming the policy token", r.code, r.stdout, r.stderr)
		}
		for _, p := range counter.snapshot() {
			if strings.Contains(p, "/v0/beads/dependencies/blocking") {
				t.Errorf("a refused --raw dialed %s", p)
			}
		}
	})

	t.Run("older server mask", func(t *testing.T) {
		addr := startE2EServer(t)
		proxyAddr, counter := startMaskingProxy(t, addr, mcContextMask)
		f := newHTTPFixture(t, bin, proxyAddr)
		counter.reset()
		r := runBDStdin(t, f, "e2e-anything\n", "blocked", "--flags", "--json")
		if r.code != 1 {
			t.Fatalf("bd blocked --flags against a server without the token exited %d, want 1: %s", r.code, r.stdout)
		}
		if !strings.Contains(r.stdout+r.stderr, "dependencies.blocking.isBlocked") {
			t.Errorf("refusal does not name the missing capability: stdout=%s stderr=%s", r.stdout, r.stderr)
		}
		for _, p := range counter.snapshot() {
			if strings.Contains(p, "/v0/beads/dependencies/blocking") {
				t.Errorf("a refused bd blocked --flags dialed %s", p)
			}
		}
	})
}
