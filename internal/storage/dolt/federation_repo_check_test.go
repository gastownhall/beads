package dolt

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRootDirHasDoltRepo verifies the discriminator that distinguishes a
// real dolt repository (initialized via `dolt init`) from a directory that
// merely contains the dolt sql-server's runtime files.
//
// Regression context: the dolt sql-server, when its `data_dir` points at
// the bead store's server root, writes `.dolt/sql-server.info` for its
// own runtime state. The previous check in migrateServerRootRemotes
// stat'd only `.dolt/` and so treated this runtime directory as a real
// repo, falling through to a 5–7 s `dolt remote -v` invocation on every
// bd write — wedging the gascity reconciler tick.
func TestRootDirHasDoltRepo(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T, dir string)
		wantRepo bool
	}{
		{
			name:     "no_dolt_directory",
			setup:    func(t *testing.T, dir string) {},
			wantRepo: false,
		},
		{
			name: "sql_server_runtime_only_regression",
			setup: func(t *testing.T, dir string) {
				dolt := filepath.Join(dir, ".dolt")
				if err := os.MkdirAll(dolt, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dolt, "sql-server.info"), []byte("port=12345\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantRepo: false,
		},
		{
			name: "partial_init_config_only_no_repo_state",
			setup: func(t *testing.T, dir string) {
				dolt := filepath.Join(dir, ".dolt")
				if err := os.MkdirAll(dolt, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dolt, "config.json"), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantRepo: false,
		},
		{
			name: "real_dolt_repo_with_repo_state",
			setup: func(t *testing.T, dir string) {
				dolt := filepath.Join(dir, ".dolt")
				if err := os.MkdirAll(dolt, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dolt, "repo_state.json"), []byte(`{"head":"refs/heads/main"}`), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantRepo: true,
		},
		{
			name: "real_dolt_repo_alongside_sql_server_info",
			setup: func(t *testing.T, dir string) {
				dolt := filepath.Join(dir, ".dolt")
				if err := os.MkdirAll(dolt, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dolt, "repo_state.json"), []byte(`{"head":"refs/heads/main"}`), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dolt, "sql-server.info"), []byte("port=12345\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantRepo: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			got := rootDirHasDoltRepo(dir)
			if got != tc.wantRepo {
				t.Errorf("rootDirHasDoltRepo(%q) = %v, want %v", dir, got, tc.wantRepo)
			}
		})
	}
}
