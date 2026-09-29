package scripts_test

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Policy tests for the side-by-side Bazel configuration. They are plain Go
// tests so they run under `go test ./scripts` and `bazel test //scripts:...`
// alike. Each invariant is a pure check function exercised against the real
// repository files and against synthetic fixtures that break it.

// bazelPolicyRoot returns the repository root holding the Bazel policy files.
// Under `bazel test` those files are declared as data (//:bazel_policy_files)
// and resolved from the runfiles tree; under `go test` the source checkout is
// used directly.
func bazelPolicyRoot(t *testing.T) string {
	t.Helper()
	if srcdir := os.Getenv("TEST_SRCDIR"); srcdir != "" {
		workspace := os.Getenv("TEST_WORKSPACE")
		if workspace == "" {
			workspace = "_main"
		}
		return filepath.Join(srcdir, workspace)
	}
	return sourceRepoRoot(t)
}

func readPolicyFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// --- go_sdk version == go.mod toolchain -------------------------------------

var (
	goModToolchainRe = regexp.MustCompile(`(?m)^toolchain\s+go(\S+)\s*$`)
	goModGoRe        = regexp.MustCompile(`(?m)^go\s+(\S+)\s*$`)
	goSDKDownloadRe  = regexp.MustCompile(`(?s)go_sdk\.download\((.*?)\)`)
	starlarkVersion  = regexp.MustCompile(`\bversion\s*=\s*"([^"]+)"`)
)

// goModToolchainVersion returns the Go version go.mod selects: the toolchain
// directive when present, otherwise the go directive.
func goModToolchainVersion(goMod string) (string, error) {
	if m := goModToolchainRe.FindStringSubmatch(goMod); m != nil {
		return m[1], nil
	}
	if m := goModGoRe.FindStringSubmatch(goMod); m != nil {
		return m[1], nil
	}
	return "", errors.New("go.mod has neither a toolchain nor a go directive")
}

// moduleGoSDKVersions returns the version of every go_sdk.download(...) call.
func moduleGoSDKVersions(module string) []string {
	var versions []string
	for _, call := range goSDKDownloadRe.FindAllStringSubmatch(stripStarlarkComments(module), -1) {
		if m := starlarkVersion.FindStringSubmatch(call[1]); m != nil {
			versions = append(versions, m[1])
		} else {
			versions = append(versions, "")
		}
	}
	return versions
}

func stripStarlarkComments(src string) string {
	var out strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func checkGoSDKMatchesToolchain(module, goMod string) error {
	want, err := goModToolchainVersion(goMod)
	if err != nil {
		return err
	}
	versions := moduleGoSDKVersions(module)
	if len(versions) == 0 {
		return errors.New("MODULE.bazel has no go_sdk.download(...) call")
	}
	for _, got := range versions {
		if got != want {
			return errors.New("MODULE.bazel go_sdk.download version " + strconv.Quote(got) +
				" != go.mod toolchain " + strconv.Quote(want) + "; update them in lockstep")
		}
	}
	return nil
}

func TestBazelGoSDKMatchesGoModToolchain(t *testing.T) {
	root := bazelPolicyRoot(t)
	if err := checkGoSDKMatchesToolchain(readPolicyFile(t, root, "MODULE.bazel"), readPolicyFile(t, root, "go.mod")); err != nil {
		t.Fatal(err)
	}

	goMod := "module example.com/m\n\ngo 1.26.0\n\ntoolchain go1.26.7\n"
	for name, module := range map[string]string{
		"skewed":      "go_sdk.download(\n    name = \"go_sdk\",\n    version = \"1.26.6\",\n)\n",
		"missing":     "bazel_dep(name = \"rules_go\", version = \"0.63.0\")\n",
		"no version":  "go_sdk.download(name = \"go_sdk\")\n",
		"commented":   "# go_sdk.download(version = \"1.26.7\")\n",
		"second skew": "go_sdk.download(version = \"1.26.7\")\ngo_sdk.download(version = \"1.25.0\")\n",
	} {
		if err := checkGoSDKMatchesToolchain(module, goMod); err == nil {
			t.Errorf("%s: expected a mismatch error for MODULE.bazel fixture:\n%s", name, module)
		}
	}
	if err := checkGoSDKMatchesToolchain("go_sdk.download(version = \"1.26.7\")\n", goMod); err != nil {
		t.Errorf("matching fixture rejected: %v", err)
	}
	if err := checkGoSDKMatchesToolchain("go_sdk.download(version = \"1.26.0\")\n", "module m\n\ngo 1.26.0\n"); err != nil {
		t.Errorf("go directive fallback rejected: %v", err)
	}
}

// --- ICU policy: gms_pure_go under Bazel ------------------------------------

var bazelrcPureGoTagRe = regexp.MustCompile(
	`^(build|common)\s+(?:.*\s)?--@@?rules_go//go/config:tags=(?:[^\s,]+,)*gms_pure_go(?:,[^\s,]+)*(?:\s|$)`)

// checkBazelrcSetsPureGo requires a build (or common) line that sets the
// gms_pure_go tag, so every bazel build/test/run links the pure-Go regex
// backend (engdocs/ICU-POLICY.md).
func checkBazelrcSetsPureGo(bazelrc string) error {
	for _, line := range strings.Split(bazelrc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if bazelrcPureGoTagRe.MatchString(line) {
			return nil
		}
	}
	return errors.New(".bazelrc must contain `build --@rules_go//go/config:tags=gms_pure_go` (engdocs/ICU-POLICY.md)")
}

// checkModulePrunesICU requires the go-mysql-server gazelle_override that
// selects the pure-Go regex file and excludes the cgo/ICU one; without it
// gazelle keeps the go-icu-regex (libicu) dependency edge.
func checkModulePrunesICU(module string) error {
	module = stripStarlarkComments(module)
	for _, call := range regexp.MustCompile(`(?s)go_deps\.gazelle_override\((.*?)\n\)`).FindAllStringSubmatch(module, -1) {
		body := call[1]
		if !strings.Contains(body, `"github.com/dolthub/go-mysql-server"`) {
			continue
		}
		if !strings.Contains(body, `"gazelle:build_tags gms_pure_go"`) {
			return errors.New("go-mysql-server gazelle_override lacks \"gazelle:build_tags gms_pure_go\"")
		}
		if !strings.Contains(body, `"gazelle:exclude internal/regex/regex_cgo.go"`) {
			return errors.New("go-mysql-server gazelle_override lacks \"gazelle:exclude internal/regex/regex_cgo.go\"")
		}
		return nil
	}
	return errors.New("MODULE.bazel has no go_deps.gazelle_override for github.com/dolthub/go-mysql-server")
}

func TestBazelICUPolicy(t *testing.T) {
	root := bazelPolicyRoot(t)
	if err := checkBazelrcSetsPureGo(readPolicyFile(t, root, ".bazelrc")); err != nil {
		t.Error(err)
	}
	if err := checkModulePrunesICU(readPolicyFile(t, root, "MODULE.bazel")); err != nil {
		t.Error(err)
	}
	if !regexp.MustCompile(`(?m)^# gazelle:build_tags (?:\S+,)*gms_pure_go(?:,\S+)*\s*$`).MatchString(readPolicyFile(t, root, "BUILD.bazel")) {
		t.Error("root BUILD.bazel must carry `# gazelle:build_tags gms_pure_go`")
	}

	for name, rc := range map[string]string{
		"absent":      "build --incompatible_strict_action_env\n",
		"commented":   "# build --@rules_go//go/config:tags=gms_pure_go\n",
		"test only":   "test --@rules_go//go/config:tags=gms_pure_go\n",
		"config only": "build:remote-exec --@rules_go//go/config:tags=gms_pure_go\n",
		"lookalike":   "build --@rules_go//go/config:tags=gms_pure_go_x\n",
	} {
		if err := checkBazelrcSetsPureGo(rc); err == nil {
			t.Errorf("%s: expected .bazelrc fixture to be rejected:\n%s", name, rc)
		}
	}
	for _, rc := range []string{
		"build --@rules_go//go/config:tags=gms_pure_go\n",
		"common --@rules_go//go/config:tags=foo,gms_pure_go\n",
		"build --@@rules_go//go/config:tags=gms_pure_go,bar  # trailing\n",
	} {
		if err := checkBazelrcSetsPureGo(rc); err != nil {
			t.Errorf("valid .bazelrc fixture rejected: %q: %v", rc, err)
		}
	}

	override := "go_deps.gazelle_override(\n    directives = [\n        \"gazelle:build_tags gms_pure_go\",\n%s    ],\n    path = \"github.com/dolthub/go-mysql-server\",\n)\n"
	if err := checkModulePrunesICU(strings.Replace(override, "%s", "", 1)); err == nil {
		t.Error("gazelle_override without the regex_cgo.go exclude was accepted")
	}
	if err := checkModulePrunesICU(strings.Replace(override, "%s", "        \"gazelle:exclude internal/regex/regex_cgo.go\",\n", 1)); err != nil {
		t.Errorf("complete gazelle_override rejected: %v", err)
	}
}

// --- .bazelversion pinned ---------------------------------------------------

func checkBazelVersionPin(content string) error {
	v := strings.TrimSpace(content)
	if !regexp.MustCompile(`^\d+\.\d+\.\d+(?:rc\d+)?$`).MatchString(v) {
		return errors.New(".bazelversion must pin an exact Bazel release (e.g. 9.2.0), got " + strconv.Quote(v))
	}
	return nil
}

func TestBazelVersionPinned(t *testing.T) {
	root := bazelPolicyRoot(t)
	content, err := os.ReadFile(filepath.Join(root, ".bazelversion"))
	if err != nil {
		t.Fatalf(".bazelversion must exist so bazelisk pins one Bazel release: %v", err)
	}
	if err := checkBazelVersionPin(string(content)); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"", "latest", "9.x", "9", "last_green"} {
		if checkBazelVersionPin(bad) == nil {
			t.Errorf(".bazelversion fixture %q was accepted", bad)
		}
	}
}

