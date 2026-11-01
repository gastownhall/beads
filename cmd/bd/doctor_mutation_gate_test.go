// Tests for doctor's mutation gate (#6028): `bd doctor --fix` / `--clean` must
// refuse to run under strict --readonly, exactly like the ~120 write commands
// that call CheckReadonly at the top of their RunE. Doctor never made that call
// — it opts out of the root PersistentPreRunE store init via
// skipStoreAnnotation, which also opted it out of that hook's gates — so both
// --readonly and an active MIGRATION-FREEZE were bypassed structurally.
//
// The freeze half of the same gate lives in migration_freeze_gate_test.go,
// beside the rest of the freeze suite; both halves share the helpers below and
// the hermetic subprocess harness declared there.
//
// The same gate for `bd backup init`, `backup remove`, `backup sync` and
// `bd import` sits further down, between the doctor tests it mirrors.
//
// This file MUST NOT carry a cgo build tag: it drives a bd binary built with
// the gms_pure_go tag via subprocess, and asserts on files and output (and, for
// the server rows of the backup/import tests, on the Dolt test server's tables).

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// doctorMutationSurface is one class of doctor invocation that mutates the
// workspace. Section 2 of the #6028 design enumerates the complete set: every
// mutating path is reached through --fix or --clean and nothing else, so these
// five rows cover the whole write surface, including fixers added later.
type doctorMutationSurface struct {
	name string
	args []string
	// op is the operation label the gate reports, derived from which flag made
	// the run mutating (--fix wins when both are set).
	op string
}

func doctorMutationSurfaces() []doctorMutationSurface {
	return []doctorMutationSurface{
		{name: "fix", args: []string{"doctor", "--fix", "--yes"}, op: "doctor --fix"},
		{name: "fix-interactive", args: []string{"doctor", "--fix", "-i"}, op: "doctor --fix"},
		{name: "validate-fix", args: []string{"doctor", "--check=validate", "--fix", "--yes"}, op: "doctor --fix"},
		{name: "pollution-clean", args: []string{"doctor", "--check=pollution", "--clean", "--yes"}, op: "doctor --clean"},
		{name: "artifacts-clean", args: []string{"doctor", "--check=artifacts", "--clean", "--yes"}, op: "doctor --clean"},
	}
}

// doctorWorkspaceFingerprint captures the two workspace files a refused doctor
// run is most likely to have touched: .local_version (trackBdVersion's target)
// and metadata.json (the Metadata Config / Database fixers' target).
func doctorWorkspaceFingerprint(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	fingerprint := make(map[string][]byte)
	for _, name := range []string{localVersionFile, "metadata.json"} {
		path := filepath.Join(dir, ".beads", name)
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				t.Fatalf("fingerprinting %s: %v", path, err)
			}
			continue
		}
		fingerprint[name] = data
	}
	return fingerprint
}

// assertDoctorWorkspaceUnchanged proves the refusal happened before any
// mutation, not merely that the refusal message was printed.
func assertDoctorWorkspaceUnchanged(t *testing.T, dir string, before map[string][]byte) {
	t.Helper()
	after := doctorWorkspaceFingerprint(t, dir)
	if len(after) != len(before) {
		t.Errorf("workspace file set changed across a refused doctor run: had %d tracked files, now %d", len(before), len(after))
	}
	for name, want := range before {
		got, ok := after[name]
		if !ok {
			t.Errorf(".beads/%s was deleted by a refused doctor run", name)
			continue
		}
		if string(got) != string(want) {
			t.Errorf(".beads/%s was rewritten by a refused doctor run:\nbefore:\n%s\nafter:\n%s", name, want, got)
		}
	}
}

// assertDoctorRefusalIsClean checks the shared shape of every gate refusal:
// nothing on stdout, and none of the output a fix or clean run would produce.
func assertDoctorRefusalIsClean(t *testing.T, stdout string) {
	t.Helper()
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty when doctor is refused, got:\n%s", stdout)
	}
	for _, marker := range []string{"Applying fixes", "Deleting", "Verifying fixes"} {
		if strings.Contains(stdout, marker) {
			t.Errorf("refused doctor run produced %q on stdout — it started mutating before refusing:\n%s", marker, stdout)
		}
	}
}

