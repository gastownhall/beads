package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// Glyphs built from their codepoints rather than typed as literals, so this
// file stays ASCII source while rendering the identical marks the rest of
// this command family uses (ui.RenderPass/RenderWarn/RenderAccent elsewhere
// in cmd/bd, e.g. label.go's reportLabelEdit and labelListAllCmd).
var (
	glyphCheck = string(rune(0x2713)) // U+2713 CHECK MARK
	glyphWarn  = string(rune(0x26A0)) // U+26A0 WARNING SIGN
)

// labelsVocabularyConfigKey is the config knob controlling whether an
// undefined label is accepted silently (open, the default -- zero behavior
// change from before this registry existed), warned about (warn), or
// refused (enforce). See docs/core-concepts/labels.md.
const labelsVocabularyConfigKey = issueops.LabelsVocabularyConfigKey

const (
	labelsVocabularyOpen    = "open"
	labelsVocabularyWarn    = "warn"
	labelsVocabularyEnforce = "enforce"
)

// validateLabelsVocabularyConfig rejects any value other than the three
// modes at `bd config set` time (Protocol v0.1 C-OQ1: validated when set,
// not discovered broken at the next write), matching
// validateStorageClassConfig's role for storage-class.<type>.
func validateLabelsVocabularyConfig(value string) error {
	switch value {
	case labelsVocabularyOpen, labelsVocabularyWarn, labelsVocabularyEnforce:
		return nil
	default:
		return fmt.Errorf("invalid value %q for %s (must be one of: %s, %s, %s)",
			value, labelsVocabularyConfigKey, labelsVocabularyOpen, labelsVocabularyWarn, labelsVocabularyEnforce)
	}
}

// normalizeLabelsVocabularyMode maps an unset/unrecognized config value to
// the documented default. It is deliberately permissive on anything it does
// not recognize (treated as open) rather than erroring on a write path:
// validateLabelsVocabularyConfig is the place a bad value is caught, at `bd
// config set` time. A write path that encountered one anyway (a config
// written before this knob existed to validate it, edited by hand, or set by
// a NEWER client that knows a mode this build does not) must fail open, not
// block every future label write - but not SILENTLY (bda-1735): the operator
// who configured `strict` on a newer client deserves one line saying this
// build reads it as open, or enforcement appears to be on while every write
// sails through.
func normalizeLabelsVocabularyMode(value string) string {
	switch value {
	case labelsVocabularyWarn, labelsVocabularyEnforce:
		return value
	case "", labelsVocabularyOpen:
		return labelsVocabularyOpen
	default:
		unknownVocabularyModeWarnOnce.Do(func() {
			if !debug.IsQuiet() {
				fmt.Fprintf(os.Stderr, "warning: unrecognized %s value %q (set by a newer client, or a hand edit?) - treating as %s\n",
					labelsVocabularyConfigKey, value, labelsVocabularyOpen)
			}
		})
		return labelsVocabularyOpen
	}
}

// unknownVocabularyModeWarnOnce keeps the unrecognized-mode warning to one
// line per process: the normalizer runs on every label write in a batch.
var unknownVocabularyModeWarnOnce sync.Once

// checkLabelVocabulary is the write-path enforcement entry point for the
// labels.vocabulary knob. It is called from the INTERACTIVE label-write call
// sites only -- create, update --add-label/--labels, label add, tag, quick,
// the same call-site set that normalizes labels per #5813 -- and NEVER from
// import/replay, where an undefined label must always be accepted silently
// regardless of the configured mode.
//
// It fails open on any read error (config unreadable, registry unreadable):
// a transient problem reading the vocabulary registry must not block, or
// spuriously warn on, a write that has nothing to do with it.
func checkLabelVocabulary(ctx context.Context, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	mode, err := readLabelsVocabularyMode(ctx)
	if err != nil || mode == labelsVocabularyOpen {
		return nil
	}
	defs, err := readLabelDefinitionsForCheck(ctx)
	if err != nil {
		return nil
	}
	known := issueops.LabelVocabularySet(defs)

	var undefined []string
	for _, label := range labels {
		if spelling, ok := known[strings.ToLower(label)]; ok && spelling == label {
			continue
		}
		undefined = append(undefined, label)
	}
	if len(undefined) == 0 {
		return nil
	}

	if mode == labelsVocabularyEnforce {
		return fmt.Errorf("undefined label(s) not in the vocabulary: %s (define with 'bd label define <label>', or disable with 'bd config set %s %s')",
			formatUndefinedLabels(undefined, known), labelsVocabularyConfigKey, labelsVocabularyOpen)
	}

	if !debug.IsQuiet() {
		fmt.Fprintf(os.Stderr, "%s Undefined label(s) not in the vocabulary: %s\n",
			ui.RenderWarn(glyphWarn), formatUndefinedLabels(undefined, known))
		fmt.Fprintf(os.Stderr, "  Define with 'bd label define <label>', or silence with 'bd config set %s %s'.\n",
			labelsVocabularyConfigKey, labelsVocabularyOpen)
	}
	return nil
}

