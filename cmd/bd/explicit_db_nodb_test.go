//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

func buildBDUnderTest(t *testing.T) string {
	t.Helper()

	binName := "bd"
	if runtime.GOOS == "windows" {
		binName = "bd.exe"
	}
	binPath := filepath.Join(t.TempDir(), binName)
	buildCmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", binPath, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	return binPath
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
	writeFile(t, filepath.Join(dir, ".gitignore"), []byte(".beads/.env\n"))
	commitCmd := exec.Command("git", "add", ".")
	commitCmd.Dir = dir
	_, _ = commitCmd.CombinedOutput()
	commitCmd = exec.Command("git", "commit", "-q", "-m", "init")
	commitCmd.Dir = dir
	commitCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	_, _ = commitCmd.CombinedOutput()
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeServerRepo(t *testing.T, repoDir, database, host, syncRemote string, port int) string {
	return writeServerRepoWithDataDir(t, repoDir, database, host, syncRemote, port, "")
}

func writeServerRepoWithDataDir(t *testing.T, repoDir, database, host, syncRemote string, port int, doltDataDir string) string {
	t.Helper()
	initGitRepo(t, repoDir)
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	doltDir := filepath.Join(beadsDir, "dolt")
	if doltDataDir != "" {
		doltDir = filepath.Join(beadsDir, doltDataDir)
	}
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("mkdir dolt dir: %v", err)
	}
	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: host,
		DoltDatabase:   database,
	}
	if doltDataDir != "" {
		cfg.DoltDataDir = doltDataDir
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save metadata: %v", err)
	}
	writeFile(t, filepath.Join(beadsDir, "dolt-server.port"), []byte(strconv.Itoa(port)))
	writeFile(t, filepath.Join(beadsDir, "config.yaml"), []byte("sync:\n  git-remote: "+syncRemote+"\n"))
	writeFile(t, filepath.Join(beadsDir, ".env"), []byte("BEADS_DOLT_SERVER_HOST="+host+"\n"))
	return beadsDir
}

func writeProjectConfig(t *testing.T, beadsDir string, syncRemote string, port int, shared bool) {
	t.Helper()
	sharedText := "false"
	if shared {
		sharedText = "true"
	}
	writeFile(t, filepath.Join(beadsDir, "config.yaml"), []byte(
		"sync:\n  git-remote: "+syncRemote+"\n"+
			"dolt:\n  port: "+strconv.Itoa(port)+"\n  shared-server: "+sharedText+"\n",
	))
}

// evalPath resolves symlinks in a path for consistent comparison.
// On macOS, t.TempDir() returns /var/folders/... but binaries resolve
// it to /private/var/folders/..., causing string comparison failures.
func evalPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", path, err)
	}
	return resolved
}

func decodeJSONOutput(t *testing.T, out []byte, target any) {
	t.Helper()
	trimmed := strings.TrimSpace(string(out))
	idx := strings.Index(trimmed, "{")
	if idx == -1 {
		t.Fatalf("no JSON object found in output:\n%s", out)
	}
	if err := json.Unmarshal([]byte(trimmed[idx:]), target); err != nil {
		t.Fatalf("unmarshal json: %v\n%s", err, out)
	}
}

func runBDCommand(t *testing.T, binPath, dir string, extraEnv []string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BEADS_DIR=",
		"BEADS_DB=",
		"BEADS_DOLT_SERVER_DATABASE=",
		"BEADS_DOLT_SERVER_HOST=",
		"BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_PORT=",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v failed: %v\n%s", args, err, out)
	}
	return out
}

func TestContextUsesExplicitDBFlagForNoDBCommand(t *testing.T) {
	binPath := buildBDUnderTest(t)
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	writeServerRepo(t, repoA, "repo_a_db", "10.0.0.1", "origin-a", 3311)
	beadsDirB := writeServerRepo(t, repoB, "repo_b_db", "10.0.0.2", "origin-b", 3312)

	out := runBDCommand(t, binPath, repoA, nil, "--db", filepath.Join(beadsDirB, "dolt"), "context", "--json")

	var got map[string]any
	decodeJSONOutput(t, out, &got)
	if evalPath(t, got["beads_dir"].(string)) != evalPath(t, beadsDirB) {
		t.Fatalf("beads_dir = %v, want %s", got["beads_dir"], beadsDirB)
	}
	if got["database"] != "repo_b_db" {
		t.Fatalf("database = %v, want repo_b_db", got["database"])
	}
	if got["server_host"] != "10.0.0.2" {
		t.Fatalf("server_host = %v, want 10.0.0.2", got["server_host"])
	}
	if got["sync_git_remote"] != "origin-b" {
		t.Fatalf("sync_git_remote = %v, want origin-b", got["sync_git_remote"])
	}
}

