package fix

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// confineSymlink creates link -> target, skipping where symlinks are unavailable.
func confineSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
}

// confineWriteFile creates path's parents and writes content.
func confineWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// confineMetadata writes raw metadata.json: Config.Save drops absolute
// dolt_data_dir values, which these fixtures need.
func confineMetadata(t *testing.T, beadsDir string, fields map[string]string) {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	confineWriteFile(t, filepath.Join(beadsDir, "metadata.json"), string(data))
}

// confineTree records every entry under root without following symlinks:
// file content, "<dir>", or "<link> target".
func confineTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			tree[rel] = "<link> " + target
		case d.IsDir():
			tree[rel] = "<dir>"
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tree[rel] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func confineTreesEqual(t *testing.T, want, got map[string]string, label string) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s changed: before %v, after %v", label, want, got)
		return
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s changed at %q: before %q, after %q", label, k, v, got[k])
		}
	}
}

// unsafeDatabaseCases are metadata selections doctor must not back up or
// delete: each arranges dir (the workspace root) and outside (unrelated data
// holding a sentinel at outside/data/sentinel).
var unsafeDatabaseCases = []struct {
	name  string
	setup func(t *testing.T, beadsDir, outside string)
}{
	{"relative dolt_data_dir escaping .beads", func(t *testing.T, beadsDir, _ string) {
		confineWriteFile(t, filepath.Join(filepath.Dir(beadsDir), "escaped", "sentinel"), "keep")
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_data_dir": "../escaped"})
	}},
	{"absolute dolt_data_dir outside", func(t *testing.T, beadsDir, outside string) {
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_data_dir": filepath.Join(outside, "data")})
	}},
	{"absolute database outside", func(t *testing.T, beadsDir, outside string) {
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "database": filepath.Join(outside, "data")})
	}},
	{"sqlite database outside with sidecars", func(t *testing.T, beadsDir, outside string) {
		db := filepath.Join(outside, "data", "beads.db")
		confineWriteFile(t, db, "db")
		confineWriteFile(t, db+"-wal", "wal")
		confineWriteFile(t, db+"-shm", "shm")
		confineMetadata(t, beadsDir, map[string]string{"backend": "sqlite", "database": db})
	}},
	{"workspace root itself", func(t *testing.T, beadsDir, _ string) {
		confineWriteFile(t, filepath.Join(beadsDir, "sentinel"), "keep")
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_data_dir": "."})
	}},
	{"symlinked ancestor escaping .beads", func(t *testing.T, beadsDir, outside string) {
		confineSymlink(t, outside, filepath.Join(beadsDir, "linked"))
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_data_dir": "linked/data"})
	}},
	{"database leaf linked outside", func(t *testing.T, beadsDir, outside string) {
		confineSymlink(t, filepath.Join(outside, "data"), filepath.Join(beadsDir, "dolt"))
	}},
	{"dangling database leaf link", func(t *testing.T, beadsDir, outside string) {
		confineSymlink(t, filepath.Join(outside, "missing"), filepath.Join(beadsDir, "dolt"))
	}},
	{"database leaf linked inside .beads", func(t *testing.T, beadsDir, _ string) {
		confineWriteFile(t, filepath.Join(beadsDir, "real-dolt", "sentinel"), "keep")
		confineSymlink(t, filepath.Join(beadsDir, "real-dolt"), filepath.Join(beadsDir, "dolt"))
	}},
	{"absolute resolved spelling of a symlinked .beads", func(t *testing.T, beadsDir, outside string) {
		real := filepath.Join(outside, "beads-real")
		if err := os.Rename(beadsDir, real); err != nil {
			t.Fatal(err)
		}
		confineSymlink(t, real, beadsDir)
		confineWriteFile(t, filepath.Join(real, "dolt", "sentinel"), "keep")
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_data_dir": filepath.Join(real, "dolt")})
	}},
}

