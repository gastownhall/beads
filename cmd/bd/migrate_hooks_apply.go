package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/remedy"
)

type hookMigrationMode struct {
	RequestedDryRun bool
	RequestedApply  bool
	RequestedYes    bool
}

func validateHookMigrationMode(requestedDryRun, requestedApply, requestedYes bool) (hookMigrationMode, error) {
	switch {
	case requestedDryRun && requestedApply:
		return hookMigrationMode{}, errors.New("cannot use --dry-run and --apply together")
	case requestedYes && !requestedApply:
		return hookMigrationMode{}, errors.New("--yes requires --apply")
	case !requestedDryRun && !requestedApply:
		return hookMigrationMode{}, errors.New("must specify exactly one mode: --dry-run or --apply")
	default:
		return hookMigrationMode{
			RequestedDryRun: requestedDryRun,
			RequestedApply:  requestedApply,
			RequestedYes:    requestedYes,
		}, nil
	}
}

func validateHookMigrationApplyConsent(requestedYes, interactive, jsonRequested bool) error {
	if requestedYes {
		return nil
	}
	if jsonRequested {
		return errors.New("--json with --apply requires --yes")
	}
	if interactive {
		return nil
	}
	return errors.New("--apply requires confirmation; rerun with --yes in non-interactive mode")
}

type hookMigrationWriteSource string

const (
	hookMigrationWriteFromTemplate hookMigrationWriteSource = "template"
	hookMigrationWriteFromHookFile hookMigrationWriteSource = "hook_file"
	hookMigrationWriteFromOld      hookMigrationWriteSource = "old_sidecar"
	hookMigrationWriteFromBackup   hookMigrationWriteSource = "backup_sidecar"
	// hookMigrationWriteFromHookFileStrip repairs a non-shell hook an older bd
	// version corrupted (GH#6348): the stale bd section is stripped in place —
	// no re-injection, no shebang, no newline normalization.
	hookMigrationWriteFromHookFileStrip hookMigrationWriteSource = "hook_file_strip"
	// hookMigrationWriteFromSidecarCopy restores a non-shell sidecar as the
	// active hook, verbatim (GH#6348): no bd section is injected, no shebang
	// is ensured, no line endings are normalized — the sidecars are retained.
	hookMigrationWriteFromSidecarCopy hookMigrationWriteSource = "sidecar_copy"
)

type hookMigrationWriteOp struct {
	HookName   string                   `json:"hook_name"`
	HookPath   string                   `json:"hook_path"`
	State      string                   `json:"state"`
	SourceKind hookMigrationWriteSource `json:"source_kind"`
	SourcePath string                   `json:"source_path,omitempty"`
}

type hookMigrationRetireOp struct {
	HookName        string `json:"hook_name"`
	SourcePath      string `json:"source_path"`
	DestinationPath string `json:"destination_path"`
}

type hookMigrationExecutionPlan struct {
	WriteOps       []hookMigrationWriteOp  `json:"write_ops"`
	RetireOps      []hookMigrationRetireOp `json:"retire_ops"`
	NoopHooks      []string                `json:"noop_hooks"`
	BlockingErrors []string                `json:"blocking_errors"`
}

type hookMigrationOutputOperation struct {
	Action      string `json:"action"`
	HookName    string `json:"hook_name"`
	Path        string `json:"path,omitempty"`
	SourcePath  string `json:"source_path,omitempty"`
	Destination string `json:"destination_path,omitempty"`
	State       string `json:"state,omitempty"`
}

type hookMigrationApplySummary struct {
	WrittenHooks     []string `json:"written_hooks"`
	RetiredArtifacts []string `json:"retired_artifacts"`
	SkippedArtifacts []string `json:"skipped_artifacts"`
	WrittenHookCount int      `json:"written_hook_count"`
	RetiredCount     int      `json:"retired_count"`
	SkippedCount     int      `json:"skipped_count"`
}

func (p hookMigrationExecutionPlan) operationCount() int {
	return len(p.WriteOps) + len(p.RetireOps)
}

func (p hookMigrationExecutionPlan) outputOperations() []hookMigrationOutputOperation {
	ops := make([]hookMigrationOutputOperation, 0, p.operationCount())
	for _, write := range p.WriteOps {
		ops = append(ops, hookMigrationOutputOperation{
			Action:     "write_hook",
			HookName:   write.HookName,
			Path:       write.HookPath,
			SourcePath: write.SourcePath,
			State:      write.State,
		})
	}
	for _, retire := range p.RetireOps {
		ops = append(ops, hookMigrationOutputOperation{
			Action:      "retire_sidecar",
			HookName:    retire.HookName,
			Path:        retire.SourcePath,
			SourcePath:  retire.SourcePath,
			Destination: retire.DestinationPath,
		})
	}
	return ops
}

