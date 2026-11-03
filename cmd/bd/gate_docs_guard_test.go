package main

// The gate-creation examples in docs/core-concepts/dependencies.md are the
// user-facing half of gascity#7664's doc fix. The page taught
// `bd create --type=gate --await-type=...`, a flag surface no command
// accepts, instead of the canonical `bd gate create --blocks ...` that makes
// the gate AND wires its blocking edge in one command.
//
// This guard extracts every `bd gate create` example the page ships, runs
// each against a fresh isolated workspace with the illustrative blocked-id
// replaced by a real one, and holds the page to three facts:
//
//  1. no stale gate-creation surface survives anywhere on the page,
//  2. every example runs as written and leaves one more gate behind,
//  3. each example's blocked issue stops being ready — the gate really
//     blocks, which is the whole point of --blocks.
//
// The base fails this guard the moment the page's examples are extracted:
// `bd create --type=gate --await-type=gh:pr` is not a command bd has.
//
// The doc file rides in as bazel test data (exports_files in the root
// BUILD.bazel plus the bd_test data list), so the guard runs in every lane
// that runs bd_test; outside bazel the relative path resolves against the
// package directory, like the tracked-hook guard's ../../.githooks probe.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gateDocEnv is the hermetic subprocess environment the guard shares with the
// create-atomicity fixtures: ambient BEADS_/BD_ configuration stripped so a
// developer shell pointing at a shared Dolt server or a real workspace
// cannot leak in, BEADS_DIR pinned to the isolated workspace.
func gateDocEnv(dir string) []string {
	var env []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "BEADS_") || strings.HasPrefix(e, "BD_") {
			continue
		}
		env = append(env, e)
	}
	return append(env,
		"BEADS_DIR="+filepath.Join(dir, ".beads"),
		"BD_NON_INTERACTIVE=1",
	)
}

// gateDocRunBD runs bd in the isolated workspace and returns stdout; a gate
// example that cannot run is the whole finding, so failures are fatal.
func gateDocRunBD(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = gateDocEnv(dir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bd %v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, out, stderr.String())
	}
	return string(out)
}

// gateDocBashBlocks returns the fenced ```bash blocks of a Markdown page,
// with backslash line-continuations joined, so an example that spans lines is
// one command.
func gateDocBashBlocks(page string) []string {
	var blocks []string
	for _, block := range strings.Split(page, "```") {
		if !strings.HasPrefix(block, "bash\n") {
			continue
		}
		var joined []string
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if n := len(joined); n > 0 && strings.HasSuffix(joined[n-1], "\\") {
				joined[n-1] = strings.TrimSuffix(joined[n-1], "\\") + " " + line
				continue
			}
			joined = append(joined, line)
		}
		blocks = append(blocks, joined...)
	}
	return blocks
}

// gateDocShellSplit splits one shell line into words, honoring double-quoted
// segments the way the page writes flag values (`--reason="Need design
// review"`); the examples carry no escapes.
func gateDocShellSplit(line string) []string {
	var words []string
	var current strings.Builder
	inQuote := false
	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

// gateDocGateCreates extracts every `bd gate create ...` example on the page
// as an argument list (the `bd` verb already dropped).
func gateDocGateCreates(page string) [][]string {
	var cmds [][]string
	for _, line := range gateDocBashBlocks(page) {
		words := gateDocShellSplit(line)
		if len(words) >= 2 && words[0] == "bd" && words[1] == "gate" && words[2] == "create" {
			cmds = append(cmds, words[3:])
		}
	}
	return cmds
}

// gateDocBlocksValue returns the example's --blocks operand, in either the
// spaced (`--blocks bd-abc`) or equals (`--blocks=bd-abc`) form.
func gateDocBlocksValue(args []string) string {
	for i, arg := range args {
		if arg == "--blocks" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "--blocks=") {
			return strings.TrimPrefix(arg, "--blocks=")
		}
	}
	return ""
}

// gateDocJSONID pulls the created issue's id out of `bd create --json`
// output, which may carry warning lines before the JSON object.
func gateDocJSONID(t *testing.T, out string) string {
	t.Helper()
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("no JSON object in create output:\n%s", out)
	}
	var issue struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out[start:]), &issue); err != nil {
		t.Fatalf("parse create --json output: %v\n%s", err, out)
	}
	if issue.ID == "" {
		t.Fatalf("create --json output has empty id:\n%s", out)
	}
	return issue.ID
}

