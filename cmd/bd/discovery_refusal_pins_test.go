//go:build cgo

package main

// Before-parity pins for workspace discovery refusals.
//
// These tests hold the exact text and exit code an ordinary bd prints when
// workspace discovery cannot select a usable workspace: a missing or corrupt
// metadata.json, an explicit BEADS_DIR that names no workspace, selection
// supplied by .beads/.env, .beads/redirect files, --db / BEADS_DB / BD_DB
// selection, and the blocked backend environment variables. They pin what
// origin/main does today, not what it ideally should do. A change that
// reorders admission, discovery or checkBlockedEnvVars in the root
// PersistentPreRunE has to change an expectation here, so it shows up in
// review as a test diff rather than as an unexplained behavior change.
//
// Every case runs the real bd binary in a fresh fixture and stops before any
// store is opened, so these need no Dolt server or database. Fixture
// workspaces with a corrupt metadata.json double as sentinels: binding the
// workspace warns, naming the metadata.json it failed to parse (the refusal
// that follows does not name it), so the text shows which directory
// discovery selected without opening a database.
//
// The pinned no-database hints are an embedded-capable bd's. A CGO_ENABLED=0
// bd is server-only and adds "run 'bd doctor' to diagnose" to them, so this
// file builds only with cgo, and the bd it runs (built by
// buildBDForInitTests, or BEADS_TEST_BD_BINARY) must be a cgo build too.

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// pinNoDatabaseRefusal is what a store-requiring command prints when
// discovery finds no workspace with a database.
const pinNoDatabaseRefusal = "Error: no beads database found\n" +
	"Hint: run 'bd where' to inspect the resolved workspace, or 'bd init' to create a new database\n" +
	"      or set BEADS_DIR to point to your .beads directory\n"

// pinNoWorkspaceWhereRefusal is bd where's text refusal when no workspace is
// selected.
const pinNoWorkspaceWhereRefusal = "Error: No active beads workspace found.\n" +
	"Hint: check BEADS_DIR/worktree setup, or run 'bd init' to create a new database\n"

// pinNoWorkspaceWhereJSON is bd where --json's refusal. It goes to stdout.
const pinNoWorkspaceWhereJSON = `{
  "error": "no_beads_directory",
  "hint": "check BEADS_DIR/worktree setup, or run 'bd init' to create a new database",
  "message": "No active beads workspace found.",
  "schema_version": 1
}
`

// pinCorruptMetadata is the content of every corrupt metadata.json fixture.
const pinCorruptMetadata = "{\n"

// pinCorruptMetadataWarning is the warning printed while the selected
// workspace is bound, before the refusal itself.
func pinCorruptMetadataWarning(beadsDir string) string {
	return "warning: load " + beadsDir + "/metadata.json: parsing config: unexpected end of JSON input; " +
		"no storage database was opened or modified (storage mode unknown; data commands refuse to fall back to the embedded store)\n"
}

// pinCorruptMetadataRefusal is the full stderr of a command refused because
// the selected workspace's metadata.json does not parse.
func pinCorruptMetadataRefusal(beadsDir string) string {
	return pinCorruptMetadataWarning(beadsDir) +
		"Error: loading config: parsing config: unexpected end of JSON input\n"
}

// pinBlockedEnvRefusal is checkBlockedEnvVars' refusal for name.
func pinBlockedEnvRefusal(name string) string {
	return "Error: " + name + " env var is not supported and has been removed to prevent data fragmentation.\n" +
		"Unset " + name + "; storage selection comes from .beads/metadata.json. " +
		"To choose a different supported backend, follow 'bd help init-safety'; do not edit metadata.json by hand\n"
}

// discoveryPinCase is one bd invocation and its exact expected result. The
// arguments and the expected strings write the fixture root as $ROOT.
type discoveryPinCase struct {
	name string
	// setup builds the fixture and returns the working directory and any
	// environment the invocation adds to the hermetic base.
	setup  func(f *discoveryPinFixture) (cwd string, env []string)
	args   []string
	exit   int
	stdout string
	stderr string
	// stderrPrefix compares only the start of stderr. Use it only where the
	// rest of the output is not part of the pin.
	stderrPrefix bool
}

