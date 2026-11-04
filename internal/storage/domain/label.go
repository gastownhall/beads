package domain

import (
	"context"
	"fmt"
)

type LabelOpts struct {
	UseWispsTable bool
}

type LabelSQLRepository interface {
	Insert(ctx context.Context, issueID, label, actor string, opts LabelOpts) error
	Delete(ctx context.Context, issueID, label, actor string, opts LabelOpts) error
	List(ctx context.Context, issueID string, opts LabelOpts) ([]string, error)
	ListByIssueIDs(ctx context.Context, issueIDs []string, opts LabelOpts) (map[string][]string, error)
	DeleteAllForIDs(ctx context.Context, ids []string, opts LabelOpts) (int, error)
	CountAllForIDs(ctx context.Context, ids []string, opts LabelOpts) (int, error)
	// RenameLabel renames oldLabel to newLabel across every issue and wisp
	// that carries it - both label planes, not the one LabelOpts would
	// otherwise pick, since a rename by definition is not scoped to one id's
	// plane. See domain.LabelUseCase.RenameLabel for the return contract.
	RenameLabel(ctx context.Context, oldLabel, newLabel, actor string) (renamed, merged int, ids []string, err error)
}

type LabelUseCase interface {
	AddLabel(ctx context.Context, issueID, label, actor string) error
	RemoveLabel(ctx context.Context, issueID, label, actor string) error
	AddLabels(ctx context.Context, issueID string, labels []string, actor string) error
	RemoveLabels(ctx context.Context, issueID string, labels []string, actor string) error
	SetLabels(ctx context.Context, issueID string, labels []string, actor string) error
	GetLabels(ctx context.Context, issueID string) ([]string, error)
	GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error)
	InheritFromParent(ctx context.Context, childID, parentID, actor string, skipExisting []string) ([]string, error)

	// RenameLabel renames oldLabel to newLabel across every issue and wisp
	// that carries it - the proxied-server counterpart of
	// DoltStore/EmbeddedDoltStore.RenameLabel, carrying the same merge
	// semantics (an issue that already carries newLabel keeps it and drops
	// the stale oldLabel row instead of erroring) and emitting one
	// EventLabelRenamed per touched issue, matching the direct route event
	// for event, rather than an add+remove pair. renamed is the count of
	// issues and wisps that carried oldLabel; merged is the subset that
	// already had newLabel; ids lists every touched id, both planes
	// together. oldLabel and newLabel equal after trimming is refused with
	// issueops.ErrRenameLabelSameName.
	RenameLabel(ctx context.Context, oldLabel, newLabel, actor string) (renamed, merged int, ids []string, err error)

	AddWispLabel(ctx context.Context, wispID, label, actor string) error
	RemoveWispLabel(ctx context.Context, wispID, label, actor string) error
	AddWispLabels(ctx context.Context, wispID string, labels []string, actor string) error
	RemoveWispLabels(ctx context.Context, wispID string, labels []string, actor string) error
	SetWispLabels(ctx context.Context, wispID string, labels []string, actor string) error
	GetWispLabels(ctx context.Context, wispID string) ([]string, error)
	GetLabelsForWisps(ctx context.Context, wispIDs []string) (map[string][]string, error)
	InheritFromWispParent(ctx context.Context, childWispID, parentWispID, actor string, skipExisting []string) ([]string, error)
}

func NewLabelUseCase(labelRepo LabelSQLRepository) LabelUseCase {
	return &labelUseCaseImpl{labelRepo: labelRepo}
}

type labelUseCaseImpl struct {
	labelRepo LabelSQLRepository
}

var _ LabelUseCase = (*labelUseCaseImpl)(nil)

func (u *labelUseCaseImpl) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return u.add(ctx, issueID, label, actor, false)
}

func (u *labelUseCaseImpl) AddWispLabel(ctx context.Context, wispID, label, actor string) error {
	return u.add(ctx, wispID, label, actor, true)
}

