//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
)

// TestDoctorFixFreshCloneLeavesTheServerAtThePortUnchanged runs bd doctor
// --fix --yes's Fresh Clone check and repair against two servers: the
// workspace's database, holding a row newer than the tracked issues.jsonl, on
// the Dolt server at the resolved port, and beside it a live unix socket that
// metadata.json names (as bd dolt set socket writes it) on a server that
// refuses the login, in a fresh clone (no .beads/dolt). The repair imports
// issues.jsonl through the library store open, which dials TCP at the port
// and never metadata.json's socket. The check dialed the socket, reported
// the server unreachable ("not a fresh clone"), and --fix imported the
// tracked copy over the live row at the port all the same (#7874 review).
// The check reads the server the repair writes and finds the database there,
// and --fix, which imports only a fresh clone the check found
// (doctor.FreshCloneDetected), changes nothing.
func TestDoctorFixFreshCloneLeavesTheServerAtThePortUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	store := newTestStoreIsolatedDB(t, filepath.Join(beadsDir, "dolt"), "tp")
	ctx := context.Background()
	issue := &types.Issue{Title: "tracked title", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatal(err)
	}
	tracked, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	// The live row moves on past the tracked copy.
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"title": "live title"}, "test"); err != nil {
		t.Fatal(err)
	}

	socketServer := newFakeMySQLLoginOn(t, "unix", diagnosticSocketPath(t))
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata: %v", err)
	}
	cfg.DoltServerSocket = socketServer.ln.Addr().String()
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(beadsDir, "dolt")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", "")

	check := convertDoctorCheck(doctor.CheckFreshClone(repo))
	origYes := doctorYes
	t.Cleanup(func() { doctorYes = origYes })
	doctorYes = true
	out := captureStdout(t, func() error {
		applyFixes(doctorResult{Path: repo, Checks: []doctorCheck{check}}, doctor.AssessSchemaFixGate(repo))
		return nil
	})

	got, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "live title" {
		t.Errorf("bd doctor --fix overwrote the live row at the port: title %q, want %q\nFresh Clone: %s %s\n%s", got.Title, "live title", check.Status, check.Message, out)
	}
	if check.Status != statusOK || !strings.Contains(check.Message, "Database exists") {
		t.Errorf("Fresh Clone = %s %q, want the database found on the server its repair writes", check.Status, check.Message)
	}
}

// TestDoctorFixDatabaseRepairLeavesLiveServerRowsUnchanged runs bd doctor
// --fix --yes's Database repair (fix.DatabaseVersionWithBdVersion), through
// the schema gate, in a server-mode workspace with no .beads/dolt, as in every
// fresh clone and on every other machine, whose database on the Dolt server
// holds a row newer than the tracked issues.jsonl. Two Database verdicts reach
// the repair there: the check read the database and found it last stamped by
// another bd version, or the check could not open the database while the
// repair's own open can. The repair took the missing directory for a missing
// database and imported the tracked copy over the live row in both. It leaves
// the row as it is.
func TestDoctorFixDatabaseRepairLeavesLiveServerRowsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(repo string) doctorCheck
	}{
		{name: "the check read a database another bd version stamped", check: func(repo string) doctorCheck {
			shared := doctor.NewSharedStore(repo)
			defer shared.Close()
			return convertDoctorCheck(doctor.CheckDatabaseVersionWithStore(shared, Version))
		}},
		{name: "the check could not open the database", check: func(string) doctorCheck {
			return doctorCheck{Name: "Database", Status: statusError, Message: "Unable to open database",
				Fix: "Run 'bd doctor --fix' to attempt repair. Check 'bd dolt status' for server configuration issues"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			beadsDir := filepath.Join(repo, ".beads")
			store := newTestStoreIsolatedDB(t, filepath.Join(beadsDir, "dolt"), "tp")
			ctx := context.Background()
			issue := &types.Issue{Title: "tracked title", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := store.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatal(err)
			}
			tracked, err := store.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Fatal(err)
			}
			line, err := json.Marshal(tracked)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), append(line, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			// The live row moves on past the tracked copy, and another bd
			// version stamped the database last.
			if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"title": "live title"}, "test"); err != nil {
				t.Fatal(err)
			}
			if err := store.SetLocalMetadata(ctx, "bd_version", "0.0.1"); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(beadsDir, "dolt")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BEADS_DIR", beadsDir)

			check := tc.check(repo)
			if check.Status == statusOK || check.Fix == "" {
				t.Fatalf("Database = %s %q (fix %q), want a verdict bd doctor --fix repairs", check.Status, check.Message, check.Fix)
			}
			gate := doctor.AssessSchemaFixGate(repo)
			if !gate.AllowsFix("Database") {
				t.Fatalf("the schema gate withholds the Database repair: %s", gate.Reason)
			}
			origYes := doctorYes
			t.Cleanup(func() { doctorYes = origYes })
			doctorYes = true
			out := captureStdout(t, func() error {
				applyFixes(doctorResult{Path: repo, Checks: []doctorCheck{check}}, gate)
				return nil
			})

			got, err := store.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != "live title" {
				t.Errorf("bd doctor --fix imported issues.jsonl over the live row: title %q, want %q\nDatabase: %s %q\n%s", got.Title, "live title", check.Status, check.Message, out)
			}
			if !strings.Contains(out, "Fix summary: 1 fixed, 0 skipped, 0 errors") {
				t.Errorf("bd doctor --fix does not report the Database repair fixed:\n%s", out)
			}
		})
	}
}