func TestDoltShowUsesExplicitDBFlagForNoDBCommand(t *testing.T) {
	binPath := buildBDUnderTest(t)
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	writeServerRepo(t, repoA, "repo_a_db", "10.0.0.1", "origin-a", 3311)
	beadsDirB := writeServerRepo(t, repoB, "repo_b_db", "10.0.0.2", "origin-b", 3312)

	out := runBDCommand(t, binPath, repoA, nil, "--db", filepath.Join(beadsDirB, "dolt"), "dolt", "show", "--json")

	var got map[string]any
	decodeJSONOutput(t, out, &got)
	if got["database"] != "repo_b_db" {
		t.Fatalf("database = %v, want repo_b_db", got["database"])
	}
	if got["host"] != "10.0.0.2" {
		t.Fatalf("host = %v, want 10.0.0.2", got["host"])
	}
	if got["port"] != float64(3312) {
		t.Fatalf("port = %v, want 3312", got["port"])
	}
}

func TestContextUsesBEADSDBForNoDBCommand(t *testing.T) {
	binPath := buildBDUnderTest(t)
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	writeServerRepo(t, repoA, "repo_a_db", "10.0.0.1", "origin-a", 3311)
	beadsDirB := writeServerRepo(t, repoB, "repo_b_db", "10.0.0.2", "origin-b", 3312)

	out := runBDCommand(t, binPath, repoA, []string{"BEADS_DB=" + filepath.Join(beadsDirB, "dolt")}, "context", "--json")

	var got map[string]any
	decodeJSONOutput(t, out, &got)
	if evalPath(t, got["beads_dir"].(string)) != evalPath(t, beadsDirB) {
		t.Fatalf("beads_dir = %v, want %s", got["beads_dir"], beadsDirB)
	}
	if got["database"] != "repo_b_db" {
		t.Fatalf("database = %v, want repo_b_db", got["database"])
	}
}

func TestContextUsesBEADSDBDirectoryForNoDBCommand(t *testing.T) {
	binPath := buildBDUnderTest(t)
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	writeServerRepo(t, repoA, "repo_a_db", "10.0.0.1", "origin-a", 3311)
	beadsDirB := writeServerRepo(t, repoB, "repo_b_db", "10.0.0.2", "origin-b", 3312)

	out := runBDCommand(t, binPath, repoA, []string{"BEADS_DB=" + beadsDirB}, "context", "--json")

	var got map[string]any
	decodeJSONOutput(t, out, &got)
	if evalPath(t, got["beads_dir"].(string)) != evalPath(t, beadsDirB) {
		t.Fatalf("beads_dir = %v, want %s", got["beads_dir"], beadsDirB)
	}
	if got["database"] != "repo_b_db" {
		t.Fatalf("database = %v, want repo_b_db", got["database"])
	}
}

func TestContextUsesExplicitDBFlagForExternalDoltDataDir(t *testing.T) {
	binPath := buildBDUnderTest(t)
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	writeServerRepo(t, repoA, "repo_a_db", "10.0.0.1", "origin-a", 3311)
	beadsDirB := writeServerRepoWithDataDir(t, repoB, "repo_b_db", "10.0.0.2", "origin-b", 3312, "../external-dolt")

	out := runBDCommand(t, binPath, repoA, nil, "--db", filepath.Join(beadsDirB, "../external-dolt"), "context", "--json")

	var got map[string]any
	decodeJSONOutput(t, out, &got)
	if evalPath(t, got["beads_dir"].(string)) != evalPath(t, beadsDirB) {
		t.Fatalf("beads_dir = %v, want %s", got["beads_dir"], beadsDirB)
	}
	if got["database"] != "repo_b_db" {
		t.Fatalf("database = %v, want repo_b_db", got["database"])
	}
	if got["server_host"] != "10.0.0.2" {
		t.Fatalf("server_host = %v, want 10.0.0.2", got["server_host"])
	}
}

