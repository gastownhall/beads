//go:build cgo

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/ui"
	"golang.org/x/term"
)

var (
	federationPeer     string
	federationStrategy string
	federationUser     string
	federationPassword string
	federationSov      string
)

var federationCmd = &cobra.Command{
	Use:     "federation",
	GroupID: "sync",
	Short:   "Manage peer-to-peer federation with other workspaces",
	Long: `Manage peer-to-peer federation between Dolt-backed beads databases.

Federation enables synchronized issue tracking across multiple workspaces,
each maintaining their own Dolt database while sharing updates via remotes.

Requires the Dolt storage backend.`,
}

var federationSyncCmd = &cobra.Command{
	Use:   "sync [--peer name]",
	Short: "Synchronize with a peer town",
	Long: `Pull from and push to peer towns.

Without --peer, syncs with all configured peers.
With --peer, syncs only with the specified peer.

Handles merge conflicts using the configured strategy:
  --strategy ours    Keep local changes on conflict
  --strategy theirs  Accept remote changes on conflict

If no strategy is specified and conflicts occur, the sync will pause
and report which tables have conflicts for manual resolution.

Examples:
  bd federation sync                      # Sync with all peers
  bd federation sync --peer town-beta     # Sync with specific peer
  bd federation sync --strategy theirs    # Auto-resolve using remote values`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runFederationSync,
}

var federationStatusCmd = &cobra.Command{
	Use:   "status [--peer name]",
	Short: "Show federation sync status",
	Long: `Show synchronization status with peer towns.

Displays:
  - Configured peers and their URLs
  - Commits ahead/behind each peer
  - Whether there are unresolved conflicts

Examples:
  bd federation status                    # Status for all peers
  bd federation status --peer town-beta   # Status for specific peer`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runFederationStatus,
}

var federationAddPeerCmd = &cobra.Command{
	Use:   "add-peer <name> <url>",
	Short: "Add a federation peer with optional SQL credentials",
	Long: `Add a new federation peer remote with optional SQL user authentication.

The URL can be:
  - dolthub://org/repo      DoltHub hosted repository
  - host:port/database      Direct dolt sql-server connection
  - file:///path/to/repo    Local file path (for testing)

Credentials are encrypted and stored locally. They are used automatically
when syncing with the peer. If --user is provided without --password,
you will be prompted for the password interactively.

Examples:
  bd federation add-peer town-beta dolthub://acme/town-beta-beads
  bd federation add-peer town-gamma 192.168.1.100:3306/beads --user sync-bot
  bd federation add-peer partner https://partner.example.com/beads --user admin --password secret`,
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runFederationAddPeer,
}

var federationRemovePeerCmd = &cobra.Command{
	Use:           "remove-peer <name>",
	Short:         "Remove a federation peer",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runFederationRemovePeer,
}

var federationListPeersCmd = &cobra.Command{
	Use:           "list-peers",
	Short:         "List configured federation peers",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runFederationListPeers,
}

func init() {
	// Add subcommands
	federationCmd.AddCommand(federationSyncCmd)
	federationCmd.AddCommand(federationStatusCmd)
	federationCmd.AddCommand(federationAddPeerCmd)
	federationCmd.AddCommand(federationRemovePeerCmd)
	federationCmd.AddCommand(federationListPeersCmd)

	// Flags for sync
	federationSyncCmd.Flags().StringVar(&federationPeer, "peer", "", "Specific peer to sync with")
	federationSyncCmd.Flags().StringVar(&federationStrategy, "strategy", "", "Conflict resolution strategy (ours|theirs)")

	// Flags for status
	federationStatusCmd.Flags().StringVar(&federationPeer, "peer", "", "Specific peer to check")

	// Flags for add-peer (SQL user authentication)
	federationAddPeerCmd.Flags().StringVarP(&federationUser, "user", "u", "", "SQL username for authentication")
	federationAddPeerCmd.Flags().StringVarP(&federationPassword, "password", "p", "", "SQL password (prompted if --user set without --password)")
	federationAddPeerCmd.Flags().StringVar(&federationSov, "sovereignty", "", "Sovereignty tier (T1, T2, T3, T4)")

	rootCmd.AddCommand(federationCmd)
}

func getFederatedStore() (storage.DoltStorage, error) {
	if store == nil {
		return nil, fmt.Errorf("no store available")
	}
	return store, nil
}

