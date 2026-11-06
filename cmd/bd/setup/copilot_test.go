package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/recipes"
	"github.com/steveyegge/beads/internal/templates/agents"
)

const testCopilotManifest = "{\"name\": \"beads\"}\n"

func newCopilotTestEnv(t *testing.T) (copilotEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return copilotEnv{
		stdout:           stdout,
		stderr:           stderr,
		manifestPath:     filepath.Join(root, filepath.FromSlash(CopilotManifestPath)),
		instructionsPath: filepath.Join(root, filepath.FromSlash(CopilotInstructionsPath)),
		manifest:         testCopilotManifest,
		renderOpts:       agents.DefaultRenderOpts,
	}, stdout, stderr
}

func writeCopilotInstructions(t *testing.T, env copilotEnv, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.instructionsPath, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func readCopilotFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

var copilotMinimalSection = agents.RenderSectionWithOpts(agents.ProfileMinimal, agents.DefaultRenderOpts())

// Install keeps every byte of an unmarked instructions file as the prefix of
// the new one, adding only the line break and blank line that set the section
// off, whatever the file's line endings, and keeps an older bd's static
// template, which it cannot tell from the user's own text.
func TestInstallCopilotKeepsUnmarkedText(t *testing.T) {
	for _, tt := range []struct {
		name, content, sep string
	}{
		{name: "final newline", content: "# Rules\n\nBe kind.\n", sep: "\n"},
		{name: "no final newline", content: "# Rules\n\nBe kind.", sep: "\n\n"},
		{name: "extra final blank lines", content: "# Rules\n\n\n", sep: "\n"},
		{name: "CRLF", content: "# Rules\r\n\r\nBe kind.\r\n", sep: "\n"},
		{name: "legacy static template", content: recipes.CopilotInstructionsTemplate, sep: "\n"},
		{name: "legacy template with user notes", content: recipes.CopilotInstructionsTemplate + "\n## Team additions\n\nOur own rule.\n", sep: "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.content, 0o644)
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}
			want := tt.content + tt.sep + copilotMinimalSection
			if got := readCopilotFile(t, env.instructionsPath); got != want {
				t.Errorf("instructions are\n%q\nwant\n%q", got, want)
			}
			if got := readCopilotFile(t, env.manifestPath); got != testCopilotManifest {
				t.Errorf("manifest is %q, want %q", got, testCopilotManifest)
			}
		})
	}
}

// A full-profile section, as bd setup claude or codex write, is kept rather
// than traded for the minimal one.
func TestInstallCopilotKeepsFullProfile(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)
	full := "# Rules\n\n" + agents.RenderSectionWithOpts(agents.ProfileFull, agents.DefaultRenderOpts())
	writeCopilotInstructions(t, env, full, 0o644)
	if err := installCopilot(env); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}
	if got := readCopilotFile(t, env.instructionsPath); got != full {
		t.Errorf("install changed a current full-profile section:\n%s", got)
	}
}

// An update keeps a narrowed file mode.
func TestInstallCopilotKeepsFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not POSIX on Windows")
	}
	env, _, _ := newCopilotTestEnv(t)
	writeCopilotInstructions(t, env, "# Private\n", 0o600)
	if err := os.Chmod(env.instructionsPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installCopilot(env); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}
	info, err := os.Stat(env.instructionsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode is %v, want 0600", info.Mode().Perm())
	}
}

// Install never writes through a symlink at the instructions path, dangling
// or not, and does not claim the instructions installed.
func TestInstallCopilotSkipsSymlinkedInstructions(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		name := "link to a file"
		if dangling {
			name = "dangling link"
		}
		t.Run(name, func(t *testing.T) {
			env, stdout, stderr := newCopilotTestEnv(t)
			target := filepath.Join(filepath.Dir(filepath.Dir(env.instructionsPath)), "shared.md")
			if !dangling {
				if err := os.WriteFile(target, []byte("# Shared\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, env.instructionsPath); err != nil {
				t.Skipf("symlink: %v", err)
			}
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}
			if got, err := os.Readlink(env.instructionsPath); err != nil || got != target {
				t.Errorf("instructions link is %q, %v; want it kept, to %q", got, err, target)
			}
			data, err := os.ReadFile(target)
			switch {
			case dangling && !os.IsNotExist(err):
				t.Errorf("install created the dangling link's target: %q, %v", data, err)
			case !dangling && string(data) != "# Shared\n":
				t.Errorf("install changed the link's target: %q, %v", data, err)
			}
			if !strings.Contains(stderr.String(), "is a symlink") {
				t.Errorf("install did not warn of the symlink; stderr:\n%s", stderr)
			}
			if out := stdout.String(); strings.Contains(out, "integration installed") || !strings.Contains(out, "Instructions skipped") {
				t.Errorf("install output should say the instructions were skipped:\n%s", out)
			}
		})
	}
}

