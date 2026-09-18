package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/syncauth"
)

var (
	githubSyncAuthProvider string
	githubSyncAuthHost     string
	githubSyncAuthDryRun   bool
)

var githubSyncAuthCmd = &cobra.Command{
	Use:     "github-sync",
	GroupID: "sync",
	Short:   "Manage secure GitHub/GitLab authentication for Dolt sync",
	Long: `Manage secure authentication when syncing beads Dolt data with
GitHub or GitLab remotes.

bd prefers the official gh and glab CLIs because they store credentials in
the OS keyring. If neither CLI is available, bd can perform an OAuth device
flow and store the token in the OS keyring itself.

For self-managed hosts (GitHub Enterprise or self-managed GitLab) pass
--host; gh and glab handle the host natively. The OAuth device flow targets
github.com and GitLab-compatible endpoints, so for GitHub Enterprise use
'--provider gh'.

Run 'bd github-sync status' to see which authentication methods are available
and 'bd github-sync login --provider gh' (or glab/oauth) to authenticate.`,
	RunE: runGitHubSyncStatus,
}

var githubSyncLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in to a GitHub or GitLab host",
	Long: `Log in to the configured host using the chosen provider.

For gh/glab, this invokes the CLI's interactive login. For OAuth, this runs
an OAuth device flow and prints a URL and user code.`,
	RunE: runGitHubSyncLogin,
}

var githubSyncLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out from a GitHub or GitLab host",
	Long:  `Log out from the configured host using the chosen provider.`,
	RunE:  runGitHubSyncLogout,
}

var githubSyncStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show authentication status for a host",
	RunE:  runGitHubSyncStatus,
}

var gitCredentialCmd = &cobra.Command{
	Use:    "git-credential [operation]",
	Short:  "git credential helper for bd OAuth tokens",
	Hidden: true,
	Args:   cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		op := ""
		if len(args) > 0 {
			op = args[0]
		}
		host, _ := cmd.Flags().GetString("host")
		return syncauth.RunGitCredential(cmd.Context(), op, host)
	},
}

func init() {
	for _, c := range []*cobra.Command{githubSyncAuthCmd, githubSyncLoginCmd, githubSyncLogoutCmd, githubSyncStatusCmd} {
		c.Flags().StringVar(&githubSyncAuthProvider, "provider", "auto", "Authentication provider: gh, glab, oauth, or auto")
		c.Flags().StringVar(&githubSyncAuthHost, "host", "", "Git host (default: inferred from remote, or github.com for login)")
		c.Flags().BoolVar(&githubSyncAuthDryRun, "dry-run", false, "Show what would happen without making changes")
	}

	gitCredentialCmd.Flags().String("host", "", "host this helper was configured for; other hosts are not served")

	githubSyncAuthCmd.AddCommand(githubSyncLoginCmd)
	githubSyncAuthCmd.AddCommand(githubSyncLogoutCmd)
	githubSyncAuthCmd.AddCommand(githubSyncStatusCmd)
	githubSyncAuthCmd.AddCommand(gitCredentialCmd)

	rootCmd.AddCommand(githubSyncAuthCmd)
}

