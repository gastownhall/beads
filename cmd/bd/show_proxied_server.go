package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/uimd"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

type showProxiedInput struct {
	ids             []string
	thread          bool
	shortMode       bool
	longMode        bool
	refs            bool
	children        bool
	asOfRef         string
	localTime       bool
	watchMode       bool
	currentMode     bool
	includeDepends  bool
	briefDeps       bool
	includeComments bool
}

func gatherShowProxiedInput(cmd *cobra.Command, args []string) *showProxiedInput {
	in := &showProxiedInput{}
	in.thread, _ = cmd.Flags().GetBool("thread")
	in.shortMode, _ = cmd.Flags().GetBool("short")
	in.longMode, _ = cmd.Flags().GetBool("long")
	in.refs, _ = cmd.Flags().GetBool("refs")
	in.children, _ = cmd.Flags().GetBool("children")
	in.asOfRef, _ = cmd.Flags().GetString("as-of")
	in.localTime, _ = cmd.Flags().GetBool("local-time")
	in.watchMode, _ = cmd.Flags().GetBool("watch")
	in.currentMode, _ = cmd.Flags().GetBool("current")
	in.includeDepends, _ = cmd.Flags().GetBool("include-dependents")
	in.briefDeps, _ = cmd.Flags().GetBool("brief-deps")
	in.includeComments, _ = cmd.Flags().GetBool("include-comments")

	idFlags, _ := cmd.Flags().GetStringArray("id")
	in.ids = append(in.ids, args...)
	in.ids = append(in.ids, idFlags...)
	return in
}

func proxiedOpenReadUOW(ctx context.Context) (uow.UnitOfWork, error) {
	if uowProvider == nil {
		return nil, HandleError("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return nil, HandleErrorRespectJSON("open unit of work: %v", err)
	}
	return uw, nil
}

// proxiedIssueReader hands back the guarded issue-query surface for the
// proxied-server provider, through the provider's OWN capability accessor —
// the same two-step a direct command performs on a store.
//
// The accessor is the door and there is no other: the cmd-bd-role-constructors
// depguard rule keeps the shared implementation's constructor out of cmd/bd
// entirely, because a decorator adds its layer in its own accessor and a
// command that built a reader directly would get an undecorated one. A
// provider that cannot answer says so with an error rather than being wired
// around.
func proxiedIssueReader() (issueops.Reader, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.IssueReaderSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the issue-query surface", uowProvider)
	}
	return src.IssueReader()
}

func runShowProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	in := gatherShowProxiedInput(cmd, args)

	if in.watchMode {
		return HandleErrorRespectJSON("watch mode not supported in proxied-server mode")
	}

	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	if in.currentMode {
		if len(in.ids) > 0 {
			return HandleErrorRespectJSON("--current cannot be combined with explicit issue IDs")
		}
		currentID := resolveCurrentIssueIDProxied(ctx, uw)
		if currentID == "" {
			return HandleErrorRespectJSON("no current issue found (no in-progress, hooked, or recently touched issues)")
		}
		in.ids = []string{currentID}
	}

	if len(in.ids) == 0 {
		return HandleErrorRespectJSON("at least one issue ID is required (use positional args, --id flag, or --current)")
	}

	switch {
	case in.asOfRef != "":
		runShowProxiedAsOf(ctx, uw, in)
	case in.thread:
		return runShowProxiedThread(ctx, uw, in)
	case in.refs:
		runShowProxiedRefs(ctx, uw, in)
	case in.children:
		runShowProxiedChildren(ctx, uw, in)
	default:
		return runShowProxiedDefault(ctx, uw, in)
	}
	return nil
}

// proxiedListDeps and proxiedGetComments stay CLI-local: they feed the
// terminal rendering below, which is presentation, not the shared detail
// shape. The domain-shaped reads live in internal/workapi.
func proxiedListDeps(ctx context.Context, uw uow.UnitOfWork, id string, isWisp bool, filter domain.DepListFilter) ([]*types.IssueWithDependencyMetadata, error) {
	if isWisp {
		return uw.DependencyUseCase().ListWispWithIssueMetadata(ctx, id, filter)
	}
	return uw.DependencyUseCase().ListWithIssueMetadata(ctx, id, filter)
}

