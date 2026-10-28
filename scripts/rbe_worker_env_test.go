package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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
	rbeWorkerEnvSyncRef    = "tools/rbe/worker-env-sync-ref"
	rbeWorkerEnvSyncIssue  = "tools/rbe/worker-env-sync-issue"
	// bazel.yml's rbe job checks out only what worker-env-sync compares.
	bazelRBESparseCheckout = "tools/rbe\nplatforms\n"
	rbeWorkerEnvEnabledIf  = "${{ steps.decide.outputs.enabled == 'true' }}"
	nightlyWorkerEnvJob    = "rbe-worker-env-sync"
	nightlyWorkerEnvReport = "${{ runner.temp }}/worker-env-sync.md"
)

// gascity's grace window (gascity tools/rbe/worker-env-grace). A push to
// gascity's main that moves the pin publishes a record, worker-env-grace.txt
// alone at the root of gascity's rbe/worker-env-grace branch: the pin before
// (previous) and, for a re-pin it can roll back (a Go release), a window,
// since to until, in which rbe-west keeps workers of the previous pin.
// bazel.yml's preflight asks worker-env-sync to accept beads' copy at that
// previous pin inside the window (WORKER_ENV_SYNC_GRACE=1, in mode remote
// while the repository variable RBE_WORKER_ENV_GRACE is true), so beads PRs
// keep building until beads' own sync PR lands.
const (
	rbeWorkerEnvGraceBranch = "rbe/worker-env-grace"
	rbeWorkerEnvGraceFile   = "worker-env-grace.txt"
	rbeWorkerEnvGraceEnv    = "${{ steps.decide.outputs.mode == 'remote' && vars.RBE_WORKER_ENV_GRACE == 'true' && '1' || '' }}"
	// The one line worker-env-sync adds, asked for the window, to an
	// out-of-step result the window does not cover.
	rbeWorkerEnvGraceRefused = "worker-env grace window does not apply: "
	// The tests' clock: gascity#8165's record would have been published at
	// rbeTestGraceSince (2026-10-21T01:10:30Z), and the tests read it 10
	// minutes later.
	rbeTestGraceSince int64 = 1792545030
	rbeTestGraceNow         = rbeTestGraceSince + 600
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

// rbeWorkerEnvFixture is a local gascity, served the way the scripts read
// the real one. git is a repository whose main (which worker-env-sync-ref
// resolves with git ls-remote, WORKER_ENV_SYNC_GIT_URL) commits manifest
// and a platforms/BUILD.bazel pinning it at pin, on top of an older commit,
// staleSHA, that pins rbeTestStaleManifest. raw is raw.githubusercontent.com's
// layout of each commit, <raw>/<sha>/<path> (WORKER_ENV_SYNC_URL, file://).
// Two traps catch a script that does not compare with the commit main
// names: <raw>/main still serves the older commit's files, as the CDN does
// for a few minutes after a push (gascity#8165 on Oct 21), and a decoy
// branch, decoy/refs/heads/main, also points at the older commit, which
// git ls-remote's pattern refs/heads/main matches too (by its trailing path
// components) and lists first. beads lays out a beads checkout with a given
// manifest and pin plus the real tools/rbe scripts, as the workflows run
// them. gascity's grace record (rbe/worker-env-grace) exists only once a
// test publishes one (grace), and has the same two traps: <raw>'s branch
// URL serves a record that would cover a beads copy at the stale pin, and
// decoy/refs/heads/rbe/worker-env-grace names a commit with no record.
type rbeWorkerEnvFixture struct {
	raw, git                     string
	manifest, pin, sha, staleSHA string
	stale                        string // the stale commit's manifest
}

func rbeTestPin(manifest string) string {
	sum := sha256.Sum256([]byte(manifest))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func rbeTestPlatformBuild(pin string) string {
	return "platform(\n    name = \"rbe_worker\",\n    exec_properties = {\n        \"worker-env\": \"" + pin + "\",\n    },\n)\n"
}

func rbeTestWrite(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// rbeTestGit runs git in dir as a fixture author, with neither the host's
// system nor its global config (no signing, hooks or URL rewrites), and
// returns its stdout without the trailing newline.
func rbeTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return rbeTestGitInput(t, dir, "", args...)
}

// rbeTestGitInput is rbeTestGit with stdin.
func rbeTestGitInput(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, stderr.String())
	}
	return strings.TrimSuffix(string(out), "\n")
}

func newRBEWorkerEnvFixture(t *testing.T) rbeWorkerEnvFixture {
	t.Helper()
	manifest := "arch x86_64\npkg git 2\n"
	f := newRBEWorkerEnvGascity(t, manifest, rbeTestPlatformBuild(rbeTestPin(manifest)))
	f.pin = rbeTestPin(manifest)
	rbeTestWrite(t, filepath.Join(f.raw, rbeWorkerEnvGraceBranch, rbeWorkerEnvGraceFile),
		rbeTestGraceOpen(f.pin, f.sha, rbeTestPin(rbeTestStaleManifest), rbeTestGraceSince).String(), 0o644)
	rbeTestGit(t, f.git, "update-ref", "refs/heads/decoy/refs/heads/"+rbeWorkerEnvGraceBranch, f.staleSHA)
	return f
}

// rbeTestGraceManifest is a manifest with a Go line, as gascity's is: a
// re-pin that moves only that line is one gascity can publish a grace
// window for.
func rbeTestGraceManifest(goVersion string) string {
	return "arch x86_64\ngo go version go" + goVersion + " linux/amd64\npkg git 2\n"
}

// newRBEWorkerEnvGraceFixture is newRBEWorkerEnvFixture for a Go re-pin:
// main's manifest is the stale commit's with only its go line moved
// (1.26.9 to 1.26.10), as on 2026-10-21 (gascity#8165), with the same
// traps for the grace record.
func newRBEWorkerEnvGraceFixture(t *testing.T) rbeWorkerEnvFixture {
	t.Helper()
	stale, manifest := rbeTestGraceManifest("1.26.9"), rbeTestGraceManifest("1.26.10")
	f := newRBEWorkerEnvGascityFrom(t, stale, manifest, rbeTestPlatformBuild(rbeTestPin(manifest)))
	f.pin = rbeTestPin(manifest)
	rbeTestWrite(t, filepath.Join(f.raw, rbeWorkerEnvGraceBranch, rbeWorkerEnvGraceFile),
		rbeTestGraceOpen(f.pin, f.sha, rbeTestPin(stale), rbeTestGraceSince).String(), 0o644)
	rbeTestGit(t, f.git, "update-ref", "refs/heads/decoy/refs/heads/"+rbeWorkerEnvGraceBranch, f.staleSHA)
	return f
}

// grace publishes record as gascity's writer does: a new commit holding
// worker-env-grace.txt alone (graceCommit), which rbe/worker-env-grace then
// names. It returns the commit.
func (f rbeWorkerEnvFixture) grace(t *testing.T, record string) string {
	t.Helper()
	commit := f.graceCommit(t, record)
	rbeTestGit(t, f.git, "update-ref", "refs/heads/"+rbeWorkerEnvGraceBranch, commit)
	return commit
}

