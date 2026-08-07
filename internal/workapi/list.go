package workapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/issueops"
)

// DefaultListLimit is the default number of rows a list query returns when the
// caller does not ask for a specific limit. Every frontend registers its limit
// knob from this constant so the surfaces cannot drift apart; 0 still means
// unlimited.
const DefaultListLimit = 50

// PageLimit is the number of rows a list request asks to RECEIVE.
func PageLimit(in issueops.ListRequest) int {
	return LimitOr(in.Limit, DefaultListLimit)
}

// SQLLimit is the row limit pushed into the query, which can differ from the
// page limit: `--sort id` needs natural-numeric comparison (bd-9 < bd-10) that
// SQL cannot express without a schema-side sort column, so that ordering
// fetches the whole result set and trims in Go afterwards.
//
// This lives beside the builder rather than in a caller because it is the
// second half of one decision — the first half is what the filter carries —
// and splitting a decision across a library and its callers is how the two
// halves drift.
func SQLLimit(in issueops.ListRequest) int {
	if in.SortBy == "id" {
		return 0
	}
	return PageLimit(in)
}

// ListConfig is the store-derived configuration BuildListFilter needs: the
// workspace's custom statuses and types, plus its infra-type set.
type ListConfig struct {
	CustomStatuses []types.CustomStatus
	CustomTypes    []string
	InfraSet       map[string]bool
}

func (c ListConfig) CustomStatusNames() []string {
	out := make([]string, len(c.CustomStatuses))
	for i, s := range c.CustomStatuses {
		out[i] = s.Name
	}
	return out
}

func (c ListConfig) InfraTypes() []string {
	if len(c.InfraSet) == 0 {
		return domain.DefaultInfraTypes()
	}
	out := make([]string, 0, len(c.InfraSet))
	for t := range c.InfraSet {
		out = append(out, t)
	}
	return out
}

func (c ListConfig) IsInfra(t string) bool {
	if len(c.InfraSet) == 0 {
		return domain.IsInfraType(types.IssueType(t))
	}
	return c.InfraSet[t]
}

// ConfigSource reads the workspace configuration BuildListFilter depends on.
// It is the seam between the filter logic and however a given frontend reaches
// storage (a direct store handle, a unit of work, ...).
type ConfigSource interface {
	GetCustomStatuses(ctx context.Context) ([]types.CustomStatus, error)
	GetCustomTypes(ctx context.Context) ([]string, error)
	GetInfraTypes(ctx context.Context) (map[string]bool, error)
}

type storeConfigSource struct{ store storage.DoltStorage }

// NewStoreConfigSource reads list configuration straight from a store handle.
func NewStoreConfigSource(store storage.DoltStorage) ConfigSource {
	return storeConfigSource{store: store}
}

func (d storeConfigSource) GetCustomStatuses(ctx context.Context) ([]types.CustomStatus, error) {
	return d.store.GetCustomStatusesDetailed(ctx)
}
func (d storeConfigSource) GetCustomTypes(ctx context.Context) ([]string, error) {
	return d.store.GetCustomTypes(ctx)
}
func (d storeConfigSource) GetInfraTypes(ctx context.Context) (map[string]bool, error) {
	return d.store.GetInfraTypes(ctx), nil
}

// UnitOfWork is the slice of an open unit of work this package reads through.
//
// It is declared here, structurally, rather than imported from
// internal/storage/uow: that package's provider implements the Reader role and
// therefore imports this one, so naming its interface here would close an
// import cycle. uow.UnitOfWork satisfies it without an adapter, which is what
// keeps the seam honest — this is the same object, described by what the
// queries need from it.
type UnitOfWork interface {
	ConfigUseCase() domain.ConfigUseCase
	IssueUseCase() domain.IssueUseCase
	DependencyUseCase() domain.DependencyUseCase
	LabelUseCase() domain.LabelUseCase
	CommentUseCase() domain.CommentUseCase
}

type uowConfigSource struct{ uw UnitOfWork }

// NewUOWConfigSource reads list configuration through an open unit of work.
func NewUOWConfigSource(uw UnitOfWork) ConfigSource {
	return uowConfigSource{uw: uw}
}

func (p uowConfigSource) GetCustomStatuses(ctx context.Context) ([]types.CustomStatus, error) {
	return p.uw.ConfigUseCase().GetCustomStatuses(ctx)
}
func (p uowConfigSource) GetCustomTypes(ctx context.Context) ([]string, error) {
	return p.uw.ConfigUseCase().GetCustomTypes(ctx)
}
func (p uowConfigSource) GetInfraTypes(ctx context.Context) (map[string]bool, error) {
	return p.uw.ConfigUseCase().GetInfraTypes(ctx)
}

