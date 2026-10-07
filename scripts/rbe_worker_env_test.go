package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"testing"
)

// Test actions exec host tools and load the shared libraries of every cgo
// binary they link, so the remote worker host is an input to every result
// rbe-west caches. //platforms:rbe_worker puts it into the action key: its
// worker-env exec property is the sha256 of tools/rbe/worker-env.txt, and
// rbe-west's schedulers run an action only on a worker that advertises that
// exact value. The OSS pool and its manifest are gastownhall/gascity's
// (tools/rbe/worker-env, blacksmith-worker.sh); beads commits a copy of the
// manifest so the pin is reviewable and moves only with it.

const (
	rbeWorkerPlatformBuild = "platforms/BUILD.bazel"
	rbeWorkerEnvManifest   = "tools/rbe/worker-env.txt"
	rbeWorkerPlatformFlag  = "--extra_execution_platforms=//platforms:rbe_worker"
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