// --- machine-local rc files are gitignored ----------------------------------

// bazelLocalRCFiles hold a developer's remote-executor endpoint and TLS
// credential paths; they must never be committed.
var bazelLocalRCFiles = []string{".bazelrc.local", "user.bazelrc"}

// checkGitignoreCoversLocalRCs reports local rc files that no top-level
// .gitignore pattern ignores (or that a later negation re-includes). It
// understands the literal and root-anchored forms used for these files.
func checkGitignoreCoversLocalRCs(gitignore string) []string {
	ignored := map[string]bool{}
	for _, line := range strings.Split(gitignore, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negate := strings.HasPrefix(line, "!")
		pattern := strings.TrimPrefix(strings.TrimPrefix(line, "!"), "/")
		for _, name := range bazelLocalRCFiles {
			if ok, _ := filepath.Match(pattern, name); ok {
				ignored[name] = !negate
			}
		}
	}
	var missing []string
	for _, name := range bazelLocalRCFiles {
		if !ignored[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

func TestBazelLocalRCFilesGitignored(t *testing.T) {
	root := bazelPolicyRoot(t)
	if missing := checkGitignoreCoversLocalRCs(readPolicyFile(t, root, ".gitignore")); len(missing) > 0 {
		t.Errorf(".gitignore must ignore %v (they carry remote endpoints and credential paths)", missing)
	}
	// Cross-check with git itself when running from a checkout.
	if gitRepoAvailable(root) {
		for _, name := range bazelLocalRCFiles {
			cmd := exec.Command("git", "-C", root, "check-ignore", "-q", "--no-index", name)
			if err := cmd.Run(); err != nil {
				t.Errorf("git check-ignore %s: not ignored (%v)", name, err)
			}
		}
	}

	if missing := checkGitignoreCoversLocalRCs("/bazel-*\n"); len(missing) != 2 {
		t.Errorf("fixture without rc patterns: missing = %v, want both", missing)
	}
	if missing := checkGitignoreCoversLocalRCs("/.bazelrc.local\n/user.bazelrc\n!user.bazelrc\n"); len(missing) != 1 || missing[0] != "user.bazelrc" {
		t.Errorf("fixture with negation: missing = %v, want [user.bazelrc]", missing)
	}
	if missing := checkGitignoreCoversLocalRCs("*.bazelrc.local\nuser.bazelrc\n"); len(missing) != 0 {
		t.Errorf("fixture with glob patterns: missing = %v, want none", missing)
	}
}

// --- no remote-execution endpoints in tracked files -------------------------

// Remote-execution endpoints, credentials and TLS material belong in a
// gitignored user.bazelrc/.bazelrc.local or a CI-generated rc outside the
// workspace, never in the repository. The scan covers committed bazelrc-like
// files, Markdown docs and .github/** (plan §3.8) and flags:
//   - any remote/BES/TLS flag below whose value is a literal (not a
//     <placeholder>, a $VARIABLE/${{ expression }}, empty, or loopback);
//   - in rc and .github files, any gRPC URL that is not loopback. Markdown is
//     exempt from this rule because beads documents OTel gRPC exporters.

var (
	remoteFlagNames = `remote_executor|remote_cache|remote_downloader|remote_header|remote_instance_name|` +
		`bes_backend|bes_results_url|bes_header|tls_client_certificate|tls_client_key|tls_certificate`
	// `--flag=value` is recognized everywhere; `--flag value` only in rc and
	// workflow files, where prose ("--remote_executor and ...") does not occur.
	remoteFlagEqRe    = regexp.MustCompile(`--(` + remoteFlagNames + `)=("[^"]*"|'[^']*'|[^\s"'` + "`" + `]*)`)
	remoteFlagSpaceRe = regexp.MustCompile(`--(` + remoteFlagNames + `)\s+("[^"]*"|'[^']*'|[^\s"'` + "`" + `]+)`)
	remoteEndpointRe  = regexp.MustCompile(`\bgrpcs?://[^\s"'<>)\]]+`)
	loopbackValueRe   = regexp.MustCompile(`^(?:[a-z][a-z0-9+.-]*://)?(?:127\.0\.0\.1|localhost|\[::1\])(?:[:/]|$)`)
	// remoteScanPrefilter: a file lacking all of these cannot produce a hit.
	remoteScanPrefilter = [][]byte{[]byte("--remote_"), []byte("--bes_"), []byte("--tls_"), []byte("grpc")}
)

type endpointHit struct {
	path string
	line int
	what string
}

// remoteScanKind classifies a tracked path for the endpoint scan.
func remoteScanKind(rel string) (scan, strict bool) {
	base := filepath.Base(rel)
	switch {
	case strings.Contains(base, "bazelrc"), strings.HasPrefix(rel, ".github/"):
		return true, true
	case strings.HasSuffix(base, ".md"):
		return true, false
	}
	return false, false
}

func allowedRemoteValue(v string) bool {
	v = strings.Trim(v, `"'`)
	if i := strings.Index(v, "://"); i >= 0 {
		v = v[i+len("://"):]
	}
	return v == "" || strings.HasPrefix(v, "<") || strings.HasPrefix(v, "$") || loopbackValueRe.MatchString(v)
}

// findRemoteEndpoints reports literal remote endpoints/credentials in content.
// strict enables the whitespace-separated flag form and the gRPC URL rule.
func findRemoteEndpoints(path string, content []byte, strict bool) []endpointHit {
	if bytes.IndexByte(content, 0) >= 0 { // binary
		return nil
	}
	relevant := false
	for _, needle := range remoteScanPrefilter {
		if bytes.Contains(content, needle) {
			relevant = true
			break
		}
	}
	if !relevant {
		return nil
	}
	flagRes := []*regexp.Regexp{remoteFlagEqRe}
	if strict {
		flagRes = append(flagRes, remoteFlagSpaceRe)
	}
	var hits []endpointHit
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		for _, re := range flagRes {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				if !allowedRemoteValue(m[2]) {
					hits = append(hits, endpointHit{path: path, line: n, what: m[0]})
				}
			}
		}
		if strict {
			for _, url := range remoteEndpointRe.FindAllString(line, -1) {
				if !allowedRemoteValue(url) {
					hits = append(hits, endpointHit{path: path, line: n, what: url})
				}
			}
		}
	}
	return hits
}

func gitRepoAvailable(root string) bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	return exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree").Run() == nil
}

