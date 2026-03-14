package dolt

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/beads/internal/storage"
)

func TestWrapDBError(t *testing.T) {
	t.Run("nil error returns nil", func(t *testing.T) {
		if err := wrapDBError("op", nil); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("sql.ErrNoRows converts to storage.ErrNotFound", func(t *testing.T) {
		err := wrapDBError("get issue", sql.ErrNoRows)
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
		if err.Error() != "get issue: not found" {
			t.Errorf("unexpected message: %s", err.Error())
		}
	})

	t.Run("other errors are wrapped with context", func(t *testing.T) {
		original := fmt.Errorf("connection refused")
		err := wrapDBError("query users", original)
		if !errors.Is(err, original) {
			t.Errorf("expected to wrap original error")
		}
		if err.Error() != "query users: connection refused" {
			t.Errorf("unexpected message: %s", err.Error())
		}
	})
}

func TestWrapTransactionError(t *testing.T) {
	t.Run("nil error returns nil", func(t *testing.T) {
		if err := wrapTransactionError("begin", nil); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("wraps with ErrTransaction sentinel", func(t *testing.T) {
		original := fmt.Errorf("connection reset")
		err := wrapTransactionError("begin tx", original)
		if !errors.Is(err, ErrTransaction) {
			t.Errorf("expected ErrTransaction in chain")
		}
		if !errors.Is(err, original) {
			t.Errorf("expected original error in chain")
		}
	})
}

func TestWrapScanError(t *testing.T) {
	t.Run("wraps with ErrScan sentinel", func(t *testing.T) {
		original := fmt.Errorf("invalid column type")
		err := wrapScanError("scan issue", original)
		if !errors.Is(err, ErrScan) {
			t.Errorf("expected ErrScan in chain")
		}
		if !errors.Is(err, original) {
			t.Errorf("expected original error in chain")
		}
	})
}

func TestWrapQueryError(t *testing.T) {
	t.Run("wraps with ErrQuery sentinel", func(t *testing.T) {
		original := fmt.Errorf("syntax error")
		err := wrapQueryError("search issues", original)
		if !errors.Is(err, ErrQuery) {
			t.Errorf("expected ErrQuery in chain")
		}
	})
}

func TestWrapExecError(t *testing.T) {
	t.Run("wraps with ErrExec sentinel", func(t *testing.T) {
		original := fmt.Errorf("duplicate key")
		err := wrapExecError("insert issue", original)
		if !errors.Is(err, ErrExec) {
			t.Errorf("expected ErrExec in chain")
		}
	})
}

func TestDatabaseNotFoundHint(t *testing.T) {
	baseCfg := Config{
		Database:   "beads_test",
		ServerHost: "127.0.0.1",
		ServerPort: 3309,
	}

	t.Run("hint suggests setting sync.git-remote when empty", func(t *testing.T) {
		cfg := baseCfg // SyncGitRemote is empty by default
		err := databaseNotFoundError(&cfg)

		msg := err.Error()

		// FR-001: Must contain the setup hint (line-wrapped in output)
		if !strings.Contains(msg, "set sync.git-remote") {
			t.Errorf("expected hint to set sync.git-remote, got:\n%s", msg)
		}
		if !strings.Contains(msg, ".beads/config.yaml") {
			t.Errorf("expected .beads/config.yaml reference, got:\n%s", msg)
		}

		// Must still contain the original error context
		if !strings.Contains(msg, `"beads_test"`) {
			t.Errorf("expected database name in error, got:\n%s", msg)
		}
		if !strings.Contains(msg, "127.0.0.1:3309") {
			t.Errorf("expected server address in error, got:\n%s", msg)
		}

		// Must contain existing suggestions
		if !strings.Contains(msg, "bd init") {
			t.Errorf("expected bd init suggestion, got:\n%s", msg)
		}
		if !strings.Contains(msg, "bd doctor") {
			t.Errorf("expected bd doctor suggestion, got:\n%s", msg)
		}
	})

	t.Run("hint mentions configured sync.git-remote when set", func(t *testing.T) {
		cfg := baseCfg
		cfg.SyncGitRemote = "https://doltremoteapi.dolthub.com/myorg/beads"
		err := databaseNotFoundError(&cfg)

		msg := err.Error()

		// FR-002: Must mention it's configured and show the URL
		if !strings.Contains(msg, "sync.git-remote is configured") {
			t.Errorf("expected configured hint, got:\n%s", msg)
		}
		if !strings.Contains(msg, "https://doltremoteapi.dolthub.com/myorg/beads") {
			t.Errorf("expected remote URL in hint, got:\n%s", msg)
		}
		if !strings.Contains(msg, "bd init") {
			t.Errorf("expected bd init suggestion, got:\n%s", msg)
		}
	})

	t.Run("hint detects backup files in beads dir (GH#2327)", func(t *testing.T) {
		// Create a temp .beads/backup/ with a JSONL file
		tmpDir := t.TempDir()
		backupDir := filepath.Join(tmpDir, "backup")
		if err := os.MkdirAll(backupDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backupDir, "issues.jsonl"), []byte(`{"id":"x"}`), 0600); err != nil {
			t.Fatal(err)
		}

		cfg := baseCfg
		cfg.BeadsDir = tmpDir
		err := databaseNotFoundError(&cfg)
		msg := err.Error()

		if !strings.Contains(msg, "Backup files found") {
			t.Errorf("expected backup detection hint, got:\n%s", msg)
		}
		if !strings.Contains(msg, "bd backup restore") {
			t.Errorf("expected bd backup restore suggestion, got:\n%s", msg)
		}
		// Should still mention branch switching as a common cause
		if !strings.Contains(msg, "branch") {
			t.Errorf("expected branch-switch mention, got:\n%s", msg)
		}
	})

	t.Run("no backup hint when no backup files exist", func(t *testing.T) {
		tmpDir := t.TempDir()

		cfg := baseCfg
		cfg.BeadsDir = tmpDir
		err := databaseNotFoundError(&cfg)
		msg := err.Error()

		if strings.Contains(msg, "Backup files found") {
			t.Errorf("should not mention backups when none exist, got:\n%s", msg)
		}
	})
}

func TestHasBackupFiles(t *testing.T) {
	t.Run("returns false for empty beadsDir", func(t *testing.T) {
		if HasBackupFiles("") {
			t.Error("expected false for empty beadsDir")
		}
	})

	t.Run("returns false when backup dir does not exist", func(t *testing.T) {
		if HasBackupFiles(t.TempDir()) {
			t.Error("expected false when backup dir missing")
		}
	})

	t.Run("returns false when backup dir is empty", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(tmpDir, "backup"), 0700); err != nil {
			t.Fatal(err)
		}
		if HasBackupFiles(tmpDir) {
			t.Error("expected false when backup dir is empty")
		}
	})

	t.Run("returns false when backup dir has non-jsonl files", func(t *testing.T) {
		tmpDir := t.TempDir()
		backupDir := filepath.Join(tmpDir, "backup")
		if err := os.MkdirAll(backupDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backupDir, "state.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if HasBackupFiles(tmpDir) {
			t.Error("expected false when only non-jsonl files present")
		}
	})

	t.Run("returns true when backup dir has jsonl files", func(t *testing.T) {
		tmpDir := t.TempDir()
		backupDir := filepath.Join(tmpDir, "backup")
		if err := os.MkdirAll(backupDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backupDir, "issues.jsonl"), []byte(`{"id":"x"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if !HasBackupFiles(tmpDir) {
			t.Error("expected true when jsonl files present")
		}
	})
}