// TestDoctorMutationBlockedInReadonlyMode is the readonly half of the gate.
// One workspace serves every row: a refused run must leave it untouched, so a
// row that leaks a mutation is caught by the next row's fingerprint too.
func TestDoctorMutationBlockedInReadonlyMode(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)
	before := doctorWorkspaceFingerprint(t, dir)

	for _, tt := range doctorMutationSurfaces() {
		t.Run(tt.name, func(t *testing.T) {
			args := append(append([]string{}, tt.args...), "--readonly")
			stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, args...)

			if code != 1 {
				t.Fatalf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			want := "operation '" + tt.op + "' is not allowed in read-only mode"
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr missing %q:\n%s", want, stderr)
			}
			assertDoctorRefusalIsClean(t, stdout)
			assertDoctorWorkspaceUnchanged(t, dir, before)
		})
	}
}

// The backup and import commands: the same gate, a different failure.
//
// `bd backup init`, `backup remove`, `backup sync` and `bd import` never called
// CheckReadonly, so strict --readonly only held as far as the store underneath
// happened to refuse. An embedded store is opened read-only, so those commands
// failed, but with the storage layer's text rather than the standard refusal,
// and `backup sync` took the backup lock first and left .beads/backup.lock
// behind. A server-mode store has no such floor: the same commands ran to
// completion and rewrote or emptied the backup registry, wrote the backup
// destination, or imported issues. So every row runs in both flavours, and the
// checks go past the file tree: a server-mode import leaves .beads untouched
// even when it succeeds, so the issue count and the registry are compared too.
//
// `backup status` and `list` only read, and stay allowed;
// TestBackupStatusAndListWorkInReadonlyMode pins that. `bd backup` on its own is
// a command group that only prints help, and is left as it is.

// backupImportSurface is one command that strict --readonly must refuse.
type backupImportSurface struct {
	name string
	// args are the command and its arguments; the test appends the global flags.
	args []string
	// op is the operation label the gate reports.
	op string
}

func backupImportSurfaces(w *readonlyGateWorkspace) []backupImportSurface {
	return []backupImportSurface{
		// A destination other than the registered one, so a run that goes
		// through shows up as a changed registry.
		{name: "backup-init", args: []string{"backup", "init", filepath.Join(w.dir, "other-backup-dest")}, op: "backup init"},
		{name: "backup-remove", args: []string{"backup", "remove"}, op: "backup remove"},
		{name: "backup-sync", args: []string{"backup", "sync"}, op: "backup sync"},
		{name: "import", args: []string{"import", w.importFile}, op: "import"},
		// A preview writes nothing, but strict --readonly already refuses
		// `create --dry-run`, so import's is refused the same way.
		{name: "import-dry-run", args: []string{"import", "--dry-run", w.importFile}, op: "import"},
	}
}

// readonlyGateFlagSets are the global flag combinations every row runs under.
// --sandbox changes how bd treats the store (it disables auto-push); the gate
// has to hold with it as well.
var readonlyGateFlagSets = []struct {
	name  string
	flags []string
}{
	{name: "readonly", flags: []string{"--readonly"}},
	{name: "readonly-sandbox", flags: []string{"--readonly", "--sandbox"}},
}

// readonlyGateFlavours are the two stores the gate has to hold over.
var readonlyGateFlavours = []struct {
	name  string
	setup func(*testing.T) *readonlyGateWorkspace
}{
	{name: "embedded", setup: newEmbeddedReadonlyGateWorkspace},
	{name: "server", setup: newServerReadonlyGateWorkspace},
}

// readonlyGateWorkspace is an initialized workspace holding one issue and one
// registered backup, plus what a refused command has to leave alone.
type readonlyGateWorkspace struct {
	bd, dir    string
	prefix     string
	dest       string // directory behind the registered default backup
	importFile string // a one-issue JSONL file for `bd import`
	// Server flavour only: the shared test server and this workspace's database
	// on it. A zero port means the embedded flavour.
	port     int
	database string
}

// readonlyGateState is everything a refused command could have changed.
type readonlyGateState struct {
	tree     readonlyTreeSnapshot // the whole .beads directory
	dest     readonlyTreeSnapshot // the backup destination
	issues   int                  // what `bd list` reports
	registry string               // server flavour only: dolt_backups as name=url rows
}

func newEmbeddedReadonlyGateWorkspace(t *testing.T) *readonlyGateWorkspace {
	t.Helper()
	bd, dir := setupMigrationFreezeWorkspace(t)
	w := &readonlyGateWorkspace{bd: bd, dir: dir, prefix: "test"}
	w.seed(t)
	return w
}