// copilotAcceptedText is user text that ends outside every Markdown block, so
// a section after it is bd's, though a fence count line by line would find
// a code block open: fences end only on a fence of their own character at
// least as long, a line that opens with three backticks and holds another is
// a code span, a fence-like line indented four spaces is code, and the end
// of a list item closes the fence it opened.
var copilotAcceptedText = map[string]string{
	"nested fences":                   "# Rules\n\n````markdown\n```sh\nmake\n```\n````\n\n~~~~\n~~~\n~~~~\n",
	"code span opening a line":        "# Rules\n\n```bd ready``` lists ready work.\n",
	"closer with text after it":       "# Rules\n\n```\n``` is how a fence starts\n```\n",
	"indented code holding a fence":   "# Rules\n\n    ```\n",
	"list item fence the list closes": "# Rules\n\n- Build with:\n  ```sh\n  make\n",
	"HTML comment":                    "# Rules\n\n<!--\nKEEP-ME-1 note\n-->\n",
}

// The markers bd writes pass, wherever its section stands in the file: each
// profile in each rendering, the legacy marker, CRLF line endings, no final
// line break, trailing spaces after the BEGIN marker, and the user's own
// fenced code, HTML and Markdown that ends outside every block ahead of the
// section.
func TestValidateCopilotSectionAcceptsSectionsBdWrites(t *testing.T) {
	sections := []string{agentsBeginMarker + "\nlegacy body\n" + agentsEndMarker + "\n"}
	for _, profile := range []agents.Profile{agents.ProfileMinimal, agents.ProfileFull} {
		for _, opts := range []agents.RenderOpts{agents.DefaultRenderOpts(), {}, {HasRemote: true, NoPush: true}, agents.UserLevelRenderOpts()} {
			sections = append(sections, agents.RenderSectionWithOpts(profile, opts))
		}
	}
	for i, section := range sections {
		contents := map[string]string{
			"alone":                         section,
			"after text":                    "# Rules\n\nBe kind.\n\n" + section,
			"between text":                  "# Rules\n\n" + section + "\n## After\n",
			"right after a paragraph":       "# Rules\nBe kind.\n" + section + "Be brief.\n",
			"CRLF":                          strings.ReplaceAll("# Rules\n\n"+section, "\n", "\r\n"),
			"no final newline":              "# Rules\n\n" + strings.TrimSuffix(section, "\n"),
			"BEGIN line with trailing tabs": strings.Replace(section, "-->\n", "--> \t\n", 1),
			"after fenced code":             "# Rules\n\n```sh\nmake\n```\n\n~~~\n```\n~~~\n\n" + section,
		}
		for name, text := range copilotAcceptedText {
			contents["after "+name] = text + "\n" + section
		}
		for name, content := range contents {
			if err := validateCopilotSection(content); err != nil {
				t.Errorf("section %d, %s: %v", i, name, err)
			}
		}
	}
}