// LoadListConfig materializes the list configuration from a ConfigSource,
// falling back to the workspace YAML for custom types the store does not know.
func LoadListConfig(ctx context.Context, src ConfigSource) (ListConfig, error) {
	var cfg ListConfig

	statuses, err := src.GetCustomStatuses(ctx)
	if err != nil {
		return cfg, fmt.Errorf("load custom statuses: %w", err)
	}
	cfg.CustomStatuses = statuses

	ct, err := src.GetCustomTypes(ctx)
	if err != nil {
		return cfg, fmt.Errorf("load custom types: %w", err)
	}
	if len(ct) > 0 {
		cfg.CustomTypes = ct
	} else {
		cfg.CustomTypes = config.GetCustomTypesFromYAML()
	}

	infraSet, err := src.GetInfraTypes(ctx)
	if err != nil {
		return cfg, fmt.Errorf("load infra types: %w", err)
	}
	if len(infraSet) > 0 {
		cfg.InfraSet = infraSet
	}

	return cfg, nil
}

// LoadStoreListConfig loads the list configuration from a store handle. A nil
// store (no workspace open) still yields the YAML custom types.
func LoadStoreListConfig(ctx context.Context, store storage.DoltStorage) (ListConfig, error) {
	if store == nil {
		return ListConfig{CustomTypes: config.GetCustomTypesFromYAML()}, nil
	}
	return LoadListConfig(ctx, NewStoreConfigSource(store))
}

// LoadUOWListConfig loads the list configuration through an open unit of work.
func LoadUOWListConfig(ctx context.Context, uw UnitOfWork) (ListConfig, error) {
	return LoadListConfig(ctx, NewUOWConfigSource(uw))
}

