package doctor

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/schema"
)

// writeGateWorkspace writes a Dolt-backend .beads/metadata.json naming dbName
// and returns the repo path handed to AssessSchemaFixGate.
func writeGateWorkspace(t *testing.T, dbName string) string {
	t.Helper()
	return writeGateConfig(t, &configfile.Config{Backend: configfile.BackendDolt, DoltDatabase: dbName})
}

// writeGateConfig writes cfg as .beads/metadata.json in a fresh repo and
// returns the repo path.
func writeGateConfig(t *testing.T, cfg *configfile.Config) string {
	t.Helper()
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	return repo
}

// mkdirGateDoltDir creates <repo>/.beads/dolt, where DatabasePath resolves for
// a config with no dolt_data_dir.
func mkdirGateDoltDir(t *testing.T, repo string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repo, ".beads", "dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
}

// closedPort returns a loopback port with nothing listening on it.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// setGatePort routes doltserver port resolution to port for this test.
func setGatePort(t *testing.T, port int) {
	t.Helper()
	// BEADS_DOLT_SERVER_PORT is the highest-priority source in
	// doltserver.DefaultConfig, which openDoltDB resolves the port through.
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
}

// TestAssessSchemaFixGate_Unreachable pins the no-database shape: no schema
// hazard exists to guard against, so filesystem repair and printed advice are
// unaffected, but schema-writing fixes stay disallowed because there is no
// database to fix. Both cases reach it with nothing on disk to migrate — a
// missing config, and a config whose database directory was never created.
//
// "Configured, on disk, and unreachable" is deliberately NOT this shape: see
// TestAssessSchemaFixGate_PresentButUnopenableFailsClosed, which pins the
// fail-closed complement.
func TestAssessSchemaFixGate_Unreachable(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"no metadata.json": func(t *testing.T) string {
			repo := t.TempDir()
			if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o750); err != nil {
				t.Fatal(err)
			}
			setGatePort(t, closedPort(t))
			return repo
		},
		"configured but no database on disk": func(t *testing.T) string {
			setGatePort(t, closedPort(t))
			return writeGateWorkspace(t, "gate_unreachable")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			gate := AssessSchemaFixGate(setup(t))

			if gate.DBReachable {
				t.Error("DBReachable = true, want false")
			}
			if gate.AllowDBFix {
				t.Error("AllowDBFix = true on an unreachable database; schema-writing fixes must stay withheld")
			}
			if !gate.AllowFSFix {
				t.Error("AllowFSFix = false; filesystem repair must not depend on the database")
			}
			if !gate.RecommendFix || !gate.Determined {
				t.Errorf("RecommendFix=%v Determined=%v, want both true (no hazard to warn about)",
					gate.RecommendFix, gate.Determined)
			}
			if gate.Ahead || gate.Pending {
				t.Errorf("Ahead=%v Pending=%v, want neither without a database", gate.Ahead, gate.Pending)
			}
			if gate.BinaryVersion != schema.LatestVersion() {
				t.Errorf("BinaryVersion = %d, want %d", gate.BinaryVersion, schema.LatestVersion())
			}
			if gate.DBPresent {
				t.Error("DBPresent = true with no database on disk; nothing exists to guard")
			}
			if gate.BlocksDestructiveWrites() {
				t.Error("BlocksDestructiveWrites() = true with no database; there is no schema to skew")
			}
		})
	}
}

// TestAssessSchemaFixGate_PresentButUnopenableFailsClosed is the regression test
// for the fail-open reported on PR #5145: a server-mode repo whose database is
// still on disk but whose server is stopped assessed identically to a repo with
// no database at all. That shape left DBReachable false, which the destructive
// `--check` refusal keyed on, so `bd doctor --check=pollution --clean` skipped
// the refusal, auto-started the server through the migrating factory, applied
// pending migrations blind and then deleted rows — while the identical command
// against the identical schema was refused whenever the server happened to be up.
//
// A database that exists has a schema a migrating open can rewrite, whether or
// not this process could read it, so the verdict must fail closed. The other
// cases pin the arms where databaseExistsOnDisk departs from, or had drifted
// from, autoMigrateOnVersionBump's probe: a proxied-server workspace, whose
// data directory is local even though auto-migrate skips it, and a data
// directory whose stat fails with something other than ENOENT.
func TestAssessSchemaFixGate_PresentButUnopenableFailsClosed(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"stopped server": func(t *testing.T) string {
			repo := writeGateWorkspace(t, "gate_present_unopenable")
			// The database directory is what makes this distinct from the
			// unreachable case.
			mkdirGateDoltDir(t, repo)
			return repo
		},
		"proxied-server workspace": func(t *testing.T) string {
			repo := writeGateConfig(t, &configfile.Config{
				Backend:      configfile.BackendDolt,
				DoltMode:     configfile.DoltModeProxiedServer,
				DoltDatabase: "gate_proxied",
			})
			// The proxied root defaults to the same directory.
			mkdirGateDoltDir(t, repo)
			return repo
		},
		"data directory cannot be stat'ed": func(t *testing.T) string {
			if runtime.GOOS == "windows" {
				t.Skip("POSIX-only: Windows can report a file used as a directory as path-not-found, which os.IsNotExist matches")
			}
			// A data directory configured beneath a regular file fails its
			// stat with ENOTDIR: unknown, not absent. Relative, because Save
			// drops an absolute dolt_data_dir as machine-local.
			repo := writeGateConfig(t, &configfile.Config{
				Backend:      configfile.BackendDolt,
				DoltDatabase: "gate_unstatable",
				DoltDataDir:  filepath.Join("not-a-directory", "dolt"),
			})
			if err := os.WriteFile(filepath.Join(repo, ".beads", "not-a-directory"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return repo
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			setGatePort(t, closedPort(t))
			t.Setenv("BEADS_DOLT_DATA_DIR", "") // would override DatabasePath
			gate := AssessSchemaFixGate(setup(t))
			assertPresentButUnopenable(t, gate)
		})
	}
}

