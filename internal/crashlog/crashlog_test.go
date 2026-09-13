package crashlog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestErrorAppendsTimestampedEntry(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	Error("list", errors.New("boom"))

	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("log file not written: %v", err)
	}
	line := string(data)
	if !strings.Contains(line, "error: boom") {
		t.Errorf("entry missing: %q", line)
	}
	// RFC3339 shape: date, T, offset/UTC marker.
	if !strings.Contains(line, "T") || len(line) < 20 || line[4] != '-' {
		t.Errorf("entry missing RFC3339 timestamp: %q", line)
	}
}

func TestRecoverLogsPanicAndRethrows(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	func() {
		defer func() { _ = recover() }() // swallow the re-panic
		defer Recover()
		panic("kaboom")
	}()

	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("log file not written: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "panic: kaboom") {
		t.Errorf("panic message missing: %q", out)
	}
	if !strings.Contains(out, "goroutine") {
		t.Errorf("stack trace missing: %q", out)
	}
}

func TestDefaultPathUnderUserState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads USERPROFILE on Windows
	want := filepath.Join(home, ".local", "state", "beads", "logs", "bd.log")
	if got := Path(); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestNilErrorNoop(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	Error("list", nil)
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Errorf("log file created for nil error")
	}
}

func TestOversizeLogIsTruncatedNotGrown(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(Path(), make([]byte, maxLogSize+1), 0o600); err != nil {
		t.Fatalf("seed oversize log: %v", err)
	}

	Error("list", errors.New("boom"))

	st, err := os.Stat(Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() > maxLogSize {
		t.Errorf("log not truncated past the cap: size %d", st.Size())
	}
	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "error: boom") {
		t.Errorf("entry after truncation missing: %q", data)
	}
}
