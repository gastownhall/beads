package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
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
  bd activity --limit 50          # Show last 50 events`,
	Run: runActivity,
}

func init() {
	activityCmd.Flags().BoolVarP(&activityFollow, "follow", "f", false, "Stream events in real-time")
	activityCmd.Flags().StringVar(&activityMol, "mol", "", "Filter by molecule/issue ID prefix")
	activityCmd.Flags().StringVar(&activitySince, "since", "", "Show events since duration (e.g., 5m, 1h, 30s)")
	activityCmd.Flags().StringVar(&activityType, "type", "", "Filter by event type (create, update, delete, comment)")
	activityCmd.Flags().IntVar(&activityLimit, "limit", 100, "Maximum number of events to show")
	activityCmd.Flags().DurationVar(&activityInterval, "interval", 500*time.Millisecond, "Polling interval for --follow mode")

	rootCmd.AddCommand(activityCmd)
}

func runActivity(cmd *cobra.Command, args []string) {
	// Activity requires daemon for mutation events
	if daemonClient == nil {
		fmt.Fprintln(os.Stderr, "Error: activity command requires daemon (mutations not available in direct mode)")
		fmt.Fprintln(os.Stderr, "Hint: Start daemon with 'bd daemons start .' or remove --no-daemon flag")
		os.Exit(1)
	}

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

	for {
		select {
		case <-rootCtx.Done():
			return
		case <-ticker.C:
			newEvents, err := fetchMutations(lastPoll)
			if err != nil {
				// Daemon might be down, continue trying
				continue
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
	}
}

// getEventDisplay returns the symbol and message for an event type
func getEventDisplay(e rpc.MutationEvent) (symbol, message string) {
	switch e.Type {
	case rpc.MutationCreate:
		return "+", fmt.Sprintf("%s created", e.IssueID)
	case rpc.MutationUpdate:
		return "→", fmt.Sprintf("%s updated", e.IssueID)
	case rpc.MutationDelete:
		return "⊘", fmt.Sprintf("%s deleted", e.IssueID)
	case rpc.MutationComment:
		return "💬", fmt.Sprintf("%s comment added", e.IssueID)
	case rpc.MutationBonded:
		if e.StepCount > 0 {
			return "+", fmt.Sprintf("%s bonded (%d steps)", e.IssueID, e.StepCount)
		}
		return "+", fmt.Sprintf("%s bonded", e.IssueID)
	case rpc.MutationSquashed:
		return "◉", fmt.Sprintf("%s SQUASHED", e.IssueID)
	case rpc.MutationBurned:
		return "🔥", fmt.Sprintf("%s burned", e.IssueID)
	case rpc.MutationStatus:
		// Status change with transition info
		if e.NewStatus == "in_progress" {
			return "→", fmt.Sprintf("%s in_progress", e.IssueID)
		} else if e.NewStatus == "closed" {
			return "✓", fmt.Sprintf("%s completed", e.IssueID)
		} else if e.NewStatus == "open" && e.OldStatus != "" {
			return "↺", fmt.Sprintf("%s reopened", e.IssueID)
		}
		return "→", fmt.Sprintf("%s %s", e.IssueID, e.NewStatus)
	default:
		return "•", fmt.Sprintf("%s %s", e.IssueID, e.Type)
	}
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
