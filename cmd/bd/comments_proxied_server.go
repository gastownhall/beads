package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/uimd"
)

func runCommentsProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	localTime, _ := cmd.Flags().GetBool("local-time")
	issueID := args[0]

	uw, err := proxiedOpenReadUOW(ctx)
	if err != nil {
		return err
	}
	defer uw.Close(ctx)

	issue, isWisp, err := proxiedGetIssueOrWisp(ctx, uw, issueID)
	if err != nil {
		return HandleErrorRespectJSON("resolving %s: %v", issueID, err)
	}
	if issue == nil {
		return HandleErrorRespectJSON("issue %s not found", issueID)
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
		commentText = args[1]
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

type addCommentProxiedResult struct {
	comment *types.Comment
	issue   *types.Issue
}

func addCommentProxied(ctx context.Context, id, author, text string) (*types.Comment, *types.Issue, error) {
	if uowProvider == nil {
		return nil, nil, HandleError("proxied-server UOW provider not initialized")
	}
	res, err := uow.RunTxResult(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (addCommentProxiedResult, string, error) {
		issue, isWisp := proxiedResolveIssueOrWisp(ctx, uw, id)
		if issue == nil {
			return addCommentProxiedResult{}, "", fmt.Errorf("issue %s not found", id)
		}
		if err := validateIssueUpdatable(id, issue); err != nil {
			return addCommentProxiedResult{}, "", err
		}
		var (
			comment *types.Comment
			cerr    error
		)
		if isWisp {
			comment, cerr = uw.CommentUseCase().AddCommentToWisp(ctx, issue.ID, author, text)
		} else {
			comment, cerr = uw.CommentUseCase().AddCommentToIssue(ctx, issue.ID, author, text)
		}
		if cerr != nil {
			return addCommentProxiedResult{}, "", fmt.Errorf("adding comment: %w", cerr)
		}
		return addCommentProxiedResult{comment: comment, issue: issue}, fmt.Sprintf("bd: comment %s", issue.ID), nil
	})
	if err != nil {
		return nil, nil, err
	}
	return res.comment, res.issue, nil
}
