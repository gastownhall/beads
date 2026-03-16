package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
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
		info, err := loadContextInfo()
		if err != nil {
			if jsonOutput {
				outputJSON(map[string]string{"error": fmt.Sprintf("cannot resolve repo context: %v", err)})
			} else {
				fmt.Fprintf(os.Stderr, "Error: cannot resolve repo context: %v\n", err)
			}
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(info)
		} else {
			printContextText(info)
		}
	},
}

func loadContextInfo() (ContextInfo, error) {
	info := ContextInfo{
		Backend:   configfile.BackendDolt,
		BdVersion: Version,
	}

	rc, err := beads.GetRepoContext()
	if err != nil {
		return info, err
	}

	info.BeadsDir = rc.BeadsDir
	info.RepoRoot = rc.RepoRoot
	info.CWDRepoRoot = rc.CWDRepoRoot
	info.IsRedirected = rc.IsRedirected
	info.IsWorktree = rc.IsWorktree

	if role, ok := rc.Role(); ok {
		info.Role = string(role)
	}

	if runtimeInfo, runtimeErr := loadCurrentRepoRuntime(); runtimeErr == nil && runtimeInfo != nil {
		info.Backend = runtimeInfo.Runtime.Backend
		info.DoltMode = runtimeInfo.Runtime.DoltMode
		info.Database = runtimeInfo.Runtime.Database
		info.ProjectID = runtimeInfo.Config.ProjectID
		if runtimeInfo.Runtime.ServerMode {
			info.ServerHost = runtimeInfo.Runtime.Host
			info.ServerPort = runtimeInfo.Runtime.Port
		}
		if dataDir := runtimeInfo.Runtime.DoltDataDir; dataDir != "" {
			info.DataDir = dataDir
		}
	} else {
		cfg, cfgErr := configfile.Load(rc.BeadsDir)
		if cfgErr != nil {
			cfg = configfile.DefaultConfig()
		}
		if cfg == nil {
			cfg = configfile.DefaultConfig()
		}
		info.DoltMode = cfg.GetDoltMode()
		info.Database = cfg.GetDoltDatabase()
		info.ProjectID = cfg.ProjectID
	}

	if remote := config.GetString("sync.git-remote"); remote != "" {
		info.SyncGitRemote = remote
	}

	return info, nil
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
