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
		{
			name: "preserve module specifier ts-node/register spaced",
			in:   "--require ts-node/register --max-old-space-size=4096",
			want: "--require ts-node/register --max-old-space-size=4096",
		},
		{
			name: "preserve module specifier ts-node/register equals",
			in:   "--require=ts-node/register",
			want: "--require=ts-node/register",
		},
		{
			name: "preserve node protocol specifier",
			in:   "--require=node:module --max-old-space-size=512",
			want: "--require=node:module --max-old-space-size=512",
		},
		{
			name: "preserve scoped package specifier",
			in:   "-r @babel/register",
			want: "-r @babel/register",
		},
		{
			name: "preserve specifier while scrubbing missing path",
			in:   "--require ts-node/register --require=" + missing,
			want: "--require ts-node/register",
		},
		{
			name: "drop missing relative filesystem path",
			in:   "--require ./gone.cjs --max-old-space-size=256",
			want: "--max-old-space-size=256",
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

func TestScrubStaleNodeOptionsRequireEnvUnsetsWhenOnlyMissing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone.cjs")
	t.Setenv("NODE_OPTIONS", "--require="+missing)

	scrubStaleNodeOptionsRequire()

	if got, ok := os.LookupEnv("NODE_OPTIONS"); ok {
		t.Fatalf("NODE_OPTIONS still set after scrubbing only-missing require: %q", got)
	}
}

func TestKeepExistingRelativeFilesystemRequire(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "present.cjs"), []byte("module.exports = {}\n"), 0o644); err != nil {
		t.Fatalf("write existing require target: %v", err)
	}
	t.Chdir(dir)

	got := scrubStaleNodeOptionsRequireValue("--require=./present.cjs --max-old-space-size=128")
	want := "--require=./present.cjs --max-old-space-size=128"
	if got != want {
		t.Fatalf("scrubStaleNodeOptionsRequireValue() = %q, want %q", got, want)
	}
}

func TestNodeOptionsRequireIsFilesystemPath(t *testing.T) {
	tests := []struct {
		target string
		want   bool
	}{
		{target: "ts-node/register", want: false},
		{target: "node:module", want: false},
		{target: "@babel/register", want: false},
		{target: "esm", want: false},
		{target: "./gone.cjs", want: true},
		{target: "../gone.cjs", want: true},
		{target: `.\gone.cjs`, want: true},
		{target: `..\gone.cjs`, want: true},
		{target: filepath.Join(string(filepath.Separator), "tmp", "gone.cjs"), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			if got := nodeOptionsRequireIsFilesystemPath(tt.target); got != tt.want {
				t.Fatalf("nodeOptionsRequireIsFilesystemPath(%q) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}