func TestBazelNoRemoteEndpointsInTrackedFiles(t *testing.T) {
	// Fixtures are assembled at runtime so this file never contains a literal
	// endpoint itself.
	scheme := "grpc" + "s://"
	flag := func(name string) string { return "--" + name }
	for _, bad := range []string{
		"build:remote-exec " + flag("remote_executor") + "=" + scheme + "rbe.example.com:443\n",
		"build:remote-exec " + flag("remote_executor") + "=rbe.example.com:443\n",
		"build:remote-exec " + flag("remote_executor") + " rbe.example.com:443\n",
		"build " + flag("remote_cache") + "=https://cache.example.com\n",
		"build " + flag("bes_backend") + "=bes.example.com\n",
		"build " + flag("bes_results_url") + "=https://results.example.com/inv/\n",
		"build " + flag("remote_header") + "=x-api-key=abc123\n",
		"build " + flag("tls_client_certificate") + "=/etc/rbe/client.crt\n",
		"build " + flag("tls_client_key") + "=\"/etc/rbe/client.key\"\n",
		"# farm: " + scheme + "10.0.0.5:8980\n",
		"REMOTE=" + "grpc" + "://cache.internal:9092\n",
	} {
		if len(findRemoteEndpoints(".bazelrc", []byte(bad), true)) == 0 {
			t.Errorf("endpoint fixture not detected: %q", bad)
		}
	}
	for _, ok := range []string{
		"build:remote-exec " + flag("remote_executor") + "=" + scheme + "<endpoint>\n",
		"build:remote-exec " + flag("remote_executor") + "=<endpoint>\n",
		"build:remote-exec " + flag("tls_client_certificate") + "=<path>\n",
		"  run: echo \"build " + flag("remote_executor") + "=${{ secrets.BAZEL_REMOTE_EXECUTOR }}\" >> \"$RUNNER_TEMP/ci.bazelrc\"\n",
		"  " + flag("tls_client_key") + " \"$RUNNER_TEMP/rbe.key\"\n",
		"  " + flag("remote_executor") + "=" + scheme + "${{ secrets.RBE_HOST }}\n",
		flag("remote_cache") + "=" + "grpc" + "://127.0.0.1:50052\n",
		flag("remote_cache") + "=localhost:9092\n",
		flag("remote_cache") + "=\n",
		"build " + flag("remote_timeout") + "=600\n",
		"use grpc for transport\n",
	} {
		if hits := findRemoteEndpoints(".bazelrc", []byte(ok), true); len(hits) != 0 {
			t.Errorf("allowed fixture flagged: %q -> %v", ok, hits)
		}
	}
	// Markdown: OTel gRPC exporter URLs and prose mentioning the flag are fine;
	// a literal flag value is not.
	for _, ok := range []string{
		"Set OTEL_EXPORTER_OTLP_ENDPOINT=" + "grpc" + "://collector:4317\n",
		"Pass " + flag("remote_executor") + " and TLS flags from user.bazelrc.\n",
	} {
		if hits := findRemoteEndpoints("docs/x.md", []byte(ok), false); len(hits) != 0 {
			t.Errorf("allowed Markdown fixture flagged: %q -> %v", ok, hits)
		}
	}
	if len(findRemoteEndpoints("docs/x.md", []byte("bazel build "+flag("remote_executor")+"=rbe.example.com:443 //...\n"), false)) == 0 {
		t.Error("literal remote_executor in Markdown not detected")
	}
	for rel, want := range map[string][2]bool{
		".bazelrc":                {true, true},
		"tools/ci.bazelrc":        {true, true},
		".bazelrc.local":          {true, true},
		".github/workflows/x.yml": {true, true},
		"docs/BAZEL.md":           {true, false},
		"issues.jsonl":            {false, false},
		"internal/telemetry/x.go": {false, false},
	} {
		if scan, strict := remoteScanKind(rel); scan != want[0] || strict != want[1] {
			t.Errorf("remoteScanKind(%q) = %v,%v, want %v,%v", rel, scan, strict, want[0], want[1])
		}
	}

	root := bazelPolicyRoot(t)
	if !gitRepoAvailable(root) {
		t.Skip("not a git checkout (e.g. Bazel sandbox); tracked-file scan runs under go test and CI")
	}
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--", "*bazelrc*", "*.md", ".github").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var hits []endpointHit
	for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		scan, strict := remoteScanKind(rel)
		if !scan {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, rel))
		if err != nil || !info.Mode().IsRegular() {
			continue // deleted in the worktree, submodule, or symlink
		}
		content, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		hits = append(hits, findRemoteEndpoints(rel, content, strict)...)
	}
	for _, h := range hits {
		t.Errorf("%s:%d: remote endpoint or credential %q in a tracked file; it belongs in a gitignored user.bazelrc/.bazelrc.local or a CI-generated rc outside the workspace", h.path, h.line, h.what)
	}
}

