//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func bdProxiedDep(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"dep"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd dep %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func bdProxiedDepFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"dep"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd dep %s to fail, but succeeded:\n%s",
			strings.Join(args, " "), out)
	}
	return string(out)
}

func bdProxiedDepJSON(t *testing.T, bd, dir string, args ...string) map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"dep"}, args...)
	fullArgs = append(fullArgs, "--json")
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd dep --json %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		t.Fatalf("no JSON in dep output: %s", s)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("parse dep JSON: %v\n%s", err, s)
	}
	return m
}

func bdProxiedDepJSONArray(t *testing.T, bd, dir string, args ...string) []interface{} {
	t.Helper()
	fullArgs := append([]string{"dep"}, args...)
	fullArgs = append(fullArgs, "--json")
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd dep --json %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
	start := strings.Index(s, "[")
	if start < 0 {
		t.Fatalf("no JSON array in dep output: %s", s)
	}
	var arr []interface{}
	if err := json.Unmarshal([]byte(s[start:]), &arr); err != nil {
		t.Fatalf("parse dep JSON array: %v\n%s", err, s)
	}
	return arr
}

func bdProxiedDepWithInput(t *testing.T, bd, dir, input string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"dep"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	cmd.Stdin = strings.NewReader(input)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd dep %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func bdProxiedDepWithInputFail(t *testing.T, bd, dir, input string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"dep"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd dep %s to fail, but succeeded:\n%s",
			strings.Join(args, " "), out)
	}
	return string(out)
}

