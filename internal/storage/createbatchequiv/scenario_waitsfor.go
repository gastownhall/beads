package createbatchequiv

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// The waitsfor scenario drives the blocked-state waits-for gate
// (waitsForGateBlockedSQL): an issue spawner and a wisp spawner, each with
// issue and wisp parent-child children, and waiters on each spawner in every
// gate shape (legacy '{}' all-children, explicit all-children, any-children,
// also_blocks). It records every waiter's is_blocked after each step — edge
// adds, child closes and reopens through the store paths, and recomputes
// (scoped and the unscoped full repair) after raw status flips.

func seedWaitsFor() []*types.Issue {
	wisp := func(is *types.Issue) *types.Issue {
		is.Ephemeral = true
		return is
	}
	withDeps := func(is *types.Issue, deps ...*types.Dependency) *types.Issue {
		is.Dependencies = deps
		return is
	}
	closed := func(is *types.Issue) *types.Issue {
		is.Status = types.StatusClosed
		return is
	}
	out := []*types.Issue{
		issue("fs", "issue spawner"),
		withDeps(issue("fs.c1", "open child"), dep(id("fs.c1"), id("fs"), types.DepParentChild)),
		closed(withDeps(issue("fs.c2", "closed child"), dep(id("fs.c2"), id("fs"), types.DepParentChild))),
		wisp(issue("fws", "wisp spawner")),
		wisp(withDeps(issue("fws.c1", "open wisp child"), dep(id("fws.c1"), id("fws"), types.DepParentChild))),
		closed(wisp(withDeps(issue("fws.c2", "closed wisp child"), dep(id("fws.c2"), id("fws"), types.DepParentChild)))),
		wisp(issue("fwc", "wisp child of the issue spawner")),
		issue("fic", "issue child of the wisp spawner"),
	}
	for _, g := range waitsForGates() {
		out = append(out, issue("fw-i-"+g.name, "waiter on the issue spawner"), issue("fw-w-"+g.name, "waiter on the wisp spawner"))
		out = append(out, wisp(issue("fx-i-"+g.name, "wisp waiter on the issue spawner")))
	}
	return out
}

type waitsForGate struct{ name, metadata string }

func waitsForGates() []waitsForGate {
	return []waitsForGate{
		{"legacy", "{}"},
		{"all", `{"gate":"all-children"}`},
		{"any", `{"gate":"any-children"}`},
		{"also", `{"also_blocks":"true"}`},
		{"anyalso", `{"gate":"any-children","also_blocks":"true"}`},
	}
}

func runWaitsFor(ctx context.Context, tx *sql.Tx) ([]string, error) {
	var out []string
	snapshot := func(step string) error {
		var rows []string
		for _, table := range []string{"issues", "wisps"} {
			//nolint:gosec // G201: table is one of two constants.
			r, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT id, status, is_blocked FROM %s WHERE id LIKE 'eq-f%%'", table))
			if err != nil {
				return err
			}
			for r.Next() {
				var rid, status string
				var blocked int
				if err := r.Scan(&rid, &status, &blocked); err != nil {
					_ = r.Close()
					return err
				}
				rows = append(rows, fmt.Sprintf("%s=%s/%d", rid, status, blocked))
			}
			_ = r.Close()
			if err := r.Err(); err != nil {
				return err
			}
		}
		sort.Strings(rows)
		out = append(out, step+": "+strings.Join(rows, " "))
		return nil
	}
	add := func(d *types.Dependency) {
		_, err := issueops.AddDependencyInTx(ctx, tx, d, "gate-writer", issueops.AddDependencyOpts{})
		out = append(out, fmt.Sprintf("add %s -%s-> %s %s: %v", d.IssueID, d.Type, d.DependsOnID, d.Metadata, err))
	}
	add(dep(id("fwc"), id("fs"), types.DepParentChild))
	add(dep(id("fic"), id("fws"), types.DepParentChild))
	var waiters, wispWaiters []string
	for _, g := range waitsForGates() {
		for _, w := range []struct{ waiter, spawner string }{{"fw-i-", "fs"}, {"fw-w-", "fws"}, {"fx-i-", "fs"}} {
			d := dep(id(w.waiter+g.name), id(w.spawner), types.DepWaitsFor)
			d.Metadata = g.metadata
			add(d)
			if w.waiter == "fx-i-" {
				wispWaiters = append(wispWaiters, d.IssueID)
			} else {
				waiters = append(waiters, d.IssueID)
			}
		}
	}
	if err := snapshot("after adds"); err != nil {
		return nil, err
	}
	steps := []struct {
		name string
		do   func() error
	}{
		{"close fs.c1", func() error {
			_, err := issueops.CloseIssueInTx(ctx, tx, id("fs.c1"), "done", "gate-writer", "")
			return err
		}},
		{"close fwc", func() error {
			_, err := issueops.CloseIssueInTx(ctx, tx, id("fwc"), "done", "gate-writer", "")
			return err
		}},
		{"close fws.c1", func() error {
			_, err := issueops.CloseIssueInTx(ctx, tx, id("fws.c1"), "done", "gate-writer", "")
			return err
		}},
		{"reopen fs.c1", func() error {
			_, err := issueops.ReopenIssueInTx(ctx, tx, id("fs.c1"), "again", "gate-writer")
			return err
		}},
		{"close fic", func() error {
			_, err := issueops.CloseIssueInTx(ctx, tx, id("fic"), "done", "gate-writer", "")
			return err
		}},
		{"close fs (spawner)", func() error {
			_, err := issueops.CloseIssueInTx(ctx, tx, id("fs"), "done", "gate-writer", "")
			return err
		}},
		{"raw: reopen every child, then scoped recompute", func() error {
			if _, err := tx.ExecContext(ctx, "UPDATE issues SET status = 'open' WHERE id IN (?, ?, ?)", id("fs.c1"), id("fs.c2"), id("fic")); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE wisps SET status = 'open' WHERE id IN (?, ?, ?)", id("fws.c1"), id("fws.c2"), id("fwc")); err != nil {
				return err
			}
			return issueops.RecomputeIsBlockedInTx(ctx, tx, waiters, wispWaiters)
		}},
		{"raw: close the wisp children and fs.c2, plant stale flags, full repair", func() error {
			if _, err := tx.ExecContext(ctx, "UPDATE wisps SET status = 'closed' WHERE id IN (?, ?, ?)", id("fws.c1"), id("fws.c2"), id("fwc")); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE issues SET status = 'closed' WHERE id = ?", id("fs.c2")); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 - is_blocked WHERE id LIKE 'eq-fw-%'"); err != nil {
				return err
			}
			_, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx)
			return err
		}},
	}
	for _, st := range steps {
		if err := st.do(); err != nil {
			out = append(out, st.name+": "+err.Error())
			continue
		}
		if err := snapshot(st.name); err != nil {
			return nil, err
		}
	}
	return out, nil
}