// --- generated go_srcs filegroups are current -------------------------------

// These checks walk the source checkout, which is not declared as Bazel data,
// so they run under plain `go test` (the gating lane) and skip under Bazel.

var (
	treeGoSrcsRe  = regexp.MustCompile(`(?s)filegroup\(\s*name\s*=\s*"tree_go_srcs",\s*srcs\s*=\s*\[(.*?)\]`)
	goSrcsLabelRe = regexp.MustCompile(`"//([^":]+):go_srcs"`)
)

// treeGoSrcsMembers returns the packages whose go_srcs a tree_go_srcs
// filegroup aggregates, other than the tree root's own ":go_srcs".
func treeGoSrcsMembers(build string) ([]string, bool) {
	m := treeGoSrcsRe.FindStringSubmatch(stripStarlarkComments(build))
	if m == nil {
		return nil, false
	}
	var members []string
	for _, l := range goSrcsLabelRe.FindAllStringSubmatch(m[1], -1) {
		members = append(members, l[1])
	}
	return members, true
}

// bazelPackagesUnder lists the repo-relative directories below treeRel (not
// treeRel itself) holding a BUILD.bazel, skipping the directories
// tools/bazel/go_srcs.py skips.
func bazelPackagesUnder(root, treeRel string) ([]string, error) {
	var pkgs []string
	err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(treeRel)), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "testdata" || name == "node_modules" || (strings.HasPrefix(name, ".") && name != ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "BUILD.bazel" {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		if rel = filepath.ToSlash(rel); rel != treeRel {
			pkgs = append(pkgs, rel)
		}
		return nil
	})
	return pkgs, err
}

func diffStringSets(want, got []string) (missing, extra []string) {
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}
	wantSet := map[string]bool{}
	for _, w := range want {
		wantSet[w] = true
		if !gotSet[w] {
			missing = append(missing, w)
		}
	}
	for _, g := range got {
		if !wantSet[g] {
			extra = append(extra, g)
		}
	}
	return missing, extra
}

// goSrcsTrees are the tools/bazel/go_srcs.py TREES roots. A test that walks
// one of these trees under Bazel sees only the packages its tree_go_srcs
// lists, so an unlisted package makes the walk pass vacuously.
var goSrcsTrees = []string{"internal/storage"}

func TestBazelTreeGoSrcsListsEveryPackage(t *testing.T) {
	build := "filegroup(\n    name = \"tree_go_srcs\",\n    srcs = [\n        \":go_srcs\",\n        \"//a/b:go_srcs\",\n        # \"//a/c:go_srcs\",\n    ],\n)\n"
	if got, ok := treeGoSrcsMembers(build); !ok || len(got) != 1 || got[0] != "a/b" {
		t.Errorf("treeGoSrcsMembers(fixture) = %v, %v; want [a/b], true", got, ok)
	}
	if missing, extra := diffStringSets([]string{"a/b", "a/c"}, []string{"a/b", "a/d"}); len(missing) != 1 || missing[0] != "a/c" || len(extra) != 1 || extra[0] != "a/d" {
		t.Errorf("diffStringSets fixture: missing=%v extra=%v", missing, extra)
	}

	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("walks the source checkout; runs under go test")
	}
	root := sourceRepoRoot(t)
	script := readPolicyFile(t, root, "tools/bazel/go_srcs.py")
	for _, tree := range goSrcsTrees {
		if !strings.Contains(script, strconv.Quote(tree)) {
			t.Errorf("tools/bazel/go_srcs.py no longer lists tree %q; update goSrcsTrees", tree)
		}
		members, ok := treeGoSrcsMembers(readPolicyFile(t, root, tree+"/BUILD.bazel"))
		if !ok {
			t.Errorf("%s/BUILD.bazel has no tree_go_srcs filegroup; run `make bazel-sync`", tree)
			continue
		}
		pkgs, err := bazelPackagesUnder(root, tree)
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
		if len(pkgs) == 0 {
			t.Fatalf("found no BUILD.bazel packages under %s; the walk is broken", tree)
		}
		missing, extra := diffStringSets(pkgs, members)
		for _, m := range missing {
			t.Errorf("//%s:tree_go_srcs does not list //%s:go_srcs; run `make bazel-sync` (tools/bazel/go_srcs.py)", tree, m)
		}
		for _, e := range extra {
			t.Errorf("//%s:tree_go_srcs lists //%s:go_srcs, which has no BUILD.bazel; run `make bazel-sync`", tree, e)
		}
	}
}

