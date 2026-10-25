package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

type advancingBackupBackend struct {
	commit          string
	backupCommit    string
	commitAfterSync string
}

func (b *advancingBackupBackend) CurrentCommit(context.Context) (string, error) {
	return b.commit, nil
}

func (b *advancingBackupBackend) BackupToDir(context.Context, string) error {
	b.backupCommit = b.commit
	b.commit = b.commitAfterSync
	return nil
}

func TestRunBackupExportWatermarkDoesNotAdvancePastSnapshot(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "forced"}[force], func(t *testing.T) {
			repo := t.TempDir()
			if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BD_BACKUP_GIT_REPO", repo)
			config.ResetForTesting()
			t.Cleanup(config.ResetForTesting)
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}

			backend := &advancingBackupBackend{commit: "head-before-sync", commitAfterSync: "head-after-sync"}
			state, err := runBackupExport(context.Background(), backend, force)
			if err != nil {
				t.Fatalf("runBackupExport: %v", err)
			}
			if backend.backupCommit != "head-before-sync" {
				t.Fatalf("backup captured %q, want head-before-sync", backend.backupCommit)
			}
			if state.LastDoltCommit != backend.backupCommit {
				t.Fatalf("watermark = %q, backup captured %q; a commit that landed during sync must remain pending", state.LastDoltCommit, backend.backupCommit)
			}
		})
	}
}
