package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/uimd"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

func runCommentsProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	localTime, _ := cmd.Flags().GetBool("local-time")
	issueID := args[0]

	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	issue, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), issueID)
	if errors.Is(err, storage.ErrNotFound) {
		return HandleErrorRespectJSON("issue %s not found", issueID)
	}
	if err != nil {
		return HandleErrorRespectJSON("resolving %s: %v", issueID, err)
	}
	issueID = issue.ID

	comments, err := proxiedGetComments(ctx, uw, issueID, isWisp)
	if err != nil {
		return HandleErrorRespectJSON("getting comments: %v", err)
	}
	if comments == nil {
		comments = make([]*types.Comment, 0)
	}

	if jsonOutput {
		return outputJSON(comments)
	}

	if len(comments) == 0 {
		fmt.Printf("No comments on %s\n", issueID)
		return nil
	}

	fmt.Printf("\nComments on %s:\n\n", issueID)
	for _, comment := range comments {
		ts := comment.CreatedAt
		if localTime {
			ts = ts.Local()
		}
		fmt.Printf("[%s] at %s\n", comment.Author, ts.Format("2006-01-02 15:04"))
		rendered := uimd.RenderMarkdown(comment.Text)
		for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println()
	}
	return nil
}

func runCommentProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	id := args[0]
	textArgs := args[1:]

	stdinFlag, _ := cmd.Flags().GetBool("stdin")
	fileFlag, _ := cmd.Flags().GetString("file")

	var commentText string
	switch {
	case stdinFlag:
		content, err := io.ReadAll(os.Stdin)
		if err != nil {
			return HandleErrorRespectJSON("reading from stdin: %v", err)
		}
		commentText = strings.TrimRight(string(content), "\n")
	case fileFlag != "":
		content, err := readBodyFile(fileFlag)
		if err != nil {
			return HandleErrorRespectJSON("reading file: %v", err)
		}
		commentText = content
	case len(textArgs) > 0:
		commentText = strings.Join(textArgs, " ")
	default:
		return HandleErrorRespectJSON("no comment text provided (use positional args, --stdin, or --file)")
	}

	if strings.TrimSpace(commentText) == "" {
		return HandleErrorRespectJSON("comment text cannot be empty")
	}

	author := getActorWithGit()

	comment, issue, err := addCommentProxied(ctx, id, author, commentText)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	SetLastTouchedID(issue.ID)

	if jsonOutput {
		return outputJSON(comment)
	}
	fmt.Printf("%s Comment added to %s\n", ui.RenderPass("✓"), formatFeedbackID(issue.ID, issue.Title))
	return nil
}

func runCommentsAddProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	issueID := args[0]

	commentText, _ := cmd.Flags().GetString("file")
	if commentText != "" {
		data, err := os.ReadFile(commentText) // #nosec G304 - user-provided file path is intentional
		if err != nil {
			return HandleErrorRespectJSON("reading file: %v", err)
		}
		commentText = string(data)
	} else if len(args) < 2 {
		return HandleErrorRespectJSON("comment text required (use -f to read from file)")
	} else {
		commentText = strings.Join(args[1:], " ")
	}

	if strings.TrimSpace(commentText) == "" {
		return HandleErrorRespectJSON("comment text cannot be empty")
	}

	author, _ := cmd.Flags().GetString("author")
	if author == "" {
		author = getActorWithGit()
	}

	comment, issue, err := addCommentProxied(ctx, issueID, author, commentText)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if jsonOutput {
		return outputJSON(comment)
	}
	fmt.Printf("Comment added to %s\n", issue.ID)
	return nil
}

// proxiedCommenter hands back the guarded add-comment surface for the
// proxied-server provider, through the provider's OWN capability accessor —
// the same two-step proxiedIssueReader performs, and for the same reason: the
// accessor is where each layer is added.
func proxiedCommenter() (issueops.Commenter, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.CommenterSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the add-comment surface", uowProvider)
	}
	return src.Commenter()
}

// addCommentProxied appends one comment through the Commenter role and returns
// it with the issue it landed on.
//
// The RESOLVE stays here, in a read-only pre-flight, for the reason the
// proxied close keeps its own policy pre-flight: `bd comment` refuses a
// template, and refusing a template is not library policy. The pre-flight is
// also where the TITLE comes from — the confirmation line prints it, and a
// result type carrying presentation for one front door is a result type that
// grows one field per front door. The role is handed the canonical id the
// pre-flight resolved, exactly as `bd show` hands Reader.Get one.
func addCommentProxied(ctx context.Context, id, author, text string) (*types.Comment, *types.Issue, error) {
	issue, err := resolveCommentTargetProxied(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	commenter, err := proxiedCommenter()
	if err != nil {
		return nil, nil, err
	}
	result, err := commenter.AddComment(ctx, issueops.AddCommentRequest{
		Author:  author,
		IssueID: issue.ID,
		Text:    text,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("adding comment: %w", err)
	}
	return result.Comment, issue, nil
}

// resolveCommentTargetProxied resolves the anchor and applies the CLI's own
// pre-flight policy. It reads in a unit of work of its own and writes nothing,
// so the role's request stays the whole of the transaction.
func resolveCommentTargetProxied(ctx context.Context, id string) (*types.Issue, error) {
	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return nil, err
	}
	defer uw.Close(ctx)

	issue, _, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("issue %s not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", id, err)
	}
	if err := validateIssueUpdatable(id, issue); err != nil {
		return nil, err
	}
	return issue, nil
}
