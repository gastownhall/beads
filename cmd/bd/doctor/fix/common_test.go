package fix

import (
	"path/filepath"
	"testing"
)

func TestSafeWorkspacePath(t *testing.T) {
	root := t.TempDir()
	absEscape, _ := filepath.Abs(filepath.Join(root, "..", "escape"))

	tests := []struct {
		name    string
		relPath string
		wantErr bool
	}{
		{
			name:    "normal relative path",
			relPath: ".beads/issues.jsonl",
			wantErr: false,
		},
		{
			name:    "nested relative path",
			relPath: filepath.Join(".beads", "nested", "file.txt"),
			wantErr: false,
		},
		{
			name:    "absolute path rejected",
			relPath: absEscape,
			wantErr: true,
		},
		{
			name:    "path traversal rejected",
			relPath: filepath.Join("..", "escape"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := safeWorkspacePath(root, tt.relPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("safeWorkspacePath() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				if !isWithinWorkspace(root, got) {
					t.Fatalf("resolved path %q not within workspace %q", got, root)
				}
				if !filepath.IsAbs(got) {
					t.Fatalf("resolved path is not absolute: %q", got)
				}
			}
		})
	}
}