// TestDoctorDependencyChecksReadTheServerTheirRepairsWrite runs bd doctor's
// Orphaned Dependencies, Child-Parent Dependencies, Dependency Keys and
// Blocked State checks against two servers: the workspace's database,
// holding a problem for each, on the Dolt server at the resolved port, where
// their bd doctor --fix repairs write (openFixDB), and beside it a live
// BEADS_DOLT_SERVER_SOCKET on a server that refuses the login, which the
// store dials socket first. The checks read the store's server, so they
// reported no database while their repairs would rewrite the rows at the
// port; with a database on the socket they reported its rows instead
// (#7874 review). Each reports the problem the server at the port holds.
func TestDoctorDependencyChecksReadTheServerTheirRepairsWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	repo, beadsDir := dependencyProblemsWorkspace(t)

	socketServer := newFakeMySQLLoginOn(t, "unix", diagnosticSocketPath(t))
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", socketServer.ln.Addr().String())
	shared := doctor.NewSharedStore(repo)
	defer shared.Close()
	if shared.Store() != nil {
		t.Fatalf("the shared store opened a database; it dials the socket %s, whose server refuses every login", socketServer.ln.Addr())
	}

	for _, check := range []doctor.DoctorCheck{
		doctor.CheckOrphanedDependencies(repo, shared),
		doctor.CheckChildParentDependencies(repo, shared),
		doctor.CheckDependencyKeysWithStore(repo, shared),
		doctor.CheckBlockedConsistencyWithStore(repo, shared),
	} {
		if check.Status != doctor.StatusWarning || strings.HasPrefix(check.Message, "Unable") {
			t.Errorf("%s = %s %q (%s), want the problem the database at the port holds, where its repair writes", check.Name, check.Status, check.Message, check.Detail)
		}
	}
}

// TestDoctorWarnsOnDependencyScansItCannotReadBesideALiveSocket runs bd
// doctor and bd doctor --check=validate on a workspace whose database, holding
// an orphaned dependency and a child→parent edge, the store reads through a
// live BEADS_DOLT_SERVER_SOCKET while nothing answers at the resolved port,
// the server the Orphaned Dependencies and Child-Parent Dependencies checks
// read because their repairs write it. They reported "N/A (no database)", so
// bd doctor dropped both problems and validate printed "All data-integrity
// checks passed" over the orphan (#7874 review). Each warns, naming the
// server it could not read, and validate fails.
func TestDoctorWarnsOnDependencyScansItCannotReadBesideALiveSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	repo, beadsDir := dependencyProblemsWorkspace(t)
	socket, _ := serveUnixProxy(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(testDoltServerPort)))
	port := strconv.Itoa(freeLocalPort(t))
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", socket)
	t.Setenv("BEADS_DOLT_SERVER_PORT", port)
	shared := doctor.NewSharedStore(repo)
	opened := shared.Store() != nil
	shared.Close()
	if !opened {
		t.Fatalf("the shared store did not open the database through the socket %s", socket)
	}

	result := runDiagnostics(repo)
	for _, name := range []string{"Orphaned Dependencies", "Child-Parent Dependencies"} {
		i := slices.IndexFunc(result.Checks, func(c doctorCheck) bool { return c.Name == name })
		if i < 0 {
			t.Errorf("bd doctor ran no %s check", name)
			continue
		}
		if c := result.Checks[i]; c.Status != statusWarning || !strings.Contains(c.Detail, "127.0.0.1:"+port) {
			t.Errorf("bd doctor %s = %s %q (%s), want a warning naming 127.0.0.1:%s, the server its repair writes", name, c.Status, c.Message, c.Detail, port)
		}
	}

	var passed bool
	out := captureStdout(t, func() error {
		var err error
		passed, err = runValidateCheckInner(repo)
		return err
	})
	if passed || strings.Contains(out, "All data-integrity checks passed") {
		t.Errorf("bd doctor --check=validate passed a database whose orphaned dependency it never read:\n%s", out)
	}
}

// dependencyProblemsWorkspace is a server-mode workspace whose database, on
// the Dolt test server, holds a problem for each of bd doctor's Child-Parent
// Dependencies, Dependency Keys, Orphaned Dependencies and Blocked State
// checks, and its .beads directory.
func dependencyProblemsWorkspace(t *testing.T) (repo, beadsDir string) {
	t.Helper()
	repo = t.TempDir()
	beadsDir = filepath.Join(repo, ".beads")
	store := newTestStoreIsolatedDB(t, filepath.Join(beadsDir, "dolt"), "dk")
	ctx := context.Background()
	parent := &types.Issue{Title: "parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic}
	stale := &types.Issue{Title: "nothing blocks this", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	for _, issue := range []*types.Issue{parent, stale} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}
	// A child blocking on its own parent (Child-Parent Dependencies), on a
	// randomly keyed row (Dependency Keys); a dependency on a missing issue
	// (Orphaned Dependencies); and an is_blocked flag nothing explains
	// (Blocked State).
	tx, err := store.UnderlyingDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	childID := parent.ID + ".1"
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{"SET FOREIGN_KEY_CHECKS = 0", nil},
		{`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at)
		  VALUES (?, 'child', '', '', '', '', 'open', 2, 'task', NOW(), NOW())`, []any{childID}},
		{"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, 'blocks', NOW(), 'test')", []any{childID, parent.ID}},
		{"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, 'blocks', NOW(), 'test')", []any{parent.ID, "dk-missing"}},
		{"UPDATE issues SET is_blocked = 1 WHERE id = ?", []any{stale.ID}},
	} {
		if _, err := tx.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			_ = tx.Rollback()
			t.Fatalf("%s: %v", stmt.query, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return repo, beadsDir
}