// TestBazelGoSrcsBlocksCurrent runs `tools/bazel/go_srcs.py --check`, which
// compares every managed block with what the script would generate.
func TestBazelGoSrcsBlocksCurrent(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("reads the source checkout; runs under go test")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; TestBazelTreeGoSrcsListsEveryPackage still guards tree membership")
	}
	cmd := exec.Command(python, filepath.Join("tools", "bazel", "go_srcs.py"), "--check")
	cmd.Dir = sourceRepoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go_srcs.py --check: %v; run `make bazel-sync`\n%s", err, out)
	}
}

// --- no Bazel packages under the docs trees ---------------------------------

// //:docsync_files globs docs/** and engdocs/**; a glob stops at a package
// boundary, so a BUILD file under either tree would silently drop that
// subtree from //test/docsync's orphan and link checks.
func TestBazelNoPackagesUnderDocsTrees(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("walks the source checkout; runs under go test")
	}
	root := sourceRepoRoot(t)
	for _, tree := range []string{"docs", "engdocs"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			if !d.IsDir() && (d.Name() == "BUILD.bazel" || d.Name() == "BUILD") {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s: no Bazel package may live under %s/ (it would cut that subtree out of //:docsync_files)", filepath.ToSlash(rel), tree)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", tree, err)
		}
	}
}

// --- test tag taxonomy ---------------------------------------------------------

// allowedBazelTestTags is the tag taxonomy. A target with no tags is hermetic
// and runs everywhere (local, remote, PR). Every other tag must be one of
// these and must be justified in a comment right above the rule that uses it.
var allowedBazelTestTags = map[string]string{
	"requires-dolt":   "uses the hermetic pinned dolt CLI (informational)",
	"host-tools":      "needs host tools (bash/git/make/jq/...) beyond the test wrapper's",
	"no-remote-exec":  "must run on the Bazel client's host, never on a remote worker",
	"no-remote-cache": "result depends on the host, so it is neither read from nor uploaded to the remote cache",
	"requires-docker": "needs a docker daemon; excluded from --config=prcore/ci, run by --config=docker",
	// No no-remote-exec: the target starts its own dolt sql-server from the
	// pinned dolt in its runfiles, so it runs on any worker, and a server that
	// cannot start fails it rather than skipping, so its cached result holds.
	"dolt-server": "starts hermetic dolt sql-servers (or completes the lane's job without -short); excluded from --config=prcore/ci, run by --config=doltserver",
	// The same rules as dolt-server (hermetic servers, remote, fail-closed:
	// checkDoltServerRules), for the tiers bazel.yml runs only with remote
	// execution and, for the server storage tier, under the integration
	// lane's build flags.
	"dolt-server-proxied":     "proxied-server cmd/bd tier: starts hermetic dolt sql-servers; excluded from --config=prcore/ci, run by --config=doltserver-proxied",
	"dolt-server-integration": "server-Dolt storage tier: starts hermetic dolt sql-servers and needs the integration build tag; excluded from --config=prcore/ci, run by --config=doltserver-integration",
	"embedded":                "embedded-Dolt tier variant; excluded from --config=prcore/ci, run by --config=embedded",
	"manual":                  "never part of //...: a repro/bench harness, or a build input only another target needs; excluded from --config=prcore/ci",
	// For a go_test whose every test file is `//go:build integration`: in any
	// other configuration rules_go drops those files and the target runs
	// zero tests, which check_testcases.py rejects and equivalence.py can
	// only note. gazelle keeps the hand-written tags attribute.
	"integration-only": "holds tests only under the integration build tag; excluded from --config=prcore/ci, run by --config=integration",
}

// bazelPRCoreExcludedTags are the tags whose targets never run in the PR-core
// lane (--config=prcore/ci): each belongs to another lane or to none. Every
// taxonomy entry that says "excluded from --config=prcore/ci" is listed here
// and vice versa (TestBazelPRCoreExcludedTagsMatchTaxonomy), so a new lane
// tag lands here, and through bazelIntegrationExcludedTags in the
// integration lane's filter too.
var bazelPRCoreExcludedTags = []string{"requires-docker", "dolt-server", "dolt-server-proxied", "dolt-server-integration", "embedded", "manual", "integration-only"}

// bazelIntegrationRunsTags are the PR-core-excluded tags --config=integration
// runs: the integration lane is main.yml's integration jobs, whose
// BEADS_TEST_SKIP=dolt skips every container- or server-backed test, so every
// other lane's variant stays out of it.
var bazelIntegrationRunsTags = map[string]bool{"integration-only": true}

// bazelIntegrationExcludedTags is bazelPRCoreExcludedTags less the tags the
// integration lane runs.
func bazelIntegrationExcludedTags() []string {
	var tags []string
	for _, tag := range bazelPRCoreExcludedTags {
		if !bazelIntegrationRunsTags[tag] {
			tags = append(tags, tag)
		}
	}
	return tags
}

func TestBazelPRCoreExcludedTagsMatchTaxonomy(t *testing.T) {
	listed := map[string]bool{}
	for _, tag := range bazelPRCoreExcludedTags {
		listed[tag] = true
		if _, ok := allowedBazelTestTags[tag]; !ok {
			t.Errorf("bazelPRCoreExcludedTags: %q is not in allowedBazelTestTags", tag)
		}
	}
	for tag, why := range allowedBazelTestTags {
		if says := strings.Contains(why, "excluded from --config=prcore/ci"); says != listed[tag] {
			t.Errorf("tag %q: taxonomy says excluded from prcore/ci = %v, bazelPRCoreExcludedTags = %v", tag, says, listed[tag])
		}
	}
	for tag := range bazelIntegrationRunsTags {
		if !listed[tag] {
			t.Errorf("bazelIntegrationRunsTags: %q is not in bazelPRCoreExcludedTags", tag)
		}
	}
}

