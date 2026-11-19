//go:build cgo

package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bd-uschhs (#5850) AC-8, proxied leg: the same three-target invariance the
// embedded test pins, over the proxied-server route (bd serve behind
// bd connect, the httpclient store under the uow decorator). The lane's
// disclosure work must not move a ghost edge into the blocked flag, the
// listing or the ready set on this route either.
func TestProxiedReadyBlockedUnresolvableOutcomesUnchanged(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "ry")

	const scope = "gh5850-ac8"
	newIssue := func(title string) *types.Issue {
		return bdProxiedCreate(t, bd, p.dir, title, "--type", "task", "--label", scope)
	}

	unblocked := newIssue("Ready baseline (proxied)")
	ghost := newIssue("Blocks-edge to a bare cross-repo id (proxied)")
	ext := newIssue("Blocks-edge to an external reference (proxied)")
	real := newIssue("Blocks-edge to a real local issue (proxied)")
	localBlocker := bdProxiedCreate(t, bd, p.dir, "The local blocker (proxied)", "--type", "task")

	bdProxiedDep(t, bd, p.dir, "add", ghost.ID, "liveop-gone", "--type", "blocks")
	bdProxiedDep(t, bd, p.dir, "add", ext.ID, "external:liveop:gone-too", "--type", "blocks")
	bdProxiedDep(t, bd, p.dir, "add", real.ID, localBlocker.ID, "--type", "blocks")

	ready := make(map[string]bool)
	for _, row := range bdProxiedReadyJSON(t, bd, p, "--label", scope) {
		ready[row.ID] = true
	}
	if !ready[unblocked.ID] || !ready[ghost.ID] {
		t.Errorf("ready membership lost ready work (unblocked=%v ghost=%v): %v",
			ready[unblocked.ID], ready[ghost.ID], ready)
	}
	if ready[ext.ID] || ready[real.ID] {
		t.Errorf("ready membership admitted blocked work (ext=%v real=%v): %v",
			ready[ext.ID], ready[real.ID], ready)
	}

	blocked := make(map[string]bool)
	for _, row := range bdProxiedBlockedJSON(t, bd, p) {
		blocked[row.ID] = true
	}
	if !blocked[ext.ID] || !blocked[real.ID] {
		t.Errorf("blocked listing membership lost blocked work (ext=%v real=%v): %v",
			blocked[ext.ID], blocked[real.ID], blocked)
	}
	if blocked[unblocked.ID] || blocked[ghost.ID] {
		t.Errorf("blocked listing admitted work that is not blocked (unblocked=%v ghost=%v): %v",
			blocked[unblocked.ID], blocked[ghost.ID], blocked)
	}

	flags := func(raw bool) map[string]bool {
		t.Helper()
		args := []string{"blocked", "--flags", "--json"}
		if raw {
			args = append(args, "--raw")
		}
		cmd := exec.Command(bd, args...)
		cmd.Dir = p.dir
		cmd.Env = bdProxiedEnv(p.dir)
		cmd.Stdin = strings.NewReader(strings.Join([]string{unblocked.ID, ghost.ID, ext.ID, real.ID}, "\n"))
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd blocked --flags failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		var rows []blockedFlagRow
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &rows); err != nil {
			t.Fatalf("parse blocked --flags JSON: %v\n%s", err, stdout.String())
		}
		got := make(map[string]bool, len(rows))
		for _, r := range rows {
			got[r.ID] = r.IsBlocked
		}
		return got
	}

	decorated := flags(false)
	if !decorated[ext.ID] || !decorated[real.ID] {
		t.Errorf("decorated blocked flag lost a blocker (ext=%v real=%v): %v",
			decorated[ext.ID], decorated[real.ID], decorated)
	}
	if decorated[unblocked.ID] || decorated[ghost.ID] {
		t.Errorf("decorated blocked flag set on unblocked work (unblocked=%v ghost=%v): %v",
			decorated[unblocked.ID], decorated[ghost.ID], decorated)
	}

	rawFlags := flags(true)
	if !rawFlags[real.ID] || rawFlags[unblocked.ID] || rawFlags[ext.ID] || rawFlags[ghost.ID] {
		t.Errorf("raw stored flag = %v, want only the real blocker set (real=true, others=false)", rawFlags)
	}
}
