package doctor

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/schema"
)

// writeGateWorkspace writes a Dolt-backend .beads/metadata.json naming dbName
// and returns the repo path handed to AssessSchemaFixGate.
func writeGateWorkspace(t *testing.T, dbName string) string {
	t.Helper()
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltDatabase: dbName}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}
	return repo
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
// database to fix. Covers both "no config at all" and "configured but the
// server is not listening".
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
		"server not listening": func(t *testing.T) string {
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
		})
	}
}
