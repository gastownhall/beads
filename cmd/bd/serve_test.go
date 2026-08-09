package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage"
)

// TestServeFlags pins the flag surface. v0 has two flags and no more: every
// other bound in this server (in-flight limit, connection cap, wait budget,
// deadline, pool caps) is a constant precisely so that it can become a flag
// later, deliberately, rather than arriving as one nobody designed.
func TestServeFlags(t *testing.T) {
	var got []string
	serveCmd.Flags().VisitAll(func(f *pflag.Flag) { got = append(got, f.Name) })
	sort.Strings(got)

	want := []string{"addr", "allow-non-loopback"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("bd serve flags = %v, want %v", got, want)
	}

	addr := serveCmd.Flags().Lookup("addr")
	if addr == nil {
		t.Fatal("no --addr flag")
	}
	// Loopback, and ephemeral: a default port would be a port nobody chose,
	// and picking one blesses a deployment shape the design deliberately does
	// not bless.
	if addr.DefValue != "127.0.0.1:0" {
		t.Errorf("--addr default = %q, want 127.0.0.1:0", addr.DefValue)
	}
	if nonLoopback := serveCmd.Flags().Lookup("allow-non-loopback"); nonLoopback == nil {
		t.Fatal("no --allow-non-loopback flag")
	} else if nonLoopback.DefValue != "false" {
		t.Errorf("--allow-non-loopback default = %q, want false", nonLoopback.DefValue)
	}
}

// TestServeHelpTracksTheReadinessProbe. The help tells operators to probe
// readiness with GET /v0/beads/ready?limit=1, which is a 501 stub in this
// build — a probe wired from the help alone is permanently not-ready. The
// caveat that says so is transitional, and this keeps it honest in both
// directions: while ready.list is unimplemented the help must carry it, and
// the moment the handler lands the caveat must go. Nobody has to remember.
func TestServeHelpTracksTheReadinessProbe(t *testing.T) {
	implemented := slices.Contains(httpapi.Capabilities(), "ready.list")
	documented := strings.Contains(serveCmd.Long, "answers 501")

	switch {
	case implemented && documented:
		t.Error("ready.list is implemented; delete the 501 caveat from the PROBES section of `bd serve --help`")
	case !implemented && !documented:
		t.Error("`bd serve --help` sends operators to a readiness endpoint this build answers 501 to, with no hint that it does")
	}
}

func TestServeCommandIsRegistered(t *testing.T) {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == serveCmdName {
			return
		}
	}
	t.Fatal("serve is not registered under the root command")
}

// TestServeSkipsPostCommandMaintenance. In a server-mode workspace bd serve
// takes the non-proxied PersistentPostRunE branch, which is the one that runs
// the auto-commit / backup / export / push net — so on the way out of a SIGTERM
// a server would push and export, hours of requests attributed to the shutdown.
// The proxied branch never had that, and this is what keeps the two modes
// telling the operator the same story.
func TestServeSkipsPostCommandMaintenance(t *testing.T) {
	if runsPostCommandMaintenance(serveCmdName, false) {
		t.Error("bd serve runs post-command maintenance; the server would export and push at shutdown")
	}
	// The exclusion is serve's alone: a write command in the same workspace
	// still gets the whole net.
	if !runsPostCommandMaintenance("update", false) {
		t.Error("bd update no longer runs post-command maintenance")
	}
	// And strict readonly still wins over everything, serve included.
	if runsPostCommandMaintenance("update", true) {
		t.Error("strict readonly no longer suppresses post-command maintenance")
	}
}

