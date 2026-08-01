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
		}
	}
	in.filter = filter

	return in, nil
}