// graceCommit commits record as worker-env-grace.txt alone, laid out at
// <raw>/<commit>/ like main's commits, without moving any branch, and
// returns the commit.
func (f rbeWorkerEnvFixture) graceCommit(t *testing.T, record string) string {
	t.Helper()
	blob := rbeTestGitInput(t, f.git, record, "hash-object", "-w", "--stdin")
	tree := rbeTestGitInput(t, f.git, "100644 blob "+blob+"\t"+rbeWorkerEnvGraceFile+"\n", "mktree")
	commit := rbeTestGit(t, f.git, "commit-tree", tree, "-m", "worker-env grace")
	rbeTestWrite(t, filepath.Join(f.raw, commit, rbeWorkerEnvGraceFile), record, 0o644)
	return commit
}

// rbeTestGraceRecord is a grace record's "key value" lines, in order.
type rbeTestGraceRecord [][2]string

// rbeTestGraceOpen is the record gascity publishes at since for a re-pin
// from previous to pin at its commit sha: beads may pin previous for two
// hours, and rbe-west serves it an hour longer.
func rbeTestGraceOpen(pin, sha, previous string, since int64) rbeTestGraceRecord {
	return rbeTestGraceRecord{
		{"version", "1"}, {"pin", pin}, {"sha", sha}, {"previous", previous}, {"go", "1.26.9"},
		{"since", strconv.FormatInt(since, 10)},
		{"until", strconv.FormatInt(since+7200, 10)},
		{"serve-until", strconv.FormatInt(since+7200+3600, 10)},
	}
}

// set is r with key's value replaced, or with key added if r has none.
func (r rbeTestGraceRecord) set(key, value string) rbeTestGraceRecord {
	out := slices.Clone(r)
	for i := range out {
		if out[i][0] == key {
			out[i][1] = value
			return out
		}
	}
	return append(out, [2]string{key, value})
}

// add is r with one more line, even for a key r has.
func (r rbeTestGraceRecord) add(key, value string) rbeTestGraceRecord {
	return append(slices.Clone(r), [2]string{key, value})
}

// drop is r without key's line.
func (r rbeTestGraceRecord) drop(key string) rbeTestGraceRecord {
	return slices.DeleteFunc(slices.Clone(r), func(kv [2]string) bool { return kv[0] == key })
}

// String renders r as gascity writes it: a comment, then a line per key.
func (r rbeTestGraceRecord) String() string {
	var b strings.Builder
	b.WriteString("# rbe-west worker-env grace window (gascity tools/rbe/worker-env-grace)\n")
	for _, kv := range r {
		b.WriteString(kv[0] + " " + kv[1] + "\n")
	}
	return b.String()
}

// rbeTestISO is unix time sec in UTC ISO 8601, as worker-env-sync prints it.
func rbeTestISO(sec int64) string {
	return time.Unix(sec, 0).UTC().Format("2006-01-02T15:04:05Z")
}

// newRBEWorkerEnvGascity: a fixture whose main commits manifest and build
// (gascity's platforms/BUILD.bazel) over the stale commit. pin is left
// empty: build need not pin manifest, or pin anything.
func newRBEWorkerEnvGascity(t *testing.T, manifest, build string) rbeWorkerEnvFixture {
	t.Helper()
	return newRBEWorkerEnvGascityFrom(t, rbeTestStaleManifest, manifest, build)
}

// newRBEWorkerEnvGascityFrom is newRBEWorkerEnvGascity over a stale commit
// that pins stale.
func newRBEWorkerEnvGascityFrom(t *testing.T, stale, manifest, build string) rbeWorkerEnvFixture {
	t.Helper()
	requireHostTool(t, "curl")
	requireHostTool(t, "bash")
	requireHostTool(t, "git")
	f := rbeWorkerEnvFixture{raw: t.TempDir(), git: t.TempDir(), manifest: manifest, stale: stale}
	rbeTestGit(t, f.git, "init", "-q", "-b", "main")
	commit := func(manifest, build string) string {
		rbeTestWrite(t, filepath.Join(f.git, rbeWorkerEnvManifest), manifest, 0o644)
		rbeTestWrite(t, filepath.Join(f.git, rbeWorkerPlatformBuild), build, 0o644)
		rbeTestGit(t, f.git, "add", "-A")
		rbeTestGit(t, f.git, "commit", "-q", "-m", "worker-env")
		sha := rbeTestGit(t, f.git, "rev-parse", "HEAD")
		rbeTestWrite(t, filepath.Join(f.raw, sha, rbeWorkerEnvManifest), manifest, 0o644)
		rbeTestWrite(t, filepath.Join(f.raw, sha, rbeWorkerPlatformBuild), build, 0o644)
		return sha
	}
	staleBuild := rbeTestPlatformBuild(rbeTestPin(stale))
	f.staleSHA = commit(stale, staleBuild)
	f.sha = commit(manifest, build)
	rbeTestWrite(t, filepath.Join(f.raw, "main", rbeWorkerEnvManifest), stale, 0o644)
	rbeTestWrite(t, filepath.Join(f.raw, "main", rbeWorkerPlatformBuild), staleBuild, 0o644)
	rbeTestGit(t, f.git, "update-ref", "refs/heads/decoy/refs/heads/main", f.staleSHA)
	return f
}

// beads lays out a beads checkout pinning manifest at pin, with the real
// worker-env-sync scripts, and returns its root.
func (f rbeWorkerEnvFixture) beads(t *testing.T, manifest, pin string) string {
	t.Helper()
	root := bazelPolicyRoot(t)
	dir := t.TempDir()
	rbeTestWrite(t, filepath.Join(dir, rbeWorkerEnvManifest), manifest, 0o644)
	rbeTestWrite(t, filepath.Join(dir, rbeWorkerPlatformBuild), rbeTestPlatformBuild(pin), 0o644)
	for _, script := range []string{rbeWorkerEnvSync, rbeWorkerEnvSyncRef, rbeWorkerEnvSyncIssue} {
		rbeTestWrite(t, filepath.Join(dir, script), readPolicyFile(t, root, script), 0o755)
	}
	return dir
}

// env is what points the scripts at this gascity instead of GitHub's.
func (f rbeWorkerEnvFixture) env() []string {
	return []string{"WORKER_ENV_SYNC_URL=file://" + f.raw, "WORKER_ENV_SYNC_GIT_URL=file://" + f.git,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
}

// run runs a workflow step's script (bash -e, as GitHub does) in dir with
// gascity served from the fixture plus env, and returns its exit code and
// output.
func (f rbeWorkerEnvFixture) run(t *testing.T, dir, script string, env ...string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append(f.env(), env...)...)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), string(out)
	default:
		t.Fatalf("run step: %v\n%s", err, out)
		return -1, ""
	}
}

const rbeTestStaleManifest = "arch x86_64\npkg git 1:2.55.0-0ppa1~ubuntu24.04.2\n"

