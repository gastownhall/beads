package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Env vars that configure the S3 backup checks. When BEADS_S3_BACKUP_PREFIX
// is unset the S3-facing checks report N/A rather than warning. This keeps
// the checks opt-in for users that do not run an off-machine backup script.
//
// The state file is written by the operator's own backup script; the format
// is documented in the CheckS3BackupFreshness comment. When
// BEADS_S3_BACKUP_STATE is unset, the default is
// $HOME/gc/.beads/last-s3-sync.json to match the shipped
// beads-backup-to-s3 layout.
const (
	envS3BackupPrefix   = "BEADS_S3_BACKUP_PREFIX"
	envS3BackupState    = "BEADS_S3_BACKUP_STATE"
	envDoltDataRoot     = "BEADS_DOLT_DATA_ROOT"
	envAWSCLIBin        = "BEADS_AWS_CLI"
	stalenessBackupFile = 25 * time.Hour
	stalenessHeadParity = 2 * time.Hour
)

// s3SyncState mirrors the JSON written by the operator's backup script.
// Extra fields are ignored so future format additions do not break the
// checks.
type s3SyncState struct {
	TS    string                    `json:"ts"`
	Heads map[string]s3SyncDBEntry `json:"heads"`
}

type s3SyncDBEntry struct {
	Head   string `json:"head"`
	Status string `json:"status"`
}

// stateFilePath returns the path of the S3 sync state file, honoring
// BEADS_S3_BACKUP_STATE and falling back to $HOME/gc/.beads/last-s3-sync.json.
func stateFilePath() string {
	if v := strings.TrimSpace(os.Getenv(envS3BackupState)); v != "" {
		return expandHome(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "gc", ".beads", "last-s3-sync.json")
}

// doltDataRoot returns the local Dolt data directory root, honoring
// BEADS_DOLT_DATA_ROOT with a default of $HOME/gc/.beads/dolt.
func doltDataRoot() string {
	if v := strings.TrimSpace(os.Getenv(envDoltDataRoot)); v != "" {
		return expandHome(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "gc", ".beads", "dolt")
}

// s3BackupPrefix returns the configured S3 URI prefix, or "" when unset.
// The URI is expected to be of the form s3://bucket/key-prefix with no
// trailing slash.
func s3BackupPrefix() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(envS3BackupPrefix)), "/")
}

// expandHome expands a leading ~ or ~/ in a path using the current user's
// home directory. Non-tilde paths are returned unchanged.
func expandHome(path string) string {
	if path == "" || (path[0] != '~') {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// readS3SyncState parses the state file at path. Returns (nil, nil) when the
// file does not exist so callers can treat "no state" as an opt-out signal.
func readS3SyncState(path string) (*s3SyncState, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is derived from env or a fixed default
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state s3SyncState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	return &state, nil
}

// listLocalDoltDBs returns the names of Dolt database directories under
// dataRoot. A database is any directory under dataRoot that contains a
// .dolt/noms/manifest file. Missing dataRoot returns (nil, nil).
func listLocalDoltDBs(dataRoot string) ([]string, error) {
	if dataRoot == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dataRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var dbs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip probe / internal DBs. A leading underscore or dot marks a
		// directory that the storage layer created for its own use
		// (liveness probes, embedded fallback stores) rather than a
		// user-owned data DB that needs backup.
		if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			continue
		}
		manifest := filepath.Join(dataRoot, name, ".dolt", "noms", "manifest")
		if _, err := os.Stat(manifest); err == nil {
			dbs = append(dbs, name)
		}
	}
	sort.Strings(dbs)
	return dbs, nil
}

// awsCLI returns the path to the aws CLI binary, honoring BEADS_AWS_CLI when
// set. Returns "" when no aws binary is available on PATH.
func awsCLI() string {
	if v := strings.TrimSpace(os.Getenv(envAWSCLIBin)); v != "" {
		return v
	}
	p, err := exec.LookPath("aws")
	if err != nil {
		return ""
	}
	return p
}