func (u *labelUseCaseImpl) add(ctx context.Context, id, label, actor string, useWisp bool) error {
	if id == "" {
		return fmt.Errorf("add label: id must not be empty")
	}
	if label == "" {
		return fmt.Errorf("add label: label must not be empty")
	}
	if err := u.labelRepo.Insert(ctx, id, label, actor, LabelOpts{UseWispsTable: useWisp}); err != nil {
		return fmt.Errorf("add label %s/%s: %w", id, label, err)
	}
	return nil
}

// RenameLabel is not id-scoped like the verbs around it - a rename sweeps
// both label planes for every carrier of oldLabel, so it validates the two
// label arguments rather than an issue id and delegates straight to the
// repository, which owns the merge semantics and the same-name refusal.
func (u *labelUseCaseImpl) RenameLabel(ctx context.Context, oldLabel, newLabel, actor string) (renamed, merged int, ids []string, err error) {
	if oldLabel == "" {
		return 0, 0, nil, fmt.Errorf("rename label: oldLabel must not be empty")
	}
	if newLabel == "" {
		return 0, 0, nil, fmt.Errorf("rename label: newLabel must not be empty")
	}
	renamed, merged, ids, err = u.labelRepo.RenameLabel(ctx, oldLabel, newLabel, actor)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("rename label %s -> %s: %w", oldLabel, newLabel, err)
	}
	return renamed, merged, ids, nil
}

func (u *labelUseCaseImpl) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	return u.remove(ctx, issueID, label, actor, false)
}

func (u *labelUseCaseImpl) RemoveWispLabel(ctx context.Context, wispID, label, actor string) error {
	return u.remove(ctx, wispID, label, actor, true)
}

func (u *labelUseCaseImpl) remove(ctx context.Context, id, label, actor string, useWisp bool) error {
	if id == "" {
		return fmt.Errorf("remove label: id must not be empty")
	}
	if label == "" {
		return fmt.Errorf("remove label: label must not be empty")
	}
	if err := u.labelRepo.Delete(ctx, id, label, actor, LabelOpts{UseWispsTable: useWisp}); err != nil {
		return fmt.Errorf("remove label %s/%s: %w", id, label, err)
	}
	return nil
}

func (u *labelUseCaseImpl) AddLabels(ctx context.Context, issueID string, labels []string, actor string) error {
	return u.addMany(ctx, issueID, labels, actor, false)
}

func (u *labelUseCaseImpl) AddWispLabels(ctx context.Context, wispID string, labels []string, actor string) error {
	return u.addMany(ctx, wispID, labels, actor, true)
}

func (u *labelUseCaseImpl) addMany(ctx context.Context, id string, labels []string, actor string, useWisp bool) error {
	if id == "" {
		return fmt.Errorf("add labels: id must not be empty")
	}
	opts := LabelOpts{UseWispsTable: useWisp}
	for _, label := range labels {
		if label == "" {
			continue
		}
		if err := u.labelRepo.Insert(ctx, id, label, actor, opts); err != nil {
			return fmt.Errorf("add labels: %s: %w", label, err)
		}
	}
	return nil
}

func (u *labelUseCaseImpl) RemoveLabels(ctx context.Context, issueID string, labels []string, actor string) error {
	return u.removeMany(ctx, issueID, labels, actor, false)
}

func (u *labelUseCaseImpl) RemoveWispLabels(ctx context.Context, wispID string, labels []string, actor string) error {
	return u.removeMany(ctx, wispID, labels, actor, true)
}

func (u *labelUseCaseImpl) removeMany(ctx context.Context, id string, labels []string, actor string, useWisp bool) error {
	if id == "" {
		return fmt.Errorf("remove labels: id must not be empty")
	}
	opts := LabelOpts{UseWispsTable: useWisp}
	for _, label := range labels {
		if label == "" {
			continue
		}
		if err := u.labelRepo.Delete(ctx, id, label, actor, opts); err != nil {
			return fmt.Errorf("remove labels: %s: %w", label, err)
		}
	}
	return nil
}

func (u *labelUseCaseImpl) SetLabels(ctx context.Context, issueID string, labels []string, actor string) error {
	return u.setMany(ctx, issueID, labels, actor, false)
}

