package batchfixtures

import (
	"fmt"
	"strings"
	"time"
)

// WaitsForSpawnerID names the spawner WaitsForInserts creates.
func WaitsForSpawnerID(prefix string) string { return prefix + "-wfspawner" }

// WaitsForChildID names the i-th child WaitsForInserts creates.
func WaitsForChildID(prefix string, i int) string { return fmt.Sprintf("%s-wfchild%05d", prefix, i) }

// WaitsForWaiterID names the i-th waiter WaitsForInserts creates.
func WaitsForWaiterID(prefix string, i int) string { return fmt.Sprintf("%s-wfwaiter%04d", prefix, i) }

// WaitsForInserts builds raw INSERTs for a formula/molecule-shaped fan-out:
// one spawner issue with children parent-child children (every tenth one
// closed) and waiters issues each holding a waits-for edge on the spawner,
// cycling through the gate shapes the blocked-state waits-for leg evaluates —
// the default all-children gate (legacy '{}' metadata), an explicit
// all-children gate, an any-children gate, and an also_blocks edge. Like
// LargeGraphInserts it writes rows only (no events, no blocked-state
// maintenance) and does no I/O.
func WaitsForInserts(prefix string, children, waiters int) []Statement {
	const rowsPerStatement = 500
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	type row struct {
		id, status string
	}
	rows := []row{{WaitsForSpawnerID(prefix), "open"}}
	for i := 0; i < children; i++ {
		status := "open"
		if i%10 == 0 {
			status = "closed"
		}
		rows = append(rows, row{WaitsForChildID(prefix, i), status})
	}
	for i := 0; i < waiters; i++ {
		rows = append(rows, row{WaitsForWaiterID(prefix, i), "open"})
	}
	var out []Statement
	for start := 0; start < len(rows); start += rowsPerStatement {
		end := min(start+rowsPerStatement, len(rows))
		var values []string
		var args []any
		for _, r := range rows[start:end] {
			values = append(values, "(?, ?, '', '', '', '', ?, 2, 'task', ?, ?, ?)")
			var closedAt any
			if r.status == "closed" {
				closedAt = at
			}
			args = append(args, r.id, "waits-for fixture "+r.id, r.status, at, at, closedAt)
		}
		out = append(out, Statement{
			SQL: "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at, closed_at) VALUES " +
				strings.Join(values, ", "),
			Args: args,
		})
	}
	type edge struct {
		source, depType, metadata string
	}
	var edges []edge
	for i := 0; i < children; i++ {
		edges = append(edges, edge{WaitsForChildID(prefix, i), "parent-child", "{}"})
	}
	gates := []string{"{}", `{"gate":"all-children"}`, `{"gate":"any-children"}`, `{"also_blocks":"true"}`}
	for i := 0; i < waiters; i++ {
		edges = append(edges, edge{WaitsForWaiterID(prefix, i), "waits-for", gates[i%len(gates)]})
	}
	spawner := WaitsForSpawnerID(prefix)
	for start := 0; start < len(edges); start += rowsPerStatement {
		end := min(start+rowsPerStatement, len(edges))
		var values []string
		var args []any
		for _, e := range edges[start:end] {
			values = append(values, "(?, ?, ?, ?, 'fixture', ?, ?)")
			args = append(args, "wf-"+e.source, e.source, spawner, e.depType, at, e.metadata)
		}
		out = append(out, Statement{
			SQL:  "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by, created_at, metadata) VALUES " + strings.Join(values, ", "),
			Args: args,
		})
	}
	return out
}
