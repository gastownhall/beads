package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMakeSmokeTargetsUseBuildOutput(t *testing.T) {
	makeName := "make"
	if runtime.GOOS == "windows" {
		makeName = "mingw32-make"
	}
	makePath := requireHostTool(t, makeName)
	bashPath := requireHostTool(t, "bash")
	repo := sourceRepoRoot(t)

	for _, target := range []struct {
		name   string
		script string
	}{
		{"test-upgrade", "scripts/upgrade-smoke-test.sh"},
		{"test-cross-version", "scripts/cross-version-smoke-test.sh"},
		{"test-migration", "scripts/migration-test/run.sh"},
	} {
		for _, platform := range []struct {
			os     string
			suffix string
		}{
			{"Linux", ""},
			{"Windows_NT", ".exe"},
		} {
			t.Run(target.name+"/"+platform.os, func(t *testing.T) {
				fixture := t.TempDir()
				for _, name := range []string{"Makefile", "go.mod"} {
					data, err := os.ReadFile(filepath.Join(repo, name))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(fixture, name), data, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				script := filepath.Join(fixture, filepath.FromSlash(target.script))
				if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
					t.Fatal(err)
				}
				writeExecutable(t, script, "#!/usr/bin/env bash\nset -eu\nprintf '%s' \"$CANDIDATE_BIN\" > received-candidate\n")

				// Suppress only the compiler prerequisite; execute the real consumer
				// recipe with both output selectors on this host.
				cmd := exec.Command(makePath, "--no-print-directory", "-o", "build",
					"GIT_BUILD=test", "GO_VERSION=", "BUILD_DIR=candidate output",
					"OS="+platform.os, target.name)
				if runtime.GOOS == "windows" && platform.os != "Windows_NT" {
					// This selector control bypasses Makefile's native Windows bootstrap.
					cmd.Args = append(cmd.Args, "SHELL="+filepath.ToSlash(bashPath))
				}
				cmd.Dir = fixture
				cmd.Env = append(os.Environ(), "MAKEFLAGS=", "MFLAGS=", "MAKEOVERRIDES=")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("make %s: %v\n%s", target.name, err, out)
				}
				got, err := os.ReadFile(filepath.Join(fixture, "received-candidate"))
				if err != nil {
					t.Fatal(err)
				}
				if want := "candidate output/bd" + platform.suffix; string(got) != want {
					t.Fatalf("%s received CANDIDATE_BIN=%q; want %q", target.script, got, want)
				}
			})
		}
	}
}