// newServerReadonlyGateWorkspace is the same workspace on the shared Dolt test
// server. The default lane starts none, so it skips there, as the other
// server-mode readonly test in this package does.
func newServerReadonlyGateWorkspace(t *testing.T) *readonlyGateWorkspace {
	t.Helper()
	port, err := strconv.Atoi(os.Getenv("BEADS_DOLT_PORT"))
	if err != nil || port <= 0 {
		t.Skip("shared Dolt test server is unavailable")
	}
	dir := t.TempDir()
	bd := buildBDForInitTests(t)
	runGitForBootstrapTest(t, dir, "init", "-q")
	runGitForBootstrapTest(t, dir, "config", "core.hooksPath", ".git/hooks")

	// The prefix names the database on the shared server, so it must be unique.
	prefix := "rog" + strings.TrimPrefix(uniqueTestDBName(t), "testdb_")
	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "init", "--server", "--server-host", "127.0.0.1",
		"--server-port", strconv.Itoa(port), "--external", "--prefix", prefix,
		"--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	if code != 0 {
		t.Fatalf("bd init --server failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	var meta struct {
		Database string `json:"dolt_database"`
	}
	data, err := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("reading metadata.json: %v", err)
	}
	if err := json.Unmarshal(data, &meta); err != nil || meta.Database == "" {
		t.Fatalf("metadata.json names no dolt_database (%v):\n%s", err, data)
	}
	t.Cleanup(func() {
		db, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String())
		if err != nil {
			return
		}
		defer db.Close()
		_, _ = db.ExecContext(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", meta.Database)) //nolint:gosec // generated test name
	})

	w := &readonlyGateWorkspace{bd: bd, dir: dir, prefix: prefix, port: port, database: meta.Database}
	w.seed(t)
	return w
}

