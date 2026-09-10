package doltversion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeStub(t *testing.T, dir, name, script string, executable bool) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("write stub %s: %v", path, err)
	}
	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod stub %s: %v", path, err)
	}
	return path
}

func TestResolvePrecedence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skipf("shell-script stubs require a POSIX shell; skipping on windows")
	}
	dir := t.TempDir()
	envBin := writeStub(t, dir, "env-dolt", "#!/bin/sh\necho 'dolt version 2.0.0'\n", true)
	sidecarBin := writeStub(t, dir, "sidecar-dolt", "#!/bin/sh\necho 'dolt version 2.0.0'\n", true)

	t.Run("env beats sidecar and PATH", func(t *testing.T) {
		path, src, err := Resolve(ResolveOptions{EnvValue: envBin, SidecarValue: sidecarBin})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if path != envBin || src != SourceEnv {
			t.Errorf("Resolve = (%q, %v), want (%q, SourceEnv)", path, src, envBin)
		}
	})

	t.Run("sidecar beats PATH", func(t *testing.T) {
		path, src, err := Resolve(ResolveOptions{SidecarValue: sidecarBin})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if path != sidecarBin || src != SourceSidecar {
			t.Errorf("Resolve = (%q, %v), want (%q, SourceSidecar)", path, src, sidecarBin)
		}
	})

	t.Run("explicit but broken env value errors rather than falling through", func(t *testing.T) {
		broken := filepath.Join(dir, "does-not-exist")
		_, src, err := Resolve(ResolveOptions{EnvValue: broken, SidecarValue: sidecarBin})
		if err == nil {
			t.Fatal("Resolve with broken EnvValue: want error, got nil")
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Resolve error = %v, want wrapping ErrNotFound", err)
		}
		if src != SourceEnv {
			t.Errorf("Resolve source = %v, want SourceEnv (should report which precedence level failed)", src)
		}
	})

	t.Run("missing everything falls through to PATH and fails not found", func(t *testing.T) {
		emptyPath := t.TempDir()
		t.Setenv("PATH", emptyPath)
		_, src, err := Resolve(ResolveOptions{})
		if err == nil {
			t.Fatal("Resolve with nothing configured and empty PATH: want error, got nil")
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Resolve error = %v, want wrapping ErrNotFound", err)
		}
		if src != SourcePath {
			t.Errorf("Resolve source = %v, want SourcePath", src)
		}
	})
}

func TestResolveExplicitBrokenSidecarErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skipf("requires POSIX file permission semantics")
	}
	dir := t.TempDir()
	nonExec := writeStub(t, dir, "not-exec", "#!/bin/sh\necho hi\n", false)
	_, src, err := Resolve(ResolveOptions{SidecarValue: nonExec})
	if err == nil {
		t.Fatal("Resolve with non-executable SidecarValue: want error, got nil")
	}
	if !errors.Is(err, ErrNotExecutable) {
		t.Errorf("Resolve error = %v, want wrapping ErrNotExecutable", err)
	}
	if src != SourceSidecar {
		t.Errorf("Resolve source = %v, want SourceSidecar", src)
	}
}

func TestReadEnvOverride(t *testing.T) {
	t.Setenv(DoltBinEnvVar, "/some/path")
	if got := ReadEnvOverride(); got != "/some/path" {
		t.Errorf("ReadEnvOverride() = %q, want /some/path", got)
	}
	t.Setenv(DoltBinEnvVar, "")
	if got := ReadEnvOverride(); got != "" {
		t.Errorf("ReadEnvOverride() = %q, want empty", got)
	}
}

