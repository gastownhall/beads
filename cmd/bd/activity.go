package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/ui"
)

var (
	activityFollow   bool
	activityMol      string
	activitySince    string
	activityType     string
	activityLimit    int
	activityInterval time.Duration
	activityTown     bool
)

// ActivityEvent represents a formatted activity event for output
type ActivityEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	IssueID   string    `json:"issue_id"`
	Symbol    string    `json:"symbol"`
	Message   string    `json:"message"`
	// Optional metadata from richer events
	OldStatus string `json:"old_status,omitempty"`
	NewStatus string `json:"new_status,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
	StepCount int    `json:"step_count,omitempty"`
	Actor     string `json:"actor,omitempty"`
}

var activityCmd = &cobra.Command{
	Use:     "activity",
	GroupID: "views",
	Short:   "Show real-time molecule state feed",
	Long: `Display a real-time feed of issue and molecule state changes.

This command shows mutations (create, update, delete) as they happen,
providing visibility into workflow progress.

Event symbols:
  +  created/bonded  - New issue or molecule created
  →  in_progress     - Work started on an issue
  ✓  completed       - Issue closed or step completed
  ✗  failed          - Step or issue failed
  ⊘  deleted         - Issue removed

Examples:
  bd activity                     # Show last 100 events
  bd activity --follow            # Real-time streaming
  bd activity --mol bd-x7k        # Filter by molecule prefix
  bd activity --since 5m          # Events from last 5 minutes
  bd activity --since 1h          # Events from last hour
  bd activity --type update       # Only show updates
  bd activity --limit 50          # Show last 50 events
  bd activity --town              # Aggregated feed from all rigs
  bd activity --follow --town     # Stream all rig activity`,
	Run: runActivity,
}

func init() {
	activityCmd.Flags().BoolVarP(&activityFollow, "follow", "f", false, "Stream events in real-time")
	activityCmd.Flags().StringVar(&activityMol, "mol", "", "Filter by molecule/issue ID prefix")
	activityCmd.Flags().StringVar(&activitySince, "since", "", "Show events since duration (e.g., 5m, 1h, 30s)")
	activityCmd.Flags().StringVar(&activityType, "type", "", "Filter by event type (create, update, delete, comment)")
	activityCmd.Flags().IntVar(&activityLimit, "limit", 100, "Maximum number of events to show")
	activityCmd.Flags().DurationVar(&activityInterval, "interval", 500*time.Millisecond, "Polling interval for --follow mode")
	activityCmd.Flags().BoolVar(&activityTown, "town", false, "Aggregated feed from all rigs (uses routes.jsonl)")

	rootCmd.AddCommand(activityCmd)
}

func runActivity(cmd *cobra.Command, args []string) {
	// Parse --since duration
	var sinceTime time.Time
	if activitySince != "" {
		duration, err := parseDurationString(activitySince)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --since duration: %v\n", err)
			os.Exit(1)
		}
		sinceTime = time.Now().Add(-duration)
	}

	// Town-wide aggregated feed
	if activityTown {
		if activityFollow {
			runTownActivityFollow(sinceTime)
		} else {
			runTownActivityOnce(sinceTime)
		}
		return
	}

	// Single-rig activity requires daemon
	if daemonClient == nil {
		fmt.Fprintln(os.Stderr, "Error: activity command requires daemon (mutations not available in direct mode)")
		fmt.Fprintln(os.Stderr, "Hint: Start daemon with 'bd daemons start .' or remove --no-daemon flag")
		os.Exit(1)
	}

	if activityFollow {
		runActivityFollow(sinceTime)
	} else {
		runActivityOnce(sinceTime)
	}
}