// TestRBEWorkerEnvSync runs tools/rbe/worker-env-sync against a local
// gascity: quiet success when the manifest and pin match gascity's at REF;
// exit 1 with the diff and the pin to commit in the output, step summary and
// WORKER_ENV_SYNC_REPORT for a manifest or a pin that differs; and exit 2
// ("unknown", a warning) when gascity cannot be read, never "in step".
// REF is compared at the commit it names (worker-env-sync-ref): the
// fixture's raw main and its decoy branch both serve the commit before
// main's, so the in-step case passes only when the copy is compared with
// main's own commit, and a copy that matches only the CDN's stale main is
// out of step.
func TestRBEWorkerEnvSync(t *testing.T) {
	f := newRBEWorkerEnvFixture(t)
	run := func(ourManifest, ourPin string) (int, string, string, string) {
		t.Helper()
		beads := f.beads(t, ourManifest, ourPin)
		summary := filepath.Join(beads, "summary.md")
		report := filepath.Join(beads, "report.md")
		code, out := f.run(t, beads, "tools/rbe/worker-env-sync\n", "GITHUB_STEP_SUMMARY="+summary, "WORKER_ENV_SYNC_REPORT="+report)
		s, _ := os.ReadFile(summary)
		r, _ := os.ReadFile(report)
		return code, out, string(s), string(r)
	}

	code, out, summary, report := run(f.manifest, f.pin)
	if code != 0 || !strings.Contains(out, "worker-env: in step with gascity main ("+f.pin+") at gascity commit "+f.sha+"\n") || summary != "" || report != "" {
		t.Fatalf("in step: exit %d\n%s\nsummary:\n%s\nreport:\n%s", code, out, summary, report)
	}

	stalePin := rbeTestPin(rbeTestStaleManifest)
	for name, c := range map[string][2]string{
		"manifest and pin": {rbeTestStaleManifest, stalePin},
		"pin only":         {f.manifest, stalePin},
		"manifest only":    {rbeTestStaleManifest, f.pin},
	} {
		code, out, summary, report := run(c[0], c[1])
		if code != 1 {
			t.Errorf("%s out of step: exit %d, want 1\n%s", name, code, out)
			continue
		}
		for _, want := range []string{
			"::error title=rbe worker-env out of step::beads pins worker-env=" + c[1] + ", gascity main pins " + f.pin,
			"beads' worker-env pin is out of step with gascity main",
			"        \"worker-env\": \"" + f.pin + "\",\n",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q:\n%s", name, want, out)
			}
		}
		for what, md := range map[string]string{"step summary": summary, "report": report} {
			if !strings.Contains(md, "### rbe worker-env out of step with gascity") || !strings.Contains(md, "\"worker-env\": \""+f.pin+"\"") ||
				!strings.Contains(md, "gascity `main` (commit `"+f.sha+"`) pins") {
				t.Errorf("%s: no %s naming gascity's commit:\n%s", name, what, md)
			}
		}
		if c[0] != f.manifest && !strings.Contains(out, "-pkg git 1:2.55.0-0ppa1~ubuntu24.04.2\n+pkg git 2\n") {
			t.Errorf("%s: no manifest diff:\n%s", name, out)
		}
	}

	// A REF that is a commit is compared at that commit, with no lookup:
	// gascity's git endpoint unreachable does not matter.
	beads := f.beads(t, rbeTestStaleManifest, stalePin)
	code, out = f.run(t, beads, `tools/rbe/worker-env-sync "$REF"`+"\n", "REF="+f.staleSHA, "WORKER_ENV_SYNC_GIT_URL=file:///nonexistent-gascity")
	if code != 0 || !strings.Contains(out, "worker-env: in step with gascity "+f.staleSHA+" ("+stalePin+") at gascity commit "+f.staleSHA+"\n") {
		t.Errorf("worker-env-sync at a commit: exit %d, want 0, in step with that commit:\n%s", code, out)
	}

	// gascity unreachable (a REF that does not exist, gascity's git endpoint
	// or its raw files out of reach): exit 2 with a warning, never "in
	// step" and never "out of step".
	beads = f.beads(t, f.manifest, f.pin)
	for name, env := range map[string][]string{
		"missing ref":     {"REF=no-such-ref"},
		"unreachable":     {"REF=main", "WORKER_ENV_SYNC_URL=file:///nonexistent-gascity"},
		"git unreachable": {"REF=main", "WORKER_ENV_SYNC_GIT_URL=file:///nonexistent-gascity"},
	} {
		code, out := f.run(t, beads, `tools/rbe/worker-env-sync "$REF"`+"\n", env...)
		if code != 2 || !strings.Contains(out, "::warning title=rbe worker-env unknown::") || strings.Contains(out, "in step") || strings.Contains(out, "::error") {
			t.Errorf("worker-env-sync, %s: exit %d, want 2 with an unknown warning:\n%s", name, code, out)
		}
	}
}

