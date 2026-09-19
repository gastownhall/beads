package doctor

import (
	"reflect"
	"strconv"
	"testing"
)

func TestDependencyCycleNodes(t *testing.T) {
	tests := []struct {
		name  string
		edges map[string][]string
		want  []string
	}{
		{
			name:  "empty graph",
			edges: map[string][]string{},
			want:  nil,
		},
		{
			name: "acyclic chain",
			edges: map[string][]string{
				"a": {"b"},
				"b": {"c"},
			},
			want: nil,
		},
		{
			name: "diamond DAG",
			edges: map[string][]string{
				"a": {"b", "c"},
				"b": {"d"},
				"c": {"d"},
			},
			want: nil,
		},
		{
			name: "self edge",
			edges: map[string][]string{
				"a": {"a"},
			},
			want: []string{"a"},
		},
		{
			name: "two-node cycle",
			edges: map[string][]string{
				"a": {"b"},
				"b": {"a"},
			},
			want: []string{"a", "b"},
		},
		{
			name: "three-node cycle with acyclic tail",
			edges: map[string][]string{
				"a": {"b"},
				"b": {"c"},
				"c": {"a"},
				"d": {"a"},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "two disjoint cycles",
			edges: map[string][]string{
				"a": {"b"},
				"b": {"a"},
				"x": {"y"},
				"y": {"x"},
			},
			want: []string{"a", "b", "x", "y"},
		},
		{
			name: "cycle reachable only through DAG prefix",
			edges: map[string][]string{
				"root": {"mid"},
				"mid":  {"c1"},
				"c1":   {"c2"},
				"c2":   {"c1"},
			},
			want: []string{"c1", "c2"},
		},
		{
			name: "edges to nodes with no outgoing edges",
			edges: map[string][]string{
				"a": {"leaf1", "leaf2"},
				"b": {"leaf1"},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dependencyCycleNodes(tt.edges)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("dependencyCycleNodes() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDependencyCycleNodes_DeepChain guards the iterative traversal against
// stack-depth limits a recursive implementation would hit.
func TestDependencyCycleNodes_DeepChain(t *testing.T) {
	const n = 200000
	edges := make(map[string][]string, n)
	for i := 0; i < n; i++ {
		edges["n"+strconv.Itoa(i)] = []string{"n" + strconv.Itoa((i+1)%n)}
	}

	got := dependencyCycleNodes(edges)
	if len(got) != n {
		t.Errorf("deep cycle: got %d nodes, want %d", len(got), n)
	}
}
