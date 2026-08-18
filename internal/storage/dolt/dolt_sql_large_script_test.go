package dolt

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
)

// runDoltSQL executes SQL via `dolt sql` CLI in the given directory. The
// script is piped over stdin rather than passed as a `-q` argv element:
// schema.AllMigrationsSQL() is ~134KB, past Linux's per-argv MAX_ARG_STRLEN
// (131072 bytes), which fails execve with E2BIG when passed as an argument.
func runDoltSQL(t *testing.T, dir, query string) {
	t.Helper()
	cmd := exec.Command("dolt", "sql")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(query)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt sql failed in %s: %v\nQuery: %.200s...\nOutput: %s", dir, err, query, output)
	}
}

// TestRunDoltSQLHandlesLargeScript proves runDoltSQL can execute a SQL
// script larger than Linux's per-argv MAX_ARG_STRLEN (128KiB = 131072
// bytes). Passing the script as a single argv element (`dolt sql -q
// <script>`) blows past that limit and fails execve with E2BIG; the real
// schema init script (schema.AllMigrationsSQL()) is ~134KB and triggers it.
//
// Gated on testutil.RequireDoltCLIOnly (local dolt CLI only), not
// RequireDoltBinary/skipIfNoDolt (which also honor BEADS_TEST_SKIP=dolt, a
// blanket switch broad test wrappers set to exclude tests that depend on the
// shared containerized Dolt SQL server via testServerPort) — this test never
// touches that server, so it belongs in the default build lane and must keep
// running even when BEADS_TEST_SKIP=dolt is set for the container-dependent
// tests around it.
func TestRunDoltSQLHandlesLargeScript(t *testing.T) {
	testutil.RequireDoltCLIOnly(t)

	dir := t.TempDir()
	cmd := exec.Command("dolt", "init")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init failed in %s: %v\nOutput: %s", dir, err, output)
	}

	script := schema.AllMigrationsSQL()
	const maxArgStrlen = 131072 // Linux MAX_ARG_STRLEN, in bytes
	if len(script) <= maxArgStrlen {
		t.Fatalf("schema script is %d bytes, must exceed MAX_ARG_STRLEN (%d) to exercise the argv-limit path", len(script), maxArgStrlen)
	}

	runDoltSQL(t, dir, script)
}
