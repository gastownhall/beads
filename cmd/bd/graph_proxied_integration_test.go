//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"fmt"
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

	// capRun runs bd under the given extra env and reports its exit code.
	capRun := func(t *testing.T, dir string, env []string, args ...string) (string, string, int) {
		t.Helper()
		stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, dir, env, args...)
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			return stdout, stderr, 0
		case errors.As(err, &exitErr):
			return stdout, stderr, exitErr.ExitCode()
		default:
			t.Fatalf("bd %s: %v", strings.Join(args, " "), err)
			return "", "", -1
		}
	}
	capSources := []struct {
		name   string
		source string
		args   func(n int) ([]string, []string)
	}{
		{name: "flag", source: "--max-rows", args: func(n int) ([]string, []string) {
			return nil, []string{"--max-rows", fmt.Sprint(n)}
		}},
		{name: "env", source: "BEADS_MAX_ROWS", args: func(n int) ([]string, []string) {
			return []string{fmt.Sprintf("BEADS_MAX_ROWS=%d", n)}, nil
		}},
	}

	t.Run("max_rows_caps_the_connected_component", func(t *testing.T) {
		cp := newSharedProxiedProject(t, bd, "grpcap")
		a := bdProxiedCreate(t, bd, cp.dir, "Component A", "--type", "task")
		b := bdProxiedCreate(t, bd, cp.dir, "Component B", "--type", "task")
		c := bdProxiedCreate(t, bd, cp.dir, "Component C", "--type", "task")
		for _, pair := range [][2]string{{b.ID, a.ID}, {c.ID, b.ID}} {
			if out, err := bdProxiedRun(t, bd, cp.dir, "dep", "add", pair[0], pair[1]); err != nil {
				t.Fatalf("dep add: %v\n%s", err, out)
			}
		}
		for _, format := range [][]string{nil, {"--json"}} {
			base := append([]string{"graph", a.ID}, format...)
			wantOut, wantErr, code := capRun(t, cp.dir, nil, base...)
			if code != 0 {
				t.Fatalf("uncapped graph %s %v exited %d: %s", a.ID, format, code, wantErr)
			}

			for _, src := range capSources {
				env, flags := src.args(2)
				stdout, stderr, code := capRun(t, cp.dir, env, append(append([]string{}, base...), flags...)...)
				if code != 2 {
					t.Errorf("%s %v cap 2: exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", src.name, format, code, stdout, stderr)
				}
				if want := "3 found, " + src.source + "=2 exceeded"; !strings.Contains(stderr, want) {
					t.Errorf("%s %v cap 2: stderr = %q, want %q", src.name, format, stderr, want)
				}
				if stdout != "" {
					t.Errorf("%s %v cap 2: stdout = %q, want nothing on a cap overage", src.name, format, stdout)
				}

				env, flags = src.args(3)
				stdout, stderr, code = capRun(t, cp.dir, env, append(append([]string{}, base...), flags...)...)
				if code != 0 || stdout != wantOut || stderr != wantErr {
					t.Errorf("%s %v cap 3: exit %d, output differs from uncapped\ncapped:\n%s%s\nuncapped:\n%s%s",
						src.name, format, code, stdout, stderr, wantOut, wantErr)
				}
			}
		}
	})

	t.Run("max_rows_caps_each_status_of_all", func(t *testing.T) {
		cp := newSharedProxiedProject(t, bd, "grpall")
		want := map[string]bool{}
		for _, status := range []string{"open", "in_progress", "blocked"} {
			for i := 0; i < 2; i++ {
				iss := bdProxiedCreate(t, bd, cp.dir, fmt.Sprintf("All %s %d", status, i), "--type", "task")
				if status != "open" {
					if out, err := bdProxiedRun(t, bd, cp.dir, "update", iss.ID, "--status", status); err != nil {
						t.Fatalf("update %s --status %s: %v\n%s", iss.ID, status, err, out)
					}
				}
				want[iss.ID] = true
			}
		}

		for _, src := range capSources {
			env, flags := src.args(2)
			stdout, stderr, code := capRun(t, cp.dir, env, append([]string{"graph", "--all", "--json"}, flags...)...)
			if code != 0 {
				t.Fatalf("%s cap 2: exit = %d, want 0\nstderr:\n%s", src.name, code, stderr)
			}
			var subgraphs []struct {
				Issues []struct {
					ID string `json:"id"`
				} `json:"Issues"`
			}
			if err := json.Unmarshal([]byte(stdout), &subgraphs); err != nil {
				t.Fatalf("%s cap 2: unmarshal: %v\n%s", src.name, err, stdout)
			}
			got := map[string]bool{}
			for _, sg := range subgraphs {
				for _, iss := range sg.Issues {
					got[iss.ID] = true
				}
			}
			if len(got) != len(want) {
				t.Errorf("%s cap 2: graph --all holds %v, want all six of %v", src.name, got, want)
			}
			for id := range want {
				if !got[id] {
					t.Errorf("%s cap 2: graph --all is missing %s", src.name, id)
				}
			}

			env, flags = src.args(1)
			stdout, stderr, code = capRun(t, cp.dir, env, append([]string{"graph", "--all"}, flags...)...)
			if code != 2 {
				t.Errorf("%s cap 1: exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", src.name, code, stdout, stderr)
			}
			if want := src.source + "=1 exceeded"; !strings.Contains(stderr, want) {
				t.Errorf("%s cap 1: stderr = %q, want %q", src.name, stderr, want)
			}
			if stdout != "" {
				t.Errorf("%s cap 1: stdout = %q, want nothing on a cap overage", src.name, stdout)
			}
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