// Markers other than the one pair bd writes are refused, saying what is
// wrong with them.
func TestValidateCopilotSectionRefusesOtherMarkers(t *testing.T) {
	section := agentsBeginMarker + "\nbody\n" + agentsEndMarker + "\n"
	for _, tt := range []struct{ name, content, problem string }{
		{"END without BEGIN", "# Rules\n\n" + agentsEndMarker + "\n", "END marker without a BEGIN marker"},
		{"BEGIN without END", "# Rules\n\n" + agentsBeginMarker + "\nbody\n", "BEGIN marker without an END marker"},
		{"END before BEGIN", agentsEndMarker + "\n" + agentsBeginMarker + "\n", "END marker before the BEGIN marker"},
		{"BEGIN mentioned before the section", "See " + agentsBeginMarker + ".\n\n" + section, "2 BEGIN markers, not one"},
		{"BEGIN mentioned in the section", agentsBeginMarker + "\nSee " + agentsBeginMarker + ".\n" + agentsEndMarker + "\n", "2 BEGIN markers, not one"},
		{"END mentioned after the section", section + "\nSee " + agentsEndMarker + ".\n", "2 END markers, not one"},
		{"inline BEGIN and END", "Between " + agentsBeginMarker + " and " + agentsEndMarker + ".\n", "BEGIN marker not on a line of its own"},
		{"BEGIN line with more text", agentsBeginMarker + " notes\nbody\n" + agentsEndMarker + "\n", "BEGIN marker not on a line of its own"},
		{"BEGIN line with text and a comment", agentsBeginMarker + " KEEP-ME <!-- note -->\nbody\n" + agentsEndMarker + "\n", "BEGIN marker not on a line of its own"},
		{"BEGIN line with a comment", agentsBeginMarker + "<!-- note -->\nbody\n" + agentsEndMarker + "\n", "BEGIN marker not on a line of its own"},
		{"BEGIN comment over two lines", "<!-- BEGIN BEADS INTEGRATION\nKEEP-ME -->\nbody\n" + agentsEndMarker + "\n", "BEGIN marker not on a line of its own"},
		{"indented BEGIN", "  " + section, "BEGIN marker not on a line of its own"},
		{"END line with more text", agentsBeginMarker + "\nbody\n" + agentsEndMarker + " notes\n", "END marker not on a line of its own"},
		{"fenced example", "```markdown\n" + section + "```\n", "BEGIN marker inside the fenced code block that starts at line 1"},
		{"tilde-fenced example", "~~~\n" + section + "~~~\n", "BEGIN marker inside the fenced code block that starts at line 1"},
		{"END in a fence", agentsBeginMarker + "\nbody\n```\n" + agentsEndMarker + "\n```\n", "END marker inside the fenced code block that starts at line 3"},
		{"example in a longer fence", "# Rules\n\n````markdown\n```text\n" + section + "```\n````\n", "BEGIN marker inside the fenced code block that starts at line 3"},
		{"example in a longer tilde fence", "~~~~\n~~~\n" + section + "~~~\n~~~~\n", "BEGIN marker inside the fenced code block that starts at line 1"},
		{"fence line with text after it", "```\n``` not a closing fence\n" + section + "```\n", "BEGIN marker inside the fenced code block that starts at line 1"},
		{"fence line indented four spaces", "```\n    ```\n" + section + "```\n", "BEGIN marker inside the fenced code block that starts at line 1"},
		{"fence after indented code", "    ```\n```\n" + section + "```\n", "BEGIN marker inside the fenced code block that starts at line 2"},
		{"fence after a list item's fence", "- item\n  ```\n```\n" + section + "```\n", "BEGIN marker inside the fenced code block that starts at line 3"},
		{"unclosed fence", "# Rules\n\n```\n" + section, "BEGIN marker inside the fenced code block that starts at line 3"},
		{"commented-out example", "<!--\n" + section + "-->\n", "BEGIN marker inside the HTML comment that starts at line 1"},
		{"example in a pre block", "<pre>\n" + section + "</pre>\n", "BEGIN marker inside the HTML block that starts at line 1"},
		{"END in an HTML block", agentsBeginMarker + "\n<div>\n" + agentsEndMarker + "\n</div>\n", "END marker inside the HTML block that starts at line 2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCopilotSection(tt.content)
			if !errors.Is(err, errCopilotMalformedSection) || !strings.Contains(err.Error(), tt.problem) {
				t.Errorf("validateCopilotSection = %v, want %v naming %q", err, errCopilotMalformedSection, tt.problem)
			}
		})
	}
}

// copilotAmbiguousMarkers are instructions whose first BEGIN marker comes
// before their first END marker, where the section helpers would find a
// section, though the markers are not the one pair bd writes: the text also
// mentions one, holds them inline or in an example, or puts other text on
// the BEGIN marker's line. Install, --check and --remove refuse each, and
// change nothing.
var copilotAmbiguousMarkers = map[string]string{
	"stray BEGIN before a section":       "# My rules\n\nNote: bd manages the block that starts with " + agentsBeginMarker + " below.\n\nKEEP-ME-1 user paragraph.\n\n" + copilotMinimalSection,
	"END mention after a section":        copilotMinimalSection + "\nThe section ends at " + agentsEndMarker + ".\n\nKEEP-ME-1 user paragraph.\n",
	"inline BEGIN and END":               "# Rules\n\nThe block between " + agentsBeginMarker + " and " + agentsEndMarker + " is bd's.\n\nKEEP-ME-1 user paragraph.\n",
	"BEGIN line with text and a comment": "# Rules\n\n" + agentsBeginMarker + " KEEP-ME inline user text <!-- note -->\nbody\n" + agentsEndMarker + "\n",
	"fenced example":                     "# Rules\n\n```markdown\n" + agentsBeginMarker + "\nEXAMPLE-BODY\n" + agentsEndMarker + "\n```\n\nKEEP-ME-1 user paragraph.\n",
	"fenced example before a section":    "# Rules\n\n```markdown\n" + agentsBeginMarker + "\nEXAMPLE-BODY\n" + agentsEndMarker + "\n```\n\nKEEP-ME-1 user paragraph.\n\n" + copilotMinimalSection,
	"example in a longer fence":          "# Rules\n\n````markdown\n```text\n" + agentsBeginMarker + "\nKEEP-ME example instructions\n" + agentsEndMarker + "\n```\n````\n",
	"example in a longer tilde fence":    "# Rules\n\n~~~~markdown\n~~~text\n" + agentsBeginMarker + "\nKEEP-ME example instructions\n" + agentsEndMarker + "\n~~~\n~~~~\n",
	"commented-out example":              "# Rules\n\n<!--\n" + agentsBeginMarker + "\nKEEP-ME example instructions\n" + agentsEndMarker + "\n-->\n",
}

