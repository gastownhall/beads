package issueops

import "testing"

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