func TestProxiedServerDep(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("Blocks", func(t *testing.T) {
		t.Parallel()
		t.Run("happy_path", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpb1")
			a := bdProxiedCreate(t, bd, p.dir, "Block A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "Block B", "--type", "task")
			out := bdProxiedDep(t, bd, p.dir, a.ID, "--blocks", b.ID)
			if !strings.Contains(out, "Added") || !strings.Contains(out, "blocks") {
				t.Errorf("expected 'Added ... blocks' output: %s", out)
			}
		})
	})

	t.Run("Add", func(t *testing.T) {
		t.Parallel()
		t.Run("positional_args", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa1")
			a := bdProxiedCreate(t, bd, p.dir, "Pos A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "Pos B", "--type", "task")
			out := bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			if !strings.Contains(out, "Added dependency") {
				t.Errorf("expected 'Added dependency': %s", out)
			}
		})

		t.Run("type_parent_child", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa2")
			epic := bdProxiedCreate(t, bd, p.dir, "Epic", "--type", "epic")
			c1 := bdProxiedCreate(t, bd, p.dir, "Child 1", "--type", "task")
			c2 := bdProxiedCreate(t, bd, p.dir, "Child 2", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", c1.ID, epic.ID, "--type", "parent-child")
			bdProxiedDep(t, bd, p.dir, "add", c2.ID, epic.ID, "--type", "parent-child")
			out := bdProxiedDep(t, bd, p.dir, "list", epic.ID, "--direction", "up")
			if !strings.Contains(out, c1.ID) || !strings.Contains(out, c2.ID) {
				t.Errorf("expected both children in dependents: %s", out)
			}
		})

		t.Run("type_tracks", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa3")
			tracker := bdProxiedCreate(t, bd, p.dir, "Tracker", "--type", "task")
			tracked := bdProxiedCreate(t, bd, p.dir, "Tracked", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", tracker.ID, tracked.ID, "--type", "tracks")
			out := bdProxiedDep(t, bd, p.dir, "list", tracker.ID)
			if !strings.Contains(out, tracked.ID) {
				t.Errorf("expected tracked issue in deps: %s", out)
			}
		})

		t.Run("type_related", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa4")
			r1 := bdProxiedCreate(t, bd, p.dir, "Related 1", "--type", "task")
			r2 := bdProxiedCreate(t, bd, p.dir, "Related 2", "--type", "task")
			out := bdProxiedDep(t, bd, p.dir, "add", r1.ID, r2.ID, "--type", "related")
			if !strings.Contains(out, "Added dependency") {
				t.Errorf("expected related edge accepted: %s", out)
			}
		})

		t.Run("blocked_by_flag", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa5")
			x := bdProxiedCreate(t, bd, p.dir, "Blocked", "--type", "task")
			y := bdProxiedCreate(t, bd, p.dir, "Blocker", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", x.ID, "--blocked-by", y.ID)
			out := bdProxiedDep(t, bd, p.dir, "list", x.ID)
			if !strings.Contains(out, y.ID) {
				t.Errorf("expected blocker in deps: %s", out)
			}
		})

		t.Run("depends_on_flag", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa6")
			x := bdProxiedCreate(t, bd, p.dir, "Dependent", "--type", "task")
			y := bdProxiedCreate(t, bd, p.dir, "Dependency", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", x.ID, "--depends-on", y.ID)
			out := bdProxiedDep(t, bd, p.dir, "list", x.ID)
			if !strings.Contains(out, y.ID) {
				t.Errorf("expected dependency in deps: %s", out)
			}
		})

		t.Run("cycle_rejected", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa7")
			a := bdProxiedCreate(t, bd, p.dir, "Cyc A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "Cyc B", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			out := bdProxiedDepFail(t, bd, p.dir, "add", b.ID, a.ID)
			if !strings.Contains(strings.ToLower(out), "cycle") {
				t.Errorf("expected 'cycle' error: %s", out)
			}
		})

		t.Run("child_parent_antipattern", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa8")
			parent := bdProxiedCreate(t, bd, p.dir, "AP Parent", "--type", "epic")
			child := bdProxiedCreate(t, bd, p.dir, "AP Child", "--type", "task", "--parent", parent.ID)
			out := bdProxiedDepFail(t, bd, p.dir, "add", child.ID, parent.ID)
			if !strings.Contains(out, "child") && !strings.Contains(out, "deadlock") {
				t.Errorf("expected child→parent rejection: %s", out)
			}
		})

		t.Run("json_output", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa9")
			j1 := bdProxiedCreate(t, bd, p.dir, "JSON A", "--type", "task")
			j2 := bdProxiedCreate(t, bd, p.dir, "JSON B", "--type", "task")
			m := bdProxiedDepJSON(t, bd, p.dir, "add", j1.ID, j2.ID)
			if m["status"] != "added" {
				t.Errorf("expected status=added, got %v", m["status"])
			}
			if m["issue_id"] != j1.ID || m["depends_on_id"] != j2.ID {
				t.Errorf("unexpected envelope: %v", m)
			}
		})

		t.Run("bulk_file_jsonl", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa10")
			b1 := bdProxiedCreate(t, bd, p.dir, "Bulk A", "--type", "task")
			b2 := bdProxiedCreate(t, bd, p.dir, "Bulk B", "--type", "task")
			b3 := bdProxiedCreate(t, bd, p.dir, "Bulk C", "--type", "task")
			path := filepath.Join(t.TempDir(), "deps.jsonl")
			body := fmt.Sprintf("{\"from\":%q,\"to\":%q}\n{\"issue_id\":%q,\"depends_on_id\":%q,\"type\":\"tracks\"}\n",
				b1.ID, b2.ID, b3.ID, b2.ID)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write deps file: %v", err)
			}
			out := bdProxiedDep(t, bd, p.dir, "add", "--file", path)
			if !strings.Contains(out, "Added 2 dependencies") {
				t.Fatalf("expected bulk add summary: %s", out)
			}
			list1 := bdProxiedDep(t, bd, p.dir, "list", b1.ID)
			if !strings.Contains(list1, b2.ID) {
				t.Fatalf("expected first bulk dep in list: %s", list1)
			}
			list3 := bdProxiedDep(t, bd, p.dir, "list", b3.ID)
			if !strings.Contains(list3, b2.ID) || !strings.Contains(list3, "tracks") {
				t.Fatalf("expected typed bulk dep in list: %s", list3)
			}
		})

		t.Run("bulk_file_validation_atomic", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa11")
			v1 := bdProxiedCreate(t, bd, p.dir, "Valid A", "--type", "task")
			v2 := bdProxiedCreate(t, bd, p.dir, "Valid B", "--type", "task")
			path := filepath.Join(t.TempDir(), "bad-deps.jsonl")
			body := fmt.Sprintf("{\"from\":%q,\"to\":%q}\n{\"from\":\"\",\"to\":%q}\n{\"from\":%q,\"to\":\"\"}\n",
				v1.ID, v2.ID, v2.ID, v1.ID)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write bad deps file: %v", err)
			}
			out := bdProxiedDepFail(t, bd, p.dir, "add", "--file", path)
			if !strings.Contains(out, "line 2: missing from") || !strings.Contains(out, "line 3: missing to") {
				t.Fatalf("expected all validation errors: %s", out)
			}
			list := bdProxiedDep(t, bd, p.dir, "list", v1.ID)
			if strings.Contains(list, v2.ID) {
				t.Fatalf("bulk validation failure should not partial-commit: %s", list)
			}
		})

		t.Run("no_cycle_check_chain", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa12")
			const n = 10
			ids := make([]string, n)
			for i := 0; i < n; i++ {
				issue := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("chain-%d", i), "--type", "task")
				ids[i] = issue.ID
			}
			for i := 1; i < n; i++ {
				out := bdProxiedDep(t, bd, p.dir, "add", ids[i], ids[i-1], "--no-cycle-check")
				if strings.Contains(out, "cycle") {
					t.Errorf("unexpected cycle output with --no-cycle-check: %s", out)
				}
			}
			cyclesOut := bdProxiedDep(t, bd, p.dir, "cycles")
			if strings.Contains(cyclesOut, "Found") {
				t.Errorf("unexpected cycles after acyclic chain wiring: %s", cyclesOut)
			}
			extra := bdProxiedCreate(t, bd, p.dir, "chain-extra", "--type", "task")
			bdProxiedDep(t, bd, p.dir, extra.ID, "--blocks", ids[n-1], "--no-cycle-check")
		})

		t.Run("bulk_no_cycle_check_whole_graph_gate", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa13")
			a := bdProxiedCreate(t, bd, p.dir, "Gate A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "Gate B", "--type", "task")
			c := bdProxiedCreate(t, bd, p.dir, "Gate C", "--type", "task")

			okInput := fmt.Sprintf("{\"from\":%q,\"to\":%q}\n{\"from\":%q,\"to\":%q}\n",
				a.ID, b.ID, b.ID, c.ID)
			out := bdProxiedDepWithInput(t, bd, p.dir, okInput, "add", "--file", "-", "--no-cycle-check")
			if !strings.Contains(out, "Added 2 dependencies") {
				t.Fatalf("expected acyclic bulk add summary: %s", out)
			}

			cycleInput := fmt.Sprintf("{\"from\":%q,\"to\":%q}\n", c.ID, a.ID)
			failOut := bdProxiedDepWithInputFail(t, bd, p.dir, cycleInput, "add", "--file", "-", "--no-cycle-check")
			if !strings.Contains(strings.ToLower(failOut), "cycle") {
				t.Fatalf("expected whole-graph cycle rejection: %s", failOut)
			}

			cycles := bdProxiedDep(t, bd, p.dir, "cycles")
			if !strings.Contains(cycles, "No dependency cycles detected") {
				t.Fatalf("expected rolled-back bulk add to leave graph acyclic: %s", cycles)
			}
		})

		t.Run("commit_visible_to_subsequent_invocation", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa14")
			a := bdProxiedCreate(t, bd, p.dir, "Commit A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "Commit B", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			out := bdProxiedDep(t, bd, p.dir, "list", a.ID)
			if !strings.Contains(out, b.ID) {
				t.Fatalf("dep not visible in fresh CLI invocation after add: %s", out)
			}
		})

		t.Run("bulk_external_ref_passthrough", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpa15")
			src := bdProxiedCreate(t, bd, p.dir, "Ext src", "--type", "task")
			input := fmt.Sprintf("{\"from\":%q,\"to\":\"external:other:capability\"}\n", src.ID)
			cmd := exec.Command(bd, "dep", "add", "--file", "-")
			cmd.Dir = p.dir
			cmd.Env = bdProxiedEnv(p.dir)
			cmd.Stdin = strings.NewReader(input)
			out, err := cmd.CombinedOutput()
			if err != nil {
				if !strings.Contains(string(out), "external") {
					t.Fatalf("external-ref bulk add failed without a clear external-ref error: %v\n%s", err, out)
				}
				return
			}
			if !strings.Contains(string(out), "Added 1 dependencies") {
				t.Fatalf("expected 1-edge bulk add summary or clear external-ref error: %s", out)
			}
		})
	})

}