func runDiscoveryPinCases(t *testing.T, cases []discoveryPinCase) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the pinned texts embed POSIX paths")
	}
	t.Parallel()
	bd := buildBDForInitTests(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newDiscoveryPinFixture(t)
			cwd, env := tc.setup(f)
			args := make([]string, len(tc.args))
			for i, arg := range tc.args {
				args[i] = strings.ReplaceAll(arg, "$ROOT", f.root)
			}
			exit, stdout, stderr := f.run(bd, cwd, env, args...)
			if exit != tc.exit {
				t.Errorf("bd %s: exit = %d, want %d", strings.Join(tc.args, " "), exit, tc.exit)
			}
			if stdout != tc.stdout {
				t.Errorf("bd %s: stdout differs\n got: %q\nwant: %q", strings.Join(tc.args, " "), stdout, tc.stdout)
			}
			gotStderr := stderr
			if tc.stderrPrefix && len(gotStderr) > len(tc.stderr) {
				gotStderr = gotStderr[:len(tc.stderr)]
			}
			if gotStderr != tc.stderr {
				t.Errorf("bd %s: stderr differs\n got: %q\nwant: %q", strings.Join(tc.args, " "), stderr, tc.stderr)
			}
		})
	}
}

type discoveryPinFixture struct {
	t     *testing.T
	root  string
	forms []string // spellings of root to rewrite as $ROOT, longest first
}

func newDiscoveryPinFixture(t *testing.T) *discoveryPinFixture {
	t.Helper()
	root := t.TempDir()
	forms := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		forms = append(forms, resolved)
	}
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	f := &discoveryPinFixture{t: t, root: root, forms: forms}
	f.mkdir("home")
	return f
}

// path returns the absolute path of a root-relative slash path.
func (f *discoveryPinFixture) path(rel string) string {
	return filepath.Join(f.root, filepath.FromSlash(rel))
}

func (f *discoveryPinFixture) mkdir(rel string) string {
	f.t.Helper()
	dir := f.path(rel)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

func (f *discoveryPinFixture) write(rel, content string) {
	f.t.Helper()
	if err := os.WriteFile(f.path(rel), []byte(content), 0o600); err != nil {
		f.t.Fatalf("write %s: %v", rel, err)
	}
}

// project creates rel as a git repository with no .beads. The repository
// root is the discovery walk's boundary, so nothing above the fixture leaks
// into a case.
func (f *discoveryPinFixture) project(rel string) string {
	f.t.Helper()
	dir := f.mkdir(rel)
	initGitRepoAt(f.t, dir)
	return dir
}

// workspace creates project rel with a .beads directory holding files, each
// named relative to .beads.
func (f *discoveryPinFixture) workspace(rel string, files map[string]string) string {
	f.t.Helper()
	dir := f.project(rel)
	f.mkdir(rel + "/.beads")
	for name, content := range files {
		f.write(rel+"/.beads/"+name, content)
	}
	return dir
}

// corruptWorkspace creates project rel whose metadata.json does not parse,
// plus any extra .beads files.
func (f *discoveryPinFixture) corruptWorkspace(rel string, extra map[string]string) string {
	f.t.Helper()
	files := map[string]string{"metadata.json": pinCorruptMetadata}
	for name, content := range extra {
		files[name] = content
	}
	return f.workspace(rel, files)
}

// yamlWorkspace creates project rel whose .beads holds only a config.yaml
// (no metadata.json and no database), plus any extra .beads files.
func (f *discoveryPinFixture) yamlWorkspace(rel string, extra map[string]string) string {
	f.t.Helper()
	files := map[string]string{"config.yaml": "# discovery pin fixture\n"}
	for name, content := range extra {
		files[name] = content
	}
	return f.workspace(rel, files)
}

// discoveryPinEnv is the caller environment without any BEADS_*/BD_*
// selector or setting and without git repository-location variables, with
// HOME and XDG_CONFIG_HOME under home, metrics and event flush off, and the
// migration freeze marker pointed at freezeMarker, a path that does not exist.
func discoveryPinEnv(home, freezeMarker string) []string {
	var env []string
	for _, kv := range testutil.WithoutGitRepoLocationEnv(os.Environ()) {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "BEADS_") || strings.HasPrefix(key, "BD_") ||
			key == "HOME" || key == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"BD_MIGRATION_FREEZE_FILE="+freezeMarker,
	)
}