// Install refuses a file whose beads markers do not bound a section, and
// writes nothing.
func TestInstallCopilotRefusesMalformedMarkers(t *testing.T) {
	cases := map[string]string{
		"BEGIN without END":  "# Rules\n\n" + agentsBeginMarker + "\nbody\n",
		"END without BEGIN":  "# Rules\n\n" + agentsEndMarker + "\n",
		"END before BEGIN":   agentsEndMarker + "\n" + agentsBeginMarker + "\n",
		"stray inline BEGIN": "# My rules\n\nSee " + agentsBeginMarker + " for the old note.\n\nKEEP-ME-1 user paragraph.\n",
	}
	for name, content := range copilotAmbiguousMarkers {
		cases[name] = content
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			env, _, stderr := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, content, 0o644)
			if err := installCopilot(env); err == nil {
				t.Fatalf("installCopilot succeeded; want a refusal")
			}
			if got := readCopilotFile(t, env.instructionsPath); got != content {
				t.Errorf("install changed the instructions to\n%q", got)
			}
			if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
				t.Errorf("install wrote the manifest, stat err = %v", err)
			}
			if !strings.Contains(stderr.String(), "malformed beads section markers") {
				t.Errorf("stderr does not name the malformed markers:\n%s", stderr)
			}
		})
	}
}

// Install refuses a file without beads markers that ends inside a block a
// blank line does not close, where the section it appends would land, and
// writes nothing: --check and --remove would take that section's markers
// for an example and refuse them.
func TestInstallCopilotRefusesFileEndingInOpenBlock(t *testing.T) {
	for _, tt := range []struct{ name, content, block string }{
		{"unclosed fence", "# Rules\n\n```sh\nmake\n", "the fenced code block that starts at line 3"},
		{"unclosed tilde fence", "# Rules\n\n~~~\nmake\n", "the fenced code block that starts at line 3"},
		{"shorter fence in a longer one", "# Rules\n\n````markdown\n```sh\nmake\n```\n", "the fenced code block that starts at line 3"},
		{"no final newline", "# Rules\n\n```", "the fenced code block that starts at line 3"},
		{"unclosed HTML comment", "# Rules\n\n<!-- TODO\n", "the HTML comment that starts at line 3"},
		{"unclosed pre block", "# Rules\n\n<pre>\nmake\n", "the HTML block that starts at line 3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, _, stderr := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.content, 0o644)
			if err := installCopilot(env); !errors.Is(err, errCopilotOpenBlock) {
				t.Fatalf("installCopilot = %v, want %v", err, errCopilotOpenBlock)
			}
			if got := readCopilotFile(t, env.instructionsPath); got != tt.content {
				t.Errorf("install changed the instructions to\n%q", got)
			}
			if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
				t.Errorf("install wrote the manifest, stat err = %v", err)
			}
			if want := env.instructionsPath + " ends inside " + tt.block; !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr does not say %q:\n%s", want, stderr)
			}
		})
	}
}