func proxiedGetComments(ctx context.Context, uw uow.UnitOfWork, id string, isWisp bool) ([]*types.Comment, error) {
	if isWisp {
		return uw.CommentUseCase().GetCommentsForWisp(ctx, id)
	}
	return uw.CommentUseCase().GetCommentsForIssue(ctx, id)
}

// reportIssueLookupFailure prints the stderr line for a failed issue lookup,
// keeping "no such issue" distinct from a backend that fell over. Before the
// lookup normalized its sentinel, proxied mode printed the raw
// "sql: no rows in result set" for a missing id and had no way to tell the
// two apart at all.
func reportIssueLookupFailure(verb, id string, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
		fmt.Fprintf(os.Stderr, "Hint: %s\n", showNotFoundHint(id))
		return
	}
	fmt.Fprintf(os.Stderr, "Error %s %s: %v\n", verb, id, err)
}

func runShowProxiedAsOf(ctx context.Context, uw uow.UnitOfWork, in *showProxiedInput) {
	var jsonIssues []*types.Issue
	for idx, id := range in.ids {
		issue, err := uw.IssueUseCase().AsOf(ctx, id, in.asOfRef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching %s as of %s: %v\n", id, in.asOfRef, err)
			continue
		}
		if issue == nil {
			fmt.Fprintf(os.Stderr, "Issue %s did not exist at %s\n", id, in.asOfRef)
			continue
		}

		if in.shortMode {
			fmt.Println(formatShortIssue(issue))
			continue
		}
		if jsonOutput {
			jsonIssues = append(jsonIssues, issue)
			continue
		}

		if idx > 0 {
			fmt.Println("\n" + ui.RenderMuted(strings.Repeat("-", 60)))
		}
		fmt.Printf("\n%s (as of %s)\n", formatIssueHeader(issue), ui.RenderMuted(in.asOfRef))
		fmt.Println(formatIssueMetadata(issue))
		if issue.Description != "" {
			fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), uimd.RenderMarkdown(issue.Description))
		}
		fmt.Println()
	}
	if jsonOutput && len(jsonIssues) > 0 {
		_ = outputJSON(jsonIssues)
	}
}

func runShowProxiedRefs(ctx context.Context, uw uow.UnitOfWork, in *showProxiedInput) {
	src := workapi.NewUOWDetailSource(uw)
	allRefs := make(map[string][]*types.IssueWithDependencyMetadata, len(in.ids))
	for _, id := range in.ids {
		_, isWisp, err := workapi.GetIssueOrWisp(ctx, src, id)
		if err != nil {
			reportIssueLookupFailure("resolving", id, err)
			continue
		}
		refs, err := proxiedListDeps(ctx, uw, id, isWisp, domain.DepListFilter{Direction: domain.DepDirectionIn})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting refs for %s: %v\n", id, err)
			continue
		}
		allRefs[id] = refs
	}

	if jsonOutput {
		_ = outputJSON(allRefs)
		return
	}
	for id, refs := range allRefs {
		if len(refs) == 0 {
			fmt.Printf("\n%s: No references found\n", ui.RenderAccent(id))
			continue
		}
		fmt.Printf("\n%s References to %s:\n", ui.RenderAccent("📎"), id)
		for _, sec := range groupDepSections(refs, false, nil) {
			displayRefGroup(sec)
		}
		fmt.Println()
	}
}

