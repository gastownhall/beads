package embeddeddolt

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestHasRepository(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, beadsDir string)
		want  bool
	}{
		{name: "missing root", want: false},
		{
			name: "empty root",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(beadsDir, "embeddeddolt"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
		{
			name: "empty repository marker",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
		{
			name: "nonempty repository marker",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				marker := filepath.Join(beadsDir, "embeddeddolt", "beads", ".dolt")
				if err := os.MkdirAll(marker, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(marker, "opaque-entry"), []byte("not inspected"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: true,
		},
		{
			name: "symlink marker is refused",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				databaseDir := filepath.Join(beadsDir, "embeddeddolt", "beads")
				if err := os.MkdirAll(databaseDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(beadsDir, filepath.Join(databaseDir, ".dolt")); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, beadsDir)
			}
			if got := HasRepository(beadsDir); got != tt.want {
				t.Fatalf("HasRepository() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDatabases(t *testing.T) {
	// seed creates database under root with a nonempty .dolt marker.
	seed := func(t *testing.T, root, database string) {
		t.Helper()
		marker := filepath.Join(root, database, ".dolt")
		if err := os.MkdirAll(marker, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(marker, "opaque-entry"), []byte("not inspected"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, beadsDir string)
		want  []string
	}{
		{name: "missing root"},
		{
			name: "one database",
			setup: func(t *testing.T, beadsDir string) {
				seed(t, filepath.Join(beadsDir, "embeddeddolt"), "proj")
			},
			want: []string{"proj"},
		},
		{
			name: "several databases are sorted",
			setup: func(t *testing.T, beadsDir string) {
				root := filepath.Join(beadsDir, "embeddeddolt")
				seed(t, root, "proj")
				seed(t, root, "beads")
			},
			want: []string{"beads", "proj"},
		},
		{
			name: "entries failing the marker probe are skipped",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				root := filepath.Join(beadsDir, "embeddeddolt")
				seed(t, root, "proj")
				if err := os.MkdirAll(filepath.Join(root, "empty", ".dolt"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "notes"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "proj"), filepath.Join(root, "linked")); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"proj"},
		},
		{
			name: "symlink root is refused",
			setup: func(t *testing.T, beadsDir string) {
				t.Helper()
				elsewhere := t.TempDir()
				seed(t, elsewhere, "proj")
				if err := os.Symlink(elsewhere, filepath.Join(beadsDir, "embeddeddolt")); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, beadsDir)
			}
			if got := Databases(beadsDir); !slices.Equal(got, tt.want) {
				t.Fatalf("Databases() = %v, want %v", got, tt.want)
			}
		})
	}
}