func TestProxiedServerDep2(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("Remove", func(t *testing.T) {
		t.Parallel()
		t.Run("basic", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpr1")
			a := bdProxiedCreate(t, bd, p.dir, "Rm A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "Rm B", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			out := bdProxiedDep(t, bd, p.dir, "remove", a.ID, b.ID)
			if !strings.Contains(out, "Removed") {
				t.Errorf("expected 'Removed': %s", out)
			}
		})

		t.Run("rm_alias", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpr2")
			a := bdProxiedCreate(t, bd, p.dir, "Rm A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "Rm B", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			out := bdProxiedDep(t, bd, p.dir, "rm", a.ID, b.ID)
			if !strings.Contains(out, "Removed") {
				t.Errorf("expected 'Removed' via rm alias: %s", out)
			}
		})

		t.Run("json_output", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpr3")
			a := bdProxiedCreate(t, bd, p.dir, "Rm JSON A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "Rm JSON B", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			m := bdProxiedDepJSON(t, bd, p.dir, "remove", a.ID, b.ID)
			if m["status"] != "removed" {
				t.Errorf("expected status=removed, got %v", m["status"])
			}
		})
	})

	t.Run("List", func(t *testing.T) {
		t.Parallel()
		t.Run("default_direction_down", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpl1")
			a := bdProxiedCreate(t, bd, p.dir, "L A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "L B", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			out := bdProxiedDep(t, bd, p.dir, "list", a.ID)
			if !strings.Contains(out, b.ID) {
				t.Errorf("expected dependency in list output: %s", out)
			}
		})

		t.Run("direction_up", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpl2")
			a := bdProxiedCreate(t, bd, p.dir, "Up A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "Up B", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			out := bdProxiedDep(t, bd, p.dir, "list", b.ID, "--direction", "up")
			if !strings.Contains(out, a.ID) {
				t.Errorf("expected dependent in --direction up: %s", out)
			}
		})

		t.Run("type_filter", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpl3")
			epic := bdProxiedCreate(t, bd, p.dir, "TF epic", "--type", "epic")
			c1 := bdProxiedCreate(t, bd, p.dir, "TF c1", "--type", "task")
			c2 := bdProxiedCreate(t, bd, p.dir, "TF c2", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", c1.ID, epic.ID, "--type", "parent-child")
			bdProxiedDep(t, bd, p.dir, "add", c2.ID, epic.ID, "--type", "parent-child")
			out := bdProxiedDep(t, bd, p.dir, "list", epic.ID, "--direction", "up", "--type", "parent-child")
			if !strings.Contains(out, c1.ID) || !strings.Contains(out, c2.ID) {
				t.Errorf("expected children in type-filtered list: %s", out)
			}
		})

		t.Run("json_output", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpl4")
			a := bdProxiedCreate(t, bd, p.dir, "LJ A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "LJ B", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			arr := bdProxiedDepJSONArray(t, bd, p.dir, "list", a.ID)
			if len(arr) == 0 {
				t.Fatalf("expected non-empty JSON list array")
			}
			raw, _ := json.Marshal(arr)
			if !strings.Contains(string(raw), b.ID) {
				t.Errorf("expected dependency target in JSON: %s", string(raw))
			}
		})

		t.Run("batch_multi_arg", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpl5")
			a := bdProxiedCreate(t, bd, p.dir, "B A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "B B", "--type", "task")
			c := bdProxiedCreate(t, bd, p.dir, "B C", "--type", "task")
			d := bdProxiedCreate(t, bd, p.dir, "B D", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			bdProxiedDep(t, bd, p.dir, "add", c.ID, d.ID)
			out := bdProxiedDep(t, bd, p.dir, "list", a.ID, c.ID)
			if !strings.Contains(out, b.ID) || !strings.Contains(out, d.ID) {
				t.Errorf("expected both deps in batch list: %s", out)
			}
			arr := bdProxiedDepJSONArray(t, bd, p.dir, "list", a.ID, c.ID)
			if len(arr) != 2 {
				t.Errorf("expected 2 deps in batch JSON, got %d: %v", len(arr), arr)
			}
		})

		t.Run("batch_partitions_wisp_and_issue_ids", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpl6")
			issueSrc := bdProxiedCreate(t, bd, p.dir, "WP issue src", "--type", "task")
			issueTgt := bdProxiedCreate(t, bd, p.dir, "WP issue tgt", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", issueSrc.ID, issueTgt.ID)

			db := openProxiedDB(t, p)
			wispSrc := fmt.Sprintf("%s-wisp-src", p.prefix)
			wispTgt := fmt.Sprintf("%s-wisp-tgt", p.prefix)
			if _, err := db.ExecContext(context.Background(),
				"INSERT INTO wisps (id, title) VALUES (?, ?), (?, ?)",
				wispSrc, "wisp src", wispTgt, "wisp tgt"); err != nil {
				t.Fatalf("seed wisps: %v", err)
			}
			if _, err := db.ExecContext(context.Background(),
				"INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by, metadata) VALUES (UUID(), ?, ?, 'blocks', NOW(), 'tester', '{}')",
				wispSrc, wispTgt); err != nil {
				t.Fatalf("seed wisp dep: %v", err)
			}

			arr := bdProxiedDepJSONArray(t, bd, p.dir, "list", issueSrc.ID, wispSrc)
			raw, _ := json.Marshal(arr)
			if !strings.Contains(string(raw), issueTgt.ID) {
				t.Errorf("expected issue target %s in batch list: %s", issueTgt.ID, string(raw))
			}
			if !strings.Contains(string(raw), wispTgt) {
				t.Errorf("expected wisp target %s in batch list (partitioning path): %s", wispTgt, string(raw))
			}
		})
	})

	t.Run("Tree", func(t *testing.T) {
		t.Parallel()
		t.Run("basic", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpt1")
			root := bdProxiedCreate(t, bd, p.dir, "Tree root", "--type", "task")
			child := bdProxiedCreate(t, bd, p.dir, "Tree child", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", root.ID, child.ID)
			out := bdProxiedDep(t, bd, p.dir, "tree", root.ID)
			if !strings.Contains(out, root.ID) {
				t.Errorf("expected root ID in tree: %s", out)
			}
		})

		t.Run("direction_up", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpt2")
			root := bdProxiedCreate(t, bd, p.dir, "Up tree root", "--type", "task")
			dep := bdProxiedCreate(t, bd, p.dir, "Up tree dep", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", root.ID, dep.ID)
			out := bdProxiedDep(t, bd, p.dir, "tree", dep.ID, "--direction", "up")
			if len(strings.TrimSpace(out)) == 0 {
				t.Error("expected non-empty tree output for --direction up")
			}
		})

		t.Run("direction_both", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpt3")
			root := bdProxiedCreate(t, bd, p.dir, "Both root", "--type", "task")
			dep := bdProxiedCreate(t, bd, p.dir, "Both dep", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", root.ID, dep.ID)
			out := bdProxiedDep(t, bd, p.dir, "tree", root.ID, "--direction", "both")
			if len(strings.TrimSpace(out)) == 0 {
				t.Error("expected non-empty tree output for --direction both")
			}
		})

		t.Run("both_does_not_duplicate_root", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpt4")
			root := bdProxiedCreate(t, bd, p.dir, "Dedup root", "--type", "task")
			depDown := bdProxiedCreate(t, bd, p.dir, "Dedup down", "--type", "task")
			depUp := bdProxiedCreate(t, bd, p.dir, "Dedup up", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", root.ID, depDown.ID)
			bdProxiedDep(t, bd, p.dir, "add", depUp.ID, root.ID)
			arr := bdProxiedDepJSONArray(t, bd, p.dir, "tree", root.ID, "--direction", "both")
			count := 0
			for _, node := range arr {
				m, ok := node.(map[string]interface{})
				if !ok {
					continue
				}
				if id, _ := m["id"].(string); id == root.ID {
					count++
				}
			}
			if count != 1 {
				t.Errorf("expected root %s to appear exactly once in both tree, got %d: %v", root.ID, count, arr)
			}
		})

		t.Run("max_depth", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpt5")
			a := bdProxiedCreate(t, bd, p.dir, "MD A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "MD B", "--type", "task")
			c := bdProxiedCreate(t, bd, p.dir, "MD C", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			bdProxiedDep(t, bd, p.dir, "add", b.ID, c.ID)
			out := bdProxiedDep(t, bd, p.dir, "tree", a.ID, "--max-depth", "1")
			if !strings.Contains(out, a.ID) {
				t.Errorf("expected root in shallow tree: %s", out)
			}
		})

		t.Run("status_filter", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpt6")
			root := bdProxiedCreate(t, bd, p.dir, "SF root", "--type", "task")
			dep := bdProxiedCreate(t, bd, p.dir, "SF dep", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", root.ID, dep.ID)
			_ = bdProxiedDep(t, bd, p.dir, "tree", root.ID, "--status", "open")
		})

		t.Run("format_mermaid", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpt7")
			root := bdProxiedCreate(t, bd, p.dir, "Mer root", "--type", "task")
			dep := bdProxiedCreate(t, bd, p.dir, "Mer dep", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", root.ID, dep.ID)
			out := bdProxiedDep(t, bd, p.dir, "tree", root.ID, "--format", "mermaid")
			if !strings.Contains(out, "flowchart") && !strings.Contains(out, "graph") {
				t.Errorf("expected mermaid flowchart/graph syntax: %s", out)
			}
		})

		t.Run("json_output", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpt8")
			root := bdProxiedCreate(t, bd, p.dir, "JT root", "--type", "task")
			dep := bdProxiedCreate(t, bd, p.dir, "JT dep", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", root.ID, dep.ID)
			arr := bdProxiedDepJSONArray(t, bd, p.dir, "tree", root.ID)
			raw, _ := json.Marshal(arr)
			if !strings.Contains(string(raw), root.ID) {
				t.Errorf("expected root ID in JSON tree: %s", string(raw))
			}
		})

		t.Run("ignores_relates_to", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpt9")
			root := bdProxiedCreate(t, bd, p.dir, "Rel root", "--type", "task")
			blocker := bdProxiedCreate(t, bd, p.dir, "Real blocker", "--type", "task")
			related := bdProxiedCreate(t, bd, p.dir, "Loose relation", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", root.ID, blocker.ID, "--type", "blocks")
			bdProxiedDep(t, bd, p.dir, "add", root.ID, related.ID, "--type", "relates-to")
			bdProxiedDep(t, bd, p.dir, "add", related.ID, root.ID, "--type", "relates-to")

			listOut := bdProxiedDep(t, bd, p.dir, "list", root.ID)
			if !strings.Contains(listOut, related.ID) || !strings.Contains(listOut, "relates-to") {
				t.Fatalf("expected relates-to in dep list: %s", listOut)
			}

			treeOut := bdProxiedDep(t, bd, p.dir, "tree", root.ID)
			if !strings.Contains(treeOut, root.ID) || !strings.Contains(treeOut, blocker.ID) {
				t.Fatalf("expected root + real blocker in tree: %s", treeOut)
			}
			if strings.Contains(treeOut, related.ID) {
				t.Fatalf("relates-to should not render in tree: %s", treeOut)
			}

			upOut := bdProxiedDep(t, bd, p.dir, "tree", related.ID, "--direction", "up")
			if strings.Contains(upOut, root.ID) {
				t.Fatalf("reverse relates-to should not render in dependent tree: %s", upOut)
			}
		})
	})

	t.Run("Cycles", func(t *testing.T) {
		t.Parallel()
		t.Run("detect", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpc1")
			a := bdProxiedCreate(t, bd, p.dir, "C A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "C B", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)

			db := openProxiedDB(t, p)
			if _, err := db.ExecContext(context.Background(),
				"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata) VALUES (UUID(), ?, ?, 'blocks', NOW(), 'tester', '{}')",
				b.ID, a.ID); err != nil {
				t.Fatalf("seed cycle: %v", err)
			}

			out := bdProxiedDep(t, bd, p.dir, "cycles")
			if !strings.Contains(out, a.ID) || !strings.Contains(out, b.ID) {
				t.Errorf("expected cycle members in output: %s", out)
			}
		})

		t.Run("none", func(t *testing.T) {
			t.Parallel()
			p := newSharedProxiedProject(t, bd, "dpc2")
			a := bdProxiedCreate(t, bd, p.dir, "NC A", "--type", "task")
			b := bdProxiedCreate(t, bd, p.dir, "NC B", "--type", "task")
			bdProxiedDep(t, bd, p.dir, "add", a.ID, b.ID)
			out := bdProxiedDep(t, bd, p.dir, "cycles")
			if !strings.Contains(out, "No dependency cycles detected") {
				t.Errorf("expected no-cycle message: %s", out)
			}
		})
	})
}

func TestProxiedServerDepConcurrent(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "dpcon")

	var ids []string
	for i := 0; i < 16; i++ {
		issue := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("concurrent-%d", i), "--type", "task")
		ids = append(ids, issue.ID)
	}
	for i := 0; i < 8; i++ {
		bdProxiedDep(t, bd, p.dir, "add", ids[i*2], ids[i*2+1])
	}

	const numWorkers = 8
	type workerResult struct {
		worker int
		err    error
	}
	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}
			id := ids[worker*2]

			cmd := exec.Command(bd, "dep", "list", id, "--json")
			cmd.Dir = p.dir
			cmd.Env = bdProxiedEnv(p.dir)
			if out, err := cmd.CombinedOutput(); err != nil {
				r.err = fmt.Errorf("worker %d dep list: %v\n%s", worker, err, out)
				results[worker] = r
				return
			}

			cmd = exec.Command(bd, "dep", "tree", id)
			cmd.Dir = p.dir
			cmd.Env = bdProxiedEnv(p.dir)
			if out, err := cmd.CombinedOutput(); err != nil {
				r.err = fmt.Errorf("worker %d dep tree: %v\n%s", worker, err, out)
				results[worker] = r
				return
			}
			results[worker] = r
		}(w)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