// BuildListFilter turns list parameters into the storage-level filter. It is
// the single definition of what `bd list` means: the closed/done/frozen status
// exclusions, the pinned and template defaults, and the gate, infra-type, and
// wisp suppression that make the default listing show durable work only.
func BuildListFilter(in issueops.ListRequest, cfg ListConfig) (types.IssueFilter, error) {
	// The --ready arm reaches its query through ReadyFilterFromIssueFilter,
	// which carries only part of what this filter can express. A request that
	// asks --ready to honor something the projection drops is refused here,
	// at the one point every frontend and every implementation of
	// issueops.Reader passes through, rather than answered with the wider set.
	// The drop set and the refusal text live beside the promise they enforce,
	// in issueops.
	if err := issueops.ValidateReadyFlagScope(in); err != nil {
		return types.IssueFilter{}, err
	}

	filter := types.IssueFilter{
		Limit:          SQLLimit(in),
		Offset:         in.Offset,
		SortBy:         in.SortBy,
		SortDesc:       in.Reverse,
		AfterCreatedAt: in.AfterCreatedAt,
		AfterID:        in.AfterID,
		// The defensive cap travels ON the request, so this builder is the
		// only writer of the filter's two cap fields. `bd list` used to stamp
		// them onto the filter after the builder returned, which is the
		// "build it, then reach in and change it" half-step the role exists to
		// make unreachable — and it left the cap invisible to every
		// implementation of Reader.List.
		MaxRows:       in.MaxRows,
		MaxRowsSource: in.MaxRowsSource,
	}

	if in.ReadyFlag {
		s := types.StatusOpen
		filter.Status = &s
	} else if in.Status != "" && in.Status != "all" {
		if err := ApplyStatusFilter(&filter, in.Status, cfg.CustomStatusNames()); err != nil {
			return filter, err
		}
	}

	if in.Status == "" && !in.AllFlag && !in.ReadyFlag && !in.PinnedFlag {
		excludeStatuses := []types.Status{types.StatusClosed, types.StatusPinned}
		for _, cs := range cfg.CustomStatuses {
			if cs.Category == types.CategoryDone || cs.Category == types.CategoryFrozen {
				excludeStatuses = append(excludeStatuses, types.Status(cs.Name))
			}
		}
		filter.ExcludeStatus = excludeStatuses
	}

	if in.Priority != nil {
		p := *in.Priority
		filter.Priority = &p
	}
	if in.Assignee != "" {
		a := in.Assignee
		filter.Assignee = &a
	}
	if in.IssueType != "" {
		t := types.IssueType(in.IssueType)
		if !t.IsValidWithCustom(cfg.CustomTypes) {
			validTypes := "bug, feature, task, epic, chore, decision"
			if len(cfg.CustomTypes) > 0 {
				validTypes += ", " + strings.Join(cfg.CustomTypes, ", ")
			}
			return filter, fmt.Errorf("invalid issue type %q (valid: %s)", in.IssueType, validTypes)
		}
		filter.IssueType = &t
	}

	if len(in.Labels) > 0 {
		filter.Labels = in.Labels
	}
	if len(in.LabelsAny) > 0 {
		filter.LabelsAny = in.LabelsAny
	}
	if len(in.ExcludeLabels) > 0 {
		filter.ExcludeLabels = in.ExcludeLabels
	}
	if in.LabelPattern != "" {
		filter.LabelPattern = in.LabelPattern
	}
	if in.LabelRegex != "" {
		filter.LabelRegex = in.LabelRegex
	}
	if in.TitleSearch != "" {
		filter.TitleSearch = in.TitleSearch
	}
	if in.IDFilter != "" {
		ids := utils.NormalizeLabels(strings.Split(in.IDFilter, ","))
		if len(ids) > 0 {
			filter.IDs = ids
		}
	}
	if in.SpecPrefix != "" {
		filter.SpecIDPrefix = in.SpecPrefix
	}

	if in.TitleContains != "" {
		filter.TitleContains = in.TitleContains
	}
	if in.DescContains != "" {
		filter.DescriptionContains = in.DescContains
	}
	if in.NotesContains != "" {
		filter.NotesContains = in.NotesContains
	}
	if in.ExternalContains != "" {
		filter.ExternalRefContains = in.ExternalContains
	}
	if in.ExternalRef != "" {
		filter.ExternalRef = &in.ExternalRef
	}

	filter.CreatedAfter = in.CreatedAfter
	filter.CreatedBefore = in.CreatedBefore
	filter.UpdatedAfter = in.UpdatedAfter
	filter.UpdatedBefore = in.UpdatedBefore
	filter.ClosedAfter = in.ClosedAfter
	filter.ClosedBefore = in.ClosedBefore

	if in.EmptyDesc {
		filter.EmptyDescription = true
	}
	if in.NoAssignee {
		filter.NoAssignee = true
	}
	if in.NoLabels {
		filter.NoLabels = true
	}
	if in.SkipLabels {
		filter.SkipLabels = true
	}
	if in.SkipCounts {
		filter.SkipCounts = true
	}

	if in.PriorityMin != nil {
		p := *in.PriorityMin
		filter.PriorityMin = &p
	}
	if in.PriorityMax != nil {
		p := *in.PriorityMax
		filter.PriorityMax = &p
	}

	if in.PinnedFlag {
		pinned := true
		filter.Pinned = &pinned
	} else if in.NoPinnedFlag || (in.Status != "pinned" && in.Status != "hooked" && !in.AllFlag) {
		pinned := false
		filter.Pinned = &pinned
	}

	if !in.IncludeTemplates {
		isTemplate := false
		filter.IsTemplate = &isTemplate
	}

	if !in.IncludeGates && in.IssueType != "gate" {
		filter.ExcludeTypes = append(filter.ExcludeTypes, "gate")
	}

	if !in.IncludeInfra && !cfg.IsInfra(in.IssueType) {
		for _, t := range cfg.InfraTypes() {
			filter.ExcludeTypes = append(filter.ExcludeTypes, types.IssueType(t))
		}
	}

	for _, raw := range in.ExcludeTypes {
		for _, t := range strings.Split(raw, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				filter.ExcludeTypes = append(filter.ExcludeTypes, types.IssueType(utils.NormalizeIssueType(t)))
			}
		}
	}

	if cfg.IsInfra(in.IssueType) {
		ephemeral := true
		filter.Ephemeral = &ephemeral
	}

	if in.ParentID != "" {
		pid := in.ParentID
		filter.ParentID = &pid
	}
	if in.NoParent {
		filter.NoParent = true
	}

	if in.MolType != nil {
		filter.MolType = in.MolType
	}
	if in.WispType != nil {
		filter.WispType = in.WispType
	}

	if in.DeferredFlag {
		filter.Deferred = true
	}
	filter.DeferAfter = in.DeferAfter
	filter.DeferBefore = in.DeferBefore
	filter.DueAfter = in.DueAfter
	filter.DueBefore = in.DueBefore
	if in.OverdueFlag {
		filter.Overdue = true
	}

	if len(in.MetadataFields) > 0 {
		filter.MetadataFields = in.MetadataFields
	}
	if in.HasMetadataKey != "" {
		filter.HasMetadataKey = in.HasMetadataKey
	}
	if err := ValidateMetadataFilters(in.MetadataFields, in.HasMetadataKey); err != nil {
		return filter, err
	}

	if !in.IncludeInfra && (in.IssueType == "" || !cfg.IsInfra(in.IssueType)) {
		filter.SkipWisps = true
	}

	return filter, nil
}

// ValidStatusList renders the status names a filter accepts, for error text.
func ValidStatusList(customStatusNames []string) string {
	validList := "open, in_progress, blocked, deferred, closed, pinned, hooked"
	if len(customStatusNames) > 0 {
		validList += ", " + strings.Join(customStatusNames, ", ")
	}
	return validList
}

// ApplyStatusFilter parses a status selector - one status, or a
// comma-separated OR set - onto the filter.
func ApplyStatusFilter(filter *types.IssueFilter, status string, customStatusNames []string) error {
	statusParts := strings.Split(status, ",")
	if len(statusParts) == 1 {
		s := types.Status(strings.TrimSpace(statusParts[0]))
		if !s.IsValidWithCustom(customStatusNames) {
			return fmt.Errorf("invalid status %q (valid: %s)", status, ValidStatusList(customStatusNames))
		}
		filter.Status = &s
		return nil
	}

	for _, part := range statusParts {
		s := types.Status(strings.TrimSpace(part))
		if !s.IsValidWithCustom(customStatusNames) {
			return fmt.Errorf("invalid status %q in multi-status filter (valid: %s)", strings.TrimSpace(part), ValidStatusList(customStatusNames))
		}
		filter.Statuses = append(filter.Statuses, s)
	}
	return nil
}
