package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// readyInput is everything `bd ready` parsed off the command line: the
// frontend-independent query knobs (issueops.ReadyRequest, the request the
// reader role takes and the filter is built from), the filter itself, and the
// mode and presentation choices that never leave the CLI.
type readyInput struct {
	issueops.ReadyRequest

	filter types.WorkFilter

	// dirLabels is the directory-label default (GH#541) this listing applied,
	// or nil when it did not apply one. It is the gatherer's DECISION rather
	// than the configured value: the question is answerable only BEFORE the
	// default has been written onto the filter. See readyRoleRequest.
	dirLabels []string

	claim        bool
	gated        bool
	molID        string
	explain      bool
	prettyFormat bool
	plainFormat  bool
	jsonOut      bool
}

// gatherReadyInput parses `bd ready`'s flags and builds the work filter. Both
// routes call it - the direct one in ready.go and the proxied one in
// ready_proxied_server.go - so there is one definition of what the command
// accepts. Usage errors are reported through HandleErrorRespectJSON, which is
// what a --json caller has always gotten from the direct route; the proxied
// route printed five of them as plain stderr text before the two builders were
// collapsed, so TestGatherReadyInputUsageErrorsRespectJSON pins the unified
// behavior that replaced them.
//
// resolveCap resolves --max-rows / BEADS_MAX_ROWS and is passed in rather than
// called directly because only the direct route has a cap to enforce: the
// proxied route rejects a live one in its own RunE and ignores it for --claim,
// and resolving a second time here would repeat resolveMaxRowsEnvOnly's
// malformed-value warning. It runs where the direct route's inline builder ran
// it, ahead of the metadata and sort checks, so a doubly-invalid command line
// still reports the cap first and a malformed BEADS_MAX_ROWS still warns even
// when a later check aborts the command.
func gatherReadyInput(cmd *cobra.Command, resolveCap func(*cobra.Command) (int, string, error)) (readyInput, error) {
	in := readyInput{}

	in.claim, _ = cmd.Flags().GetBool("claim")
	in.gated, _ = cmd.Flags().GetBool("gated")
	in.molID, _ = cmd.Flags().GetString("mol")
	in.explain, _ = cmd.Flags().GetBool("explain")
	in.Brief, _ = cmd.Flags().GetBool("brief")
	in.prettyFormat, _ = cmd.Flags().GetBool("pretty")
	in.plainFormat, _ = cmd.Flags().GetBool("plain")
	in.jsonOut = jsonOutput

	limit, _ := cmd.Flags().GetInt("limit")
	in.Limit = &limit
	// A negative --offset is not a page request, so it never reaches the
	// filter. Rejecting it belongs to the proxied RunE, the only route that
	// pages at all: the direct route rejects --offset > 0 outright and has
	// always ignored a negative value rather than failing on it.
	if offset, _ := cmd.Flags().GetInt("offset"); offset > 0 {
		in.Offset = offset
	}
	in.Assignee, _ = cmd.Flags().GetString("assignee")
	in.Unassigned, _ = cmd.Flags().GetBool("unassigned")
	in.Sort, _ = cmd.Flags().GetString("sort")
	in.Labels, _ = cmd.Flags().GetStringSlice("label")
	in.LabelsAny, _ = cmd.Flags().GetStringSlice("label-any")
	in.ExcludeLabels, _ = cmd.Flags().GetStringSlice("exclude-label")
	in.LabelPattern, _ = cmd.Flags().GetString("label-pattern")
	in.LabelRegex, _ = cmd.Flags().GetString("label-regex")
	in.IssueType, _ = cmd.Flags().GetString("type")
	in.ParentID, _ = cmd.Flags().GetString("parent")
	in.IncludeDeferred, _ = cmd.Flags().GetBool("include-deferred")
	in.IncludeEphemeral, _ = cmd.Flags().GetBool("include-ephemeral")
	in.ExcludeTypes, _ = cmd.Flags().GetStringSlice("exclude-type")

	if molTypeStr, _ := cmd.Flags().GetString("mol-type"); molTypeStr != "" {
		mt := types.MolType(molTypeStr)
		if !mt.IsValid() {
			return in, HandleErrorRespectJSON("invalid mol-type %q (must be %s)", molTypeStr, types.ValidMolTypeNames())
		}
		in.MolType = &mt
	}

	if in.claim && in.Assignee != "" {
		return in, HandleErrorRespectJSON("--claim cannot be combined with --assignee")
	}
	if in.claim && in.gated {
		return in, HandleErrorRespectJSON("--claim cannot be combined with --gated")
	}
	if in.claim && in.molID != "" {
		return in, HandleErrorRespectJSON("--claim cannot be combined with --mol")
	}
	if in.claim && in.explain {
		return in, HandleErrorRespectJSON("--claim cannot be combined with --explain")
	}
	if err := briefModeConflict(in.Brief, in.claim, in.gated, in.explain, in.molID, in.jsonOut); err != nil {
		return in, err
	}
	if in.Offset > 0 && in.claim {
		return in, HandleErrorRespectJSON("--offset cannot be combined with --claim")
	}
	if in.Offset > 0 && in.gated {
		return in, HandleErrorRespectJSON("--offset cannot be combined with --gated")
	}
	if in.Offset > 0 && in.molID != "" {
		return in, HandleErrorRespectJSON("--offset cannot be combined with --mol")
	}
	if in.Offset > 0 && in.explain {
		return in, HandleErrorRespectJSON("--offset cannot be combined with --explain")
	}

	var maxRows int
	var maxRowsSource string
	if resolveCap != nil {
		var err error
		if maxRows, maxRowsSource, err = resolveCap(cmd); err != nil {
			return in, err
		}
	}

	// Use Changed() to properly handle P0 (priority=0)
	if cmd.Flags().Changed("priority") {
		priority, _ := cmd.Flags().GetInt("priority")
		in.Priority = &priority
	}

	// Metadata filters (GH#1406)
	metadataFieldFlags, _ := cmd.Flags().GetStringArray("metadata-field")
	if len(metadataFieldFlags) > 0 {
		in.MetadataFields = make(map[string]string, len(metadataFieldFlags))
		for _, mf := range metadataFieldFlags {
			k, v, ok := strings.Cut(mf, "=")
			if !ok || k == "" {
				return in, HandleErrorRespectJSON("invalid --metadata-field: expected key=value, got %q", mf)
			}
			if err := storage.ValidateMetadataKey(k); err != nil {
				return in, HandleErrorRespectJSON("invalid --metadata-field key: %v", err)
			}
			in.MetadataFields[k] = v
		}
	}
	if k, _ := cmd.Flags().GetString("has-metadata-key"); k != "" {
		if err := storage.ValidateMetadataKey(k); err != nil {
			return in, HandleErrorRespectJSON("invalid --has-metadata-key: %v", err)
		}
		in.HasMetadataKey = k
	}

	// `bd ready` builds the filter rather than calling IssueReader(): its
	// routes hand this filter to --claim, --gated, --explain and --mol, none
	// of which the Reader role expresses, and they stamp the --max-rows cap
	// onto it below. Routing only the plain listing through the role would
	// fork the command in two.
	//
	// CONSTRUCTION is shared with the role and always was: this is the same
	// builder, over the same issueops.ReadyRequest, that both Reader
	// implementations call, pinned by the builder's golden file.
	//
	// EXECUTION is shared on the proxied route, which runs the same
	// workapi.FinishPage the role's two implementations run. It is NOT shared
	// on the direct route, and that is a real and remaining difference: the
	// role answers "did the limit hide anything" by fetching one row past the
	// page, while `bd ready --json` answers the strictly larger question "how
	// many were hidden" with a second counting query and reports the total in
	// its pagination meta. That total is the CLI's published output, so the
	// two epilogues cannot be collapsed without changing one surface's answer.
	// See issueops.Reader's doc comment.
	filter, err := workapi.BuildReadyFilter(in.ReadyRequest)
	if err != nil {
		return in, HandleErrorRespectJSON("%v", err)
	}
	// The cap is deliberately NOT a field on ReadyRequest: it is a local
	// defense against a runaway query on this machine, not a property of the
	// question being asked, and a server has no business honoring a client's.
	filter.MaxRows = maxRows
	filter.MaxRowsSource = maxRowsSource

	// Directory-aware label scoping (GH#541). It is applied to the built
	// filter rather than passed in as a parameter for two reasons: it is
	// derived from the client's cwd, which a server process does not share,
	// and `bd ready` has always put the configured label on the filter
	// verbatim. Routing it through issueops.ReadyRequest would run it through
	// NormalizeLabels, so a directory.labels value with stray whitespace
	// would start matching a different label than it does today.
	//
	// The emptiness test reads the filter's label sets, which BuildReadyFilter
	// has already normalized: `--label "  "` is no label at all and must not
	// suppress the default.
	if len(filter.Labels) == 0 && len(filter.LabelsAny) == 0 {
		if dirLabels := config.GetDirectoryLabels(); len(dirLabels) > 0 {
			filter.LabelsAny = dirLabels
			// Recorded rather than recomputed downstream: the line above has
			// just made the filter's label sets non-empty, so re-running the
			// emptiness test would read its own output.
			in.dirLabels = dirLabels
		}
	}
	in.filter = filter

	return in, nil
}

