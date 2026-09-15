//go:build integration && !windows

package ownershiphandoffv2

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// The fixture workspace's database is built by bd's OWN migrations, never by a
// hand-written CREATE TABLE. A fixture that creates the schema it wants is a
// fixture that can disagree with production and still go green — which is
// exactly what happened here: an invented `dependencies (from_id, to_id)` meant
// the sentinel query was only ever tested against its own invention, and every
// prepare against a real workspace died on a column that has never existed.
//
// Applying ~100 migrations takes minutes against a real sql-server, so it is
// done once for the package and the resulting data directory is copied per
// test. The copy is a plain file copy because a Dolt database on disk is just a
// directory — which is also what makes the D3 rollback diff test meaningful.

var (
	templateOnce sync.Once
	templatePath string
	templateErr  error
)

// templateDatabase is the database name every fixture serves.
const templateDatabase = "scope_db"

func TestMain(m *testing.M) { os.Exit(testMainInner(m)) }

// testMainInner holds the cleanup so it actually runs: os.Exit in TestMain's own
// body would skip every deferred call (see test/testmainconvention).
func testMainInner(m *testing.M) int {
	defer func() {
		if templatePath != "" {
			_ = os.RemoveAll(filepath.Dir(templatePath))
		}
	}()
	return m.Run()
}

// schemaTemplate returns a Dolt data directory holding one migrated, seeded
// database. Built at most once per package run.
func schemaTemplate(t *testing.T) string {
	t.Helper()
	requireDolt(t)
	templateOnce.Do(func() { templatePath, templateErr = buildSchemaTemplate() })
	if templateErr != nil {
		t.Fatalf("build the schema template: %v", templateErr)
	}
	return templatePath
}

func buildSchemaTemplate() (string, error) {
	base, err := os.MkdirTemp("", "bd-handoff-template-*")
	if err != nil {
		return "", err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	// A private dolt root keeps the template off the machine's global dolt
	// config and supplies the identity `dolt init` refuses to run without.
	doltRoot := filepath.Join(base, "doltroot")
	if err := os.MkdirAll(filepath.Join(doltRoot, ".dolt"), 0o755); err != nil {
		return "", err
	}
	globalCfg := `{"user.name":"bd handoff test","user.email":"handoff@example.invalid"}`
	if err := os.WriteFile(filepath.Join(doltRoot, ".dolt", "config_global.json"), []byte(globalCfg), 0o600); err != nil {
		return "", err
	}

	dataDir := filepath.Join(base, "dolt")
	dbDir := filepath.Join(dataDir, templateDatabase)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return "", err
	}
	initCmd := exec.Command("dolt", "init", "--name", "bd handoff test", "--email", "handoff@example.invalid")
	initCmd.Dir = dbDir
	initCmd.Env = append(os.Environ(), "DOLT_ROOT_PATH="+doltRoot)
	if out, err := initCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("dolt init: %w\n%s", err, out)
	}

	port, err := freeLoopbackPort("127.0.0.1")
	if err != nil {
		return "", err
	}
	logPath := filepath.Join(base, "template-server.log")
	log, err := os.Create(logPath) //nolint:gosec // test artifact
	if err != nil {
		return "", err
	}
	defer log.Close() //nolint:errcheck // test artifact
	server := exec.Command("dolt", "sql-server",
		"--host", "127.0.0.1", "--port", strconv.Itoa(port), "--data-dir", dataDir)
	server.Dir = dataDir
	server.Env = append(os.Environ(), "DOLT_ROOT_PATH="+doltRoot)
	server.Stdout, server.Stderr = log, log
	server.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := server.Start(); err != nil {
		return "", fmt.Errorf("start template server: %w", err)
	}
	stopped := false
	stop := func() {
		if stopped || server.Process == nil {
			return
		}
		stopped = true
		_ = syscall.Kill(-server.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _, _ = server.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			_ = syscall.Kill(-server.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}
	defer stop()

	deadline := time.Now().Add(120 * time.Second)
	for !handshakes("127.0.0.1", port) {
		if time.Now().After(deadline) {
			contents, _ := os.ReadFile(logPath) //nolint:gosec,errcheck // diagnostic
			return "", fmt.Errorf("template server never greeted on port %d\n%s", port, contents)
		}
		time.Sleep(250 * time.Millisecond)
	}

	if err := migrateAndSeed(port); err != nil {
		return "", err
	}
	// The data dir must be quiescent before it is copied: a half-flushed store
	// copies as a corrupt one.
	stop()
	return dataDir, nil
}

func migrateAndSeed(port int) error {
	db, err := openScope("127.0.0.1", port, templateDatabase)
	if err != nil {
		return fmt.Errorf("connect to the template server: %w", err)
	}
	defer db.Close() //nolint:errcheck // test cleanup

	// Migrations and seeding get separate budgets, and the migration's pinned
	// connection is RELEASED before seeding starts. openScope caps the pool at
	// one connection — MigrateUpWithLock needs a pinned one because its
	// GET_LOCK lives on that connection — so holding it across the inserts
	// deadlocks the seed against the migration that already finished.
	if err := runMigrations(db); err != nil {
		return err
	}

	// A row in each sentinel table, through the real columns: `issues.id`, and
	// `dependencies.issue_id` with the split target the schema actually has.
	stmts := []string{
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) " +
			"VALUES ('bd-0001', 'first', '', '', '', '', 'open', 2, 'task', NOW(), NOW())",
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at) " +
			"VALUES ('bd-0002', 'second', '', '', '', '', 'open', 2, 'task', NOW(), NOW())",
		"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) " +
			"VALUES ('11111111-2222-3333-4444-555555555555', 'bd-0001', 'bd-0002', 'blocks', 'handoff-test')",
		"CALL DOLT_COMMIT('-Am', 'seed the scope', '--author', 'bd handoff test <handoff@example.invalid>')",
	}
	for _, stmt := range stmts {
		stmtCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		_, execErr := db.ExecContext(stmtCtx, stmt)
		cancel()
		if execErr != nil {
			return fmt.Errorf("seed %q: %w", stmt, execErr)
		}
	}
	return nil
}

// runMigrations applies bd's schema to the template, on a pinned connection it
// gives back before returning.
func runMigrations(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin connection: %w", err)
	}
	defer conn.Close() //nolint:errcheck // test cleanup
	if _, err := schema.MigrateUpWithLock(ctx, conn, templateDatabase); err != nil {
		return fmt.Errorf("apply bd's schema migrations: %w", err)
	}
	return nil
}

// copyTree copies src's contents into dst, creating dst. Plain file copy: a Dolt
// database on disk is a directory, which is the same property the rollback diff
// test relies on.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			link, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			return os.Symlink(link, target)
		case !info.Mode().IsRegular():
			return nil // sockets and the like are not part of a database
		}
		in, openErr := os.Open(path) //nolint:gosec // walked path under the template
		if openErr != nil {
			return openErr
		}
		defer in.Close() //nolint:errcheck // read-only
		out, createErr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if createErr != nil {
			return createErr
		}
		if _, copyErr := io.Copy(out, in); copyErr != nil {
			_ = out.Close()
			return copyErr
		}
		return out.Close()
	})
	if err != nil {
		t.Fatalf("copy %s to %s: %v", src, dst, err)
	}
}