// TestServeRefusalsPromiseNothing is the honesty gate on the mode gate. The
// refusal is typed so a caller can dispatch on it, and it must promise nothing
// — claiming otherwise sends an operator to do the wrong work.
//
// The second case was, until bd-emv, the mirror image: it pinned the STAGED
// refusal for dolt server mode and required its text to read as "not yet". That
// wiring landed, so the case now asserts the reality it was staging for rather
// than being deleted with the refusal. Deleting it would have left nothing
// asserting that these workspaces are served: a later change could route them
// back into a refusal and no test would notice.
func TestServeRefusalsPromiseNothing(t *testing.T) {
	t.Run("embedded is permanent", func(t *testing.T) {
		err := errServeEmbedded()

		var unsupported *storage.ErrUnsupported
		if !errors.As(err, &unsupported) {
			t.Fatalf("err = %v, want a typed storage.ErrUnsupported", err)
		}
		if unsupported.Op != "serve" {
			t.Errorf("Op = %q, want serve", unsupported.Op)
		}
		// Backend names a BACKEND. The type documents it that way and it is the
		// embryo of the pluggable-backend error taxonomy, so a topology string
		// here would hand every downstream errors.As a mixed vocabulary.
		if unsupported.Backend != "embedded-dolt" {
			t.Errorf("Backend = %q, want embedded-dolt", unsupported.Backend)
		}

		msg := err.Error()
		if !strings.Contains(msg, "embedded Dolt") {
			t.Errorf("message does not name the workspace's actual backend: %q", msg)
		}
		for _, promise := range []string{"not yet", "coming", "tracked", "will be"} {
			if strings.Contains(strings.ToLower(msg), promise) {
				t.Errorf("permanent refusal hints at future support (%q): %q", promise, msg)
			}
		}
	})

	t.Run("the dolt server modes are served, not refused", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			apply func(t *testing.T)
		}{
			{
				name:  "server / external-server",
				apply: func(t *testing.T) { serverMode = true },
			},
			{
				name: "shared-server",
				apply: func(t *testing.T) {
					t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
				},
			},
			{
				name:  "proxied-server",
				apply: func(t *testing.T) { proxiedServerMode = true },
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				useStorageModeGlobals(t)
				beadsDir := writeContractBackendConfig(t, configfile.BackendDolt)
				tc.apply(t)
				db, err := serveDatabaseSource(beadsDir)
				if err != nil {
					t.Fatalf("serveDatabaseSource() = %v, want nil: this mode has a SQL server and bd serve builds a provider for it", err)
				}
				if db.source != serveSourceProvider {
					t.Errorf("source = %v, want serveSourceProvider", db.source)
				}
			})
		}
	})

	t.Run("embedded is still gated", func(t *testing.T) {
		useStorageModeGlobals(t)
		if !isEmbeddedMode() {
			// The !cgo build has no embedded backend to refuse: isEmbeddedMode
			// is a constant false there, so there is no case to make.
			t.Skip("this build cannot open an embedded workspace")
		}
		beadsDir := writeContractBackendConfig(t, configfile.BackendDolt)
		_, err := serveDatabaseSource(beadsDir)
		var unsupported *storage.ErrUnsupported
		if !errors.As(err, &unsupported) {
			t.Fatalf("serveDatabaseSource() = %v, want the typed embedded refusal", err)
		}
		if unsupported.Backend != "embedded-dolt" {
			t.Errorf("Backend = %q, want embedded-dolt", unsupported.Backend)
		}
	})
}

// useStorageModeGlobals points the storage-mode accessors at the package
// globals for the duration of one test and restores them after. The mode gate
// reads them through cmdCtx otherwise, which no unit test builds.
func useStorageModeGlobals(t *testing.T) {
	t.Helper()
	oldServerMode, oldProxied := serverMode, proxiedServerMode
	oldCmdCtx, oldUseGlobals := cmdCtx, testModeUseGlobals
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	serverMode, proxiedServerMode = false, false
	cmdCtx, testModeUseGlobals = nil, true
	t.Cleanup(func() {
		serverMode, proxiedServerMode = oldServerMode, oldProxied
		cmdCtx, testModeUseGlobals = oldCmdCtx, oldUseGlobals
	})
}