// TestAssessSchemaFixGate_MissingMetadataReadsAbsent pins databaseExistsOnDisk's
// deliberate departure from autoMigrateOnVersionBump, which falls back to
// DefaultConfig: with no metadata.json, a data directory on disk still assesses
// as the no-database shape. The gate cannot connect without metadata.json, so
// failing closed could never be cleared, and it would turn doctor's advice for
// this state — the filesystem-only `bd doctor --fix` that regenerates
// metadata.json — into "Do NOT run 'bd doctor --fix'".
func TestAssessSchemaFixGate_MissingMetadataReadsAbsent(t *testing.T) {
	setGatePort(t, closedPort(t))
	repo := t.TempDir()
	mkdirGateDoltDir(t, repo)

	gate := AssessSchemaFixGate(repo)

	if gate.DBPresent || gate.BlocksDestructiveWrites() {
		t.Errorf("DBPresent=%v BlocksDestructiveWrites()=%v, want both false without metadata.json",
			gate.DBPresent, gate.BlocksDestructiveWrites())
	}
	if !gate.RecommendFix || !gate.Determined {
		t.Errorf("RecommendFix=%v Determined=%v, want both true: the advice for this state is `bd doctor --fix`",
			gate.RecommendFix, gate.Determined)
	}
	if !gate.AllowsFix("Metadata Config") {
		t.Error(`AllowsFix("Metadata Config") = false; regenerating metadata.json is the repair for this state`)
	}
}

// assertPresentButUnopenable checks the fail-closed shape for a database that
// may exist but whose schema version was never read.
func assertPresentButUnopenable(t *testing.T, gate FixGate) {
	t.Helper()
	if !gate.DBPresent {
		t.Fatal("DBPresent = false; a database that may be on disk must be guarded")
	}
	if gate.DBReachable {
		t.Error("DBReachable = true although nothing is listening; the connection did not happen")
	}
	if !gate.BlocksDestructiveWrites() {
		t.Error("BlocksDestructiveWrites() = false; a destructive --check must be refused here")
	}
	if gate.AllowDBFix {
		t.Error("AllowDBFix = true for a database whose schema version was never read")
	}
	if gate.Determined {
		t.Error("Determined = true although the schema version could not be read")
	}
	if gate.RecommendFix {
		t.Error("RecommendFix = true; printed advice must not steer at --fix for an unread schema")
	}
	if gate.Reason == "" {
		t.Error("Reason is empty; the refusal and the sanitized advice both interpolate it")
	}
	if !gate.AllowFSFix {
		t.Error("AllowFSFix = false; filesystem repair does not depend on the schema")
	}
	// Recovery fixers are the cure for a database that cannot be opened, so they
	// must survive this shape exactly as they survive the no-database one.
	for _, name := range []string{"Corrupt Manifest", "Database Integrity"} {
		if !gate.AllowsFix(name) {
			t.Errorf("AllowsFix(%q) = false; the recovery fixers for an unopenable database must stay available", name)
		}
	}
	// ...but a repair that opens the store through the migrating factory is
	// exactly the hazard, and stays withheld.
	for _, name := range []string{"Fresh Clone", "Schema Compatibility"} {
		if gate.AllowsFix(name) {
			t.Errorf("AllowsFix(%q) = true; a migrating open is the hazard this gate exists for", name)
		}
	}
}

// TestFixGateBlocksDestructiveWrites pins the hazard predicate across every
// shape, so the destructive `--check` refusal cannot drift back onto a
// connectivity test. Only "no database at all" and "schema verified current"
// may admit a destructive write.
func TestFixGateBlocksDestructiveWrites(t *testing.T) {
	cases := []struct {
		name string
		gate FixGate
		want bool
	}{
		{"no database", FixGate{Determined: true, RecommendFix: true, AllowFSFix: true}, false},
		{"schema current", FixGate{Determined: true, DBReachable: true, DBPresent: true, AllowDBFix: true, AllowFSFix: true}, false},
		{"present but unopenable", FixGate{DBPresent: true, AllowFSFix: true}, true},
		{"reachable but unreadable", FixGate{DBReachable: true, DBPresent: true, AllowFSFix: true}, true},
		{"pending", FixGate{Determined: true, DBReachable: true, DBPresent: true, Pending: true, AllowFSFix: true}, true},
		{"ahead", FixGate{Determined: true, DBReachable: true, DBPresent: true, Ahead: true, AllowFSFix: true}, true},
	}
	for _, c := range cases {
		if got := c.gate.BlocksDestructiveWrites(); got != c.want {
			t.Errorf("%s: BlocksDestructiveWrites() = %v, want %v", c.name, got, c.want)
		}
	}
}
