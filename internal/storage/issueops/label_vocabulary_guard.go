package issueops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// LabelsVocabularyConfigKey is the workspace knob controlling whether an
// undefined label is accepted silently (open, the default), warned about
// (warn), or refused (enforce). The spelling is owned HERE, at the layer that
// enforces it; cmd/bd's interactive checker aliases this constant rather than
// re-declaring the string (data-integrity 0.0: one authority per concept).
const LabelsVocabularyConfigKey = "labels.vocabulary"

// LabelsVocabularyEnforce is the only mode this layer acts on. warn stays an
// interactive-edge concern: a guarded mutation has no stderr to warn on, and
// inventing a warning channel in every result type is an API change this
// guard must not smuggle in. open and warn both pass here.
const LabelsVocabularyEnforce = "enforce"

// checkLabelVocabularyForGuardedWriteInTx refuses, under
// labels.vocabulary=enforce, any label in candidates that is not in the
// defined vocabulary (case-insensitively, exact defined spelling required -
// the same predicate as cmd/bd's checkLabelVocabulary).
//
// WHY HERE (bda-yxac): enforcement used to live only in selected CLI call
// sites, so the same patch was refused by `bd label add` and accepted over
// HTTP/MCP - the policy depended on which front door served the write. This
// function runs inside the guarded-mutation transaction (ExecuteCreate,
// ExecuteUpdate, ExecuteCreateBatch), the chokepoint every guarded route
// reaches on every backend, so the front doors cannot disagree.
//
// WHAT IS EXEMPT, deliberately:
//   - import/replay: the importer writes through CreateIssuesInTxWithResult
//     directly, not through the guarded verbs, so an undefined label in an
//     imported snapshot is always accepted silently regardless of mode (the
//     same contract cmd/bd documents for its interactive checker).
//   - removals: candidates must already exclude labels the patch removes -
//     a legacy label written before enforce was configured must stay
//     deletable, or the mode locks out its own cleanup.
//
// It fails open on any read error (config unreadable, registry unreadable,
// table absent on a pre-migration workspace): a transient problem reading the
// vocabulary must not block a write that has nothing to do with it.
func checkLabelVocabularyForGuardedWriteInTx(ctx context.Context, tx DBTX, candidates []string) error {
	if len(candidates) == 0 {
		return nil
	}
	cfg, err := getConfigKeysInTx(ctx, tx, LabelsVocabularyConfigKey)
	if err != nil || cfg[LabelsVocabularyConfigKey] != LabelsVocabularyEnforce {
		return nil
	}
	defs, err := ListLabelDefinitionsInTx(ctx, tx)
	if err != nil {
		return nil
	}
	return CheckLabelVocabularyAgainst(defs, candidates)
}

// checkLabelRenameVocabularyInTx is the rename path's in-transaction
// vocabulary guard (bda-6x7o). A rename is an add of newLabel plus a removal
// of oldLabel, so only newLabel is judged: renaming AWAY from an undefined
// label onto a defined one is the cleanup path and must stay open under
// enforce. It runs inside RenameLabelAndDefinitionInTx, which every backend
// (embedded, dolt, domain/db) reaches, so the refusal does not depend on the
// front door. Same fail-open and enforce-only contract as
// checkLabelVocabularyForGuardedWriteInTx.
func checkLabelRenameVocabularyInTx(ctx context.Context, tx DBTX, oldLabel, newLabel string) error {
	cfg, err := getConfigKeysInTx(ctx, tx, LabelsVocabularyConfigKey)
	if err != nil || cfg[LabelsVocabularyConfigKey] != LabelsVocabularyEnforce {
		return nil
	}
	defs, err := ListLabelDefinitionsInTx(ctx, tx)
	if err != nil {
		return nil
	}
	return CheckLabelVocabularyAgainst(defs, LabelRenameVocabularyCandidates(defs, oldLabel, newLabel))
}

// LabelRenameVocabularyCandidates lists the labels a rename of oldLabel to
// newLabel must have judged against the vocabulary defs, evaluated on the
// state BEFORE the rename. The answer is newLabel, except when the rename
// itself carries oldLabel's definition onto newLabel (renameLabelDefinitionInTx):
// oldLabel is defined under exactly that spelling and newLabel's folded key
// is either free or the same key (a case-only respelling). Then newLabel ends
// the transaction defined under exactly its own spelling and there is nothing
// to judge. When newLabel's folded key is held by a different definition the
// registry merges into that row and keeps its spelling, so newLabel is judged
// as written and a spelling mismatch is refused with the usual suggestion.
func LabelRenameVocabularyCandidates(defs []types.LabelDefinition, oldLabel, newLabel string) []string {
	known := LabelVocabularySet(defs)
	oldFolded := strings.ToLower(oldLabel)
	newFolded := strings.ToLower(newLabel)
	if spelling, ok := known[oldFolded]; ok && spelling == oldLabel {
		if _, taken := known[newFolded]; !taken || oldFolded == newFolded {
			return nil
		}
	}
	return []string{newLabel}
}

// CheckLabelVocabularyAgainst is the pure predicate-and-message half of the
// guard, shared with the uow backend's guarded verbs (which read config and
// definitions through their own use cases rather than a transaction handle).
// One authority for what "undefined under enforce" MEANS and how the refusal
// reads; the callers own only the reads (quality-standards 0.00).
func CheckLabelVocabularyAgainst(defs []types.LabelDefinition, candidates []string) error {
	known := LabelVocabularySet(defs)

	undefinedSet := make(map[string]struct{})
	var undefined []string
	for _, label := range candidates {
		if spelling, ok := known[strings.ToLower(label)]; ok && spelling == label {
			continue
		}
		if _, seen := undefinedSet[label]; seen {
			continue
		}
		undefinedSet[label] = struct{}{}
		undefined = append(undefined, label)
	}
	if len(undefined) == 0 {
		return nil
	}
	sort.Strings(undefined)
	quoted := make([]string, len(undefined))
	for i, label := range undefined {
		if suggestion, ok := known[strings.ToLower(label)]; ok {
			quoted[i] = fmt.Sprintf("%q (did you mean %q?)", label, suggestion)
		} else {
			quoted[i] = fmt.Sprintf("%q", label)
		}
	}
	return fmt.Errorf("%w: undefined label(s) not in the vocabulary: %s (define with 'bd label define <label>', or disable with 'bd config set %s open')",
		storage.ErrValidation, strings.Join(quoted, ", "), LabelsVocabularyConfigKey)
}

// guardedLabelPatchCandidates lists the labels a LabelPatch would WRITE:
// Replace's starting set and Add, minus Remove (removal wins, so a label
// named in both Add and Remove never lands and must not be judged).
func GuardedLabelPatchCandidates(replaceSet bool, replace, add, remove []string) []string {
	if !replaceSet && len(add) == 0 {
		return nil
	}
	removing := make(map[string]struct{}, len(remove))
	for _, label := range remove {
		removing[label] = struct{}{}
	}
	var candidates []string
	for _, label := range replace {
		if _, dropped := removing[label]; !dropped {
			candidates = append(candidates, label)
		}
	}
	for _, label := range add {
		if _, dropped := removing[label]; !dropped {
			candidates = append(candidates, label)
		}
	}
	return candidates
}