func runShowProxiedChildren(ctx context.Context, uw uow.UnitOfWork, in *showProxiedInput) {
	src := workapi.NewUOWDetailSource(uw)
	allChildren := make(map[string][]*types.IssueWithDependencyMetadata, len(in.ids))
	for _, id := range in.ids {
		_, isWisp, err := workapi.GetIssueOrWisp(ctx, src, id)
		if err != nil {
			reportIssueLookupFailure("resolving", id, err)
			continue
		}
		kids, err := proxiedListDeps(ctx, uw, id, isWisp, domain.DepListFilter{
			Types:     []types.DependencyType{types.DepParentChild},
			Direction: domain.DepDirectionIn,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting children for %s: %v\n", id, err)
			continue
		}
		if kids == nil {
			kids = []*types.IssueWithDependencyMetadata{}
		}
		allChildren[id] = kids
	}

	if jsonOutput {
		_ = outputJSON(allChildren)
		return
	}
	for id, kids := range allChildren {
		if len(kids) == 0 {
			fmt.Printf("%s: No children found\n", ui.RenderAccent(id))
			continue
		}
		fmt.Printf("%s Children of %s (%d):\n", ui.RenderAccent("↳"), id, len(kids))
		for _, c := range kids {
			if in.shortMode {
				fmt.Printf("  %s\n", formatShortIssue(&c.Issue))
			} else {
				fmt.Println(formatDependencyLine("↳", c))
			}
		}
		fmt.Println()
	}
}

func runShowProxiedThread(ctx context.Context, uw uow.UnitOfWork, in *showProxiedInput) error {
	if len(in.ids) == 0 {
		return nil
	}
	startMsg, _, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), in.ids[0])
	if errors.Is(err, storage.ErrNotFound) {
		return HandleErrorRespectJSON("message %s not found", in.ids[0])
	}
	if err != nil {
		return HandleErrorRespectJSON("fetching message %s: %v", in.ids[0], err)
	}

	rootMsg := startMsg
	seen := map[string]bool{rootMsg.ID: true}
	for {
		parentID := proxiedFindRepliesTo(ctx, uw, rootMsg.ID)
		if parentID == "" || seen[parentID] {
			break
		}
		seen[parentID] = true
		parentMsg, _ := uw.IssueUseCase().GetIssue(ctx, parentID)
		if parentMsg == nil {
			parentMsg, _ = uw.IssueUseCase().GetWisp(ctx, parentID)
		}
		if parentMsg == nil {
			break
		}
		rootMsg = parentMsg
	}

	threadMessages := []*types.Issue{rootMsg}
	threadIDs := map[string]bool{rootMsg.ID: true}
	repliesTo := map[string]string{}
	queue := []string{rootMsg.ID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		replies := proxiedFindReplies(ctx, uw, current)
		for _, reply := range replies {
			if threadIDs[reply.ID] {
				continue
			}
			r := reply
			threadMessages = append(threadMessages, &r)
			threadIDs[reply.ID] = true
			repliesTo[reply.ID] = current
			queue = append(queue, reply.ID)
		}
	}

	slices.SortFunc(threadMessages, func(a, b *types.Issue) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(threadMessages)
		return nil
	}

	fmt.Printf("\n%s Thread: %s\n", ui.RenderAccent("📬"), rootMsg.Title)
	fmt.Println(strings.Repeat("─", 66))
	for _, msg := range threadMessages {
		depth := 0
		parent := repliesTo[msg.ID]
		for parent != "" && depth < 5 {
			depth++
			parent = repliesTo[parent]
		}
		indent := strings.Repeat("  ", depth)
		timeStr := msg.CreatedAt.Format("2006-01-02 15:04")
		statusIcon := "📧"
		if msg.Status == types.StatusClosed {
			statusIcon = "✓"
		}
		fmt.Printf("%s%s %s %s\n", indent, statusIcon, ui.RenderAccent(msg.ID), ui.RenderMuted(timeStr))
		fmt.Printf("%s  From: %s  To: %s\n", indent, msg.Sender, msg.Assignee)
		if parentID := repliesTo[msg.ID]; parentID != "" {
			fmt.Printf("%s  Re: %s\n", indent, parentID)
		}
		fmt.Printf("%s  %s: %s\n", indent, ui.RenderMuted("Subject"), msg.Title)
		if msg.Description != "" {
			for _, line := range strings.Split(msg.Description, "\n") {
				fmt.Printf("%s  %s\n", indent, line)
			}
		}
		fmt.Println()
	}
	fmt.Printf("Total: %d messages in thread\n\n", len(threadMessages))
	return nil
}