// readyRoleRequest is the request `bd ready` hands the two roles that take a
// whole ready question rather than a filter: ReadyClaimer, which claims one
// row out of it, and ReadyCounter, which sizes it. Both routes of both
// operations build it here, so a claim, a count and the listing beside them
// ask ONE question no matter which door they came through.
//
// The two things the filter carries that the request does not:
//
//   - The --max-rows cap. It never applied to either role. A claim consumes
//     one row however large the pool it scanned (ClaimReadyIssueInTx clears
//     MaxRows, MaxRowsSource and Limit before it scans, because a cap sized
//     for bulk reads must not make a single-row claim report an empty front),
//     and a count materializes no rows at all.
//
//   - The directory-label default (GH#541). The listing puts it on the built
//     filter verbatim; here it goes on the request, where it is normalized
//     along with every other label. That differs only for a directory.labels
//     value carrying stray whitespace.
//
//     IT IS READ OFF readyInput.dirLabels, NEVER RECOMPUTED FROM in.filter.
//     The gatherer has already written the default INTO that filter, so
//     re-running its emptiness test here sees a non-empty label set and skips.
//     That is what made `bd ready --claim` in a scoped directory claim from
//     the WHOLE ready front while `bd ready` beside it listed only the
//     configured scope — the one thing ReadyClaimer's contract says a claim
//     must never do (issueops/readyclaimer.go:9-14).
//
// Limit and Offset are ErrValidation on BOTH roles, so they are cleared here
// once. Neither was ever live for the claim; for the count they are exactly
// what has to go — the page is the thing being sized.
func readyRoleRequest(in readyInput) issueops.ReadyRequest {
	req := in.ReadyRequest
	req.Limit, req.Offset = nil, 0
	if len(in.dirLabels) > 0 {
		req.LabelsAny = in.dirLabels
	}
	return req
}

