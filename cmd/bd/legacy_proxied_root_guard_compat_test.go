package main

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
	"github.com/stretchr/testify/require"
)

// legacyProxiedRootGuardV1110Env names the runfile that holds the RELEASED
// bd v1.1.0 binary: @bd_releases' sha256-verified catalog pin, so the compat
// test runs the release a real operator could run, needs no network and no
// Go toolchain, and fails loudly when a test target forgets the wiring.
const legacyProxiedRootGuardV1110Env = "BEADS_TEST_BD_V1110_BIN"

// releasedBd1110 resolves the released bd v1.1.0 binary for this test run:
// the pinned release runfile under Bazel, and — for a plain `go test` run,
// which has no runfiles — a one-time build from the module cache's
// beads@v1.1.0, falling back to `git archive v1.1.0` from the checkout. A
// test that cannot get a v1.1.0 binary fails, never skips: the whole point
// of the guard (GH#5104) is what that release does to a proxied-server
// workspace, and a skipped compat test is no compat test.
func releasedBd1110(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skipf("the catalog pins linux/amd64 release assets only; no released bd v1.1.0 runs on %s/%s",
			runtime.GOOS, runtime.GOARCH)
	}
	path, err := bazeltest.RunfileEnv(legacyProxiedRootGuardV1110Env)
	if err == nil {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("released bd v1.1.0 at %s is not usable: %v", path, statErr)
		}
		return path
	}
	path, buildErr := buildReleasedBd1110()
	if buildErr != nil {
		t.Fatalf("no released bd v1.1.0 for the compat test: %v (runfile: %v); without it the guard's behavior against the release it exists for is unverified (GH#5104)", buildErr, err)
	}
	return path
}

// buildReleasedBd1110 builds the released bd v1.1.0 from source, once per
// test binary, for runners without the pinned release runfile. It is never
// a substitute for the pinned binary where one exists — the release is the
// artifact the test speaks for — only the offline fallback for `go test`.
var (
	buildReleasedBd1110Once sync.Once
	buildReleasedBd1110Path string
	buildReleasedBd1110Err  error
)

func buildReleasedBd1110() (string, error) {
	buildReleasedBd1110Once.Do(func() {
		buildReleasedBd1110Path, buildReleasedBd1110Err = buildReleasedBd1110FromSource()
	})
	return buildReleasedBd1110Path, buildReleasedBd1110Err
}

func buildReleasedBd1110FromSource() (string, error) {
	out, err := testTempDir("bd-1-1-0-*")
	if err != nil {
		return "", fmt.Errorf("create build dir: %w", err)
	}
	out = filepath.Join(out, "bd")

	// Source 1: the module cache's copy of the release, no git needed.
	if modcache := os.Getenv("GOMODCACHE"); modcache != "" {
		src := filepath.Join(modcache, "github.com", "steveyegge", "beads@v1.1.0")
		if _, statErr := os.Stat(filepath.Join(src, "cmd", "bd")); statErr == nil {
			if err := goBuildBd1110(src, out); err != nil {
				return out, fmt.Errorf("go build from module cache %s: %w", src, err)
			}
			return out, nil
		}
	}

	// Source 2: git archive v1.1.0 out of this checkout.
	repo := findRepoRoot()
	if repo == "" {
		return out, errors.New("no module-cache beads@v1.1.0 and no repo root to git archive v1.1.0 from")
	}
	src, err := testTempDir("bd-1-1-0-src-*")
	if err != nil {
		return out, fmt.Errorf("create source dir: %w", err)
	}
	if err := gitArchiveInto(repo, "v1.1.0", src); err != nil {
		return out, fmt.Errorf("git archive v1.1.0: %w", err)
	}
	if err := goBuildBd1110(src, out); err != nil {
		return out, fmt.Errorf("go build from git archive: %w", err)
	}
	return out, nil
}

