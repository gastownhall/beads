package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Test actions exec host tools and load the shared libraries of every cgo
// binary they link, so the remote worker host is an input to every result
// rbe-west caches. //platforms:rbe_worker puts it into the action key: its
// worker-env exec property is the sha256 of tools/rbe/worker-env.txt, and
// rbe-west's schedulers run an action only on a worker that advertises that
// exact value. The OSS pool and its manifest are gastownhall/gascity's
// (tools/rbe/worker-env, blacksmith-worker.sh); beads commits a byte-for-byte
// copy of the manifest so the pin is reviewable and moves only with it, and
// tools/rbe/worker-env-sync (nightly) checks the copy against gascity's main.
//
// The manifest is the worker's toolchain, not its image: arch, OS release,
// Go, dolt, and the upstream releases (at most major.minor) of the libraries
// and tools actions reach. The Blacksmith image's Ubuntu security revisions
// are not in it, so they neither re-key every action nor strand the pool.

const (
	rbeWorkerPlatformBuild = "platforms/BUILD.bazel"
	rbeWorkerEnvManifest   = "tools/rbe/worker-env.txt"
	rbeWorkerPlatformFlag  = "--extra_execution_platforms=//platforms:rbe_worker"
	rbeWorkerEnvSync       = "tools/rbe/worker-env-sync"
)