func (f *discoveryPinFixture) run(bd, cwd string, extraEnv []string, args ...string) (exit int, stdout, stderr string) {
	f.t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = cwd
	cmd.Env = append(discoveryPinEnv(f.path("home"), f.path("no-such-freeze-marker")), extraEnv...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			f.t.Fatalf("run bd %s: %v", strings.Join(args, " "), err)
		}
		exit = exitErr.ExitCode()
	}
	return exit, f.normalize(out.String(), false), f.normalize(errOut.String(), true)
}

var discoveryPinVersionLine = regexp.MustCompile(`(?m)^bd version .*$`)

// discoveryPinDuplicateBinaries matches the warning bd version appends when
// the caller's PATH holds more than one distinct bd binary.
var discoveryPinDuplicateBinaries = regexp.MustCompile(
	`\nWarning: multiple 'bd' binaries found in PATH:\n(?:  [^\n]*\n)*` +
		`The first one is being used\. Remove duplicates to avoid confusion\.\n`)

// discoveryPinSandboxNotice matches the notice bd prints when it auto-detects
// a sandbox, which it does when it is not allowed to signal its own process.
var discoveryPinSandboxNotice = regexp.MustCompile(`(?m)^.*Sandbox detected, using direct mode\n`)

// normalize rewrites the fixture root as $ROOT and the build's version line
// as "bd version $VERSION". For stderr it also drops the notices that depend
// on the caller's environment rather than on the fixture (sandbox
// auto-detection, and bd version's duplicate binaries in PATH) and collapses
// consecutive repeats of a "Warning: redirect" line: discovery re-resolves
// the same redirect several times per invocation and how many times it warns
// is not part of the pin. Any other repeated line, such as a doubled
// "Error:", is kept.
func (f *discoveryPinFixture) normalize(s string, stderr bool) string {
	for _, form := range f.forms {
		s = strings.ReplaceAll(s, form, "$ROOT")
	}
	s = discoveryPinVersionLine.ReplaceAllLiteralString(s, "bd version $VERSION")
	if !stderr {
		return s
	}
	s = discoveryPinDuplicateBinaries.ReplaceAllLiteralString(s, "")
	s = discoveryPinSandboxNotice.ReplaceAllLiteralString(s, "")
	var b strings.Builder
	prev := ""
	for _, line := range strings.SplitAfter(s, "\n") {
		if line == prev && strings.HasPrefix(line, "Warning: redirect ") {
			continue
		}
		b.WriteString(line)
		prev = line
	}
	return b.String()
}