// bazelTagsRequiring maps tags whose targets depend on the host to the tags
// they must also carry: no-remote-exec, or remote execution would run them on a
// worker that lacks the tool or daemon; and for host-tools, no-remote-cache,
// because the action key does not cover the host's tool inventory, so a result
// produced on one host must not be served to another. (requires-docker
// targets fail rather than skip without their daemon, so their passes are
// safe to share.)
var bazelTagsRequiring = map[string][]string{
	"host-tools":      {"no-remote-exec", "no-remote-cache"},
	"requires-docker": {"no-remote-exec"},
}

var (
	bazelTagsAttrRe  = regexp.MustCompile(`\btags\s*=\s*`)
	bazelTopRuleRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*\(`)
	bazelQuotedStrRe = regexp.MustCompile(`"([^"]*)"`)
)

// checkBazelBuildTags checks every `tags = [...]` in one BUILD file: the list
// is a literal, each tag is in the taxonomy and is named in the comment block
// directly above its rule, and host-bound tags come with no-remote-exec.
func checkBazelBuildTags(name, build string) []error {
	var errs []error
	lines := strings.Split(build, "\n")
	for _, loc := range bazelTagsAttrRe.FindAllStringIndex(build, -1) {
		line := strings.Count(build[:loc[0]], "\n")
		rest := build[loc[1]:]
		if !strings.HasPrefix(rest, "[") {
			errs = append(errs, errors.New(name+":"+strconv.Itoa(line+1)+": tags must be a literal list"))
			continue
		}
		end := strings.Index(rest, "]")
		if end < 0 {
			errs = append(errs, errors.New(name+":"+strconv.Itoa(line+1)+": unterminated tags list"))
			continue
		}
		// `tags = ["x"] + OTHER` is not a literal either: only a comma, the
		// rule's closing paren or the end of the line may follow the list.
		if after := strings.TrimLeft(rest[end+1:], " \t"); after != "" && !strings.ContainsAny(after[:1], ",)\n#") {
			errs = append(errs, errors.New(name+":"+strconv.Itoa(line+1)+": tags must be a literal list"))
			continue
		}
		var tags []string
		for _, m := range bazelQuotedStrRe.FindAllStringSubmatch(rest[:end], -1) {
			tags = append(tags, m[1])
		}

		// The rule is the nearest top-level call above; its justification is
		// the contiguous comment block right above that.
		start := line
		for start > 0 && !bazelTopRuleRe.MatchString(lines[start]) {
			start--
		}
		var comment strings.Builder
		for i := start - 1; i >= 0 && strings.HasPrefix(strings.TrimSpace(lines[i]), "#"); i-- {
			comment.WriteString(lines[i])
			comment.WriteByte('\n')
		}
		where := name + ":" + strconv.Itoa(start+1)

		have := map[string]bool{}
		for _, tag := range tags {
			have[tag] = true
			if _, ok := allowedBazelTestTags[tag]; !ok {
				errs = append(errs, errors.New(where+": tag "+strconv.Quote(tag)+" is not in the taxonomy (allowedBazelTestTags)"))
				continue
			}
			if !strings.Contains(comment.String(), tag) {
				errs = append(errs, errors.New(where+": tag "+strconv.Quote(tag)+" is not justified in a comment directly above the rule"))
			}
		}
		for _, tag := range tags {
			for _, need := range bazelTagsRequiring[tag] {
				if !have[need] {
					errs = append(errs, errors.New(where+": tag "+strconv.Quote(tag)+" also requires "+need))
				}
			}
		}
	}
	return errs
}

func TestBazelTestTagsFollowTaxonomy(t *testing.T) {
	for name, good := range map[string]string{
		"docker": "# Tags:\n#   requires-docker: needs a daemon.\n#   no-remote-exec: the daemon is local.\n" +
			"sh_test(\n    name = \"x\",\n    tags = [\n        \"no-remote-exec\",\n        \"requires-docker\",\n    ],\n)\n",
		"host-tools": "# host-tools, no-remote-exec, no-remote-cache: git.\n" +
			"go_test(\n    name = \"x\",\n    tags = [\"host-tools\", \"no-remote-exec\", \"no-remote-cache\"],  # why\n)\n",
	} {
		if errs := checkBazelBuildTags(name, good); len(errs) != 0 {
			t.Errorf("%s: justified fixture rejected: %v", name, errs)
		}
	}
	for name, build := range map[string]string{
		"unknown tag":     "# flaky: why\ngo_test(\n    name = \"x\",\n    tags = [\"flaky\"],\n)\n",
		"no comment":      "go_test(\n    name = \"x\",\n    tags = [\"manual\"],\n)\n",
		"comment too far": "# manual: harness\n\ngo_test(\n    name = \"x\",\n    tags = [\"manual\"],\n)\n",
		"other rule's":    "# manual: harness\ngo_test(name = \"a\", tags = [\"manual\"])\n\ngo_test(\n    name = \"b\",\n    tags = [\"manual\"],\n)\n",
		"docker unpinned": "# requires-docker: daemon\ngo_test(\n    name = \"x\",\n    tags = [\"requires-docker\"],\n)\n",
		"not a literal":   "# manual\ngo_test(\n    name = \"x\",\n    tags = MANUAL,\n)\n",
		"literal plus":    "# manual\ngo_test(\n    name = \"x\",\n    tags = [\"manual\"] + MORE,\n)\n",
		"host cacheable":  "# host-tools no-remote-exec\ngo_test(\n    name = \"x\",\n    tags = [\"host-tools\", \"no-remote-exec\"],\n)\n",
		"one-line rule":   "go_test(name = \"x\", tags = [\"manual\"])\n",
	} {
		if errs := checkBazelBuildTags(name, build); len(errs) == 0 {
			t.Errorf("%s: expected a tag policy error for fixture:\n%s", name, build)
		}
	}

	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("walks every BUILD file in the source checkout; runs under go test")
	}
	root := sourceRepoRoot(t)
	pkgs, err := bazelPackagesUnder(root, ".")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("found no BUILD.bazel packages; the walk is broken")
	}
	for _, pkg := range append([]string{"."}, pkgs...) {
		rel := filepath.ToSlash(filepath.Join(pkg, "BUILD.bazel"))
		for _, err := range checkBazelBuildTags(rel, readPolicyFile(t, root, rel)) {
			t.Error(err)
		}
	}
}