func TestContextExplicitDBFlagOverridesBEADSDBForNoDBCommand(t *testing.T) {
	binPath := buildBDUnderTest(t)
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	repoC := filepath.Join(root, "repo-c")
	writeServerRepo(t, repoA, "repo_a_db", "10.0.0.1", "origin-a", 3311)
	beadsDirB := writeServerRepo(t, repoB, "repo_b_db", "10.0.0.2", "origin-b", 3312)
	beadsDirC := writeServerRepo(t, repoC, "repo_c_db", "10.0.0.3", "origin-c", 3313)

	out := runBDCommand(t, binPath, repoA, []string{"BEADS_DB=" + filepath.Join(beadsDirC, "dolt")}, "--db", filepath.Join(beadsDirB, "dolt"), "context", "--json")

	var got map[string]any
	decodeJSONOutput(t, out, &got)
	if evalPath(t, got["beads_dir"].(string)) != evalPath(t, beadsDirB) {
		t.Fatalf("beads_dir = %v, want %s", got["beads_dir"], beadsDirB)
	}
	if got["database"] != "repo_b_db" {
		t.Fatalf("database = %v, want repo_b_db", got["database"])
	}
}

func TestContextBEADSDBOverridesBDDBForNoDBCommand(t *testing.T) {
	binPath := buildBDUnderTest(t)
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	repoC := filepath.Join(root, "repo-c")
	writeServerRepo(t, repoA, "repo_a_db", "10.0.0.1", "origin-a", 3311)
	beadsDirB := writeServerRepo(t, repoB, "repo_b_db", "10.0.0.2", "origin-b", 3312)
	beadsDirC := writeServerRepo(t, repoC, "repo_c_db", "10.0.0.3", "origin-c", 3313)

	out := runBDCommand(t, binPath, repoA, []string{"BEADS_DB=" + filepath.Join(beadsDirB, "dolt"), "BD_DB=" + filepath.Join(beadsDirC, "dolt")}, "context", "--json")

	var got map[string]any
	decodeJSONOutput(t, out, &got)
	if evalPath(t, got["beads_dir"].(string)) != evalPath(t, beadsDirB) {
		t.Fatalf("beads_dir = %v, want %s", got["beads_dir"], beadsDirB)
	}
	if got["database"] != "repo_b_db" {
		t.Fatalf("database = %v, want repo_b_db", got["database"])
	}
}

func TestContextPreservesSourceDatabaseAcrossRedirectForNoDBCommand(t *testing.T) {
	binPath := buildBDUnderTest(t)
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	writeServerRepo(t, repoA, "repo_a_db", "10.0.0.1", "origin-a", 3311)
	beadsDirB := writeServerRepo(t, repoB, "repo_b_db", "10.0.0.2", "origin-b", 3312)
	sharedBeadsDir := filepath.Join(repoB, "shared-beads")
	if err := os.RemoveAll(beadsDirB); err != nil {
		t.Fatalf("remove source beads dir: %v", err)
	}
	if err := os.MkdirAll(beadsDirB, 0o755); err != nil {
		t.Fatalf("mkdir source beads dir: %v", err)
	}
	writeFile(t, filepath.Join(beadsDirB, "redirect"), []byte("../shared-beads\n"))
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "10.0.0.2",
		DoltDatabase:   "repo_b_db",
	}).Save(beadsDirB); err != nil {
		t.Fatalf("save source metadata: %v", err)
	}
	writeFile(t, filepath.Join(beadsDirB, "dolt-server.port"), []byte("3312"))
	if err := os.MkdirAll(filepath.Join(sharedBeadsDir, "dolt"), 0o755); err != nil {
		t.Fatalf("mkdir shared dolt dir: %v", err)
	}
	if err := (&configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "10.0.0.9",
		DoltDatabase:   "shared_db",
	}).Save(sharedBeadsDir); err != nil {
		t.Fatalf("save shared metadata: %v", err)
	}
	writeFile(t, filepath.Join(sharedBeadsDir, "dolt-server.port"), []byte("3399"))
	writeFile(t, filepath.Join(sharedBeadsDir, "config.yaml"), []byte("sync:\n  git-remote: shared-origin\n"))
	writeFile(t, filepath.Join(sharedBeadsDir, ".env"), []byte("BEADS_DOLT_SERVER_HOST=10.0.0.9\n"))

	out := runBDCommand(t, binPath, repoA, nil, "--db", filepath.Join(beadsDirB, "dolt"), "context", "--json")

	var got map[string]any
	decodeJSONOutput(t, out, &got)
	if got["database"] != "repo_b_db" {
		t.Fatalf("database = %v, want repo_b_db", got["database"])
	}
}