// setupUnsafeDatabase returns a workspace and an outside directory arranged
// by setup, with BEADS_DOLT_DATA_DIR cleared so only metadata selects.
func setupUnsafeDatabase(t *testing.T, setup func(t *testing.T, beadsDir, outside string)) (dir, outside string) {
	t.Helper()
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	dir = setupTestWorkspace(t)
	outside = t.TempDir()
	confineWriteFile(t, filepath.Join(outside, "data", "sentinel"), "keep")
	setup(t, filepath.Join(dir, ".beads"), outside)
	return dir, outside
}

func TestDatabaseIntegrity_RefusesUnconfinedDatabasePath(t *testing.T) {
	for _, tc := range unsafeDatabaseCases {
		t.Run(tc.name, func(t *testing.T) {
			dir, outside := setupUnsafeDatabase(t, tc.setup)
			dirBefore, outsideBefore := confineTree(t, dir), confineTree(t, outside)

			err := DatabaseIntegrity(dir)
			if err == nil || errors.Is(err, ErrTestBinary) || !strings.Contains(err.Error(), "refusing") {
				t.Fatalf("expected a confinement refusal before backup, got %v", err)
			}
			confineTreesEqual(t, dirBefore, confineTree(t, dir), "workspace")
			confineTreesEqual(t, outsideBefore, confineTree(t, outside), "outside data")
		})
	}
}

func TestRepoFingerprint_Choice2RefusesUnconfinedDatabaseBeforeConfirmation(t *testing.T) {
	for _, tc := range unsafeDatabaseCases {
		t.Run(tc.name, func(t *testing.T) {
			dir, outside := setupUnsafeDatabase(t, tc.setup)
			dirBefore, outsideBefore := confineTree(t, dir), confineTree(t, outside)

			oldReadLine := repoFingerprintReadLine
			t.Cleanup(func() { repoFingerprintReadLine = oldReadLine })
			answers := []string{"2", "n"}
			reads := 0
			repoFingerprintReadLine = func() (string, error) {
				reads++
				return answers[min(reads, len(answers))-1], nil
			}

			err := RepoFingerprint(dir, false)
			if err == nil || !strings.Contains(err.Error(), "refusing") {
				t.Fatalf("expected a confinement refusal, got %v", err)
			}
			if reads != 1 {
				t.Fatalf("expected the refusal before the DELETE confirmation (1 read), got %d reads", reads)
			}
			confineTreesEqual(t, dirBefore, confineTree(t, dir), "workspace")
			confineTreesEqual(t, outsideBefore, confineTree(t, outside), "outside data")
		})
	}
}

// Choice 2 on an admitted database unlinks its sidecars without following
// them: the parent is confined, and an outside sidecar target survives.
func TestRepoFingerprint_Choice2KeepsOutsideSidecarTargets(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	outside := t.TempDir()
	db := filepath.Join(beadsDir, "beads.db")
	confineWriteFile(t, db, "db")
	for _, suffix := range []string{"-wal", "-shm"} {
		confineWriteFile(t, filepath.Join(outside, "target"+suffix), "keep")
		confineSymlink(t, filepath.Join(outside, "target"+suffix), db+suffix)
	}
	confineMetadata(t, beadsDir, map[string]string{"backend": "sqlite", "database": db})
	outsideBefore := confineTree(t, outside)

	oldReadLine := repoFingerprintReadLine
	t.Cleanup(func() { repoFingerprintReadLine = oldReadLine })
	answers := []string{"2", "y"}
	repoFingerprintReadLine = func() (string, error) {
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}

	_ = RepoFingerprint(dir, false) // reinitializing a sqlite workspace fails; only the removal matters
	if _, err := os.Lstat(db); !os.IsNotExist(err) {
		t.Fatalf("expected the admitted database removed, got %v", err)
	}
	confineTreesEqual(t, outsideBefore, confineTree(t, outside), "outside sidecar targets")
}

