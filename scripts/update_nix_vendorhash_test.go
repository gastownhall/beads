package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The fixture's vendorHash values carry '+' and '/' on purpose: the script's
// got: regex and its sed replacement must both pass them through intact.
const (
	nixFixtureOriginalHash = "sha256-9A9lzocuEii/t2AHC1ca5uQsB965NqhQW3wpmwklZeg="
	nixFixtureUpdatedHash  = "sha256-IL80Qs9c7ZG17R/hRF6D+F6WVmGB0I+k60+nFuUpIJA="
	// nixFixturePlaceholder is the known-bad hash the script probes with.
	nixFixturePlaceholder = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	nixFixtureOperatorBak = "operator backup\n"
)

// nixStub stands in for `nix build`. Each call records the vendorHash it was
// asked to build, then acts out the next word of NIX_STUB_PLAN (the last word
// repeats):
//
//	toolchain    fails before any hash check (#7441's Go-floor failure)
//	mismatch     reports a hash mismatch whose got: line is NIX_STUB_GOT
//	unparseable  reports a hash mismatch in a got: format the script can't read
//	garbled      reports a got: line its regex only half matches ("sha256-")
//	ok           builds
//	fail         fails after the hash was accepted
//	block        records blocked-<call>, then sleeps until signalled
const nixStub = `#!/bin/sh
set -eu
[ "${1:-}" = build ] || { echo "nix stub: unexpected invocation: $*" >&2; exit 64; }
call=0
if [ -s "$NIX_STUB_STATE/count" ]; then
  IFS= read -r call <"$NIX_STUB_STATE/count"
fi
call=$((call + 1))
printf '%s\n' "$call" >"$NIX_STUB_STATE/count"
grep 'vendorHash = ' default.nix >>"$NIX_STUB_STATE/builds"
n=$call
set -- $NIX_STUB_PLAN
while [ "$n" -gt 1 ] && [ "$#" -gt 1 ]; do
  shift
  n=$((n - 1))
done
mismatch_header="error: hash mismatch in fixed-output derivation '/nix/store/x-beads-go-modules.drv':"
case "$1" in
toolchain)
  echo 'go: github.com/dolthub/driver/v2@v2.4.1 requires go >= 1.26.5 (running go 1.26.4; GOTOOLCHAIN=local)' >&2
  exit 1 ;;
mismatch)
  printf '%s\n         specified: %s\n            got:    %s\n' "$mismatch_header" "$NIX_STUB_PLACEHOLDER" "$NIX_STUB_GOT" >&2
  exit 1 ;;
unparseable)
  printf '%s\n            got:    sha256:0c4fd1m3dpbg2l2kq3zcmjb5x8qgvs1zl5z0a3k1yhb5rwbnk6zz\n' "$mismatch_header" >&2
  exit 1 ;;
garbled)
  printf '%s\n            got:    sha256-!\n' "$mismatch_header" >&2
  exit 1 ;;
ok)
  exit 0 ;;
fail)
  echo "error: builder for '/nix/store/x-beads.drv' failed with exit code 2" >&2
  exit 1 ;;
block)
  : >"$NIX_STUB_STATE/blocked-$call"
  exec sleep 60 ;;
*)
  echo "nix stub: unknown plan step $1" >&2
  exit 64 ;;
esac
`

var nixFixtureVendorHashRe = regexp.MustCompile(`vendorHash = "([^"]*)";`)

// nixVendorHashFixture is a checkout holding only default.nix (and an
// operator's own default.nix.backup), with the nix stub first on PATH and
// TMPDIR fenced into the test, so it sees every file the script leaves behind.
type nixVendorHashFixture struct {
	bash, script, dir, state, tmp string
	env                           []string
}