func runFederationSync(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("federation sync is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("federation-sync")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	ds, err := getFederatedStore()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if federationStrategy != "" && federationStrategy != "ours" && federationStrategy != "theirs" {
		return HandleErrorRespectJSON("invalid strategy %q: must be 'ours' or 'theirs'", federationStrategy)
	}

	var peers []string
	if federationPeer != "" {
		peers = []string{federationPeer}
	} else {
		remotes, err := ds.ListRemotes(ctx)
		if err != nil {
			return HandleErrorRespectJSON("failed to list peers: %v", err)
		}
		for _, r := range remotes {
			if r.Name != "origin" {
				peers = append(peers, r.Name)
			}
		}
	}

	if len(peers) == 0 {
		return HandleErrorRespectJSON("no federation peers configured (use 'bd federation add-peer' to add peers)")
	}

	// Sync with each peer
	var results []*storage.SyncResult
	for _, peer := range peers {
		if !jsonOutput {
			fmt.Printf("%s Syncing with %s...\n", ui.RenderAccent("🔄"), peer)
		}

		result, err := ds.Sync(ctx, peer, federationStrategy)
		results = append(results, result)

		if err != nil {
			if !jsonOutput {
				fmt.Printf("  %s %v\n", ui.RenderFail("✗"), err)
			}
			continue
		}

		if !jsonOutput {
			if result.Fetched {
				fmt.Printf("  %s Fetched\n", ui.RenderPass("✓"))
			}
			if result.Merged {
				fmt.Printf("  %s Merged", ui.RenderPass("✓"))
				if result.PulledCommits > 0 {
					fmt.Printf(" (%d commits)", result.PulledCommits)
				}
				fmt.Println()
			}
			if len(result.Conflicts) > 0 {
				if result.ConflictsResolved {
					fmt.Printf("  %s Resolved %d conflicts using %s strategy\n",
						ui.RenderPass("✓"), len(result.Conflicts), federationStrategy)
				} else {
					fmt.Printf("  %s %d conflicts need resolution\n",
						ui.RenderWarn("⚠"), len(result.Conflicts))
					for _, c := range result.Conflicts {
						fmt.Printf("    - %s\n", c.Field)
					}
				}
			}
			if result.Pushed {
				fmt.Printf("  %s Pushed\n", ui.RenderPass("✓"))
			} else if result.PushError != nil {
				fmt.Printf("  %s Push skipped: %v\n", ui.RenderMuted("○"), result.PushError)
			}
		}
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"peers":   peers,
			"results": results,
		})
	}
	return nil
}

func runFederationStatus(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("federation status is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("federation-status")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	ds, err := getFederatedStore()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	allRemotes, err := ds.ListRemotes(ctx)
	if err != nil {
		return HandleErrorRespectJSON("failed to list remotes: %v", err)
	}
	remoteURLs := make(map[string]string)
	for _, r := range allRemotes {
		remoteURLs[r.Name] = r.URL
	}

	var peers []string
	if federationPeer != "" {
		peers = []string{federationPeer}
	} else {
		for _, r := range allRemotes {
			peers = append(peers, r.Name)
		}
	}

	if len(peers) == 0 {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"peers":          []string{},
				"pendingChanges": 0,
			})
		}
		fmt.Println("No federation peers configured.")
		return nil
	}

	doltStatus, _ := ds.Status(ctx)
	pendingChanges := 0
	if doltStatus != nil {
		pendingChanges = len(doltStatus.Staged) + len(doltStatus.Unstaged)
	}

	type peerStatus struct {
		Status     *storage.SyncStatus
		URL        string
		Reachable  bool
		ReachError string
	}
	var peerStatuses []peerStatus

	for _, peer := range peers {
		ps := peerStatus{
			URL: remoteURLs[peer],
		}

		status, _ := ds.SyncStatus(ctx, peer)
		ps.Status = status

		fetchErr := ds.Fetch(ctx, peer)
		if fetchErr == nil {
			ps.Reachable = true
			status, _ = ds.SyncStatus(ctx, peer)
			ps.Status = status
		} else {
			ps.ReachError = fetchErr.Error()
		}

		peerStatuses = append(peerStatuses, ps)
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"peers":          peerStatuses,
			"pendingChanges": pendingChanges,
		})
	}

	fmt.Printf("\n%s Federation Status:\n\n", ui.RenderAccent("🌐"))

	if pendingChanges > 0 {
		fmt.Printf("  %s %d pending local changes\n\n", ui.RenderWarn("⚠"), pendingChanges)
	}

	for _, ps := range peerStatuses {
		status := ps.Status
		fmt.Printf("  %s  %s\n", ui.RenderAccent(status.Peer), ui.RenderMuted(ps.URL))

		if ps.Reachable {
			fmt.Printf("    %s Reachable\n", ui.RenderPass("✓"))
		} else {
			fmt.Printf("    %s Unreachable: %s\n", ui.RenderFail("✗"), ps.ReachError)
		}

		if status.LocalAhead >= 0 {
			fmt.Printf("    Ahead:  %d commits\n", status.LocalAhead)
			fmt.Printf("    Behind: %d commits\n", status.LocalBehind)
		} else {
			fmt.Printf("    Sync:   %s\n", ui.RenderMuted("not fetched yet"))
		}

		if !status.LastSync.IsZero() {
			fmt.Printf("    Last sync: %s\n", status.LastSync.Format("2006-01-02 15:04:05"))
		}

		if status.HasConflicts {
			fmt.Printf("    %s Unresolved conflicts\n", ui.RenderWarn("⚠"))
		}
		fmt.Println()
	}
	return nil
}

