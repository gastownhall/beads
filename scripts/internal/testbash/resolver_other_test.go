//go:build !windows

package testbash

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveUsesPATH(t *testing.T) {
	bin := t.TempDir()
	bash := filepath.Join(bin, "bash")
	if err := os.WriteFile(bash, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write PATH Bash: %v", err)
	}
	t.Setenv("PATH", bin)

	resolved, err := Resolve()
	if err != nil {
		t.Fatalf("resolve PATH Bash: %v", err)
	}
	if resolved != bash {
		t.Fatalf("resolved Bash = %q, want PATH result %q", resolved, bash)
	}
}
