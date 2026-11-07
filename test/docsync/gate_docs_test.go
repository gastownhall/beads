package docsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDependenciesDocGateExamplesUseGateCreate holds the gate examples in
// docs/core-concepts/dependencies.md to the command that exists. `bd create
// --type=gate` has no --await-type flag, so the old examples failed when
// pasted; `bd gate create --blocks <issue>` creates the gate and wires the
// waiter in one step (cmd/bd/gate.go), which makes a following
// `bd dep add <issue> <gate-id>` a second edge to the same gate.
func TestDependenciesDocGateExamplesUseGateCreate(t *testing.T) {
	root := repoRoot()
	path := filepath.Join(root, "docs", "core-concepts", "dependencies.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	// The flags gate create registers in cmd/bd/gate.go, minus --repo, which
	// the pinned release the docs describe does not have.
	allowed := map[string]bool{"blocks": true, "type": true, "reason": true, "await-id": true, "timeout": true, "title": true}
	// The flag that carries each type's condition.
	conditionFlag := map[string]string{"gh:pr": "await-id", "gh:run": "await-id", "bead": "await-id", "timer": "timeout", "human": "reason"}

	seen := map[string]bool{}
	for _, cmd := range shellCommands(string(data)) {
		fields := strings.Fields(cmd)
		switch {
		case strings.HasPrefix(cmd, "bd create") && (strings.Contains(cmd, "--type=gate") || strings.Contains(cmd, "--await-type")):
			t.Errorf("gate example uses bd create, which has no --await-type flag; use bd gate create --blocks: %s", cmd)
		case strings.HasPrefix(cmd, "bd dep add") && strings.Contains(cmd, "<gate-id>"):
			t.Errorf("gate example adds a second edge to a gate that --blocks already wired: %s", cmd)
		case len(fields) >= 3 && fields[0] == "bd" && fields[1] == "gate" && fields[2] == "create":
			flags := map[string]string{}
			args := fields[3:]
			for i, f := range args {
				if !strings.HasPrefix(f, "--") {
					continue
				}
				name, value, hasValue := strings.Cut(strings.TrimPrefix(f, "--"), "=")
				if !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
					value = args[i+1]
				}
				if !allowed[name] {
					t.Errorf("gate example uses --%s, which bd gate create does not take: %s", name, cmd)
				}
				flags[name] = value
			}
			if flags["blocks"] == "" {
				t.Errorf("gate example must name the waiting issue with --blocks: %s", cmd)
			}
			typ := flags["type"]
			if typ == "" {
				typ = "human"
			}
			if want, ok := conditionFlag[typ]; !ok {
				t.Errorf("gate example uses unknown type %q: %s", typ, cmd)
			} else if flags[want] == "" {
				t.Errorf("%s gate example must set --%s: %s", typ, want, cmd)
			}
			seen[typ] = true
		}
	}
	for typ := range conditionFlag {
		if !seen[typ] {
			t.Errorf("docs/core-concepts/dependencies.md has no bd gate create example for the %s gate type", typ)
		}
	}
}

// shellCommands returns the commands in doc's fenced code blocks with
// backslash continuations joined and comments dropped. A trailing comment
// starts at " # ", so a quoted "PR #42" survives.
func shellCommands(doc string) []string {
	var (
		out     []string
		fenced  bool
		pending string
	)
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			fenced = !fenced
			pending = ""
			continue
		}
		if !fenced {
			continue
		}
		if i := strings.Index(trimmed, " # "); i >= 0 {
			trimmed = strings.TrimSpace(trimmed[:i])
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if cont, ok := strings.CutSuffix(trimmed, "\\"); ok {
			pending += cont
			continue
		}
		if cmd := strings.TrimSpace(pending + trimmed); cmd != "" {
			out = append(out, cmd)
		}
		pending = ""
	}
	return out
}
