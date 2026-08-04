package main

import (
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
				tc.apply(t)
				if err := serveModeGate(); err != nil {
					t.Fatalf("serveModeGate() = %v, want nil: this mode has a SQL server and bd serve builds a provider for it", err)
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
		err := serveModeGate()
		var unsupported *storage.ErrUnsupported
		if !errors.As(err, &unsupported) {
			t.Fatalf("serveModeGate() = %v, want the typed embedded refusal", err)
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

// TestServeBuildsOnlyAProviderBackedServer is the source-level half of the
// embedded refusal, and it exists because the other half stopped being
// structural.
//
// httpapi.Listen once took a unit-of-work provider or nothing, and there is no
// provider for the embedded backend, so an embedded-backed server could not be
// built even with serveModeGate deleted. httpapi.Config now also accepts the
// two issue roles as a database source, and the embedded store publishes both
// accessors — so the shortest edit from here to a server whose per-request
// atomicity claim is false is handing Listen the roles off the store the root
// command already opened. internal/httpapi will not catch it: a role is an
// interface, and nothing about one says which backend is behind it.
//
// So the claim this pins is narrow and checkable: bd names exactly one database
// source, and it is the provider serveModeGate has already vouched for. An edit
// that reaches for the roles instead fails here and has to read that gate.
func TestServeBuildsOnlyAProviderBackedServer(t *testing.T) {
	dir := packageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	configs, provided := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Config" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "httpapi" {
				return true
			}
			configs++
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "Reader", "Claimer":
					t.Errorf("%s: bd serve sets httpapi.Config.%s. The roles source bypasses the unit-of-work "+
						"provider serveModeGate vouched for, and internal/httpapi cannot tell an embedded-backed "+
						"role from any other — read serveModeGate before changing this",
						fset.Position(kv.Pos()), key.Name)
				case "Provider":
					provided++
				}
			}
			return true
		})
	}

	// Both counts, so the test cannot pass because the literal moved, was
	// renamed, or stopped naming a source at all.
	if configs == 0 {
		t.Fatal("no httpapi.Config literal in cmd/bd: this test no longer looks at the code that builds the server")
	}
	if provided != configs {
		t.Errorf("%d of %d httpapi.Config literals set Provider; every server bd builds is provider-backed", provided, configs)
	}
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
