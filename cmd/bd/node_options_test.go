package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScrubStaleNodeOptionsRequireValue(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "present.cjs")
	if err := os.WriteFile(existing, []byte("module.exports = {}\n"), 0o644); err != nil {
		t.Fatalf("write existing require target: %v", err)
	}
	missing := filepath.Join(dir, "missing.cjs")

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "drop missing equals form",
			in:   "--require=" + missing + " --max-old-space-size=4096",
			want: "--max-old-space-size=4096",
		},
		{
			name: "keep existing equals form",
			in:   "--require=" + existing + " --max-old-space-size=4096",
			want: "--require=" + existing + " --max-old-space-size=4096",
		},
		{
			name: "drop missing spaced form",
			in:   "--require " + missing + " --max-old-space-size=4096",
			want: "--max-old-space-size=4096",
		},
		{
			name: "keep existing spaced short form",
			in:   "-r " + existing + " --max-old-space-size=512",
			want: "-r " + existing + " --max-old-space-size=512",
		},
		{
			name: "unset when only missing require remains",
			in:   "--require=" + missing,
			want: "",
		},
		{
			name: "preserve unrelated flags only",
			in:   "--max-old-space-size=4096",
			want: "--max-old-space-size=4096",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scrubStaleNodeOptionsRequireValue(tt.in)
			if got != tt.want {
				t.Fatalf("scrubStaleNodeOptionsRequireValue(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestScrubStaleNodeOptionsRequireEnv(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone.cjs")
	t.Setenv("NODE_OPTIONS", "--require="+missing+" --max-old-space-size=1024")

	scrubStaleNodeOptionsRequire()

	got := os.Getenv("NODE_OPTIONS")
	want := "--max-old-space-size=1024"
	if got != want {
		t.Fatalf("NODE_OPTIONS after scrub = %q, want %q", got, want)
	}
}
