package dolt

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
)

// maxArgStrlen is Linux's per-argv limit, in bytes. An argv element larger
// than this fails execve with E2BIG.
const maxArgStrlen = 131072

// runDoltSQL executes SQL via `dolt sql` CLI in the given directory. The
// script is piped over stdin rather than passed as a `-q` argv element:
// schema.AllMigrationsSQL() is ~130KB and sits right at Linux's per-argv
// MAX_ARG_STRLEN (131072 bytes), which fails execve with E2BIG when passed as
// an argument.
func runDoltSQL(t *testing.T, dir, query string) {
	t.Helper()
	cmd := exec.Command("dolt", "sql")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(query)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt sql failed in %s: %v\nQuery: %.200s...\nOutput: %s", dir, err, query, output)
	}
}

// oversizedSchemaScript returns the real schema init script, padded with
// statements the CLI still has to parse until it is guaranteed to exceed
// MAX_ARG_STRLEN.
//
// The bundle used to clear the limit on its own and the test read its length
// directly. It no longer can: registering a direct-DDL override in
// cli_migrations.go REPLACES a migration's guarded PREPARE text with a much
// shorter statement, so the bundle shrinks. 0065's override took it from
// 132601 to 130281 bytes (gastownhall/beads#5910) and this test went red for a
// reason that had nothing to do with the argv path. The bundle's size is an
// accident of migration content; what this test needs is a script that is
// definitely too big, and it should build one.
func oversizedSchemaScript() string {
	return padPastArgvLimit(schema.AllMigrationsSQL())
}

// padPastArgvLimit appends no-op statements to script until it exceeds
// maxArgStrlen, and returns a script already over the limit unchanged.
func padPastArgvLimit(script string) string {
	const filler = "SELECT 1;\n"
	if short := maxArgStrlen + 1 - len(script); short > 0 {
		script += "\n" + strings.Repeat(filler, short/len(filler)+1)
	}
	return script
}

// TestPadPastArgvLimit is the control for the padding: a helper that quietly
// returned its input would leave TestRunDoltSQLHandlesLargeScript passing on a
// script small enough to have fit in argv, which is the failure mode the whole
// test exists to rule out.
func TestPadPastArgvLimit(t *testing.T) {
	for _, size := range []int{0, 1, maxArgStrlen - 1, maxArgStrlen, maxArgStrlen + 1, maxArgStrlen * 2} {
		got := padPastArgvLimit(strings.Repeat("x", size))
		if len(got) <= maxArgStrlen {
			t.Errorf("padPastArgvLimit(%d bytes) returned %d bytes, want > %d", size, len(got), maxArgStrlen)
		}
		if size > maxArgStrlen && len(got) != size {
			t.Errorf("padPastArgvLimit(%d bytes) padded an already-oversized script to %d bytes", size, len(got))
		}
	}
}

// TestRunDoltSQLHandlesLargeScript proves runDoltSQL can execute a SQL
// script larger than Linux's per-argv MAX_ARG_STRLEN (128KiB = 131072
// bytes). Passing the script as a single argv element (`dolt sql -q
// <script>`) blows past that limit and fails execve with E2BIG.
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

	// Keep the precondition assertion: without it a broken padding helper
	// would leave this passing on a script small enough to have gone through
	// argv, proving nothing.
	script := oversizedSchemaScript()
	if len(script) <= maxArgStrlen {
		t.Fatalf("script is %d bytes, must exceed MAX_ARG_STRLEN (%d) to exercise the argv-limit path", len(script), maxArgStrlen)
	}

	runDoltSQL(t, dir, script)
}
