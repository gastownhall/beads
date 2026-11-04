package remotecache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltversion"
)

// stubDoltOnPath puts a fake dolt first on PATH for t. It answers
// `dolt version` with version and appends every other invocation's
// arguments to the returned calls file, exiting zero.
func stubDoltOnPath(t *testing.T, version string) (calls string) {
	t.Helper()
	binDir := t.TempDir()
	calls = filepath.Join(t.TempDir(), "calls")
	stub := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'dolt version " + version + "'; exit 0; fi\necho \"$@\" >> '" + calls + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(stub), 0o755); err != nil { //nolint:gosec // G306: the stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// TestCacheRefusesDoltBelowRequiredMin pins the binary floor on the
// remote-repo cache (`bd create --repo <url>`, `bd repo sync`): a dolt on
// PATH whose `dolt version` reports a release below doltversion.RequiredMin
// is refused before Ensure clones or pulls and before Push pushes, so the
// old binary's engine never runs a transfer there. A dolt at the minimum
// runs the transfer.
func TestCacheRefusesDoltBelowRequiredMin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the dolt stub is a POSIX shell script")
	}
	ctx := context.Background()
	const remoteURL = "file:///nonexistent/remote"

	assertRefused := func(t *testing.T, err error, op string) {
		t.Helper()
		if !errors.Is(err, doltversion.ErrBelowRequiredMin) {
			t.Fatalf("%s = %v, want ErrBelowRequiredMin", op, err)
		}
		for _, want := range []string{op, "2.5.1", "requires dolt " + doltversion.RequiredMin.String(), "Upgrade dolt"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal = %q, want it to contain %q", err, want)
			}
		}
	}
	assertNotRun := func(t *testing.T, calls string) {
		t.Helper()
		if got, err := os.ReadFile(calls); err == nil {
			t.Fatalf("the dolt stub ran %q after a refusal, want only `dolt version`", got)
		}
	}
	// warmEntry makes the cache entry look cloned, so Ensure pulls and
	// Push pushes.
	warmEntry := func(t *testing.T, c *Cache) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(c.cloneTarget(remoteURL), ".dolt"), 0o750); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("clone", func(t *testing.T) {
		calls := stubDoltOnPath(t, "2.5.1")
		c := &Cache{Dir: t.TempDir()}
		_, err := c.Ensure(ctx, remoteURL)
		assertRefused(t, err, "dolt clone")
		assertNotRun(t, calls)
	})

	t.Run("pull", func(t *testing.T) {
		calls := stubDoltOnPath(t, "2.5.1")
		c := &Cache{Dir: t.TempDir()}
		warmEntry(t, c)
		_, err := c.Ensure(ctx, remoteURL)
		assertRefused(t, err, "dolt pull")
		assertNotRun(t, calls)
	})

	t.Run("push", func(t *testing.T) {
		calls := stubDoltOnPath(t, "2.5.1")
		c := &Cache{Dir: t.TempDir()}
		warmEntry(t, c)
		assertRefused(t, c.Push(ctx, remoteURL), "dolt push")
		assertNotRun(t, calls)
	})

	t.Run("at the minimum", func(t *testing.T) {
		calls := stubDoltOnPath(t, doltversion.RequiredMin.String())
		c := &Cache{Dir: t.TempDir()}
		if _, err := c.Ensure(ctx, remoteURL); err != nil {
			t.Fatalf("Ensure (clone) = %v, want nil for dolt %s", err, doltversion.RequiredMin)
		}
		warmEntry(t, c)
		if _, err := c.Ensure(ctx, remoteURL); err != nil {
			t.Fatalf("Ensure (pull) = %v, want nil for dolt %s", err, doltversion.RequiredMin)
		}
		if err := c.Push(ctx, remoteURL); err != nil {
			t.Fatalf("Push = %v, want nil for dolt %s", err, doltversion.RequiredMin)
		}
		got, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"clone " + remoteURL, "pull origin main", "push origin main"} {
			if !strings.Contains(string(got), want) {
				t.Errorf("stub calls = %q, want %q to have run", got, want)
			}
		}
	})
}
