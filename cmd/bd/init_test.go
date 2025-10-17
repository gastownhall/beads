package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
)

func TestInitCommand(t *testing.T) {
	tests := []struct {
		name           string
		prefix         string
		quiet          bool
		wantOutputText string
		wantNoOutput   bool
	}{
		{
			name:           "init with default prefix",
			prefix:         "",
			quiet:          false,
			wantOutputText: "bd initialized successfully",
		},
		{
			name:           "init with custom prefix",
			prefix:         "myproject",
			quiet:          false,
			wantOutputText: "myproject-1, myproject-2",
		},
		{
			name:         "init with quiet flag",
			prefix:       "test",
			quiet:        true,
			wantNoOutput: true,
		},
		{
			name:           "init with prefix ending in hyphen",
			prefix:         "test-",
			quiet:          false,
			wantOutputText: "test-1, test-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset Cobra command state
			rootCmd.SetArgs([]string{})
			initCmd.Flags().Set("prefix", "")
			initCmd.Flags().Set("quiet", "false")
			
			tmpDir := t.TempDir()
			originalWd, err := os.Getwd()
			if err != nil {
				t.Fatalf("Failed to get working directory: %v", err)
			}
			defer os.Chdir(originalWd)

			if err := os.Chdir(tmpDir); err != nil {
				t.Fatalf("Failed to change to temp directory: %v", err)
			}

			// Capture output
			var buf bytes.Buffer
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w
			defer func() {
				os.Stdout = oldStdout
			}()

			// Build command arguments
			args := []string{"init"}
			if tt.prefix != "" {
				args = append(args, "--prefix", tt.prefix)
			}
			if tt.quiet {
				args = append(args, "--quiet")
			}
			
			rootCmd.SetArgs(args)

			// Run command
			err = rootCmd.Execute()
			
			// Restore stdout and read output
			w.Close()
			buf.ReadFrom(r)
			os.Stdout = oldStdout
			output := buf.String()

			if err != nil {
				t.Fatalf("init command failed: %v", err)
			}

			// Check output
			if tt.wantNoOutput {
				if output != "" {
					t.Errorf("Expected no output with --quiet, got: %s", output)
				}
			} else if tt.wantOutputText != "" {
				if !strings.Contains(output, tt.wantOutputText) {
					t.Errorf("Expected output to contain %q, got: %s", tt.wantOutputText, output)
				}
			}

			// Verify .beads directory was created
			beadsDir := filepath.Join(tmpDir, ".beads")
			if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
				t.Error(".beads directory was not created")
			}

			// Verify database was created
			var dbPath string
			if tt.prefix != "" {
				expectedPrefix := strings.TrimRight(tt.prefix, "-")
				dbPath = filepath.Join(beadsDir, expectedPrefix+".db")
			} else {
				// Should use directory name as prefix
				dirName := filepath.Base(tmpDir)
				dbPath = filepath.Join(beadsDir, dirName+".db")
			}

			if _, err := os.Stat(dbPath); os.IsNotExist(err) {
				t.Errorf("Database file was not created at %s", dbPath)
			}

			// Verify database has correct prefix
			store, err := sqlite.New(dbPath)
			if err != nil {
				t.Fatalf("Failed to open created database: %v", err)
			}
			defer store.Close()

			ctx := context.Background()
			prefix, err := store.GetConfig(ctx, "issue_prefix")
			if err != nil {
				t.Fatalf("Failed to get issue prefix from database: %v", err)
			}

			expectedPrefix := tt.prefix
			if expectedPrefix == "" {
				expectedPrefix = filepath.Base(tmpDir)
			} else {
				expectedPrefix = strings.TrimRight(expectedPrefix, "-")
			}

			if prefix != expectedPrefix {
				t.Errorf("Expected prefix %q, got %q", expectedPrefix, prefix)
			}

			// Verify version metadata was set
			version, err := store.GetMetadata(ctx, "bd_version")
			if err != nil {
				t.Errorf("Failed to get bd_version metadata: %v", err)
			}
			if version == "" {
				t.Error("bd_version metadata was not set")
			}
		})
	}
}

// Note: Error case testing is omitted because the init command calls os.Exit()
// on errors, which makes it difficult to test in a unit test context.

func TestInitAlreadyInitialized(t *testing.T) {
	tmpDir := t.TempDir()
	originalWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}
	defer os.Chdir(originalWd)

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}

	// Initialize once
	rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("First init failed: %v", err)
	}

	// Initialize again with same prefix - should succeed (overwrites)
	rootCmd.SetArgs([]string{"init", "--prefix", "test", "--quiet"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Second init failed: %v", err)
	}

	// Verify database still works
	dbPath := filepath.Join(tmpDir, ".beads", "test.db")
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database after re-init: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("Failed to get prefix after re-init: %v", err)
	}

	if prefix != "test" {
		t.Errorf("Expected prefix 'test', got %q", prefix)
	}
}