func buildHookMigrationExecutionPlan(plan doctor.HookMigrationPlan) hookMigrationExecutionPlan {
	execPlan := hookMigrationExecutionPlan{
		WriteOps:       make([]hookMigrationWriteOp, 0, plan.NeedsMigrationCount),
		RetireOps:      make([]hookMigrationRetireOp, 0, plan.NeedsMigrationCount*2),
		NoopHooks:      make([]string, 0, plan.TotalHooks),
		BlockingErrors: make([]string, 0),
	}

	for _, hook := range plan.Hooks {
		switch hook.State {
		case "marker_managed", "unmanaged_custom", "missing_no_artifacts", "non_shell_manual":
			// non_shell_manual (GH#6348) is deliberate: bd never rewrites a
			// non-shell hook whose markers do not validate as pairs, or binary
			// content — the plan row's suggested action carries the
			// manual-inspection diagnostic instead.
			execPlan.NoopHooks = append(execPlan.NoopHooks, hook.Name)
			continue
		case "marker_managed_non_shell":
			// GH#6348: strip the stale bd section in place. The hook's
			// interpreter cannot run bd's sh section, so nothing is
			// re-injected, and its sidecars are retained, not retired.
			execPlan.WriteOps = append(execPlan.WriteOps, hookMigrationWriteOp{
				HookName:   hook.Name,
				HookPath:   hook.HookPath,
				State:      hook.State,
				SourceKind: hookMigrationWriteFromHookFileStrip,
				SourcePath: hook.HookPath,
			})
			continue
		case "read_error":
			execPlan.BlockingErrors = append(execPlan.BlockingErrors, formatHookMigrationBlockingError(hook))
			continue
		case "marker_broken":
			// Broken markers are fixable: read existing file and re-inject.
			// injectHookSection handles orphaned/reversed markers while preserving
			// user content outside the broken markers.
			execPlan.WriteOps = append(execPlan.WriteOps, hookMigrationWriteOp{
				HookName:   hook.Name,
				HookPath:   hook.HookPath,
				State:      hook.State,
				SourceKind: hookMigrationWriteFromHookFile,
				SourcePath: hook.HookPath,
			})
			continue
		}

		// For legacy hooks with sidecars, the migration discards the current hook
		// file in favor of sidecar content. Check that the user hasn't added custom
		// logic to the shim — if they have, block migration to avoid silent data loss.
		if hook.LegacyBDHook && (hook.HasOldSidecar || hook.HasBackupSidecar) {
			content, readErr := os.ReadFile(hook.HookPath) // #nosec G304 -- path from migration planner
			if readErr == nil && !doctor.IsUnmodifiedLegacyHook(string(content)) {
				execPlan.BlockingErrors = append(execPlan.BlockingErrors,
					fmt.Sprintf("%s: legacy hook appears user-modified; review manually before migration (state: %s)", hook.Name, hook.State))
				continue
			}
		}

		sourceKind, sourcePath, err := chooseHookMigrationWriteSource(hook)
		if err != nil {
			execPlan.BlockingErrors = append(execPlan.BlockingErrors, err.Error())
			continue
		}

		// GH#6348: when the source is a sidecar, its content — not the shim
		// that displaced it — decides what the migrated hook becomes. A
		// sidecar run by a non-shell interpreter is restored verbatim: no bd
		// section is injected (the interpreter could not run it), and the
		// sidecars are retained, not retired. Ambiguous bd markers in the
		// sidecar are never rewritten automatically; migration stops for that
		// hook and asks for manual inspection instead.
		if sourceKind == hookMigrationWriteFromOld || sourceKind == hookMigrationWriteFromBackup {
			content, readErr := os.ReadFile(sourcePath) // #nosec G304 -- sidecar path from migration planner
			if readErr == nil {
				if lang := doctor.ClassifyHookLanguage(content); lang.Kind != doctor.HookLanguageShell {
					if lang.Kind != doctor.HookLanguageBinary &&
						doctor.InspectBdSectionMarkers(string(content)) == doctor.BdSectionMarkersAmbiguous {
						execPlan.BlockingErrors = append(execPlan.BlockingErrors,
							fmt.Sprintf("%s: sidecar %s has bd markers that do not form complete, ordered pairs; inspect manually before migration (state: %s)", hook.Name, sourcePath, hook.State))
						continue
					}
					execPlan.WriteOps = append(execPlan.WriteOps, hookMigrationWriteOp{
						HookName:   hook.Name,
						HookPath:   hook.HookPath,
						State:      hook.State,
						SourceKind: hookMigrationWriteFromSidecarCopy,
						SourcePath: sourcePath,
					})
					continue
				}
			}
			// A read failure falls through: prepareHookMigrationWrites surfaces
			// it without writing anything.
		}

		execPlan.WriteOps = append(execPlan.WriteOps, hookMigrationWriteOp{
			HookName:   hook.Name,
			HookPath:   hook.HookPath,
			State:      hook.State,
			SourceKind: sourceKind,
			SourcePath: sourcePath,
		})

		if hook.HasOldSidecar {
			execPlan.RetireOps = append(execPlan.RetireOps, hookMigrationRetireOp{
				HookName:        hook.Name,
				SourcePath:      hook.HookPath + ".old",
				DestinationPath: hook.HookPath + ".old.migrated",
			})
		}
		if hook.HasBackupSidecar {
			execPlan.RetireOps = append(execPlan.RetireOps, hookMigrationRetireOp{
				HookName:        hook.Name,
				SourcePath:      hook.HookPath + ".backup",
				DestinationPath: hook.HookPath + ".backup.migrated",
			})
		}
	}

	return execPlan
}

