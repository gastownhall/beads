//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestProxiedServerCloseSiblingBlockersFanIn is gastownhall/beads#6716 end to
// end on the proxied CLI: the fan-in of a formula whose parallel workers
// close the two blockers of one step at the same time. Each round closes the
// blockers with two concurrent `bd close` processes and then requires the
// dependent in `bd ready`. Without the post-commit recheck on the uow path a
// round whose closes overlap leaves the dependent is_blocked=1 and missing
// from ready work. The interleaving is not pinned here (see
// TestProxiedBlockedRecheckAfterRacingUnblocks in internal/storage/uow for
// the deterministic race); this proves the settled CLI outcome.
func TestProxiedServerCloseSiblingBlockersFanIn(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "fan")

	const rounds = 6
	for round := 0; round < rounds; round++ {
		a := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("round %d blocker a", round)).ID
		b := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("round %d blocker b", round)).ID
		c := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("round %d fan-in", round)).ID
		for _, blocker := range []string{a, b} {
			if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", c, blocker); err != nil {
				t.Fatalf("round %d: dep add %s %s: %v\n%s", round, c, blocker, err, out)
			}
		}

		var wg sync.WaitGroup
		errs := make([]string, 2)
		for i, id := range []string{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cmd := exec.Command(bd, "close", id)
				cmd.Dir = p.dir
				cmd.Env = bdProxiedEnv(p.dir)
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				if err := cmd.Run(); err != nil {
					errs[i] = fmt.Sprintf("close %s: %v\n%s", id, err, stderr.String())
				}
			}()
		}
		wg.Wait()
		if joined := strings.TrimSpace(strings.Join(errs, "")); joined != "" {
			t.Fatalf("round %d: %s", round, joined)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "ready", "--json", "--limit", "0")
		if err != nil {
			t.Fatalf("round %d: bd ready: %v\n%s", round, err, out)
		}
		start := bytes.IndexByte(out, '[')
		if start < 0 {
			t.Fatalf("round %d: bd ready printed no JSON array: %s", round, out)
		}
		var ready []types.Issue
		if err := json.Unmarshal(out[start:], &ready); err != nil {
			t.Fatalf("round %d: parse bd ready: %v\n%s", round, err, out)
		}
		found := false
		for _, issue := range ready {
			found = found || issue.ID == c
		}
		if !found {
			t.Fatalf("round %d: %s has both blockers closed but is missing from bd ready", round, c)
		}
	}
}