// Install appends the section to text that ends outside every Markdown
// block, however a line-by-line fence count would read it, and --check,
// install and --remove then take the section for bd's: install never writes
// a section they refuse. --remove gives the text back.
func TestInstallCopilotAppendsAfterClosedBlocks(t *testing.T) {
	for name, content := range copilotAcceptedText {
		t.Run(name, func(t *testing.T) {
			env, stdout, _ := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, content, 0o644)
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}
			if got, want := readCopilotFile(t, env.instructionsPath), content+"\n"+copilotMinimalSection; got != want {
				t.Fatalf("instructions are\n%q\nwant\n%q", got, want)
			}
			stdout.Reset()
			if err := checkCopilot(env); err != nil {
				t.Errorf("checkCopilot after install = %v:\n%s", err, stdout)
			}
			if err := installCopilot(env); err != nil {
				t.Errorf("installCopilot over its own section = %v", err)
			}
			if err := removeCopilot(env); err != nil {
				t.Fatalf("removeCopilot: %v", err)
			}
			if got := readCopilotFile(t, env.instructionsPath); got != content {
				t.Errorf("--remove left\n%q\nwant\n%q", got, content)
			}
		})
	}
}

// Install refuses to write the plugin manifest through a symlink, which a
// cloned repository could point at any file the user can write, and then
// leaves the link, its target and the instructions as they were.
func TestInstallCopilotRefusesSymlinkedManifest(t *testing.T) {
	env, _, stderr := newCopilotTestEnv(t)
	const targetContent = "not bd's\n"
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte(targetContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(env.manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, env.manifestPath); err != nil {
		t.Skipf("symlink: %v", err)
	}
	writeCopilotInstructions(t, env, "# Rules\n", 0o644)
	if err := installCopilot(env); err == nil {
		t.Fatalf("installCopilot wrote the manifest through a symlink")
	}
	if got := readCopilotFile(t, target); got != targetContent {
		t.Errorf("install changed the manifest link's target to %q", got)
	}
	if got, err := os.Readlink(env.manifestPath); err != nil || got != target {
		t.Errorf("manifest link is %q, %v; want it kept, to %q", got, err, target)
	}
	if got := readCopilotFile(t, env.instructionsPath); got != "# Rules\n" {
		t.Errorf("install changed the instructions to %q", got)
	}
	if !strings.Contains(stderr.String(), env.manifestPath) || !strings.Contains(stderr.String(), "symlink") {
		t.Errorf("stderr does not name the symlinked manifest:\n%s", stderr)
	}
}

// --check names each state of the integration, and passes only for the
// manifest beside a current beads section.
func TestCheckCopilotStates(t *testing.T) {
	stale := "# Rules\n\n" + agentsBeginMarker + "\nold\n" + agentsEndMarker + "\n"
	for _, tt := range []struct {
		name         string
		manifest     bool
		instructions *string
		want         string
		pass         bool
	}{
		{name: "nothing installed", want: filepath.FromSlash(CopilotManifestPath) + "\n"},
		{name: "missing manifest", instructions: &copilotMinimalSection, want: filepath.FromSlash(CopilotManifestPath) + "\n"},
		{name: "missing instructions", manifest: true, want: filepath.FromSlash(CopilotInstructionsPath) + "\n"},
		{name: "unmarked instructions", manifest: true, instructions: copilotText("# Rules\n"), want: "exists but no beads section found"},
		{name: "stale section", manifest: true, instructions: &stale, want: "installed but stale"},
		{name: "current section", manifest: true, instructions: &copilotMinimalSection, want: "(current)", pass: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, stdout, _ := newCopilotTestEnv(t)
			if tt.manifest {
				if err := os.MkdirAll(filepath.Dir(env.manifestPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(env.manifestPath, []byte(testCopilotManifest), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.instructions != nil {
				writeCopilotInstructions(t, env, *tt.instructions, 0o644)
			}
			err := checkCopilot(env)
			if (err == nil) != tt.pass {
				t.Errorf("checkCopilot = %v, want pass %v", err, tt.pass)
			}
			out := stdout.String()
			if !strings.Contains(out, tt.want) {
				t.Errorf("--check output does not say %q:\n%s", tt.want, out)
			}
			if !tt.manifest && !strings.Contains(out, "integration not installed") {
				t.Errorf("--check output does not say the integration is not installed:\n%s", out)
			}
			if tt.pass && !strings.Contains(out, env.manifestPath) {
				t.Errorf("--check output does not name %s:\n%s", env.manifestPath, out)
			}
			if !tt.manifest || tt.pass {
				return
			}
			// Install makes the integration current.
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}
			stdout.Reset()
			if err := checkCopilot(env); err != nil {
				t.Errorf("checkCopilot after install = %v:\n%s", err, stdout)
			}
		})
	}
}

// --check fails, naming the file, for markers that are not the one pair bd
// writes around its section, and changes nothing.
func TestCheckCopilotRefusesMalformedMarkers(t *testing.T) {
	cases := map[string]string{
		"BEGIN without END": "# Rules\n\n" + agentsBeginMarker + "\nbody\n",
		"END without BEGIN": "# Rules\n\n" + agentsEndMarker + "\n",
		"END before BEGIN":  agentsEndMarker + "\n" + agentsBeginMarker + "\nbody\n" + agentsEndMarker + "\n",
	}
	for name, content := range copilotAmbiguousMarkers {
		cases[name] = content
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			env, stdout, stderr := newCopilotTestEnv(t)
			writeCopilotManifest(t, env)
			writeCopilotInstructions(t, env, content, 0o644)
			if err := checkCopilot(env); err == nil {
				t.Errorf("checkCopilot passed malformed markers")
			}
			if !strings.Contains(stderr.String(), env.instructionsPath) || !strings.Contains(stderr.String(), "malformed beads section markers") {
				t.Errorf("--check does not name the file and its malformed markers:\n%s", stderr)
			}
			if strings.Contains(stdout.String(), "(current)") {
				t.Errorf("--check reported malformed markers current:\n%s", stdout)
			}
			if got := readCopilotFile(t, env.instructionsPath); got != content {
				t.Errorf("--check changed the instructions to\n%q", got)
			}
		})
	}
}

// --check judges a symlinked instructions file, which install and --remove
// leave alone, by the beads section in its target, which Copilot reads
// through the link. For a target without a current section it names the
// link, the target and both remedies. It names a relative link's target as
// the link resolves it, not as the link spells it, which from where bd runs
// would be a file outside the repository. It writes through the link to
// nothing.
func TestCheckCopilotJudgesSymlinkByTarget(t *testing.T) {
	full := "# Shared\n\n" + agents.RenderSectionWithOpts(agents.ProfileFull, agents.DefaultRenderOpts())
	stale := "# Shared\n\n" + agentsBeginMarker + "\nold\n" + agentsEndMarker + "\n"
	type linkCase struct {
		name     string
		target   *string // the target's content; nil leaves the link dangling
		want     string
		pass     bool
		relative bool
	}
	var cases []linkCase
	for _, tt := range []linkCase{
		{name: "current minimal section", target: &copilotMinimalSection, want: "(current)", pass: true},
		{name: "current full section", target: &full, want: "(current)", pass: true},
		{name: "no section", target: copilotText("# Shared\n"), want: "which has no beads section"},
		{name: "stale section", target: &stale, want: "whose beads section is stale"},
		{name: "dangling link", want: "which does not exist"},
	} {
		cases = append(cases, tt)
		tt.name, tt.relative = tt.name+", relative link", true
		cases = append(cases, tt)
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			env, stdout, _ := newCopilotTestEnv(t)
			writeCopilotManifest(t, env)
			if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(filepath.Dir(filepath.Dir(env.instructionsPath)), "AGENTS.md")
			if tt.target != nil {
				if err := os.WriteFile(target, []byte(*tt.target), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			link := target
			if tt.relative {
				link = filepath.Join("..", "AGENTS.md")
			}
			if err := os.Symlink(link, env.instructionsPath); err != nil {
				t.Skipf("symlink: %v", err)
			}
			err := checkCopilot(env)
			if (err == nil) != tt.pass {
				t.Errorf("checkCopilot = %v, want pass %v", err, tt.pass)
			}
			out := stdout.String()
			if !strings.Contains(out, tt.want) || !strings.Contains(out, target) {
				t.Errorf("--check output does not say %q of %s:\n%s", tt.want, target, out)
			}
			if tt.relative && strings.Contains(out, link) {
				t.Errorf("--check output names the target as the link spells it, %s:\n%s", link, out)
			}
			if tt.pass && !strings.Contains(out, env.manifestPath) {
				t.Errorf("--check output does not name %s:\n%s", env.manifestPath, out)
			}
			if !tt.pass && (!strings.Contains(out, env.instructionsPath+" is a symlink") || !strings.Contains(out, "replace the link with a regular file and run: bd setup copilot")) {
				t.Errorf("--check output does not name the symlink and the remedies:\n%s", out)
			}
			if !tt.pass && (strings.Contains(out, "(current)") || strings.Contains(out, "Missing:")) {
				t.Errorf("--check misreports the symlink:\n%s", out)
			}
			if got, err := os.Readlink(env.instructionsPath); err != nil || got != link {
				t.Errorf("instructions link is %q, %v; want it kept, to %q", got, err, link)
			}
			data, err := os.ReadFile(target)
			switch {
			case tt.target == nil && !os.IsNotExist(err):
				t.Errorf("--check created the dangling link's target: %q, %v", data, err)
			case tt.target != nil && string(data) != *tt.target:
				t.Errorf("--check changed the link's target to %q, %v", data, err)
			}
		})
	}
}

// --check fails, naming the manifest path and the error, when the manifest
// cannot be inspected for a reason other than its absence, rather than
// reporting the integration installed or merely missing.
func TestCheckCopilotReportsManifestStatError(t *testing.T) {
	env, stdout, stderr := newCopilotTestEnv(t)
	// A regular file where the manifest's directory belongs makes stat of the
	// manifest fail with ENOTDIR, not ENOENT.
	if err := os.WriteFile(filepath.Dir(env.manifestPath), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.manifestPath); err == nil || os.IsNotExist(err) {
		t.Skipf("stat through a file is %v here, not a non-ENOENT error", err)
	}
	writeCopilotInstructions(t, env, copilotMinimalSection, 0o644)
	if err := checkCopilot(env); err == nil {
		t.Fatalf("checkCopilot passed a manifest it could not inspect:\n%s", stdout)
	}
	if !strings.Contains(stderr.String(), "failed to inspect "+env.manifestPath) {
		t.Errorf("--check does not name the manifest it could not inspect:\n%s", stderr)
	}
	if out := stdout.String(); strings.Contains(out, "(current)") || strings.Contains(out, "Missing:") {
		t.Errorf("--check misreports an uninspectable manifest:\n%s", out)
	}
}

// writeCopilotManifest installs the test manifest so a check exercises the
// instructions half of the recipe.
func writeCopilotManifest(t *testing.T, env copilotEnv) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(env.manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.manifestPath, []byte(testCopilotManifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func copilotText(s string) *string { return &s }

// --remove takes the section out of a file install appended it to, giving
// back the user's text up to removeBeadsSection's normalization of the line
// breaks next to the section, and keeps unmarked text such as an older bd's
// static template.
func TestRemoveCopilotKeepsUserText(t *testing.T) {
	for _, tt := range []struct{ name, content, want string }{
		{name: "final newline", content: "# Rules\n\nBe kind.\n", want: "# Rules\n\nBe kind.\n"},
		{name: "no final newline", content: "# Rules\n\nBe kind.", want: "# Rules\n\nBe kind.\n"},
		{name: "extra final blank lines", content: "# Rules\n\n\n", want: "# Rules\n"},
		{name: "CRLF", content: "# Rules\r\n\r\nBe kind.\r\n", want: "# Rules\r\n\r\nBe kind.\r\n"},
		{name: "legacy static template", content: recipes.CopilotInstructionsTemplate, want: recipes.CopilotInstructionsTemplate},
		{name: "legacy template with user notes", content: recipes.CopilotInstructionsTemplate + "\n## Team additions\n\nOur own rule.\n", want: recipes.CopilotInstructionsTemplate + "\n## Team additions\n\nOur own rule.\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.content, 0o644)
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}
			if err := removeCopilot(env); err != nil {
				t.Fatalf("removeCopilot: %v", err)
			}
			if got := readCopilotFile(t, env.instructionsPath); got != tt.want {
				t.Errorf("instructions are %q, want %q", got, tt.want)
			}
			if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
				t.Errorf("--remove kept the manifest, stat err = %v", err)
			}
		})
	}
}