// TestDiscoveryRefusalPinsMissingMetadata pins discovery when no workspace,
// a workspace without metadata.json and without a database, or a .beads that
// is a regular file is found.
func TestDiscoveryRefusalPinsMissingMetadata(t *testing.T) {
	noBeads := func(f *discoveryPinFixture) (string, []string) { return f.project("p"), nil }
	regularFileBeads := func(f *discoveryPinFixture) (string, []string) {
		cwd := f.project("p")
		f.write("p/.beads", "not a workspace\n")
		return cwd, nil
	}
	runDiscoveryPinCases(t, []discoveryPinCase{
		{
			name:   "no-beads-dir/list",
			setup:  noBeads,
			args:   []string{"list"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
		{
			// --json does not turn the refusal into a JSON document.
			name:   "no-beads-dir/list-json",
			setup:  noBeads,
			args:   []string{"list", "--json"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
		{
			name:   "no-beads-dir/where",
			setup:  noBeads,
			args:   []string{"where"},
			exit:   1,
			stderr: pinNoWorkspaceWhereRefusal,
		},
		{
			name:   "no-beads-dir/where-json",
			setup:  noBeads,
			args:   []string{"where", "--json"},
			exit:   1,
			stdout: pinNoWorkspaceWhereJSON,
		},
		{
			name: "empty-beads-dir/list",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.workspace("p", nil), nil
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
		{
			name: "config-yaml-only/list",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.yamlWorkspace("p", nil), nil
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
		{
			// The same directory is a workspace to bd where.
			name: "config-yaml-only/where",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.yamlWorkspace("p", nil), nil
			},
			args:   []string{"where"},
			stdout: "$ROOT/p/.beads\n",
		},
		{
			// The working-directory walk selects a .beads that is a regular
			// file, then fails to read its metadata.json (the BEADS_DIR form
			// is in TestDiscoveryRefusalPinsExplicitBeadsDir).
			name:   "beads-is-regular-file/list",
			setup:  regularFileBeads,
			args:   []string{"list"},
			exit:   1,
			stderr: "Error: loading config: reading config: open $ROOT/p/.beads/metadata.json: not a directory\n",
		},
		{
			name:   "beads-is-regular-file/where",
			setup:  regularFileBeads,
			args:   []string{"where"},
			exit:   1,
			stderr: "Error: loading config: reading config: open $ROOT/p/.beads/metadata.json: not a directory\n",
		},
	})
}

// TestDiscoveryRefusalPinsCorruptMetadata pins the refusal for a discovered
// workspace whose metadata.json does not parse.
func TestDiscoveryRefusalPinsCorruptMetadata(t *testing.T) {
	corrupt := func(f *discoveryPinFixture) (string, []string) { return f.corruptWorkspace("p", nil), nil }
	runDiscoveryPinCases(t, []discoveryPinCase{
		{
			name:   "list",
			setup:  corrupt,
			args:   []string{"list"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/p/.beads"),
		},
		{
			name:   "list-json",
			setup:  corrupt,
			args:   []string{"list", "--json"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/p/.beads"),
		},
		{
			name:   "create",
			setup:  corrupt,
			args:   []string{"create", "must not be written", "--json"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/p/.beads"),
		},
		{
			// bd where never opens a store, but it still refuses.
			name:   "where",
			setup:  corrupt,
			args:   []string{"where"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/p/.beads"),
		},
		{
			name:   "where-json",
			setup:  corrupt,
			args:   []string{"where", "--json"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/p/.beads"),
		},
		{
			// bd version warns and succeeds.
			name:   "version",
			setup:  corrupt,
			args:   []string{"version"},
			stdout: "bd version $VERSION\n",
			stderr: pinCorruptMetadataWarning("$ROOT/p/.beads"),
		},
	})
}

// TestDiscoveryRefusalPinsExplicitBeadsDir pins an exported BEADS_DIR that
// names no workspace. Each case runs from a corrupt workspace, so falling
// back to discovery from the working directory would print that workspace's
// refusal instead.
func TestDiscoveryRefusalPinsExplicitBeadsDir(t *testing.T) {
	from := func(beadsDir func(f *discoveryPinFixture) string) func(f *discoveryPinFixture) (string, []string) {
		return func(f *discoveryPinFixture) (string, []string) {
			cwd := f.corruptWorkspace("cwd", nil)
			return cwd, []string{"BEADS_DIR=" + beadsDir(f)}
		}
	}
	missing := from(func(f *discoveryPinFixture) string { return f.path("missing/.beads") })
	runDiscoveryPinCases(t, []discoveryPinCase{
		{
			name:   "missing-dir/list",
			setup:  missing,
			args:   []string{"list"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
		{
			name:   "missing-dir/where",
			setup:  missing,
			args:   []string{"where"},
			exit:   1,
			stderr: pinNoWorkspaceWhereRefusal,
		},
		{
			name:   "missing-dir/where-json",
			setup:  missing,
			args:   []string{"where", "--json"},
			exit:   1,
			stdout: pinNoWorkspaceWhereJSON,
		},
		{
			name:   "empty-dir/list",
			setup:  from(func(f *discoveryPinFixture) string { return f.mkdir("empty") }),
			args:   []string{"list"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
		{
			name: "regular-file/list",
			setup: from(func(f *discoveryPinFixture) string {
				f.write("file", "not a workspace\n")
				return f.path("file")
			}),
			args:   []string{"list"},
			exit:   1,
			stderr: "Error: loading config: reading config: open $ROOT/file/metadata.json: not a directory\n",
		},
		{
			name: "corrupt-workspace/list",
			setup: from(func(f *discoveryPinFixture) string {
				f.corruptWorkspace("other", nil)
				return f.path("other/.beads")
			}),
			args:   []string{"list"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/other/.beads"),
		},
	})
}

// TestDiscoveryRefusalPinsBeadsEnvFile pins settings supplied by the
// discovered workspace's .beads/.env.
func TestDiscoveryRefusalPinsBeadsEnvFile(t *testing.T) {
	runDiscoveryPinCases(t, []discoveryPinCase{
		{
			// A .env BEADS_DIR routes discovery to its target.
			name: "beads-dir-routes-to-target",
			setup: func(f *discoveryPinFixture) (string, []string) {
				f.corruptWorkspace("target", nil)
				return f.yamlWorkspace("p", map[string]string{".env": "BEADS_DIR=" + f.path("target/.beads") + "\n"}), nil
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/target/.beads"),
		},
		{
			// The routing workspace's own corrupt metadata.json causes no refusal.
			name: "beads-dir-route-skips-local-metadata",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.corruptWorkspace("p", map[string]string{".env": "BEADS_DIR=" + f.path("missing/.beads") + "\n"}), nil
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
		{
			// An exported BEADS_DIR wins; the .env is never read.
			name: "exported-beads-dir-wins",
			setup: func(f *discoveryPinFixture) (string, []string) {
				f.corruptWorkspace("target", nil)
				cwd := f.yamlWorkspace("p", map[string]string{".env": "BEADS_DIR=" + f.path("target/.beads") + "\n"})
				return cwd, []string{"BEADS_DIR=" + f.path("missing/.beads")}
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
		{
			// A .env BEADS_DB does not route a data command away from the
			// workspace that holds the .env (the shadowing
			// TestDiscoveryRefusalPinsDBSelection records for an exported
			// BEADS_DB) ...
			name: "beads-db-list",
			setup: func(f *discoveryPinFixture) (string, []string) {
				f.corruptWorkspace("target", nil)
				return f.yamlWorkspace("p", map[string]string{".env": "BEADS_DB=" + f.path("target/.beads/dolt") + "\n"}), nil
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
		{
			// ... but it does route bd where.
			name: "beads-db-where",
			setup: func(f *discoveryPinFixture) (string, []string) {
				f.corruptWorkspace("target", nil)
				return f.yamlWorkspace("p", map[string]string{".env": "BEADS_DB=" + f.path("target/.beads/dolt") + "\n"}), nil
			},
			args:   []string{"where"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/target/.beads"),
		},
		{
			// Settings in the selected workspace's .env apply (BD_JSON here).
			name: "setting-applies-in-selected-workspace",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.yamlWorkspace("p", map[string]string{".env": "BD_JSON=true\n"}), nil
			},
			args:   []string{"where"},
			stdout: "{\n  \"path\": \"$ROOT/p/.beads\",\n  \"schema_version\": 1\n}\n",
		},
		{
			// A .env that routes elsewhere contributes only its selector:
			// its other settings are not imported, so the refusal is text.
			name: "setting-not-imported-from-routing-source",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.yamlWorkspace("p", map[string]string{".env": "BD_JSON=true\nBEADS_DIR=" + f.path("missing/.beads") + "\n"}), nil
			},
			args:   []string{"where"},
			exit:   1,
			stderr: pinNoWorkspaceWhereRefusal,
		},
		{
			// checkBlockedEnvVars sees only the exported environment, and runs
			// before any .env is loaded: a .env BD_BACKEND is not refused, here
			// or anywhere before discovery's own no-database refusal.
			name: "blocked-backend-not-checked",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.yamlWorkspace("p", map[string]string{".env": "BD_BACKEND=sqlite\n"}), nil
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
	})
}

// TestDiscoveryRefusalPinsRedirect pins .beads/redirect handling: the
// warnings for unusable targets and which directory is selected afterwards.
func TestDiscoveryRefusalPinsRedirect(t *testing.T) {
	runDiscoveryPinCases(t, []discoveryPinCase{
		{
			// A missing target is ignored and discovery keeps the source.
			name: "missing-target",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.corruptWorkspace("p", map[string]string{"redirect": f.path("missing/.beads") + "\n"}), nil
			},
			args: []string{"list"},
			exit: 1,
			stderr: "Warning: redirect target does not exist or is not a directory: $ROOT/missing/.beads\n" +
				pinCorruptMetadataRefusal("$ROOT/p/.beads"),
		},
		{
			// A target without project files is ignored the same way.
			name: "target-without-workspace",
			setup: func(f *discoveryPinFixture) (string, []string) {
				f.mkdir("bare/.beads")
				return f.corruptWorkspace("p", map[string]string{"redirect": "../bare/.beads\n"}), nil
			},
			args: []string{"list"},
			exit: 1,
			stderr: "Warning: ignoring redirect from $ROOT/p/.beads to $ROOT/bare/.beads because the target has no database or metadata.json; fix or delete the redirect file\n" +
				pinCorruptMetadataRefusal("$ROOT/p/.beads"),
		},
		{
			name: "corrupt-target",
			setup: func(f *discoveryPinFixture) (string, []string) {
				f.corruptWorkspace("target", nil)
				return f.workspace("p", map[string]string{"redirect": "../target/.beads\n"}), nil
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/target/.beads"),
		},
		{
			// Only one level is followed.
			name: "chain",
			setup: func(f *discoveryPinFixture) (string, []string) {
				f.corruptWorkspace("final", nil)
				f.corruptWorkspace("middle", map[string]string{"redirect": "../final/.beads\n"})
				return f.workspace("p", map[string]string{"redirect": "../middle/.beads\n"}), nil
			},
			args: []string{"list"},
			exit: 1,
			stderr: "Warning: redirect chains not allowed, ignoring redirect in $ROOT/middle/.beads\n" +
				pinCorruptMetadataRefusal("$ROOT/middle/.beads"),
		},
		{
			// An explicit BEADS_DIR is followed through its redirect.
			name: "explicit-beads-dir",
			setup: func(f *discoveryPinFixture) (string, []string) {
				f.corruptWorkspace("target", nil)
				f.workspace("source", map[string]string{"redirect": "../target/.beads\n"})
				return f.project("p"), []string{"BEADS_DIR=" + f.path("source/.beads")}
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/target/.beads"),
		},
		{
			name: "where-reports-source",
			setup: func(f *discoveryPinFixture) (string, []string) {
				f.yamlWorkspace("target", nil)
				return f.workspace("p", map[string]string{"redirect": "../target/.beads\n"}), nil
			},
			args:   []string{"where"},
			stdout: "$ROOT/target/.beads\n  (via redirect from $ROOT/p/.beads)\n",
		},
	})
}

// TestDiscoveryRefusalPinsDBSelection pins database selection by --db and by
// the BEADS_DB and BD_DB environment variables.
//
// Two of these record inconsistencies as they stand rather than endorse
// them: a data command ignores BD_DB, and lets a discoverable workspace in
// the working directory shadow BEADS_DB, while bd where honors both.
func TestDiscoveryRefusalPinsDBSelection(t *testing.T) {
	target := func(f *discoveryPinFixture) string {
		f.corruptWorkspace("target", nil)
		return f.path("target/.beads/dolt")
	}
	runDiscoveryPinCases(t, []discoveryPinCase{
		{
			// A --db path selects the workspace that owns it.
			name: "db-path",
			setup: func(f *discoveryPinFixture) (string, []string) {
				target(f)
				return f.project("p"), nil
			},
			args:   []string{"--db", "$ROOT/target/.beads/dolt", "list"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/target/.beads"),
		},
		{
			name: "db-path-beats-beads-dir",
			setup: func(f *discoveryPinFixture) (string, []string) {
				target(f)
				f.corruptWorkspace("other", nil)
				return f.project("p"), []string{"BEADS_DIR=" + f.path("other/.beads")}
			},
			args:   []string{"--db", "$ROOT/target/.beads/dolt", "list"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/target/.beads"),
		},
		{
			name: "db-path-beats-working-directory",
			setup: func(f *discoveryPinFixture) (string, []string) {
				target(f)
				return f.corruptWorkspace("p", nil), nil
			},
			args:   []string{"--db", "$ROOT/target/.beads/dolt", "list"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/target/.beads"),
		},
		{
			// A --db value that is a database name rather than an existing
			// path leaves discovery to the working directory.
			name: "db-name-without-workspace",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.project("p"), nil
			},
			args:   []string{"--db", "pindb", "list"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
		{
			name: "db-name-in-workspace",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.corruptWorkspace("p", nil), nil
			},
			args:   []string{"--db", "pindb", "list"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/p/.beads"),
		},
		{
			name: "beads-db-env",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.project("p"), []string{"BEADS_DB=" + target(f)}
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/target/.beads"),
		},
		{
			// Inconsistency: the working directory's workspace shadows BEADS_DB.
			name: "beads-db-env-shadowed-by-working-directory",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.corruptWorkspace("p", nil), []string{"BEADS_DB=" + target(f)}
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/p/.beads"),
		},
		{
			name: "beads-db-env-where",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.corruptWorkspace("p", nil), []string{"BEADS_DB=" + target(f)}
			},
			args:   []string{"where"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/target/.beads"),
		},
		{
			// Inconsistency: a data command ignores BD_DB.
			name: "bd-db-env",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.project("p"), []string{"BD_DB=" + target(f)}
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinNoDatabaseRefusal,
		},
		{
			name: "bd-db-env-where",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.project("p"), []string{"BD_DB=" + target(f)}
			},
			args:   []string{"where"},
			exit:   1,
			stderr: pinCorruptMetadataRefusal("$ROOT/target/.beads"),
		},
	})
}

// TestDiscoveryRefusalPinsBlockedEnvVarsRunFirst pins that the blocked
// backend variables are refused before any discovery, for every command.
func TestDiscoveryRefusalPinsBlockedEnvVarsRunFirst(t *testing.T) {
	runDiscoveryPinCases(t, []discoveryPinCase{
		{
			name: "no-workspace",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.project("p"), []string{"BD_BACKEND=sqlite"}
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinBlockedEnvRefusal("BD_BACKEND"),
		},
		{
			name: "corrupt-metadata",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.corruptWorkspace("p", nil), []string{"BD_BACKEND=sqlite"}
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinBlockedEnvRefusal("BD_BACKEND"),
		},
		{
			name: "explicit-beads-dir",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.project("p"), []string{"BD_BACKEND=sqlite", "BEADS_DIR=" + f.path("missing/.beads")}
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinBlockedEnvRefusal("BD_BACKEND"),
		},
		{
			// A BEADS_DIR that cannot even be read as a directory is still
			// reached only after the blocked variable is refused.
			name: "unreadable-beads-dir",
			setup: func(f *discoveryPinFixture) (string, []string) {
				f.write("file", "not a workspace\n")
				return f.project("p"), []string{"BD_BACKEND=sqlite", "BEADS_DIR=" + f.path("file")}
			},
			args:   []string{"list"},
			exit:   1,
			stderr: pinBlockedEnvRefusal("BD_BACKEND"),
		},
		{
			name: "db-path",
			setup: func(f *discoveryPinFixture) (string, []string) {
				f.corruptWorkspace("target", nil)
				return f.project("p"), []string{"BD_BACKEND=sqlite"}
			},
			args:   []string{"--db", "$ROOT/target/.beads/dolt", "list"},
			exit:   1,
			stderr: pinBlockedEnvRefusal("BD_BACKEND"),
		},
		{
			// --json does not turn the refusal into a JSON document.
			name: "database-backend-json",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.corruptWorkspace("p", nil), []string{"BD_DATABASE_BACKEND=dolt"}
			},
			args:   []string{"list", "--json"},
			exit:   1,
			stderr: pinBlockedEnvRefusal("BD_DATABASE_BACKEND"),
		},
		{
			name: "where-json",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.project("p"), []string{"BD_BACKEND=sqlite"}
			},
			args:   []string{"where", "--json"},
			exit:   1,
			stderr: pinBlockedEnvRefusal("BD_BACKEND"),
		},
		{
			name: "version-command",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.project("p"), []string{"BD_BACKEND=sqlite"}
			},
			args:   []string{"version"},
			exit:   1,
			stderr: pinBlockedEnvRefusal("BD_BACKEND"),
		},
		{
			// The root command then prints cobra's error and usage; only the
			// refusal itself is pinned.
			name: "version-flag",
			setup: func(f *discoveryPinFixture) (string, []string) {
				return f.project("p"), []string{"BD_BACKEND=sqlite"}
			},
			args:         []string{"--version"},
			exit:         1,
			stderr:       pinBlockedEnvRefusal("BD_BACKEND"),
			stderrPrefix: true,
		},
	})
}