func TestDoltShowUsesSelectedRepoConfigForNoDBCommand(t *testing.T) {
	binPath := buildBDUnderTest(t)
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	beadsDirA := writeServerRepo(t, repoA, "repo_a_db", "10.0.0.1", "origin-a", 3311)
	beadsDirB := writeServerRepo(t, repoB, "repo_b_db", "10.0.0.2", "origin-b", 3312)
	if err := os.Remove(filepath.Join(beadsDirA, "dolt-server.port")); err != nil {
		t.Fatalf("remove port file A: %v", err)
	}
	if err := os.Remove(filepath.Join(beadsDirB, "dolt-server.port")); err != nil {
		t.Fatalf("remove port file B: %v", err)
	}
	writeProjectConfig(t, beadsDirA, "origin-a", 4401, true)
	writeProjectConfig(t, beadsDirB, "origin-b", 4402, false)

	out := runBDCommand(t, binPath, repoA, nil, "--db", filepath.Join(beadsDirB, "dolt"), "dolt", "show", "--json")

	var got map[string]any
	decodeJSONOutput(t, out, &got)
	if got["host"] != "10.0.0.2" {
		t.Fatalf("host = %v, want 10.0.0.2", got["host"])
	}
	if got["port"] != float64(4402) {
		t.Fatalf("port = %v, want 4402", got["port"])
	}
	if got["shared_server"] != false {
		t.Fatalf("shared_server = %v, want false", got["shared_server"])
	}
	if got["database"] != "repo_b_db" {
		t.Fatalf("database = %v, want repo_b_db", got["database"])
	}
}

func TestContextPreservesShellEnvPrecedenceForNoDBCommand(t *testing.T) {
	binPath := buildBDUnderTest(t)
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	writeServerRepo(t, repoA, "repo_a_db", "10.0.0.1", "origin-a", 3311)
	beadsDirB := writeServerRepo(t, repoB, "repo_b_db", "10.0.0.2", "origin-b", 3312)

	out := runBDCommand(t, binPath, repoA, []string{"BEADS_DOLT_SERVER_HOST=9.9.9.9"}, "--db", filepath.Join(beadsDirB, "dolt"), "context", "--json")

	var got map[string]any
	decodeJSONOutput(t, out, &got)
	if got["server_host"] != "9.9.9.9" {
		t.Fatalf("server_host = %v, want 9.9.9.9", got["server_host"])
	}
	if got["database"] != "repo_b_db" {
		t.Fatalf("database = %v, want repo_b_db", got["database"])
	}
}

func TestDoltShowUsesBEADSDBForNoDBCommand(t *testing.T) {
	binPath := buildBDUnderTest(t)
	root := t.TempDir()
	repoA := filepath.Join(root, "repo-a")
	repoB := filepath.Join(root, "repo-b")
	writeServerRepo(t, repoA, "repo_a_db", "10.0.0.1", "origin-a", 3311)
	beadsDirB := writeServerRepo(t, repoB, "repo_b_db", "10.0.0.2", "origin-b", 3312)

	out := runBDCommand(t, binPath, repoA, []string{"BEADS_DB=" + filepath.Join(beadsDirB, "dolt")}, "dolt", "show", "--json")

	var got map[string]any
	decodeJSONOutput(t, out, &got)
	if got["database"] != "repo_b_db" {
		t.Fatalf("database = %v, want repo_b_db", got["database"])
	}
	if got["host"] != "10.0.0.2" {
		t.Fatalf("host = %v, want 10.0.0.2", got["host"])
	}
}
