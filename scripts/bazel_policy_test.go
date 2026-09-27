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
