package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

var commentsCmd = &cobra.Command{
	Use:   "comments [issue-id]",
	Short: "View or manage comments on an issue",
	Long: `View or manage comments on an issue.

Examples:
  # List all comments on an issue
  bd comments bd-123

  # List comments in JSON format
  bd comments bd-123 --json

  # Add a comment
  bd comments add bd-123 "This is a comment"

  # Add a comment from a file
  bd comments add bd-123 -f notes.txt`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		issueID := args[0]

		var comments []*types.Comment
		usedDaemon := false
		if daemonClient != nil {
			resp, err := daemonClient.ListComments(&rpc.CommentListArgs{ID: issueID})
			if err != nil {
				if isUnknownOperationError(err) {
					if err := fallbackToDirectMode("daemon does not support comment_list RPC"); err != nil {
						fmt.Fprintf(os.Stderr, "Error getting comments: %v\n", err)
						os.Exit(1)
					}
				} else {
					fmt.Fprintf(os.Stderr, "Error getting comments: %v\n", err)
					os.Exit(1)
				}
			} else {
				if err := json.Unmarshal(resp.Data, &comments); err != nil {
					fmt.Fprintf(os.Stderr, "Error decoding comments: %v\n", err)
					os.Exit(1)
				}
				usedDaemon = true
			}
		}

		if !usedDaemon {
			if err := ensureStoreActive(); err != nil {
				fmt.Fprintf(os.Stderr, "Error getting comments: %v\n", err)
				os.Exit(1)
			}
			ctx := context.Background()
			result, err := store.GetIssueComments(ctx, issueID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting comments: %v\n", err)
				os.Exit(1)
			}
			comments = result
		}

		if jsonOutput {
			data, err := json.MarshalIndent(comments, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
				os.Exit(1)
			}
			fmt.Println(string(data))
			return
		}

		// Human-readable output
		if len(comments) == 0 {
			fmt.Printf("No comments on %s\n", issueID)
			return
		}

		fmt.Printf("\nComments on %s:\n\n", issueID)
		for _, comment := range comments {
			fmt.Printf("[%s] %s at %s\n", comment.Author, comment.Text, comment.CreatedAt.Format("2006-01-02 15:04"))
			fmt.Println()
		}
	},
}

var commentsAddCmd = &cobra.Command{
	Use:   "add [issue-id] [text]",
	Short: "Add a comment to an issue",
	Long: `Add a comment to an issue.

Examples:
  # Add a comment
  bd comments add bd-123 "Working on this now"

  # Add a comment from a file
  bd comments add bd-123 -f notes.txt`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		issueID := args[0]

		// Get comment text from flag or argument
		commentText, _ := cmd.Flags().GetString("file")
		if commentText != "" {
			// Read from file
			data, err := os.ReadFile(commentText) // #nosec G304 - user-provided file path is intentional
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
				os.Exit(1)
			}
			commentText = string(data)
		} else if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Error: comment text required (use -f to read from file)\n")
			os.Exit(1)
		} else {
			commentText = args[1]
		}

		// Get author from author flag, BD_ACTOR var, or system USER var
		author, _ := cmd.Flags().GetString("author")
		if author == "" {
			author = os.Getenv("BD_ACTOR")
			if author == "" {
				author = os.Getenv("USER")
			}
			if author == "" {
				if u, err := user.Current(); err == nil {
					author = u.Username
				} else {
					author = "unknown"
				}
			}
		}

		var comment *types.Comment
		if daemonClient != nil {
			resp, err := daemonClient.AddComment(&rpc.CommentAddArgs{
				ID:     issueID,
				Author: author,
				Text:   commentText,
			})
			if err != nil {
				if isUnknownOperationError(err) {
					if err := fallbackToDirectMode("daemon does not support comment_add RPC"); err != nil {
						fmt.Fprintf(os.Stderr, "Error adding comment: %v\n", err)
						os.Exit(1)
					}
				} else {
					fmt.Fprintf(os.Stderr, "Error adding comment: %v\n", err)
					os.Exit(1)
				}
			} else {
				var parsed types.Comment
				if err := json.Unmarshal(resp.Data, &parsed); err != nil {
					fmt.Fprintf(os.Stderr, "Error decoding comment: %v\n", err)
					os.Exit(1)
				}
				comment = &parsed
			}
		}

		if comment == nil {
			if err := ensureStoreActive(); err != nil {
				fmt.Fprintf(os.Stderr, "Error adding comment: %v\n", err)
				os.Exit(1)
			}
			ctx := context.Background()
			var err error
			comment, err = store.AddIssueComment(ctx, issueID, author, commentText)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error adding comment: %v\n", err)
				os.Exit(1)
			}
		}

		if jsonOutput {
			data, err := json.MarshalIndent(comment, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
				os.Exit(1)
			}
			fmt.Println(string(data))
			return
		}

		fmt.Printf("Comment added to %s\n", issueID)
	},
}

func init() {
	commentsCmd.AddCommand(commentsAddCmd)
	commentsAddCmd.Flags().StringP("file", "f", "", "Read comment text from file")
	commentsAddCmd.Flags().StringP("author", "a", "", "Add author to comment")
	rootCmd.AddCommand(commentsCmd)
}

func isUnknownOperationError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "unknown operation")
}