// TestResolveAbsolutizesBareEnvValue regression-tests the bug both cross-
// vendor reviewers flagged: a bare, separator-free BEADS_DOLT_BIN value
// (e.g. "dolt-next", no "/" in it) is resolved two DIFFERENT ways by
// os.Stat (cwd-relative) and by exec.CommandContext/the OS exec syscalls
// (PATH search) if left as-is. Without absolutizing in Resolve, validation
// would approve the cwd-relative file while Probe's later exec searches
// PATH instead — approving one binary, launching a different one. This
// test proves the two candidates are actually different files at the OS
// level (different content/version) and that Resolve+Probe together
// consistently pick the cwd-relative one, not the PATH one.
func TestResolveAbsolutizesBareEnvValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skipf("shell-script stubs require a POSIX shell; skipping on windows")
	}

	cwdDir := t.TempDir()
	pathDir := t.TempDir()

	// The real target: a bare-named binary sitting in the current
	// directory, never intended to be found via PATH search.
	writeStub(t, cwdDir, "dolt-next", "#!/bin/sh\necho 'dolt version 2.5.0'\n", true)
	// A decoy with the SAME bare name on PATH, reporting a different
	// version. If Resolve failed to absolutize, Probe would silently run
	// this one instead of the cwd file Resolve validated.
	writeStub(t, pathDir, "dolt-next", "#!/bin/sh\necho 'dolt version 9.9.9-decoy'\n", true)

	t.Chdir(cwdDir)
	t.Setenv("PATH", pathDir)

	path, src, err := Resolve(ResolveOptions{EnvValue: "dolt-next"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if src != SourceEnv {
		t.Fatalf("Resolve source = %v, want SourceEnv", src)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("Resolve returned non-absolute path %q for bare env value", path)
	}
	wantPath := filepath.Join(cwdDir, "dolt-next")
	if path != wantPath {
		t.Fatalf("Resolve path = %q, want %q (the cwd-relative file, not a PATH lookup result)", path, wantPath)
	}

	id, err := Probe(context.Background(), path)
	if err != nil {
		t.Fatalf("Probe(%q): %v", path, err)
	}
	if id.Version.String() != "2.5.0" {
		t.Fatalf("Probe ran the wrong binary: got version %v, want 2.5.0 (the cwd file, not the 9.9.9-decoy on PATH)", id.Version)
	}
}

// TestResolveCompletesWindowsExeExtension regression-tests the review
// finding that BEADS_DOLT_BIN=C:\tools\dolt reported "not found" while
// dolt.exe sat right there: os.Lstat has no PATHEXT concept, so an
// explicit path spelled without its extension (the way operators type
// paths everywhere else on Windows) must be completed via exec.LookPath
// before validation. Windows-only by nature; the completion helper is an
// identity function elsewhere.
func TestResolveCompletesWindowsExeExtension(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PATHEXT extension completion is Windows-specific")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "dolt.exe")
	if err := os.WriteFile(target, []byte("MZ fake binary for stat-level tests"), 0o644); err != nil {
		t.Fatalf("write fake exe: %v", err)
	}

	t.Run("extensionless env value finds the .exe", func(t *testing.T) {
		path, src, err := Resolve(ResolveOptions{EnvValue: filepath.Join(dir, "dolt")})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if src != SourceEnv {
			t.Errorf("source = %v, want SourceEnv", src)
		}
		if path != target {
			t.Errorf("path = %q, want %q (completed extension)", path, target)
		}
	})

	t.Run("exact extensionless file wins when it exists", func(t *testing.T) {
		exact := filepath.Join(dir, "doltbare")
		if err := os.WriteFile(exact, []byte("exact file, no extension"), 0o644); err != nil {
			t.Fatalf("write exact file: %v", err)
		}
		// A sibling with .exe must NOT shadow the exactly-spelled file.
		if err := os.WriteFile(exact+".exe", []byte("decoy"), 0o644); err != nil {
			t.Fatalf("write decoy: %v", err)
		}
		path, _, err := Resolve(ResolveOptions{EnvValue: exact})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if path != exact {
			t.Errorf("path = %q, want %q (exact spelled path wins)", path, exact)
		}
	})

	t.Run("still not-found when no completion exists", func(t *testing.T) {
		_, _, err := Resolve(ResolveOptions{EnvValue: filepath.Join(dir, "no-such-binary")})
		if err == nil {
			t.Fatal("Resolve: want error, got nil")
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("error = %v, want wrapping ErrNotFound", err)
		}
	})
}

func TestSourceString(t *testing.T) {
	cases := map[Source]string{
		SourceEnv:     DoltBinEnvVar,
		SourceSidecar: "sidecar config",
		SourcePath:    "PATH",
	}
	for src, want := range cases {
		if got := src.String(); got != want {
			t.Errorf("Source(%d).String() = %q, want %q", src, got, want)
		}
	}
}