func newNixVendorHashFixture(t *testing.T, plan string) nixVendorHashFixture {
	t.Helper()

	bash := requireHostTool(t, "bash")
	bin := t.TempDir()
	fx := nixVendorHashFixture{bash: bash, dir: t.TempDir(), state: t.TempDir(), tmp: t.TempDir()}
	writeExecutable(t, filepath.Join(bin, "nix"), nixStub)
	writeNixFixtureFile(t, filepath.Join(fx.dir, "default.nix"), nixFixtureDefaultNix(nixFixtureOriginalHash), 0o640)
	writeNixFixtureFile(t, filepath.Join(fx.dir, "default.nix.backup"), nixFixtureOperatorBak, 0o600)

	pathEnv := shellPathEnv()
	binPath := shellPathUnderEnv(t, bash, bin, pathEnv)
	commandPath := binPath + ":" + os.Getenv("PATH") + ":/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		commandPath = binPath + ":/usr/bin:/bin"
	}
	fx.env = []string{
		"PATH=" + os.Getenv("PATH"),
		"BEADS_TEST_COMMAND_PATH=" + commandPath,
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"TMPDIR=" + shellPathUnderEnv(t, bash, fx.tmp, pathEnv),
		"NIX_STUB_STATE=" + shellPathUnderEnv(t, bash, fx.state, pathEnv),
		"NIX_STUB_PLAN=" + plan,
		"NIX_STUB_GOT=" + nixFixtureUpdatedHash,
		"NIX_STUB_PLACEHOLDER=" + nixFixturePlaceholder,
	}
	requireShellCommandPath(t, bash, fx.dir, fx.env, "nix", binPath+"/nix")
	fx.script = shellPathUnderEnv(t, bash, filepath.Join(sourceRepoRoot(t), "scripts", "update-nix-vendorhash.sh"), pathEnv)
	return fx
}

func nixFixtureDefaultNix(hash string) string {
	return "# operator comment the script must keep\n{ buildGoModule }:\nbuildGoModule {\n  vendorHash = \"" + hash + "\";\n}\n"
}

func writeNixFixtureFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode is filtered by the umask; the mode assertions need it exact.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// command runs the repository's script from the fixture checkout, the way the
// workflow and CONTRIBUTING.md run it from the repository root.
func (fx nixVendorHashFixture) command() *exec.Cmd {
	cmd := bashScriptCommand(fx.bash, fx.script)
	cmd.Dir = fx.dir
	cmd.Env = fx.env
	return cmd
}

// builds returns the vendorHash each stubbed `nix build` saw, in call order.
func (fx nixVendorHashFixture) builds(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fx.state, "builds"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for _, m := range nixFixtureVendorHashRe.FindAllStringSubmatch(string(data), -1) {
		hashes = append(hashes, m[1])
	}
	return hashes
}

