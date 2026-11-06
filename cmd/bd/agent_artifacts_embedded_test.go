//go:build cgo

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/templates/agents"
)

// TestEmbeddedInitAgentArtifactsGolden pins the Claude agent artifacts a real
// `bd init` writes — .claude/settings.json with its hook events and commands,
// CLAUDE.md and AGENTS.md with their managed sections — and what
// `bd setup claude --check`, `--remove` and a reinstall then report and leave
// behind, through the bd binary. Each scenario is a transcript golden under
// testdata/agent_artifacts/, so a change to any artifact or outcome (a new
// hook event, a changed command, new template text and its hash) shows up as
// a reviewed golden diff.
//
// A managed section is shown by its BEGIN marker line (profile and hash) and a
// pointer to the internal/templates/agents golden that pins its bytes
// (TestManagedSectionGolden), after checking the file holds exactly that
// section; one that matches no rendered variant is shown in full. The
// in-process `bd setup claude` scenarios are pinned by
// TestClaudeSetupArtifactsGolden in cmd/bd/setup.
//
// The scenarios run a direct (embedded) bd init with no Dolt remote. Not
// pinned here: a proxied-server bd init, which installs no Codex section and
// decides HasRemote from its own remote URL; a bd init with a remote, which
// renders the managed section's with-remote variant; and the Cursor files.
// AGENTS.md is shown in full apart from its managed section, so a change to
// its wrapper text or its Codex section can pass the cmd/bd/setup and
// internal/templates/agents tests and fail only here.
//
// bd runs with HOME outside the project, so an artifact written to the home
// directory instead of the project shows up under $HOME.
//
// Regenerate with
//
//	BEADS_TEST_EMBEDDED_DOLT=1 BD_UPDATE_GOLDEN=1 go test -tags gms_pure_go ./cmd/bd/ -run TestEmbeddedInitAgentArtifactsGolden
func TestEmbeddedInitAgentArtifactsGolden(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	scenarios := []struct {
		name     string
		initArgs []string
		steps    [][]string
	}{
		{
			name:     "init-default",
			initArgs: []string{"--prefix", "aa"},
			steps: [][]string{
				{"setup", "claude", "--check"},
				{"setup", "claude"},
				{"setup", "claude", "--remove"},
				{"setup", "claude", "--check"},
				{"setup", "claude"},
				{"setup", "claude", "--check"},
			},
		},
		{
			name:     "init-agents-profile-full",
			initArgs: []string{"--prefix", "ab", "--agents-profile", "full"},
			steps: [][]string{
				{"setup", "claude", "--check"},
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			// bdInit, with HOME in a directory of its own.
			dir, home := t.TempDir(), t.TempDir()
			initGitRepoAt(t, dir)
			database, seeded := seededInitDatabase(sc.initArgs)
			if seeded {
				seedEmbeddedSchema(t, dir, database)
			}
			initArgs := append([]string{"init", "--quiet"}, sc.initArgs...)
			if stdout, stderr, exitCode := runAgentArtifactCommand(t, bd, dir, home, initArgs...); exitCode != 0 {
				t.Fatalf("bd %s: exit %d\nstdout:\n%s\nstderr:\n%s", strings.Join(initArgs, " "), exitCode, stdout, stderr)
			}
			if seeded {
				requireSeededEmbeddedInit(t, filepath.Join(dir, ".beads"), database)
			}

			var transcript strings.Builder
			fmt.Fprintf(&transcript, "# bd init agent artifact transcript: %s\n", sc.name)
			fmt.Fprintf(&transcript, "# Regenerate with BEADS_TEST_EMBEDDED_DOLT=1 BD_UPDATE_GOLDEN=1 go test -tags gms_pure_go ./cmd/bd/ -run TestEmbeddedInitAgentArtifactsGolden\n")
			fmt.Fprintf(&transcript, "\n=== bd %s\n", strings.Join(initArgs, " "))
			previous := map[string]string{}
			writeAgentArtifactChanges(t, &transcript, dir, home, previous)

			for _, args := range sc.steps {
				stdout, stderr, exitCode := runAgentArtifactCommand(t, bd, dir, home, args...)
				fmt.Fprintf(&transcript, "\n=== bd %s\n", strings.Join(args, " "))
				fmt.Fprintf(&transcript, "exit: %d\n", exitCode)
				writeAgentArtifactBlock(&transcript, "stdout", normalizeAgentArtifactOutput(stdout, dir, home))
				writeAgentArtifactBlock(&transcript, "stderr", normalizeAgentArtifactOutput(stderr, dir, home))
				writeAgentArtifactChanges(t, &transcript, dir, home, previous)
			}

			assertAgentArtifactGolden(t, filepath.Join("testdata", "agent_artifacts", sc.name+".golden"), transcript.String())
		})
	}
}