// formatUndefinedLabels renders the undefined labels for the warn/enforce
// message, appending a case-insensitive suggestion from the defined
// vocabulary where one exists (defining "backend" and writing "Backend"
// suggests the defined spelling rather than just saying it is unknown).
func formatUndefinedLabels(undefined []string, known map[string]string) string {
	parts := make([]string, len(undefined))
	for i, label := range undefined {
		if suggestion, ok := known[strings.ToLower(label)]; ok {
			parts[i] = fmt.Sprintf("%q (did you mean %q?)", label, suggestion)
		} else {
			parts[i] = fmt.Sprintf("%q", label)
		}
	}
	return strings.Join(parts, ", ")
}

// readLabelsVocabularyMode reads the labels.vocabulary config value on
// whichever route this invocation is on, normalizing an unset/invalid value
// to "open".
func readLabelsVocabularyMode(ctx context.Context) (string, error) {
	if usesProxiedServer() {
		uw, err := proxiedOpenReadUOW(ctx)
		if err != nil {
			return "", err
		}
		defer uw.Close(ctx)
		v, err := uw.ConfigUseCase().GetConfig(ctx, labelsVocabularyConfigKey)
		if err != nil {
			return "", err
		}
		return normalizeLabelsVocabularyMode(v), nil
	}
	if store == nil {
		return labelsVocabularyOpen, nil
	}
	v, err := store.GetConfig(ctx, labelsVocabularyConfigKey)
	if err != nil {
		return "", err
	}
	return normalizeLabelsVocabularyMode(v), nil
}

// readLabelDefinitionsForCheck reads the full vocabulary registry on
// whichever route this invocation is on.
func readLabelDefinitionsForCheck(ctx context.Context) ([]types.LabelDefinition, error) {
	if usesProxiedServer() {
		uw, err := proxiedOpenReadUOW(ctx)
		if err != nil {
			return nil, err
		}
		defer uw.Close(ctx)
		return uw.LabelVocabularyUseCase().List(ctx)
	}
	if store == nil {
		return nil, nil
	}
	return store.ListLabelDefinitions(ctx)
}