// --remove leaves a file it holds no section in, however empty, a symlink and
// its target, and a file whose markers are malformed, which it refuses
// before removing anything.
func TestRemoveCopilotLeavesFilesWithoutASection(t *testing.T) {
	type removeCase struct {
		name, content string
		fails         bool
	}
	cases := []removeCase{
		{name: "unmarked", content: "# Rules\n"},
		{name: "empty", content: ""},
		{name: "blank", content: "\n"},
		{name: "BEGIN without END", content: "# Rules\n\n" + agentsBeginMarker + "\nbody\n", fails: true},
		{name: "END before BEGIN", content: agentsEndMarker + "\n" + agentsBeginMarker + "\n", fails: true},
		{name: "stray inline BEGIN", content: "# My rules\n\nSee " + agentsBeginMarker + " for the old note.\n\nKEEP-ME-1 user paragraph.\n", fails: true},
	}
	for name, content := range copilotAmbiguousMarkers {
		cases = append(cases, removeCase{name: name, content: content, fails: true})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newCopilotTestEnv(t)
			writeCopilotInstructions(t, env, tt.content, 0o644)
			if err := os.MkdirAll(filepath.Dir(env.manifestPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(env.manifestPath, []byte(testCopilotManifest), 0o644); err != nil {
				t.Fatal(err)
			}
			err := removeCopilot(env)
			if (err != nil) != tt.fails {
				t.Errorf("removeCopilot = %v, want failure %v", err, tt.fails)
			}
			if got := readCopilotFile(t, env.instructionsPath); got != tt.content {
				t.Errorf("--remove changed the instructions to %q", got)
			}
			if _, err := os.Stat(env.manifestPath); (err == nil) != tt.fails {
				t.Errorf("manifest stat err = %v; want it kept only when --remove fails", err)
			}
		})
	}

	t.Run("symlink", func(t *testing.T) {
		env, _, _ := newCopilotTestEnv(t)
		if err := os.MkdirAll(filepath.Dir(env.instructionsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(filepath.Dir(env.instructionsPath), "real.md")
		if err := os.WriteFile(target, []byte(copilotMinimalSection), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, env.instructionsPath); err != nil {
			t.Skipf("symlink: %v", err)
		}
		writeCopilotManifest(t, env)
		if err := removeCopilot(env); err != nil {
			t.Fatalf("removeCopilot: %v", err)
		}
		if got, err := os.Readlink(env.instructionsPath); err != nil || got != target {
			t.Errorf("instructions link is %q, %v; want it kept, to %q", got, err, target)
		}
		if got := readCopilotFile(t, target); got != copilotMinimalSection {
			t.Errorf("--remove changed the link's target to %q", got)
		}
		if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
			t.Errorf("--remove kept the manifest beside a symlink, stat err = %v", err)
		}
	})

	t.Run("missing instructions", func(t *testing.T) {
		env, _, _ := newCopilotTestEnv(t)
		writeCopilotManifest(t, env)
		if err := removeCopilot(env); err != nil {
			t.Fatalf("removeCopilot: %v", err)
		}
		if _, err := os.Lstat(env.instructionsPath); !os.IsNotExist(err) {
			t.Errorf("--remove created %s, lstat err = %v", env.instructionsPath, err)
		}
		if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
			t.Errorf("--remove kept the manifest, stat err = %v", err)
		}
	})
}