func runGitHubSyncStatus(cmd *cobra.Command, args []string) error {
	cfg, err := githubSyncConfig(cmd)
	if err != nil {
		return HandleError("%v", err)
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// A nil Auth means auto found no provider; status reports unauthenticated
	// rather than exiting with an error.
	a, err := resolveSyncAuth(ctx, cfg)
	if err != nil {
		return HandleError("%v", err)
	}

	ok := false
	if a != nil {
		ok, err = a.Detect(ctx)
		if err != nil {
			return HandleError("%v", err)
		}
	}

	if jsonOutput {
		return outputJSONRaw(map[string]any{
			"host":     cfg.Host,
			"provider": cfg.Provider.String(),
			"ready":    ok,
		})
	}

	status := "not authenticated"
	if ok {
		status = "authenticated"
	}
	fmt.Printf("Host: %s\nProvider: %s\nStatus: %s\n", cfg.Host, cfg.Provider, status)

	if !ok {
		fmt.Fprintf(os.Stderr, "\nTo authenticate, run one of:\n")
		fmt.Fprintf(os.Stderr, "  bd github-sync login --provider gh --host %s\n", cfg.Host)
		fmt.Fprintf(os.Stderr, "  bd github-sync login --provider glab --host %s\n", cfg.Host)
		fmt.Fprintf(os.Stderr, "  bd github-sync login --provider oauth --host %s\n", cfg.Host)
	}
	return nil
}

func runGitHubSyncLogin(cmd *cobra.Command, args []string) error {
	cfg, err := githubSyncConfig(cmd)
	if err != nil {
		return HandleError("%v", err)
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	switch cfg.Provider {
	case syncauth.ProviderAuto:
		// Default to gh for GitHub, glab for GitLab. For unknown hosts
		// (self-managed GitHub Enterprise or GitLab — indistinguishable by
		// name) prefer whichever CLI is installed, else OAuth.
		if syncauth.IsGitHubHost(cfg.Host) {
			cfg.Provider = syncauth.ProviderGH
		} else if syncauth.IsGitLabHost(cfg.Host) {
			cfg.Provider = syncauth.ProviderGLab
		} else if _, err := exec.LookPath("gh"); err == nil {
			cfg.Provider = syncauth.ProviderGH
		} else if _, err := exec.LookPath("glab"); err == nil {
			cfg.Provider = syncauth.ProviderGLab
		} else {
			cfg.Provider = syncauth.ProviderOAuth
		}
	}

	if githubSyncAuthDryRun {
		fmt.Printf("Would log in to %s using %s\n", cfg.Host, cfg.Provider)
		return nil
	}

	switch cfg.Provider {
	case syncauth.ProviderGH:
		return runCLIAuthLogin("gh", cfg.Host)
	case syncauth.ProviderGLab:
		return runCLIAuthLogin("glab", cfg.Host)
	case syncauth.ProviderOAuth:
		return runOAuthLogin(ctx, cfg)
	default:
		return HandleError("unsupported login provider: %s", cfg.Provider)
	}
}

func runGitHubSyncLogout(cmd *cobra.Command, args []string) error {
	cfg, err := githubSyncConfig(cmd)
	if err != nil {
		return HandleError("%v", err)
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if githubSyncAuthDryRun {
		fmt.Printf("Would log out from %s using %s\n", cfg.Host, cfg.Provider)
		return nil
	}

	switch cfg.Provider {
	case syncauth.ProviderGH:
		return runCLIAuthLogout("gh", cfg.Host)
	case syncauth.ProviderGLab:
		return runCLIAuthLogout("glab", cfg.Host)
	case syncauth.ProviderOAuth, syncauth.ProviderAuto:
		if err := syncauth.OAuthLogout(ctx, cfg.Host, syncauth.DefaultKeyring()); err != nil {
			return HandleError("%v", err)
		}
		fmt.Printf("Removed OAuth token for %s\n", cfg.Host)
		return nil
	default:
		return HandleError("unsupported logout provider: %s", cfg.Provider)
	}
}

func runCLIAuthLogin(name, host string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return HandleError("%s not found in PATH: %v", name, err)
	}
	args := []string{"auth", "login"}
	if host != "" && host != defaultForCLI(name) {
		args = append(args, "--hostname", host)
	}
	c := exec.Command(path, args...) // #nosec G204 -- validated program name
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return HandleError("%s auth login failed: %v", name, err)
	}
	fmt.Printf("Logged in with %s for %s\n", name, host)
	return nil
}

func runCLIAuthLogout(name, host string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return HandleError("%s not found in PATH: %v", name, err)
	}
	args := []string{"auth", "logout"}
	if host != "" && host != defaultForCLI(name) {
		args = append(args, "--hostname", host)
	}
	c := exec.Command(path, args...) // #nosec G204 -- validated program name
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return HandleError("%s auth logout failed: %v", name, err)
	}
	fmt.Printf("Logged out from %s for %s\n", name, host)
	return nil
}

func defaultForCLI(name string) string {
	if name == "gh" {
		return "github.com"
	}
	return "gitlab.com"
}