// seed gives the workspace one issue, one registered backup and a JSONL file
// whose single issue is not in the workspace yet.
func (w *readonlyGateWorkspace) seed(t *testing.T) {
	t.Helper()
	w.dest = filepath.Join(w.dir, "backup-dest")
	w.importFile = filepath.Join(w.dir, "import.jsonl")
	record := fmt.Sprintf(`{"id":"%s-gate1","title":"imported under readonly","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z"}`+"\n", w.prefix)
	if err := os.WriteFile(w.importFile, []byte(record), 0o644); err != nil {
		t.Fatalf("writing %s: %v", w.importFile, err)
	}
	if stdout, stderr, code := w.run(t, "create", "seed issue", "-p", "2"); code != 0 {
		t.Fatalf("bd create failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	w.registerBackup(t)
}

func (w *readonlyGateWorkspace) run(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	return runBDMigrationFreeze(t, w.bd, w.dir, args...)
}

// registerBackup points the default backup at w.dest. Rows call it before each
// run: a row that went through when it should have been refused may have moved
// or removed the backup, and the next row still needs one to be refused against.
func (w *readonlyGateWorkspace) registerBackup(t *testing.T) {
	t.Helper()
	if stdout, stderr, code := w.run(t, "backup", "init", "file://"+w.dest); code != 0 {
		t.Fatalf("bd backup init failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

func (w *readonlyGateWorkspace) state(t *testing.T) readonlyGateState {
	t.Helper()
	// Count the issues first: that is itself a strict --readonly read, so
	// anything it creates lazily exists before the snapshot instead of showing
	// up in it as a change.
	s := readonlyGateState{issues: w.issueCount(t)}
	s.tree = snapshotReadonlyTree(t, filepath.Join(w.dir, ".beads"))
	s.dest = snapshotReadonlyTree(t, w.dest)
	if w.port != 0 {
		s.registry = w.backupRegistry(t)
	}
	return s
}

func (w *readonlyGateWorkspace) issueCount(t *testing.T) int {
	t.Helper()
	stdout, stderr, code := w.run(t, "list", "--json", "--readonly")
	if code != 0 {
		t.Fatalf("bd list failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	var issues []json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &issues); err != nil {
		t.Fatalf("bd list --json is not a JSON array: %v\n%s", err, stdout)
	}
	return len(issues)
}

// backupRegistry reads the backups the server has registered for this
// workspace's database. No bd command prints them, and the file tree never
// shows them, so a refused run is checked against the server itself.
func (w *readonlyGateWorkspace) backupRegistry(t *testing.T) string {
	t.Helper()
	db, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: w.port, User: "root", Database: w.database}.String())
	if err != nil {
		t.Fatalf("connecting to the test server: %v", err)
	}
	defer db.Close()
	rows, err := db.QueryContext(context.Background(), "SELECT name, url FROM dolt_backups ORDER BY name")
	if err != nil {
		t.Fatalf("reading dolt_backups: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var name, url string
		if err := rows.Scan(&name, &url); err != nil {
			t.Fatalf("scanning dolt_backups: %v", err)
		}
		lines = append(lines, name+"="+url)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading dolt_backups: %v", err)
	}
	return strings.Join(lines, "\n")
}

// assertUnchanged proves a refusal happened before any mutation, not merely
// that the refusal text was printed.
func (w *readonlyGateWorkspace) assertUnchanged(t *testing.T, before readonlyGateState) {
	t.Helper()
	after := w.state(t)
	if diff := readonlyTreeDiff(before.tree, after.tree); len(diff) > 0 {
		t.Errorf("a refused run changed .beads:\n  %s", strings.Join(diff, "\n  "))
	}
	if diff := readonlyTreeDiff(before.dest, after.dest); len(diff) > 0 {
		t.Errorf("a refused run changed the backup destination:\n  %s", strings.Join(diff, "\n  "))
	}
	if after.issues != before.issues {
		t.Errorf("a refused run changed the issue count: %d -> %d", before.issues, after.issues)
	}
	if after.registry != before.registry {
		t.Errorf("a refused run changed the backup registry:\nbefore:\n%s\nafter:\n%s", before.registry, after.registry)
	}
}

// readonlyTreeDiff names what differs between two snapshots, so a leaked
// mutation reads as "+ backup.lock" rather than as two opaque hash maps.
func readonlyTreeDiff(before, after readonlyTreeSnapshot) []string {
	var diff []string
	if before.Exists != after.Exists {
		diff = append(diff, fmt.Sprintf("exists: %t -> %t", before.Exists, after.Exists))
	}
	for path, was := range before.Entries {
		if now, ok := after.Entries[path]; !ok {
			diff = append(diff, "- "+path)
		} else if now != was {
			diff = append(diff, "~ "+path)
		}
	}
	for path := range after.Entries {
		if _, ok := before.Entries[path]; !ok {
			diff = append(diff, "+ "+path)
		}
	}
	sort.Strings(diff)
	return diff
}

// TestBackupAndImportBlockedInReadonlyMode is the readonly gate for the backup
// and import commands. One workspace per flavour serves every row; each row
// re-registers the backup first, so none depends on what the previous one did.
func TestBackupAndImportBlockedInReadonlyMode(t *testing.T) {
	for _, flavour := range readonlyGateFlavours {
		t.Run(flavour.name, func(t *testing.T) {
			w := flavour.setup(t)
			for _, tt := range backupImportSurfaces(w) {
				t.Run(tt.name, func(t *testing.T) {
					for _, flagSet := range readonlyGateFlagSets {
						t.Run(flagSet.name, func(t *testing.T) {
							w.registerBackup(t)
							before := w.state(t)

							args := append(append([]string{}, tt.args...), flagSet.flags...)
							stdout, stderr, code := w.run(t, args...)

							want := "Error: operation '" + tt.op + "' is not allowed in read-only mode\n"
							if code != 1 {
								t.Errorf("exit code = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
							}
							if stderr != want {
								t.Errorf("stderr is not exactly the standard refusal\ngot:  %q\nwant: %q", stderr, want)
							}
							if stdout != "" {
								t.Errorf("stdout should be empty when the command is refused, got:\n%s", stdout)
							}
							w.assertUnchanged(t, before)
						})
					}
				})
			}
		})
	}
}

// TestDoctorDiagnosisWorksInReadonlyMode pins the other half of the contract:
// the gate keys on --fix/--clean alone, so every diagnosis mode still runs
// under --readonly. Diagnosing a sandboxed workspace is the whole point.
func TestDoctorDiagnosisWorksInReadonlyMode(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	for _, args := range [][]string{
		{"doctor"},
		{"doctor", "--dry-run"},
		{"doctor", "--check=pollution"},
		{"doctor", "--check=artifacts"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			stdout, stderr, _ := runBDMigrationFreeze(t, bd, dir, append(args, "--readonly")...)
			if strings.Contains(stderr, "not allowed in read-only mode") {
				t.Errorf("bd %v was refused under --readonly but mutates nothing:\nstderr:\n%s\nstdout:\n%s", args, stderr, stdout)
			}
		})
	}
}

// TestBackupStatusAndListWorkInReadonlyMode is the control for the backup and
// import gate: it keys on the commands that write, so `backup status` and
// `list` still run under --readonly, in both flavours.
func TestBackupStatusAndListWorkInReadonlyMode(t *testing.T) {
	for _, flavour := range readonlyGateFlavours {
		t.Run(flavour.name, func(t *testing.T) {
			w := flavour.setup(t)
			for _, args := range [][]string{{"backup", "status"}, {"list"}} {
				t.Run(strings.Join(args, "_"), func(t *testing.T) {
					stdout, stderr, code := w.run(t, append(append([]string{}, args...), "--readonly")...)
					if code != 0 || strings.Contains(stderr, "not allowed in read-only mode") {
						t.Errorf("bd %v under --readonly should run (exit %d):\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
					}
				})
			}
		})
	}
}

// TestDoctorMaintenanceSkippedInReadonlyMode is the readonly twin of
// TestDoctorMaintenanceSkippedDuringMigrationFreeze — see that test for why
// plain `bd doctor` had hidden writes at all, and for the shared-server setup.
func TestDoctorMaintenanceSkippedInReadonlyMode(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)
	seedStaleLocalVersion(t, dir)

	stdout, stderr, _ := runBDMigrationFreezeWithEnv(t, bd, dir, doctorMaintenanceEnv(), "doctor", "--readonly")

	assertDoctorReportedStaleVersionWithoutHealing(t, dir, stdout, stderr)
	if strings.Contains(stderr, "not allowed in read-only mode") {
		t.Errorf("plain 'bd doctor --readonly' must diagnose, not refuse:\nstderr:\n%s", stderr)
	}
}

// TestDoctorMutationNotBlockedWithoutGates is the regression-safety control:
// with no freeze sentinel and no --readonly, the gate must be invisible. The
// fix outcome itself is not this test's business — only that neither refusal
// fires and neither exit path is the gate's.
func TestDoctorMutationNotBlockedWithoutGates(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)

	for _, tt := range doctorMutationSurfaces() {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, tt.args...)
			if code == ExitMigrationFrozen || strings.Contains(stderr, "frozen for migration") {
				t.Errorf("bd %v hit the freeze gate with no marker present (exit %d):\nstderr:\n%s", tt.args, code, stderr)
			}
			if strings.Contains(stderr, "not allowed in read-only mode") {
				t.Errorf("bd %v hit the readonly gate without --readonly (exit %d):\nstderr:\n%s", tt.args, code, stderr)
			}
			_ = stdout
		})
	}
}

// TestDoctorMaintenanceStillRunsWithoutGates is the companion control for the
// diagnosis-path change: unfrozen and not read-only, plain `bd doctor` must
// still reconcile a stale .local_version exactly as it did before #6028.
func TestDoctorMaintenanceStillRunsWithoutGates(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)
	seedStaleLocalVersion(t, dir)

	stdout, stderr, _ := runBDMigrationFreezeWithEnv(t, bd, dir, doctorMaintenanceEnv(), "doctor")

	if !strings.Contains(stderr, "auto-migrate:") {
		t.Errorf("expected autoMigrateOnVersionBump to run (an 'auto-migrate:' debug line) with no gate active, got none:\nstderr:\n%s", stderr)
	}
	got := readWorkspaceLocalVersion(t, dir)
	if got == staleLocalVersion {
		t.Errorf("%s = %q — trackBdVersion did not run with no gate active; the #6028 skip is too broad:\nstdout:\n%s",
			localVersionFile, got, stdout)
	}
}