// TestRBEWorkerEnvSyncGrace: gascity's grace window. Asked for it
// (WORKER_ENV_SYNC_GRACE=1, which only bazel.yml's preflight sets), a beads
// copy out of step with gascity at REF because it still pins the previous
// pin of gascity's latest re-pin, inside that re-pin's window, is covered:
// exit 3, a notice and a step-summary section naming the window's end, and
// no report. Anything else is out of step exactly as without the variable
// (exit 1, the same output, summary and report) plus one line, in the
// output and the summary, saying why the window does not apply: no record,
// an unreadable or malformed one, previous none, another pin, a window not
// open yet (beyond 5 minutes of clock skew), closed, or longer than two
// hours. A record for a pin other than gascity's (an earlier re-pin's,
// before gascity publishes the new one) is read again every 15 seconds for
// up to WORKER_ENV_SYNC_GRACE_WAIT (default 150), a line per wait; nothing
// else waits. A failure to read the record is "not covered", never
// "unknown", and record text is printed with its workflow commands broken.
// The record is read at the commit rbe/worker-env-grace names: the
// fixture's CDN branch URL serves a covering record and its decoy branch
// names a commit with none. Without the variable, or with any value but 1,
// a covering record changes nothing.
func TestRBEWorkerEnvSyncGrace(t *testing.T) {
	f := newRBEWorkerEnvGraceFixture(t)
	stalePin := rbeTestPin(f.stale)
	otherPin := rbeTestPin("arch x86_64\npkg git 3\n")
	beads := f.beads(t, f.stale, stalePin)
	sec := func(n int64) string { return strconv.FormatInt(n, 10) }
	at := func(n int64) string { return "WORKER_ENV_SYNC_NOW=" + sec(n) }
	const grace = "WORKER_ENV_SYNC_GRACE=1"
	// A stand-in sleep, first on PATH: it logs how long it was asked to
	// sleep and returns at once, after pointing rbe/worker-env-grace at
	// GRACE_PUBLISH if that is set (gascity's grace job publishing while
	// worker-env-sync waits).
	bin := t.TempDir()
	rbeTestWrite(t, filepath.Join(bin, "sleep"), `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$SLEEP_LOG"
if [ -n "${GRACE_PUBLISH:-}" ]; then
	git -C "$GRACE_GIT" update-ref refs/heads/`+rbeWorkerEnvGraceBranch+` "$GRACE_PUBLISH"
fi
`, 0o755)
	type result struct {
		code                       int
		out, summary, report, naps string
		reported                   bool
	}
	run := func(dir string, env ...string) result {
		t.Helper()
		tmp := t.TempDir()
		summary, report, naps := filepath.Join(tmp, "summary.md"), filepath.Join(tmp, "report.md"), filepath.Join(tmp, "naps")
		env = append([]string{"GITHUB_STEP_SUMMARY=" + summary, "WORKER_ENV_SYNC_REPORT=" + report, at(rbeTestGraceNow),
			"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "SLEEP_LOG=" + naps, "GRACE_GIT=" + f.git}, env...)
		code, out := f.run(t, dir, rbeWorkerEnvSync+"\n", env...)
		s, _ := os.ReadFile(summary)
		r, err := os.ReadFile(report)
		n, _ := os.ReadFile(naps)
		return result{code, out, string(s), string(r), string(n), err == nil}
	}
	// waiting strips the lines worker-env-sync prints while it waits for
	// gascity's record of its pin, one per nap, from the start of out.
	waiting := func(name, out, naps string) (string, bool) {
		t.Helper()
		for range strings.Count(naps, "\n") {
			line, rest, _ := strings.Cut(out, "\n")
			if !strings.HasPrefix(line, "worker-env: waiting for gascity's grace record for "+f.pin+", ") {
				t.Errorf("%s: want a waiting line per nap (%q) first; got:\n%s", name, naps, out)
				return "", false
			}
			out = rest
		}
		return out, true
	}

	// Today's out-of-step result, strict: no window asked for, and gascity
	// has published no record yet.
	strict := run(beads)
	if strict.code != 1 || !strict.reported || !strings.Contains(strict.out, "::error title=rbe worker-env out of step::") {
		t.Fatalf("out of step, strict: exit %d, report written %v, want exit 1 and the report:\n%s", strict.code, strict.reported, strict.out)
	}
	refused := func(name string, got result, why, naps string) {
		t.Helper()
		if got.naps != naps {
			t.Errorf("%s: slept %q, want %q", name, got.naps, naps)
		}
		out, ok := waiting(name, got.out, naps)
		if !ok {
			return
		}
		extra, ok := strings.CutPrefix(out, strict.out)
		line, rest, _ := strings.Cut(extra, "\n")
		if got.code != 1 || !ok || rest != "" || !strings.HasPrefix(line, rbeWorkerEnvGraceRefused) || !strings.Contains(line, why) {
			t.Errorf("%s: exit %d, want 1 with today's output plus one line %q...%q; got:\n%s", name, got.code, rbeWorkerEnvGraceRefused, why, got.out)
			return
		}
		// The summary says the same in a code span: the reason is gascity's
		// text, never markup there.
		spanned := rbeWorkerEnvGraceRefused + "`" + strings.TrimPrefix(line, rbeWorkerEnvGraceRefused) + "`"
		if got.summary != strict.summary+"\n"+spanned+"\n" || got.report != strict.report || !got.reported {
			t.Errorf("%s: want today's summary plus %q and today's report; summary:\n%s\nreport:\n%s", name, spanned, got.summary, got.report)
		}
		if strings.Contains(got.out, "##[") {
			t.Errorf("%s: output holds a workflow command opener (##[):\n%s", name, got.out)
		}
	}
	covered := func(name string, got result, until int64, naps string) {
		t.Helper()
		end := rbeTestISO(until)
		notice := "::notice title=rbe worker-env grace::beads pins gascity's previous worker-env pin (" + stalePin + "): gascity re-pinned at commit " + f.sha +
			"; beads may pin the previous pin until " + end + ", and rbe-west serves it until "
		line := "worker-env: inside gascity's grace window: beads pins gascity's previous pin (" + stalePin + "), gascity main pins " + f.pin
		if got.naps != naps {
			t.Errorf("%s: slept %q, want %q", name, got.naps, naps)
		}
		out, ok := waiting(name, got.out, naps)
		if !ok {
			return
		}
		if got.code != 3 || !strings.HasPrefix(out, line) || !strings.Contains(out, "\n"+notice) || strings.Contains(out, "::error") || strings.Contains(out, "out of step with gascity") {
			t.Errorf("%s: exit %d, want 3 with %q and %q, and no error:\n%s", name, got.code, line, notice, got.out)
		}
		if !strings.Contains(got.summary, "### rbe worker-env in gascity's grace window") || !strings.Contains(got.summary, "`worker-env="+stalePin+"`") ||
			!strings.Contains(got.summary, end) || strings.Contains(got.summary, "out of step with gascity") || strings.Contains(got.summary, "waiting") {
			t.Errorf("%s: no grace window section ending %s in the summary:\n%s", name, end, got.summary)
		}
		if got.reported {
			t.Errorf("%s: wrote the out-of-step report:\n%s", name, got.report)
		}
	}

	// No branch: decided at once, no wait.
	refused("no record", run(beads, grace), "no record: gascity has no branch rbe/worker-env-grace", "")

	// A covering record: the strict result is still today's, byte for
	// byte, with WORKER_ENV_SYNC_GRACE anything but 1.
	open := rbeTestGraceOpen(f.pin, f.sha, stalePin, rbeTestGraceSince)
	until := rbeTestGraceSince + 7200
	f.grace(t, open.String())
	for _, env := range [][]string{nil, {"WORKER_ENV_SYNC_GRACE="}, {"WORKER_ENV_SYNC_GRACE=0"}, {"WORKER_ENV_SYNC_GRACE=true"}} {
		if got := run(beads, env...); got != strict {
			t.Errorf("covering record, %v: exit %d, want today's strict result (exit 1):\n%s\nsummary:\n%s", env, got.code, got.out, got.summary)
		}
	}
	covered("open", run(beads, grace), until, "")

	// In step, or unknown, the window is never consulted.
	if got := run(f.beads(t, f.manifest, f.pin), grace); got.code != 0 || strings.Contains(got.out, "grace") || got.summary != "" {
		t.Errorf("in step, window asked for: exit %d, want 0 and no grace output:\n%s\nsummary:\n%s", got.code, got.out, got.summary)
	}
	if got := run(beads, grace, "WORKER_ENV_SYNC_URL=file:///nonexistent-gascity"); got.code != 2 || strings.Contains(got.out, "grace") {
		t.Errorf("gascity unreachable, window asked for: exit %d, want 2 and no grace output:\n%s", got.code, got.out)
	}

	for _, c := range []struct {
		name, record string
		env          []string
		until        int64
	}{
		{"opens within the clock skew", open.set("since", sec(rbeTestGraceNow+300)).set("until", sec(rbeTestGraceNow+300+7200)).String(), nil, rbeTestGraceNow + 300 + 7200},
		{"its last second", open.String(), []string{at(until - 1)}, until},
		{"a shorter window", open.set("until", sec(rbeTestGraceSince+3600)).set("serve-until", sec(rbeTestGraceSince+7200)).String(), nil, rbeTestGraceSince + 3600},
		{"comments and blank lines", "\n" + open.String() + "\n# serve-until is rbe-west's\n\n", nil, until},
		{"no newline at the end", strings.TrimSuffix(open.String(), "\n"), nil, until},
	} {
		f.grace(t, c.record)
		covered(c.name, run(beads, append([]string{grace}, c.env...)...), c.until, "")
	}

	// gascity publishes a re-pin's record about a minute after the re-pin:
	// until then the branch holds an earlier re-pin's (here from otherPin
	// to the stale pin, a day before). Published while worker-env-sync
	// waits, the new record covers.
	earlier := rbeTestGraceOpen(stalePin, f.staleSHA, otherPin, rbeTestGraceSince-86400)
	f.grace(t, earlier.String())
	covered("published while waiting", run(beads, grace, "GRACE_PUBLISH="+f.graceCommit(t, open.String())), until, "15\n")

	noWindow := rbeTestGraceRecord{{"version", "1"}, {"pin", f.pin}, {"sha", f.sha}, {"previous", "none"},
		{"reason", "the re-pin moves more than the go line: -dolt dolt version 2.1.8, +dolt dolt version 2.1.9"}}
	notPublished := "other pin: gascity has not published a grace record for " + f.pin + " (its record is for "
	for _, c := range []struct {
		name, record string
		env          []string
		why, naps    string
	}{
		{"unknown key", open.add("color", "blue").String(), nil, "malformed: unknown key 'color'", ""},
		{"repeated key", open.add("go", "1.26.8").String(), nil, "malformed: repeated key 'go'", ""},
		{"version 2", open.set("version", "2").String(), nil, "malformed: version '2', want 1", ""},
		{"missing key", open.drop("until").String(), nil, "malformed: no until", ""},
		{"no version", open.drop("version").String(), nil, "malformed: no version", ""},
		{"a key without a value", open.String() + "reason\n", nil, `malformed: line 'reason' is not "key value"`, ""},
		{"a pin that is not one", open.set("pin", "sha256:1234").String(), nil, "malformed: pin 'sha256:1234' is not a sha256 pin", ""},
		{"not unix seconds", open.set("since", "soon").String(), nil, "malformed: since 'soon' is not unix seconds", ""},
		{"previous is the pin", open.set("previous", f.pin).String(), nil, "malformed: previous is the pin", ""},
		{"a reason with a window", open.add("reason", "why not").String(), nil, "malformed: reason with a previous pin", ""},
		{"previous none with a window", open.set("previous", "none").String(), nil, "malformed: go with previous none", ""},
		{"previous none without a reason", noWindow.drop("reason").String(), nil, "malformed: previous none without a reason", ""},
		{"previous none", noWindow.String(), nil, "previous none: the re-pin moves more than the go line: -dolt dolt version 2.1.8, +dolt dolt version 2.1.9", ""},
		{"previous none, workflow commands in its reason", noWindow.set("reason", "see ##[warning]x::y").String(), nil, "previous none: see # #[warning]x: :y", ""},
		// The bytes: printable ASCII and newlines, at most 64 KiB, as
		// gascity's and rbe-west's readers take them.
		{"a carriage return", strings.ReplaceAll(open.String(), "\n", "\r\n"), nil, "malformed: not printable ASCII", ""},
		{"a NUL in a comment", open.String() + "# a\x00b\n", nil, "malformed: not printable ASCII", ""},
		{"a NUL in a value", strings.Replace(open.String(), "version 1\n", "version 1\x00\n", 1), nil, "malformed: not printable ASCII", ""},
		{"a non-ASCII comment", "# café\n" + open.String(), nil, "malformed: not printable ASCII", ""},
		{"an escape in a reason", noWindow.set("reason", "x\x1b[31m").String(), nil, "malformed: not printable ASCII", ""},
		{"over 64 KiB", open.String() + strings.Repeat("# padding\n", 6600), nil, "malformed: over 64 KiB", ""},
		{"a whitespace-only line", open.String() + "   \n", nil, "malformed: unknown key ''", ""},
		{"a leading zero", open.set("since", "0"+sec(rbeTestGraceSince)).String(), nil, "malformed: since '0" + sec(rbeTestGraceSince) + "' is not unix seconds", ""},
		{"not a Go version", open.set("go", "1.26.9;x").String(), nil, "malformed: go '1.26.9;x' is not a Go version", ""},
		{"a drain over an hour", open.set("serve-until", sec(until+3601)).String(), nil, "malformed: serve-until - until is 3601s, want 0 to 3600", ""},
		{"serve-until before until", open.set("serve-until", sec(until-1)).String(), nil, "malformed: serve-until - until is -1s, want 0 to 3600", ""},
		// The record's previous host must be beads' copy: gascity's
		// manifest with only the go line rolled back, to the record's go.
		{"another Go", open.set("go", "1.26.8").String(), nil, "other Go: beads' manifest has Go '1.26.9', the record's previous host Go 1.26.8", ""},
		{"another previous pin", open.set("previous", otherPin).String(), nil, "other pin: the record's previous pin is " + otherPin + ", but beads pins " + stalePin, ""},
		{"longer than two hours", open.set("until", sec(rbeTestGraceSince+7201)).String(), nil, "refused: until - since is 7201s, want 1 to 7200", ""},
		{"until before since", open.set("until", sec(rbeTestGraceSince-1)).set("serve-until", sec(rbeTestGraceSince+3599)).String(), nil, "refused: until - since is -1s, want 1 to 7200", ""},
		{"not open yet", open.set("since", sec(rbeTestGraceNow+301)).set("until", sec(rbeTestGraceNow+301+7200)).String(), nil, "not open yet: it opens at " + rbeTestISO(rbeTestGraceNow+301), ""},
		{"closed", open.String(), []string{at(until)}, "closed at " + rbeTestISO(until), ""},
		{"a wait that is not seconds", open.String(), []string{"WORKER_ENV_SYNC_GRACE_WAIT=soon"}, "WORKER_ENV_SYNC_GRACE_WAIT 'soon' is not seconds", ""},
		// A record for another pin is read again every 15 seconds, for up
		// to WORKER_ENV_SYNC_GRACE_WAIT (default 150) seconds.
		{"another pin", open.set("pin", otherPin).String(), nil, notPublished + otherPin + "; waited 150s)", strings.Repeat("15\n", 10)},
		{"an earlier re-pin's record, still after the wait", earlier.String(), []string{"WORKER_ENV_SYNC_GRACE_WAIT=20"}, notPublished + stalePin + "; waited 20s)", "15\n5\n"},
		{"an earlier re-pin's record without a window", noWindow.set("pin", stalePin).String(), []string{"WORKER_ENV_SYNC_GRACE_WAIT=0"}, notPublished + stalePin + "; waited 0s)", ""},
	} {
		f.grace(t, c.record)
		refused(c.name, run(beads, append([]string{grace}, c.env...)...), c.why, c.naps)
	}

	// A beads copy that differs from gascity's in more than the go line
	// (here a package): a record naming its pin is still no window.
	moved := f.stale + "pkg tar 1.35\n"
	movedBeads := f.beads(t, moved, rbeTestPin(moved))
	f.grace(t, open.set("previous", rbeTestPin(moved)).String())
	movedStrict, movedGrace := run(movedBeads), run(movedBeads, grace)
	const movedWhy = rbeWorkerEnvGraceRefused + "other re-pin: gascity's manifest at main moved more than the go line"
	if movedGrace.code != 1 || !strings.HasPrefix(movedGrace.out, movedStrict.out) ||
		!strings.HasPrefix(strings.TrimPrefix(movedGrace.out, movedStrict.out), movedWhy) || !movedGrace.reported {
		t.Errorf("gascity's manifest moved more than Go: exit %d, want 1 with the strict output plus %q:\n%s", movedGrace.code, movedWhy, movedGrace.out)
	}

	// The record that replaces an earlier re-pin's is decided at once, so
	// a malformed one ends the wait.
	f.grace(t, earlier.String())
	refused("a malformed record published while waiting", run(beads, grace, "GRACE_PUBLISH="+f.graceCommit(t, open.add("color", "blue").String())),
		"malformed: unknown key 'color'", "15\n")

	// A record that cannot be read: rbe/worker-env-grace names a commit
	// whose file the raw base does not serve.
	commit := f.grace(t, open.set("go", "1.26.8").String())
	if err := os.Remove(filepath.Join(f.raw, commit, rbeWorkerEnvGraceFile)); err != nil {
		t.Fatal(err)
	}
	// Unreadable is worth reading again: it is read again every 15 seconds
	// until the wait is over (and then refused), or until it reads.
	refused("unreadable", run(beads, grace, "WORKER_ENV_SYNC_GRACE_WAIT=30"),
		"unreadable: could not fetch worker-env-grace.txt at gascity commit "+commit+" (curl exit ", "15\n15\n")
	covered("unreadable, then published while waiting", run(beads, grace, "GRACE_PUBLISH="+f.graceCommit(t, open.String())), until, "15\n")

	// gascity's manifest with the record's Go must hash to the record's
	// previous pin, the check rbe-west's previous-pin worker makes: here
	// beads' copy has the same lines, but its go line elsewhere, so its pin
	// is no host gascity's manifest rolls back to.
	reordered := strings.Replace(f.stale, "go go version go1.26.9 linux/amd64\n", "", 1) + "go go version go1.26.9 linux/amd64\n"
	f.grace(t, open.set("previous", rbeTestPin(reordered)).String())
	reorderedBeads := f.beads(t, reordered, rbeTestPin(reordered))
	reStrict, reGrace := run(reorderedBeads), run(reorderedBeads, grace)
	const reWhy = rbeWorkerEnvGraceRefused + "other host: gascity's manifest with Go 1.26.9 hashes to " + "sha256:"
	if reGrace.code != 1 || !strings.HasPrefix(reGrace.out, reStrict.out) || !strings.HasPrefix(strings.TrimPrefix(reGrace.out, reStrict.out), reWhy) {
		t.Errorf("a copy whose lines are gascity's in another order: exit %d, want 1 with %q...:\n%s", reGrace.code, reWhy, reGrace.out)
	}
}

// TestRBEWorkerEnvSyncRef: worker-env-sync-ref prints the gascity commit a
// REF names, on stdout and alone. A 40-hex REF is a commit already and is
// printed with no lookup; a branch is resolved with git ls-remote, by its
// exact name: the fixture's decoy/refs/heads/main also matches ls-remote's
// pattern, and is listed first. A branch gascity does not have, or a
// gascity it cannot reach, or one that does not answer within the lookup's
// bound, fails with nothing on stdout, so a caller never reads a partial
// answer as a commit.
func TestRBEWorkerEnvSyncRef(t *testing.T) {
	f := newRBEWorkerEnvFixture(t)
	dir := f.beads(t, f.manifest, f.pin)
	for _, c := range []struct {
		name, ref string
		env       []string
		ok        bool
		want      string
	}{
		{name: "branch", ref: "main", ok: true, want: f.sha + "\n"},
		{name: "default branch", ok: true, want: f.sha + "\n"},
		{name: "commit, no lookup", ref: f.staleSHA, env: []string{"WORKER_ENV_SYNC_GIT_URL=file:///nonexistent-gascity"}, ok: true, want: f.staleSHA + "\n"},
		{name: "missing branch", ref: "no-such-ref"},
		{name: "unreachable", ref: "main", env: []string{"WORKER_ENV_SYNC_GIT_URL=file:///nonexistent-gascity"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := []string{rbeWorkerEnvSyncRef}
			if c.ref != "" {
				args = append(args, c.ref)
			}
			cmd := exec.Command("bash", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), append(f.env(), c.env...)...)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if ok := err == nil; ok != c.ok || string(out) != c.want {
				t.Errorf("worker-env-sync-ref %q: err %v, stdout %q; want ok=%v, stdout %q\nstderr:\n%s", c.ref, err, out, c.ok, c.want, stderr.String())
			}
		})
	}

	// A gascity that takes the connection and never answers, as a stalled
	// github.com would, holds the lookup only until its bound: timeout's
	// exit 124, long before git's own 20-second low-speed limit, with
	// nothing on stdout.
	t.Run("hung", func(t *testing.T) {
		requireHostTool(t, "timeout")
		hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		t.Cleanup(hung.Close)
		cmd := exec.Command("bash", rbeWorkerEnvSyncRef, "main")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), append(f.env(), "WORKER_ENV_SYNC_GIT_URL="+hung.URL+"/gastownhall/gascity", "WORKER_ENV_SYNC_GIT_TIMEOUT=2")...)
		start := time.Now()
		out, err := cmd.Output()
		if took := time.Since(start); exitCode(err) != 124 || len(out) != 0 || took > 15*time.Second {
			t.Errorf("worker-env-sync-ref against a hung gascity: err %v after %s, stdout %q; want timeout's exit 124 within the bound and nothing on stdout", err, took.Round(time.Millisecond), out)
		}
	})
}

