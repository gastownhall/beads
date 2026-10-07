package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// ciAnalyticsExtractScript and ciAnalyticsExtractUnitTest are a byte copy of
// gascity's tools/bazel/ci_analytics_extract.py and
// tools/bazel/ci_analytics_extract_test.py (ci/analytics-extractor,
// gascity commit 00161373bc7bbfcef8d6a05ac5ce70409fc7f2ac, the final
// squashed-to-one-commit history superseding 32d51ce26a and the original
// 9104311a8a after the S2 review found the first cut's real/*-bep.json
// fixtures leaked operator env: a token, SSH IPs, the OpenBao address, the
// hostname and home paths. The fixtures are generated under env -i and
// committed with the compact exec log decompressed (*.binpb), never a
// zstd frame, so nothing opaque reaches version control), the same way
// tools/rbe/cache-zstd-probe.sh is a byte copy. gascity is canonical for
// this pair; TestCIAnalyticsExtractorIsPinnedToGascity below pins both
// files' sha256 so a local edit here is caught instead of silently
// drifting out of lockstep. The vendored test file's own 62 cases (run in
// place by TestCIAnalyticsExtractUnitTests) now include the
// mismatched-invocation-id and truncated-BEP cases beads originally added
// (test_beads_mismatch_fixture_reports_mismatch,
// test_truncated_last_line_sets_bep_truncated), so no separate Go-side
// duplicate of either remains here.
func ciAnalyticsExtractScript(root string) string {
	return filepath.Join(root, "tools", "bazel", "ci_analytics_extract.py")
}

func ciAnalyticsExtractUnitTest(root string) string {
	return filepath.Join(root, "tools", "bazel", "ci_analytics_extract_test.py")
}

func ciAnalyticsTestdataDir(root string) string {
	return filepath.Join(root, "tools", "bazel", "testdata", "ci_analytics")
}

// ciAnalyticsExtractSHA256 and ciAnalyticsExtractUnitTestSHA256 pin the
// vendored files' content, computed from gascity's final sanitized,
// squashed-to-one-commit history at 00161373bc7bbfcef8d6a05ac5ce70409fc7f2ac.
const (
	ciAnalyticsExtractSHA256         = "6bb912b17faac59a12ec53f71fbfbedfecc503037b6f5954e878d108abaae44f"
	ciAnalyticsExtractUnitTestSHA256 = "e833e1c961010cd02189afaef273a261a54ccabe916b03bae3115177378a405b"
	// ciAnalyticsTestdataSHA256 pins testdata/ci_analytics/** as a whole
	// (regen.sh, mismatch.bep.jsonl, poisoned-bep.json and real/*): a sha256
	// over every file's slash-joined relative path and bytes, each
	// NUL-terminated, sorted by path (ciAnalyticsTreeSHA256 below) -
	// computed the same way over gascity's 00161373bc tree and confirmed
	// identical, so this one hash catches a dropped, renamed or
	// individually-edited fixture that a single-file sha256 (the two
	// above) can't.
	ciAnalyticsTestdataSHA256 = "dc9ac595df095ef8fe1b965d0e2a8d0b0e5694c3122208891d27655bcc2285d5"
)

// ciAnalyticsTreeSHA256 hashes every regular file under dir: each entry
// contributes its dir-relative, slash-joined path, a NUL, its bytes and a
// NUL, in path-sorted order, so the result depends on the exact set of
// files present (not just their union of bytes) and is independent of the
// OS's directory-walk order.
func ciAnalyticsTreeSHA256(t *testing.T, dir string) string {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, rel := range paths {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(raw)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestCIAnalyticsExtractorIsPinnedToGascity fails the moment either vendored
// file is edited locally without also updating the pin (and, ideally,
// upstreaming the change to gascity first): the design treats gascity as
// canonical for this pair, like cache-zstd-probe.sh.
func TestCIAnalyticsExtractorIsPinnedToGascity(t *testing.T) {
	root := sourceRepoRoot(t)
	for _, tc := range []struct {
		path string
		want string
	}{
		{ciAnalyticsExtractScript(root), ciAnalyticsExtractSHA256},
		{ciAnalyticsExtractUnitTest(root), ciAnalyticsExtractUnitTestSHA256},
	} {
		raw, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("reading %s: %v", tc.path, err)
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != tc.want {
			t.Errorf("%s sha256 = %s, want %s (gascity ci/analytics-extractor @ 00161373bc7bbfcef8d6a05ac5ce70409fc7f2ac): "+
				"either this file drifted from gascity's canonical copy, or gascity changed it "+
				"and this pin (and the vendored copy) needs updating to match", tc.path, got, tc.want)
		}
	}

	testdataDir := ciAnalyticsTestdataDir(root)
	if got := ciAnalyticsTreeSHA256(t, testdataDir); got != ciAnalyticsTestdataSHA256 {
		t.Errorf("%s tree sha256 = %s, want %s (gascity ci/analytics-extractor @ 00161373bc7bbfcef8d6a05ac5ce70409fc7f2ac): "+
			"a fixture under testdata/ci_analytics/ was added, removed, renamed or edited without updating this pin", testdataDir, got, ciAnalyticsTestdataSHA256)
	}
}

// TestCIAnalyticsExtractUnitTests runs gascity's own 62 unittest cases
// in-place against the vendored extractor: the real Bazel 9.2.0 fixtures
// (testdata/ci_analytics/real/*, regenerated under env -i after the S2
// leak), the synthetic remote/cache-hit exec log built by the test's own
// varint encoder, the poisoned/malformed BEP and exec-log robustness cases,
// and the ported mismatched-invocation-id and truncated-BEP cases. This is
// the "thin Go wrapper" around the vendored pair.
func TestCIAnalyticsExtractUnitTests(t *testing.T) {
	root := sourceRepoRoot(t)
	python := requireHostTool(t, "python3")
	cmd := exec.Command(python, ciAnalyticsExtractUnitTest(root), "-v")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ci_analytics_extract_test.py failed: %v\n%s", err, out)
	}
}