func formatHookMigrationBlockingError(hook doctor.HookMigrationHookPlan) string {
	suggestion := strings.TrimSpace(hook.SuggestedAction)
	if suggestion == "" {
		suggestion = "Repair manually, then rerun migration"
	}
	if hook.ReadError != "" {
		return fmt.Sprintf("%s (%s): %s", hook.Name, hook.State, hook.ReadError)
	}
	return fmt.Sprintf("%s (%s): %s", hook.Name, hook.State, suggestion)
}

func chooseHookMigrationWriteSource(hook doctor.HookMigrationHookPlan) (hookMigrationWriteSource, string, error) {
	switch hook.State {
	case "legacy_only":
		return hookMigrationWriteFromTemplate, "", nil
	case "legacy_with_old_sidecar", "legacy_with_both_sidecars", "missing_with_old_sidecar", "missing_with_both_sidecars":
		return hookMigrationWriteFromOld, hook.HookPath + ".old", nil
	case "legacy_with_backup_sidecar", "missing_with_backup_sidecar":
		return hookMigrationWriteFromBackup, hook.HookPath + ".backup", nil
	case "custom_with_sidecars":
		return hookMigrationWriteFromHookFile, hook.HookPath, nil
	default:
		if hook.NeedsMigration {
			return "", "", fmt.Errorf("%s has unsupported migration state %q", hook.Name, hook.State)
		}
		return "", "", fmt.Errorf("%s does not require migration", hook.Name)
	}
}

type preparedHookWrite struct {
	HookName   string
	Path       string
	Content    []byte
	SourceKind hookMigrationWriteSource
	SourcePath string
}

func applyHookMigrationExecution(execPlan hookMigrationExecutionPlan) (hookMigrationApplySummary, error) {
	if len(execPlan.BlockingErrors) > 0 {
		return hookMigrationApplySummary{}, fmt.Errorf(
			"hook migration blocked by %d issue(s): %s",
			len(execPlan.BlockingErrors),
			strings.Join(execPlan.BlockingErrors, "; "),
		)
	}

	preparedWrites, err := prepareHookMigrationWrites(execPlan.WriteOps)
	if err != nil {
		return hookMigrationApplySummary{}, err
	}

	if err := validateRetireCollisionPolicy(execPlan.RetireOps); err != nil {
		return hookMigrationApplySummary{}, err
	}

	summary := hookMigrationApplySummary{
		WrittenHooks:     make([]string, 0, len(preparedWrites)),
		RetiredArtifacts: make([]string, 0, len(execPlan.RetireOps)),
		SkippedArtifacts: make([]string, 0),
	}

	for _, write := range preparedWrites {
		// GH#6348: a sidecar restored verbatim carries no bd section, so the
		// skip is reported here — the plan row alone does not say why.
		if write.SourceKind == hookMigrationWriteFromSidecarCopy {
			fmt.Fprintf(os.Stderr,
				"beads: hook '%s' restored from %s is run by a non-shell interpreter; no bd section was injected — call 'bd hooks run %s' from the hook itself, passing the hook's arguments and stdin\n",
				write.HookName, remedy.PrintedValue(write.SourcePath), write.HookName)
		}
		if err := guardHookWritePath(write.Path, false); err != nil {
			return summary, fmt.Errorf("refusing to write migrated hook %s: %w", write.HookName, err)
		}
		// #nosec G306 -- git hooks must be executable for Git to run them
		if err := os.WriteFile(write.Path, write.Content, 0755); err != nil {
			return summary, fmt.Errorf("writing migrated hook %s: %w", write.Path, err)
		}
		summary.WrittenHooks = append(summary.WrittenHooks, write.HookName)
	}

	for _, retire := range execPlan.RetireOps {
		retired, retiredErr := retireHookSidecar(retire)
		if retiredErr != nil {
			return summary, retiredErr
		}
		if retired == "" {
			summary.SkippedArtifacts = append(summary.SkippedArtifacts, retire.SourcePath)
			continue
		}
		summary.RetiredArtifacts = append(summary.RetiredArtifacts, retired)
	}

	summary.WrittenHookCount = len(summary.WrittenHooks)
	summary.RetiredCount = len(summary.RetiredArtifacts)
	summary.SkippedCount = len(summary.SkippedArtifacts)

	return summary, nil
}