// runActivityOnce fetches and displays events once
func runActivityOnce(sinceTime time.Time) {
	events, err := fetchMutations(sinceTime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Apply filters and limit
	events = filterEvents(events)
	if len(events) > activityLimit {
		events = events[len(events)-activityLimit:]
	}

	if jsonOutput {
		formatted := make([]ActivityEvent, 0, len(events))
		for _, e := range events {
			formatted = append(formatted, formatEvent(e))
		}
		outputJSON(formatted)
		return
	}

	if len(events) == 0 {
		fmt.Println("No recent activity")
		return
	}

	for _, e := range events {
		printEvent(e)
	}
}

// runActivityFollow streams events in real-time
func runActivityFollow(sinceTime time.Time) {
	// Start from now if no --since specified
	lastPoll := time.Now().Add(-1 * time.Second)
	if !sinceTime.IsZero() {
		lastPoll = sinceTime
	}

	// First fetch any events since the start time
	events, err := fetchMutations(sinceTime)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Apply filters and display initial events
	events = filterEvents(events)
	for _, e := range events {
		if jsonOutput {
			data, _ := json.Marshal(formatEvent(e))
			fmt.Println(string(data))
		} else {
			printEvent(e)
		}
		if e.Timestamp.After(lastPoll) {
			lastPoll = e.Timestamp
		}
	}

	// Poll for new events
	ticker := time.NewTicker(activityInterval)
	defer ticker.Stop()

	// Track consecutive failures for error reporting
	consecutiveFailures := 0
	const failureWarningThreshold = 5
	lastWarningTime := time.Time{}

	for {
		select {
		case <-rootCtx.Done():
			return
		case <-ticker.C:
			newEvents, err := fetchMutations(lastPoll)
			if err != nil {
				consecutiveFailures++
				// Show warning after threshold failures, but not more than once per 30 seconds
				if consecutiveFailures >= failureWarningThreshold {
					if time.Since(lastWarningTime) >= 30*time.Second {
						if jsonOutput {
							// Emit error event in JSON mode
							errorEvent := map[string]interface{}{
								"type":      "error",
								"message":   fmt.Sprintf("daemon unreachable (%d failures)", consecutiveFailures),
								"timestamp": time.Now().Format(time.RFC3339),
							}
							data, _ := json.Marshal(errorEvent)
							fmt.Fprintln(os.Stderr, string(data))
						} else {
							timestamp := time.Now().Format("15:04:05")
							fmt.Fprintf(os.Stderr, "[%s] %s daemon unreachable (%d consecutive failures)\n",
								timestamp, ui.RenderWarn("!"), consecutiveFailures)
						}
						lastWarningTime = time.Now()
					}
				}
				continue
			}

			// Reset failure counter on success
			if consecutiveFailures > 0 {
				if consecutiveFailures >= failureWarningThreshold && !jsonOutput {
					timestamp := time.Now().Format("15:04:05")
					fmt.Fprintf(os.Stderr, "[%s] %s daemon reconnected\n", timestamp, ui.RenderPass("✓"))
				}
				consecutiveFailures = 0
			}

			newEvents = filterEvents(newEvents)
			for _, e := range newEvents {
				if jsonOutput {
					data, _ := json.Marshal(formatEvent(e))
					fmt.Println(string(data))
				} else {
					printEvent(e)
				}
				if e.Timestamp.After(lastPoll) {
					lastPoll = e.Timestamp
				}
			}
		}
	}
}

// fetchMutations retrieves mutations from the daemon
func fetchMutations(since time.Time) ([]rpc.MutationEvent, error) {
	var sinceMillis int64
	if !since.IsZero() {
		sinceMillis = since.UnixMilli()
	}

	resp, err := daemonClient.GetMutations(&rpc.GetMutationsArgs{Since: sinceMillis})
	if err != nil {
		return nil, fmt.Errorf("failed to get mutations: %w", err)
	}

	var mutations []rpc.MutationEvent
	if err := json.Unmarshal(resp.Data, &mutations); err != nil {
		return nil, fmt.Errorf("failed to parse mutations: %w", err)
	}

	return mutations, nil
}

// filterEvents applies --mol and --type filters
func filterEvents(events []rpc.MutationEvent) []rpc.MutationEvent {
	if activityMol == "" && activityType == "" {
		return events
	}

	filtered := make([]rpc.MutationEvent, 0, len(events))
	for _, e := range events {
		// Filter by molecule/issue ID prefix
		if activityMol != "" && !strings.HasPrefix(e.IssueID, activityMol) {
			continue
		}
		// Filter by event type
		if activityType != "" && e.Type != activityType {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}

// formatEvent converts a mutation event to a formatted activity event
func formatEvent(e rpc.MutationEvent) ActivityEvent {
	symbol, message := getEventDisplay(e)
	return ActivityEvent{
		Timestamp: e.Timestamp,
		Type:      e.Type,
		IssueID:   e.IssueID,
		Symbol:    symbol,
		Message:   message,
		OldStatus: e.OldStatus,
		NewStatus: e.NewStatus,
		ParentID:  e.ParentID,
		StepCount: e.StepCount,
		Actor:     e.Actor,
	}
}

// getEventDisplay returns the symbol and message for an event type
func getEventDisplay(e rpc.MutationEvent) (symbol, message string) {
	// Build context suffix: title and/or assignee
	context := buildEventContext(e)

	switch e.Type {
	case rpc.MutationCreate:
		return "+", fmt.Sprintf("%s created%s", e.IssueID, context)
	case rpc.MutationUpdate:
		return "→", fmt.Sprintf("%s updated%s", e.IssueID, context)
	case rpc.MutationDelete:
		return "⊘", fmt.Sprintf("%s deleted%s", e.IssueID, context)
	case rpc.MutationComment:
		return "💬", fmt.Sprintf("%s comment%s", e.IssueID, context)
	case rpc.MutationBonded:
		if e.StepCount > 0 {
			return "+", fmt.Sprintf("%s bonded (%d steps)%s", e.IssueID, e.StepCount, context)
		}
		return "+", fmt.Sprintf("%s bonded%s", e.IssueID, context)
	case rpc.MutationSquashed:
		return "◉", fmt.Sprintf("%s SQUASHED%s", e.IssueID, context)
	case rpc.MutationBurned:
		return "🔥", fmt.Sprintf("%s burned%s", e.IssueID, context)
	case rpc.MutationStatus:
		// Status change with transition info
		if e.NewStatus == "in_progress" {
			return "→", fmt.Sprintf("%s started%s", e.IssueID, context)
		} else if e.NewStatus == "closed" {
			return "✓", fmt.Sprintf("%s completed%s", e.IssueID, context)
		} else if e.NewStatus == "open" && e.OldStatus != "" {
			return "↺", fmt.Sprintf("%s reopened%s", e.IssueID, context)
		}
		return "→", fmt.Sprintf("%s → %s%s", e.IssueID, e.NewStatus, context)
	default:
		return "•", fmt.Sprintf("%s %s%s", e.IssueID, e.Type, context)
	}
}

// buildEventContext creates a context string from title and actor/assignee
func buildEventContext(e rpc.MutationEvent) string {
	var parts []string

	// Add truncated title if present
	if e.Title != "" {
		title := truncateString(e.Title, 40)
		parts = append(parts, title)
	}

	// For status changes, prefer showing actor (who performed the action)
	// For other events, show assignee
	if e.Actor != "" {
		parts = append(parts, "@"+e.Actor)
	} else if e.Assignee != "" {
		parts = append(parts, "@"+e.Assignee)
	}

	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " ")
}

// truncateString truncates a string to maxLen, adding ellipsis if needed
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// printEvent prints a formatted event to stdout
func printEvent(e rpc.MutationEvent) {
	symbol, message := getEventDisplay(e)
	timestamp := e.Timestamp.Format("15:04:05")

	// Colorize output based on event type
	var coloredSymbol string
	switch e.Type {
	case rpc.MutationCreate, rpc.MutationBonded:
		coloredSymbol = ui.RenderPass(symbol)
	case rpc.MutationUpdate:
		coloredSymbol = ui.RenderWarn(symbol)
	case rpc.MutationDelete, rpc.MutationBurned:
		coloredSymbol = ui.RenderFail(symbol)
	case rpc.MutationComment:
		coloredSymbol = ui.RenderAccent(symbol)
	case rpc.MutationSquashed:
		coloredSymbol = ui.RenderAccent(symbol)
	case rpc.MutationStatus:
		// Color based on new status
		if e.NewStatus == "closed" {
			coloredSymbol = ui.RenderPass(symbol)
		} else if e.NewStatus == "in_progress" {
			coloredSymbol = ui.RenderWarn(symbol)
		} else {
			coloredSymbol = ui.RenderAccent(symbol)
		}
	default:
		coloredSymbol = symbol
	}

	fmt.Printf("[%s] %s %s\n", timestamp, coloredSymbol, message)
}

// parseDurationString parses duration strings like "5m", "1h", "30s", "2d"
func parseDurationString(s string) (time.Duration, error) {
	// Try standard Go duration first
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// Handle custom formats like "2d" for days
	re := regexp.MustCompile(`^(\d+)([dhms])$`)
	matches := re.FindStringSubmatch(strings.ToLower(s))
	if len(matches) != 3 {
		return 0, fmt.Errorf("invalid duration format: %s (use 5m, 1h, 30s, or 2d)", s)
	}

	value, _ := strconv.Atoi(matches[1])
	unit := matches[2]

	switch unit {
	case "d":
		return time.Duration(value) * 24 * time.Hour, nil
	case "h":
		return time.Duration(value) * time.Hour, nil
	case "m":
		return time.Duration(value) * time.Minute, nil
	case "s":
		return time.Duration(value) * time.Second, nil
	default:
		return 0, fmt.Errorf("unknown duration unit: %s", unit)
	}
}

// rigDaemon holds a connection to a rig's daemon
type rigDaemon struct {
	prefix string      // e.g., "bd-"
	rig    string      // e.g., "beads"
	client *rpc.Client // nil if daemon not running
}

// discoverRigDaemons finds all rigs via routes.jsonl and connects to their daemons
func discoverRigDaemons() []rigDaemon {
	var daemons []rigDaemon

	// Find town beads directory (uses findTownBeadsDir from create.go)
	townBeadsDir, err := findTownBeadsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: not in an orchestrator environment (%v)\n", err)
		os.Exit(1)
	}

	// Load routes
	routes, err := routing.LoadRoutes(townBeadsDir)
	if err != nil || len(routes) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no routes found in routes.jsonl")
		os.Exit(1)
	}

	townRoot := filepath.Dir(townBeadsDir)

	for _, route := range routes {
		// Resolve beads directory for this route
		var beadsDir string
		if route.Path == "." {
			beadsDir = townBeadsDir
		} else {
			beadsDir = filepath.Join(townRoot, route.Path, ".beads")
		}

		// Follow redirect if present
		beadsDir = resolveBeadsRedirect(beadsDir)

		// Check if daemon is running
		socketPath := filepath.Join(beadsDir, "bd.sock")
		client, _ := rpc.TryConnect(socketPath)

		rigName := routing.ExtractProjectFromPath(route.Path)
		if rigName == "" {
			rigName = "town" // For path="."
		}

		daemons = append(daemons, rigDaemon{
			prefix: route.Prefix,
			rig:    rigName,
			client: client,
		})
	}

	return daemons
}

