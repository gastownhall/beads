package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/timeparsing"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
)

type createInput struct {
	markdownFile       string
	graphFile          string
	title              string
	explicitID         string
	parentID           string
	issueType          string
	priority           int
	assignee           string
	externalRef        string
	specID             string
	description        string
	design             string
	acceptanceCriteria string
	notes              string
	appendNotes        string
	labels             []string
	noInheritLabels    bool
	deps               []string
	waitsFor           string
	waitsForGate       string
	silent             bool
	dryRun             bool
	force              bool
	validate           bool
	ephemeral          bool
	noHistory          bool
	molType            types.MolType
	wispType           types.WispType
	eventCategory      string
	eventActor         string
	eventTarget        string
	eventPayload       string
	dueAt              *time.Time
	deferUntil         *time.Time
	metadata           json.RawMessage
	metadataSet        bool
	estimatedMinutes   *int
	repoOverride       string
	repoOverrideSet    bool
	createdBy          string
	owner              string
	jsonOutput         bool
	validationMode     string
}

func gatherCreateInput(cmd *cobra.Command, args []string) (createInput, error) {
	in := createInput{}

	in.markdownFile, _ = cmd.Flags().GetString("file")
	in.graphFile, _ = cmd.Flags().GetString("graph")
	in.dryRun, _ = cmd.Flags().GetBool("dry-run")

	if in.markdownFile != "" && in.graphFile != "" {
		return in, HandleError("cannot specify both --file and --graph")
	}
	if in.markdownFile != "" {
		if len(args) > 0 {
			return in, HandleError("cannot specify both title and --file flag")
		}
		if in.dryRun {
			return in, HandleError("--dry-run is not supported with --file flag")
		}
		if err := rejectSingleIssueFlagsForMarkdown(cmd); err != nil {
			return in, err
		}
	}
	if in.graphFile != "" {
		if len(args) > 0 {
			return in, HandleError("cannot specify both title and --graph flag")
		}
		if err := rejectSingleIssueFlagsForGraph(cmd); err != nil {
			return in, err
		}
	}

	in.silent, _ = cmd.Flags().GetBool("silent")
	in.force, _ = cmd.Flags().GetBool("force")
	in.validate, _ = cmd.Flags().GetBool("validate")
	in.noInheritLabels, _ = cmd.Flags().GetBool("no-inherit-labels")
	in.ephemeral, _ = cmd.Flags().GetBool("ephemeral")
	in.noHistory, _ = cmd.Flags().GetBool("no-history")

	if in.ephemeral && in.noHistory {
		return in, HandleError("--ephemeral and --no-history are mutually exclusive")
	}

	titleFlag, _ := cmd.Flags().GetString("title")
	title, err := resolveTitle(args, titleFlag, in.markdownFile, in.graphFile)
	if err != nil {
		return in, err
	}
	in.title = title

	desc, _, err := getDescriptionFlag(cmd)
	if err != nil {
		return in, err
	}
	in.description = desc
	skills, _ := cmd.Flags().GetString("skills")
	if skills != "" {
		if in.description != "" {
			in.description += "\n\n"
		}
		in.description += "## Required Skills\n" + skills
	}
	ctxStr, _ := cmd.Flags().GetString("context")
	if ctxStr != "" {
		if in.description != "" {
			in.description += "\n\n"
		}
		in.description += "## Context\n" + ctxStr
	}

	design, _, err := getDesignFlag(cmd)
	if err != nil {
		return in, err
	}
	in.design = design
	in.acceptanceCriteria, _ = cmd.Flags().GetString("acceptance")
	in.notes, _ = cmd.Flags().GetString("notes")
	in.appendNotes, _ = cmd.Flags().GetString("append-notes")
	in.specID, _ = cmd.Flags().GetString("spec-id")

	if in.markdownFile == "" && in.graphFile == "" {
		if in.description == "" && !isTestIssue(in.title) {
			if config.GetBool("create.require-description") {
				return in, HandleError("description is required (set create.require-description: false in config.yaml to disable)")
			}
		}
	}

	priorityStr, _ := cmd.Flags().GetString("priority")
	priority, err := validation.ValidatePriority(priorityStr)
	if err != nil {
		return in, HandleError("%v", err)
	}
	in.priority = priority

	in.issueType, _ = cmd.Flags().GetString("type")
	in.assignee, _ = cmd.Flags().GetString("assignee")
	in.externalRef, _ = cmd.Flags().GetString("external-ref")
	in.explicitID, _ = cmd.Flags().GetString("id")
	in.parentID, _ = cmd.Flags().GetString("parent")
	in.waitsFor, _ = cmd.Flags().GetString("waits-for")
	in.waitsForGate, _ = cmd.Flags().GetString("waits-for-gate")

	if in.explicitID != "" && in.parentID != "" {
		return in, HandleError("cannot specify both --id and --parent flags")
	}

	in.labels, _ = cmd.Flags().GetStringSlice("labels")
	labelAlias, _ := cmd.Flags().GetStringSlice("label")
	if len(labelAlias) > 0 {
		in.labels = append(in.labels, labelAlias...)
	}
	in.deps, _ = cmd.Flags().GetStringSlice("deps")

	in.repoOverride, _ = cmd.Flags().GetString("repo")
	in.repoOverrideSet = cmd.Flags().Changed("repo")

	if molTypeStr, _ := cmd.Flags().GetString("mol-type"); molTypeStr != "" {
		mt := types.MolType(molTypeStr)
		if !mt.IsValid() {
			return in, HandleError("invalid mol-type %q (must be swarm, patrol, or work)", molTypeStr)
		}
		in.molType = mt
	}
	if wispTypeStr, _ := cmd.Flags().GetString("wisp-type"); wispTypeStr != "" {
		wt := types.WispType(wispTypeStr)
		if !wt.IsValid() {
			return in, HandleError("invalid wisp-type %q (must be heartbeat, ping, patrol, gc_report, recovery, error, or escalation)", wispTypeStr)
		}
		in.wispType = wt
	}

	in.eventCategory, _ = cmd.Flags().GetString("event-category")
	in.eventActor, _ = cmd.Flags().GetString("event-actor")
	in.eventTarget, _ = cmd.Flags().GetString("event-target")
	in.eventPayload, _ = cmd.Flags().GetString("event-payload")
	if (in.eventCategory != "" || in.eventActor != "" || in.eventTarget != "" || in.eventPayload != "") && in.issueType != "event" {
		return in, HandleError("--event-category, --event-actor, --event-target, and --event-payload flags require --type=event")
	}

	if dueStr, _ := cmd.Flags().GetString("due"); dueStr != "" {
		t, err := timeparsing.ParseRelativeTime(dueStr, time.Now())
		if err != nil {
			return in, HandleError("invalid --due format %q. Examples: +6h, tomorrow, next monday, 2025-01-15", dueStr)
		}
		in.dueAt = &t
	}

	if deferStr, _ := cmd.Flags().GetString("defer"); deferStr != "" {
		t, err := timeparsing.ParseRelativeTime(deferStr, time.Now())
		if err != nil {
			return in, HandleError("invalid --defer format %q. Examples: +1h, tomorrow, next monday, 2025-01-15", deferStr)
		}
		if t.Before(time.Now()) && !in.silent && !debug.IsQuiet() {
			fmt.Fprintf(os.Stderr, "%s Defer date %q is in the past. Issue will appear in bd ready immediately.\n",
				ui.RenderWarn("!"), t.Format("2006-01-02 15:04"))
			fmt.Fprintf(os.Stderr, "  Did you mean a future date? Use --defer=+1h or --defer=tomorrow\n")
		}
		in.deferUntil = &t
	}

	if cmd.Flags().Changed("metadata") {
		metadataValue, _ := cmd.Flags().GetString("metadata")
		var metadataJSON string
		if strings.HasPrefix(metadataValue, "@") {
			filePath := metadataValue[1:]
			// #nosec G304 -- user explicitly provides file path via @file.json syntax
			data, err := os.ReadFile(filePath)
			if err != nil {
				return in, HandleError("failed to read metadata file %s: %v", filePath, err)
			}
			metadataJSON = string(data)
		} else {
			metadataJSON = metadataValue
		}
		if !json.Valid([]byte(metadataJSON)) {
			return in, HandleError("invalid JSON in --metadata: must be valid JSON")
		}
		in.metadata = json.RawMessage(metadataJSON)
		in.metadataSet = true
	}

	if cmd.Flags().Changed("estimate") {
		est, _ := cmd.Flags().GetInt("estimate")
		if est < 0 {
			return in, HandleError("estimate must be a non-negative number of minutes")
		}
		in.estimatedMinutes = &est
	}

	in.createdBy = getActorWithGit()
	in.owner = getOwner()

	in.jsonOutput = jsonOutput

	in.validationMode = config.GetString("validation.on-create")
	if in.validate {
		in.validationMode = "error"
	}

	return in, nil
}

