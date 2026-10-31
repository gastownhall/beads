package main

import (
	"context"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// configKeyEventsRetainDays is the workspace setting the Events phase of
// `bd gc` reads when --events-older-than is not given: keep history events
// (the events table) for this many days. 0, the default, keeps every event.
// It is a yaml-only key (config.YamlOnlyKeys): read through viper, so
// `bd config set events-retain-days N` lands in config.yaml and is seen.
const configKeyEventsRetainDays = "events-retain-days"

// gcEventsRetention resolves how old a history event must be for this gc run
// to prune it. The flag wins when set (0 means every event older than now);
// otherwise the config key, where 0 disables the phase.
func gcEventsRetention() (days int, enabled bool, source string) {
	if gcEventsOlderThan >= 0 {
		return gcEventsOlderThan, true, "--events-older-than"
	}
	days = config.GetInt(configKeyEventsRetainDays)
	return days, days > 0, configKeyEventsRetainDays
}

// gcEventsOutcome is one line of the gc summary: the Events phase's result.
type gcEventsOutcome struct {
	skipped bool
	detail  string
}

// runGCEventsPhase runs the Events phase of `bd gc` against a backend's count
// and prune, printing the phase's text-mode lines and returning its summary
// row. The route (direct store or proxied server) supplies the two callbacks;
// a nil count means the backend has no events table to prune. prune is called
// only for a run that is neither --dry-run nor refused for want of --force,
// and only when count found something.
func runGCEventsPhase(ctx context.Context,
	count func(context.Context, time.Time) (int64, error),
	prune func(context.Context, time.Time) (int64, error),
) (gcEventsOutcome, error) {
	days, enabled, source := gcEventsRetention()
	if gcSkipEvents {
		return gcEventsOutcome{skipped: true}, nil
	}
	if !jsonOutput {
		fmt.Println("Phase 2/4: Events (prune history events)")
	}
	finish := func(o gcEventsOutcome, line string) (gcEventsOutcome, error) {
		if !jsonOutput {
			fmt.Println(line)
			fmt.Println()
		}
		return o, nil
	}
	if !enabled {
		return finish(gcEventsOutcome{skipped: true, detail: "no retention set"},
			fmt.Sprintf("  %s is 0: keeping every history event (set it, or pass --events-older-than N)", configKeyEventsRetainDays))
	}
	if count == nil {
		return finish(gcEventsOutcome{detail: "not supported"}, "  Storage backend does not support pruning history events, skipping")
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	n, err := count(ctx, cutoff)
	if err != nil {
		return gcEventsOutcome{}, fmt.Errorf("counting history events older than %d days: %w", days, err)
	}
	switch {
	case n == 0:
		return finish(gcEventsOutcome{detail: "0 events pruned"},
			fmt.Sprintf("  No history events older than %d days (%s)", days, source))
	case gcDryRun:
		return finish(gcEventsOutcome{detail: fmt.Sprintf("%d events (dry-run)", n)},
			fmt.Sprintf("  Would prune %d history event(s) older than %d days (%s)", n, days, source))
	case !gcForce:
		return gcEventsOutcome{}, HandleErrorWithHintRespectJSON(
			fmt.Sprintf("would prune %d history event(s) older than %d days", n, days),
			"Use --force to confirm or --dry-run to preview.")
	}
	pruned, err := prune(ctx, cutoff)
	if err != nil {
		return gcEventsOutcome{}, fmt.Errorf("pruning history events older than %d days: %w", days, err)
	}
	commandDidWrite.Store(true)
	return finish(gcEventsOutcome{detail: fmt.Sprintf("%d events pruned", pruned)},
		fmt.Sprintf("  Pruned %d history event(s) older than %d days (%s); the rows leave disk once the commits that held them are squashed and GC'd", pruned, days, source))
}