func proxiedFindRepliesTo(ctx context.Context, uw uow.UnitOfWork, id string) string {
	deps, err := uw.DependencyUseCase().ListWithIssueMetadata(ctx, id, domain.DepListFilter{
		Types:     []types.DependencyType{types.DepRepliesTo},
		Direction: domain.DepDirectionOut,
	})
	if err != nil || len(deps) == 0 {
		return ""
	}
	return deps[0].ID
}

func proxiedFindReplies(ctx context.Context, uw uow.UnitOfWork, id string) []types.Issue {
	deps, err := uw.DependencyUseCase().ListWithIssueMetadata(ctx, id, domain.DepListFilter{
		Types:     []types.DependencyType{types.DepRepliesTo},
		Direction: domain.DepDirectionIn,
	})
	if err != nil {
		return nil
	}
	out := make([]types.Issue, 0, len(deps))
	for _, d := range deps {
		out = append(out, d.Issue)
	}
	return out
}

func runShowProxiedDefault(ctx context.Context, uw uow.UnitOfWork, in *showProxiedInput) error {
	formatTime := func(t time.Time) string {
		if in.localTime {
			t = t.Local()
		}
		return t.Format("2006-01-02 15:04")
	}

	// Shaping the detail view belongs to the reader role, not the CLI, and
	// this route reaches it the same way the direct one does: through the
	// provider's own accessor. The count-only default (be-ijck6q), the
	// comments-omitted flag (ga-clgh) and the shallow dependent rows
	// (be-4d36f2) then read the same from every frontend by construction
	// rather than by everyone remembering to call the same helper. The
	// role opens one unit of work per call; the one this function holds stays
	// for the terminal rendering below, which is not on the contract.
	var rd issueops.Reader
	if jsonOutput && !in.shortMode {
		var rerr error
		if rd, rerr = proxiedIssueReader(); rerr != nil {
			return HandleErrorRespectJSON("%v", rerr)
		}
	}

	src := workapi.NewUOWDetailSource(uw)
	var allDetails []interface{}
	foundCount := 0
	for idx, id := range in.ids {
		if rd != nil {
			details, derr := rd.Get(ctx, in.getRequest(id))
			if derr != nil {
				if errors.Is(derr, storage.ErrNotFound) {
					// The corpus pins this pair for a missing id: the human
					// line here, and the envelope below once the batch ends
					// with nothing to emit.
					reportIssueLookupFailure("fetching", id, derr)
					continue
				}
				// A BACKEND failure, which the split this replaced reported
				// per-id and carried on from when it surfaced during
				// resolution, and aborted on when it surfaced one call later
				// during assembly. One call means one answer, and abort is
				// the one to keep: a JSON array missing the rows a database
				// error swallowed is indistinguishable from a complete one.
				return HandleErrorRespectJSON("%v", derr)
			}
			foundCount++
			allDetails = append(allDetails, details)
			continue
		}

		issue, isWisp, err := workapi.GetIssueOrWisp(ctx, src, id)
		if err != nil {
			reportIssueLookupFailure("fetching", id, err)
			continue
		}
		foundCount++

		if in.shortMode {
			fmt.Println(formatShortIssue(issue))
			continue
		}

		proxiedRenderIssue(ctx, uw, issue, isWisp, in, idx, formatTime)
	}

	if jsonOutput {
		if len(allDetails) > 0 {
			_ = outputJSON(allDetails)
		} else {
			return HandleErrorWithHintRespectJSON("no issues found matching the provided IDs",
				"some IDs may reference deleted/purged records with no trace left in the live database — try 'bd history <id>' to check")
		}
	} else if foundCount == 0 {
		return SilentExit()
	}
	return nil
}