// TestBazelRBEWorkerEnvPreflight: when gascity re-pins and beads' copy falls
// behind, a remote-execution run fails in bazel.yml's rbe job (every lane
// needs it, so none starts and pr.yml's gate is red at once) with the
// actionable message, instead of every lane queueing on rbe-west until it
// times out. Only runs whose lanes execute remotely (enabled: remote,
// fork-ro, fork-rw; merge groups are remote) check; gascity unreachable
// warns and proceeds. In mode remote, with the repository variable
// RBE_WORKER_ENV_GRACE true, the check asks for gascity's grace window
// (WORKER_ENV_SYNC_GRACE=1): a copy at the previous pin of gascity's re-pin,
// inside the window, proceeds with a notice, on rbe-west's previous-pin
// workers; not in the fork modes (fork-ro's oss-fork pool has no
// previous-pin workers, and fork runs see no vars). The steps run after
// decide and set no output, so the mode the lanes read is decide's alone.
func TestBazelRBEWorkerEnvPreflight(t *testing.T) {
	job := readCIWorkflow(t, bazelWorkflowName).job(t, bazelRBEJobName)
	if len(job.Steps) != 4 || job.Steps[0].ID != "decide" || job.Steps[1].ID != bazelRRCModeStepID {
		t.Fatalf("%s steps = %d (first %q), want decide, rrc (TestBazelRRCModeStep), the checkout and the worker-env check", bazelRBEJobName, len(job.Steps), job.Steps[0].ID)
	}
	if !strings.Contains(job.Steps[0].Run, `case "$mode" in remote|fork-ro|fork-rw) enabled=true ;; esac`) {
		t.Errorf("%s decide no longer sets enabled exactly for the remote modes; the preflight's if depends on it", bazelRBEJobName)
	}
	checkout, check := job.Steps[2], job.Steps[3]
	if actionFamily(checkout.Uses) != "actions/checkout" || checkout.If != rbeWorkerEnvEnabledIf || checkout.With["sparse-checkout"] != bazelRBESparseCheckout {
		t.Errorf("%s step 3: uses %q, if %q, sparse-checkout %q; want a checkout of %q if %s",
			bazelRBEJobName, checkout.Uses, checkout.If, checkout.With["sparse-checkout"], bazelRBESparseCheckout, rbeWorkerEnvEnabledIf)
	}
	wantEnv := map[string]string{"MODE": "${{ steps.decide.outputs.mode }}", "WORKER_ENV_SYNC_GRACE": rbeWorkerEnvGraceEnv}
	if check.If != rbeWorkerEnvEnabledIf || check.ID != "" || check.Uses != "" || check.ContinueOnError != nil || !reflect.DeepEqual(check.Env, wantEnv) ||
		!strings.Contains(check.Run, "tools/rbe/worker-env-sync main") {
		t.Errorf("%s step 4: if %q, id %q, uses %q, continue-on-error %v, env %v; want if %s, no id, env %v, running tools/rbe/worker-env-sync main",
			bazelRBEJobName, check.If, check.ID, check.Uses, check.ContinueOnError, check.Env, rbeWorkerEnvEnabledIf, wantEnv)
	}

	// worker-env-sync waits up to WORKER_ENV_SYNC_GRACE_WAIT (its default
	// here) for gascity's record of a re-pin; the job leaves the comparison
	// two minutes more.
	if m := regexp.MustCompile(`WORKER_ENV_SYNC_GRACE_WAIT:-([0-9]+)`).FindStringSubmatch(readPolicyFile(t, bazelPolicyRoot(t), rbeWorkerEnvSync)); m == nil {
		t.Errorf("%s has no WORKER_ENV_SYNC_GRACE_WAIT default", rbeWorkerEnvSync)
	} else if wait, _ := strconv.Atoi(m[1]); job.TimeoutMinutes*60 < wait+120 {
		t.Errorf("%s timeout-minutes %d leaves less than two minutes beyond worker-env-sync's %ds wait for gascity's grace record", bazelRBEJobName, job.TimeoutMinutes, wait)
	}

	// WORKER_ENV_SYNC_GRACE as GitHub evaluates it: 1 in mode remote with
	// RBE_WORKER_ENV_GRACE true (== compares strings case-insensitively),
	// else empty, which worker-env-sync reads as strict. switchVar holds the
	// variable's value, or nothing while it is unset.
	graceFor := func(mode string, switchVar ...string) string {
		t.Helper()
		ctx := map[string]string{"steps.decide.outputs.mode": mode}
		for _, v := range switchVar {
			ctx["vars.RBE_WORKER_ENV_GRACE"] = v
		}
		v, err := evalGHExpr(check.Env["WORKER_ENV_SYNC_GRACE"], ctx)
		s, ok := v.(string)
		if err != nil || !ok {
			t.Fatalf("WORKER_ENV_SYNC_GRACE %q in mode %s: %#v, %v; want a string", check.Env["WORKER_ENV_SYNC_GRACE"], mode, v, err)
		}
		return s
	}
	on := []string{"true"}
	for _, c := range []struct {
		mode      string
		switchVar []string
		want      string
	}{
		{"remote", on, "1"},
		{"remote", []string{"True"}, "1"},
		{"remote", nil, ""},
		{"remote", []string{""}, ""},
		{"remote", []string{"false"}, ""},
		{"remote", []string{"1"}, ""},
		{"fork-rw", on, ""},
		{"fork-ro", on, ""},
		{"cache", on, ""},
	} {
		if got := graceFor(c.mode, c.switchVar...); got != c.want {
			t.Errorf("WORKER_ENV_SYNC_GRACE in mode %s with RBE_WORKER_ENV_GRACE %q (nil: unset) = %q, want %q", c.mode, c.switchVar, got, c.want)
		}
	}

	// The step's script, with the env GitHub gives it. gascity's record of
	// its re-pin from the stale pin to main's is open from
	// rbeTestGraceSince for two hours.
	f := newRBEWorkerEnvGraceFixture(t)
	stale := f.stale
	stalePin := rbeTestPin(stale)
	f.grace(t, rbeTestGraceOpen(f.pin, f.sha, stalePin, rbeTestGraceSince).String())
	const stopped = "::error title=Bazel lanes stopped before queueing::beads' worker-env pin is out of step with gascity main, so rbe-west has no worker for this run's remote actions (mode %s): copy tools/rbe/worker-env.txt and the worker-env pin in platforms/BUILD.bazel from gastownhall/gascity main"
	for _, c := range []struct {
		name, manifest, pin string
		mode                string
		switchVar           []string
		env                 []string
		code                int
		want                string
	}{
		{"in step", f.manifest, f.pin, "remote", on, nil, 0, "worker-env: in step with gascity main"},
		{"gascity re-pinned", stale, stalePin, "remote", nil, nil, 1, fmt.Sprintf(stopped, "remote")},
		{"gascity re-pinned, inside its grace window", stale, stalePin, "remote", on, nil, 0,
			"::notice title=rbe worker-env grace::beads' worker-env pin is gascity's previous pin, inside gascity's grace window: proceeding in mode remote"},
		{"gascity re-pinned, its grace window closed", stale, stalePin, "remote", on,
			[]string{"WORKER_ENV_SYNC_NOW=" + strconv.FormatInt(rbeTestGraceSince+7200, 10)}, 1, fmt.Sprintf(stopped, "remote")},
		{"gascity re-pinned, mode fork-rw", stale, stalePin, "fork-rw", on, nil, 1, fmt.Sprintf(stopped, "fork-rw")},
		{"gascity unreachable", stale, stalePin, "remote", on, []string{"WORKER_ENV_SYNC_URL=file:///nonexistent-gascity"}, 0,
			"::warning title=rbe worker-env not checked::could not compare beads' worker-env pin with gascity main (exit 2); proceeding in mode remote."},
	} {
		env := []string{"MODE=" + c.mode, "WORKER_ENV_SYNC_GRACE=" + graceFor(c.mode, c.switchVar...),
			"WORKER_ENV_SYNC_NOW=" + strconv.FormatInt(rbeTestGraceNow, 10), "GITHUB_STEP_SUMMARY=" + filepath.Join(t.TempDir(), "summary")}
		code, out := f.run(t, f.beads(t, c.manifest, c.pin), check.Run, append(env, c.env...)...)
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("rbe preflight, %s: exit %d, want %d with %q:\n%s", c.name, code, c.code, c.want, out)
		}
	}
}

