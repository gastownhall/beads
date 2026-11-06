//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedHookSectionWithoutBdKeepsChainedGate covers GH#6751: when bd is
// not on PATH the generated section must not skip a chained <hook>.old gate,
// so a refusing pre-commit.old still blocks the commit.
func TestGeneratedHookSectionWithoutBdKeepsChainedGate(t *testing.T) {
	hooksDir := t.TempDir()
	emptyPath := t.TempDir() // no bd here

	hook := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"+generateHookSection("pre-commit")), 0o755); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(hooksDir, "pre-commit.old")
	if err := os.WriteFile(gate, []byte("#!/bin/sh\necho gate refused >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", hook)
	cmd.Dir = hooksDir
	cmd.Env = []string{"PATH=" + emptyPath, "HOME=" + hooksDir}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err == nil {
		t.Fatalf("pre-commit without bd on PATH exited 0; chained pre-commit.old gate was skipped (fail-open). output:\n%s", out)
	}
	if !errors.As(err, &exitErr) {
		t.Fatalf("running hook: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "gate refused") {
		t.Errorf("hook output missing the chained gate's own diagnostic, so it may not have run:\n%s", out)
	}
}

// hookChainCase is one classification-edge scenario for the shell-side
// <hook>.old classifier the generated section runs when bd is unavailable.
type hookChainCase struct {
	name string
	// oldBody and oldMode describe the sidecar; a directory is created
	// instead when oldBody is exactly "".
	oldBody string
	oldMode os.FileMode // 0 means the default 0o755
	// hookDirName names the hooks directory inside the test's temp dir, so a
	// case can exercise paths with spaces.
	hookDirName string
	// hookArg0 invokes the hook through this $0 spelling instead of its
	// absolute path (no-slash and symlink cases).
	hookArg0 func(hooksDir string) (arg0 string, dir string)
	env      []string // extra environment entries
	prefix   string   // lines prepended to the hook body (e.g. set -eu)
	// wantRun: the sidecar must execute (its marker shows up). When false
	// the hook must succeed without running it, unless wantExit is set.
	wantRun bool
	// wantExit, when nonzero, is the exact expected hook exit code (the
	// derived default is 7 when wantRun and 0 when not).
	wantExit int
	skipFn   func(t *testing.T)
}

// TestGeneratedHookChainClassifierEdges pins the section's shell-side
// classification of <hook>.old against the same rules getHookVersion applies
// on the Go side (GH#6751): bd-owned markers and "bd hooks run" delegations
// are skipped, everything else — including files that merely mention the
// marker words — runs and its verdict is honored.
func TestGeneratedHookChainClassifierEdges(t *testing.T) {
	const ranMarker = "SIDECHAIN_RAN"
	cases := []hookChainCase{
		{
			name:    "bd section copy is not re-chained",
			oldBody: "#!/bin/sh\n" + generateHookSection("pre-commit") + "\nexit 9\n",
		},
		{
			name:    "bd shim marker is not re-chained",
			oldBody: "#!/bin/sh\n# bd-shim v1.2.3\nexit 9\n",
		},
		{
			name:    "legacy version marker is not re-chained",
			oldBody: "#!/bin/sh\n# bd-hooks-version: 1.2.3\nexit 9\n",
		},
		{
			name:    "legacy inline marker is not re-chained",
			oldBody: "#!/bin/sh\n# bd (beads)\nexit 9\n",
		},
		{
			name:    "bd hooks run delegation is not re-chained",
			oldBody: "#!/bin/sh\n# bd hooks run pre-commit \"$@\"\nexit 9\n",
		},
		{
			name:    "tab-separated bd hooks run delegation is not re-chained",
			oldBody: "#!/bin/sh\n# run via: bd\thooks\trun pre-commit\nexit 9\n",
		},
		{
			name:    "indented bd hooks run delegation is not re-chained",
			oldBody: "#!/bin/sh\n  bd hooks run pre-commit \"$@\"\nexit 9\n",
		},
		{
			name:    "re-entered invocation does not run the sidecar again",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 9\n",
			env:     []string{"BD_HOOK_CHAIN_ACTIVE=pre-commit"},
		},
		{
			name:    "foreign hook merely mentioning BEADS INTEGRATION runs",
			oldBody: "#!/bin/sh\n# see BEADS INTEGRATION docs for context\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			name:    "foreign hook with dash-banner comment runs",
			oldBody: "#!/bin/sh\n# --- NOTE: not a BEADS INTEGRATION marker\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			name:    "foreign hook begin-marker-shaped line with extra words runs",
			oldBody: "#!/bin/sh\n# --- BEGIN foo BEADS INTEGRATION\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			name:    "word-boundary: xbd hooks run is foreign and runs",
			oldBody: "#!/bin/sh\n# xbd hooks run\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			name:    "word-boundary: bd hooks running is foreign and runs",
			oldBody: "#!/bin/sh\n# bd hooks running elsewhere\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			name:    "no final newline is still scanned",
			oldBody: "#!/bin/sh\n# bd hooks run pre-commit \"$@\"\necho " + ranMarker + " >&2\nexit 7",
		},
		{
			name:    "long trailing line without newline runs",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n" + strings.Repeat("x", 5000),
			wantRun: true,
		},
		{
			name:    "non-executable sidecar is skipped",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			oldMode: 0o644,
		},
		{
			name:    "sidecar directory is skipped",
			oldBody: "",
		},
		{
			name:    "set -eu caller still honors the verdict",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			prefix:  "set -eu\n",
			wantRun: true,
		},
		{
			name:        "hooks directory with a space in its path",
			oldBody:     "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			hookDirName: "hooks with space",
			wantRun:     true,
		},
		{
			// Unreadable-but-executable: the scan cannot disqualify what it
			// cannot read, so the sidecar still runs — and the interpreter
			// cannot read it either, refusing the hook instead of skipping
			// the gate.
			name:     "unreadable sidecar is still attempted",
			oldBody:  "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			oldMode:  0o100,
			wantExit: 2,
			skipFn: func(t *testing.T) {
				if os.Geteuid() == 0 {
					t.Skip("root reads files regardless of mode bits")
				}
			},
		},
		{
			// $0 is the symlink's path, so the sidecar is looked up next to
			// the invocation point, not next to the link target.
			name:    "symlinked invocation resolves the sidecar beside the link",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
		{
			// A no-slash $0 (hook found through PATH or the working
			// directory) falls back to the working directory for the sidecar
			// lookup, matching the section's documented invocation-directory
			// rule.
			name:    "no-slash $0 resolves the sidecar via the working directory",
			oldBody: "#!/bin/sh\necho " + ranMarker + " >&2\nexit 7\n",
			wantRun: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipFn != nil {
				tc.skipFn(t)
			}
			baseDir := t.TempDir()
			emptyPath := t.TempDir() // no bd here

			hooksDir := filepath.Join(baseDir, "hooks")
			if tc.hookDirName != "" {
				hooksDir = filepath.Join(baseDir, tc.hookDirName)
			}
			if err := os.MkdirAll(hooksDir, 0o755); err != nil {
				t.Fatal(err)
			}

			sidecarDir := hooksDir
			switch tc.name {
			case "symlinked invocation resolves the sidecar beside the link":
				// The real hook lives in hooksDir; the invocation goes through
				// a symlink in a sibling directory that also holds the
				// sidecar.
				linkDir := filepath.Join(baseDir, "invocation")
				if err := os.MkdirAll(linkDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(hooksDir, "pre-commit"), filepath.Join(linkDir, "pre-commit")); err != nil {
					t.Skipf("cannot create symlink: %v", err)
				}
				tc.hookArg0 = func(string) (string, string) {
					return filepath.Join(linkDir, "pre-commit"), linkDir
				}
				sidecarDir = linkDir
			case "no-slash $0 resolves the sidecar via the working directory":
				tc.hookArg0 = func(dir string) (string, string) {
					return "pre-commit", dir
				}
			}

			if tc.oldBody == "" {
				if err := os.Mkdir(filepath.Join(sidecarDir, "pre-commit.old"), 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				mode := tc.oldMode
				if mode == 0 {
					mode = 0o755
				}
				sidecar := filepath.Join(sidecarDir, "pre-commit.old")
				if err := os.WriteFile(sidecar, []byte(tc.oldBody), mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(sidecar, mode); err != nil {
					t.Fatal(err)
				}
			}

			hookPath := filepath.Join(hooksDir, "pre-commit")
			if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n"+tc.prefix+generateHookSection("pre-commit")), 0o755); err != nil {
				t.Fatal(err)
			}

			env := append([]string{"PATH=" + emptyPath, "HOME=" + baseDir}, tc.env...)
			var out []byte
			var err error
			if tc.hookArg0 != nil {
				arg0, dir := tc.hookArg0(hooksDir)
				cmd := exec.Command("/bin/sh", arg0)
				cmd.Dir = dir
				cmd.Env = env
				out, err = cmd.CombinedOutput()
			} else {
				cmd := exec.Command("/bin/sh", hookPath)
				cmd.Dir = hooksDir
				cmd.Env = env
				out, err = cmd.CombinedOutput()
			}

			ran := strings.Contains(string(out), ranMarker)
			if ran != tc.wantRun {
				t.Fatalf("sidecar ran = %v, want %v\noutput:\n%s\nerr: %v", ran, tc.wantRun, out, err)
			}
			wantExit := tc.wantExit
			if wantExit == 0 && tc.wantRun {
				wantExit = 7
			}
			if wantExit != 0 {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != wantExit {
					t.Fatalf("hook exit = %v, want %d\noutput:\n%s", err, wantExit, out)
				}
			} else if err != nil {
				t.Fatalf("hook should succeed when the sidecar is skipped, got: %v\n%s", err, out)
			}
		})
	}
}