// replaceWithOutsideLink moves path (when present) to moved and leaves a
// symlink to target in its place, as a concurrent actor could while bd init
// runs.
func replaceWithOutsideLink(t *testing.T, path, moved, target string) {
	t.Helper()
	if err := os.Rename(path, moved); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseIntegrity_RollbackRefusesReplacedDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake bd binary is a POSIX shell script")
	}
	for _, tc := range []struct {
		name        string
		dataDir     string // dolt_data_dir ("" for the default .beads/dolt)
		replace     string // .beads-relative entry the swap replaces
		lookupError bool   // swap during the binary lookup, which then fails
	}{
		{name: "database leaf", replace: "dolt"},
		{name: "database ancestor", dataDir: "data/dolt", replace: "data"},
		{name: "binary lookup failure", replace: "dolt", lookupError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_DATA_DIR", "")
			t.Setenv("BEADS_DOLT_SERVER_MODE", "")
			t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
			// Recovery names the canonical workspace path in its error.
			dir, err := filepath.EvalSymlinks(setupTestWorkspace(t))
			if err != nil {
				t.Fatal(err)
			}
			beadsDir := filepath.Join(dir, ".beads")
			doltPath := filepath.Join(beadsDir, "dolt")
			if tc.dataDir != "" {
				doltPath = filepath.Join(beadsDir, filepath.FromSlash(tc.dataDir))
				confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_data_dir": tc.dataDir})
			}
			confineWriteFile(t, filepath.Join(doltPath, "state"), "corrupt")
			outside := t.TempDir()
			confineWriteFile(t, filepath.Join(outside, "sentinel"), "keep")
			confineWriteFile(t, filepath.Join(outside, "dolt", "sentinel"), "keep")
			outsideBefore := confineTree(t, outside)
			replaced := filepath.Join(beadsDir, tc.replace)
			moved := filepath.Join(t.TempDir(), "moved")

			lookupErr := errors.New("bd binary lookup failed")
			oldBinary := integrityBdBinary
			t.Cleanup(func() { integrityBdBinary = oldBinary })
			integrityBdBinary = func() (string, error) {
				if tc.lookupError {
					replaceWithOutsideLink(t, replaced, moved, outside)
					return "", lookupErr
				}
				script := filepath.Join(t.TempDir(), "bd")
				body := "#!/bin/sh\nif [ -e '" + replaced + "' ]; then mv '" + replaced + "' '" + moved + "'; fi\n" +
					"ln -s '" + outside + "' '" + replaced + "'\nexit 1\n"
				if err := os.WriteFile(script, []byte(body), 0o700); err != nil { // #nosec G306 -- test script must be executable
					t.Fatal(err)
				}
				return script, nil
			}

			stdout := captureFixStdout(t, func() { err = DatabaseIntegrity(dir) })
			if err == nil || !strings.Contains(err.Error(), "not restoring the corrupted Dolt database") ||
				!strings.Contains(err.Error(), "changed during recovery") {
				t.Fatalf("expected the rollback to refuse the replaced destination, got %v", err)
			}
			if tc.lookupError && !errors.Is(err, lookupErr) {
				t.Fatalf("expected the original lookup failure to be kept, got %v", err)
			}
			// Setting BEADS_DOLT_DATA_DIR, the admission remedy, would exempt
			// the replaced location.
			if strings.Contains(err.Error(), "BEADS_DOLT_DATA_DIR") {
				t.Fatalf("rollback refusal offers the BEADS_DOLT_DATA_DIR remedy: %v", err)
			}
			if strings.Contains(stdout, "restoring corrupted Dolt database") {
				t.Fatalf("recovery announced the restore it refused: %q", stdout)
			}
			// The backup sits beside the database, so an ancestor swap carries
			// it to moved; the error names where recovery wrote it and says a
			// replaced directory takes it along.
			if !strings.Contains(err.Error(), "if a directory above the database was replaced, the backup moved with that directory") {
				t.Fatalf("error %q does not warn that a replaced directory moves the backup", err)
			}
			backupDir := filepath.Dir(doltPath)
			if replaced != doltPath {
				rel, relErr := filepath.Rel(replaced, backupDir)
				if relErr != nil {
					t.Fatal(relErr)
				}
				backupDir = filepath.Join(moved, rel)
			}
			backups, globErr := filepath.Glob(filepath.Join(backupDir, "dolt.*.corrupt.backup"))
			if globErr != nil || len(backups) != 1 {
				t.Fatalf("expected the backup to stay in place, got %v (%v)", backups, globErr)
			}
			if reported := filepath.Join(filepath.Dir(doltPath), filepath.Base(backups[0])); !strings.Contains(err.Error(), reported) {
				t.Fatalf("error %q does not report the backup location %s", err, reported)
			}
			if data, rerr := os.ReadFile(filepath.Join(backups[0], "state")); rerr != nil || string(data) != "corrupt" {
				t.Fatalf("backup contents changed: %q, %v", data, rerr)
			}
			confineTreesEqual(t, outsideBefore, confineTree(t, outside), "outside data")
		})
	}
}