// agentArtifactPaths are the Claude agent artifacts bd init and bd setup
// claude write in a project ($PROJECT), and those in the home directory
// ($HOME): the $HOME/.claude/ files a global install writes, and the
// $HOME/CLAUDE.md an older --global run from $HOME wrote.
var agentArtifactPaths = []string{
	"$PROJECT/.claude/settings.json",
	"$PROJECT/.claude/settings.local.json",
	"$PROJECT/AGENTS.md",
	"$PROJECT/CLAUDE.md",
	"$HOME/.claude/settings.json",
	"$HOME/.claude/CLAUDE.md",
	"$HOME/CLAUDE.md",
}

// agentArtifactManagedSections maps each managed section bd renders to the
// internal/templates/agents golden file that pins its bytes.
var agentArtifactManagedSections = []struct {
	golden  string
	profile agents.Profile
	opts    agents.RenderOpts
}{
	{golden: "managed-section-minimal.md", profile: agents.ProfileMinimal, opts: agents.RenderOpts{HasRemote: true}},
	{golden: "managed-section-minimal-no-remote.md", profile: agents.ProfileMinimal, opts: agents.RenderOpts{}},
	{golden: "managed-section-minimal-user.md", profile: agents.ProfileMinimal, opts: agents.UserLevelRenderOpts()},
	{golden: "managed-section-full.md", profile: agents.ProfileFull, opts: agents.RenderOpts{HasRemote: true}},
	{golden: "managed-section-full-no-remote.md", profile: agents.ProfileFull, opts: agents.RenderOpts{}},
	{golden: "managed-section-full-user.md", profile: agents.ProfileFull, opts: agents.UserLevelRenderOpts()},
}

// elideAgentArtifactManagedSection replaces the body of a managed section
// that is byte-identical to a rendered variant with a pointer to that
// variant's golden, keeping the BEGIN marker line (profile and hash) and the
// END marker.
func elideAgentArtifactManagedSection(content string) string {
	const endMarker = "<!-- END BEADS INTEGRATION -->\n"
	begin := strings.Index(content, "<!-- BEGIN BEADS INTEGRATION")
	if begin < 0 {
		return content
	}
	rel := strings.Index(content[begin:], endMarker)
	if rel < 0 {
		return content
	}
	end := begin + rel + len(endMarker)
	section := content[begin:end]
	for _, v := range agentArtifactManagedSections {
		if section == agents.RenderSectionWithOpts(v.profile, v.opts) {
			marker, _, _ := strings.Cut(section, "\n")
			return content[:begin] + marker + "\n[managed section body: byte-identical to internal/templates/agents/testdata/" + v.golden + "]\n" + endMarker + content[end:]
		}
	}
	return content
}

// runAgentArtifactCommand runs bd in dir with HOME at home and returns its
// stdout, stderr and exit code.
func runAgentArtifactCommand(t *testing.T, bd, dir, home string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = agentArtifactEnv(dir, home)
	outBuf, errBuf, err := runCommandBuffers(t, cmd)
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("bd %s: %v", strings.Join(args, " "), err)
		}
		exitCode = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// agentArtifactEnv is bdEnv(dir) with HOME at home and without the caller's
