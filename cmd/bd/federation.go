package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/ui"
)

var (
	federationPeer     string
	federationStrategy string
)

var federationCmd = &cobra.Command{
	Use:     "federation",
	GroupID: "sync",
	Short:   "Manage peer-to-peer federation with other Gas Towns",
	Long: `Manage peer-to-peer federation between Dolt-backed beads databases.

Federation enables synchronized issue tracking across multiple Gas Towns,
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
	Run: runFederationSync,
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
	Run: runFederationStatus,
}

var federationAddPeerCmd = &cobra.Command{
	Use:   "add-peer <name> <url>",
	Short: "Add a federation peer",
	Long: `Add a new federation peer remote.

The URL can be:
  - dolthub://org/repo      DoltHub hosted repository
  - host:port/database      Direct dolt sql-server connection
  - file:///path/to/repo    Local file path (for testing)

Examples:
  bd federation add-peer town-beta dolthub://acme/town-beta-beads
  bd federation add-peer town-gamma 192.168.1.100:3306/beads`,
	Args: cobra.ExactArgs(2),
	Run:  runFederationAddPeer,
}

var federationRemovePeerCmd = &cobra.Command{
	Use:   "remove-peer <name>",
	Short: "Remove a federation peer",
	Args:  cobra.ExactArgs(1),
	Run:   runFederationRemovePeer,
}

var federationListPeersCmd = &cobra.Command{
	Use:   "list-peers",
	Short: "List configured federation peers",
	Run:   runFederationListPeers,
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

	rootCmd.AddCommand(federationCmd)
}

func getFederatedStore() (*dolt.DoltStore, error) {
	fs, ok := storage.AsFederated(store)
	if !ok {
		return nil, fmt.Errorf("federation requires Dolt backend (current backend does not support federation)")
	}
	// Type assert to get the concrete DoltStore for Sync method
	ds, ok := fs.(*dolt.DoltStore)
	if !ok {
		return nil, fmt.Errorf("internal error: federated storage is not DoltStore")
	}
	return ds, nil
}

func runFederationSync(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	ds, err := getFederatedStore()
	if err != nil {
		FatalErrorRespectJSON("%v", err)
	}

	// Validate strategy if provided
	if federationStrategy != "" && federationStrategy != "ours" && federationStrategy != "theirs" {
		FatalErrorRespectJSON("invalid strategy %q: must be 'ours' or 'theirs'", federationStrategy)
	}

	// Get peers to sync with
	var peers []string
	if federationPeer != "" {
		peers = []string{federationPeer}
	} else {
		// Get all configured remotes
		remotes, err := ds.ListRemotes(ctx)
		if err != nil {
			FatalErrorRespectJSON("failed to list peers: %v", err)
		}
		for _, r := range remotes {
			// Skip 'origin' which is typically the backup remote, not a peer
			if r.Name != "origin" {
				peers = append(peers, r.Name)
			}
		}
	}

	if len(peers) == 0 {
		FatalErrorRespectJSON("no federation peers configured (use 'bd federation add-peer' to add peers)")
	}

	// Sync with each peer
	var results []*dolt.SyncResult
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
		outputJSON(map[string]interface{}{
			"peers":   peers,
			"results": results,
		})
	}
}

func runFederationStatus(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	ds, err := getFederatedStore()
	if err != nil {
		FatalErrorRespectJSON("%v", err)
	}

	// Get peers to check
	var peers []string
	if federationPeer != "" {
		peers = []string{federationPeer}
	} else {
		remotes, err := ds.ListRemotes(ctx)
		if err != nil {
			FatalErrorRespectJSON("failed to list peers: %v", err)
		}
		for _, r := range remotes {
			peers = append(peers, r.Name)
		}
	}

	if len(peers) == 0 {
		if jsonOutput {
			outputJSON(map[string]interface{}{"peers": []string{}})
		} else {
			fmt.Println("No federation peers configured.")
		}
		return
	}

	var statuses []*storage.SyncStatus
	for _, peer := range peers {
		status, _ := ds.SyncStatus(ctx, peer)
		statuses = append(statuses, status)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"peers":    peers,
			"statuses": statuses,
		})
		return
	}

	fmt.Printf("\n%s Federation Status:\n\n", ui.RenderAccent("🌐"))
	for _, status := range statuses {
		fmt.Printf("  %s\n", ui.RenderAccent(status.Peer))
		if status.LocalAhead >= 0 {
			fmt.Printf("    Ahead:  %d commits\n", status.LocalAhead)
			fmt.Printf("    Behind: %d commits\n", status.LocalBehind)
		} else {
			fmt.Printf("    Status: not fetched yet\n")
		}
		if status.HasConflicts {
			fmt.Printf("    %s Unresolved conflicts\n", ui.RenderWarn("⚠"))
		}
		fmt.Println()
	}
}

func runFederationAddPeer(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	name := args[0]
	url := args[1]

	fs, ok := storage.AsFederated(store)
	if !ok {
		FatalErrorRespectJSON("federation requires Dolt backend")
	}

	if err := fs.AddRemote(ctx, name, url); err != nil {
		FatalErrorRespectJSON("failed to add peer: %v", err)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"added": name,
			"url":   url,
		})
		return
	}

	fmt.Printf("Added peer %s: %s\n", ui.RenderAccent(name), url)
}

func runFederationRemovePeer(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	name := args[0]

	fs, ok := storage.AsFederated(store)
	if !ok {
		FatalErrorRespectJSON("federation requires Dolt backend")
	}

	if err := fs.RemoveRemote(ctx, name); err != nil {
		FatalErrorRespectJSON("failed to remove peer: %v", err)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"removed": name,
		})
		return
	}

	fmt.Printf("Removed peer: %s\n", name)
}

func runFederationListPeers(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	fs, ok := storage.AsFederated(store)
	if !ok {
		FatalErrorRespectJSON("federation requires Dolt backend")
	}

	remotes, err := fs.ListRemotes(ctx)
	if err != nil {
		FatalErrorRespectJSON("failed to list peers: %v", err)
	}

	if jsonOutput {
		outputJSON(remotes)
		return
	}

	if len(remotes) == 0 {
		fmt.Println("No federation peers configured.")
		return
	}

	fmt.Printf("\n%s Federation Peers:\n\n", ui.RenderAccent("🌐"))
	for _, r := range remotes {
		fmt.Printf("  %s  %s\n", ui.RenderAccent(r.Name), ui.RenderMuted(r.URL))
	}
	fmt.Println()
}
