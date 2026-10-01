//go:build windows

package testbash

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveIgnoresEarlierBashLauncher(t *testing.T) {
	t.Setenv(OverrideEnv, "")

	shadowDir := t.TempDir()
	shadowBash := filepath.Join(shadowDir, "bash.cmd")
	if err := os.WriteFile(shadowBash, []byte("@echo off\r\nexit /b 97\r\n"), 0o600); err != nil {
		t.Fatalf("write PATH-first Bash launcher: %v", err)
	}
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	t.Setenv("PATH", shadowDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_EXEC_PATH", t.TempDir())

	ambient, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("locate PATH-first Bash launcher: %v", err)
	}
	if !strings.EqualFold(filepath.Clean(ambient), filepath.Clean(shadowBash)) {
		t.Fatalf("ambient bash = %q, want PATH-first launcher %q", ambient, shadowBash)
	}

	resolved, err := Resolve()
	if err != nil {
		t.Fatalf("resolve Git Bash with another launcher first on PATH: %v", err)
	}
	if strings.EqualFold(filepath.Clean(resolved), filepath.Clean(ambient)) {
		t.Fatalf("resolver selected PATH-first launcher %q", resolved)
	}
	if err := validateCandidate(resolved); err != nil {
		t.Fatalf("resolved interpreter is not Git Bash: %v", err)
	}
}

func TestResolveOverride(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	resolved, err := Resolve()
	if err != nil {
		t.Fatalf("resolve default Git Bash: %v", err)
	}

	t.Run("valid", func(t *testing.T) {
		t.Setenv(OverrideEnv, resolved)
		got, err := Resolve()
		if err != nil {
			t.Fatalf("resolve explicit Git Bash: %v", err)
		}
		if !strings.EqualFold(filepath.Clean(got), filepath.Clean(resolved)) {
			t.Fatalf("resolved override = %q, want %q", got, resolved)
		}
	})

	t.Run("relative", func(t *testing.T) {
		t.Setenv(OverrideEnv, "bash.exe")
		if _, err := Resolve(); err == nil {
			t.Fatal("relative override unexpectedly succeeded")
		} else if !IsConfigurationError(err) {
			t.Fatalf("relative override error was not classified as configuration: %v", err)
		} else if !strings.Contains(err.Error(), "must be an absolute path") {
			t.Fatalf("relative override error = %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		t.Setenv(OverrideEnv, t.TempDir())
		if _, err := Resolve(); err == nil {
			t.Fatal("directory override unexpectedly succeeded")
		} else if !IsConfigurationError(err) {
			t.Fatalf("directory override error was not classified as configuration: %v", err)
		} else if !strings.Contains(err.Error(), "is not an ordinary file") {
			t.Fatalf("directory override error = %v", err)
		}
	})
}

func TestResolveIgnoresBashAuthorityControls(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	poison := filepath.Join(t.TempDir(), "startup.sh")
	if err := os.WriteFile(poison, []byte("exit 97\n"), 0o600); err != nil {
		t.Fatalf("write startup poison: %v", err)
	}
	t.Setenv("BASH_ENV", poison)
	t.Setenv("ENV", poison)
	t.Setenv("SHELLOPTS", "noexec")
	t.Setenv("BASHOPTS", "failglob")
	t.Setenv("BASH_FUNC_uname%%", "() { builtin printf 'Linux\\n'; }")

	if _, err := Resolve(); err != nil {
		t.Fatalf("resolve Git Bash with poisoned Bash authority controls: %v", err)
	}
}

func TestProbeRejectsNoExecFalseSuccess(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	bash, err := Resolve()
	if err != nil {
		t.Fatalf("resolve default Git Bash: %v", err)
	}
	t.Setenv("SHELLOPTS", "noexec")

	err = runProbe(bash, "noexec falsifier", "exit 73", os.Environ())
	if err == nil {
		t.Fatal("probe accepted exit zero without executing its body")
	}
	if !strings.Contains(err.Error(), "without the exact execution sentinel") {
		t.Fatalf("noexec false-success error = %v", err)
	}
}
