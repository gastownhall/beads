package gitignore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsurePatternIgnored(t *testing.T) {
	t.Run("creates the file when missing", func(t *testing.T) {
		dir := t.TempDir()
		if err := EnsurePatternIgnored(dir, "http_target.json"); err != nil {
			t.Fatalf("EnsurePatternIgnored: %v", err)
		}
		body, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if string(body) != "http_target.json\n" {
			t.Errorf(".gitignore = %q, want %q", body, "http_target.json\n")
		}
	})

	t.Run("appends when the pattern is missing from an existing file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".gitignore")
		if err := os.WriteFile(path, []byte("local/\n"), 0o600); err != nil {
			t.Fatalf("seed .gitignore: %v", err)
		}
		if err := EnsurePatternIgnored(dir, "http_target.json"); err != nil {
			t.Fatalf("EnsurePatternIgnored: %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if !strings.Contains(string(body), "local/") {
			t.Errorf(".gitignore lost its existing content: %q", body)
		}
		if !strings.Contains(string(body), "http_target.json") {
			t.Errorf(".gitignore = %q, want it to contain http_target.json", body)
		}
	})

	t.Run("is a no-op when the pattern is already present", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".gitignore")
		original := "local/\nhttp_target.json\n"
		if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
			t.Fatalf("seed .gitignore: %v", err)
		}
		if err := EnsurePatternIgnored(dir, "http_target.json"); err != nil {
			t.Fatalf("EnsurePatternIgnored: %v", err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read .gitignore: %v", err)
		}
		if string(body) != original {
			t.Errorf(".gitignore changed on a no-op call: got %q, want unchanged %q", body, original)
		}
	})
}