func prepareHookMigrationWrites(writeOps []hookMigrationWriteOp) ([]preparedHookWrite, error) {
	prepared := make([]preparedHookWrite, 0, len(writeOps))

	for _, op := range writeOps {
		rendered, err := renderMigratedHookContent(op)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, preparedHookWrite{
			HookName:   op.HookName,
			Path:       op.HookPath,
			Content:    rendered,
			SourceKind: op.SourceKind,
			SourcePath: op.SourcePath,
		})
	}

	return prepared, nil
}

func renderMigratedHookContent(op hookMigrationWriteOp) ([]byte, error) {
	var baseContent string

	switch op.SourceKind {
	case hookMigrationWriteFromTemplate:
		baseContent = ""
	case hookMigrationWriteFromHookFileStrip:
		return renderNonShellSectionStrip(op)
	case hookMigrationWriteFromSidecarCopy:
		return renderSidecarVerbatimRestore(op)
	case hookMigrationWriteFromHookFile, hookMigrationWriteFromOld, hookMigrationWriteFromBackup:
		content, err := os.ReadFile(op.SourcePath) // #nosec G304 -- source paths come from migration planner + known sidecar suffixes
		if err != nil {
			return nil, fmt.Errorf("reading source content for %s from %s: %w", op.HookName, op.SourcePath, err)
		}
		baseContent = string(content)
	default:
		return nil, fmt.Errorf("unknown source kind %q for %s", op.SourceKind, op.HookName)
	}

	baseContent = strings.ReplaceAll(baseContent, "\r\n", "\n")
	baseContent = ensureHookShebang(baseContent)

	content := injectHookSection(baseContent, generateHookSection(op.HookName))
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	return []byte(content), nil
}

// renderNonShellSectionStrip repairs a non-shell hook that an older bd
// version corrupted by appending its POSIX-sh section (GH#6348). It
// re-validates the language classification and the marker pairs before
// reusing the remover, then strips the section: byte-exact — no shebang
// insertion, no newline normalization, no re-injection. The sidecars are
// untouched (the caller emits no retire ops for this source kind).
func renderNonShellSectionStrip(op hookMigrationWriteOp) ([]byte, error) {
	content, err := os.ReadFile(op.SourcePath) // #nosec G304 -- source paths come from migration planner + known sidecar suffixes
	if err != nil {
		return nil, fmt.Errorf("reading source content for %s from %s: %w", op.HookName, op.SourcePath, err)
	}
	if lang := doctor.ClassifyHookLanguage(content); lang.Kind == doctor.HookLanguageShell {
		return nil, fmt.Errorf("%s at %s no longer classifies as a non-shell hook; refusing to strip its bd section", op.HookName, op.SourcePath)
	}
	if doctor.InspectBdSectionMarkers(string(content)) != doctor.BdSectionMarkersPaired {
		return nil, fmt.Errorf("%s at %s: bd section markers no longer form complete, ordered pairs; inspect manually", op.HookName, op.SourcePath)
	}
	stripped, ok := stripBdSectionPairsValidated(string(content))
	if !ok {
		return nil, fmt.Errorf("%s at %s: stripping the stale bd section no longer validates; inspect manually", op.HookName, op.SourcePath)
	}
	return []byte(stripped), nil
}