func (u *labelUseCaseImpl) SetWispLabels(ctx context.Context, wispID string, labels []string, actor string) error {
	return u.setMany(ctx, wispID, labels, actor, true)
}

func (u *labelUseCaseImpl) setMany(ctx context.Context, id string, labels []string, actor string, useWisp bool) error {
	if id == "" {
		return fmt.Errorf("set labels: id must not be empty")
	}
	opts := LabelOpts{UseWispsTable: useWisp}
	current, err := u.labelRepo.List(ctx, id, opts)
	if err != nil {
		return fmt.Errorf("set labels: list current: %w", err)
	}
	desired := make(map[string]bool, len(labels))
	for _, l := range labels {
		if l != "" {
			desired[l] = true
		}
	}
	existing := make(map[string]bool, len(current))
	for _, l := range current {
		existing[l] = true
		if !desired[l] {
			if err := u.labelRepo.Delete(ctx, id, l, actor, opts); err != nil {
				return fmt.Errorf("set labels: remove %s: %w", l, err)
			}
		}
	}
	for l := range desired {
		if !existing[l] {
			if err := u.labelRepo.Insert(ctx, id, l, actor, opts); err != nil {
				return fmt.Errorf("set labels: add %s: %w", l, err)
			}
		}
	}
	return nil
}

func (u *labelUseCaseImpl) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	return u.list(ctx, issueID, false)
}

func (u *labelUseCaseImpl) GetWispLabels(ctx context.Context, wispID string) ([]string, error) {
	return u.list(ctx, wispID, true)
}

func (u *labelUseCaseImpl) list(ctx context.Context, id string, useWisp bool) ([]string, error) {
	if id == "" {
		return nil, fmt.Errorf("get labels: id must not be empty")
	}
	out, err := u.labelRepo.List(ctx, id, LabelOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("get labels %s: %w", id, err)
	}
	return out, nil
}

func (u *labelUseCaseImpl) GetLabelsForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	return u.listBulk(ctx, issueIDs, false)
}

func (u *labelUseCaseImpl) GetLabelsForWisps(ctx context.Context, wispIDs []string) (map[string][]string, error) {
	return u.listBulk(ctx, wispIDs, true)
}

func (u *labelUseCaseImpl) listBulk(ctx context.Context, ids []string, useWisp bool) (map[string][]string, error) {
	if len(ids) == 0 {
		return map[string][]string{}, nil
	}
	out, err := u.labelRepo.ListByIssueIDs(ctx, ids, LabelOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("get labels bulk: %w", err)
	}
	return out, nil
}

func (u *labelUseCaseImpl) InheritFromParent(ctx context.Context, childID, parentID, actor string, skipExisting []string) ([]string, error) {
	return u.inherit(ctx, childID, parentID, actor, skipExisting, false)
}

func (u *labelUseCaseImpl) InheritFromWispParent(ctx context.Context, childWispID, parentWispID, actor string, skipExisting []string) ([]string, error) {
	return u.inherit(ctx, childWispID, parentWispID, actor, skipExisting, true)
}

func (u *labelUseCaseImpl) inherit(ctx context.Context, childID, parentID, actor string, skipExisting []string, useWisp bool) ([]string, error) {
	if childID == "" {
		return nil, fmt.Errorf("inherit labels: childID must not be empty")
	}
	if parentID == "" {
		return nil, fmt.Errorf("inherit labels: parentID must not be empty")
	}
	parentLabels, err := u.labelRepo.List(ctx, parentID, LabelOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("inherit labels: list parent %s: %w", parentID, err)
	}
	if len(parentLabels) == 0 {
		return nil, nil
	}
	skip := make(map[string]bool, len(skipExisting))
	for _, s := range skipExisting {
		skip[s] = true
	}
	var inherited []string
	for _, label := range parentLabels {
		if skip[label] {
			continue
		}
		if err := u.labelRepo.Insert(ctx, childID, label, actor, LabelOpts{UseWispsTable: useWisp}); err != nil {
			return inherited, fmt.Errorf("inherit labels: insert %s on %s: %w", label, childID, err)
		}
		inherited = append(inherited, label)
	}
	return inherited, nil
}