// assertCheckout checks the files the script may touch: default.nix holds
// wantHash with its original bytes and mode otherwise, the operator's backup is
// untouched, and nothing else was left beside them.
func (fx nixVendorHashFixture) assertCheckout(t *testing.T, wantHash string) {
	t.Helper()

	path := filepath.Join(fx.dir, "default.nix")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := nixFixtureDefaultNix(wantHash); string(got) != want {
		t.Errorf("default.nix =\n%s\nwant\n%s", got, want)
	}
	backup, err := os.ReadFile(filepath.Join(fx.dir, "default.nix.backup"))
	if err != nil || string(backup) != nixFixtureOperatorBak {
		t.Errorf("operator's default.nix.backup = %q (%v), want it untouched (%q)", backup, err, nixFixtureOperatorBak)
	}
	if runtime.GOOS != "windows" {
		for name, want := range map[string]os.FileMode{"default.nix": 0o640, "default.nix.backup": 0o600} {
			info, err := os.Stat(filepath.Join(fx.dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != want {
				t.Errorf("%s mode = %o, want %o", name, info.Mode().Perm(), want)
			}
		}
	}
	entries, err := os.ReadDir(fx.dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, []string{"default.nix", "default.nix.backup"}) {
		t.Errorf("checkout holds %q, want only default.nix and the operator's default.nix.backup", names)
	}
}

// tmpEntries lists what the script left in its TMPDIR.
func (fx nixVendorHashFixture) tmpEntries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(fx.tmp)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// TestUpdateNixVendorHashReportsTheRealBuildResult is the regression net for
// #7498 and #7529. The script used to exit 0 ("vendorHash is already
// correct!") when the probe build failed for a reason other than the hash,
// and to print "Build successful!" when its verification build failed, which
// let update-vendor-hash.yml go green on #7441's broken Nix build (#7499). A
// failure must exit non-zero, restore default.nix byte for byte and mode for
// mode, leave an operator's own default.nix.backup alone, and keep the build
// log it names; a success must have built the file it leaves behind.
func TestUpdateNixVendorHashReportsTheRealBuildResult(t *testing.T) {
	const (
		original    = nixFixtureOriginalHash
		updated     = nixFixtureUpdatedHash
		placeholder = nixFixturePlaceholder
	)
	tests := []struct {
		name       string
		plan       string
		wantExit   int
		wantHash   string
		wantBuilds []string
		wantOutput string
	}{
		{
			name:       "build fails for a reason other than the hash",
			plan:       "toolchain",
			wantExit:   1,
			wantHash:   original,
			wantBuilds: []string{placeholder},
			wantOutput: "Build failed for a reason other than a hash mismatch",
		},
		{
			name:       "hash mismatch with a got line the regex cannot read",
			plan:       "unparseable",
			wantExit:   1,
			wantHash:   original,
			wantBuilds: []string{placeholder},
			wantOutput: "didn't match the extraction regex",
		},
		{
			name:       "verification build fails",
			plan:       "mismatch fail",
			wantExit:   1,
			wantHash:   original,
			wantBuilds: []string{placeholder, updated},
			wantOutput: "Build failed after updating hash",
		},
		{
			name:       "half-parsed got hash fails verification",
			plan:       "garbled fail",
			wantExit:   1,
			wantHash:   original,
			wantBuilds: []string{placeholder, "sha256-"},
			wantOutput: "Build failed after updating hash",
		},
		{
			name:       "hash updated and verified",
			plan:       "mismatch ok",
			wantExit:   0,
			wantHash:   updated,
			wantBuilds: []string{placeholder, updated},
			wantOutput: "default.nix has been updated",
		},
		{
			name:       "probe accepts the placeholder and the restored file builds",
			plan:       "ok",
			wantExit:   0,
			wantHash:   original,
			wantBuilds: []string{placeholder, original},
			wantOutput: "vendorHash is already correct",
		},
		{
			name:       "probe accepts the placeholder but the restored file does not build",
			plan:       "ok fail",
			wantExit:   1,
			wantHash:   original,
			wantBuilds: []string{placeholder, original},
			wantOutput: "the restored file doesn't build either",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newNixVendorHashFixture(t, test.plan)
			output, err := fx.command().CombinedOutput()
			if got := exitCode(err); got != test.wantExit {
				t.Fatalf("exit = %d, want %d\n%s", got, test.wantExit, output)
			}
			if !strings.Contains(string(output), test.wantOutput) {
				t.Errorf("output lacks %q:\n%s", test.wantOutput, output)
			}
			if got := fx.builds(t); !slices.Equal(got, test.wantBuilds) {
				t.Errorf("nix built vendorHash %q, want %q\n%s", got, test.wantBuilds, output)
			}
			fx.assertCheckout(t, test.wantHash)

			// The private backup directory never outlives the run. A success
			// removes its logs too; a failure keeps the build logs it names.
			var logs []string
			for _, name := range fx.tmpEntries(t) {
				if !strings.HasSuffix(name, ".log") {
					t.Errorf("TMPDIR still holds %s after the run", name)
					continue
				}
				logs = append(logs, name)
			}
			if test.wantExit == 0 && len(logs) != 0 {
				t.Errorf("successful run left logs %q in TMPDIR", logs)
			}
			if test.wantExit != 0 {
				if len(logs) == 0 {
					t.Errorf("failed run kept no build log in TMPDIR\n%s", output)
				}
				for _, name := range logs {
					if !strings.Contains(string(output), name) {
						t.Errorf("failed run kept %s but never named it\n%s", name, output)
					}
				}
			}
		})
	}
}