var singleIssueOnlyFlags = []string{
	"title",
	"id", "parent", "no-inherit-labels",
	"deps", "waits-for", "waits-for-gate",
	"type", "priority", "assignee", "external-ref", "spec-id",
	"description", "body", "message", "body-file", "description-file", "stdin",
	"design", "design-file", "acceptance", "notes", "append-notes",
	"labels", "label", "skills", "context",
	"event-category", "event-actor", "event-target", "event-payload",
	"due", "defer",
	"metadata", "estimate", "force", "wisp-type",
}

func rejectSingleIssueFlagsForMarkdown(cmd *cobra.Command) error {
	for _, name := range singleIssueOnlyFlags {
		if cmd.Flags().Changed(name) {
			return HandleError("--%s is not valid with --file (markdown templates supply per-issue fields)", name)
		}
	}
	return nil
}

func rejectSingleIssueFlagsForGraph(cmd *cobra.Command) error {
	for _, name := range singleIssueOnlyFlags {
		if cmd.Flags().Changed(name) {
			return HandleError("--%s is not valid with --graph (graph plans supply per-node fields)", name)
		}
	}
	if cmd.Flags().Changed("mol-type") {
		return HandleError("--mol-type is not valid with --graph (graph plans don't carry molecule semantics)")
	}
	return nil
}

func resolveTitle(args []string, titleFlag, markdownFile, graphFile string) (string, error) {
	if markdownFile != "" || graphFile != "" {
		return "", nil
	}

	switch {
	case len(args) > 0 && titleFlag != "":
		if args[0] != titleFlag {
			return "", HandleError("cannot specify different titles as both positional argument and --title flag\n  Positional: %q\n  --title:    %q", args[0], titleFlag)
		}
		return args[0], nil
	case len(args) > 0:
		if strings.HasPrefix(args[0], "-") {
			return "", HandleError("title %q looks like a flag (starts with '-').\n  Run 'bd create --help' for available options.\n  To use this title anyway, pass it explicitly: bd create --title=%q", args[0], args[0])
		}
		return args[0], nil
	case titleFlag != "":
		return titleFlag, nil
	default:
		return "", HandleError("title required (or use --file to create from markdown)")
	}
}