// bazelrcOption is one flag set by a `command:config` line of .bazelrc; a
// flag and its separate value are joined as flag=value.
type bazelrcOption struct{ config, flag string }

func parseBazelrcOptions(bazelrc string) []bazelrcOption {
	var opts []bazelrcOption
	for _, line := range strings.Split(bazelrc, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		_, config, ok := strings.Cut(fields[0], ":")
		if !ok {
			continue
		}
		for i := 1; i < len(fields); i++ {
			flag := fields[i]
			if (flag == "--config" || flag == "--test_tag_filters" || flag == "--test_arg") && i+1 < len(fields) {
				i++
				flag += "=" + fields[i]
			}
			opts = append(opts, bazelrcOption{config, flag})
		}
	}
	return opts
}

// bazelrcLaneOptions returns the options of config and of every config that
// expands it (test:ci --config=prcore), in file order: a later filter line in
// any of them overrides an earlier one.
func bazelrcLaneOptions(opts []bazelrcOption, config string) []bazelrcOption {
	lane := map[string]bool{config: true}
	for grew := true; grew; {
		grew = false
		for _, o := range opts {
			if !lane[o.config] && strings.HasPrefix(o.flag, "--config=") && lane[strings.TrimPrefix(o.flag, "--config=")] {
				lane[o.config] = true
				grew = true
			}
		}
	}
	var out []bazelrcOption
	for _, o := range opts {
		if lane[o.config] {
			out = append(out, o)
		}
	}
	return out
}

// checkBazelrcLaneTagFilter requires --config=<config> to set
// --test_tag_filters and every --test_tag_filters set by it or by a config
// that expands it to exclude each of the tags: since the last one wins, each
// must.
func checkBazelrcLaneTagFilter(bazelrc, config string, exclude []string) error {
	own := false
	for _, o := range bazelrcLaneOptions(parseBazelrcOptions(bazelrc), config) {
		if !strings.HasPrefix(o.flag, "--test_tag_filters=") {
			continue
		}
		own = own || o.config == config
		filters := map[string]bool{}
		for _, f := range strings.Split(strings.TrimPrefix(o.flag, "--test_tag_filters="), ",") {
			filters[f] = true
		}
		for _, tag := range exclude {
			if !filters["-"+tag] {
				return errors.New(o.config + " --test_tag_filters does not exclude " + tag)
			}
		}
	}
	if !own {
		return errors.New(".bazelrc has no test:" + config + " --test_tag_filters line")
	}
	return nil
}

// checkBazelrcPrcoreTagFilter requires --config=prcore (and test:ci, which
// expands it) to exclude the tags that never run in the PR-core lane.
func checkBazelrcPrcoreTagFilter(bazelrc string) error {
	return checkBazelrcLaneTagFilter(bazelrc, "prcore", bazelPRCoreExcludedTags)
}

// checkBazelrcIntegrationLane requires --config=integration to exclude every
// other lane's tags and to pass the tests no -test.short, -test.skip or
// -test.run (nor --test_filter): main.yml's integration jobs run every test.
func checkBazelrcIntegrationLane(bazelrc string) error {
	if err := checkBazelrcLaneTagFilter(bazelrc, "integration", bazelIntegrationExcludedTags()); err != nil {
		return err
	}
	for _, o := range bazelrcLaneOptions(parseBazelrcOptions(bazelrc), "integration") {
		arg, isArg := strings.CutPrefix(o.flag, "--test_arg=")
		if strings.HasPrefix(o.flag, "--test_filter") {
			return errors.New(o.config + " sets " + o.flag + "; the integration jobs select every test")
		}
		if !isArg {
			continue
		}
		arg = strings.TrimLeft(arg, "-")
		for _, bad := range []string{"test.short", "test.skip", "test.run"} {
			if arg == bad || strings.HasPrefix(arg, bad+"=") {
				return errors.New(o.config + " passes " + o.flag + "; the integration jobs run no -short/-skip/-run")
			}
		}
	}
	return nil
}

func TestBazelrcPrcoreExcludesNonPRTags(t *testing.T) {
	if err := checkBazelrcPrcoreTagFilter(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc")); err != nil {
		t.Fatal(err)
	}
	for name, rc := range map[string]string{
		"missing":        "test:ci --keep_going\n",
		"no docker":      "test:prcore --test_tag_filters=-dolt-server,-embedded,-manual\n",
		"no dolt-server": "test:prcore --test_tag_filters=-requires-docker,-embedded,-manual\n",
		"ci override": "test:prcore --test_tag_filters=-requires-docker,-embedded,-manual\n" +
			"test:ci --config=prcore\ntest:ci --test_tag_filters=requires-docker\n",
		"second prcore line": "test:prcore --test_tag_filters=-requires-docker,-embedded,-manual\n" +
			"test:prcore --test_tag_filters=-manual\n",
		"transitive": "test:prcore --test_tag_filters=-requires-docker,-embedded,-manual\n" +
			"test:ci --config=prcore\nbuild:nightly --config ci --test_tag_filters=\n",
	} {
		if err := checkBazelrcPrcoreTagFilter(rc); err == nil {
			t.Errorf("%s: expected an error for .bazelrc fixture:\n%s", name, rc)
		}
	}
}

// TestBazelrcPrcoreRequiresExcludePermission keeps test:prcore in step with
// pr.yml's PR Core step (TestPRCoreRequiresExcludeReadPermissionCoverage):
// without the variable, TestAddExcludePatternsRefusesReadErrors skips on a
// root executor instead of failing.
func TestBazelrcPrcoreRequiresExcludePermission(t *testing.T) {
	const want = "test:prcore --test_env=BEADS_TEST_REQUIRE_EXCLUDE_PERMISSION=1"
	for _, line := range strings.Split(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc"), "\n") {
		if strings.TrimSpace(line) == want {
			return
		}
	}
	t.Fatalf(".bazelrc lacks %q", want)
}

