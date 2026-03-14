package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/gastownhall/beads/internal/audit"
)

var (
	auditRecordKind     string
	auditRecordModel    string
	auditRecordPrompt   string
	auditRecordResponse string
	auditRecordIssueID  string
	auditRecordToolName string
	auditRecordExitCode int
	auditRecordError    string
	auditRecordStdin    bool

	auditLabelValue  string
	auditLabelReason string
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Record and label agent interactions (append-only JSONL)",
	Long: `Audit log entries are appended to .beads/interactions.jsonl.

Each line is one event. This file is intended to be versioned in git and used for:
- auditing ("why did the agent do that?")
- dataset generation (SFT/RL fine-tuning)

Entries are append-only. Labeling creates a new "label" entry that references a parent entry.`,
}

var auditRecordCmd = &cobra.Command{
	Use:   "record",
	Short: "Append an audit interaction entry",
	Run: func(cmd *cobra.Command, _ []string) {
		var e audit.Entry

		// If stdin is piped and no explicit record fields were provided, assume stdin JSON.
		// This matches "or pipe JSON via stdin" without requiring a flag.
		fi, _ := os.Stdin.Stat() // Best effort: nil FileInfo means not a pipe, use default behavior
		stdinPiped := fi != nil && (fi.Mode()&os.ModeCharDevice) == 0
		noFieldsProvided := auditRecordKind == "" &&
			auditRecordModel == "" &&
			auditRecordPrompt == "" &&
			auditRecordResponse == "" &&
			auditRecordIssueID == "" &&
			auditRecordToolName == "" &&
			auditRecordExitCode < 0 &&
			auditRecordError == ""

		if auditRecordStdin || (stdinPiped && noFieldsProvided) {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to read stdin: %v\n", err)
				os.Exit(1)
			}
			if err := json.Unmarshal(b, &e); err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid JSON on stdin: %v\n", err)
				os.Exit(1)
			}
			// Allow --actor to override/augment stdin.
			if actor != "" {
				e.Actor = actor
			}
		} else {
			if auditRecordKind == "" {
				fmt.Fprintf(os.Stderr, "Error: --kind is required\n")
				os.Exit(1)
			}
			e = audit.Entry{
				Kind:     auditRecordKind,
				Actor:    actor,
				IssueID:  auditRecordIssueID,
				Model:    auditRecordModel,
				Prompt:   auditRecordPrompt,
				Response: auditRecordResponse,
				ToolName: auditRecordToolName,
				Error:    auditRecordError,
			}
			if auditRecordExitCode >= 0 {
				exit := auditRecordExitCode
				e.ExitCode = &exit
			}
		}

		id, err := audit.Append(&e)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(map[string]any{
				"id":   id,
				"kind": e.Kind,
			})
			return
		}

		fmt.Println(id)
	},
}

var auditLabelCmd = &cobra.Command{
	Use:   "label <entry-id>",
	Short: "Append a label entry referencing an existing interaction",
	Args:  cobra.ExactArgs(1),
	Run: func(_ *cobra.Command, args []string) {
		parentID := args[0]
		if auditLabelValue == "" {
			fmt.Fprintf(os.Stderr, "Error: --label is required\n")
			os.Exit(1)
		}
		e := audit.Entry{
			Kind:     "label",
			Actor:    actor,
			ParentID: parentID,
			Label:    auditLabelValue,
			Reason:   auditLabelReason,
		}

		id, err := audit.Append(&e)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(map[string]any{
				"id":        id,
				"parent_id": parentID,
				"label":     auditLabelValue,
			})
			return
		}

		fmt.Println(id)
	},
}

func init() {
	auditRecordCmd.Flags().StringVar(&auditRecordKind, "kind", "", "Entry kind (e.g. llm_call, tool_call, label)")
	auditRecordCmd.Flags().StringVar(&auditRecordModel, "model", "", "Model name (llm_call)")
	auditRecordCmd.Flags().StringVar(&auditRecordPrompt, "prompt", "", "Prompt text (llm_call)")
	auditRecordCmd.Flags().StringVar(&auditRecordResponse, "response", "", "Response text (llm_call)")
	auditRecordCmd.Flags().StringVar(&auditRecordIssueID, "issue-id", "", "Related issue id (bd-...)")
	auditRecordCmd.Flags().StringVar(&auditRecordToolName, "tool-name", "", "Tool name (tool_call)")
	auditRecordCmd.Flags().IntVar(&auditRecordExitCode, "exit-code", -1, "Exit code (tool_call)")
	auditRecordCmd.Flags().StringVar(&auditRecordError, "error", "", "Error string (llm_call/tool_call)")
	auditRecordCmd.Flags().BoolVar(&auditRecordStdin, "stdin", false, "Read a JSON object from stdin (must match audit.Entry schema)")

	auditLabelCmd.Flags().StringVar(&auditLabelValue, "label", "", `Label value (e.g. "good" or "bad")`)
	auditLabelCmd.Flags().StringVar(&auditLabelReason, "reason", "", "Reason for label")

	// Issue ID completions
	auditCmd.ValidArgsFunction = issueIDCompletion

	auditCmd.AddCommand(auditRecordCmd)
	auditCmd.AddCommand(auditLabelCmd)
	rootCmd.AddCommand(auditCmd)
}
