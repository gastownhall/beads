// Package main implements the bd CLI provenance event-log commands.
package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var provenanceCmd = &cobra.Command{
	Use:           "provenance",
	GroupID:       "issues",
	Short:         "Append-only provenance event log",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `Record and read provenance events: typed bindings from an issue to an
opaque external artifact (a git SHA, PR, work-id, transcript, or branch).

The log is append-only — there is no update or delete. bd never interprets the
actor or ref; only kind and ref-kind are structurally validated. Recording is
idempotent on a deterministic id, so a producer firing twice is harmless.`,
}

var (
	provIssue   string
	provKind    string
	provSource  string
	provActor   string
	provRef     string
	provRefKind string
	provAt      string
	provPayload string
	// provLogKind backs `provenance log --kind`; kept separate from provKind
	// (which backs `record --kind`) so the two flags cannot race a shared
	// package var across command runs.
	provLogKind string
)

var provenanceRecordCmd = &cobra.Command{
	Use:           "record --issue <id> --kind <k> --source <s>",
	Short:         "Record a provenance event (idempotent)",
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `Record a provenance event. The event is appended idempotently: a
deterministic id is computed from source:issue:kind:(ref or --at), so re-running
the same record is a no-op.

An event recorded without --ref requires --at so the id is caller-owned.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("provenance record")
		ctx := rootCtx

		issueID, err := utils.ResolvePartialID(ctx, store, provIssue)
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", provIssue, err)
		}

		ev := types.ProvenanceEvent{
			IssueID: issueID,
			Kind:    types.ProvKind(provKind),
			Source:  provSource,
		}
		if provActor != "" {
			ev.Actor = &provActor
		}
		if provRef != "" {
			ev.Ref = &provRef
		}
		if provRefKind != "" {
			ev.RefKind = &provRefKind
		}
		if provPayload != "" {
			ev.Payload = &provPayload
		}
		if provAt != "" {
			at, err := time.Parse(time.RFC3339, provAt)
			if err != nil {
				return HandleErrorRespectJSON("--at must be an RFC3339 timestamp (e.g. 2026-06-19T12:00:00Z): %v", err)
			}
			atUTC := at.UTC()
			ev.OccurredAt = &atUTC
		}

		// Fail early with the same structural rules the store enforces, so the CLI
		// gives friendly feedback before opening a transaction.
		if err := issueops.ValidateProvenanceEvent(ev); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		id, inserted, err := store.RecordProvenanceEvent(ctx, ev)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if inserted {
			commandDidWrite.Store(true)
		}

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"id":       id,
				"inserted": inserted,
				"issue_id": issueID,
				"kind":     provKind,
			})
		}
		if inserted {
			fmt.Printf("%s Recorded %s provenance %s on %s\n", ui.RenderPass("✓"), provKind, id, issueID)
		} else {
			fmt.Printf("%s Provenance %s already recorded (id %s)\n", ui.RenderAccent("•"), provKind, id)
		}
		return nil
	},
}

var provenanceLogCmd = &cobra.Command{
	Use:           "log <issue-id>",
	Short:         "List provenance events for an issue",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := rootCtx
		issueID, err := utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", args[0], err)
		}
		events, err := store.GetProvenanceEvents(ctx, issueID, provLogKind)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		return outputProvenanceEvents(events)
	},
}

var provenanceByRefCmd = &cobra.Command{
	Use:           "by-ref <ref>",
	Short:         "List provenance events bound to a ref",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := rootCtx
		events, err := store.GetProvenanceByRef(ctx, args[0])
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		return outputProvenanceEvents(events)
	},
}

func outputProvenanceEvents(events []types.ProvenanceEvent) error {
	if jsonOutput {
		if events == nil {
			events = []types.ProvenanceEvent{}
		}
		return outputJSON(events)
	}
	if len(events) == 0 {
		fmt.Println("No provenance events")
		return nil
	}
	for _, ev := range events {
		when := "—"
		if ev.OccurredAt != nil {
			when = ev.OccurredAt.Format(time.RFC3339)
		}
		line := fmt.Sprintf("%s  %-8s  %s", when, ev.Kind, ev.IssueID)
		if ev.Ref != nil {
			refKind := ""
			if ev.RefKind != nil {
				refKind = *ev.RefKind + ":"
			}
			line += fmt.Sprintf("  %s%s", refKind, *ev.Ref)
		}
		if ev.Actor != nil {
			line += fmt.Sprintf("  by %s", *ev.Actor)
		}
		line += fmt.Sprintf("  (%s)", ev.Source)
		fmt.Println(line)
	}
	return nil
}

func init() {
	provenanceRecordCmd.Flags().StringVar(&provIssue, "issue", "", "issue id (required)")
	provenanceRecordCmd.Flags().StringVar(&provKind, "kind", "", "event kind: cut|claim|suspend|resume|handoff|commit|land|used (required)")
	provenanceRecordCmd.Flags().StringVar(&provSource, "source", "", "producer of the event, e.g. git-hook, orchestrator (required)")
	provenanceRecordCmd.Flags().StringVar(&provActor, "actor", "", "opaque actor identifier (optional)")
	provenanceRecordCmd.Flags().StringVar(&provRef, "ref", "", "opaque external reference, e.g. a SHA or PR url (optional)")
	provenanceRecordCmd.Flags().StringVar(&provRefKind, "ref-kind", "", "ref kind: git-sha|pr|work-id|transcript|branch (optional)")
	provenanceRecordCmd.Flags().StringVar(&provAt, "at", "", "event-time as RFC3339 (required for ref-less kinds)")
	provenanceRecordCmd.Flags().StringVar(&provPayload, "payload", "", "opaque payload, e.g. JSON (optional)")
	_ = provenanceRecordCmd.MarkFlagRequired("issue")
	_ = provenanceRecordCmd.MarkFlagRequired("kind")
	_ = provenanceRecordCmd.MarkFlagRequired("source")

	provenanceLogCmd.Flags().StringVar(&provLogKind, "kind", "", "filter by kind (optional)")
	provenanceLogCmd.ValidArgsFunction = issueIDCompletion

	provenanceCmd.AddCommand(provenanceRecordCmd)
	provenanceCmd.AddCommand(provenanceLogCmd)
	provenanceCmd.AddCommand(provenanceByRefCmd)
	rootCmd.AddCommand(provenanceCmd)
}
