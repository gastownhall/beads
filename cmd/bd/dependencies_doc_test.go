package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

// docGateCreateLine matches one `bd gate create` invocation in the flattened
// guide text (backslash continuations joined first, so a wrapped example is
// still one invocation).
var docGateCreateLine = regexp.MustCompile(`(?m)^\s*bd gate create .*$`)

// docDepAddGateLine matches a `bd dep add <issue> <gate-id>` line and captures
// the issue being wired.
var docDepAddGateLine = regexp.MustCompile(`(?m)^\s*bd dep add (\S+) <gate-id>`)

// TestDependenciesDocGateExamplesUseRegisteredFlags guards the gate examples
// in the dependencies guide (gascity#7664): every example must be a real
// `bd gate create` invocation whose flags the command registers, must wire
// the waiter through --blocks — once, at creation, where the blocking edge is
// written in the same transaction — and must carry a timer's duration in
// --timeout, never in --await-id. The guide once showed a
// `bd create --type=gate --await-type=...` form that no bd release accepted.
func TestDependenciesDocGateExamplesUseRegisteredFlags(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(bazeltest.RepoRoot(t), "docs", "core-concepts", "dependencies.md"))
	if err != nil {
		t.Fatalf("read dependencies guide: %v", err)
	}
	flat := strings.ReplaceAll(string(raw), "\\\n", " ")

	if strings.Contains(flat, "--await-type") {
		t.Errorf("guide still shows --await-type, which no bd command registers")
	}
	if strings.Contains(flat, "bd create --type=gate") {
		t.Errorf("guide still creates gates via `bd create --type=gate`; gates are created via `bd gate create`")
	}

	invocations := docGateCreateLine.FindAllString(flat, -1)
	if len(invocations) != 7 {
		t.Errorf("guide shows %d `bd gate create` examples, want 7 (five introductory, two recipes)", len(invocations))
	}

	// The issues the examples wire at creation; a later `bd dep add` for the
	// same issue would duplicate that edge.
	wiredAtCreation := map[string]bool{}
	for _, line := range invocations {
		fields := strings.Fields(line)
		flagValues := map[string]string{}
		for i := 1; i < len(fields); i++ { // skip "bd"
			tok := fields[i]
			if !strings.HasPrefix(tok, "--") {
				continue
			}
			name, value, hasValue := strings.Cut(strings.TrimPrefix(tok, "--"), "=")
			if gateCreateCmd.Flags().Lookup(name) == nil {
				t.Errorf("example %q uses --%s, which `bd gate create` does not register", line, name)
				continue
			}
			if !hasValue && i+1 < len(fields) && !strings.HasPrefix(fields[i+1], "--") && !strings.HasPrefix(fields[i+1], "<") {
				value = fields[i+1]
				hasValue = true
				i++
			}
			if hasValue {
				if d, err := time.ParseDuration(value); err == nil && d > 0 && name != "timeout" {
					t.Errorf("example %q binds a duration (%s) to --%s; timer durations belong in --timeout", line, value, name)
				}
			}
			flagValues[name] = value
		}
		if _, ok := flagValues["blocks"]; !ok {
			t.Errorf("example %q does not set --blocks; a gate wires the issue it blocks at creation", line)
		}
		if id := flagValues["blocks"]; id != "" {
			wiredAtCreation[id] = true
		}
	}

	for _, m := range docDepAddGateLine.FindAllStringSubmatch(flat, -1) {
		if wiredAtCreation[m[1]] {
			t.Errorf("guide wires %s twice: --blocks at gate creation and `bd dep add %s <gate-id>`", m[1], m[1])
		}
	}
}