func runFederationAddPeer(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("federation add-peer is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("federation-add-peer")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	name := args[0]
	url := args[1]

	password := federationPassword
	if federationUser != "" && password == "" {
		fmt.Fprint(os.Stderr, "Password: ")
		pwBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return HandleErrorRespectJSON("failed to read password: %v", err)
		}
		password = string(pwBytes)
	}

	sov := federationSov
	if sov != "" {
		sov = strings.ToUpper(sov)
		if sov != "T1" && sov != "T2" && sov != "T3" && sov != "T4" {
			return HandleErrorRespectJSON("invalid sovereignty tier: %s (must be T1, T2, T3, or T4)", federationSov)
		}
	}

	if federationUser != "" {
		peer := &storage.FederationPeer{
			Name:        name,
			RemoteURL:   url,
			Username:    federationUser,
			Password:    password,
			Sovereignty: sov,
		}
		if err := store.AddFederationPeer(ctx, peer); err != nil {
			return HandleErrorRespectJSON("failed to add peer: %v", err)
		}
	} else {
		if err := store.AddRemote(ctx, name, url); err != nil {
			return HandleErrorRespectJSON("failed to add peer: %v", err)
		}
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"added":       name,
			"url":         url,
			"has_auth":    federationUser != "",
			"sovereignty": sov,
		})
	}

	fmt.Printf("Added peer %s: %s\n", ui.RenderAccent(name), url)
	if federationUser != "" {
		fmt.Printf("  User: %s (credentials stored)\n", federationUser)
	}
	if sov != "" {
		fmt.Printf("  Sovereignty: %s\n", sov)
	}
	return nil
}

func runFederationRemovePeer(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("federation remove-peer is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("federation-remove-peer")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	name := args[0]

	if err := store.RemoveRemote(ctx, name); err != nil {
		return HandleErrorRespectJSON("failed to remove peer: %v", err)
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"removed": name,
		})
	}

	fmt.Printf("Removed peer: %s\n", name)
	return nil
}

func runFederationListPeers(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("federation list-peers is not supported in proxied-server mode")
	}
	evt := metrics.NewCommandEvent("federation-list-peers")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	remotes, err := store.ListRemotes(ctx)
	if err != nil {
		return HandleErrorRespectJSON("failed to list peers: %v", err)
	}

	if jsonOutput {
		return outputJSON(formatFederationPeerListJSON(remotes))
	}

	if len(remotes) == 0 {
		fmt.Println("No federation peers configured.")
		return nil
	}

	fmt.Printf("\n%s Federation Peers:\n\n", ui.RenderAccent("🌐"))
	for _, r := range remotes {
		fmt.Printf("  %s  %s\n", ui.RenderAccent(r.Name), ui.RenderMuted(r.URL))
	}
	fmt.Println()
	return nil
}

type federationPeerListJSON struct {
	Name string `json:"Name"`
	URL  string `json:"URL"`
}

func formatFederationPeerListJSON(remotes []storage.RemoteInfo) []federationPeerListJSON {
	out := make([]federationPeerListJSON, 0, len(remotes))
	for _, r := range remotes {
		out = append(out, federationPeerListJSON{
			Name: r.Name,
			URL:  r.URL,
		})
	}
	return out
}