// TestDoctorReadonlyWinsOverMigrationFreeze pins the branch order inside the
// gate: --readonly is checked first, so a workspace that is both frozen and
// sandboxed reports the readonly refusal. This mirrors CheckReadonly, which
// also answers readonly before delegating to the freeze check. Either refusal
// stops the run, but they carry different exit codes — 1 vs ExitMigrationFrozen
// — so which one wins is observable, and the precedence has to be deliberate.
func TestDoctorReadonlyWinsOverMigrationFreeze(t *testing.T) {
	bd, dir := setupMigrationFreezeWorkspace(t)
	writeFreezeMarker(t, dir, "migrator", "dolt v2 migration")
	before := doctorWorkspaceFingerprint(t, dir)

	stdout, stderr, code := runBDFrozen(t, bd, dir, "doctor", "--fix", "--yes", "--readonly")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (not %d — readonly is answered before the freeze)\nstdout:\n%s\nstderr:\n%s",
			code, ExitMigrationFrozen, stdout, stderr)
	}
	if !strings.Contains(stderr, "operation 'doctor --fix' is not allowed in read-only mode") {
		t.Errorf("stderr missing the readonly refusal (readonly must be checked before the freeze):\n%s", stderr)
	}
	if strings.Contains(stderr, "frozen for migration") {
		t.Errorf("stderr carries the freeze refusal too; the gate should report exactly one reason:\n%s", stderr)
	}
	assertDoctorRefusalIsClean(t, stdout)
	assertDoctorWorkspaceUnchanged(t, dir, before)
}
