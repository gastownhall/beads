package doctor

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"gopkg.in/yaml.v3"
)

// findJSONLFileWithSyncWorktree returns the effective JSONL path for doctor checks.
//
// In sync-branch mode, JSONL writes happen in the sync worktree
// (.git/beads-worktrees/<branch>/.beads/issues.jsonl) while main's .beads/issues.jsonl
// may lag behind until branch merge. For DB-vs-JSONL checks we should compare against
// the worktree JSONL when available.
func findJSONLFileWithSyncWorktree(repoPath, beadsDir string) string {
	mainJSONL := findJSONLFile(beadsDir)
	if mainJSONL == "" {
		return ""
	}

	worktreeJSONL := resolveSyncWorktreeJSONLPath(repoPath, beadsDir, mainJSONL)
	if worktreeJSONL != "" {
		return worktreeJSONL
	}

	return mainJSONL
}

func resolveSyncWorktreeJSONLPath(repoPath, beadsDir, mainJSONLPath string) string {
	syncBranch := getConfiguredSyncBranch(beadsDir)
	if syncBranch == "" {
		return ""
	}

	repoRoot, gitCommonDir := getGitPaths(repoPath)
	if repoRoot == "" || gitCommonDir == "" {
		return ""
	}

	relPath, err := filepath.Rel(repoRoot, mainJSONLPath)
	if err != nil {
		return ""
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return ""
	}

	worktreeRoot := filepath.Join(gitCommonDir, "beads-worktrees", syncBranch)
	worktreeJSONL := filepath.Join(worktreeRoot, relPath)
	if _, err := os.Stat(worktreeJSONL); err != nil {
		return ""
	}
	return worktreeJSONL
}

func getGitPaths(repoPath string) (repoRoot, gitCommonDir string) {
	rootCmd := exec.Command("git", "rev-parse", "--show-toplevel")
	rootCmd.Dir = repoPath
	rootOut, err := rootCmd.Output()
	if err != nil {
		return "", ""
	}
	repoRoot = strings.TrimSpace(string(rootOut))
	if repoRoot == "" {
		return "", ""
	}

	commonCmd := exec.Command("git", "rev-parse", "--git-common-dir")
	commonCmd.Dir = repoPath
	commonOut, err := commonCmd.Output()
	if err != nil {
		return "", ""
	}
	gitCommonDir = strings.TrimSpace(string(commonOut))
	if gitCommonDir == "" {
		return "", ""
	}
	if !filepath.IsAbs(gitCommonDir) {
		gitCommonDir = filepath.Join(repoRoot, gitCommonDir)
	}

	return repoRoot, gitCommonDir
}

type syncBranchYAML struct {
	SyncBranch string `yaml:"sync-branch"`
	Sync       struct {
		Branch string `yaml:"branch"`
	} `yaml:"sync"`
}

func getConfiguredSyncBranch(beadsDir string) string {
	if envBranch := strings.TrimSpace(os.Getenv("BEADS_SYNC_BRANCH")); envBranch != "" {
		return envBranch
	}

	if yamlBranch := getSyncBranchFromYAML(beadsDir); yamlBranch != "" {
		return yamlBranch
	}

	return getSyncBranchFromDB(beadsDir)
}

func getSyncBranchFromYAML(beadsDir string) string {
	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		return ""
	}

	var cfg syncBranchYAML
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}

	if b := strings.TrimSpace(cfg.SyncBranch); b != "" {
		return b
	}
	if b := strings.TrimSpace(cfg.Sync.Branch); b != "" {
		return b
	}
	return ""
}

func getSyncBranchFromDB(beadsDir string) string {
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	}

	if _, err := os.Stat(dbPath); err != nil {
		return ""
	}

	db, err := sql.Open("sqlite3", sqliteConnString(dbPath, true))
	if err != nil {
		return ""
	}
	defer db.Close()

	var branch string
	err = db.QueryRow("SELECT value FROM config WHERE key = 'sync.branch'").Scan(&branch)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(branch)
}