// TestBazelrcPrcoreMatchesPRCoreParallel keeps test:prcore's -test.parallel
// equal to pr-core.sh's -parallel default: without it Go uses GOMAXPROCS, the
// executor's core count, and runs far more tests at once than PR Core does.
func TestBazelrcPrcoreMatchesPRCoreParallel(t *testing.T) {
	root := bazelPolicyRoot(t)
	m := regexp.MustCompile(`(?m)^GO_TEST_PARALLEL="\$\{GO_TEST_PARALLEL:-(\d+)\}"$`).FindStringSubmatch(readPolicyFile(t, root, "scripts/ci/pr-core.sh"))
	if m == nil {
		t.Fatal("scripts/ci/pr-core.sh has no GO_TEST_PARALLEL default")
	}
	want := "test:prcore --test_arg=-test.parallel=" + m[1]
	for _, line := range strings.Split(readPolicyFile(t, root, ".bazelrc"), "\n") {
		if strings.TrimSpace(line) == want {
			return
		}
	}
	t.Fatalf(".bazelrc lacks %q (pr-core.sh runs go test -parallel %s)", want, m[1])
}

// Docker-lane results depend on host state no action key sees (daemon, dolt
// image, network), so they must always execute, like the container jobs'
// -count=1; and no remote-exec run may upload a locally executed result to
// the shared cache.
func TestBazelrcDockerLaneNeverCached(t *testing.T) {
	lines := map[string]bool{}
	for _, line := range strings.Split(readPolicyFile(t, bazelPolicyRoot(t), ".bazelrc"), "\n") {
		lines[strings.TrimSpace(line)] = true
	}
	for _, want := range []string{
		"test:docker --nocache_test_results",
		"build:remote-exec --noremote_upload_local_results",
	} {
		if !lines[want] {
			t.Errorf(".bazelrc lacks %q", want)
		}
	}
}

// --- integration lane ----------------------------------------------------------

// tagSet parses a comma-separated Go build tag list.
func tagSet(list string) map[string]bool {
	set := map[string]bool{}
	for _, tag := range strings.Split(list, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			set[tag] = true
		}
	}
	return set
}

func sameTagSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for tag := range a {
		if !b[tag] {
			return false
		}
	}
	return true
}

// TestBazelIntegrationLaneMatchesMainWorkflow keeps --config=integration in
// step with main.yml's "Main Linux integration" jobs: the same build tags,
// race, BEADS_TEST_SKIP=dolt, and none of the variants those jobs do not run.
// It also requires gazelle to see the same tags (root BUILD.bazel
// `gazelle:build_tags`): gazelle drops a file whose build constraint names a
// tag it does not know, so without it no BUILD file would list the integration
// test files and the lane would silently run the plain package tests instead.
// With it, a file gaining `//go:build integration` lands in its package's srcs
// through `make bazel-sync`, whose staleness bazel.yml already fails on.
func TestBazelIntegrationLaneMatchesMainWorkflow(t *testing.T) {
	root := bazelPolicyRoot(t)
	mainYML := readPolicyFile(t, root, ".github/workflows/main.yml")
	jobTags := regexp.MustCompile(`-race -tags=(\S+) -timeout=30m`).FindAllStringSubmatch(mainYML, -1)
	if len(jobTags) != 2 {
		t.Fatalf("main.yml: want the two integration jobs' `go test -race -tags=... -timeout=30m`, found %d", len(jobTags))
	}
	want := tagSet(jobTags[0][1])
	if !want["integration"] || !sameTagSet(want, tagSet(jobTags[1][1])) {
		t.Fatalf("main.yml integration jobs' tags differ or lack integration: %q, %q", jobTags[0][1], jobTags[1][1])
	}
	if strings.Count(mainYML, "env BEADS_TEST_SKIP=dolt gotestsum") < 2 {
		t.Fatal("main.yml integration jobs no longer run with BEADS_TEST_SKIP=dolt; update test:integration")
	}

	bazelrc := readPolicyFile(t, root, ".bazelrc")
	lines := map[string]bool{}
	var laneTags map[string]bool
	for _, line := range strings.Split(bazelrc, "\n") {
		line = strings.TrimSpace(line)
		lines[line] = true
		if v, ok := strings.CutPrefix(line, "build:integration --@rules_go//go/config:tags="); ok {
			laneTags = tagSet(v)
		}
	}
	if !sameTagSet(laneTags, want) {
		t.Errorf(".bazelrc build:integration tags = %v, want main.yml's %v", laneTags, want)
	}
	if err := checkBazelrcIntegrationLane(bazelrc); err != nil {
		t.Error(err)
	}
	for _, need := range []string{
		"test:integration --@rules_go//go/config:race",
		"test:integration --test_env=BEADS_TEST_SKIP=dolt",
	} {
		if !lines[need] {
			t.Errorf(".bazelrc lacks %q", need)
		}
	}

	m := regexp.MustCompile(`(?m)^# gazelle:build_tags (\S+)$`).FindStringSubmatch(readPolicyFile(t, root, "BUILD.bazel"))
	if m == nil {
		t.Fatal("BUILD.bazel has no `# gazelle:build_tags` directive")
	}
	gazelleTags := tagSet(m[1])
	for tag := range want {
		if !gazelleTags[tag] {
			t.Errorf("BUILD.bazel `gazelle:build_tags %s` lacks %q: gazelle would leave that lane's files out of every BUILD file", m[1], tag)
		}
	}
}

func TestBazelrcIntegrationLaneFixtures(t *testing.T) {
	exclude := bazelIntegrationExcludedTags()
	good := "test:integration --test_tag_filters=-" + strings.Join(exclude, ",-") + "\n" +
		"test:integration --test_arg=-test.parallel=4\ntest:integration --test_arg=-test.timeout=19m\n"
	if err := checkBazelrcIntegrationLane(good); err != nil {
		t.Errorf("good fixture rejected: %v", err)
	}
	for name, rc := range map[string]string{
		"missing":          "test:integration --keep_going\n",
		"one missing":      "test:integration --test_tag_filters=-" + strings.Join(exclude[1:], ",-") + "\n",
		"later override":   good + "test:integration --test_tag_filters=-manual\n",
		"expanding config": good + "test:nightly --config=integration\ntest:nightly --test_tag_filters=\n",
		"short":            good + "test:integration --test_arg=-test.short\n",
		"short spaced":     good + "test:integration --test_arg -test.short\n",
		"skip":             good + "test:integration --test_arg=-test.skip=TestX\n",
		"run":              good + "test:integration --test_arg=--test.run=TestX\n",
		"test_filter":      good + "test:integration --test_filter=TestX\n",
	} {
		if err := checkBazelrcIntegrationLane(rc); err == nil {
			t.Errorf("%s: expected an error for .bazelrc fixture:\n%s", name, rc)
		}
	}
}
