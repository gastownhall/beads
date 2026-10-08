package batchfixtures

import (
	"fmt"
	"strings"
	"time"
)

// Statement is one SQL statement with its bound arguments.
type Statement struct {
	SQL  string
	Args []any
}

// LargeGraphID names the i-th issue LargeGraphInserts creates.
func LargeGraphID(prefix string, i int) string {
	return fmt.Sprintf("%s-g%06d", prefix, i)
}

// LargeGraphInserts builds raw multi-row INSERTs that pre-populate a migrated
// database with issues open issues and up to two outgoing edges each, for
// measuring writes against a database that already holds a large graph
// rather than a fresh one. Issue i (i >= 1) blocks on issue (i-1)/2, so every
// node sits in a shallow blocks tree, and issue i >= 2 also points at an
// earlier issue a pseudo-random distance back (parent-child for every fifth
// issue, blocks otherwise), so reachable sets are wide and the parent-child
// forest is deep. Every edge points backwards, so the graph is acyclic.
//
// The rows go straight into issues and dependencies — no events, no labels,
// no blocked-state maintenance — because the fixture is the backdrop, not
// what is measured. It does no I/O.
func LargeGraphInserts(prefix string, issues int) []Statement {
	const rowsPerStatement = 500
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out []Statement
	for start := 0; start < issues; start += rowsPerStatement {
		end := min(start+rowsPerStatement, issues)
		var values []string
		var args []any
		for i := start; i < end; i++ {
			values = append(values, "(?, ?, '', '', '', '', 'open', 2, 'task', ?, ?)")
			args = append(args, LargeGraphID(prefix, i), fmt.Sprintf("graph issue %d", i), at, at)
		}
		out = append(out, Statement{
			SQL: "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) VALUES " +
				strings.Join(values, ", "),
			Args: args,
		})
	}
	type edge struct {
		source, target int
		depType        string
	}
	var edges []edge
	for i := 1; i < issues; i++ {
		edges = append(edges, edge{i, (i - 1) / 2, "blocks"})
		if i >= 2 {
			back := 1 + (i*7919)%min(i, 997)
			target := i - back
			if target != (i-1)/2 {
				depType := "blocks"
				if i%5 == 0 {
					depType = "parent-child"
				}
				edges = append(edges, edge{i, target, depType})
			}
		}
	}
	for start := 0; start < len(edges); start += rowsPerStatement {
		end := min(start+rowsPerStatement, len(edges))
		var values []string
		var args []any
		for _, e := range edges[start:end] {
			source, target := LargeGraphID(prefix, e.source), LargeGraphID(prefix, e.target)
			values = append(values, "(?, ?, ?, ?, 'fixture', ?, '{}')")
			args = append(args, "lg-"+source+"-"+target, source, target, e.depType, at)
		}
		out = append(out, Statement{
			SQL:  "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by, created_at, metadata) VALUES " + strings.Join(values, ", "),
			Args: args,
		})
	}
	return out
}