// gateDocJSONArray parses a bd --json array that may be preceded by notice
// lines; an empty answer is a nil slice, the way the gate-list fixtures read
// it.
func gateDocJSONArray(t *testing.T, out string) []map[string]any {
	t.Helper()
	start := strings.Index(out, "[")
	if start < 0 {
		return nil
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(out[start:]), &arr); err != nil {
		t.Fatalf("parse JSON array output: %v\n%s", err, out)
	}
	return arr
}

func TestGateDocExamplesRun(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "core-concepts", "dependencies.md"))
	if err != nil {
		t.Fatalf("read docs/core-concepts/dependencies.md: %v", err)
	}
	page := string(doc)

	// 1. The stale surface must be gone from the page outright — a reader
	// who copies any command on it must land on flags bd has.
	for _, stale := range []string{"--await-type", "bd create --type=gate"} {
		if strings.Contains(page, stale) {
			t.Errorf("docs/core-concepts/dependencies.md still teaches %q, the surface bd gate create replaced", stale)
		}
	}

	cmds := gateDocGateCreates(page)
	if len(cmds) < 5 {
		t.Fatalf("expected at least 5 `bd gate create` examples on the page, found %d", len(cmds))
	}

	bd := buildBDForInitTests(t)
	dir := t.TempDir()
	gateDocRunBD(t, bd, dir, "init", "--backend", "dolt", "--prefix", "gg",
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")

	// One real issue per distinct illustrative blocked-id the examples name;
	// every --blocks on the page is an id the examples made up.
	blockedIDs := make(map[string]string)
	var blockedReal []string
	for _, args := range cmds {
		illustrative := gateDocBlocksValue(args)
		if illustrative == "" {
			t.Errorf("example `bd gate create %s` has no --blocks: gate create must name the issue it blocks", strings.Join(args, " "))
			continue
		}
		if _, ok := blockedIDs[illustrative]; ok {
			continue
		}
		out := gateDocRunBD(t, bd, dir, "create", "guard target for "+illustrative, "--json")
		blockedIDs[illustrative] = gateDocJSONID(t, out)
		blockedReal = append(blockedReal, blockedIDs[illustrative])
	}

	// 2. Every example runs as written, with the real blocked-id
	// substituted, and reports the gate it created.
	for _, args := range cmds {
		illustrative := gateDocBlocksValue(args)
		if illustrative == "" {
			continue
		}
		runArgs := make([]string, 0, len(args)+2)
		runArgs = append(runArgs, "gate", "create")
		for i, arg := range args {
			switch {
			case arg == "--blocks" && i+1 < len(args):
				runArgs = append(runArgs, "--blocks", blockedIDs[illustrative])
				continue
			case strings.HasPrefix(arg, "--blocks="):
				runArgs = append(runArgs, "--blocks="+blockedIDs[illustrative])
				continue
			case i > 0 && args[i-1] == "--blocks":
				continue // already substituted above
			}
			runArgs = append(runArgs, arg)
		}
		out := gateDocRunBD(t, bd, dir, runArgs...)
		if !strings.Contains(out, "Created gate") {
			t.Errorf("example `bd %s` ran but reported no created gate:\n%s", strings.Join(runArgs, " "), out)
		}
	}

	list := gateDocRunBD(t, bd, dir, "gate", "list", "--json")
	if got, want := len(gateDocJSONArray(t, list)), len(cmds); got != want {
		t.Errorf("the page's %d examples left %d gates behind:\n%s", want, got, list)
	}

	// 3. The gates really block: none of the blocked issues is ready.
	ready := gateDocRunBD(t, bd, dir, "ready", "--json")
	readyIDs := make(map[string]bool)
	for _, row := range gateDocJSONArray(t, ready) {
		if id, _ := row["id"].(string); id != "" {
			readyIDs[id] = true
		}
	}
	for illustrative, id := range blockedIDs {
		if readyIDs[id] {
			t.Errorf("issue %s (%q on the page) is ready although a gate blocks it", id, illustrative)
		}
	}
}