// claimNextRequest is `bd ready --claim`'s request to the ReadyClaimer role:
// the shared ready question above, plus the claimant.
func claimNextRequest(in readyInput) issueops.ClaimNextRequest {
	return issueops.ClaimNextRequest{Actor: actor, Filter: readyRoleRequest(in)}
}

// briefModeConflict reports the usage error for a --brief combination that no
// route can honor, and nil when there is none.
//
// THE PROJECTION IS REFUSED WHEREVER IT CANNOT BE HONORED, rather than accepted
// and dropped. types.WorkFilter.Lite reaches the driver through one query, the
// counts mega-query behind GetReadyWorkWithCounts, and `bd ready` only runs
// that query for --json. Everything else reaches a different one: the text
// routes call the bare GetReadyWork, and --gated, --mol and --explain answer
// with shapes of their own. A flag those routes take and ignore is exactly the
// silent no-op this feature exists to close, so each combination says so.
//
// --claim is refused for a stronger reason than the rest: it cannot be served
// on ANY route. ClaimNext refetches its winning row whole and returns
// ErrValidation for a projected request (issueops.ValidateClaimNextRequest).
// Refusing here keeps the message about the two flags the user typed, and
// leaves readyRoleRequest free to carry Brief for the COUNT, which needs it.
//
// IT IS ONE BODY WITH TWO CALLERS, and that is the point. readyCmd's RunE
// dispatches --gated, --mol and --explain before gathering, so a copy of these
// checks written into each of those branches would guard the direct route only,
// with nothing driving it: the gatherer's tests reach the proxied route alone,
// and deleting one of the copies would re-open the silent no-op while every
// test stayed green. RunE calls this once, above the dispatch, and the gatherer
// calls it for every other arrival.
func briefModeConflict(brief, claim, gated, explain bool, molID string, jsonOut bool) error {
	if !brief {
		return nil
	}
	switch {
	case claim:
		return HandleErrorRespectJSON("--claim cannot be combined with --brief")
	case gated:
		return HandleErrorRespectJSON("--gated cannot be combined with --brief")
	case molID != "":
		return HandleErrorRespectJSON("--mol cannot be combined with --brief")
	case explain:
		return HandleErrorRespectJSON("--explain cannot be combined with --brief")
	case !jsonOut:
		return HandleErrorRespectJSON("--brief requires --json; the text renderings print none of the fields it omits")
	}
	return nil
}

// briefModeConflictFromFlags is readyCmd's entry to the check above, reading the
// mode flags off the command because it runs before gatherReadyInput has.
func briefModeConflictFromFlags(cmd *cobra.Command) error {
	brief, _ := cmd.Flags().GetBool("brief")
	claim, _ := cmd.Flags().GetBool("claim")
	gated, _ := cmd.Flags().GetBool("gated")
	explain, _ := cmd.Flags().GetBool("explain")
	molID, _ := cmd.Flags().GetString("mol")
	return briefModeConflict(brief, claim, gated, explain, molID, jsonOutput)
}
