package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/audit"
	"github.com/steveyegge/beads/internal/metrics"
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
	Long: `Record explicit agent/tool interaction audit entries in .beads/interactions.jsonl.

This optional JSONL sidecar is disabled by default. Enable it with:

  bd config set audit.enabled true

Issue history is always recorded in the database and is visible with
bd history <id> --events. The JSONL sidecar is for explicit interaction capture:
- auditing ("why did the agent do that?")
- dataset generation (SFT/RL fine-tuning)

Entries are append-only. Labeling creates a new "label" entry that references a parent entry.`,
}

var auditRecordCmd = &cobra.Command{
	Use:           "record",
	Short:         "Append an audit interaction entry",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		evt := metrics.NewCommandEvent("audit-record")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		var e audit.Entry

		fi, _ := os.Stdin.Stat()
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
				return HandleError("failed to read stdin: %v", err)
			}
			if err := json.Unmarshal(b, &e); err != nil {
				return HandleError("invalid JSON on stdin: %v", err)
			}
			if actor != "" {
				e.Actor = actor
			}
		} else {
			if auditRecordKind == "" {
				return HandleError("--kind is required")
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

		id, err := audit.AppendIfEnabled(&e)
		if err != nil {
			return HandleError("%v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]any{
				"id":   id,
				"kind": e.Kind,
			})
		}

		fmt.Println(id)
		return nil
	},
}

var auditLabelCmd = &cobra.Command{
	Use:           "label <entry-id>",
	Short:         "Append a label entry referencing an existing interaction",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("audit-label")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		parentID := args[0]
		if auditLabelValue == "" {
			return HandleError("--label is required")
		}
		e := audit.Entry{
			Kind:     "label",
			Actor:    actor,
			ParentID: parentID,
			Label:    auditLabelValue,
			Reason:   auditLabelReason,
		}

		id, err := audit.AppendIfEnabled(&e)
		if err != nil {
			return HandleError("%v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]any{
				"id":        id,
				"parent_id": parentID,
				"label":     auditLabelValue,
			})
		}

		fmt.Println(id)
		return nil
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

	auditCmd.ValidArgsFunction = issueIDCompletion

	auditCmd.AddCommand(auditRecordCmd)
	auditCmd.AddCommand(auditLabelCmd)
	rootCmd.AddCommand(auditCmd)
}
