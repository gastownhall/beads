package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor"
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
		case "marker_managed", "unmanaged_custom", "missing_no_artifacts":
			execPlan.NoopHooks = append(execPlan.NoopHooks, hook.Name)
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
	HookName string
	Path     string
	Content  []byte
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
			HookName: op.HookName,
			Path:     op.HookPath,
			Content:  rendered,
		})
	}

	return prepared, nil
}

func renderMigratedHookContent(op hookMigrationWriteOp) ([]byte, error) {
	var baseContent string

	switch op.SourceKind {
	case hookMigrationWriteFromTemplate:
		baseContent = ""
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