// TestServeNamesOneDatabaseSourcePerServerItBuilds is the source-level half of
// the embedded refusal. It replaces TestServeBuildsOnlyAProviderBackedServer,
// which pinned a world in which bd built provider-backed servers only.
//
// That world ended for a reason and not by accident: the registered-backend arm
// exists precisely so a store bd already opened can be served. But the property
// the old test protected did not end with it — internal/httpapi still cannot
// tell an embedded-backed role from any other, so the shortest edit from here to
// a server whose per-request atomicity claim is false is still handing Listen
// the roles off whatever store happens to be open.
//
// So this pins what is left, and it is narrow and checkable:
//
//   - every httpapi.Config bd builds names exactly ONE COMPLETE database
//     source — Provider alone, or Reader and Claimer together. A half-set pair
//     binds, answers every read, and nil-dereferences on the first claim;
//     Listen refuses it, and so does this, one layer earlier;
//   - both arms exist, so the test cannot pass because one was deleted;
//   - a roles-bearing Config is built only inside a function that consults
//     serveDatabaseSource and names serveSourceStore. That is the gate holding
//     the embedded refusal, so an edit that reaches for the roles anywhere else
//     fails here and has to go read it.
func TestServeNamesOneDatabaseSourcePerServerItBuilds(t *testing.T) {
	dir := packageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	var providerBacked, rolesBacked int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		// Attribute every literal to its enclosing function. A literal outside
		// one is not reachable through the gate by construction, so the
		// whole-file count below refuses to let it hide.
		attributed := 0
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			// NOTE: Reader+Claimer is the ROLES-SOURCE SIGNAL, not the whole
			// role set — Config also carries Settings, Stats, CycleDetector,
			// ReadyCounter, Sweeper, EdgeReader, BatchCreator and more. That is
			// sound for what this test asks (which database source did this
			// Config name), but the pair is hand-maintained: a future source
			// signal that is not Reader+Claimer would read as "no database
			// source". Deriving it is bd-lidlu's territory.
			for _, lit := range httpapiConfigLiterals(fn) {
				attributed++
				keys := configLiteralKeys(lit)
				provider := keys["Provider"]
				reader, claimer := keys["Reader"], keys["Claimer"]

				switch {
				case provider && (reader || claimer):
					t.Errorf("%s: this httpapi.Config names two database sources; pass exactly one",
						fset.Position(lit.Pos()))
				case reader != claimer:
					t.Errorf("%s: this httpapi.Config sets one issue role without the other; a reader without a "+
						"claimer binds, answers every read, and fails the first claim on a live server",
						fset.Position(lit.Pos()))
				case provider:
					providerBacked++
				case reader && claimer:
					rolesBacked++
					if !functionMentions(fn, "serveDatabaseSource") || !functionMentions(fn, "serveSourceStore") {
						t.Errorf("%s: %s builds a roles-backed httpapi.Config without consulting serveDatabaseSource. "+
							"That gate is where the permanent embedded-Dolt refusal lives, and internal/httpapi "+
							"cannot tell an embedded-backed role from any other — read it before changing this",
							fset.Position(lit.Pos()), fn.Name.Name)
					}
				default:
					t.Errorf("%s: this httpapi.Config names no database source", fset.Position(lit.Pos()))
				}
			}
		}
		if total := len(httpapiConfigLiterals(file)); total != attributed {
			t.Errorf("%s: %d of %d httpapi.Config literals are outside any function; such a literal cannot be "+
				"reached through serveDatabaseSource", name, total-attributed, total)
		}
	}

	// Both arms, so the test cannot pass because one was deleted, renamed, or
	// stopped naming a source at all.
	if providerBacked == 0 {
		t.Error("no provider-backed httpapi.Config in cmd/bd: the dolt SQL-server workspaces are no longer served")
	}
	if rolesBacked == 0 {
		t.Error("no roles-backed httpapi.Config in cmd/bd: a registered backend is no longer served from its store")
	}
}

// httpapiConfigLiterals returns every `httpapi.Config{...}` composite literal
// under n.
func httpapiConfigLiterals(n ast.Node) []*ast.CompositeLit {
	var out []*ast.CompositeLit
	ast.Inspect(n, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Config" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "httpapi" {
			out = append(out, lit)
		}
		return true
	})
	return out
}

// configLiteralKeys is the set of field names a keyed composite literal sets.
func configLiteralKeys(lit *ast.CompositeLit) map[string]bool {
	keys := make(map[string]bool, len(lit.Elts))
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok {
			keys[key.Name] = true
		}
	}
	return keys
}