func proxiedRenderIssue(ctx context.Context, uw uow.UnitOfWork, issue *types.Issue, isWisp bool, in *showProxiedInput, idx int, formatTime func(time.Time) string) {
	if idx > 0 {
		fmt.Println("\n" + ui.RenderMuted(strings.Repeat("─", 60)))
		fmt.Printf("\n%s\n", formatIssueHeader(issue))
	} else {
		fmt.Printf("%s\n", formatIssueHeader(issue))
	}
	fmt.Println(formatIssueMetadata(issue))

	if issue.Description != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), uimd.RenderMarkdown(issue.Description))
	} else {
		fmt.Printf("\n%s\n  %s\n", ui.RenderBold("DESCRIPTION"), ui.RenderMuted("(none)"))
	}
	if issue.Design != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESIGN"), uimd.RenderMarkdown(issue.Design))
	}
	if issue.Notes != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("NOTES"), uimd.RenderMarkdown(issue.Notes))
	}
	if issue.AcceptanceCriteria != "" {
		fmt.Printf("\n%s\n%s\n", ui.RenderBold("ACCEPTANCE CRITERIA"), uimd.RenderMarkdown(issue.AcceptanceCriteria))
	}

	// A READ on an ALTERNATE view. `bd show`'s detail view is on
	// issueops.Reader on both routes and gets its labels hydrated there; this
	// renderer serves --refs, --children, --thread and --as-of, which answer
	// with shapes the Reader contract does not describe, from a unit of work
	// the caller already holds and has already read the issue from. Asking the
	// role here would open a second transaction to re-fetch a row this function
	// was handed. Alternate views reaching roles of their own is the follow-up
	// (ga-2ltro.12).
	var labels []string
	if isWisp {
		labels, _ = uw.LabelUseCase().GetWispLabels(ctx, issue.ID) //nolint:forbidigo // alternate view, caller-owned UOW; the detail view is on the role
	} else {
		labels, _ = uw.LabelUseCase().GetLabels(ctx, issue.ID) //nolint:forbidigo // alternate view, caller-owned UOW; the detail view is on the role
	}
	if len(labels) > 0 {
		fmt.Printf("\n%s %s\n", ui.RenderBold("LABELS:"), strings.Join(labels, ", "))
	}

	if metaStr := formatIssueCustomMetadata(issue); metaStr != "" {
		fmt.Printf("\n%s\n", metaStr)
	}

	relatedSeen := make(map[string]*types.IssueWithDependencyMetadata)

	depsWithMeta, _ := proxiedListDeps(ctx, uw, issue.ID, isWisp, domain.DepListFilter{Direction: domain.DepDirectionOut})
	for _, sec := range groupDepSections(depsWithMeta, true, relatedSeen) {
		printDepSection(sec)
	}

	dependentsWithMeta, _ := proxiedListDeps(ctx, uw, issue.ID, isWisp, domain.DepListFilter{Direction: domain.DepDirectionIn})
	for _, sec := range groupDepSections(dependentsWithMeta, false, relatedSeen) {
		printDepSection(sec)
		if sec.Type == types.DepParentChild && issue.IssueType == types.TypeEpic {
			printEpicChildProgress(sec.Deps)
		}
	}

	printRelatedSection(relatedSeen)

	comments, _ := proxiedGetComments(ctx, uw, issue.ID, isWisp)
	if len(comments) > 0 {
		fmt.Printf("\n%s\n", ui.RenderBold("COMMENTS"))
		for _, c := range comments {
			fmt.Printf("  %s %s\n", ui.RenderMuted(formatTime(c.CreatedAt)), c.Author)
			rendered := uimd.RenderMarkdown(c.Text)
			for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}

	if in.longMode {
		fmt.Print(formatIssueLongExtras(issue, formatTime))
	}

	fmt.Println()
}

// getRequest carries the proxied show flags onto the read contract. See
// showGetRequest: the two routes build this independently.
func (in *showProxiedInput) getRequest(id string) issueops.GetRequest {
	return showGetRequest(id, in.includeDepends, in.includeComments, in.briefDeps)
}
