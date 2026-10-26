package fix

// Regression tests for the openFixDB password/port pairing. openFixDB dials
// the RUNTIME port from doltserver.DefaultConfig (env > port file >
// config.yaml > shared-server default 3308) but used to resolve the
// credentials-file password with the deprecated cfg.GetDoltServerPassword(),
// keyed on the CONFIG port (env > metadata.json > DefaultDoltServerPort 3307).
// The credentials file is sectioned [host:port], so any divergence misses the
// section and silently yields an EMPTY password: one auth failure per call,
// and the read-only checks that go through openFixDB (Clone-Local FKs,
// Cross-Table Duplicates) report a false "N/A (no database)". The store path
// (internal/storage/dolt) and the doctor read path (cmd/bd/doctor/dolt.go)
// already pair the lookup with the dialed port; fixDBDSN now does the same.
//
// These tests build the DSN only; no server is dialed.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/configfile"
)

// isolateDoltEnv clears every env var that changes fixDBDSN's port, host,
// shared-mode, or password resolution, so results don't depend on the host
// running the tests. Every reader treats "" as unset.
func isolateDoltEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BEADS_DOLT_SERVER_PORT",
		"BEADS_DOLT_PORT",
		"BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_SERVER_HOST",
		"BEADS_DOLT_PASSWORD",
	} {
		t.Setenv(k, "")
	}
}

// writeDSNFixture creates a beads dir whose port file names runtimePort and
// points BEADS_CREDENTIALS_FILE at a credentials file holding the given
// [host:port] -> password sections.
func writeDSNFixture(t *testing.T, runtimePort string, sections map[string]string) (beadsDir string) {
	t.Helper()
	beadsDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte(runtimePort+"\n"), 0o600); err != nil {
		t.Fatalf("write port file: %v", err)
	}
	var creds string
	for key, pass := range sections {
		creds += "[" + key + "]\npassword = " + pass + "\n"
	}
	credFile := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(credFile, []byte(creds), 0o600); err != nil {
		t.Fatalf("write credentials file: %v", err)
	}
	t.Setenv("BEADS_CREDENTIALS_FILE", credFile)
	return beadsDir
}

func parseDSN(t *testing.T, dsn string) *mysql.Config {
	t.Helper()
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("fixDBDSN produced an unparseable DSN %q: %v", dsn, err)
	}
	return parsed
}

// TestFixDBDSN_PasswordMatchesRuntimePort: metadata.json carries a config port
// (3307) that differs from the runtime port (3308). Credentials exist only
// under the runtime port.
func TestFixDBDSN_PasswordMatchesRuntimePort(t *testing.T) {
	isolateDoltEnv(t)
	beadsDir := writeDSNFixture(t, "3308", map[string]string{"127.0.0.1:3308": "SECRET-runtime"})

	cfg := &configfile.Config{
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerUser: "skillrx",
		DoltServerPort: 3307, // config port DIFFERS from runtime port 3308
		DoltDatabase:   "hendrix",
	}

	parsed := parseDSN(t, fixDBDSN(beadsDir, cfg))
	if parsed.Addr != "127.0.0.1:3308" {
		t.Errorf("DSN addr = %q, want runtime port 127.0.0.1:3308", parsed.Addr)
	}
	// Pre-fix behavior: lookup keyed [127.0.0.1:3307] -> section miss -> "".
	if parsed.Passwd != "SECRET-runtime" {
		t.Errorf("DSN password = %q, want SECRET-runtime (credentials lookup must use the runtime port)", parsed.Passwd)
	}
	if parsed.User != "skillrx" || parsed.DBName != "hendrix" {
		t.Errorf("DSN user/db = %q/%q, want skillrx/hendrix", parsed.User, parsed.DBName)
	}
}

// TestFixDBDSN_PasswordResolvedWhenMetadataOmitsPort is the shape that
// reproduced in the field: metadata.json has no dolt_server_port at all, so
// the deprecated getter falls back to DefaultDoltServerPort (3307) while the
// shared server listens on 3308 and the credentials file only has a
// [127.0.0.1:3308] section.
func TestFixDBDSN_PasswordResolvedWhenMetadataOmitsPort(t *testing.T) {
	isolateDoltEnv(t)
	beadsDir := writeDSNFixture(t, "3308", map[string]string{"127.0.0.1:3308": "s3cret"})

	cfg := &configfile.Config{
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerUser: "skillrx",
		DoltDatabase:   "beads",
		// DoltServerPort deliberately omitted.
	}

	// Precondition: the deprecated getter does miss; this is the bug.
	if got := cfg.GetDoltServerPassword(); got != "" {
		t.Fatalf("precondition broken: deprecated GetDoltServerPassword() = %q, want empty (3307 miss)", got)
	}

	parsed := parseDSN(t, fixDBDSN(beadsDir, cfg))
	if parsed.Addr != "127.0.0.1:3308" {
		t.Errorf("DSN addr = %q, want 127.0.0.1:3308", parsed.Addr)
	}
	if parsed.Passwd != "s3cret" {
		t.Errorf("DSN password = %q, want s3cret from the [127.0.0.1:3308] section", parsed.Passwd)
	}
}

// TestFixDBDSN_EnvPasswordStillWins locks BEADS_DOLT_PASSWORD precedence over
// the credentials file; the fix must not change that contract.
func TestFixDBDSN_EnvPasswordStillWins(t *testing.T) {
	isolateDoltEnv(t)
	beadsDir := writeDSNFixture(t, "3308", map[string]string{"127.0.0.1:3308": "from-file"})
	t.Setenv("BEADS_DOLT_PASSWORD", "from-env")

	cfg := &configfile.Config{
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerUser: "u",
		DoltServerPort: 3307,
		DoltDatabase:   "db",
	}
	parsed := parseDSN(t, fixDBDSN(beadsDir, cfg))
	if parsed.Passwd != "from-env" {
		t.Errorf("DSN password = %q, want from-env (env precedence preserved)", parsed.Passwd)
	}
}
