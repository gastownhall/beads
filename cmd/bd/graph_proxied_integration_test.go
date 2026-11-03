//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestProxiedServerGraph(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "grp")

	epic := bdProxiedCreate(t, bd, p.dir, "Graph epic", "--type", "epic")
	c1 := bdProxiedCreate(t, bd, p.dir, "Child one", "--type", "task", "--parent", epic.ID)
	c2 := bdProxiedCreate(t, bd, p.dir, "Child two", "--type", "task", "--parent", epic.ID)
	// c2 depends on c1 (c1 blocks c2).
	if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", c2.ID, c1.ID); err != nil {
		t.Fatalf("dep add: %v\n%s", err, out)
	}
	standalone := bdProxiedCreate(t, bd, p.dir, "Standalone issue", "--type", "task")

	singleGraphIDs := func(t *testing.T, id string) map[string]bool {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, "graph", id, "--json")
		if err != nil {
			t.Fatalf("graph %s --json: %v\n%s", id, err, out)
		}
		var got struct {
			Root   *struct{ ID string } `json:"root"`
			Issues []struct {
				ID string `json:"id"`
			} `json:"issues"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		set := make(map[string]bool, len(got.Issues))
		for _, iss := range got.Issues {
			set[iss.ID] = true
		}
		return set
	}

	t.Run("single_issue_human", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", epic.ID)
		if err != nil {
			t.Fatalf("graph %s: %v\n%s", epic.ID, err, out)
		}
		if !strings.Contains(string(out), epic.ID) {
			t.Errorf("expected epic ID in graph: %s", out)
		}
	})

	t.Run("epic_subgraph_includes_children", func(t *testing.T) {
		ids := singleGraphIDs(t, epic.ID)
		for _, want := range []string{epic.ID, c1.ID, c2.ID} {
			if !ids[want] {
				t.Errorf("epic subgraph missing %s, got %v", want, ids)
			}
		}
	})

	t.Run("dependency_bfs_reaches_dependent", func(t *testing.T) {
		// Starting from c1, the BFS should reach c2 (which depends on c1).
		ids := singleGraphIDs(t, c1.ID)
		if !ids[c2.ID] {
			t.Errorf("graph from %s should reach dependent %s, got %v", c1.ID, c2.ID, ids)
		}
	})

	t.Run("no_dependencies", func(t *testing.T) {
		ids := singleGraphIDs(t, standalone.ID)
		if !ids[standalone.ID] {
			t.Errorf("expected standalone %s in its own graph, got %v", standalone.ID, ids)
		}
	})

	t.Run("compact_format", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "--compact", epic.ID)
		if err != nil {
			t.Fatalf("graph --compact: %v\n%s", err, out)
		}
		if len(out) == 0 {
			t.Error("expected non-empty compact output")
		}
	})

	t.Run("box_format", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "--box", epic.ID)
		if err != nil {
			t.Fatalf("graph --box: %v\n%s", err, out)
		}
		if len(out) == 0 {
			t.Error("expected non-empty box output")
		}
	})

	t.Run("dot_format", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "--dot", epic.ID)
		if err != nil {
			t.Fatalf("graph --dot: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "digraph") {
			t.Errorf("expected 'digraph' in DOT output: %s", out)
		}
	})

	t.Run("html_format", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "--html", epic.ID)
		if err != nil {
			t.Fatalf("graph --html: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, "<html") && !strings.Contains(s, "<!DOCTYPE") {
			t.Errorf("expected HTML output, got: %.200s", s)
		}
	})

	t.Run("html_output_error", func(t *testing.T) {
		stderr, err := runWithReadOnlyStdout(t, bd, p.dir, bdProxiedEnv(p.dir), "graph", "--html", epic.ID)
		if err == nil {
			t.Fatalf("proxied graph --html succeeded with read-only stdout; stderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "writing HTML output") {
			t.Fatalf("proxied graph --html stderr = %q, want writer diagnostic", stderr)
		}
	})

	t.Run("json_output_contains_root", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", epic.ID, "--json")
		if err != nil {
			t.Fatalf("graph --json: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), epic.ID) {
			t.Errorf("expected epic ID in JSON output: %s", out)
		}
	})

	t.Run("all_has_component", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "--all", "--json")
		if err != nil {
			t.Fatalf("graph --all --json: %v\n%s", err, out)
		}
		var subgraphs []struct {
			Issues []json.RawMessage `json:"Issues"`
		}
		if err := json.Unmarshal(out, &subgraphs); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if len(subgraphs) == 0 {
			t.Fatalf("expected at least one component, got 0")
		}
		var total int
		for _, sg := range subgraphs {
			total += len(sg.Issues)
		}
		if total == 0 {
			t.Errorf("components have no issues")
		}
	})

	t.Run("all_human", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "--all")
		if err != nil {
			t.Fatalf("graph --all: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), epic.ID) {
			t.Errorf("expected epic in --all graph: %s", out)
		}
	})

	t.Run("all_compact", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "--all", "--compact")
		if err != nil {
			t.Fatalf("graph --all --compact: %v\n%s", err, out)
		}
		if len(out) == 0 {
			t.Error("expected non-empty all+compact output")
		}
	})

	t.Run("all_dot", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "--all", "--dot")
		if err != nil {
			t.Fatalf("graph --all --dot: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "digraph") {
			t.Errorf("expected 'digraph' in all+dot: %s", out)
		}
	})

	t.Run("max_rows_is_honored", func(t *testing.T) {
		// The epic's component is three nodes (epic, c1, c2), so a single-
		// issue graph is the post-walk check: the whole subgraph walks first,
		// then the node count pays the cap. Cap 2 exits 2 naming its source
		// with nothing rendered; cap 3 — the component's exact size —
		// answers with the uncapped graph's bytes.
		for _, tc := range []struct {
			name string
			env  []string
			args []string
			want string
		}{
			{name: "flag", args: []string{"graph", "--max-rows", "2", epic.ID}, want: "--max-rows=2 exceeded."},
			{name: "env", env: []string{"BEADS_MAX_ROWS=2"}, args: []string{"graph", epic.ID}, want: "BEADS_MAX_ROWS=2 exceeded."},
		} {
			stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, tc.env, tc.args...)
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
				t.Errorf("%s: exit=%v, want exit 2 with the cap diagnosis on stderr (stderr=%q)", tc.name, err, stderr)
				continue
			}
			if stdout != "" {
				t.Errorf("%s: stdout = %q, want nothing rendered under a refused cap", tc.name, stdout)
			}
			if combined := stdout + stderr; !strings.Contains(combined, "too many rows") || !strings.Contains(combined, tc.want) {
				t.Errorf("%s: expected the cap diagnosis naming %q, got:\n%s", tc.name, tc.want, combined)
			}
			if combined := stdout + stderr; strings.Contains(combined, "not supported in proxied-server mode") {
				t.Errorf("%s: the stale unsupported-cap refusal still fired, got:\n%s", tc.name, combined)
			}
		}

		wantJSON, err := bdProxiedRun(t, bd, p.dir, "graph", epic.ID, "--json")
		if err != nil {
			t.Fatalf("uncapped graph --json: %v\n%s", err, wantJSON)
		}
		for _, tc := range []struct {
			name string
			env  []string
			args []string
		}{
			{name: "cap_fits_flag", args: []string{"graph", "--max-rows", "3", epic.ID, "--json"}},
			{name: "cap_fits_env", env: []string{"BEADS_MAX_ROWS=3"}, args: []string{"graph", epic.ID, "--json"}},
		} {
			stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, tc.env, tc.args...)
			if err != nil {
				t.Errorf("%s: expected success under a cap the component fits inside: %v\nstderr:\n%s", tc.name, err, stderr)
				continue
			}
			if string(stdout) != string(wantJSON) {
				t.Errorf("%s: capped graph differs from the uncapped one:\ncapped:\n%s\nuncapped:\n%s", tc.name, stdout, wantJSON)
			}
		}

		// --all binds the cap per status, the direct route's documented
		// behavior: all four issues here are open, so cap 4 shows every
		// node while cap 2 refuses the one status search that matched four.
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "--all", "--max-rows", "4", "--json")
		if err != nil {
			t.Fatalf("graph --all under a per-status cap the set fits inside: %v\n%s", err, out)
		}
		for _, id := range []string{epic.ID, c1.ID, c2.ID, standalone.ID} {
			if !strings.Contains(string(out), id) {
				t.Errorf("graph --all under cap 4 is missing %s:\n%s", id, out)
			}
		}
		for _, tc := range []struct {
			name string
			env  []string
			args []string
			want string
		}{
			{name: "flag", args: []string{"graph", "--all", "--max-rows", "2", "--json"}, want: "--max-rows=2 exceeded."},
			{name: "env", env: []string{"BEADS_MAX_ROWS=2"}, args: []string{"graph", "--all", "--json"}, want: "BEADS_MAX_ROWS=2 exceeded."},
		} {
			stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, tc.env, tc.args...)
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
				t.Errorf("%s: exit=%v, want exit 2 with the cap diagnosis on stderr (stderr=%q)", tc.name, err, stderr)
				continue
			}
			if stdout != "" {
				t.Errorf("%s: stdout = %q, want nothing rendered under a refused cap", tc.name, stdout)
			}
			if combined := stdout + stderr; !strings.Contains(combined, "too many rows") || !strings.Contains(combined, tc.want) {
				t.Errorf("%s: expected the cap diagnosis naming %q, got:\n%s", tc.name, tc.want, combined)
			}
		}
	})

	t.Run("nonexistent_id_errors", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "grp-999999", "--json")
		if err == nil {
			t.Fatalf("expected error for nonexistent id, got success: %s", out)
		}
		if !strings.Contains(string(out), "not found") {
			t.Errorf("expected 'not found' in error, got: %s", out)
		}
	})

	t.Run("all_with_id_conflict", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "--all", epic.ID)
		if err == nil {
			t.Fatalf("expected conflict error for --all with id, got success: %s", out)
		}
		if !strings.Contains(string(out), "cannot specify issue ID with --all") {
			t.Errorf("expected --all conflict error, got: %s", out)
		}
	})

	t.Run("check_clean", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "graph", "check", "--json")
		if err != nil {
			t.Fatalf("graph check --json: %v\n%s", err, out)
		}
		var res struct {
			Clean  bool       `json:"clean"`
			Cycles [][]string `json:"cycles"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if !res.Clean || len(res.Cycles) != 0 {
			t.Errorf("expected clean graph, got clean=%v cycles=%v", res.Clean, res.Cycles)
		}
	})

	// NOTE: cycle-detection (graph check on a cyclic graph) is not ported here.
	// The embedded graph test does not cover it, and `bd dep add` refuses to
	// create a cycle, so one can't be built through supported commands. The
	// DetectCycles path is exercised by the dep/link/ready proxied duals.
}
