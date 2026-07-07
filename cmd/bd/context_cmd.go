package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/metrics"
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
	ProxiedDir    string `json:"proxied_dir,omitempty"`
	Database      string `json:"database"`
	DataDir       string `json:"data_dir,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	SyncRemote    string `json:"sync_remote,omitempty"`
	SyncGitRemote string `json:"sync_git_remote,omitempty"` // Deprecated: use sync_remote
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("context")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runContextProxiedServer(cmd, rootCtx)
		}

		info := ContextInfo{
			Backend:   configfile.BackendDolt,
			BdVersion: Version,
		}

		if selected := selectedNoDBBeadsDir(cmd); selected != "" {
			prepareSelectedNoDBContext(selected)
		}

		rc, err := beads.GetRepoContext()
		if err != nil {
			if jsonOutput {
				if jerr := outputJSON(map[string]string{"error": fmt.Sprintf("cannot resolve repo context: %v", err)}); jerr != nil {
					return jerr
				}
				return SilentExit()
			}
			return HandleError("cannot resolve repo context: %v", err)
		}

		info.BeadsDir = rc.BeadsDir
		info.RepoRoot = rc.RepoRoot
		info.CWDRepoRoot = rc.CWDRepoRoot
		info.IsRedirected = rc.IsRedirected
		info.IsWorktree = rc.IsWorktree

		if role, ok := rc.Role(); ok {
			info.Role = string(role)
		}

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
			dsCfg := doltserver.DefaultConfig(rc.BeadsDir)
			info.ServerPort = dsCfg.Port
		}
		if cfg.IsDoltProxiedServerMode() {
			p, err := resolveProxiedServerRootPath(rc.BeadsDir)
			if err != nil {
				return HandleError("resolve proxied server root: %v", err)
			}
			info.ProxiedDir = p
		}

		if dataDir := cfg.GetDoltDataDir(); dataDir != "" {
			info.DataDir = dataDir
		}

		if remote := resolveSyncRemoteFromDir(rc.BeadsDir); remote != "" {
			info.SyncRemote = remote
			info.SyncGitRemote = remote
		}

		if jsonOutput {
			return outputJSON(info)
		}
		printContextText(info)
		return nil
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
	if info.ProxiedDir != "" {
		fmt.Printf("  proxied dir:  %s\n", info.ProxiedDir)
	}
	if info.DataDir != "" {
		fmt.Printf("  data dir:     %s\n", info.DataDir)
	}
	if info.ProjectID != "" {
		fmt.Printf("  project id:   %s\n", info.ProjectID)
	}

	// Sync
	if info.SyncRemote != "" {
		fmt.Println()
		fmt.Println("Sync:")
		fmt.Printf("  remote:       %s\n", info.SyncRemote)
	}
}

func init() {
	rootCmd.AddCommand(contextCmd)
	readOnlyCommands["context"] = true
}