// s3HeadObject calls aws s3api head-object for the given s3://... URI. Returns
// the object's LastModified time on success. An "object does not exist"
// response is signaled by returning (zero time, ErrS3ObjectMissing).
type s3HeadResult struct {
	LastModified time.Time
	Missing      bool
}

var errS3ObjectMissing = fmt.Errorf("s3 object missing")

// preflightAWSCreds invokes aws sts get-caller-identity once with a short
// timeout to detect expired or missing credentials before running per-DB
// checks. Returns nil when credentials look usable.
func preflightAWSCreds(awsBin string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, awsBin, "sts", "get-caller-identity", "--output", "json")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("aws sts get-caller-identity failed: %s", msg)
	}
	return nil
}

// s3HeadObject issues aws s3api head-object against the given URI. When the
// object is absent, s3HeadResult.Missing is set. All other errors bubble up.
func s3HeadObject(ctx context.Context, awsBin, uri string) (s3HeadResult, error) {
	if !strings.HasPrefix(uri, "s3://") {
		return s3HeadResult{}, fmt.Errorf("invalid s3 uri: %s", uri)
	}
	rest := strings.TrimPrefix(uri, "s3://")
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return s3HeadResult{}, fmt.Errorf("invalid s3 uri: %s", uri)
	}
	bucket := rest[:slash]
	key := rest[slash+1:]
	cmd := exec.CommandContext(ctx, awsBin, "s3api", "head-object",
		"--bucket", bucket, "--key", key, "--output", "json")
	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		out := stderr.String()
		if strings.Contains(out, "Not Found") || strings.Contains(out, "404") ||
			strings.Contains(out, "NoSuchKey") {
			return s3HeadResult{Missing: true}, errS3ObjectMissing
		}
		return s3HeadResult{}, fmt.Errorf("aws s3api head-object: %w: %s", err, strings.TrimSpace(out))
	}
	var payload struct {
		LastModified string `json:"LastModified"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &payload); err != nil {
		return s3HeadResult{}, fmt.Errorf("parse head-object output: %w", err)
	}
	t, err := time.Parse(time.RFC3339, payload.LastModified)
	if err != nil {
		// aws sometimes emits a friendlier layout; try a fallback.
		t2, err2 := time.Parse("2006-01-02T15:04:05.000Z", payload.LastModified)
		if err2 != nil {
			return s3HeadResult{}, fmt.Errorf("parse LastModified %q: %w", payload.LastModified, err)
		}
		t = t2
	}
	return s3HeadResult{LastModified: t}, nil
}

// s3BackupCheckName constants keep test assertions stable.
const (
	CheckNameS3BackupFreshness  = "S3 Backup Freshness"
	CheckNameS3BackupCoverage   = "S3 Backup Coverage"
	CheckNameS3BackupHeadParity = "S3 Backup Head Parity"
)

// CheckS3BackupFreshness verifies that the state file left by the operator's
// backup script (default: $HOME/gc/.beads/last-s3-sync.json) has been updated
// within the last 25 hours. That grace window covers a missed hourly run
// without alarming, and catches a script that has stopped writing state at
// all (broken cron, crashed launchd job, revoked AWS credentials).
//
// The check is opt-in: when neither the state file nor
// BEADS_S3_BACKUP_PREFIX is present, it reports N/A. The path can be
// overridden with BEADS_S3_BACKUP_STATE.
//
// State file format (written by ~/gc/bin/beads-backup-to-s3):
//
//	{
//	  "ts": "2026-09-11T10:08:00-07:00",
//	  "heads": {
//	    "hq": {"head": "1789146381", "status": "ok"},
//	    ...
//	  }
//	}
func CheckS3BackupFreshness(_ string) DoctorCheck {
	statePath := stateFilePath()
	prefix := s3BackupPrefix()

	// Opt-out: state file absent and no prefix configured.
	info, statErr := os.Stat(statePath)
	if os.IsNotExist(statErr) && prefix == "" {
		return DoctorCheck{
			Name:     CheckNameS3BackupFreshness,
			Status:   StatusOK,
			Message:  "N/A (S3 backup not configured)",
			Category: CategoryData,
		}
	}
	if os.IsNotExist(statErr) {
		return DoctorCheck{
			Name:     CheckNameS3BackupFreshness,
			Status:   StatusWarning,
			Message:  fmt.Sprintf("State file missing: %s", statePath),
			Detail:   "The backup script has never written a state file. This suggests it has not run yet or is failing before completion.",
			Fix:      "Run the backup script manually (e.g., ~/gc/bin/beads-backup-to-s3) and confirm it exits 0.",
			Category: CategoryData,
		}
	}
	if statErr != nil {
		return DoctorCheck{
			Name:     CheckNameS3BackupFreshness,
			Status:   StatusError,
			Message:  fmt.Sprintf("Cannot stat state file: %v", statErr),
			Category: CategoryData,
		}
	}

	age := time.Since(info.ModTime())
	if age <= stalenessBackupFile {
		return DoctorCheck{
			Name:     CheckNameS3BackupFreshness,
			Status:   StatusOK,
			Message:  fmt.Sprintf("Last sync %s ago", formatDuration(age)),
			Category: CategoryData,
		}
	}
	return DoctorCheck{
		Name:     CheckNameS3BackupFreshness,
		Status:   StatusWarning,
		Message:  fmt.Sprintf("S3 backup state file is %s old (threshold %s)", formatDuration(age), stalenessBackupFile),
		Detail:   fmt.Sprintf("State file: %s\nLast modified: %s", statePath, info.ModTime().Format(time.RFC3339)),
		Fix:      "Check that the hourly backup job is running: launchctl list | grep beads-backup (macOS) or systemctl status beads-backup.timer (Linux). Run the backup script manually to verify AWS credentials.",
		Category: CategoryData,
	}
}

// CheckS3BackupCoverage verifies that every local Dolt database has a
// corresponding entry in the S3 sync state file with status=ok. A database
// present under ~/gc/.beads/dolt/<db> but missing from the state file heads
// map indicates the backup script has not been taught about it. A DB with
// status != ok indicates the last run failed for that DB specifically.
//
// This check is state-file-only and does not call aws. It is safe on hosts
// without AWS credentials.
func CheckS3BackupCoverage(_ string) DoctorCheck {
	statePath := stateFilePath()
	prefix := s3BackupPrefix()

	state, err := readS3SyncState(statePath)
	if err != nil {
		return DoctorCheck{
			Name:     CheckNameS3BackupCoverage,
			Status:   StatusError,
			Message:  fmt.Sprintf("Cannot parse state file: %v", err),
			Category: CategoryData,
		}
	}
	if state == nil && prefix == "" {
		return DoctorCheck{
			Name:     CheckNameS3BackupCoverage,
			Status:   StatusOK,
			Message:  "N/A (S3 backup not configured)",
			Category: CategoryData,
		}
	}
	if state == nil {
		return DoctorCheck{
			Name:     CheckNameS3BackupCoverage,
			Status:   StatusWarning,
			Message:  fmt.Sprintf("No state file at %s", statePath),
			Fix:      "Run the backup script manually to establish state.",
			Category: CategoryData,
		}
	}

	localDBs, err := listLocalDoltDBs(doltDataRoot())
	if err != nil {
		return DoctorCheck{
			Name:     CheckNameS3BackupCoverage,
			Status:   StatusError,
			Message:  fmt.Sprintf("Cannot enumerate local DBs: %v", err),
			Category: CategoryData,
		}
	}
	if len(localDBs) == 0 {
		return DoctorCheck{
			Name:     CheckNameS3BackupCoverage,
			Status:   StatusOK,
			Message:  "N/A (no local Dolt DBs found)",
			Category: CategoryData,
		}
	}

	var missing []string
	var failed []string
	for _, db := range localDBs {
		entry, ok := state.Heads[db]
		if !ok {
			missing = append(missing, db)
			continue
		}
		if entry.Status != "ok" {
			failed = append(failed, fmt.Sprintf("%s (status=%s)", db, entry.Status))
		}
	}

	if len(missing) == 0 && len(failed) == 0 {
		return DoctorCheck{
			Name:     CheckNameS3BackupCoverage,
			Status:   StatusOK,
			Message:  fmt.Sprintf("All %d local DB(s) covered", len(localDBs)),
			Category: CategoryData,
		}
	}

	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("no S3 backup entry: %s", strings.Join(missing, ", ")))
	}
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("failed on last sync: %s", strings.Join(failed, ", ")))
	}
	return DoctorCheck{
		Name:     CheckNameS3BackupCoverage,
		Status:   StatusWarning,
		Message:  strings.Join(parts, "; "),
		Detail:   fmt.Sprintf("State file: %s\nLocal DBs: %s", statePath, strings.Join(localDBs, ", ")),
		Fix:      "Add missing DBs to the backup script's DBS list, or investigate the failed run in ~/logs/beads-backup.err.",
		Category: CategoryData,
	}
}

// CheckS3BackupHeadParity compares each local Dolt database's on-disk
// manifest mtime with the LastModified timestamp on the corresponding S3
// object. When the S3 copy is stale by more than stalenessHeadParity
// (2 hours), the check warns.
//
// This check requires aws CLI on PATH and a configured BEADS_S3_BACKUP_PREFIX.
// If either is missing, it reports N/A. Per-DB missing-object results are
// aggregated into the warning; a single missing object does not fail the
// check outright since the coverage check already handles that.
func CheckS3BackupHeadParity(_ string) DoctorCheck {
	prefix := s3BackupPrefix()
	if prefix == "" {
		return DoctorCheck{
			Name:     CheckNameS3BackupHeadParity,
			Status:   StatusOK,
			Message:  fmt.Sprintf("N/A (%s not set)", envS3BackupPrefix),
			Category: CategoryData,
		}
	}
	awsBin := awsCLI()
	if awsBin == "" {
		return DoctorCheck{
			Name:     CheckNameS3BackupHeadParity,
			Status:   StatusOK,
			Message:  "N/A (aws CLI not found on PATH)",
			Category: CategoryData,
		}
	}

	// Pre-flight: if AWS credentials are expired, aws head-object calls can
	// each take 10+ seconds to fail. A single sts get-caller-identity call
	// keeps a well-configured environment fast and a broken one honest.
	if err := preflightAWSCreds(awsBin); err != nil {
		return DoctorCheck{
			Name:     CheckNameS3BackupHeadParity,
			Status:   StatusWarning,
			Message:  "AWS credentials unavailable",
			Detail:   err.Error(),
			Fix:      "Refresh AWS credentials (e.g., ada credentials update --provider=isengard --role=Admin) and re-run bd doctor.",
			Category: CategoryData,
		}
	}

	localDBs, err := listLocalDoltDBs(doltDataRoot())
	if err != nil {
		return DoctorCheck{
			Name:     CheckNameS3BackupHeadParity,
			Status:   StatusError,
			Message:  fmt.Sprintf("Cannot enumerate local DBs: %v", err),
			Category: CategoryData,
		}
	}
	if len(localDBs) == 0 {
		return DoctorCheck{
			Name:     CheckNameS3BackupHeadParity,
			Status:   StatusOK,
			Message:  "N/A (no local Dolt DBs found)",
			Category: CategoryData,
		}
	}

	root := doltDataRoot()
	var stale []string
	var missing []string
	var opErrs []string
	checked := 0

	// Bound per-DB call latency. 6s is enough for head-object against a
	// warm S3 endpoint with fresh creds; expired-creds latency has already
	// been handled by preflight. Cap total wall time at 45s regardless of
	// DB count.
	perCallTimeout := 6 * time.Second
	total := time.Duration(len(localDBs))*perCallTimeout + 5*time.Second
	if total > 45*time.Second {
		total = 45 * time.Second
	}
	overallCtx, cancel := context.WithTimeout(context.Background(), total)
	defer cancel()

	for _, db := range localDBs {
		localManifest := filepath.Join(root, db, ".dolt", "noms", "manifest")
		info, err := os.Stat(localManifest)
		if err != nil {
			opErrs = append(opErrs, fmt.Sprintf("%s: local manifest: %v", db, err))
			continue
		}
		localMtime := info.ModTime()
		s3URI := prefix + "/" + db + "/.dolt/noms/manifest"

		ctx, ccancel := context.WithTimeout(overallCtx, perCallTimeout)
		res, herr := s3HeadObject(ctx, awsBin, s3URI)
		ccancel()
		if herr == errS3ObjectMissing {
			missing = append(missing, db)
			continue
		}
		if herr != nil {
			opErrs = append(opErrs, fmt.Sprintf("%s: %v", db, herr))
			continue
		}
		checked++
		// S3 is expected to be >= local (S3 lags the write). When local is
		// newer by more than the tolerance, S3 is stale.
		lag := localMtime.Sub(res.LastModified)
		if lag > stalenessHeadParity {
			stale = append(stale, fmt.Sprintf("%s (lag %s)", db, formatDuration(lag)))
		}
	}

	if len(stale) == 0 && len(missing) == 0 && len(opErrs) == 0 {
		return DoctorCheck{
			Name:     CheckNameS3BackupHeadParity,
			Status:   StatusOK,
			Message:  fmt.Sprintf("All %d DB(s) parity within %s", checked, stalenessHeadParity),
			Category: CategoryData,
		}
	}
	// Op errors that leave every DB unchecked usually mean expired AWS
	// credentials or an unreachable bucket. Report as a warning with a
	// specific fix so it does not fail the overall doctor run for a
	// transient auth issue.
	if checked == 0 && len(stale) == 0 && len(missing) == 0 && len(opErrs) > 0 {
		return DoctorCheck{
			Name:     CheckNameS3BackupHeadParity,
			Status:   StatusWarning,
			Message:  "Head parity check could not run against any DB",
			Detail:   strings.Join(opErrs, "\n"),
			Fix:      "Refresh AWS credentials (e.g., ada credentials update --provider=isengard --role=Admin) and confirm the S3 bucket in $BEADS_S3_BACKUP_PREFIX is reachable.",
			Category: CategoryData,
		}
	}

	var parts []string
	if len(stale) > 0 {
		parts = append(parts, fmt.Sprintf("S3 stale by >%s: %s", stalenessHeadParity, strings.Join(stale, ", ")))
	}
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("no S3 manifest: %s", strings.Join(missing, ", ")))
	}
	if len(opErrs) > 0 {
		parts = append(parts, fmt.Sprintf("errors: %d", len(opErrs)))
	}
	return DoctorCheck{
		Name:     CheckNameS3BackupHeadParity,
		Status:   StatusWarning,
		Message:  strings.Join(parts, "; "),
		Detail:   strings.Join(append([]string{fmt.Sprintf("S3 prefix: %s", prefix)}, opErrs...), "\n"),
		Fix:      "Run the backup script manually to sync the stale DBs; check ~/logs/beads-backup.err for the last error.",
		Category: CategoryData,
	}
}

// formatDuration returns a short human-readable representation of d.
// Examples: "45m", "1h23m", "2d3h".
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d / (24 * time.Hour))
	h := int((d - time.Duration(days)*24*time.Hour).Hours())
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
}