// A failed reinitialization at an unchanged destination still rolls back:
// what bd init left is removed, the original database is restored and the
// backup is consumed.
// Adapted from the GLM candidate's TestRestoreIntegrityBackupRestoresNormally.
func TestDatabaseIntegrity_RollbackRestoresUnchangedDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake bd binary is a POSIX shell script")
	}
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	dir := setupTestWorkspace(t)
	doltPath := filepath.Join(dir, ".beads", "dolt")
	confineWriteFile(t, filepath.Join(doltPath, "state"), "corrupt")

	oldBinary := integrityBdBinary
	t.Cleanup(func() { integrityBdBinary = oldBinary })
	integrityBdBinary = func() (string, error) {
		script := filepath.Join(t.TempDir(), "bd")
		body := "#!/bin/sh\nmkdir -p '" + doltPath + "' && echo partial > '" + filepath.Join(doltPath, "partial") + "'\nexit 1\n"
		if err := os.WriteFile(script, []byte(body), 0o700); err != nil { // #nosec G306 -- test script must be executable
			t.Fatal(err)
		}
		return script, nil
	}

	var err error
	stdout := captureFixStdout(t, func() { err = DatabaseIntegrity(dir) })
	if err == nil || !strings.Contains(err.Error(), "failed to reinitialize") || strings.Contains(err.Error(), "not restoring") {
		t.Fatalf("expected the reinitialize failure without a refusal, got %v", err)
	}
	if !strings.Contains(stdout, "restoring corrupted Dolt database") {
		t.Fatalf("expected recovery to announce the restore, got %q", stdout)
	}
	if data, rerr := os.ReadFile(filepath.Join(doltPath, "state")); rerr != nil || string(data) != "corrupt" {
		t.Fatalf("original database not restored: %q, %v", data, rerr)
	}
	if _, serr := os.Lstat(filepath.Join(doltPath, "partial")); !os.IsNotExist(serr) {
		t.Fatalf("expected the partial reinitialization removed, got %v", serr)
	}
	if backups, _ := filepath.Glob(filepath.Join(filepath.Dir(doltPath), "dolt.*.corrupt.backup")); len(backups) != 0 {
		t.Fatalf("expected the backup consumed by the restore, got %v", backups)
	}
}

// confinedDatabaseLayouts are layouts doctor still backs up and deletes:
// each returns the database path the fix selects.
var confinedDatabaseLayouts = []struct {
	name  string
	setup func(t *testing.T, dir string) string
}{
	{"default .beads/dolt", func(t *testing.T, dir string) string {
		return filepath.Join(dir, ".beads", "dolt")
	}},
	{"dolt_data_dir inside .beads", func(t *testing.T, dir string) string {
		beadsDir := filepath.Join(dir, ".beads")
		confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_data_dir": "data/dolt"})
		return filepath.Join(beadsDir, "data", "dolt")
	}},
	{"symlinked .beads", func(t *testing.T, dir string) string {
		real := filepath.Join(t.TempDir(), "beads-data")
		if err := os.MkdirAll(real, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, ".beads")); err != nil {
			t.Fatal(err)
		}
		confineSymlink(t, real, filepath.Join(dir, ".beads"))
		return filepath.Join(real, "dolt")
	}},
	{"absolute BEADS_DOLT_DATA_DIR outside", func(t *testing.T, dir string) string {
		confineMetadata(t, filepath.Join(dir, ".beads"), map[string]string{"backend": "dolt"})
		selected := filepath.Join(t.TempDir(), "external-dolt")
		t.Setenv("BEADS_DOLT_DATA_DIR", selected)
		return selected
	}},
	{"relative BEADS_DOLT_DATA_DIR outside", func(t *testing.T, dir string) string {
		confineMetadata(t, filepath.Join(dir, ".beads"), map[string]string{"backend": "dolt"})
		t.Setenv("BEADS_DOLT_DATA_DIR", "../external-dolt")
		return filepath.Join(dir, "external-dolt")
	}},
}