var (
	rbeWorkerPlatformRE = regexp.MustCompile(`(?s)\nplatform\(\n    name = "rbe_worker",\n(.*?)\n\)\n`)
	rbeExecPropsRE      = regexp.MustCompile(`(?s)exec_properties = \{\n(.*?)\n    \},`)
	rbeExecPropRE       = regexp.MustCompile(`^\s*"([^"]+)": "([^"]*)",$`)
	workerEnvPinRE      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// checkRBEWorkerPlatform: the rbe_worker platform's only exec property is
// worker-env, pinned to the sha256 of manifest. Any other property would be
// one no OSS worker advertises, and no action would ever schedule.
func checkRBEWorkerPlatform(build, manifest string) []error {
	m := rbeWorkerPlatformRE.FindStringSubmatch(build)
	if m == nil {
		return []error{errors.New(rbeWorkerPlatformBuild + ": no platform rbe_worker")}
	}
	props := rbeExecPropsRE.FindStringSubmatch(m[1])
	if props == nil {
		return []error{errors.New(rbeWorkerPlatformBuild + ": platform rbe_worker has no exec_properties")}
	}
	got := map[string]string{}
	for _, line := range strings.Split(props[1], "\n") {
		e := rbeExecPropRE.FindStringSubmatch(line)
		if e == nil {
			return []error{errors.New(rbeWorkerPlatformBuild + ": unexpected exec_properties line " + line)}
		}
		got[e[1]] = e[2]
	}
	pin := got["worker-env"]
	if len(got) != 1 || !workerEnvPinRE.MatchString(pin) {
		return []error{errors.New(rbeWorkerPlatformBuild + ": rbe_worker exec_properties must be worker-env=sha256:<hex> alone")}
	}
	sum := sha256.Sum256([]byte(manifest))
	if want := "sha256:" + hex.EncodeToString(sum[:]); pin != want {
		return []error{errors.New(rbeWorkerPlatformBuild + " pins worker-env=" + pin + ", but " +
			rbeWorkerEnvManifest + " hashes to " + want + ": commit the manifest and its sha256 together")}
	}
	return nil
}

// checkRBEWorkerSelected: every command executes on rbe_worker (the flag is
// key-affecting, so it may not depend on a config or command).
func checkRBEWorkerSelected(rc string) error {
	for _, line := range strings.Split(rc, "\n") {
		if strings.TrimSpace(line) == "build "+rbeWorkerPlatformFlag {
			return nil
		}
	}
	return errors.New(".bazelrc must set `build " + rbeWorkerPlatformFlag + "` unconditionally")
}

func TestRBEWorkerPlatformPinsWorkerEnv(t *testing.T) {
	root := bazelPolicyRoot(t)
	for _, err := range checkRBEWorkerPlatform(readPolicyFile(t, root, rbeWorkerPlatformBuild), readPolicyFile(t, root, rbeWorkerEnvManifest)) {
		t.Error(err)
	}
	if err := checkRBEWorkerSelected(readPolicyFile(t, root, ".bazelrc")); err != nil {
		t.Error(err)
	}
}

func TestRBEWorkerPlatformGuards(t *testing.T) {
	manifest := "arch x86_64\nos ubuntu 24.04\n"
	sum := sha256.Sum256([]byte(manifest))
	pin := "sha256:" + hex.EncodeToString(sum[:])
	build := "# header\nplatform(\n    name = \"rbe_worker\",\n    exec_properties = {\n        \"worker-env\": \"" + pin + "\",\n    },\n    parents = [\"@bazel_tools//tools:host_platform\"],\n)\n"
	if errs := checkRBEWorkerPlatform(build, manifest); len(errs) != 0 {
		t.Fatalf("good platform fixture: %v", errs)
	}
	for name, bad := range map[string][2]string{
		"manifest moved": {build, manifest + "pkg git 1\n"},
		"no platform":    {strings.Replace(build, `name = "rbe_worker"`, `name = "other"`, 1), manifest},
		"extra property": {strings.Replace(build, "    },", "        \"pool\": \"x\",\n    },", 1), manifest},
		"not a sha":      {strings.Replace(build, pin, "latest", 1), manifest},
	} {
		if len(checkRBEWorkerPlatform(bad[0], bad[1])) == 0 {
			t.Errorf("%s: expected an error", name)
		}
	}

	rc := "common --enable_bzlmod\nbuild " + rbeWorkerPlatformFlag + "\n"
	if err := checkRBEWorkerSelected(rc); err != nil {
		t.Fatalf("good .bazelrc fixture: %v", err)
	}
	for name, bad := range map[string]string{
		"missing":          "common --enable_bzlmod\n",
		"only in a config": strings.Replace(rc, "build "+rbeWorkerPlatformFlag, "build:remote-exec "+rbeWorkerPlatformFlag, 1),
		"commented out":    strings.Replace(rc, "build "+rbeWorkerPlatformFlag, "# build "+rbeWorkerPlatformFlag, 1),
	} {
		if checkRBEWorkerSelected(bad) == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// rbeWorkerEnvLineRE is every line gascity's tools/rbe/worker-env prints on
// a worker that has what it measures: one arch, dolt, go, os and yq line,
// and each package at its upstream release, at most major.minor.
var rbeWorkerEnvLineRE = map[string]*regexp.Regexp{
	"arch": regexp.MustCompile(`^arch [a-z0-9_]+$`),
	"dolt": regexp.MustCompile(`^dolt dolt version \S+$`),
	"go":   regexp.MustCompile(`^go go version go\S+ linux/\S+$`),
	"os":   regexp.MustCompile(`^os ubuntu \d+\.\d+$`),
	"tool": regexp.MustCompile(`^tool yq \d+$`),
	"pkg":  regexp.MustCompile(`^pkg [a-z0-9][a-z0-9+.-]* \d+(\.\d+)?$`),
}

// checkRBEWorkerEnvManifest: manifest is a rendering of gascity's
// tools/rbe/worker-env in its toolchain-only form. A copy of an older
// gascity manifest (dpkg revisions such as 9.4-3ubuntu6.2), a hand edit, or
// a truncated copy fails here rather than queueing every remote action.
func checkRBEWorkerEnvManifest(manifest string) []error {
	if !strings.HasSuffix(manifest, "\n") {
		return []error{errors.New(rbeWorkerEnvManifest + " must end with a newline, as gascity's tools/rbe/worker-env prints it")}
	}
	lines := strings.Split(strings.TrimSuffix(manifest, "\n"), "\n")
	var errs []error
	if !slices.IsSorted(lines) {
		errs = append(errs, errors.New(rbeWorkerEnvManifest+" is not sorted (LC_ALL=C), as gascity's tools/rbe/worker-env prints it"))
	}
	seen := map[string]int{}
	for _, line := range lines {
		kind, _, _ := strings.Cut(line, " ")
		seen[kind]++
		re := rbeWorkerEnvLineRE[kind]
		if re == nil || !re.MatchString(line) {
			errs = append(errs, errors.New(rbeWorkerEnvManifest+": "+strings.TrimSpace(line)+
				" is not a gascity tools/rbe/worker-env line (packages are measured at their upstream release, at most major.minor, and installed)"))
		}
	}
	for _, kind := range []string{"arch", "dolt", "go", "os", "tool"} {
		if seen[kind] != 1 {
			errs = append(errs, errors.New(rbeWorkerEnvManifest+" needs exactly one "+kind+" line"))
		}
	}
	if seen["pkg"] == 0 {
		errs = append(errs, errors.New(rbeWorkerEnvManifest+" has no packages"))
	}
	return errs
}

func TestRBEWorkerEnvManifestIsGascitysRendering(t *testing.T) {
	root := bazelPolicyRoot(t)
	for _, err := range checkRBEWorkerEnvManifest(readPolicyFile(t, root, rbeWorkerEnvManifest)) {
		t.Error(err)
	}
}

func TestRBEWorkerEnvManifestGuards(t *testing.T) {
	good := "arch x86_64\ndolt dolt version 2.1.8\ngo go version go1.26.6 linux/amd64\nos ubuntu 24.04\n" +
		"pkg coreutils 9.4\npkg git 2\npkg libc6 2.39\ntool yq 4\n"
	if errs := checkRBEWorkerEnvManifest(good); len(errs) != 0 {
		t.Fatalf("good manifest fixture: %v", errs)
	}
	for name, bad := range map[string]string{
		"dpkg revision":   strings.Replace(good, "pkg coreutils 9.4\n", "pkg coreutils 9.4-3ubuntu6.2\n", 1),
		"epoch":           strings.Replace(good, "pkg git 2\n", "pkg git 1:2.55.0-0ppa1~ubuntu24.04.2\n", 1),
		"patch level":     strings.Replace(good, "pkg libc6 2.39\n", "pkg libc6 2.39.0\n", 1),
		"missing package": strings.Replace(good, "pkg git 2\n", "pkg git missing\n", 1),
		"no yq line":      strings.Replace(good, "tool yq 4\n", "", 1),
		"unsorted":        strings.Replace(good, "arch x86_64\n", "", 1) + "arch x86_64\n",
		"no newline":      strings.TrimSuffix(good, "\n"),
		"unknown line":    good + "zzz extra\n",
		"two os lines":    strings.Replace(good, "os ubuntu 24.04\n", "os ubuntu 24.04\nos ubuntu 26.04\n", 1),
	} {
		if len(checkRBEWorkerEnvManifest(bad)) == 0 {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestRBEWorkerEnvSync runs tools/rbe/worker-env-sync against a local
// gascity (file:// URLs): quiet success when the manifest and pin match
// gascity's at REF, and otherwise exit 1 with the diff and the pin to commit
// in the output and step summary, for a manifest or a pin that differs.
func TestRBEWorkerEnvSync(t *testing.T) {
	requireHostTool(t, "curl")
	requireHostTool(t, "bash")
	script := filepath.Join(bazelPolicyRoot(t), rbeWorkerEnvSync)
	manifest := "arch x86_64\npkg git 2\n"
	sum := sha256.Sum256([]byte(manifest))
	pin := "sha256:" + hex.EncodeToString(sum[:])
	build := func(pin string) string {
		return "platform(\n    name = \"rbe_worker\",\n    exec_properties = {\n        \"worker-env\": \"" + pin + "\",\n    },\n)\n"
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gascity := t.TempDir()
	write(filepath.Join(gascity, "main", "tools/rbe/worker-env.txt"), manifest)
	write(filepath.Join(gascity, "main", "platforms/BUILD.bazel"), build(pin))

	run := func(ourManifest, ourPin string) (string, string, error) {
		t.Helper()
		beads := t.TempDir()
		write(filepath.Join(beads, rbeWorkerEnvManifest), ourManifest)
		write(filepath.Join(beads, rbeWorkerPlatformBuild), build(ourPin))
		summary := filepath.Join(beads, "summary.md")
		cmd := exec.Command("bash", script)
		cmd.Dir = beads
		cmd.Env = append(os.Environ(), "WORKER_ENV_SYNC_URL=file://"+gascity, "GITHUB_STEP_SUMMARY="+summary)
		out, err := cmd.CombinedOutput()
		b, _ := os.ReadFile(summary)
		return string(out), string(b), err
	}

	out, summary, err := run(manifest, pin)
	if err != nil || !strings.Contains(out, "worker-env: in step with gascity main ("+pin+")") || summary != "" {
		t.Fatalf("in step: %v\n%s\nsummary:\n%s", err, out, summary)
	}

	stale := "arch x86_64\npkg git 1:2.55.0-0ppa1~ubuntu24.04.2\n"
	staleSum := sha256.Sum256([]byte(stale))
	stalePin := "sha256:" + hex.EncodeToString(staleSum[:])
	for name, c := range map[string][2]string{
		"manifest and pin": {stale, stalePin},
		"pin only":         {manifest, stalePin},
		"manifest only":    {stale, pin},
	} {
		out, summary, err := run(c[0], c[1])
		if err == nil {
			t.Errorf("%s out of step: succeeded\n%s", name, out)
			continue
		}
		for _, want := range []string{
			"::error title=rbe worker-env out of step::beads pins worker-env=" + c[1] + ", gascity main pins " + pin,
			"        \"worker-env\": \"" + pin + "\",\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q:\n%s", name, want, out)
			}
		}
		if !strings.Contains(summary, "### rbe worker-env out of step with gascity") {
			t.Errorf("%s: no step summary:\n%s", name, summary)
		}
		if c[0] != manifest && !strings.Contains(out, "-pkg git 1:2.55.0-0ppa1~ubuntu24.04.2\n+pkg git 2\n") {
			t.Errorf("%s: no manifest diff:\n%s", name, out)
		}
	}

	// gascity unreachable (a REF that does not exist): an error, never "in step".
	cmd := exec.Command("bash", script, "no-such-ref")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "WORKER_ENV_SYNC_URL=file://"+gascity)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("worker-env-sync of a missing ref succeeded:\n%s", out)
	}
}
