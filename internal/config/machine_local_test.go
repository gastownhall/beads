package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sampleValueFor returns a value that passes validateYamlConfigValue for key.
func sampleValueFor(key string) string {
	switch key {
	case "dolt.mode":
		return "server"
	case "dolt.port":
		return "3306"
	case "dolt.host":
		return "100.64.0.1"
	case "dolt.socket":
		return "/tmp/mysql.sock"
	case "dolt.user":
		return "bd"
	case "dolt.data-dir":
		return "/var/lib/bd"
	case "dolt.shared-server", "dolt.debug", "backup.enabled":
		return "true"
	case "backup.interval":
		return "30m"
	default:
		return "x"
	}
}

// trackedConfigFixture is a config.yaml whose content is project contract:
// every key in it is shared, and none is machine-local.
const trackedConfigFixture = `# Project contract.
issue_prefix: vp
dolt.auto-start: false          # shared: fleet policy
export.auto: false
types.custom: molecule,convoy
dolt:
  disable-event-flush: true
`

func newWorkspace(t *testing.T, configContent string) (beadsDir, configPath, localPath string) {
	t.Helper()
	beadsDir = filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	configPath = filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	return beadsDir, configPath, filepath.Join(beadsDir, LocalConfigFileName)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestMachineLocalKeysNeverReachTrackedConfig is the CLASS GUARD.
//
// It asserts the property the whole change exists to establish — no key in the
// registry is ever written to the git-tracked config.yaml — over the registry
// itself rather than over a hand-listed sample, so a key added to
// MachineLocalKeys later is covered the moment it is added.
func TestMachineLocalKeysNeverReachTrackedConfig(t *testing.T) {
	// Both public writers are covered. They resolve the target workspace
	// differently — SetYamlConfig discovers it, SetYamlConfigInDir is handed
	// it — and `bd config set`, the command that produced the reported
	// defect, goes through the discovering one.
	writers := map[string]func(t *testing.T, beadsDir, key, value string) error{
		"SetYamlConfigInDir": func(_ *testing.T, beadsDir, key, value string) error {
			return SetYamlConfigInDir(beadsDir, key, value)
		},
		"SetYamlConfig": func(t *testing.T, beadsDir, key, value string) error {
			t.Setenv("BEADS_DIR", beadsDir)
			return SetYamlConfig(key, value)
		},
	}

	for writerName, write := range writers {
		for key := range MachineLocalKeys {
			t.Run(writerName+"/"+key, func(t *testing.T) {
				beadsDir, configPath, localPath := newWorkspace(t, trackedConfigFixture)
				before := readFile(t, configPath)

				if err := write(t, beadsDir, key, sampleValueFor(key)); err != nil {
					t.Fatalf("%s(%s): %v", writerName, key, err)
				}

				if after := readFile(t, configPath); after != before {
					t.Errorf("config.yaml was modified by writing machine-local key %q via %s\n--- before ---\n%s\n--- after ---\n%s",
						key, writerName, before, after)
				}
				if got, ok := readYamlValueAtPath(localPath, key); !ok {
					t.Errorf("%s does not contain %q after the write", LocalConfigFileName, key)
				} else if want := sampleValueFor(key); got != want {
					t.Errorf("%s has %s = %q, want %q", LocalConfigFileName, key, got, want)
				}
			})
		}
	}
}

// TestSharedKeysStillReachTrackedConfig is the SURVIVING CONTROL for the class
// guard: it fails if routing is applied too broadly. Without it, a change that
// sent every key to the sidecar would pass the guard above.
func TestSharedKeysStillReachTrackedConfig(t *testing.T) {
	shared := []struct{ key, value string }{
		{"dolt.auto-start", "false"},     // fleet policy, committed on purpose
		{"dolt.max-conns", "20"},         // project tuning
		{"export.auto", "true"},          // project behavior
		{"sync.remote", "file:///tmp/r"}, // project remote
	}
	for _, tc := range shared {
		t.Run(tc.key, func(t *testing.T) {
			beadsDir, configPath, localPath := newWorkspace(t, trackedConfigFixture)
			before := readFile(t, configPath)

			if err := SetYamlConfigInDir(beadsDir, tc.key, tc.value); err != nil {
				t.Fatalf("SetYamlConfigInDir(%s): %v", tc.key, err)
			}

			if after := readFile(t, configPath); after == before {
				t.Errorf("config.yaml unchanged after writing SHARED key %q; it must still be written there", tc.key)
			}
			if _, err := os.Stat(localPath); err == nil {
				t.Errorf("writing shared key %q created %s; only machine-local keys belong there", tc.key, LocalConfigFileName)
			}
		})
	}
}

// TestMachineLocalSidecarWinsOnRead pins the precedence half of the contract:
// routing writes to the sidecar is only correct because reads merge it last.
func TestMachineLocalSidecarWinsOnRead(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A committed shared DEFAULT in the tracked file...
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
		[]byte("dolt.mode: embedded\ndolt.auto-start: false\n"), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	// ...overridden for THIS machine by the sidecar.
	if err := os.WriteFile(filepath.Join(beadsDir, LocalConfigFileName),
		[]byte("dolt.mode: server\n"), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	t.Chdir(tmpDir)
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if got := GetYamlConfig("dolt.mode"); got != "server" {
		t.Errorf("dolt.mode = %q, want \"server\" (sidecar must win over config.yaml)", got)
	}
	if got := GetBool("dolt.auto-start"); got != false {
		t.Errorf("dolt.auto-start = %v, want false (shared key still read from config.yaml)", got)
	}
	// The sidecar value must register as an explicit setting, not a default:
	// backup auto-detection and `bd config get` both branch on this.
	if src := GetValueSource("dolt.mode"); src != SourceConfigFile {
		t.Errorf("GetValueSource(dolt.mode) = %v, want %v", src, SourceConfigFile)
	}
}

func TestMigrationLiftsBothKeyFormsOutOfTrackedConfig(t *testing.T) {
	const tracked = `# Project contract.
issue_prefix: vp
dolt.auto-start: false          # shared: fleet policy
dolt:
  disable-event-flush: true
  mode: server
backup.enabled: false
`
	beadsDir, configPath, localPath := newWorkspace(t, tracked)

	if err := SetYamlConfigInDir(beadsDir, "dolt.port", "3306"); err != nil {
		t.Fatalf("SetYamlConfigInDir: %v", err)
	}

	after := readFile(t, configPath)

	// Both forms of machine-local key are gone from the tracked file...
	if _, ok := yamlValueInContent(after, "dolt.mode"); ok {
		t.Errorf("nested dolt.mode survived migration:\n%s", after)
	}
	if _, ok := yamlValueInContent(after, "backup.enabled"); ok {
		t.Errorf("flat backup.enabled survived migration:\n%s", after)
	}
	// ...and both landed in the sidecar with their values intact.
	if got, _ := readYamlValueAtPath(localPath, "dolt.mode"); got != "server" {
		t.Errorf("sidecar dolt.mode = %q, want \"server\"", got)
	}
	if got, _ := readYamlValueAtPath(localPath, "backup.enabled"); got != "false" {
		t.Errorf("sidecar backup.enabled = %q, want \"false\"", got)
	}

	// Shared keys and the file's shape are untouched: the operator has to
	// review and commit this diff, so it must stay small and readable.
	if !strings.Contains(after, "dolt.auto-start: false          # shared: fleet policy") {
		t.Errorf("shared key or its inline comment was disturbed:\n%s", after)
	}
	if !strings.Contains(after, "  disable-event-flush: true") {
		t.Errorf("shared nested sibling was disturbed:\n%s", after)
	}
	if !strings.Contains(after, "# Project contract.") {
		t.Errorf("leading comment was lost:\n%s", after)
	}
	if !strings.HasSuffix(after, "\n") {
		t.Errorf("trailing newline was dropped, adding noise to the cleanup diff:\n%q", after)
	}
}

// TestMigrationRunsOnlyOnce protects the escape hatch. A value an operator
// deliberately restores to config.yaml as a shared default must survive later
// machine-local writes; re-running the migration on every write would re-take
// it, reintroducing the self-dirtying churn with the sign flipped.
func TestMigrationRunsOnlyOnce(t *testing.T) {
	beadsDir, configPath, localPath := newWorkspace(t, "issue_prefix: vp\nbackup.enabled: false\n")

	if err := SetYamlConfigInDir(beadsDir, "dolt.port", "3306"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if !strings.Contains(readFile(t, localPath), machineLocalMigrationMarker) {
		t.Fatalf("migration marker not recorded in %s", LocalConfigFileName)
	}

	// The operator commits the cleanup, then deliberately re-adds a shared
	// default to the tracked file.
	restored := "issue_prefix: vp\n# backup.enabled: false\ndolt.mode: embedded\n"
	if err := os.WriteFile(configPath, []byte(restored), 0o600); err != nil {
		t.Fatalf("restore config.yaml: %v", err)
	}

	if err := SetYamlConfigInDir(beadsDir, "dolt.user", "bd"); err != nil {
		t.Fatalf("second write: %v", err)
	}

	if after := readFile(t, configPath); after != restored {
		t.Errorf("migration ran a second time and modified config.yaml\n--- want ---\n%s\n--- got ---\n%s", restored, after)
	}
	if count := strings.Count(readFile(t, localPath), machineLocalMigrationMarker); count != 1 {
		t.Errorf("migration marker appears %d times, want exactly 1", count)
	}
}

// TestMigrationMarkerSurvivesLaterWrites guards the marker's durability.
// Later writes round-trip the sidecar through yaml.Node; if the marker were
// dropped there, the one-time migration would silently become repeating.
func TestMigrationMarkerSurvivesLaterWrites(t *testing.T) {
	beadsDir, _, localPath := newWorkspace(t, "issue_prefix: vp\nbackup.enabled: false\n")

	for _, key := range []string{"dolt.port", "dolt.user", "dolt.debug", "backup.interval", "dolt.host"} {
		if err := SetYamlConfigInDir(beadsDir, key, sampleValueFor(key)); err != nil {
			t.Fatalf("SetYamlConfigInDir(%s): %v", key, err)
		}
	}

	if count := strings.Count(readFile(t, localPath), machineLocalMigrationMarker); count != 1 {
		t.Fatalf("marker appears %d times after 5 writes, want exactly 1:\n%s", count, readFile(t, localPath))
	}
}

func TestUnsetMachineLocalKeyLeavesTrackedConfigAlone(t *testing.T) {
	beadsDir, configPath, localPath := newWorkspace(t, trackedConfigFixture)

	if err := SetYamlConfigInDir(beadsDir, "dolt.mode", "server"); err != nil {
		t.Fatalf("set: %v", err)
	}
	before := readFile(t, configPath)

	t.Chdir(filepath.Dir(beadsDir))
	if err := UnsetYamlConfig("dolt.mode"); err != nil {
		t.Fatalf("UnsetYamlConfig: %v", err)
	}

	if after := readFile(t, configPath); after != before {
		t.Errorf("unsetting a machine-local key modified config.yaml:\n%s", after)
	}
	if _, ok := readYamlValueAtPath(localPath, "dolt.mode"); ok {
		t.Errorf("dolt.mode still live in %s after unset", LocalConfigFileName)
	}
}

func TestIsMachineLocalKeyIsExactNotPrefix(t *testing.T) {
	local := []string{"dolt.mode", "dolt.host", "backup.enabled", "backup.interval"}
	for _, key := range local {
		if !IsMachineLocalKey(key) {
			t.Errorf("IsMachineLocalKey(%q) = false, want true", key)
		}
	}
	// Unrecognized siblings under the same prefix must stay SHARED: prefix
	// matching is what made IsYamlOnlyKey sweep in keys nobody classified.
	shared := []string{
		"dolt.auto-start", "dolt.disable-event-flush", "dolt.max-conns",
		"dolt.pool-read-timeout", "backup.git-push", "backup.git-repo",
		"dolt", "backup", "dolt.mode.extra",
	}
	for _, key := range shared {
		if IsMachineLocalKey(key) {
			t.Errorf("IsMachineLocalKey(%q) = true, want false (unclassified keys stay shared)", key)
		}
	}
}

// TestMachineLocalKeysExcludeSecrets: secrets are covered by a STRICTER
// control (CheckSecretKeyGitSafety refuses the write). Routing one here would
// silently downgrade a refusal into a relocation.
func TestMachineLocalKeysExcludeSecrets(t *testing.T) {
	var offenders []string
	for key := range MachineLocalKeys {
		if IsSecretKey(key) {
			offenders = append(offenders, key)
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("secret keys must not be in MachineLocalKeys: %v", offenders)
	}
}

func TestCommentOutYamlKeyAnyForm(t *testing.T) {
	tests := []struct {
		name    string
		content string
		key     string
		want    string
	}{
		{
			name:    "flat form",
			content: "a: 1\nbackup.enabled: false\nb: 2",
			key:     "backup.enabled",
			want:    "a: 1\n# backup.enabled: false\nb: 2",
		},
		{
			name:    "nested form with surviving sibling",
			content: "dolt:\n  disable-event-flush: true\n  mode: server\nb: 2",
			key:     "dolt.mode",
			want:    "dolt:\n  disable-event-flush: true\n  # mode: server\nb: 2",
		},
		{
			name:    "nested form, last child empties the parent",
			content: "dolt:\n  mode: server\nb: 2",
			key:     "dolt.mode",
			want:    "# dolt:\n  # mode: server\nb: 2",
		},
		{
			name:    "deeper key of the same name is NOT touched",
			content: "dolt:\n  pool:\n    mode: fast\n  disable: true",
			key:     "dolt.mode",
			want:    "dolt:\n  pool:\n    mode: fast\n  disable: true",
		},
		{
			name:    "three-segment key is commented at the right depth",
			content: "dolt:\n  pool:\n    mode: fast\n    size: 4",
			key:     "dolt.pool.mode",
			want:    "dolt:\n  pool:\n    # mode: fast\n    size: 4",
		},
		{
			name:    "emptied ancestors are commented out up the chain",
			content: "dolt:\n  pool:\n    mode: fast\nother: 1",
			key:     "dolt.pool.mode",
			want:    "# dolt:\n  # pool:\n    # mode: fast\nother: 1",
		},
		{
			name:    "same-named key under a different parent is left alone",
			content: "other:\n  mode: keep\ndolt:\n  mode: server",
			key:     "dolt.mode",
			want:    "other:\n  mode: keep\n# dolt:\n  # mode: server",
		},
		{
			name:    "absent key is a no-op",
			content: "a: 1\nb: 2",
			key:     "dolt.mode",
			want:    "a: 1\nb: 2",
		},
		{
			name:    "already commented is left alone",
			content: "# dolt.mode: server\na: 1",
			key:     "dolt.mode",
			want:    "# dolt.mode: server\na: 1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := commentOutYamlKeyAnyForm(tc.content, tc.key); got != tc.want {
				t.Errorf("commentOutYamlKeyAnyForm()\ngot:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// TestUnsetMachineLocalKeyClearsAValueStillInTrackedConfig covers the state
// every workspace is in immediately after upgrading: the live value is still
// in config.yaml and no sidecar exists yet. Clearing only the sidecar would
// report success while the value stayed in effect.
func TestUnsetMachineLocalKeyClearsAValueStillInTrackedConfig(t *testing.T) {
	beadsDir, configPath, localPath := newWorkspace(t,
		"issue_prefix: vp\nbackup.enabled: false\nother-setting: value\n")

	t.Chdir(filepath.Dir(beadsDir))
	if err := UnsetYamlConfig("backup.enabled"); err != nil {
		t.Fatalf("UnsetYamlConfig: %v", err)
	}

	if _, ok := yamlValueInContent(readFile(t, configPath), "backup.enabled"); ok {
		t.Errorf("backup.enabled still live in config.yaml after unset:\n%s", readFile(t, configPath))
	}
	if _, ok := readYamlValueAtPath(localPath, "backup.enabled"); ok {
		t.Errorf("backup.enabled still live in %s after unset", LocalConfigFileName)
	}
	if !strings.Contains(readFile(t, configPath), "other-setting: value") {
		t.Errorf("unset disturbed an unrelated key:\n%s", readFile(t, configPath))
	}
}

// TestMigrationMarkerNotRecordedWhenTrackedWriteFails: the marker must not
// outlive a failed cleanup, or the one-time migration skips forever and
// strands the keys in the tracked file.
func TestMigrationMarkerNotRecordedWhenTrackedWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	beadsDir, configPath, localPath := newWorkspace(t, "issue_prefix: vp\nbackup.enabled: false\n")
	if err := os.Chmod(configPath, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = os.Chmod(configPath, 0o600) }()

	if err := SetYamlConfigInDir(beadsDir, "dolt.port", "3306"); err == nil {
		t.Fatal("expected an error when config.yaml is not writable")
	}
	if strings.Contains(readFile(t, localPath), machineLocalMigrationMarker) {
		t.Error("migration marker was recorded even though the config.yaml cleanup failed")
	}
}

// TestMachineLocalKeysAreReadableByDirScopedReaders pins the reader half.
// GetStringFromDir opens the workspace's files directly rather than going
// through merged viper; `bd bootstrap` resolves dolt.port through it, so a
// sidecar value invisible there would silently fall back to a default.
func TestMachineLocalKeysAreReadableByDirScopedReaders(t *testing.T) {
	beadsDir, _, _ := newWorkspace(t, "issue_prefix: vp\ndolt.port: \"1111\"\n")

	if err := SetYamlConfigInDir(beadsDir, "dolt.port", "3306"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := GetStringFromDir(beadsDir, "dolt.port"); got != "3306" {
		t.Errorf("GetStringFromDir(dolt.port) = %q, want \"3306\" (sidecar must win)", got)
	}
	// A shared key still resolves from config.yaml.
	if got := GetStringFromDir(beadsDir, "issue_prefix"); got != "vp" {
		t.Errorf("GetStringFromDir(issue_prefix) = %q, want \"vp\"", got)
	}
}

// preChangeBeadsGitignore is a .beads/.gitignore as written by a bd that
// predates the sidecar: it excludes the runtime files of its day and knows
// nothing about config.local.yaml. Repositories initialized before this change
// carry exactly this shape, and they are the ones the clean-tree property has
// to hold for.
const preChangeBeadsGitignore = `# Dolt database (managed by Dolt, not git)
dolt/
embeddeddolt/

# Runtime files
bd.sock
*.lock

# Backup data (auto-exported JSONL, local-only)
backup/
`

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// hooksPath and gpgsign are pinned so a developer's global git config
	// cannot make this test pass or fail for reasons unrelated to bd.
	full := append([]string{"-c", "core.hooksPath=", "-c", "commit.gpgsign=false",
		"-c", "user.email=bd-test@example.invalid", "-c", "user.name=bd test"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// newCommittedGitWorkspace builds a git repo whose working tree is clean and
// whose .beads/.gitignore predates the sidecar.
func newCommittedGitWorkspace(t *testing.T, trackedConfig string) (repoDir, beadsDir string) {
	t.Helper()
	repoDir = t.TempDir()
	runGit(t, repoDir, "init", "-q")

	beadsDir = filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, ".gitignore"), []byte(preChangeBeadsGitignore), 0o600); err != nil {
		t.Fatalf("write .beads/.gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(trackedConfig), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	runGit(t, repoDir, "add", "-A")
	runGit(t, repoDir, "commit", "-q", "--no-verify", "-m", "seed")

	if status := runGit(t, repoDir, "status", "--porcelain"); status != "" {
		t.Fatalf("fixture is not clean before the write:\n%s", status)
	}
	return repoDir, beadsDir
}

// TestMachineLocalWriteLeavesGitTreeClean is the PROPERTY the whole change
// exists to deliver, asserted the way an operator experiences it.
//
// Routing the key out of the tracked config.yaml is only half the job: on a
// checkout whose .beads/.gitignore predates the sidecar, the write used to
// leave `?? .beads/config.local.yaml` behind, and the release script, the
// pre-commit hook and the CI clean-tree step refused for the same reason as
// before — only the filename had changed. The ignore entry otherwise arrives
// only through `bd init` / `bd bootstrap` / `bd doctor --fix`, none of which a
// plain `bd config set` runs.
func TestMachineLocalWriteLeavesGitTreeClean(t *testing.T) {
	for _, key := range sortedMachineLocalKeys() {
		t.Run(key, func(t *testing.T) {
			repoDir, beadsDir := newCommittedGitWorkspace(t, trackedConfigFixture)

			if err := SetYamlConfigInDir(beadsDir, key, sampleValueFor(key)); err != nil {
				t.Fatalf("SetYamlConfigInDir(%s): %v", key, err)
			}

			// The sidecar really was written — otherwise a no-op write would
			// pass this test trivially.
			localPath := filepath.Join(beadsDir, LocalConfigFileName)
			if _, err := os.Stat(localPath); err != nil {
				t.Fatalf("%s was not created: %v", LocalConfigFileName, err)
			}

			if status := runGit(t, repoDir, "status", "--porcelain"); status != "" {
				t.Errorf("working tree is dirty after writing machine-local key %q:\n%s\n.beads/.gitignore:\n%s",
					key, status, readFile(t, filepath.Join(beadsDir, ".gitignore")))
			}
		})
	}
}

// TestMigrationLeavesGitTreeCleanApartFromTheOneTimeCleanup covers the harder
// case: config.yaml really does contain machine-local keys, so the one-time
// migration edits it. That single edit is the reviewable diff the operator
// commits once; nothing else may show up beside it, and in particular not an
// untracked sidecar.
func TestMigrationLeavesGitTreeCleanApartFromTheOneTimeCleanup(t *testing.T) {
	const tracked = `issue_prefix: vp
dolt.auto-start: false
dolt:
  mode: embedded
backup.enabled: true
`
	repoDir, beadsDir := newCommittedGitWorkspace(t, tracked)

	if err := SetYamlConfigInDir(beadsDir, "dolt.port", "3306"); err != nil {
		t.Fatalf("SetYamlConfigInDir: %v", err)
	}

	status := runGit(t, repoDir, "status", "--porcelain")
	if status != " M .beads/config.yaml\n" {
		t.Errorf("want only the one-time config.yaml cleanup in git status, got:\n%s", status)
	}

	// And once that cleanup is committed, later writes leave nothing at all.
	runGit(t, repoDir, "commit", "-q", "--no-verify", "-a", "-m", "cleanup")
	if err := SetYamlConfigInDir(beadsDir, "dolt.user", "bd"); err != nil {
		t.Fatalf("second SetYamlConfigInDir: %v", err)
	}
	if status := runGit(t, repoDir, "status", "--porcelain"); status != "" {
		t.Errorf("working tree is dirty after the migration was committed:\n%s", status)
	}
}

// TestSidecarExcludeIsIdempotentAndAdditive pins the ways the per-clone
// exclusion could go wrong. Operators and other bd code write to
// .git/info/exclude too, so repeated writes must not accumulate duplicate
// lines, and an existing file must keep its content and its shape.
func TestSidecarExcludeIsIdempotentAndAdditive(t *testing.T) {
	excludeOf := func(repoDir string) string {
		return filepath.Join(repoDir, ".git", "info", "exclude")
	}

	t.Run("appends once across repeated writes", func(t *testing.T) {
		repoDir, beadsDir := newCommittedGitWorkspace(t, trackedConfigFixture)
		const existing = "# operator's own exclusions\nscratch/\n"
		if err := os.WriteFile(excludeOf(repoDir), []byte(existing), 0o600); err != nil {
			t.Fatalf("seed exclude: %v", err)
		}

		for _, key := range []string{"dolt.mode", "dolt.port", "dolt.user"} {
			if err := SetYamlConfigInDir(beadsDir, key, sampleValueFor(key)); err != nil {
				t.Fatalf("SetYamlConfigInDir(%s): %v", key, err)
			}
		}

		got := readFile(t, excludeOf(repoDir))
		if count := strings.Count(got, "/.beads/"+LocalConfigFileName); count != 1 {
			t.Errorf("sidecar excluded %d times after 3 writes, want exactly 1:\n%s", count, got)
		}
		if !strings.HasPrefix(got, existing) {
			t.Errorf("existing exclude content was not preserved verbatim:\n%s", got)
		}
	})

	t.Run("does nothing when the tracked .gitignore already covers it", func(t *testing.T) {
		repoDir, beadsDir := newCommittedGitWorkspace(t, trackedConfigFixture)
		ignorePath := filepath.Join(beadsDir, ".gitignore")
		if err := os.WriteFile(ignorePath, []byte(preChangeBeadsGitignore+LocalConfigFileName+"\n"), 0o600); err != nil {
			t.Fatalf("seed .gitignore: %v", err)
		}
		runGit(t, repoDir, "commit", "-q", "--no-verify", "-a", "-m", "modern gitignore")
		before, err := os.ReadFile(excludeOf(repoDir))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read exclude: %v", err)
		}

		if err := SetYamlConfigInDir(beadsDir, "dolt.mode", "server"); err != nil {
			t.Fatalf("SetYamlConfigInDir: %v", err)
		}

		after, err := os.ReadFile(excludeOf(repoDir))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read exclude: %v", err)
		}
		if string(after) != string(before) {
			t.Errorf("wrote a redundant per-clone exclusion although the tracked .gitignore already lists %s:\n%s", LocalConfigFileName, after)
		}
		if status := runGit(t, repoDir, "status", "--porcelain"); status != "" {
			t.Errorf("working tree is dirty:\n%s", status)
		}
	})

	t.Run("a commented-out .gitignore entry does not count", func(t *testing.T) {
		repoDir, beadsDir := newCommittedGitWorkspace(t, trackedConfigFixture)
		ignorePath := filepath.Join(beadsDir, ".gitignore")
		if err := os.WriteFile(ignorePath, []byte("# "+LocalConfigFileName+"\n"), 0o600); err != nil {
			t.Fatalf("seed .gitignore: %v", err)
		}
		runGit(t, repoDir, "commit", "-q", "--no-verify", "-a", "-m", "commented out")

		if err := SetYamlConfigInDir(beadsDir, "dolt.mode", "server"); err != nil {
			t.Fatalf("SetYamlConfigInDir: %v", err)
		}

		if status := runGit(t, repoDir, "status", "--porcelain"); status != "" {
			t.Errorf("a commented-out line was mistaken for a live entry; tree is dirty:\n%s", status)
		}
	})

	t.Run("outside a git work tree it is a no-op", func(t *testing.T) {
		beadsDir, _, localPath := newWorkspace(t, trackedConfigFixture)

		if err := SetYamlConfigInDir(beadsDir, "dolt.mode", "server"); err != nil {
			t.Fatalf("SetYamlConfigInDir: %v", err)
		}
		if got, _ := readYamlValueAtPath(localPath, "dolt.mode"); got != "server" {
			t.Errorf("sidecar dolt.mode = %q, want \"server\"", got)
		}
	})
}

// TestMigrationOutputIsDeterministic pins the fix for a migration that ranged
// over the MachineLocalKeys map.
//
// updateYamlKey picks the flat or the nested form from what the document
// already contains, so map order decided the shape of the result: identical
// input produced a different sidecar on each run, one of them carrying a
// top-level flat `dolt.mode` and a separate nested `dolt:` block for the same
// namespace. Two competing representations of one namespace read back fine
// through viper but are a duplicate-key hazard for a stricter parser.
func TestMigrationOutputIsDeterministic(t *testing.T) {
	// A tracked file holding machine-local keys in BOTH forms, so the
	// migration has to write into both shapes in one pass.
	const tracked = `issue_prefix: vp
dolt.auto-start: false
dolt:
  mode: embedded
  host: 10.0.0.1
backup.enabled: true
backup.interval: 15m
dolt.port: 3307
dolt.user: alice
`

	var firstLocal, firstTracked string
	const runs = 12 // enough that map order would differ across runs with high probability
	for i := 0; i < runs; i++ {
		beadsDir, configPath, localPath := newWorkspace(t, tracked)
		if err := SetYamlConfigInDir(beadsDir, "dolt.debug", "true"); err != nil {
			t.Fatalf("run %d: SetYamlConfigInDir: %v", i, err)
		}
		gotLocal, gotTracked := readFile(t, localPath), readFile(t, configPath)
		if i == 0 {
			firstLocal, firstTracked = gotLocal, gotTracked
			continue
		}
		if gotLocal != firstLocal {
			t.Fatalf("run %d produced a different %s\n--- run 0 ---\n%s\n--- run %d ---\n%s",
				i, LocalConfigFileName, firstLocal, i, gotLocal)
		}
		if gotTracked != firstTracked {
			t.Fatalf("run %d produced a different config.yaml\n--- run 0 ---\n%s\n--- run %d ---\n%s",
				i, firstTracked, i, gotTracked)
		}
	}

	// The specific corruption the reported defect showed: one namespace must
	// not appear in both the flat and the nested form in the same document.
	for _, ns := range []string{"dolt", "backup"} {
		nested := strings.Contains(firstLocal, "\n"+ns+":\n")
		flat := strings.Contains(firstLocal, "\n"+ns+".")
		if nested && flat {
			t.Errorf("%s carries both a flat %s.* key and a nested %s: block:\n%s",
				LocalConfigFileName, ns, ns, firstLocal)
		}
	}

	// The scratch key that anchors the mapping must never survive to disk;
	// viper would merge it into the live settings and `bd config list` would
	// show it.
	if strings.Contains(firstLocal, migrationAnchorKey) {
		t.Errorf("migration anchor leaked into %s:\n%s", LocalConfigFileName, firstLocal)
	}

	// Determinism is worthless if the values are wrong; every migrated key
	// still has to resolve.
	for key, want := range map[string]string{
		"dolt.mode": "embedded", "dolt.host": "10.0.0.1", "dolt.port": "3307",
		"dolt.user": "alice", "backup.enabled": "true", "backup.interval": "15m",
		"dolt.debug": "true",
	} {
		if got, ok := yamlValueInContent(firstLocal, key); !ok || got != want {
			t.Errorf("sidecar %s = %q (found=%v), want %q\n%s", key, got, ok, want, firstLocal)
		}
	}
}

// TestSaveConfigValueRefusesMachineLocalKeys guards the one writer of the
// tracked config.yaml that is deliberately NOT routed.
//
// SaveConfigValue takes an interface{} value and re-marshals the whole
// document through viper, which is exactly the whole-file rewrite the sidecar
// path exists to avoid; routing it would mean duplicating the validated
// string-write path for a caller that does not need it. Its only caller writes
// no-git-ops, a genuine project key. Refusing instead keeps the "no machine-
// local key ever reaches config.yaml" invariant enforced at runtime for the
// next key added to the registry, rather than leaving it to whoever notices.
func TestSaveConfigValueRefusesMachineLocalKeys(t *testing.T) {
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(ResetForTesting)

	for _, key := range sortedMachineLocalKeys() {
		t.Run(key, func(t *testing.T) {
			beadsDir, configPath, _ := newWorkspace(t, trackedConfigFixture)
			before := readFile(t, configPath)

			err := SaveConfigValue(key, sampleValueFor(key), beadsDir)
			if err == nil {
				t.Fatalf("SaveConfigValue(%s) succeeded; it must refuse a machine-local key", key)
			}
			if !strings.Contains(err.Error(), LocalConfigFileName) {
				t.Errorf("error does not point the caller at the sidecar: %v", err)
			}
			if after := readFile(t, configPath); after != before {
				t.Errorf("config.yaml was modified despite the refusal:\n%s", after)
			}
		})
	}
}

// TestSaveConfigValueStillWritesSharedKeys is the surviving control for the
// guard above: it fails if the refusal is applied too broadly and breaks the
// no-git-ops caller in cmd/bd/init.go.
func TestSaveConfigValueStillWritesSharedKeys(t *testing.T) {
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(ResetForTesting)

	beadsDir, configPath, _ := newWorkspace(t, trackedConfigFixture)
	if err := SaveConfigValue("no-git-ops", true, beadsDir); err != nil {
		t.Fatalf("SaveConfigValue(no-git-ops): %v", err)
	}
	if !strings.Contains(readFile(t, configPath), "no-git-ops") {
		t.Errorf("no-git-ops was not written to config.yaml:\n%s", readFile(t, configPath))
	}
}
