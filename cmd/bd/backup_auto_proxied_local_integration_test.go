//go:build cgo && unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/workspacegate"
)

// autoBackupOptIn is the explicit opt-in a server-mode workspace needs, with
// the throttle collapsed so every command in a test is due.
var autoBackupOptIn = []string{"BD_BACKUP_ENABLED=true", "BD_BACKUP_INTERVAL=1ms"}

// TestManagedLocalProxiedAutoBackup is the claim this slice makes: on a
// managed-local proxied workspace, an explicit backup.enabled=true makes the
// post-command hook take a Dolt-native backup into .beads/backup, exactly as
// it does in direct mode — and that backup restores.
//
// Like the explicit-verb round trip next door, the proof is a restore, not an
// exit code: an auto-backup that wrote a manifest nobody can restore from would
// pass every other assertion here.
//
// Named TestManagedLocalProxied* so the proxied-local smoke lane runs it.
func TestManagedLocalProxiedAutoBackup(t *testing.T) {
	requireManagedLocalProxiedEnv(t)

	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "bkauto", 5*time.Minute)
	autoDir := filepath.Join(p.beadsDir, "backup")
	manifest := filepath.Join(autoDir, "manifest")

	// Server mode defaults auto-backup OFF: many clients share one server.
	// Nothing may be written without the opt-in.
	bdProxiedCreate(t, bd, p.dir, "no opt-in, no backup", "-p", "2")
	if _, err := os.Stat(manifest); err == nil {
		t.Fatalf("auto-backup ran without an opt-in: %s exists", manifest)
	}

	// With the opt-in, one ordinary write is enough.
	created := proxiedAutoBackupCreate(t, bd, p, "captured by auto-backup")
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("opted-in auto-backup produced no manifest at %s: %v", manifest, err)
	}
	state := readProxiedAutoBackupState(t, bd, p)
	if state.LastDoltCommit == "" || state.Timestamp.IsZero() {
		t.Fatalf("auto-backup did not record its watermark: %+v", state)
	}
	wantIDs := proxiedIssueIDs(t, bd, p)
	if !slices.Contains(wantIDs, created) {
		t.Fatalf("precondition: %s missing from %v", created, wantIDs)
	}

	// A read-only command with nothing new to capture must not advance the
	// watermark: change detection runs before the backup, as in direct mode.
	if _, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, autoBackupOptIn, "list", "--json"); err != nil {
		t.Fatalf("bd list with auto-backup opted in: %v\n%s", err, stderr)
	}
	if again := readProxiedAutoBackupState(t, bd, p); again.LastDoltCommit != state.LastDoltCommit {
		t.Fatalf("auto-backup advanced with no new commit: %q -> %q", state.LastDoltCommit, again.LastDoltCommit)
	}

	// `bd dolt stop` opens no provider, so its post-run hook has nothing to
	// back up — and must not relaunch the server it just stopped to find out.
	if _, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, autoBackupOptIn, "dolt", "stop"); err != nil {
		t.Fatalf("bd dolt stop: %v\n%s", err, stderr)
	}
	assertManagedProxiedQuiescent(t, p, "bd dolt stop with auto-backup opted in")

	// Diverge from the backup, then restore it and compare.
	after := bdProxiedCreate(t, bd, p.dir, "created after the auto-backup", "-p", "2")
	out, err := bdProxiedRun(t, bd, p.dir, "backup", "restore", autoDir, "--force", "--json")
	if err != nil {
		t.Fatalf("bd backup restore %s --force: %v\n%s", autoDir, err, out)
	}
	gotIDs := proxiedIssueIDs(t, bd, p)
	if slices.Contains(gotIDs, after.ID) {
		t.Fatalf("restore kept %s, created after the auto-backup; got %v", after.ID, gotIDs)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("restored issues differ from the auto-backed-up set:\n got  %v\n want %v", gotIDs, wantIDs)
	}
}

