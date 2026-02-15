package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckPendingMigrations(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		wantStatus     string
		wantMessage    string
		wantMigrations int
	}{
		{
			name:        "no beads directory",
			setup:       func(t *testing.T, dir string) {},
			wantStatus:  StatusOK,
			wantMessage: "None required",
		},
		{
			name: "empty beads directory",
			setup: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0755); err != nil {
					t.Fatalf("failed to create .beads: %v", err)
				}
			},
			wantStatus:  StatusOK,
			wantMessage: "None required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "bd-doctor-migration-*")
			if err != nil {
				t.Fatalf("failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			tt.setup(t, tmpDir)

			check := CheckPendingMigrations(tmpDir)

			if check.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", check.Status, tt.wantStatus)
			}

			if tt.wantMessage != "" && check.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", check.Message, tt.wantMessage)
			}

			if check.Category != CategoryMaintenance {
				t.Errorf("category = %q, want %q", check.Category, CategoryMaintenance)
			}
		})
	}
}

func TestDetectPendingMigrations(t *testing.T) {
	t.Run("no beads directory returns empty", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "bd-doctor-migration-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		migrations := DetectPendingMigrations(tmpDir)
		if len(migrations) != 0 {
			t.Errorf("expected 0 migrations, got %d", len(migrations))
		}
	})

	t.Run("empty beads directory returns empty", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "bd-doctor-migration-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0755); err != nil {
			t.Fatalf("failed to create .beads: %v", err)
		}

		migrations := DetectPendingMigrations(tmpDir)
		if len(migrations) != 0 {
			t.Errorf("expected 0 migrations, got %d", len(migrations))
		}
	})

}