// functionMentions reports whether fn's body names the given identifier.
func functionMentions(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && ident.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// packageDir is this test file's own directory, resolved from the compiled-in
// source path rather than from the working directory: tests in this package
// chdir into temporary workspaces, and a relative path would read whichever one
// happened to be current.
func packageDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve this test's source path")
	}
	return filepath.Dir(file)
}

// TestServeRefusesStrictReadonly.
//
// `bd --readonly serve` is a contradiction, and until it was refused it
// resolved differently on each of the two database sources — badly on both.
//
// On the STORE source the root command opens the workspace through
// backend.OpenReadOnly and serve takes its claimer off that store, so the
// server bound, GET /v0/beads/context went on advertising `issues.claim` (the
// capability set is derived from the route table and knows nothing about a CLI
// flag), and every claim came back 500 with the issue left open and unassigned.
// A server that advertises a write it will always fail is worse than no server.
//
// On the PROVIDER source it was the other silent answer: serve builds its own
// unit-of-work provider from the workspace's connection settings, which has no
// read-only posture at all, so `--readonly` bought the operator nothing and
// every claim landed. (Proxied mode never got that far — the root pre-run
// already refuses strict readonly for it.)
//
// Refusing is the same policy bd already applies one layer down, where a
// backend that cannot guarantee mutation-free access is turned away rather than
// opened anyway. It is also the only answer that is the same on both sources.
//
// The gate is ahead of the workspace, which this pins by refusing in a
// directory that has no workspace at all: no topology can reach past it.
func TestServeRefusesStrictReadonly(t *testing.T) {
	// Refused in a directory with NO workspace, which is how the ordering is
	// pinned: the gate cannot be sitting behind a topology if there is no
	// topology to resolve. That is what makes one test cover both sources.
	// TestServeRefusesStrictReadonlyOnARegisteredBackend drives the same
	// refusal in a workspace that would otherwise have served.
	t.Run("before any workspace is resolved", func(t *testing.T) {
		stderr, err := runServeUnderReadonly(t, t.TempDir())
		if err == nil {
			t.Fatalf("bd --readonly serve bound a server\nstderr:\n%s", stderr)
		}
		if !strings.Contains(stderr, "--readonly") {
			t.Errorf("the refusal does not name the flag that caused it: %q", stderr)
		}
		if strings.Contains(stderr, "cannot resolve workspace context") {
			t.Errorf("the readonly refusal runs after the workspace is resolved: %q", stderr)
		}
	})

	t.Run("the capability set stays honest", func(t *testing.T) {
		// The other way to settle this would have been to drop issues.claim
		// from what a read-only server advertises. That is a wire change —
		// `capabilities` is the documented pre-flight a client checks — and it
		// would make one operation's presence depend on a CLI flag, which no
		// client can discover before connecting. Refusing the process instead
		// leaves the published surface a property of the build.
		if !slices.Contains(httpapi.Capabilities(), "issues.claim") {
			t.Error("issues.claim left the advertised capability set; bd serve refuses --readonly " +
				"precisely so that set never has to vary")
		}
	})
}

// runServeUnderReadonly runs bd serve with strict readonly set, in dir, and
// returns what it wrote to stderr. The refusal reaches the operator through
// HandleError, which writes the message to stderr and returns an opaque exit
// error, so the message is only observable here.
func runServeUnderReadonly(t *testing.T, dir string) (string, error) {
	t.Helper()
	useStorageModeGlobals(t)
	restoreServeGlobals(t)

	origReadonly := readonlyMode
	readonlyMode = true
	t.Cleanup(func() { readonlyMode = origReadonly })

	store = nil
	serveAddr, serveAllowNonLoopback = "127.0.0.1:0", false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	setRootContext(ctx, cancel)
	t.Chdir(dir)
	// The workspace snapshot is resolved once per process and cached, so
	// without this a directory with no workspace would still resolve to
	// whichever one an earlier test in this binary left behind — and "no
	// workspace" is the whole premise of the ordering assertion above.
	beads.ResetCaches()
	t.Cleanup(beads.ResetCaches)

	var err error
	stderr := captureBootstrapStderr(t, func() { err = runServe() })
	return stderr, err
}
