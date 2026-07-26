package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/timeparsing"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
)

type updateInput struct {
	fields           map[string]any
	addLabels        []string
	removeLabels     []string
	setLabels        *[]string
	reparent         *string
	claim            bool
	appendNotes      string
	hasAppendNotes   bool
	setMetadata      []string
	unsetMetadata    []string
	mergeMetadataIn  json.RawMessage
	clearDeferStatus bool
	// bd-wsqvw conditional-update guards; non-nil only when the flag was
	// explicitly passed (a pointer to "" is the real "expected unassigned"
	// guard).
	ifAssignee *string
	ifStatus   *string
}

func gatherUpdateInput(ctx context.Context, cmd *cobra.Command) (*updateInput, error) {
	in := &updateInput{fields: map[string]any{}}

	if cmd.Flags().Changed("status") {
		status, _ := cmd.Flags().GetString("status")
		if err := validateUpdateStatus(ctx, status); err != nil {
			return nil, err
		}
		in.fields["status"] = status
		if status == "closed" {
			session, _ := cmd.Flags().GetString("session")
			if session == "" {
				session = os.Getenv("CLAUDE_SESSION_ID")
			}
			if session != "" {
				in.fields["closed_by_session"] = session
			}
		}
	}
	if cmd.Flags().Changed("priority") {
		priorityStr, _ := cmd.Flags().GetString("priority")
		priority, err := validation.ValidatePriority(priorityStr)
		if err != nil {
			return nil, HandleErrorRespectJSON("%v", err)
		}
		in.fields["priority"] = priority
	}
	if cmd.Flags().Changed("title") {
		title, _ := cmd.Flags().GetString("title")
		title = strings.TrimSpace(title)
		if title == "" {
			return nil, HandleErrorRespectJSON("title cannot be empty")
		}
		in.fields["title"] = title
	}
	if cmd.Flags().Changed("assignee") {
		assignee, _ := cmd.Flags().GetString("assignee")
		in.fields["assignee"] = assignee
	}
	description, descChanged, err := getDescriptionFlag(cmd)
	if err != nil {
		return nil, HandleErrorRespectJSON("%v", err)
	}
	if descChanged {
		if err := validateDescriptionUpdate(cmd, description, descChanged); err != nil {
			return nil, HandleErrorRespectJSON("%v", err)
		}
		in.fields["description"] = description
	}
	design, designChanged, err := getDesignFlag(cmd)
	if err != nil {
		return nil, HandleErrorRespectJSON("%v", err)
	}
	if designChanged {
		in.fields["design"] = design
	}
	if cmd.Flags().Changed("notes") && cmd.Flags().Changed("append-notes") {
		return nil, HandleErrorRespectJSON("cannot specify both --notes and --append-notes")
	}
	if cmd.Flags().Changed("notes") {
		notes, _ := cmd.Flags().GetString("notes")
		in.fields["notes"] = notes
	}
	if cmd.Flags().Changed("append-notes") {
		in.appendNotes, _ = cmd.Flags().GetString("append-notes")
		in.hasAppendNotes = true
	}
	if cmd.Flags().Changed("acceptance") || cmd.Flags().Changed("acceptance-criteria") {
		var ac string
		if cmd.Flags().Changed("acceptance") {
			ac, _ = cmd.Flags().GetString("acceptance")
		} else {
			ac, _ = cmd.Flags().GetString("acceptance-criteria")
		}
		in.fields["acceptance_criteria"] = ac
	}
	if cmd.Flags().Changed("external-ref") {
		externalRef, _ := cmd.Flags().GetString("external-ref")
		if externalRef == "" {
			in.fields["external_ref"] = nil
		} else {
			in.fields["external_ref"] = externalRef
		}
	}
	if cmd.Flags().Changed("spec-id") {
		specID, _ := cmd.Flags().GetString("spec-id")
		in.fields["spec_id"] = specID
	}
	if cmd.Flags().Changed("estimate") {
		estimate, _ := cmd.Flags().GetInt("estimate")
		if estimate < 0 {
			return nil, HandleErrorRespectJSON("estimate must be a non-negative number of minutes")
		}
		in.fields["estimated_minutes"] = estimate
	}
	if cmd.Flags().Changed("type") {
		issueType, _ := cmd.Flags().GetString("type")
		in.fields["issue_type"] = utils.NormalizeIssueType(issueType)
	}
	if cmd.Flags().Changed("add-label") {
		in.addLabels, _ = cmd.Flags().GetStringSlice("add-label")
	}
	if cmd.Flags().Changed("remove-label") {
		in.removeLabels, _ = cmd.Flags().GetStringSlice("remove-label")
	}
	if cmd.Flags().Changed("set-labels") {
		labels, _ := cmd.Flags().GetStringSlice("set-labels")
		in.setLabels = &labels
	}
	if cmd.Flags().Changed("parent") {
		parent, _ := cmd.Flags().GetString("parent")
		in.reparent = &parent
	}
	if cmd.Flags().Changed("await-id") {
		awaitID, _ := cmd.Flags().GetString("await-id")
		in.fields["await_id"] = awaitID
	}
	if cmd.Flags().Changed("due") {
		dueStr, _ := cmd.Flags().GetString("due")
		if dueStr == "" {
			in.fields["due_at"] = nil
		} else {
			t, err := timeparsing.ParseRelativeTime(dueStr, time.Now())
			if err != nil {
				return nil, HandleErrorRespectJSON("invalid --due format %q. Examples: +6h, tomorrow, next monday, 2025-01-15", dueStr)
			}
			in.fields["due_at"] = t
		}
	}
	if cmd.Flags().Changed("defer") {
		deferStr, _ := cmd.Flags().GetString("defer")
		jsonOut, _ := cmd.Flags().GetBool("json")
		if deferStr == "" {
			in.fields["defer_until"] = nil
			if _, ok := in.fields["status"]; !ok {
				in.clearDeferStatus = true
			}
		} else {
			t, err := timeparsing.ParseRelativeTime(deferStr, time.Now())
			if err != nil {
				return nil, HandleErrorRespectJSON("invalid --defer format %q. Examples: +1h, tomorrow, next monday, 2025-01-15", deferStr)
			}
			inPast := t.Before(time.Now())
			if inPast && !jsonOut {
				fmt.Fprintf(os.Stderr, "%s Defer date %q is in the past. Issue will appear in bd ready immediately.\n",
					ui.RenderWarn("!"), t.Format("2006-01-02 15:04"))
				fmt.Fprintf(os.Stderr, "  Did you mean a future date? Use --defer=+1h or --defer=tomorrow\n")
			}
			in.fields["defer_until"] = t
			if _, ok := in.fields["status"]; !ok && !inPast {
				in.fields["status"] = string(types.StatusDeferred)
			}
		}
	}
	ephemeralChanged := cmd.Flags().Changed("ephemeral")
	persistentChanged := cmd.Flags().Changed("persistent")
	noHistoryChanged := cmd.Flags().Changed("no-history")
	historyChanged := cmd.Flags().Changed("history")
	if ephemeralChanged && persistentChanged {
		return nil, HandleErrorRespectJSON("cannot specify both --ephemeral and --persistent flags")
	}
	if noHistoryChanged && ephemeralChanged {
		return nil, HandleErrorRespectJSON("cannot specify both --no-history and --ephemeral flags")
	}
	if noHistoryChanged && historyChanged {
		return nil, HandleErrorRespectJSON("cannot specify both --no-history and --history flags")
	}
	if ephemeralChanged {
		in.fields["wisp"] = true
	}
	if persistentChanged {
		in.fields["wisp"] = false
	}
	if noHistoryChanged {
		in.fields["no_history"] = true
	}
	if historyChanged {
		in.fields["no_history"] = false
	}
	if cmd.Flags().Changed("metadata") {
		metadataValue, _ := cmd.Flags().GetString("metadata")
		var metadataJSON string
		if strings.HasPrefix(metadataValue, "@") {
			filePath := metadataValue[1:]
			data, err := os.ReadFile(filePath) //#nosec G304 -- user-supplied path via @file syntax
			if err != nil {
				return nil, HandleErrorRespectJSON("failed to read metadata file %s: %v", filePath, err)
			}
			metadataJSON = string(data)
		} else {
			metadataJSON = metadataValue
		}
		if !json.Valid([]byte(metadataJSON)) {
			return nil, HandleErrorRespectJSON("invalid JSON in --metadata: must be valid JSON")
		}
		in.mergeMetadataIn = json.RawMessage(metadataJSON)
	}
	setMetadataFlags, _ := cmd.Flags().GetStringArray("set-metadata")
	unsetMetadataFlags, _ := cmd.Flags().GetStringArray("unset-metadata")
	if (len(setMetadataFlags) > 0 || len(unsetMetadataFlags) > 0) && cmd.Flags().Changed("metadata") {
		return nil, HandleErrorRespectJSON("cannot combine --metadata with --set-metadata or --unset-metadata")
	}
	in.setMetadata = setMetadataFlags
	in.unsetMetadata = unsetMetadataFlags

	in.claim, _ = cmd.Flags().GetBool("claim")

	// bd-wsqvw conditional-update guards, mirroring the non-proxied path's
	// updateGuardsFromFlags rules: Changed()-detected presence (so
	// `--if-assignee ""` guards on unassigned), --if-status validated against
	// the live status set, mutually exclusive with --claim, and requiring a
	// field update to ride on.
	if cmd.Flags().Changed("if-assignee") {
		v, _ := cmd.Flags().GetString("if-assignee")
		in.ifAssignee = &v
	}
	if cmd.Flags().Changed("if-status") {
		v, _ := cmd.Flags().GetString("if-status")
		if err := validateUpdateStatus(ctx, v); err != nil {
			return nil, err
		}
		in.ifStatus = &v
	}
	if in.ifAssignee != nil || in.ifStatus != nil {
		if in.claim {
			return nil, HandleErrorRespectJSON("cannot combine --if-assignee/--if-status with --claim (--claim is already an atomic compare-and-set)")
		}
		if len(in.fields) == 0 && !in.hasAppendNotes && len(in.mergeMetadataIn) == 0 && len(in.setMetadata) == 0 && len(in.unsetMetadata) == 0 {
			return nil, HandleErrorRespectJSON("--if-assignee/--if-status require at least one field update (e.g. -a, -s); label and parent edits are not covered by the guard")
		}
	}
	return in, nil
}

func validateUpdateStatus(ctx context.Context, status string) error {
	if uowProvider == nil {
		return HandleError("proxied-server UOW provider not initialized")
	}
	uw, err := uowProvider.NewUOW(ctx)
	if err != nil {
		return HandleError("open unit of work: %v", err)
	}
	names, err := uw.ConfigUseCase().ListAllStatusNames(ctx)
	uw.Close(ctx)
	if err != nil {
		return HandleErrorRespectJSON("read status set: %v", err)
	}
	for _, name := range names {
		if name == status {
			return nil
		}
	}
	return HandleErrorRespectJSON("invalid status %q (allowed: %s)", status, strings.Join(names, ", "))
}

func isUpdateInputNoop(in *updateInput) bool {
	if in.claim {
		return false
	}
	if len(in.fields) > 0 || in.hasAppendNotes || in.setLabels != nil || in.reparent != nil {
		return false
	}
	if len(in.addLabels) > 0 || len(in.removeLabels) > 0 {
		return false
	}
	if len(in.mergeMetadataIn) > 0 || len(in.setMetadata) > 0 || len(in.unsetMetadata) > 0 {
		return false
	}
	return true
}