var labelDefineCmd = &cobra.Command{
	Use:   "define <label> [--description <text>]",
	Short: "Declare a label in the curated vocabulary registry",
	Long: `Declare a label in the opt-in curated vocabulary registry.

Defining a label changes nothing about what label a caller may write by
itself. Whether an UNDEFINED label is accepted silently, warned about, or
refused on an interactive write (create, update, label add, tag, quick) is
controlled by:

  bd config set labels.vocabulary open      # default: no behavior change
  bd config set labels.vocabulary warn      # write proceeds, warns on stderr
  bd config set labels.vocabulary enforce   # write is refused

Defining a label that collides with an already-defined label under a
DIFFERENT case ("Backend" when "backend" is defined) is refused, naming the
existing spelling: this registry never holds two case-variant spellings of
the same word. It does not fold labels already stored on issues in a
different case -- there is no dedicated rename command yet; reconcile those
by hand with 'bd label remove <old>' / 'bd label add <new>' on each issue.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("label define")

		evt := metrics.NewCommandEvent("label-define")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		description, _ := cmd.Flags().GetString("description")

		if usesProxiedServer() {
			return runLabelDefineProxiedServer(rootCtx, args[0], description)
		}
		if err := ensureDirectMode("label define requires direct database access"); err != nil {
			return HandleError("%v", err)
		}
		if err := store.DefineLabel(rootCtx, args[0], description, actor); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		commandDidWrite.Store(true)

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status": "defined",
				"label":  strings.TrimSpace(args[0]),
			})
		}
		fmt.Printf("%s Defined label %q\n", ui.RenderPass(glyphCheck), strings.TrimSpace(args[0]))
		return nil
	},
}

var labelUndefineCmd = &cobra.Command{
	Use:   "undefine <label>",
	Short: "Remove a label from the curated vocabulary registry",
	Long: `Remove a label from the opt-in curated vocabulary registry.

Matches case-insensitively (only one spelling of a label can ever be
defined). Undefining a label that is not currently defined is an error.

This does not touch any issue that carries the label -- it only affects
future labels.vocabulary=warn/enforce checks.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("label undefine")

		evt := metrics.NewCommandEvent("label-undefine")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runLabelUndefineProxiedServer(rootCtx, args[0])
		}
		if err := ensureDirectMode("label undefine requires direct database access"); err != nil {
			return HandleError("%v", err)
		}
		if err := store.UndefineLabel(rootCtx, args[0]); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		commandDidWrite.Store(true)

		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"status": "undefined",
				"label":  strings.TrimSpace(args[0]),
			})
		}
		fmt.Printf("%s Undefined label %q\n", ui.RenderPass(glyphCheck), strings.TrimSpace(args[0]))
		return nil
	},
}

// labelDefinedInfo is the `bd label defined` JSON/table row: a registry
// entry plus how many issues currently carry it, so an operator can see at
// a glance which curated labels are actually in use.
type labelDefinedInfo struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedAt   string `json:"created_at"`
	InUseCount  int    `json:"in_use_count"`
}

var labelDefinedCmd = &cobra.Command{
	Use:   "defined",
	Short: "List the curated label vocabulary",
	Long: `List every label declared in the opt-in curated vocabulary registry,
with its description and how many issues currently carry it.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("label-defined")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runLabelDefinedProxiedServer(rootCtx)
		}
		if err := ensureDirectMode("label defined requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		defs, err := store.ListLabelDefinitions(rootCtx)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		counts, err := countLabelsAcrossIssues(rootCtx, store)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		rows := make([]labelDefinedInfo, 0, len(defs))
		for _, d := range defs {
			row := labelDefinedInfo{
				Label:      d.Label,
				CreatedAt:  d.CreatedAt.UTC().Format("2006-01-02"),
				InUseCount: counts[d.Label],
			}
			if d.Description != nil {
				row.Description = *d.Description
			}
			if d.CreatedBy != nil {
				row.CreatedBy = *d.CreatedBy
			}
			rows = append(rows, row)
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })

		return renderLabelDefinedRows(rows)
	},
}

// renderLabelDefinedRows prints `bd label defined`'s rows in the shape both
// routes have always printed (matching labelListAllCmd's convention): one
// JSON array, or one human line per label naming its in-use count and
// description.
func renderLabelDefinedRows(rows []labelDefinedInfo) error {
	if jsonOutput {
		return outputJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Println("\nNo labels defined. Define one with: bd label define <label>")
		return nil
	}
	// No glyph on the header: U+1F3F7 LABEL is an emoji blob, which the CLI
	// visual design system prohibits (AGENT_INSTRUCTIONS.md: "Small Unicode
	// symbols only; avoid emoji blobs") - codex cross-model finding, bda-1735.
	fmt.Printf("\n%s\n", ui.RenderAccent(fmt.Sprintf("Defined labels (%d):", len(rows))))
	maxLen := 0
	for _, row := range rows {
		if len(row.Label) > maxLen {
			maxLen = len(row.Label)
		}
	}
	for _, row := range rows {
		padding := strings.Repeat(" ", maxLen-len(row.Label))
		desc := row.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Printf("  %s%s  %d issue(s)  %s\n", row.Label, padding, row.InUseCount, desc)
	}
	fmt.Println()
	return nil
}

func init() {
	labelDefineCmd.Flags().String("description", "", "Optional description of what this label means")

	labelCmd.AddCommand(labelDefineCmd)
	labelCmd.AddCommand(labelUndefineCmd)
	labelCmd.AddCommand(labelDefinedCmd)
}