// TestManagedLocalProxiedBackupRestoreRefusedWhileAttached pins the restore
// design choice: restore REPLACES the database, so it refuses while any other
// bd command is attached to the workspace, and in that case touches nothing —
// the serving child keeps running and the data stays as it was.
//
// "Attached" is what another bd process holds for its lifetime: a SHARED
// workspace gate. The test holds exactly that, in-process, rather than racing
// a second process into position.
func TestManagedLocalProxiedBackupRestoreRefusedWhileAttached(t *testing.T) {
	requireManagedLocalProxiedEnv(t)

	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "bkatt", 5*time.Minute)

	bdProxiedCreate(t, bd, p.dir, "in the backup", "-p", "1")
	dest := filepath.Join(t.TempDir(), "dolt-backup")
	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "init", dest); err != nil {
		t.Fatalf("bd backup init: %v\n%s", err, out)
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "sync"); err != nil {
		t.Fatalf("bd backup sync: %v\n%s", err, out)
	}
	after := bdProxiedCreate(t, bd, p.dir, "not in the backup", "-p", "1")
	before := proxiedIssueIDs(t, bd, p)

	proxyPID := readManagedProxyPidFile(t, p)
	backendPID := readManagedBackendPidFile(t, p)
	if proxyPID == nil || backendPID == nil {
		t.Fatal("precondition: the managed topology should be serving")
	}

	gates, err := buildWorkspaceGateSet(p.beadsDir)
	if err != nil {
		t.Fatalf("resolve workspace gates: %v", err)
	}
	held, err := workspacegate.AcquireAll(context.Background(), workspacegate.Shared,
		workspacegate.Options{Reason: "test: attached bd client"}, gates...)
	if err != nil {
		t.Fatalf("hold the workspace gate: %v", err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			if err := held.Release(); err != nil {
				t.Errorf("release workspace gate: %v", err)
			}
		}
	}
	t.Cleanup(release)

	stdout, _, err := bdProxiedRunBuffers(t, bd, p.dir, "backup", "restore", dest, "--force", "--json")
	if err == nil {
		t.Fatalf("bd backup restore succeeded while another bd client was attached:\n%s", stdout)
	}
	var refusal struct {
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stdout[strings.Index(stdout, "{"):]), &refusal); jerr != nil {
		t.Fatalf("restore refusal is not JSON on stdout: %v\n%s", jerr, stdout)
	}
	if !strings.Contains(refusal.Error, "other bd commands are using this workspace") {
		t.Fatalf("restore refusal does not say why: %q", refusal.Error)
	}

	// Refused means untouched: the serving topology is the same processes.
	if pf := readManagedProxyPidFile(t, p); pf == nil || pf.Pid != proxyPID.Pid || !processAlive(pf.Pid) {
		t.Fatalf("refused restore disturbed the proxy: before %+v, now %+v", proxyPID, pf)
	}
	if pf := readManagedBackendPidFile(t, p); pf == nil || pf.Pid != backendPID.Pid || !processAlive(pf.Pid) {
		t.Fatalf("refused restore disturbed the dolt child: before %+v, now %+v", backendPID, pf)
	}

	release()
	if got := proxiedIssueIDs(t, bd, p); !slices.Equal(got, before) || !slices.Contains(got, after.ID) {
		t.Fatalf("refused restore changed the data:\n got  %v\n want %v", got, before)
	}

	// Once the other client detaches, the same restore goes through.
	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "restore", dest, "--force", "--json"); err != nil {
		t.Fatalf("bd backup restore after the client detached: %v\n%s", err, out)
	}
	if slices.Contains(proxiedIssueIDs(t, bd, p), after.ID) {
		t.Fatalf("restore after detach did not replace the database: %s is still here", after.ID)
	}
}

// proxiedAutoBackupCreate creates an issue with auto-backup opted in and
// returns its ID.
func proxiedAutoBackupCreate(t *testing.T, bd string, p proxiedProject, title string) string {
	t.Helper()
	stdout, stderr, err := bdProxiedRunBuffersWithEnv(t, bd, p.dir, autoBackupOptIn, "create", title, "-p", "2", "--silent")
	if err != nil {
		t.Fatalf("bd create with auto-backup opted in: %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "auto-backup") {
		t.Fatalf("auto-backup reported a problem:\n%s", stderr)
	}
	id := strings.TrimSpace(stdout)
	if id == "" {
		t.Fatalf("bd create --silent printed no ID; stderr:\n%s", stderr)
	}
	return id
}

// readProxiedAutoBackupState reads the auto-backup watermark through
// `bd backup status --json`, the surface an operator uses.
func readProxiedAutoBackupState(t *testing.T, bd string, p proxiedProject) backupState {
	t.Helper()
	out, err := bdProxiedRun(t, bd, p.dir, "backup", "status", "--json")
	if err != nil {
		t.Fatalf("bd backup status --json: %v\n%s", err, out)
	}
	start := strings.Index(string(out), "{")
	if start < 0 {
		t.Fatalf("bd backup status --json produced no JSON:\n%s", out)
	}
	var status struct {
		Backup backupState `json:"backup"`
	}
	if err := json.Unmarshal(out[start:], &status); err != nil {
		t.Fatalf("parse backup status JSON: %v\n%s", err, out)
	}
	return status.Backup
}

// proxiedIssueIDs lists every issue ID in the workspace, sorted.
func proxiedIssueIDs(t *testing.T, bd string, p proxiedProject) []string {
	t.Helper()
	var ids []string
	for _, issue := range bdProxiedListJSON(t, bd, p, "--all") {
		ids = append(ids, issue.ID)
	}
	slices.Sort(ids)
	return ids
}

// assertManagedProxiedQuiescent fails when either managed-local pidfile is
// present, i.e. something relaunched the topology.
func assertManagedProxiedQuiescent(t *testing.T, p proxiedProject, after string) {
	t.Helper()
	for _, name := range []string{proxy.PIDFileName, server.PIDFileName} {
		if _, err := os.Stat(filepath.Join(p.proxyRoot, name)); err == nil {
			t.Fatalf("after %s, %s is present: the topology was relaunched", after, name)
		}
	}
}