// --remove with nothing installed says so and succeeds.
func TestRemoveCopilotNothingInstalled(t *testing.T) {
	env, stdout, _ := newCopilotTestEnv(t)
	if err := removeCopilot(env); err != nil {
		t.Fatalf("removeCopilot: %v", err)
	}
	if !strings.Contains(stdout.String(), "No integration files found") {
		t.Errorf("--remove with nothing installed does not say so:\n%s", stdout)
	}
}

// A file install created with the section alone goes with the section, and
// so does its directory when that leaves it empty, but not one holding
// workflows.
func TestRemoveCopilotDeletesSectionOnlyFile(t *testing.T) {
	for _, workflows := range []bool{false, true} {
		name := "empty .github"
		if workflows {
			name = ".github holding workflows"
		}
		t.Run(name, func(t *testing.T) {
			env, _, _ := newCopilotTestEnv(t)
			github := filepath.Dir(env.instructionsPath)
			if workflows {
				if err := os.MkdirAll(filepath.Join(github, "workflows"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := installCopilot(env); err != nil {
				t.Fatalf("installCopilot: %v", err)
			}
			if got := readCopilotFile(t, env.instructionsPath); got != copilotMinimalSection {
				t.Fatalf("install created %q, want the section alone", got)
			}
			if err := removeCopilot(env); err != nil {
				t.Fatalf("removeCopilot: %v", err)
			}
			if _, err := os.Lstat(env.instructionsPath); !os.IsNotExist(err) {
				t.Errorf("--remove kept the section-only file, lstat err = %v", err)
			}
			if _, err := os.Stat(github); (err == nil) != workflows {
				t.Errorf(".github stat err = %v; want it kept only when it holds workflows", err)
			}
			if _, err := os.Stat(env.manifestPath); !os.IsNotExist(err) {
				t.Errorf("--remove kept the manifest, stat err = %v", err)
			}
			if _, err := os.Stat(filepath.Dir(env.manifestPath)); !os.IsNotExist(err) {
				t.Errorf("--remove kept the emptied manifest directory, stat err = %v", err)
			}
		})
	}
}
