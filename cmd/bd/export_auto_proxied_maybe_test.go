package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// sqlmockAutoExportProvider is a proxied provider whose maintenance seam runs
// on a sqlmock connection, so maybeAutoExport's proxied backend executes its
// real versioning SQL against scripted answers. Its read unit of work never
// opens: NewUOW counts the attempt and returns newUOWErr.
type sqlmockAutoExportProvider struct {
	db        *sql.DB
	newUOWErr error
	newUOWs   int
}

func (p *sqlmockAutoExportProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	p.newUOWs++
	if p.newUOWErr == nil {
		return nil, errors.New("sqlmockAutoExportProvider: no read unit of work in this test")
	}
	return nil, p.newUOWErr
}

func (p *sqlmockAutoExportProvider) Close(context.Context) error { return nil }

func (p *sqlmockAutoExportProvider) RunNonTx(ctx context.Context, fn func(context.Context, *sql.Conn) error) error {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return fn(ctx, conn)
}

// proxiedAutoExportUnit is a proxied-server command's post-run state with
// export.auto on: a workspace holding an existing issues.jsonl, no classic
// store, and a sqlmock-backed provider. It returns the beads dir, the
// provider and its mock.
func proxiedAutoExportUnit(t *testing.T, interval string) (string, *sqlmockAutoExportProvider, sqlmock.Sqlmock) {
	t.Helper()
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"beads","backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"_type":"issue","id":"pxu-1","title":"existing"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BD_GIT_HOOK", "")
	initConfigForTest(t)
	config.Set("export.auto", true)
	config.Set("export.interval", interval)

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	oldStore, oldProvider := store, uowProvider
	oldProxied := proxiedServerMode
	t.Cleanup(func() {
		store, uowProvider = oldStore, oldProvider
		proxiedServerMode = oldProxied
	})
	provider := &sqlmockAutoExportProvider{db: db}
	store = nil
	uowProvider = provider
	proxiedServerMode = true
	return beadsDir, provider, mock
}

// readAutoExportFiles returns issues.jsonl and export-state.json ("" when
// absent).
func readAutoExportFiles(t *testing.T, beadsDir string) (jsonl, state string) {
	t.Helper()
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(beadsDir, name))
		if os.IsNotExist(err) {
			return ""
		}
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}
	return read("issues.jsonl"), read(exportAutoStateFile)
}

// TestMaybeAutoExportProxiedBackendSkipsUnchangedOrThrottled pins proxied
// change detection: an unchanged working-set hash, or a changed one inside
// export.interval, ends the run before any read unit of work opens, leaving
// the JSONL and export state exactly as they were.
//
// Cannot be parallel: mutates package globals and the process config.
func TestMaybeAutoExportProxiedBackendSkipsUnchangedOrThrottled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval string
		state    exportAutoState
		hash     string
	}{
		{name: "unchanged state hash", interval: "1ms", state: exportAutoState{LastDoltCommit: "hashsame"}, hash: "hashsame"},
		{name: "changed hash inside the interval", interval: "1h", state: exportAutoState{LastDoltCommit: "hashold", Timestamp: time.Now()}, hash: "hashnew"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, provider, mock := proxiedAutoExportUnit(t, tc.interval)
			state := tc.state
			saveExportAutoState(beadsDir, &state)
			mock.ExpectQuery("SELECT DOLT_HASHOF_DB()").WillReturnRows(sqlmock.NewRows([]string{"h"}).AddRow(tc.hash))
			wantJSONL, wantState := readAutoExportFiles(t, beadsDir)

			var err error
			stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
			if err != nil {
				t.Fatalf("maybeAutoExport: %v", err)
			}
			if provider.newUOWs != 0 {
				t.Fatalf("opened %d read unit(s) of work; nothing is due, so nothing may be read", provider.newUOWs)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("state hash never read through the maintenance seam: %v", err)
			}
			if gotJSONL, gotState := readAutoExportFiles(t, beadsDir); gotJSONL != wantJSONL || gotState != wantState {
				t.Fatalf("files changed:\njsonl %q -> %q\nstate %q -> %q", wantJSONL, gotJSONL, wantState, gotState)
			}
			if strings.TrimSpace(stderr) != "" {
				t.Fatalf("unexpected stderr: %q", stderr)
			}
		})
	}
}