// resolveBeadsRedirect follows a redirect file if present.
// Similar to routing.resolveRedirect but simplified for activity use.
func resolveBeadsRedirect(beadsDir string) string {
	redirectFile := filepath.Join(beadsDir, "redirect")
	data, err := os.ReadFile(redirectFile) // #nosec G304 - redirects are trusted within beads rig paths
	if err != nil {
		return beadsDir
	}

	redirectPath := strings.TrimSpace(string(data))
	if redirectPath == "" {
		return beadsDir
	}

	// Handle relative paths
	if !filepath.IsAbs(redirectPath) {
		redirectPath = filepath.Join(beadsDir, redirectPath)
	}

	redirectPath = filepath.Clean(redirectPath)

	// Verify target exists before returning
	if info, err := os.Stat(redirectPath); err == nil && info.IsDir() {
		return redirectPath
	}

	return beadsDir // Fallback to original if redirect target invalid
}

// fetchTownMutations retrieves mutations from all rig daemons
func fetchTownMutations(daemons []rigDaemon, since time.Time) []rpc.MutationEvent {
	events, _ := fetchTownMutationsWithStatus(daemons, since)
	return events
}

// fetchTownMutationsWithStatus retrieves mutations and returns count of responding daemons
func fetchTownMutationsWithStatus(daemons []rigDaemon, since time.Time) ([]rpc.MutationEvent, int) {
	var allEvents []rpc.MutationEvent
	activeCount := 0

	var sinceMillis int64
	if !since.IsZero() {
		sinceMillis = since.UnixMilli()
	}

	for _, d := range daemons {
		if d.client == nil {
			continue
		}

		resp, err := d.client.GetMutations(&rpc.GetMutationsArgs{Since: sinceMillis})
		if err != nil {
			continue
		}

		activeCount++

		var mutations []rpc.MutationEvent
		if err := json.Unmarshal(resp.Data, &mutations); err != nil {
			continue
		}

		allEvents = append(allEvents, mutations...)
	}

	// Sort by timestamp
	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].Timestamp.Before(allEvents[j].Timestamp)
	})

	return allEvents, activeCount
}