// goBuildBd1110 builds cmd/bd from src into out with an isolated build
// cache, so the fallback build never writes into a HOME the test harness
// re-homed (its go-build cache is a temp dir the suite reaps).
func goBuildBd1110(src, out string) error {
	cache, err := testTempDir("bd-1-1-0-gocache-*")
	if err != nil {
		return fmt.Errorf("create build cache dir: %w", err)
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/bd")
	cmd.Dir = src
	cmd.Env = append(
		testutil.WithoutGitRepoLocationEnv(os.Environ()),
		"GOCACHE="+cache,
		// The v1.1.0 sources carry their own complete go.mod/go.sum; nothing
		// may be added to them.
		"GOFLAGS=-mod=readonly",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build: %w\n%s", err, out)
	}
	return nil
}

// gitArchiveInto extracts `git archive --format=tar <ref>` from repo into
// dst, without a shell and without writing anywhere but dst.
func gitArchiveInto(repo, ref, dst string) error {
	archive := exec.Command("git", "archive", "--format=tar", ref) // #nosec G204 - ref is this test's constant
	archive.Dir = repo
	archive.Stderr = os.Stderr
	tarball, err := archive.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe git archive: %w", err)
	}
	if err := archive.Start(); err != nil {
		return fmt.Errorf("start git archive: %w", err)
	}
	reader := tar.NewReader(tarball)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name := filepath.Clean(header.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("archive entry %q escapes the extraction dir", header.Name)
		}
		target := filepath.Join(dst, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return fmt.Errorf("extract %s: %w", header.Name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf("extract %s: %w", header.Name, err)
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0o700)
			if err != nil {
				return fmt.Errorf("extract %s: %w", header.Name, err)
			}
			if _, err := io.Copy(file, reader); err != nil { // #nosec G110 - the archive is the repo's own tag
				_ = file.Close()
				return fmt.Errorf("extract %s: %w", header.Name, err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("extract %s: %w", header.Name, err)
			}
		default:
			return fmt.Errorf("archive entry %s is not a regular file or directory", header.Name)
		}
	}
	return archive.Wait()
}

// TestArmedGuardStopsReleasedBd1110 is the end-to-end compat test for the
// legacy proxied-root guard (GH#5104): the released bd v1.1.0, run against a
// proxied-server workspace the current bd has armed, must refuse with
// "not a directory" — the guard's regular file at .beads/proxieddb is the
// seam validateProxiedServerRootPath already fails on — instead of doing
// what it does to an unarmed workspace: silently reporting the workspace's
// real tracker as "No issues found." with exit 0, or panicking in stats.
// And the refusal must be clean: no database created, the guard marker
// untouched.
func TestArmedGuardStopsReleasedBd1110(t *testing.T) {
	bd1110 := releasedBd1110(t)
	root, beadsDir := legacyProxiedRootGuardWorkspace(t)
	// The workspace metadata a current bd leaves: proxied-server mode, the
	// current root layout, no obsolete-root entry (the guard's precondition)
	// and an effectively ignored marker (the guard's own doing).
	require.NoError(t, (&configfile.Config{
		Backend:      configfile.BackendDolt,
		DoltMode:     configfile.DoltModeProxiedServer,
		DoltDatabase: "beads",
	}).Save(beadsDir))
	armLegacyProxiedRootGuard(beadsDir)
	guardPath := guardPathFor(beadsDir)
	note, err := os.ReadFile(guardPath)
	require.NoError(t, err, "arming left no guard file")

	for _, args := range [][]string{{"list", "--json"}, {"stats"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := exec.Command(bd1110, args...)
			cmd.Dir = root
			cmd.Env = removedBackendTestEnv(beadsDir)
			out, err := cmd.CombinedOutput()
			require.Error(t, err,
				"released bd v1.1.0 exited 0 on an armed proxied-server workspace:\n%s", out)
			message := string(out)
			if !strings.Contains(message, legacyProxiedRootGuardName+": not a directory") {
				t.Errorf("released bd v1.1.0 did not refuse at the guard file; want %q in the error:\n%s",
					legacyProxiedRootGuardName+": not a directory", out)
			}
			if strings.Contains(message, "No issues found") {
				t.Errorf("silent empty-tracker failure of GH#5104 survived the guard:\n%s", out)
			}
			if strings.Contains(message, "panic:") {
				t.Errorf("released bd v1.1.0 panicked on an armed workspace instead of refusing:\n%s", out)
			}
		})
	}

	// The refusal was clean: v1.1.0 created no database, and the guard it
	// tripped over is exactly the file the current bd wrote.
	for _, name := range []string{"embeddeddolt", "dolt", "beads.db"} {
		if _, err := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(err) {
			t.Errorf("released bd v1.1.0 created %s while refusing (stat error: %v)", name, err)
		}
	}
	got, err := os.ReadFile(guardPath)
	require.NoError(t, err)
	require.Equal(t, string(note), string(got), "the v1.1.0 run modified the guard file")
}
