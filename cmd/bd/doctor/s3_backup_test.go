package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeStateFile writes a JSON state file at path with the given heads and
// resets the modification time to mtime.
func writeStateFile(t *testing.T, path string, heads map[string]s3SyncDBEntry, mtime time.Time) {
	t.Helper()
	state := s3SyncState{TS: mtime.Format(time.RFC3339), Heads: heads}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write state file: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// makeDBDir creates a fake local Dolt DB directory at root/db with the
// manifest file that listLocalDoltDBs looks for. Returns the manifest path.
func makeDBDir(t *testing.T, root, db string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, db, ".dolt", "noms")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "manifest")
	if err := os.WriteFile(path, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

// setupS3Env wires the env vars for a hermetic test run and returns the
// state file path plus the Dolt data root. Both live under t.TempDir().
func setupS3Env(t *testing.T) (statePath, dataRoot string) {
	t.Helper()
	tmp := t.TempDir()
	statePath = filepath.Join(tmp, "last-s3-sync.json")
	dataRoot = filepath.Join(tmp, "dolt")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatalf("mkdir dataRoot: %v", err)
	}
	t.Setenv(envS3BackupState, statePath)
	t.Setenv(envDoltDataRoot, dataRoot)
	t.Setenv(envS3BackupPrefix, "")
	t.Setenv(envAWSCLIBin, "")
	return statePath, dataRoot
}

func TestCheckS3BackupFreshness_NotConfigured(t *testing.T) {
	setupS3Env(t)
	c := CheckS3BackupFreshness("")
	if c.Status != StatusOK {
		t.Fatalf("expected ok when unconfigured, got %s", c.Status)
	}
	if !strings.Contains(c.Message, "N/A") {
		t.Fatalf("expected N/A message, got %q", c.Message)
	}
	if c.Category != CategoryData {
		t.Fatalf("expected CategoryData, got %q", c.Category)
	}
}

func TestCheckS3BackupFreshness_MissingWithPrefix(t *testing.T) {
	setupS3Env(t)
	t.Setenv(envS3BackupPrefix, "s3://test-bucket/dolt-snapshots/live")
	c := CheckS3BackupFreshness("")
	if c.Status != StatusWarning {
		t.Fatalf("expected warning when configured but state missing, got %s", c.Status)
	}
	if !strings.Contains(c.Message, "missing") {
		t.Fatalf("expected 'missing' in message, got %q", c.Message)
	}
}

func TestCheckS3BackupFreshness_Fresh(t *testing.T) {
	statePath, _ := setupS3Env(t)
	writeStateFile(t, statePath, map[string]s3SyncDBEntry{
		"hq": {Head: "1", Status: "ok"},
	}, time.Now().Add(-30*time.Minute))
	c := CheckS3BackupFreshness("")
	if c.Status != StatusOK {
		t.Fatalf("expected ok when fresh, got %s: %q", c.Status, c.Message)
	}
}