// BD_OTEL_* and OTEL_* variables: bd warns on stderr about a legacy BD_OTEL_*
// one whether or not telemetry is on, and the transcript pins stderr byte for
// byte. (clearTelemetryEnv cannot be used: it calls t.Setenv, and these tests
// run in parallel.)
func agentArtifactEnv(dir, home string) []string {
	var env []string
	for _, e := range bdEnv(dir) {
		if strings.HasPrefix(e, "BD_OTEL_") || strings.HasPrefix(e, "OTEL_") || strings.HasPrefix(e, "HOME=") {
			continue
		}
		env = append(env, e)
	}
	return append(env, "HOME="+home)
}

// writeAgentArtifactChanges appends every artifact created, modified or
// deleted since previous (which it then updates), with its contents.
func writeAgentArtifactChanges(t *testing.T, b *strings.Builder, dir, home string, previous map[string]string) {
	t.Helper()
	roots := strings.NewReplacer("$PROJECT", dir, "$HOME", home)
	changed := false
	for _, path := range agentArtifactPaths {
		data, err := os.ReadFile(filepath.FromSlash(roots.Replace(path)))
		before, existed := previous[path]
		switch {
		case os.IsNotExist(err):
			if existed {
				fmt.Fprintf(b, "--- files: deleted %s\n", path)
				delete(previous, path)
				changed = true
			}
		case err != nil:
			t.Fatalf("read %s: %v", path, err)
		case !existed || before != string(data):
			verb := "created"
			if existed {
				verb = "modified"
			}
			shown := string(data)
			if strings.HasSuffix(path, ".md") {
				shown = elideAgentArtifactManagedSection(shown)
			}
			writeAgentArtifactBlock(b, fmt.Sprintf("files: %s %s", verb, path), shown)
			previous[path] = string(data)
			changed = true
		}
	}
	if !changed {
		b.WriteString("--- files: unchanged\n")
	}
}

// writeAgentArtifactBlock appends a titled block, marking empty content and a
// missing trailing newline so neither is lost in the golden.
func writeAgentArtifactBlock(b *strings.Builder, title, content string) {
	fmt.Fprintf(b, "--- %s\n", title)
	switch {
	case content == "":
		b.WriteString("(empty)\n")
	case !strings.HasSuffix(content, "\n"):
		b.WriteString(content)
		b.WriteString("\n(no newline at end of file)\n")
	default:
		b.WriteString(content)
	}
}

// normalizeAgentArtifactOutput replaces the project and home directories (as
// given and with symlinks resolved, since bd reports the paths it sees) with
// $PROJECT and $HOME and uses forward slashes, so the transcript is the same
// on every host.
func normalizeAgentArtifactOutput(s, dir, home string) string {
	for _, root := range []struct{ path, label string }{{dir, "$PROJECT"}, {home, "$HOME"}} {
		if resolved, err := filepath.EvalSymlinks(root.path); err == nil && resolved != root.path {
			s = strings.ReplaceAll(s, resolved, root.label)
		}
		s = strings.ReplaceAll(s, root.path, root.label)
	}
	if filepath.Separator != '/' {
		s = strings.ReplaceAll(s, string(filepath.Separator), "/")
	}
	return s
}

// assertAgentArtifactGolden compares got with the golden file at path, or
// rewrites the file when BD_UPDATE_GOLDEN=1.
func assertAgentArtifactGolden(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("BD_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		t.Logf("updated golden %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (regenerate with BD_UPDATE_GOLDEN=1): %v", path, err)
	}
	if got == string(want) {
		return
	}
	wantLines, gotLines := strings.Split(string(want), "\n"), strings.Split(got, "\n")
	line := func(lines []string, i int) string {
		if i >= len(lines) {
			return "(end of output)"
		}
		return fmt.Sprintf("%q", lines[i])
	}
	i := 0
	for i < len(wantLines) && i < len(gotLines) && wantLines[i] == gotLines[i] {
		i++
	}
	t.Errorf("bd init agent artifacts drifted from %s (regenerate with BD_UPDATE_GOLDEN=1 if intended)\nfirst difference at line %d:\n  want: %s\n  got:  %s\n--- got ---\n%s", path, i+1, line(wantLines, i), line(gotLines, i), got)
}