// TestWorkerEnvSyncGraceStaysInThePreflight: WORKER_ENV_SYNC_GRACE, and
// the switch RBE_WORKER_ENV_GRACE behind it, appear in bazel.yml's
// worker-env preflight env and nowhere else under .github. The sync PR
// (worker-env-autosync.yml opens it, worker-env-sync-automerge.yml checks
// it against gascity main with worker-env-sync-verify) and nightly.yml's
// issue are for any copy out of step, at gascity's previous pin or not, so
// their worker-env-sync stays strict: exit 1.
func TestWorkerEnvSyncGraceStaysInThePreflight(t *testing.T) {
	const preflight = ".jobs." + bazelRBEJobName + ".steps[3].env.WORKER_ENV_SYNC_GRACE"
	mentions := func(s string) bool {
		return strings.Contains(s, "WORKER_ENV_SYNC_GRACE") || strings.Contains(s, "RBE_WORKER_ENV_GRACE")
	}
	root := sourceRepoRoot(t)
	read := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, ".github"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		read[rel] = true
		if rel == ".github/workflows/"+bazelWorkflowName {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if mentions(string(data)) {
			t.Errorf("%s mentions WORKER_ENV_SYNC_GRACE or RBE_WORKER_ENV_GRACE; only bazel.yml's worker-env preflight (%s) asks for gascity's grace window", rel, preflight)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{workerEnvAutosyncWorkflow, "nightly.yml", workerEnvAutomergeWorkflow, bazelWorkflowName} {
		if !read[".github/workflows/"+name] {
			t.Errorf(".github/workflows/%s was not among the files read", name)
		}
	}
	walkYAML(readYAMLNode(t, filepath.Join(".github", "workflows", bazelWorkflowName)), "", func(path string, key bool, value string) {
		if mentions(value) && path != preflight {
			t.Errorf("%s: %s holds %q; only the worker-env preflight's env (%s) may", bazelWorkflowName, path, value, preflight)
		}
	})
}

// TestNightlyWorkerEnvSyncOpensIssue: nightly's check, out of step, opens a
// ci/infra issue with the diff (or, with one open, updates its body and
// comments with the run), with GITHUB_TOKEN's issues: write scoped to that
// job and the token in that one step; the rest of nightly.yml reads only.
// gascity unreachable fails the job without an issue.
func TestNightlyWorkerEnvSyncOpensIssue(t *testing.T) {
	nightly := readCIWorkflow(t, "nightly.yml")
	var top struct {
		Permissions map[string]string `yaml:"permissions"`
	}
	if err := yaml.Unmarshal([]byte(readPolicyFile(t, sourceRepoRoot(t), ".github/workflows/nightly.yml")), &top); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(top.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("nightly.yml permissions = %v, want contents: read", top.Permissions)
	}
	for name, job := range nightly.Jobs {
		want := any(nil)
		switch name {
		case nightlyWorkerEnvJob:
			want = map[string]any{"contents": "read", "issues": "write"}
		case "bazel": // the bazel.yml call: what its rrc jobs ask for
			want = bazelCallPermissions
		}
		if !reflect.DeepEqual(job.Permissions, want) {
			t.Errorf("nightly.yml job %s permissions = %v, want %v", name, job.Permissions, want)
		}
	}
	job := nightly.job(t, nightlyWorkerEnvJob)
	if len(job.Steps) != 3 {
		t.Fatalf("nightly.yml %s has %d steps, want checkout, check, issue", nightlyWorkerEnvJob, len(job.Steps))
	}
	check, issue := job.Steps[1], job.Steps[2]
	if check.ID != "sync" || check.If != "" || !reflect.DeepEqual(check.Env, map[string]string{"WORKER_ENV_SYNC_REPORT": nightlyWorkerEnvReport}) {
		t.Errorf("nightly check step: id %q, if %q, env %v; want id sync, no if, WORKER_ENV_SYNC_REPORT=%s", check.ID, check.If, check.Env, nightlyWorkerEnvReport)
	}
	wantIssueEnv := map[string]string{
		"GH_TOKEN": "${{ github.token }}",
		"REPORT":   nightlyWorkerEnvReport,
		"RUN_URL":  "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}",
	}
	if issue.If != "${{ failure() && steps.sync.outputs.rc == '1' }}" || !reflect.DeepEqual(issue.Env, wantIssueEnv) || !strings.Contains(issue.Run, rbeWorkerEnvSyncIssue) {
		t.Errorf("nightly issue step: if %q, env %v, run %q; want only on rc 1, env %v, running %s", issue.If, issue.Env, issue.Run, wantIssueEnv, rbeWorkerEnvSyncIssue)
	}
	for name, j := range nightly.Jobs {
		for i, step := range j.Steps {
			if (name != nightlyWorkerEnvJob || i != 2) && strings.Contains(fmt.Sprint(step.Env, step.With, step.Run), "github.token") {
				t.Errorf("nightly.yml job %s step %d uses github.token; only the issue step may", name, i)
			}
		}
	}

	// The check step's rc output: 1 out of step (the issue step runs), 2
	// unknown (it does not), 0 in step.
	f := newRBEWorkerEnvFixture(t)
	stalePin := rbeTestPin(rbeTestStaleManifest)
	for _, c := range []struct {
		name, manifest, pin, url, rc string
	}{
		{"in step", f.manifest, f.pin, "", "0"},
		{"out of step", rbeTestStaleManifest, stalePin, "", "1"},
		{"gascity unreachable", f.manifest, f.pin, "file:///nonexistent-gascity", "2"},
	} {
		dir := t.TempDir()
		output := filepath.Join(dir, "output")
		report := filepath.Join(dir, "report.md")
		env := []string{"GITHUB_OUTPUT=" + output, "WORKER_ENV_SYNC_REPORT=" + report}
		if c.url != "" {
			env = append(env, "WORKER_ENV_SYNC_URL="+c.url)
		}
		code, out := f.run(t, f.beads(t, c.manifest, c.pin), check.Run, env...)
		got, _ := os.ReadFile(output)
		if fmt.Sprint(code) != c.rc || string(got) != "rc="+c.rc+"\n" {
			t.Errorf("nightly check, %s: exit %d, output %q; want exit and rc %s\n%s", c.name, code, got, c.rc, out)
		}
		if r, _ := os.ReadFile(report); (c.rc == "1") != strings.Contains(string(r), "### rbe worker-env out of step with gascity") {
			t.Errorf("nightly check, %s: report %q", c.name, r)
		}
	}

	// The issue step, against a stand-in gh that logs its calls and lists
	// the open ci/infra issues it is given.
	const ghStub = `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$GH_LOG"
for a in "$@"; do
	case "$prev" in --body-file) cp "$a" "$GH_LOG.body" ;; esac
	prev=$a
done
case "$1 $2" in
	"issue list") jq -r "$(for a in "$@"; do [ "$p" = --jq ] && echo "$a"; p=$a; done)" <<<"$GH_ISSUES" ;;
esac
`
	requireHostTool(t, "jq")
	title := "rbe worker-env out of step with gascity main"
	for _, c := range []struct {
		name, issues string
		want         []string
	}{
		{"no open issue", `[{"number":7,"title":"something else"}]`, []string{
			"issue create --repo gastownhall/beads --title " + title + " --label ci/infra --body-file ",
		}},
		{"open issue", `[{"number":7,"title":"something else"},{"number":42,"title":"` + title + `"}]`, []string{
			"issue edit 42 --repo gastownhall/beads --body-file ",
			"issue comment 42 --repo gastownhall/beads --body Still out of step with gascity main: https://github.com/gastownhall/beads/actions/runs/9",
		}},
	} {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		rbeTestWrite(t, filepath.Join(bin, "gh"), ghStub, 0o755)
		report := filepath.Join(dir, "report.md")
		rbeTestWrite(t, report, "### rbe worker-env out of step with gascity\n\nthe diff\n", 0o644)
		log := filepath.Join(dir, "gh.log")
		code, out := f.run(t, f.beads(t, f.manifest, f.pin), issue.Run,
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GH_LOG="+log, "GH_ISSUES="+c.issues,
			"GITHUB_REPOSITORY=gastownhall/beads", "REPORT="+report, "RUN_URL=https://github.com/gastownhall/beads/actions/runs/9")
		calls, _ := os.ReadFile(log)
		body, _ := os.ReadFile(log + ".body")
		if code != 0 {
			t.Errorf("issue step, %s: exit %d\n%s", c.name, code, out)
		}
		for _, want := range c.want {
			if !strings.Contains(string(calls), want) {
				t.Errorf("issue step, %s: gh calls lack %q:\n%s", c.name, want, calls)
			}
		}
		if strings.Contains(string(calls), "issue create") == (c.name == "open issue") {
			t.Errorf("issue step, %s: gh calls:\n%s", c.name, calls)
		}
		if !strings.Contains(string(body), "the diff") || !strings.Contains(string(body), "https://github.com/gastownhall/beads/actions/runs/9") {
			t.Errorf("issue step, %s: body:\n%s", c.name, body)
		}
	}
}
