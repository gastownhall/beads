package issueops

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestDependencyGraphReadersAskForPrimaryKeyOrder pins the statement each
// graph reader sends for each dependency table: only the types it keeps, in
// primary-key order. The type filter lets the engine read through a
// type-leading index, which returns the rows grouped by type rather than in
// the primary-key order an unfiltered scan used, so the ORDER BY is what keeps
// the order. The order shows in refusals: each adjacency list is built in row
// order, and CycleThroughEdgesInGraph names the first of two equally short
// cycle paths it reaches. Here d -> a closes both d -> a -> b -> d and
// d -> a -> e -> d, and the rows put a -> b first.
func TestDependencyGraphReadersAskForPrimaryKeyOrder(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		read      func(context.Context, DBTX, []string, map[string][]string) error
		typeNames string
	}{
		{"blocking", AppendBlockingGraphInTx, "'blocks', 'conditional-blocks'"},
		{"scheduling", AppendSchedulingGraphInTx, "'blocks', 'conditional-blocks', 'parent-child'"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			graphRead := func(table string) string {
				return `(?s)SELECT issue_id, ` + regexp.QuoteMeta(DepTargetExpr) + ` AS depends_on_id, type\s+FROM ` +
					table + `\s+WHERE type IN \(` + regexp.QuoteMeta(tt.typeNames) + `\)\s+ORDER BY id$`
			}
			mock.ExpectQuery(graphRead("dependencies")).WillReturnRows(
				sqlmock.NewRows([]string{"issue_id", "depends_on_id", "type"}).
					AddRow("a", "b", "blocks").
					AddRow("a", "e", "conditional-blocks").
					AddRow("b", "d", "blocks").
					AddRow("e", "d", "blocks"))
			mock.ExpectQuery(graphRead("wisp_dependencies")).WillReturnRows(
				sqlmock.NewRows([]string{"issue_id", "depends_on_id", "type"}).
					AddRow("d", "a", "blocks"))

			graph := make(map[string][]string)
			if err := tt.read(t.Context(), db, []string{"dependencies", "wisp_dependencies"}, graph); err != nil {
				t.Fatalf("graph read: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
			if got := CycleThroughEdgesInGraph(graph, [][2]string{{"d", "a"}}); got != "d → a → b → d" {
				t.Errorf("cycle through d -> a = %q, want the path through the first row, d → a → b → d", got)
			}
		})
	}
}

// bd-578h9.9: the bulk-add cycle gate must block only cycles that actually
// traverse a new edge. Endpoint membership over-blocks (a pre-existing
// committed cycle at an endpoint rolls back unrelated wiring), and matching
// against DFS-enumerated cycles under-blocks (the DFS records one cycle per
// back edge, which can be a pre-existing cycle through the same nodes).
func TestCycleThroughEdgesInGraph(t *testing.T) {
	t.Parallel()

	t.Run("preexisting_cycle_at_endpoint_does_not_block", func(t *testing.T) {
		// a <-> b is a committed cycle; the new edge a -> c is unrelated.
		graph := map[string][]string{
			"a": {"b", "c"},
			"b": {"a"},
		}
		if got := CycleThroughEdgesInGraph(graph, [][2]string{{"a", "c"}}); got != "" {
			t.Errorf("unrelated edge blocked by pre-existing cycle: %q", got)
		}
	})

	t.Run("new_edge_on_cycle_blocks", func(t *testing.T) {
		graph := map[string][]string{
			"a": {"b"},
			"b": {"c"},
			"c": {"a"}, // new edge closing the cycle
		}
		if got := CycleThroughEdgesInGraph(graph, [][2]string{{"c", "a"}}); got != "c → a → b → c" {
			t.Errorf("cycle through new edge: got %q, want rendered path", got)
		}
	})

	t.Run("cycle_sharing_nodes_with_preexisting_cycle_blocks", func(t *testing.T) {
		// Pre-existing cycle u -> w -> v -> u; the new edge u -> v creates a
		// second cycle u -> v -> u on the same nodes. A DFS enumerator can
		// record only the pre-existing cycle, so edge-membership against its
		// output would miss this; reachability does not.
		graph := map[string][]string{
			"u": {"w", "v"},
			"w": {"v"},
			"v": {"u"},
		}
		if got := CycleThroughEdgesInGraph(graph, [][2]string{{"u", "v"}}); got != "u → v → u" {
			t.Errorf("new cycle on shared nodes: got %q, want u → v → u", got)
		}
	})

	t.Run("self_edge_blocks", func(t *testing.T) {
		graph := map[string][]string{"a": {"a"}}
		if got := CycleThroughEdgesInGraph(graph, [][2]string{{"a", "a"}}); got != "a → a" {
			t.Errorf("self edge: got %q", got)
		}
	})

	t.Run("acyclic_passes", func(t *testing.T) {
		graph := map[string][]string{
			"a": {"b"},
			"b": {"c"},
		}
		if got := CycleThroughEdgesInGraph(graph, [][2]string{{"a", "b"}, {"b", "c"}}); got != "" {
			t.Errorf("acyclic graph flagged: %q", got)
		}
	})
}