// TestMaybeAutoExportProxiedBackendReadFailureWarns pins proxied failure
// handling to direct mode's: a versioning read or the snapshot read failing
// mid-export prints `Warning: auto-export skipped: …`, returns nil (the
// command still exits 0), and leaves the JSONL and export state untouched.
//
// Cannot be parallel: mutates package globals and the process config.
func TestMaybeAutoExportProxiedBackendReadFailureWarns(t *testing.T) {
	for _, tc := range []struct {
		name        string
		script      func(sqlmock.Sqlmock)
		newUOWErr   error
		wantNewUOWs int
		wantWarning string
	}{
		{
			name: "state hash unreadable",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery("SELECT DOLT_HASHOF_DB()").WillReturnError(errors.New("connection refused"))
				m.ExpectQuery("SELECT DOLT_HASHOF('HEAD')").WillReturnError(errors.New("connection refused"))
			},
			wantWarning: "Warning: auto-export skipped: failed to get current commit",
		},
		{
			name: "snapshot read fails",
			script: func(m sqlmock.Sqlmock) {
				m.ExpectQuery("SELECT DOLT_HASHOF_DB()").WillReturnRows(sqlmock.NewRows([]string{"h"}).AddRow("hashnew"))
			},
			newUOWErr:   errors.New("begin read: connection reset by peer"),
			wantNewUOWs: 1,
			wantWarning: "Warning: auto-export skipped: begin read: connection reset by peer",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, provider, mock := proxiedAutoExportUnit(t, "1ms")
			provider.newUOWErr = tc.newUOWErr
			tc.script(mock)
			wantJSONL, wantState := readAutoExportFiles(t, beadsDir)

			var err error
			stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
			if err != nil {
				t.Fatalf("maybeAutoExport returned %v; a read failure must not fail the command", err)
			}
			if !strings.Contains(stderr, tc.wantWarning) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr, tc.wantWarning)
			}
			if provider.newUOWs != tc.wantNewUOWs {
				t.Fatalf("read units of work opened = %d, want %d", provider.newUOWs, tc.wantNewUOWs)
			}
			if gotJSONL, gotState := readAutoExportFiles(t, beadsDir); gotJSONL != wantJSONL || gotState != wantState {
				t.Fatalf("files changed:\njsonl %q -> %q\nstate %q -> %q", wantJSONL, gotJSONL, wantState, gotState)
			}
		})
	}
}

// TestMaybeAutoExportProxiedBackendNeedsTheOpenProvider pins that proxied
// auto-export only reads through the provider the command opened: with none
// open, or one without the maintenance seam, it opens nothing of its own,
// prints nothing and writes nothing.
//
// Cannot be parallel: mutates package globals and the process config.
func TestMaybeAutoExportProxiedBackendNeedsTheOpenProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider func() uow.UnitOfWorkProvider
	}{
		{name: "no provider open", provider: func() uow.UnitOfWorkProvider { return nil }},
		{name: "provider without the maintenance seam", provider: func() uow.UnitOfWorkProvider {
			var events []string
			return &closeRecordingProvider{events: &events}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, _, mock := proxiedAutoExportUnit(t, "1ms")
			uowProvider = tc.provider()
			wantJSONL, wantState := readAutoExportFiles(t, beadsDir)

			var err error
			stderr := captureStderr(t, func() { err = maybeAutoExport(context.Background(), false) })
			if err != nil {
				t.Fatalf("maybeAutoExport: %v", err)
			}
			if strings.TrimSpace(stderr) != "" {
				t.Fatalf("unexpected stderr: %q", stderr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unexpected SQL: %v", err)
			}
			if gotJSONL, gotState := readAutoExportFiles(t, beadsDir); gotJSONL != wantJSONL || gotState != wantState {
				t.Fatalf("files changed:\njsonl %q -> %q\nstate %q -> %q", wantJSONL, gotJSONL, wantState, gotState)
			}
		})
	}
}