func TestCheckS3BackupFreshness_Stale(t *testing.T) {
	statePath, _ := setupS3Env(t)
	writeStateFile(t, statePath, map[string]s3SyncDBEntry{
		"hq": {Head: "1", Status: "ok"},
	}, time.Now().Add(-30*time.Hour))
	c := CheckS3BackupFreshness("")
	if c.Status != StatusWarning {
		t.Fatalf("expected warning when stale, got %s: %q", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "old") {
		t.Fatalf("expected 'old' in message, got %q", c.Message)
	}
}

func TestCheckS3BackupCoverage_AllCovered(t *testing.T) {
	statePath, dataRoot := setupS3Env(t)
	now := time.Now()
	makeDBDir(t, dataRoot, "hq", now)
	makeDBDir(t, dataRoot, "kp", now)
	writeStateFile(t, statePath, map[string]s3SyncDBEntry{
		"hq": {Head: "1", Status: "ok"},
		"kp": {Head: "2", Status: "ok"},
	}, now)
	c := CheckS3BackupCoverage("")
	if c.Status != StatusOK {
		t.Fatalf("expected ok when all covered, got %s: %q", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "2") {
		t.Fatalf("expected count in message, got %q", c.Message)
	}
}

func TestCheckS3BackupCoverage_MissingDB(t *testing.T) {
	statePath, dataRoot := setupS3Env(t)
	now := time.Now()
	makeDBDir(t, dataRoot, "hq", now)
	makeDBDir(t, dataRoot, "bm", now)
	writeStateFile(t, statePath, map[string]s3SyncDBEntry{
		"hq": {Head: "1", Status: "ok"},
	}, now)
	c := CheckS3BackupCoverage("")
	if c.Status != StatusWarning {
		t.Fatalf("expected warning when DB missing from state, got %s", c.Status)
	}
	if !strings.Contains(c.Message, "bm") {
		t.Fatalf("expected bm in message, got %q", c.Message)
	}
}

func TestCheckS3BackupCoverage_FailedStatus(t *testing.T) {
	statePath, dataRoot := setupS3Env(t)
	now := time.Now()
	makeDBDir(t, dataRoot, "hq", now)
	writeStateFile(t, statePath, map[string]s3SyncDBEntry{
		"hq": {Head: "1", Status: "fail"},
	}, now)
	c := CheckS3BackupCoverage("")
	if c.Status != StatusWarning {
		t.Fatalf("expected warning when DB status=fail, got %s", c.Status)
	}
	if !strings.Contains(c.Message, "fail") {
		t.Fatalf("expected 'fail' in message, got %q", c.Message)
	}
}

func TestCheckS3BackupCoverage_NoLocalDBs(t *testing.T) {
	setupS3Env(t)
	c := CheckS3BackupCoverage("")
	if c.Status != StatusOK {
		t.Fatalf("expected ok when no local DBs, got %s", c.Status)
	}
	if !strings.Contains(c.Message, "no local Dolt DBs") && !strings.Contains(c.Message, "N/A") {
		t.Fatalf("expected N/A message, got %q", c.Message)
	}
}

func TestCheckS3BackupHeadParity_NoPrefix(t *testing.T) {
	setupS3Env(t)
	c := CheckS3BackupHeadParity("")
	if c.Status != StatusOK {
		t.Fatalf("expected ok when prefix unset, got %s", c.Status)
	}
	if !strings.Contains(c.Message, "N/A") {
		t.Fatalf("expected N/A, got %q", c.Message)
	}
}

func TestCheckS3BackupHeadParity_NoAWSCLI(t *testing.T) {
	setupS3Env(t)
	t.Setenv(envS3BackupPrefix, "s3://test-bucket/dolt-snapshots/live")
	// Point BEADS_AWS_CLI at a nonexistent file and empty PATH so LookPath fails.
	t.Setenv(envAWSCLIBin, "")
	// Empty PATH so exec.LookPath("aws") fails deterministically.
	t.Setenv("PATH", "")
	c := CheckS3BackupHeadParity("")
	if c.Status != StatusOK {
		t.Fatalf("expected ok when aws CLI missing, got %s: %q", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "aws CLI") {
		t.Fatalf("expected 'aws CLI' in message, got %q", c.Message)
	}
}

func TestCheckS3BackupHeadParity_Stale(t *testing.T) {
	_, dataRoot := setupS3Env(t)
	t.Setenv(envS3BackupPrefix, "s3://test-bucket/dolt-snapshots/live")

	now := time.Now()
	// Local manifest is 5 hours newer than the fake S3 LastModified.
	makeDBDir(t, dataRoot, "hq", now)

	fakeAWS := writeFakeAWS(t, map[string]string{
		"dolt-snapshots/live/hq/.dolt/noms/manifest": now.Add(-5 * time.Hour).UTC().Format(time.RFC3339),
	})
	t.Setenv(envAWSCLIBin, fakeAWS)

	c := CheckS3BackupHeadParity("")
	if c.Status != StatusWarning {
		t.Fatalf("expected warning when S3 stale, got %s: %q", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "hq") {
		t.Fatalf("expected hq in message, got %q", c.Message)
	}
	if !strings.Contains(c.Message, "stale") {
		t.Fatalf("expected 'stale' in message, got %q", c.Message)
	}
}

func TestCheckS3BackupHeadParity_Fresh(t *testing.T) {
	_, dataRoot := setupS3Env(t)
	t.Setenv(envS3BackupPrefix, "s3://test-bucket/dolt-snapshots/live")

	now := time.Now()
	makeDBDir(t, dataRoot, "hq", now.Add(-30*time.Minute))
	fakeAWS := writeFakeAWS(t, map[string]string{
		"dolt-snapshots/live/hq/.dolt/noms/manifest": now.Add(-15 * time.Minute).UTC().Format(time.RFC3339),
	})
	t.Setenv(envAWSCLIBin, fakeAWS)

	c := CheckS3BackupHeadParity("")
	if c.Status != StatusOK {
		t.Fatalf("expected ok when parity fresh, got %s: %q\n%s", c.Status, c.Message, c.Detail)
	}
}

func TestCheckS3BackupHeadParity_MissingObject(t *testing.T) {
	_, dataRoot := setupS3Env(t)
	t.Setenv(envS3BackupPrefix, "s3://test-bucket/dolt-snapshots/live")

	now := time.Now()
	makeDBDir(t, dataRoot, "hq", now)
	fakeAWS := writeFakeAWS(t, map[string]string{
		// hq key intentionally absent so the fake returns 404
	})
	t.Setenv(envAWSCLIBin, fakeAWS)

	c := CheckS3BackupHeadParity("")
	if c.Status != StatusWarning {
		t.Fatalf("expected warning when object missing, got %s: %q", c.Status, c.Message)
	}
	if !strings.Contains(c.Message, "no S3 manifest") {
		t.Fatalf("expected 'no S3 manifest' in message, got %q", c.Message)
	}
}

func TestListLocalDoltDBs_SkipsProbeAndHidden(t *testing.T) {
	tmp := t.TempDir()
	// Real DBs
	makeDBDir(t, tmp, "hq", time.Now())
	makeDBDir(t, tmp, "kp", time.Now())
	// Probe / hidden DBs that should be filtered out
	makeDBDir(t, tmp, "__gc_probe", time.Now())
	makeDBDir(t, tmp, "_scratch", time.Now())
	makeDBDir(t, tmp, ".tmp", time.Now())

	got, err := listLocalDoltDBs(tmp)
	if err != nil {
		t.Fatalf("listLocalDoltDBs: %v", err)
	}
	want := []string{"hq", "kp"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{15 * time.Minute, "15m"},
		{2 * time.Hour, "2h"},
		{2*time.Hour + 30*time.Minute, "2h30m"},
		{25 * time.Hour, "1d1h"},
		{72 * time.Hour, "3d"},
	}
	for _, c := range cases {
		got := formatDuration(c.in)
		if got != c.want {
			t.Errorf("formatDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// writeFakeAWS drops a tiny shell script into t.TempDir() that emulates
// "aws s3api head-object" by looking up the --key argument in the provided
// keyToLastModified map. Missing keys produce a Not Found stderr and exit 1
// so the check treats them as errS3ObjectMissing.
func writeFakeAWS(t *testing.T, keyToLastModified map[string]string) string {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); errors.Is(err, os.ErrNotExist) {
		t.Skip("skipping: /bin/sh not available")
	}
	tmp := t.TempDir()
	script := filepath.Join(tmp, "aws")

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# fake aws for tests\n")
	// Handle sts get-caller-identity preflight: succeed with a stub JSON.
	b.WriteString("if [ \"$1\" = \"sts\" ] && [ \"$2\" = \"get-caller-identity\" ]; then\n")
	b.WriteString("  echo '{\"UserId\":\"test\",\"Account\":\"000000000000\",\"Arn\":\"arn:aws:iam::000000000000:user/test\"}'\n")
	b.WriteString("  exit 0\n")
	b.WriteString("fi\n")
	b.WriteString("key=\"\"\n")
	b.WriteString("while [ $# -gt 0 ]; do\n")
	b.WriteString("  case \"$1\" in\n")
	b.WriteString("    --key) key=\"$2\"; shift 2;;\n")
	b.WriteString("    *) shift;;\n")
	b.WriteString("  esac\n")
	b.WriteString("done\n")
	// Cases for every configured key.
	for k, ts := range keyToLastModified {
		fmt.Fprintf(&b, "if [ \"$key\" = %q ]; then\n", k)
		fmt.Fprintf(&b, "  echo '{\"LastModified\": \"%s\"}'\n", ts)
		b.WriteString("  exit 0\nfi\n")
	}
	// Default: 404.
	b.WriteString("echo 'An error occurred (404) when calling the HeadObject operation: Not Found' >&2\n")
	b.WriteString("exit 254\n")
	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write fake aws: %v", err)
	}
	return script
}
