package docsync

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pinnedFlag matches one flag line of a generated cli-reference Flags block:
// "  -t, --type string   ..." or "      --blocks string   ...".
var pinnedFlag = regexp.MustCompile(`^\s+(?:-(\w), )?--([\w-]+)`)

// TestDependenciesDocGateExamplesUsePinnedGateCreate pins that the gate
// examples in docs/core-concepts/dependencies.md are commands the pinned
// release accepts: each creates its gate with `bd gate create --blocks
// <dependent>`, which stores the blocking edge itself, and passes only flags
// `bd gate create` registers. The flag set is read from
// docs/cli-reference/gate.md, which scripts/generate-cli-docs.sh renders from
// the bd built at docs/cli-docs.pin. `bd create` has no --await-type flag, so
// the earlier `bd create --type=gate --await-type=...` examples failed with
// "unknown flag" and left the dependent unblocked.
func TestDependenciesDocGateExamplesUsePinnedGateCreate(t *testing.T) {
	root := repoRoot()
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("reading %s: %v%s", rel, err, declaredDataHint())
		}
		return string(data)
	}
	flags := pinnedGateCreateFlags(t, read("docs/cli-reference/gate.md"))
	doc := read("docs/core-concepts/dependencies.md")

	for _, heading := range []string{"### Creating Gates", "### PR Merge Gate", "### CI Gate Before Deploy"} {
		section, ok := docSection(doc, heading)
		if !ok {
			t.Errorf("docs/core-concepts/dependencies.md has no %q section", heading)
			continue
		}
		creates := 0
		for _, cmd := range shellCommands(section) {
			if strings.Contains(cmd, "--await-type") || strings.Contains(cmd, "--type=gate") {
				t.Errorf("%s: %q creates a gate through `bd create`, which has no --await-type flag and wires no dependent: use `bd gate create --blocks <dependent>`", heading, cmd)
				continue
			}
			if !strings.HasPrefix(cmd, "bd gate create ") {
				continue
			}
			creates++
			blocks := false
			for _, arg := range strings.Fields(strings.TrimPrefix(cmd, "bd gate create ")) {
				if !strings.HasPrefix(arg, "-") {
					continue
				}
				name := strings.TrimLeft(strings.SplitN(arg, "=", 2)[0], "-")
				if !flags[name] {
					t.Errorf("%s: %q passes %s, which the pinned `bd gate create` does not register", heading, cmd, arg)
				}
				blocks = blocks || name == "blocks"
			}
			if !blocks {
				t.Errorf("%s: %q names no --blocks dependent, so it blocks nothing", heading, cmd)
			}
		}
		if creates == 0 {
			t.Errorf("%s: no `bd gate create` example", heading)
		}
	}
}

// pinnedGateCreateFlags returns the long and short flag names in the Flags
// block of the "## bd gate create" section of a generated gate.md.
func pinnedGateCreateFlags(t *testing.T, ref string) map[string]bool {
	t.Helper()
	section, ok := docSection(ref, "## bd gate create")
	if !ok {
		t.Fatal("docs/cli-reference/gate.md has no `bd gate create` section")
	}
	_, block, ok := strings.Cut(section, "**Flags:**")
	if !ok {
		t.Fatal("docs/cli-reference/gate.md `bd gate create` section has no Flags block")
	}
	flags := map[string]bool{}
	for _, line := range strings.Split(block, "\n") {
		if m := pinnedFlag.FindStringSubmatch(line); m != nil {
			flags[m[2]] = true
			if m[1] != "" {
				flags[m[1]] = true
			}
		}
	}
	if !flags["blocks"] {
		t.Fatalf("parsed no --blocks flag from the pinned `bd gate create` Flags block: %v", flags)
	}
	return flags
}

// shellCommands returns the commands in section's fenced code blocks, one per
// logical line: backslash continuations joined, comments and blank lines
// dropped.
func shellCommands(section string) []string {
	var (
		cmds    []string
		pending string
		fenced  bool
	)
	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			fenced = !fenced
			continue
		}
		if !fenced || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if body, ok := strings.CutSuffix(trimmed, `\`); ok {
			pending += body
			continue
		}
		cmd := pending + trimmed
		pending = ""
		// Trailing comments are aligned with a run of spaces; a single
		// space before "#" is inside an argument ("Wait for PR #42").
		if i := strings.Index(cmd, "  #"); i >= 0 {
			cmd = cmd[:i]
		}
		cmds = append(cmds, strings.Join(strings.Fields(cmd), " "))
	}
	return cmds
}