// runTownActivityOnce fetches and displays events from all rigs once
func runTownActivityOnce(sinceTime time.Time) {
	daemons := discoverRigDaemons()
	defer closeDaemons(daemons)

	// Count active daemons
	activeCount := 0
	for _, d := range daemons {
		if d.client != nil {
			activeCount++
		}
	}

	if activeCount == 0 {
		fmt.Fprintln(os.Stderr, "Error: no rig daemons running")
		fmt.Fprintln(os.Stderr, "Hint: Start daemons with 'bd daemons start' in each rig")
		os.Exit(1)
	}

	events := fetchTownMutations(daemons, sinceTime)

	// Apply filters and limit
	events = filterEvents(events)
	if len(events) > activityLimit {
		events = events[len(events)-activityLimit:]
	}

	if jsonOutput {
		formatted := make([]ActivityEvent, 0, len(events))
		for _, e := range events {
			formatted = append(formatted, formatEvent(e))
		}
		outputJSON(formatted)
		return
	}

	if len(events) == 0 {
		fmt.Printf("No recent activity across %d rigs\n", activeCount)
		return
	}

	for _, e := range events {
		printEvent(e)
	}
}

// runTownActivityFollow streams events from all rigs in real-time
func runTownActivityFollow(sinceTime time.Time) {
	daemons := discoverRigDaemons()
	defer closeDaemons(daemons)

	// Count active daemons
	activeCount := 0
	var activeRigs []string
	for _, d := range daemons {
		if d.client != nil {
			activeCount++
			activeRigs = append(activeRigs, d.rig)
		}
	}

	if activeCount == 0 {
		fmt.Fprintln(os.Stderr, "Error: no rig daemons running")
		fmt.Fprintln(os.Stderr, "Hint: Start daemons with 'bd daemons start' in each rig")
		os.Exit(1)
	}

	// Show which rigs we're monitoring
	if !jsonOutput {
		fmt.Printf("Streaming activity from %d rigs: %s\n", activeCount, strings.Join(activeRigs, ", "))
	}

	// Start from now if no --since specified
	lastPoll := time.Now().Add(-1 * time.Second)
	if !sinceTime.IsZero() {
		lastPoll = sinceTime
	}

	// First fetch any events since the start time
	events := fetchTownMutations(daemons, sinceTime)
	events = filterEvents(events)

	for _, e := range events {
		if jsonOutput {
			data, _ := json.Marshal(formatEvent(e))
			fmt.Println(string(data))
		} else {
			printEvent(e)
		}
		if e.Timestamp.After(lastPoll) {
			lastPoll = e.Timestamp
		}
	}

	// Poll for new events
	ticker := time.NewTicker(activityInterval)
	defer ticker.Stop()

	// Track failures for warning messages
	consecutiveFailures := 0
	const failureWarningThreshold = 5
	lastWarningTime := time.Time{}
	lastActiveCount := activeCount

	for {
		select {
		case <-rootCtx.Done():
			return
		case <-ticker.C:
			newEvents, currentActive := fetchTownMutationsWithStatus(daemons, lastPoll)

			// Track daemon availability changes
			if currentActive < lastActiveCount {
				consecutiveFailures++
				if consecutiveFailures >= failureWarningThreshold {
					if time.Since(lastWarningTime) >= 30*time.Second {
						if !jsonOutput {
							timestamp := time.Now().Format("15:04:05")
							fmt.Fprintf(os.Stderr, "[%s] %s some rigs unreachable (%d/%d active)\n",
								timestamp, ui.RenderWarn("!"), currentActive, len(daemons))
						}
						lastWarningTime = time.Now()
					}
				}
			} else if currentActive > lastActiveCount {
				// Daemon came back
				if !jsonOutput {
					timestamp := time.Now().Format("15:04:05")
					fmt.Fprintf(os.Stderr, "[%s] %s rig reconnected (%d/%d active)\n",
						timestamp, ui.RenderPass("✓"), currentActive, len(daemons))
				}
				consecutiveFailures = 0
			}
			lastActiveCount = currentActive

			newEvents = filterEvents(newEvents)

			for _, e := range newEvents {
				if jsonOutput {
					data, _ := json.Marshal(formatEvent(e))
					fmt.Println(string(data))
				} else {
					printEvent(e)
				}
				if e.Timestamp.After(lastPoll) {
					lastPoll = e.Timestamp
				}
			}
		}
	}
}

// closeDaemons closes all daemon connections
func closeDaemons(daemons []rigDaemon) {
	for _, d := range daemons {
		if d.client != nil {
			if err := d.client.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to close daemon client: %v\n", err)
			}
		}
	}
}