func runOAuthLogin(ctx context.Context, cfg syncauth.Config) error {
	if cfg.ClientID == "" {
		return HandleError("OAuth requires github.client_id or gitlab.client_id to be set in config or BD_GITHUB_CLIENT_ID / BD_GITLAB_CLIENT_ID environment variables")
	}
	tok, err := syncauth.OAuthLogin(ctx, cfg, func(code, uri string) error {
		fmt.Printf("Please authenticate at %s\n", uri)
		fmt.Printf("Enter this user code if prompted: %s\n", code)
		fmt.Println("Waiting for authorization...")
		return nil
	})
	if err != nil {
		return HandleError("OAuth login failed: %v", err)
	}
	fmt.Printf("Authenticated with OAuth for %s (expires %s)\n", cfg.Host, tok.Expiry.Format("2006-01-02 15:04:05 MST"))
	return nil
}

func githubSyncConfig(cmd *cobra.Command) (syncauth.Config, error) {
	provider := syncauth.Provider(githubSyncAuthProvider)
	if !syncauth.IsValidProvider(provider) {
		return syncauth.Config{}, fmt.Errorf("invalid provider %q", githubSyncAuthProvider)
	}

	host := githubSyncAuthHost
	if host == "" {
		// Try to infer the host from the Dolt remote.
		host = inferRemoteHost(cmd.Context())
	}
	if host == "" {
		host = "github.com"
	}
	host = syncauth.NormalizeHost(host)

	cfg := syncauth.Config{
		Provider:     provider,
		Host:         host,
		ClientID:     clientIDForHost(host),
		ClientSecret: clientSecretForHost(host),
		Exe:          syncauth.CurrentExecutable(),
	}

	if provider == syncauth.ProviderOAuth {
		cfg.Scopes = oauthScopesForHost(host)
	}

	return cfg, nil
}

func inferRemoteHost(ctx context.Context) string {
	st := getStore()
	if st == nil {
		return ""
	}
	remotes, err := st.ListRemotes(ctx)
	if err != nil || len(remotes) == 0 {
		return ""
	}
	if syncauth.CredentialScheme(remotes[0].URL) == "" {
		return ""
	}
	host, err := syncauth.HostFromRemoteURL(remotes[0].URL)
	if err != nil {
		return ""
	}
	return host
}

func clientIDForHost(host string) string {
	host = syncauth.NormalizeHost(host)
	if syncauth.IsGitHubHost(host) {
		if v := os.Getenv("BD_GITHUB_CLIENT_ID"); v != "" {
			return v
		}
		return config.GetString("github.client_id")
	}
	if v := os.Getenv("BD_GITLAB_CLIENT_ID"); v != "" {
		return v
	}
	return config.GetString("gitlab.client_id")
}

// clientSecretForHost reads the OAuth client secret from the environment only.
// Secrets are deliberately not read from beads config — putting them there is
// the pattern this feature exists to remove. A configured-but-ignored
// client_secret key earns a warning rather than silently rotting in
// config.yaml.
func clientSecretForHost(host string) string {
	key, env := "gitlab.client_secret", "BD_GITLAB_CLIENT_SECRET"
	if syncauth.IsGitHubHost(syncauth.NormalizeHost(host)) {
		key, env = "github.client_secret", "BD_GITHUB_CLIENT_SECRET"
	}
	if config.GetString(key) != "" {
		fmt.Fprintf(os.Stderr, "warning: %s in config.yaml is ignored; set %s in the environment instead\n", key, env)
	}
	return os.Getenv(env)
}

func oauthScopesForHost(host string) []string {
	if syncauth.IsGitHubHost(host) {
		return []string{"repo"}
	}
	return []string{"read_repository", "write_repository"}
}

func resolveSyncAuth(ctx context.Context, cfg syncauth.Config) (syncauth.Auth, error) {
	if cfg.Provider == syncauth.ProviderAuto {
		return syncauth.ResolveAuto(ctx, cfg.Host, cfg, nil)
	}
	return syncauth.New(cfg)
}
