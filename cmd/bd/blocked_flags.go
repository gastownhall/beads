package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
)

// blockedFlagRow is one line of `bd blocked --flags --json`: an id the caller
// named and the blocked flag bd holds for it.
type blockedFlagRow struct {
	ID        string `json:"id"`
	IsBlocked bool   `json:"is_blocked"`
}

// blockedFlagFilterFlags are the listing's filters, which --flags does not
// take: it answers about the ids it is handed, not about a set it derives.
var blockedFlagFilterFlags = []string{"parent", "label", "label-any", "exclude-label"}

// runBlockedFlags is `bd blocked --flags`: the blocked flag for each id read
// from stdin (one per line; blank lines and repeats ignored), through the
// issueops.BlockedFlags library entry over the route's BlockingAnnotator.
//
// IT IS THE FLAG, NOT THE LISTING. The listing narrows the stored flag to
// rows bd can name a blocker for; this answers the flag itself, the column
// `bd ready` filters on, so a row whose flag is set with no nameable blocker
// reads true here and is absent from the listing. Where the
// external-dependency policy applies, an unsatisfied external: blocker sets
// the flag as it keeps the row out of `bd ready`.
//
// Every id that names a row on either plane is answered, in the order the ids
// were first given, a closed row included (its flag is whatever is stored);
// an id naming no row is omitted. Over HTTP the read is
// listBlockingAnnotations with is_blocked, behind
// dependencies.blocking.isBlocked; a server without that token is refused
// before the dial, naming it.
//
// --raw answers the stored column itself, without the external-dependency
// decoration (blockedFlagsAnnotator): exactly what reading is_blocked from
// the issues and wisps tables returns.
func runBlockedFlags(cmd *cobra.Command, ctx context.Context, in io.Reader) error {
	for _, name := range blockedFlagFilterFlags {
		if cmd.Flags().Changed(name) {
			return HandleErrorRespectJSON("--flags answers the ids read from stdin and takes no --%s", name)
		}
	}
	ids, err := readBlockedFlagIDs(in)
	if err != nil {
		return HandleErrorRespectJSON("reading ids from stdin: %v", err)
	}
	raw, _ := cmd.Flags().GetBool("raw")
	annotator, err := blockedFlagsAnnotator(ctx, raw)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	flags, err := issueops.BlockedFlags(ctx, annotator, ids)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	rows := make([]blockedFlagRow, 0, len(flags))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		blocked, ok := flags[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		rows = append(rows, blockedFlagRow{ID: id, IsBlocked: blocked})
	}
	if jsonOutput {
		return outputJSON(rows)
	}
	for _, row := range rows {
		state := "not blocked"
		if row.IsBlocked {
			state = "blocked"
		}
		fmt.Printf("%s\t%s\n", row.ID, state)
	}
	return nil
}

// readBlockedFlagIDs reads one id per line, trimmed, skipping blank lines.
func readBlockedFlagIDs(in io.Reader) ([]string, error) {
	var ids []string
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if id := strings.TrimSpace(scanner.Text()); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, scanner.Err()
}

// blockedFlagsAnnotator is the BlockingAnnotator for the route this
// invocation runs: the store's own accessor on the direct routes (dolt,
// embedded dolt, HTTP), and the proxied-server provider's on the proxied one.
// Both carry the external-dependency decoration the rest of bd reads through.
//
// With raw it is the same role taken from BENEATH every decorator (the
// innermost store, the innermost provider), so the answer is the stored
// is_blocked column and nothing else: an unsatisfied external: blocker does
// not set it, because the column has never known about other projects.
func blockedFlagsAnnotator(ctx context.Context, raw bool) (issueops.BlockingAnnotator, error) {
	if !raw {
		if usesProxiedServer() {
			return proxiedBlockingAnnotator()
		}
		if store == nil {
			return nil, fmt.Errorf("no storage available")
		}
		return store.BlockingAnnotator()
	}
	if usesProxiedServer() {
		if uowProvider == nil {
			return nil, fmt.Errorf("proxied-server UOW provider not initialized")
		}
		src, ok := uow.UnwrapProvider(uowProvider).(uow.BlockingAnnotatorSource)
		if !ok {
			return nil, fmt.Errorf("proxied-server provider %T does not offer the blocking-decoration surface", uowProvider)
		}
		return src.BlockingAnnotator()
	}
	if store == nil {
		return nil, fmt.Errorf("no storage available")
	}
	base := storage.UnwrapStore(store)
	if err := refuseRawFlagsFromAPolicyServer(ctx, base); err != nil {
		return nil, err
	}
	return base.BlockingAnnotator()
}

// refuseRawFlagsFromAPolicyServer refuses --raw against a served workspace
// whose server applies the external-dependency policy itself
// (policy.external_dependencies). That server's blocking annotation already
// sets the flag for an unsatisfied external: blocker, the v0 wire has no
// member that asks it for the bare column, and the decorated flag cannot be
// turned back into the column on this side (a row that is externally blocked
// may or may not also carry the stored flag). Answering anyway would be the
// decorated flag spelled as the raw one. Against a server that does not apply
// the policy, its annotation IS the column, and --raw is served.
func refuseRawFlagsFromAPolicyServer(ctx context.Context, base storage.DoltStorage) error {
	prober, ok := base.(storage.ExternalDependencyPolicyProber)
	if !ok {
		return nil
	}
	enforced, err := prober.ServerEnforcesExternalDependencyPolicy(ctx)
	if err != nil {
		return err
	}
	if enforced {
		return fmt.Errorf("bd blocked --flags --raw is not served by this workspace's server: it applies the " +
			"external-dependency policy (policy.external_dependencies) to the blocked flag it answers, and the v0 " +
			"wire cannot ask it for the stored column; use bd blocked --flags (the policy-aware flag)")
	}
	return nil
}