func setupConfinedDatabase(t *testing.T, setup func(t *testing.T, dir string) string) (dir, dbPath string) {
	t.Helper()
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	dir = setupTestWorkspace(t)
	dbPath = setup(t, dir)
	confineWriteFile(t, filepath.Join(dbPath, "state"), "corrupt")
	return dir, dbPath
}

func TestDatabaseIntegrity_AdmitsConfinedAndSelectedDatabases(t *testing.T) {
	for _, tc := range confinedDatabaseLayouts {
		t.Run(tc.name, func(t *testing.T) {
			dir, dbPath := setupConfinedDatabase(t, tc.setup)

			// Admission reaches the backup and the bd binary lookup, which a
			// test binary refuses; the backup is then moved back.
			if err := DatabaseIntegrity(dir); !errors.Is(err, ErrTestBinary) {
				t.Fatalf("expected recovery to reach the bd binary lookup, got %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(dbPath, "state")); err != nil || string(data) != "corrupt" {
				t.Fatalf("database not restored after the lookup failure: %q, %v", data, err)
			}
		})
	}
}

func TestRepoFingerprint_Choice2AdmitsConfinedAndSelectedDatabases(t *testing.T) {
	for _, tc := range confinedDatabaseLayouts {
		t.Run(tc.name, func(t *testing.T) {
			dir, dbPath := setupConfinedDatabase(t, tc.setup)

			oldReadLine := repoFingerprintReadLine
			t.Cleanup(func() { repoFingerprintReadLine = oldReadLine })
			answers := []string{"2", "n"}
			reads := 0
			repoFingerprintReadLine = func() (string, error) {
				reads++
				return answers[min(reads, len(answers))-1], nil
			}

			if err := RepoFingerprint(dir, false); err != nil {
				t.Fatalf("expected the DELETE confirmation to be offered and declined, got %v", err)
			}
			if reads != 2 {
				t.Fatalf("expected the DELETE confirmation to be read, got %d reads", reads)
			}
			if _, err := os.Stat(filepath.Join(dbPath, "state")); err != nil {
				t.Fatalf("declined confirmation changed the database: %v", err)
			}
		})
	}
}

func TestDatabaseIntegrity_ServerModeRefusalPrecedesConfinement(t *testing.T) {
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	dir := setupTestWorkspace(t)
	beadsDir := filepath.Join(dir, ".beads")
	confineWriteFile(t, filepath.Join(dir, "escaped", "sentinel"), "keep")
	confineMetadata(t, beadsDir, map[string]string{"backend": "dolt", "dolt_mode": "server", "dolt_data_dir": "../escaped"})

	err := DatabaseIntegrity(dir)
	if err == nil || !strings.Contains(err.Error(), "automatic integrity recovery is disabled for server-mode repos") {
		t.Fatalf("expected the server-mode refusal first, got %v", err)
	}
}

func TestConfinedDatabasePath_EnvExemptsOnlyTheSelectedPath(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(t.TempDir(), "selected")
	other := filepath.Join(t.TempDir(), "other")

	t.Setenv("BEADS_DOLT_DATA_DIR", selected)
	if got, err := confinedDatabasePath(beadsDir, selected+string(filepath.Separator)); err != nil || got != selected {
		t.Fatalf("expected the env-selected path admitted, got %q, %v", got, err)
	}
	if _, err := confinedDatabasePath(beadsDir, other); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("expected a path the env does not select refused, got %v", err)
	}

	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	if _, err := confinedDatabasePath(beadsDir, selected); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("expected an empty BEADS_DOLT_DATA_DIR to exempt nothing, got %v", err)
	}
}
