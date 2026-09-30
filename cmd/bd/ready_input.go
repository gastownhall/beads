package main

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// readyInput is everything `bd ready` parsed off the command line: the
// frontend-independent query (issueops.ReadyListRequest — the ready question
// plus the client's --max-rows cap, which is exactly what the ReadyLister role
// takes) and the mode and presentation choices that never leave the CLI. Both
// routes hand the request to a ReadyLister — the store's on the direct route,
// the provider's on the proxied one — and neither builds a filter of its own.
type readyInput struct {
	issueops.ReadyListRequest

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
	if flat, _ := cmd.Flags().GetBool("flat"); flat {
		in.plainFormat = true
	}
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

	// The cap rides the LISTING request, not ReadyRequest: it is a local
	// defense against a runaway query on this machine, not a property of the
	// question being asked, so a claim and a count over the same question
	// never see it (see readyRoleRequest).
	in.MaxRows = maxRows
	in.MaxRowsSource = maxRowsSource

	// Directory-aware label scoping (GH#541) goes ON THE REQUEST, where every
	// consumer — the ReadyLister listing and its total, the ReadyClaimer
	// claim, on either route — reads it from one place. It is derived from the
	// client's cwd, which a server never shares, so it is resolved here and
	// sent as an ordinary LabelsAny.
	//
	// ON THE REQUEST MEANS NORMALIZED, which is the one visible difference
	// from the listing's old behavior: the listing used to put the configured
	// value on its filter verbatim, while the claim and the count already
	// normalized it. A directory.labels value with stray whitespace
	// ("  scope:web  ") now selects the trimmed label on the listing too, and
	// a value that is nothing BUT whitespace normalizes away to no scope at
	// all. Labels are matched exactly, so the verbatim spelling matched only a
	// label stored with that whitespace, and the listing, its "of N" and a
	// --claim beside it disagreed about which set they described.
	//
	// The emptiness test normalizes the user's own label sets first: `--label
	// "  "` is no label at all and must not suppress the default.
	if len(utils.NormalizeLabels(in.Labels)) == 0 && len(utils.NormalizeLabels(in.LabelsAny)) == 0 {
		if dirLabels := config.GetDirectoryLabels(); len(dirLabels) > 0 {
			in.LabelsAny = slices.Clone(dirLabels)
		}
	}

	// Both listings are on the ReadyLister role and build nothing; the role
	// builds its own filter from this request. It is built once here only to
	// VALIDATE the request up front, so a bad --sort is reported before any
	// mode runs — including the modes (--claim, --gated, --mol, --explain)
	// that never reach a listing.
	if _, err := workapi.BuildReadyFilter(in.ReadyRequest); err != nil {
		return in, HandleErrorRespectJSON("%v", err)
	}

	return in, nil
}

// readyRoleRequest is the whole ready question `bd ready` asks, without a
// page: what ReadyClaimer claims one row out of on both routes, and what a
// ReadyCounter would size — the number the ReadyLister's Total already is, by
// that role's documented identity, so neither route asks a counter any more.
// A claim and the listing beside it therefore ask ONE question no matter
// which door they came through.
//
// It is the listing's request minus the two things neither role takes:
//
//   - The --max-rows cap, which lives on the ReadyListRequest around this
//     ReadyRequest and so never reaches either role. A claim consumes one row
//     however large the pool it scanned (ClaimReadyIssueInTx clears MaxRows,
//     MaxRowsSource and Limit before it scans), and a count materializes no
//     rows at all.
//
//   - The page. Limit and Offset are ErrValidation on BOTH roles, so they are
//     cleared here once. Neither was ever live for the claim; for the count
//     they are exactly what has to go — the page is the thing being sized.
//
// The directory-label default (GH#541) is already ON the request — the
// gatherer puts it there — so the claim, the count and the listing ask about
// the same scoped set by construction rather than by each re-deriving it.
func readyRoleRequest(in readyInput) issueops.ReadyRequest {
	req := in.ReadyRequest
	req.Limit, req.Offset = nil, 0
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
// and dropped. The text renderings print none of the fields --brief omits, and
// --gated, --mol and --explain answer with shapes of their own. A flag those routes take and ignore is exactly the
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