// renderSidecarVerbatimRestore restores a non-shell sidecar as the active
// hook, verbatim (GH#6348): no bd section injected, no shebang ensured, no
// line endings normalized. It re-validates the classification before
// copying, and applies the same validated repair as installation when the
// sidecar itself carries a complete stale bd section an older bd version
// left in it — so the restored hook is clean.
func renderSidecarVerbatimRestore(op hookMigrationWriteOp) ([]byte, error) {
	content, err := os.ReadFile(op.SourcePath) // #nosec G304 -- source paths come from migration planner + known sidecar suffixes
	if err != nil {
		return nil, fmt.Errorf("reading source content for %s from %s: %w", op.HookName, op.SourcePath, err)
	}
	if lang := doctor.ClassifyHookLanguage(content); lang.Kind == doctor.HookLanguageShell {
		return nil, fmt.Errorf("%s sidecar %s no longer classifies as non-shell; refusing to restore it without a bd section", op.HookName, op.SourcePath)
	}
	if doctor.InspectBdSectionMarkers(string(content)) == doctor.BdSectionMarkersPaired {
		stripped, ok := stripBdSectionPairsValidated(string(content))
		if !ok {
			return nil, fmt.Errorf("%s sidecar %s has bd markers that no longer validate; inspect manually", op.HookName, op.SourcePath)
		}
		return []byte(stripped), nil
	}
	return content, nil
}

func ensureHookShebang(content string) string {
	if strings.HasPrefix(content, "#!") {
		return content
	}

	trimmedLeading := strings.TrimLeft(content, "\n")
	if trimmedLeading == "" {
		return "#!/usr/bin/env sh\n"
	}

	return "#!/usr/bin/env sh\n" + trimmedLeading
}

func validateRetireCollisionPolicy(retireOps []hookMigrationRetireOp) error {
	for _, op := range retireOps {
		sourceExists, err := pathExists(op.SourcePath)
		if err != nil {
			return fmt.Errorf("checking source sidecar %s: %w", op.SourcePath, err)
		}
		if !sourceExists {
			continue
		}

		destinationExists, err := pathExists(op.DestinationPath)
		if err != nil {
			return fmt.Errorf("checking destination sidecar %s: %w", op.DestinationPath, err)
		}
		if !destinationExists {
			continue
		}

		equal, err := filesEqual(op.SourcePath, op.DestinationPath)
		if err != nil {
			return fmt.Errorf("comparing sidecars %s and %s: %w", op.SourcePath, op.DestinationPath, err)
		}
		if !equal {
			return fmt.Errorf(
				"artifact collision for %s: %s already exists with different content",
				op.SourcePath,
				op.DestinationPath,
			)
		}
	}

	return nil
}

func retireHookSidecar(op hookMigrationRetireOp) (string, error) {
	sourceExists, err := pathExists(op.SourcePath)
	if err != nil {
		return "", fmt.Errorf("checking sidecar %s: %w", op.SourcePath, err)
	}
	if !sourceExists {
		return "", nil
	}

	destinationExists, err := pathExists(op.DestinationPath)
	if err != nil {
		return "", fmt.Errorf("checking sidecar destination %s: %w", op.DestinationPath, err)
	}

	if destinationExists {
		equal, err := filesEqual(op.SourcePath, op.DestinationPath)
		if err != nil {
			return "", fmt.Errorf("comparing sidecar %s to %s: %w", op.SourcePath, op.DestinationPath, err)
		}
		if !equal {
			return "", fmt.Errorf("artifact collision for %s: %s already exists with different content", op.SourcePath, op.DestinationPath)
		}
		if err := os.Remove(op.SourcePath); err != nil {
			return "", fmt.Errorf("removing already-retired sidecar %s: %w", op.SourcePath, err)
		}
		return op.SourcePath + " -> " + op.DestinationPath + " (destination already existed)", nil
	}

	if err := os.Rename(op.SourcePath, op.DestinationPath); err != nil {
		return "", fmt.Errorf("retiring sidecar %s -> %s: %w", op.SourcePath, op.DestinationPath, err)
	}

	return op.SourcePath + " -> " + op.DestinationPath, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func filesEqual(pathA, pathB string) (bool, error) {
	a, err := os.ReadFile(pathA) // #nosec G304 -- compared paths come from deterministic migration operations
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(pathB) // #nosec G304 -- compared paths come from deterministic migration operations
	if err != nil {
		return false, err
	}
	return bytes.Equal(a, b), nil
}

func confirmHookMigrationApply(totalOperations int) (bool, error) {
	fmt.Printf("\nThis will apply %d hook migration operation(s). Continue? (Y/n): ", totalOperations)
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("reading confirmation: %w", err)
	}
	response = strings.TrimSpace(strings.ToLower(response))
	if response == "" || response == "y" || response == "yes" {
		return true, nil
	}
	return false, nil
}
