package scripts_test

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Scans of every tracked file: //scripts:tracked_files_test, the one target
// here that declares //:repo_files (the whole checkout), so that every other
// target can declare only the partition it reads and stay cached when an
// unrelated file changes. Keep this file to tests that really read every
// tracked file, and keep them cheap: any edit anywhere re-runs them.

// trackedFilesMin is the fewest files a listing of //:repo_files may hold
// (the checkout has several thousand).
const trackedFilesMin = 1000

// --- no remote-execution endpoints in tracked files -------------------------

// Remote-execution endpoints, credentials and TLS material belong in a
// gitignored user.bazelrc/.bazelrc.local or a CI-generated rc outside the
// workspace, never in the repository. The scan covers committed bazelrc-like
// files, Markdown docs and .github/** (plan §3.8) and flags:
//   - any remote/BES/TLS flag below whose value is a literal (not a
//     <placeholder>, a $VARIABLE/${{ expression }}, empty, or loopback);
//   - in rc and .github files, any gRPC URL that is not loopback. Markdown is
//     exempt from this rule because beads documents OTel gRPC exporters.
//
// The one exception is publicForkCacheLines.

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

// publicForkCacheLines: the endpoint lines of .bazelrc's fork-cache config,
// public by design. rbe-west's rbe-cache endpoint is anonymous and read-only
// (action-cache and CAS reads; writes and Execute are refused by the farm),
// so there is no credential to leak. Exactly these lines, trimmed, and only
// in .bazelrc; the same line anywhere else, or any other endpoint, is still a
// hit. Assembled so this file never contains a literal endpoint.
var publicForkCacheLines = map[string]bool{
	"build:fork-cache --remote_cache=" + forkCacheEndpoint:         true,
	"build:fork-cache --remote_instance_name=" + forkCacheInstance: true,
}

// publicRBEForkPin: setup-bazel's fork-credential.sh pins the one endpoint
// rbe-fork-mint may hand a fork run (rbe-fork, :8444), so a compromised
// mint cannot point fork builds elsewhere. It is public and carries no
// credential: allowed as exactly this line, in exactly that file (a byte
// copy of gascity's tools/rbe/fork-credential.sh).
const (
	publicRBEForkPinFile = ".github/actions/setup-bazel/fork-credential.sh"
	publicRBEForkPinLine = `ENDPOINT_RE=${RBE_FORK_ENDPOINT_RE:-'^` + "grpc" + `s://rbe-fork\.ops\.gascity\.com:8444$'}`
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
		if path == ".bazelrc" && publicForkCacheLines[strings.TrimSpace(line)] {
			continue
		}
		if path == publicRBEForkPinFile && line == publicRBEForkPinLine {
			continue
		}
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
	// The fork cache's public endpoint: exactly its two .bazelrc lines.
	forkCache := "build:fork-cache " + flag("remote_cache") + "=" + forkCacheEndpoint + "\n" +
		"  build:fork-cache " + flag("remote_instance_name") + "=oss  \n"
	if hits := findRemoteEndpoints(".bazelrc", []byte(forkCache), true); len(hits) != 0 {
		t.Errorf("fork-cache endpoint lines flagged in .bazelrc: %v", hits)
	}
	for _, path := range []string{"tools/ci.bazelrc", ".bazelrc.local", "user.bazelrc", ".github/workflows/x.yml", ".github/actions/setup-bazel/x.sh"} {
		if hits := findRemoteEndpoints(path, []byte(forkCache), true); len(hits) < 2 {
			t.Errorf("fork-cache endpoint lines in %s: hits %v, want both lines flagged (allowlisted in .bazelrc only)", path, hits)
		}
	}
	for _, bad := range []string{
		"build:fork-cache " + flag("remote_cache") + "=" + scheme + "other.example:8443\n",
		"build:fork-cache " + flag("remote_cache") + "=" + forkCacheEndpoint + "/x\n",
		"build:fork-cache " + flag("remote_cache") + "=" + forkCacheEndpoint + " " + flag("remote_header") + "=x-api-key=abc\n",
		"build " + flag("remote_cache") + "=" + forkCacheEndpoint + "\n",
		"build:remote-exec " + flag("remote_executor") + "=" + forkCacheEndpoint + "\n",
		"build:fork-cache " + flag("remote_instance_name") + "=beads\n",
		"# see " + forkCacheEndpoint + "\n",
	} {
		if len(findRemoteEndpoints(".bazelrc", []byte(bad), true)) == 0 {
			t.Errorf("non-allowlisted fork-cache variant not detected in .bazelrc: %q", bad)
		}
	}
	// rbe-fork's endpoint pin: that line in that file only; anything else
	// naming the endpoint, there or elsewhere, is flagged.
	if hits := findRemoteEndpoints(publicRBEForkPinFile, []byte("set -eu\n"+publicRBEForkPinLine+"\n"), true); len(hits) != 0 {
		t.Errorf("rbe-fork endpoint pin flagged in %s: %v", publicRBEForkPinFile, hits)
	}
	for _, path := range []string{".github/actions/setup-bazel/write-bazelrc.sh", ".github/workflows/bazel.yml", ".bazelrc", "tools/rbe/fork-credential.sh"} {
		if len(findRemoteEndpoints(path, []byte(publicRBEForkPinLine+"\n"), true)) == 0 {
			t.Errorf("rbe-fork endpoint pin not flagged in %s (allowlisted in %s only)", path, publicRBEForkPinFile)
		}
	}
	for _, bad := range []string{
		"  " + publicRBEForkPinLine,
		strings.Replace(publicRBEForkPinLine, "8444", "443", 1),
		`ENDPOINT=` + "grpc" + `s://rbe-fork.ops.gascity.com:8444`,
		"build:remote-exec " + flag("remote_executor") + "=" + scheme + "rbe-fork.ops.gascity.com:8444",
	} {
		if len(findRemoteEndpoints(publicRBEForkPinFile, []byte(bad+"\n"), true)) == 0 {
			t.Errorf("non-allowlisted rbe-fork line not detected in %s: %q", publicRBEForkPinFile, bad)
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
	var hits []endpointHit
	for _, rel := range repoFiles(t, root, trackedFilesMin) {
		scan, strict := remoteScanKind(rel)
		if !scan {
			continue
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

// TestNoLocalPlanPathsInTrackedFiles: tracked files must not point readers
// at a maintainer's private, out-of-repo planning notes (a home-directory
// planning-notes tree), which no other contributor can open. Cite an
// in-repo doc, a bead, or a PR instead. The needle is assembled at runtime so
// this file does not match itself.
func TestNoLocalPlanPathsInTrackedFiles(t *testing.T) {
	root := bazelPolicyRoot(t)
	needle := []byte("beads-" + "bazel-plan")
	for _, rel := range repoFiles(t, root, trackedFilesMin) {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if bytes.IndexByte(content, 0) >= 0 {
			continue // binary, as git grep -I
		}
		for n, line := range bytes.Split(content, []byte("\n")) {
			if bytes.Contains(line, needle) {
				t.Errorf("%s:%d: tracked file references a local, non-repo %s path; point at an in-repo doc, bead or PR instead", rel, n+1, needle)
			}
		}
	}
}
