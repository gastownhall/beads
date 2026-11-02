package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeResumedLogJournal writes a forward journal past target configuration
// whose log_assets are the given entries, after the sidecar is gone: the
// resumed run retires those logs from the journal alone.
func writeResumedLogJournal(t *testing.T, beadsDir, root string, logAssets []string) {
	t.Helper()
	writeMetadataConfig(t, beadsDir, configfile.DoltModeServer, "myproj")
	j := migrateJournal{Version: 1, SourceMode: configfile.DoltModeProxiedServer, TargetMode: configfile.DoltModeServer, RootPath: root, LogAssets: logAssets, Ownership: "managed-local", Attempt: 1, Phase: migrateTargetConfigured, Sidecar: &configfile.ProxiedServerClientInfo{RootPath: root}}
	b, err := json.Marshal(j)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(migrateJournalPath(beadsDir), b, 0o600))
}

// TestMigrationLogAssetsStayConfined forges a journal whose log_assets name
// files outside the validated root's server.log and server.log.1: those are
// warned about and left alone, while the eligible logs are retired.
func TestMigrationLogAssetsStayConfined(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	for _, dryRun := range []bool{false, true} {
		name := "migrate"
		if dryRun {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
			root := filepath.Join(beadsDir, "dolt")
			outside := filepath.Join(filepath.Dir(beadsDir), "sentinel")
			linkTarget := filepath.Join(filepath.Dir(beadsDir), "link-target")
			sibling := filepath.Join(beadsDir, proxiedServerLogName)
			unrelated := filepath.Join(root, "unrelated")
			eligible := filepath.Join(root, proxiedServerLogName)
			rotated := filepath.Join(root, proxiedServerLogName+".1")
			for _, p := range []string{outside, linkTarget, sibling, unrelated} {
				require.NoError(t, os.WriteFile(p, []byte("unchanged"), 0o600))
			}
			require.NoError(t, os.WriteFile(eligible, []byte("log"), 0o600))
			require.NoError(t, os.Symlink(linkTarget, rotated))
			traversal := filepath.Join(root, "..", proxiedServerLogName)
			writeResumedLogJournal(t, beadsDir, root, []string{outside, rotated, eligible, unrelated, traversal})

			var err error
			stdout, stderr := captureStdio(t, func() { err = runMigrateFromProxiedServer(dryRun, false) })
			require.NoError(t, err)

			for _, p := range []string{outside, rotated, unrelated, traversal} {
				assert.Contains(t, stderr, p, "skipped entry not named in warnings")
				assert.NotContains(t, stdout, "Would remove "+p+"\n")
			}
			for _, p := range []string{outside, linkTarget, sibling, unrelated} {
				assert.Equal(t, "unchanged", string(mustReadFile(t, p)), p)
			}
			info, lerr := os.Lstat(rotated)
			require.NoError(t, lerr, "rotated-log symlink removed")
			assert.NotZero(t, info.Mode()&os.ModeSymlink)

			if dryRun {
				assert.Contains(t, stdout, "Would remove "+eligible+"\n")
				assert.FileExists(t, eligible)
				return
			}
			assert.NoFileExists(t, eligible, "eligible log not retired")
			cfg, cerr := configfile.Load(beadsDir)
			require.NoError(t, cerr)
			assert.True(t, cfg.IsDoltServerMode())
		})
	}
}

// TestMigrationLogAssetsToleratesAbsentLogs retires a regular rotated log and
// accepts that server.log is already gone.
func TestMigrationLogAssetsToleratesAbsentLogs(t *testing.T) {
	beadsDir := migrateModeWorkspace(t, configfile.DoltModeProxiedServer)
	root := filepath.Join(beadsDir, "dolt")
	rotated := filepath.Join(root, proxiedServerLogName+".1")
	require.NoError(t, os.WriteFile(rotated, []byte("rotation"), 0o600))
	writeResumedLogJournal(t, beadsDir, root, []string{filepath.Join(root, proxiedServerLogName), rotated})

	var err error
	stderr := captureStderr(t, func() { err = runMigrateFromProxiedServer(false, false) })
	require.NoError(t, err)
	assert.NotContains(t, stderr, "Warning")
	assert.NoFileExists(t, rotated)
}

// TestMigrationLogAssetsRejectSymlinkDirectory replaces the root with a
// symlink. The command refuses a symlinked workspace root before cleanup, so
// this drives the cleanup filter directly: the log it reaches stays.
func TestMigrationLogAssetsRejectSymlinkDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	root := filepath.Join(dir, "dolt")
	sentinel := filepath.Join(outside, proxiedServerLogName)
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, os.WriteFile(sentinel, []byte("unchanged"), 0o600))
	require.NoError(t, os.Symlink(outside, root))
	logPath := filepath.Join(root, proxiedServerLogName)

	var errs []error
	stderr := captureStderr(t, func() {
		errs = removeMigrateAssets(root, confinedMigrateLogAssets(root, []string{logPath}))
	})
	assert.Empty(t, errs)
	assert.True(t, strings.Contains(stderr, "Warning") && strings.Contains(stderr, logPath), "missing warning: %q", stderr)
	assert.Equal(t, "unchanged", string(mustReadFile(t, sentinel)))

	// The remover enforces the same boundary without the filter.
	stderr = captureStderr(t, func() { errs = removeMigrateAssets(root, []string{logPath}) })
	assert.Empty(t, errs)
	assert.True(t, strings.Contains(stderr, "Warning") && strings.Contains(stderr, logPath), "missing warning: %q", stderr)
	assert.Equal(t, "unchanged", string(mustReadFile(t, sentinel)))
}

// TestRemoveMigrateAssetsGuardsItsBoundary drives the remover directly: only
// logDir's regular server.log and server.log.1 are removed; an outside path,
// an unrelated sibling and a symlinked rotated log stay.
func TestRemoveMigrateAssetsGuardsItsBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	dir := t.TempDir()
	logDir := filepath.Join(dir, ".beads", "dolt")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	outside := filepath.Join(dir, "sentinel")
	legit := filepath.Join(logDir, proxiedServerLogName)
	link := filepath.Join(logDir, proxiedServerLogName+".1")
	other := filepath.Join(logDir, "unrelated")
	for _, p := range []string{outside, other} {
		require.NoError(t, os.WriteFile(p, []byte("unchanged"), 0o600))
	}
	require.NoError(t, os.WriteFile(legit, []byte("log"), 0o600))
	require.NoError(t, os.Symlink(outside, link))

	var errs []error
	stderr := captureStderr(t, func() { errs = removeMigrateAssets(logDir, []string{outside, link, legit, other}) })
	assert.Empty(t, errs)
	for _, p := range []string{outside, link, other} {
		assert.Contains(t, stderr, p, "skipped entry not named in warnings")
	}
	for _, p := range []string{outside, other} {
		assert.Equal(t, "unchanged", string(mustReadFile(t, p)), p)
	}
	_, lerr := os.Lstat(link)
	assert.NoError(t, lerr, "rotated-log symlink removed")
	assert.NoFileExists(t, legit, "eligible log not removed")

	assert.Empty(t, removeMigrateAssets(logDir, nil))
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.WriteFile(link, []byte("rotation"), 0o600))
	assert.Empty(t, removeMigrateAssets(logDir, []string{legit, link}), "absent server.log must be tolerated")
	assert.NoFileExists(t, link, "regular rotated log not removed")
}
