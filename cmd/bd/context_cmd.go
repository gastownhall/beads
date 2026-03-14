package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/gastownhall/beads/internal/beads"
	"github.com/gastownhall/beads/internal/config"
	"github.com/gastownhall/beads/internal/configfile"
)

// ContextInfo contains the effective backend identity and repository context.
type ContextInfo struct {
	BeadsDir      string `json:"beads_dir"`
	RepoRoot      string `json:"repo_root"`
	CWDRepoRoot   string `json:"cwd_repo_root,omitempty"`
	IsRedirected  bool   `json:"is_redirected"`
	IsWorktree    bool   `json:"is_worktree"`
	Backend       string `json:"backend"`
	DoltMode      string `json:"dolt_mode"`
	ServerHost    string `json:"server_host,omitempty"`
	ServerPort    int    `json:"server_port,omitempty"`
	Database      string `json:"database"`
	DataDir       string `json:"data_dir,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	SyncGitRemote string `json:"sync_git_remote,omitempty"`
	Role          string `json:"role,omitempty"`
	BdVersion     string `json:"bd_version"`
}

var contextCmd = &cobra.Command{
	Use:     "context",
	GroupID: "setup",
	Short:   "Show effective backend identity and repository context",
	Long: `Show the effective backend identity information including repository paths,
backend configuration, and sync settings.

This command reads directly from config files and does not require the
database to be open, making it useful for diagnostics in degraded states.

Examples:
  bd context           # Show context information
  bd context --json    # Output in JSON format
`,
	Run: func(cmd *cobra.Command, args []string) {
		info := ContextInfo{
			Backend:   configfile.BackendDolt,
			BdVersion: Version,
		}

		// Resolve repo context (works without DB open)
		rc, err := beads.GetRepoContext()
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": fmt.Sprintf("cannot resolve repo context: %v", err)})
			} else {
				fmt.Fprintf(os.Stderr, "Error: cannot resolve repo context: %v\n", err)
			}
			os.Exit(1)
		}

		info.BeadsDir = rc.BeadsDir
		info.RepoRoot = rc.RepoRoot
		info.CWDRepoRoot = rc.CWDRepoRoot
		info.IsRedirected = rc.IsRedirected
		info.IsWorktree = rc.IsWorktree

		// Read role from repo context
		if role, ok := rc.Role(); ok {
			info.Role = string(role)
		}

		// Load metadata.json config (does not require DB)
		cfg, err := configfile.Load(rc.BeadsDir)
		if err != nil {
			cfg = configfile.DefaultConfig()
		}
		if cfg == nil {
			cfg = configfile.DefaultConfig()
		}

		info.DoltMode = cfg.GetDoltMode()
		info.Database = cfg.GetDoltDatabase()
		info.ProjectID = cfg.ProjectID

		if cfg.IsDoltServerMode() {
			info.ServerHost = cfg.GetDoltServerHost()
			info.ServerPort = cfg.GetDoltServerPort()
		}

		if dataDir := cfg.GetDoltDataDir(); dataDir != "" {
			info.DataDir = dataDir
		}

		// Read sync.git-remote from config.yaml
		if remote := config.GetString("sync.git-remote"); remote != "" {
			info.SyncGitRemote = remote
		}

		if jsonOutput {
			outputJSON(info)
		} else {
			printContextText(info)
		}
	},
}

func printContextText(info ContextInfo) {
	fmt.Printf("bd version:     %s\n", info.BdVersion)
	fmt.Println()

	// Repository
	fmt.Println("Repository:")
	fmt.Printf("  beads dir:    %s\n", info.BeadsDir)
	fmt.Printf("  repo root:    %s\n", info.RepoRoot)
	if info.CWDRepoRoot != "" && info.CWDRepoRoot != info.RepoRoot {
		fmt.Printf("  cwd repo:     %s\n", info.CWDRepoRoot)
	}
	if info.IsRedirected {
		fmt.Printf("  redirected:   yes\n")
	}
	if info.IsWorktree {
		fmt.Printf("  worktree:     yes\n")
	}
	if info.Role != "" {
		fmt.Printf("  role:         %s\n", info.Role)
	}
	fmt.Println()

	// Backend
	fmt.Println("Backend:")
	fmt.Printf("  type:         %s\n", info.Backend)
	fmt.Printf("  mode:         %s\n", info.DoltMode)
	fmt.Printf("  database:     %s\n", info.Database)
	if info.ServerHost != "" {
		fmt.Printf("  server:       %s:%d\n", info.ServerHost, info.ServerPort)
	}
	if info.DataDir != "" {
		fmt.Printf("  data dir:     %s\n", info.DataDir)
	}
	if info.ProjectID != "" {
		fmt.Printf("  project id:   %s\n", info.ProjectID)
	}

	// Sync
	if info.SyncGitRemote != "" {
		fmt.Println()
		fmt.Println("Sync:")
		fmt.Printf("  git remote:   %s\n", info.SyncGitRemote)
	}
}

func init() {
	rootCmd.AddCommand(contextCmd)
	readOnlyCommands["context"] = true
}
