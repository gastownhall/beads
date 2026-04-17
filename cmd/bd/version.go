package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
)

var (
	// Version is the current version of bd (overridden by ldflags at build time)
	Version = "1.0.2"
	// Build can be set via ldflags at compile time
	Build = "dev"
	// Commit and branch the git revision the binary was built from (optional ldflag)
	Commit = ""
	Branch = ""
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		commit := resolveCommitHash()
		branch := resolveBranch()

		if jsonOutput {
			result := map[string]interface{}{
				"version": Version,
				"build":   Build,
			}
			if commit != "" {
				result["commit"] = commit
			}
			if branch != "" {
				result["branch"] = branch
			}
			outputJSON(result)
		} else {
			if commit != "" && branch != "" {
				fmt.Printf("bd version %s (%s: %s@%s)\n", Version, Build, branch, shortCommit(commit))
			} else if commit != "" {
				fmt.Printf("bd version %s (%s: %s)\n", Version, Build, shortCommit(commit))
			} else {
				fmt.Printf("bd version %s (%s)\n", Version, Build)
			}
		}

		// Check for multiple bd binaries in PATH
		if dups := findDuplicateBinaries(); len(dups) > 1 {
			fmt.Fprintf(os.Stderr, "\nWarning: multiple 'bd' binaries found in PATH:\n")
			for _, p := range dups {
				fmt.Fprintf(os.Stderr, "  %s\n", p)
			}
			fmt.Fprintf(os.Stderr, "The first one is being used. Remove duplicates to avoid confusion.\n")
		}
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

func resolveCommitHash() string {
	if Commit != "" {
		return Commit
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value
			}
		}
	}

	return ""
}

func shortCommit(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func resolveBranch() string {
	if Branch != "" {
		return Branch
	}

	// Try to get branch from build info (build-time VCS detection)
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.branch" && setting.Value != "" {
				return setting.Value
			}
		}
	}

	// Fallback: try to get branch from git at runtime
	// Use symbolic-ref to work in fresh repos without commits
	// Uses CWD repo context since this shows user's current branch
	if rc, err := beads.GetRepoContext(); err == nil {
		cmd := rc.GitCmdCWD(context.Background(), "symbolic-ref", "--short", "HEAD")
		if output, err := cmd.Output(); err == nil {
			if branch := strings.TrimSpace(string(output)); branch != "" && branch != "HEAD" {
				return branch
			}
		}
	}

	return ""
}

// findDuplicateBinaries searches PATH for all "bd" executables.
// Returns their full paths. If len > 1, there are duplicates.
func findDuplicateBinaries() []string {
	name := "bd"
	if runtime.GOOS == "windows" {
		name = "bd.exe"
	}

	seen := make(map[string]bool)
	var paths []string

	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		candidate := filepath.Join(dir, name)
		// Resolve symlinks so we don't double-count
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			// Try the raw path (might be a valid binary without symlinks)
			resolved = candidate
		}
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		if !seen[resolved] {
			seen[resolved] = true
			paths = append(paths, candidate)
		}
	}
	return paths
}
