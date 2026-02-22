package query

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/timeparsing"
	"github.com/steveyegge/beads/internal/types"
)

// QueryResult contains the result of evaluating a query.
// For simple queries, Filter will be populated and Predicate will be nil.
// For complex queries with OR, Predicate will be set and Filter will contain
// base filters that can pre-filter issues.
type QueryResult struct {
	// Filter contains filters that can be passed to SearchIssues.
	// This is always populated with at least base filters.
	Filter types.IssueFilter

	// Predicate is a function that evaluates whether an issue matches the query.
	// If nil, the Filter alone is sufficient.
	// If non-nil, issues matching Filter should be further filtered by Predicate.
	Predicate func(*types.Issue) bool

	// RequiresPredicate indicates if in-memory filtering is needed.
	// True when the query contains OR or complex NOT expressions.
	RequiresPredicate bool
}

// Evaluator converts a query AST to an IssueFilter and/or predicate function.
type Evaluator struct {
	now time.Time
}

// NewEvaluator creates a new Evaluator with the given reference time.
func NewEvaluator(now time.Time) *Evaluator {
	return &Evaluator{now: now}
}

// Evaluate evaluates the query AST and returns a QueryResult.
func (e *Evaluator) Evaluate(node Node) (*QueryResult, error) {
	result := &QueryResult{
		Filter: types.IssueFilter{},
	}

	// Check if we can use Filter-only mode (simple AND chains)
	if e.canUseFilterOnly(node) {
		if err := e.buildFilter(node, &result.Filter); err != nil {
			return nil, err
		}
		return result, nil
	}

	// Complex query: build predicate and extract base filters
	pred, err := e.buildPredicate(node)
	if err != nil {
		return nil, err
	}
	result.Predicate = pred
	result.RequiresPredicate = true

	// Extract base filters for pre-filtering (optional optimization)
	e.extractBaseFilters(node, &result.Filter)

	return result, nil
}

// canUseFilterOnly returns true if the query can be expressed as IssueFilter only.
// This is true for:
// - Simple comparisons
// - AND chains of simple comparisons
// - NOT with certain fields
func (e *Evaluator) canUseFilterOnly(node Node) bool {
	switch n := node.(type) {
	case *ComparisonNode:
		return true
	case *AndNode:
		return e.canUseFilterOnly(n.Left) && e.canUseFilterOnly(n.Right)
	case *NotNode:
		// NOT is only filter-compatible for certain fields
		if comp, ok := n.Operand.(*ComparisonNode); ok {
			switch comp.Field {
			case "status":
				return comp.Op == OpEquals
			case "type":
				return comp.Op == OpEquals
			default:
				return false
			}
		}
		return false
	case *OrNode:
		// OR can be filter-compatible for labels
		return e.canUseLabelsAnyOptimization(n)
	default:
		return false
	}
}

// canUseLabelsAnyOptimization checks if an OR node can use LabelsAny.
func (e *Evaluator) canUseLabelsAnyOptimization(node *OrNode) bool {
	labels := e.collectOrLabels(node)
	return len(labels) > 0
}

// collectOrLabels collects label values from an OR chain of label=X comparisons.
// Returns nil if the OR chain contains non-label comparisons.
func (e *Evaluator) collectOrLabels(node Node) []string {
	switch n := node.(type) {
	case *ComparisonNode:
		if (n.Field == "label" || n.Field == "labels") && n.Op == OpEquals {
			return []string{n.Value}
		}
		return nil
	case *OrNode:
		left := e.collectOrLabels(n.Left)
		right := e.collectOrLabels(n.Right)
		if left == nil || right == nil {
			return nil
		}
		return append(left, right...)
	default:
		return nil
	}
}

// buildFilter populates the IssueFilter from a filter-compatible AST.
func (e *Evaluator) buildFilter(node Node, filter *types.IssueFilter) error {
	switch n := node.(type) {
	case *ComparisonNode:
		return e.applyComparison(n, filter)
	case *AndNode:
		if err := e.buildFilter(n.Left, filter); err != nil {
			return err
		}
		return e.buildFilter(n.Right, filter)
	case *NotNode:
		return e.applyNot(n, filter)
	case *OrNode:
		// Only reached for LabelsAny optimization
		labels := e.collectOrLabels(n)
		if labels != nil {
			filter.LabelsAny = append(filter.LabelsAny, labels...)
			return nil
		}
		return fmt.Errorf("OR not supported for this field combination")
	default:
		return fmt.Errorf("unexpected node type: %T", node)
	}
}

// applyComparison applies a comparison to the filter.
func (e *Evaluator) applyComparison(comp *ComparisonNode, filter *types.IssueFilter) error {
	switch comp.Field {
	case "status":
		return e.applyStatusFilter(comp, filter)
	case "priority":
		return e.applyPriorityFilter(comp, filter)
	case "type":
		return e.applyTypeFilter(comp, filter)
	case "assignee":
		return e.applyAssigneeFilter(comp, filter)
	case "owner":
		return e.applyOwnerFilter(comp, filter)
	case "label", "labels":
		return e.applyLabelFilter(comp, filter)
	case "title":
		return e.applyTitleFilter(comp, filter)
	case "description", "desc":
		return e.applyDescriptionFilter(comp, filter)
	case "notes":
		return e.applyNotesFilter(comp, filter)
	case "created", "created_at":
		return e.applyCreatedFilter(comp, filter)
	case "updated", "updated_at":
		return e.applyUpdatedFilter(comp, filter)
	case "closed", "closed_at":
		return e.applyClosedFilter(comp, filter)
	case "id":
		return e.applyIDFilter(comp, filter)
	case "spec", "spec_id":
		return e.applySpecFilter(comp, filter)
	case "parent":
		return e.applyParentFilter(comp, filter)
	case "pinned":
		return e.applyBoolFilter(comp, filter, "pinned")
	case "ephemeral":
		return e.applyBoolFilter(comp, filter, "ephemeral")
	case "template":
		return e.applyBoolFilter(comp, filter, "template")
	case "mol_type":
		return e.applyMolTypeFilter(comp, filter)
	default:
		if strings.HasPrefix(comp.Field, "metadata.") {
			return e.applyMetadataFilter(comp, filter)
		}
		return fmt.Errorf("unknown field: %s", comp.Field)
	}
}

func (e *Evaluator) applyStatusFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals && comp.Op != OpNotEquals {
		return fmt.Errorf("status only supports = and != operators")
	}
	status := types.Status(strings.ToLower(comp.Value))
	if !status.IsValid() {
		return fmt.Errorf("invalid status: %s", comp.Value)
	}
	if comp.Op == OpEquals {
		filter.Status = &status
	} else {
		filter.ExcludeStatus = append(filter.ExcludeStatus, status)
	}
	return nil
}

func (e *Evaluator) applyPriorityFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	priority, err := strconv.Atoi(comp.Value)
	if err != nil {
		return fmt.Errorf("invalid priority value: %s", comp.Value)
	}
	if priority < 0 || priority > 4 {
		return fmt.Errorf("priority must be between 0 and 4")
	}

	switch comp.Op {
	case OpEquals:
		filter.Priority = &priority
	case OpNotEquals:
		// For != we need predicate filtering
		return fmt.Errorf("priority != requires predicate filtering")
	case OpLess:
		// priority < X means PriorityMax = X-1
		max := priority - 1
		if max < 0 {
			return fmt.Errorf("priority < %d matches nothing", priority)
		}
		filter.PriorityMax = &max
	case OpLessEq:
		filter.PriorityMax = &priority
	case OpGreater:
		// priority > X means PriorityMin = X+1
		min := priority + 1
		if min > 4 {
			return fmt.Errorf("priority > %d matches nothing", priority)
		}
		filter.PriorityMin = &min
	case OpGreaterEq:
		filter.PriorityMin = &priority
	}
	return nil
}

func (e *Evaluator) applyTypeFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals && comp.Op != OpNotEquals {
		return fmt.Errorf("type only supports = and != operators")
	}
	issueType := types.IssueType(strings.ToLower(comp.Value))
	if comp.Op == OpEquals {
		filter.IssueType = &issueType
	} else {
		filter.ExcludeTypes = append(filter.ExcludeTypes, issueType)
	}
	return nil
}

func (e *Evaluator) applyAssigneeFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals {
		return fmt.Errorf("assignee only supports = operator")
	}
	if comp.Value == "" || strings.ToLower(comp.Value) == "none" || strings.ToLower(comp.Value) == "null" {
		filter.NoAssignee = true
	} else {
		filter.Assignee = &comp.Value
	}
	return nil
}

func (e *Evaluator) applyOwnerFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	// Owner filtering requires predicate
	return fmt.Errorf("owner filtering requires predicate mode")
}

func (e *Evaluator) applyLabelFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals {
		return fmt.Errorf("label only supports = operator")
	}
	if comp.Value == "" || strings.ToLower(comp.Value) == "none" || strings.ToLower(comp.Value) == "null" {
		filter.NoLabels = true
	} else {
		filter.Labels = append(filter.Labels, comp.Value)
	}
	return nil
}

func (e *Evaluator) applyTitleFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals {
		return fmt.Errorf("title only supports = operator (use title contains pattern)")
	}
	filter.TitleContains = comp.Value
	return nil
}

func (e *Evaluator) applyDescriptionFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals {
		return fmt.Errorf("description only supports = operator (use desc contains pattern)")
	}
	if comp.Value == "" || strings.ToLower(comp.Value) == "none" || strings.ToLower(comp.Value) == "null" {
		filter.EmptyDescription = true
	} else {
		filter.DescriptionContains = comp.Value
	}
	return nil
}

func (e *Evaluator) applyNotesFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals {
		return fmt.Errorf("notes only supports = operator")
	}
	filter.NotesContains = comp.Value
	return nil
}

func (e *Evaluator) applyCreatedFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	t, err := e.parseTimeValue(comp)
	if err != nil {
		return fmt.Errorf("invalid created time: %w", err)
	}
	switch comp.Op {
	case OpEquals:
		// For equals, set both before and after to bracket the day
		dayStart := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		dayEnd := dayStart.Add(24 * time.Hour)
		filter.CreatedAfter = &dayStart
		filter.CreatedBefore = &dayEnd
	case OpGreater:
		filter.CreatedAfter = &t
	case OpGreaterEq:
		filter.CreatedAfter = &t
	case OpLess:
		filter.CreatedBefore = &t
	case OpLessEq:
		endOfDay := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 999999999, t.Location())
		filter.CreatedBefore = &endOfDay
	default:
		return fmt.Errorf("created does not support %s operator", comp.Op.String())
	}
	return nil
}

func (e *Evaluator) applyUpdatedFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	t, err := e.parseTimeValue(comp)
	if err != nil {
		return fmt.Errorf("invalid updated time: %w", err)
	}
	switch comp.Op {
	case OpEquals:
		dayStart := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		dayEnd := dayStart.Add(24 * time.Hour)
		filter.UpdatedAfter = &dayStart
		filter.UpdatedBefore = &dayEnd
	case OpGreater:
		filter.UpdatedAfter = &t
	case OpGreaterEq:
		filter.UpdatedAfter = &t
	case OpLess:
		filter.UpdatedBefore = &t
	case OpLessEq:
		endOfDay := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 999999999, t.Location())
		filter.UpdatedBefore = &endOfDay
	default:
		return fmt.Errorf("updated does not support %s operator", comp.Op.String())
	}
	return nil
}

func (e *Evaluator) applyClosedFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	t, err := e.parseTimeValue(comp)
	if err != nil {
		return fmt.Errorf("invalid closed time: %w", err)
	}
	switch comp.Op {
	case OpGreater:
		filter.ClosedAfter = &t
	case OpGreaterEq:
		filter.ClosedAfter = &t
	case OpLess:
		filter.ClosedBefore = &t
	case OpLessEq:
		endOfDay := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 999999999, t.Location())
		filter.ClosedBefore = &endOfDay
	default:
		return fmt.Errorf("closed does not support %s operator", comp.Op.String())
	}
	return nil
}

func (e *Evaluator) applyIDFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals {
		return fmt.Errorf("id only supports = operator")
	}
	// Check if it looks like a prefix (ends with *)
	if strings.HasSuffix(comp.Value, "*") {
		filter.IDPrefix = strings.TrimSuffix(comp.Value, "*")
	} else {
		filter.IDs = append(filter.IDs, comp.Value)
	}
	return nil
}

func (e *Evaluator) applySpecFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals {
		return fmt.Errorf("spec only supports = operator")
	}
	// Support prefix matching
	if strings.HasSuffix(comp.Value, "*") {
		filter.SpecIDPrefix = strings.TrimSuffix(comp.Value, "*")
	} else {
		filter.SpecIDPrefix = comp.Value
	}
	return nil
}

func (e *Evaluator) applyParentFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals {
		return fmt.Errorf("parent only supports = operator")
	}
	filter.ParentID = &comp.Value
	return nil
}

func (e *Evaluator) applyBoolFilter(comp *ComparisonNode, filter *types.IssueFilter, field string) error {
	if comp.Op != OpEquals {
		return fmt.Errorf("%s only supports = operator", field)
	}
	val := strings.ToLower(comp.Value)
	var boolVal bool
	switch val {
	case "true", "yes", "1":
		boolVal = true
	case "false", "no", "0":
		boolVal = false
	default:
		return fmt.Errorf("invalid boolean value for %s: %s", field, comp.Value)
	}

	switch field {
	case "pinned":
		filter.Pinned = &boolVal
	case "ephemeral":
		filter.Ephemeral = &boolVal
	case "template":
		filter.IsTemplate = &boolVal
	}
	return nil
}

func (e *Evaluator) applyMolTypeFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals {
		return fmt.Errorf("mol_type only supports = operator")
	}
	mt := types.MolType(strings.ToLower(comp.Value))
	if !mt.IsValid() {
		return fmt.Errorf("invalid mol_type: %s", comp.Value)
	}
	filter.MolType = &mt
	return nil
}

// applyMetadataFilter handles metadata.<key>=<value> queries (GH#1406).
func (e *Evaluator) applyMetadataFilter(comp *ComparisonNode, filter *types.IssueFilter) error {
	if comp.Op != OpEquals {
		return fmt.Errorf("metadata fields only support = operator")
	}
	key := strings.TrimPrefix(comp.Field, "metadata.")
	if err := storage.ValidateMetadataKey(key); err != nil {
		return err
	}
	if filter.MetadataFields == nil {
		filter.MetadataFields = make(map[string]string)
	}
	filter.MetadataFields[key] = comp.Value
	return nil
}

// buildMetadataPredicate builds a predicate for metadata.<key>=<value> in OR queries.
// Parses the issue's JSON metadata and compares the top-level scalar at the given key.
func (e *Evaluator) buildMetadataPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	if comp.Op != OpEquals {
		return nil, fmt.Errorf("metadata fields only support = operator")
	}
	key := strings.TrimPrefix(comp.Field, "metadata.")
	if err := storage.ValidateMetadataKey(key); err != nil {
		return nil, err
	}
	value := comp.Value
	return func(i *types.Issue) bool {
		if len(i.Metadata) == 0 {
			return false
		}
		var data map[string]json.RawMessage
		if err := json.Unmarshal(i.Metadata, &data); err != nil {
			return false
		}
		raw, ok := data[key]
		if !ok {
			return false
		}
		// Try to unmarshal as a string first (most common case)
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s == value
		}
		// Fall back to comparing the raw JSON representation (numbers, bools)
		return strings.Trim(string(raw), "\"") == value
	}, nil
}

// applyNot applies a NOT expression to the filter.
func (e *Evaluator) applyNot(not *NotNode, filter *types.IssueFilter) error {
	comp, ok := not.Operand.(*ComparisonNode)
	if !ok {
		return fmt.Errorf("NOT only supports simple comparisons in filter mode")
	}

	switch comp.Field {
	case "status":
		if comp.Op != OpEquals {
			return fmt.Errorf("NOT status only supports = operator")
		}
		status := types.Status(strings.ToLower(comp.Value))
		filter.ExcludeStatus = append(filter.ExcludeStatus, status)
		return nil
	case "type":
		if comp.Op != OpEquals {
			return fmt.Errorf("NOT type only supports = operator")
		}
		issueType := types.IssueType(strings.ToLower(comp.Value))
		filter.ExcludeTypes = append(filter.ExcludeTypes, issueType)
		return nil
	default:
		return fmt.Errorf("NOT not supported for field %s in filter mode", comp.Field)
	}
}

// parseTimeValue parses a time value from a comparison node.
// Supports duration values (7d, 24h) which are interpreted as "now - duration".
func (e *Evaluator) parseTimeValue(comp *ComparisonNode) (time.Time, error) {
	if comp.ValueType == TokenDuration {
		// Duration values like 7d mean "7 days ago" for < comparisons
		// and "within the last 7 days" for > comparisons
		// We parse as relative to now, going backwards
		return e.parseDurationAgo(comp.Value)
	}
	// Otherwise use the standard time parser
	return timeparsing.ParseRelativeTime(comp.Value, e.now)
}

// parseDurationAgo parses a duration and returns now - duration.
func (e *Evaluator) parseDurationAgo(s string) (time.Time, error) {
	// Negate the duration to get time in the past
	negated := "-" + strings.TrimPrefix(s, "+")
	return timeparsing.ParseCompactDuration(negated, e.now)
}

// extractBaseFilters extracts filter-compatible portions from a complex query.
// This is used to pre-filter before applying the predicate.
func (e *Evaluator) extractBaseFilters(node Node, filter *types.IssueFilter) {
	switch n := node.(type) {
	case *ComparisonNode:
		// Try to apply, ignore errors (best-effort optimization: incompatible filters are safely skipped)
		_ = e.applyComparison(n, filter)
	case *AndNode:
		e.extractBaseFilters(n.Left, filter)
		e.extractBaseFilters(n.Right, filter)
	case *NotNode:
		_ = e.applyNot(n, filter) // Best-effort optimization: incompatible NOT filters are safely skipped
	case *OrNode:
		// For OR, we can't safely extract base filters
		// (extracting from either side would over-filter)
	}
}

// buildPredicate builds a predicate function for complex queries.
func (e *Evaluator) buildPredicate(node Node) (func(*types.Issue) bool, error) {
	switch n := node.(type) {
	case *ComparisonNode:
		return e.buildComparisonPredicate(n)
	case *AndNode:
		left, err := e.buildPredicate(n.Left)
		if err != nil {
			return nil, err
		}
		right, err := e.buildPredicate(n.Right)
		if err != nil {
			return nil, err
		}
		return func(issue *types.Issue) bool {
			return left(issue) && right(issue)
		}, nil
	case *OrNode:
		left, err := e.buildPredicate(n.Left)
		if err != nil {
			return nil, err
		}
		right, err := e.buildPredicate(n.Right)
		if err != nil {
			return nil, err
		}
		return func(issue *types.Issue) bool {
			return left(issue) || right(issue)
		}, nil
	case *NotNode:
		operand, err := e.buildPredicate(n.Operand)
		if err != nil {
			return nil, err
		}
		return func(issue *types.Issue) bool {
			return !operand(issue)
		}, nil
	default:
		return nil, fmt.Errorf("unexpected node type: %T", node)
	}
}

// buildComparisonPredicate builds a predicate for a single comparison.
func (e *Evaluator) buildComparisonPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	switch comp.Field {
	case "status":
		return e.buildStatusPredicate(comp)
	case "priority":
		return e.buildPriorityPredicate(comp)
	case "type":
		return e.buildTypePredicate(comp)
	case "assignee":
		return e.buildAssigneePredicate(comp)
	case "owner":
		return e.buildOwnerPredicate(comp)
	case "label", "labels":
		return e.buildLabelPredicate(comp)
	case "title":
		return e.buildTitlePredicate(comp)
	case "description", "desc":
		return e.buildDescriptionPredicate(comp)
	case "notes":
		return e.buildNotesPredicate(comp)
	case "created", "created_at":
		return e.buildCreatedPredicate(comp)
	case "updated", "updated_at":
		return e.buildUpdatedPredicate(comp)
	case "closed", "closed_at":
		return e.buildClosedPredicate(comp)
	case "id":
		return e.buildIDPredicate(comp)
	case "spec", "spec_id":
		return e.buildSpecPredicate(comp)
	case "pinned":
		return e.buildBoolPredicate(comp, func(i *types.Issue) bool { return i.Pinned })
	case "ephemeral":
		return e.buildBoolPredicate(comp, func(i *types.Issue) bool { return i.Ephemeral })
	case "template":
		return e.buildBoolPredicate(comp, func(i *types.Issue) bool { return i.IsTemplate })
	default:
		if strings.HasPrefix(comp.Field, "metadata.") {
			return e.buildMetadataPredicate(comp)
		}
		return nil, fmt.Errorf("unknown field: %s", comp.Field)
	}
}

func (e *Evaluator) buildStatusPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	status := types.Status(strings.ToLower(comp.Value))
	switch comp.Op {
	case OpEquals:
		return func(i *types.Issue) bool { return i.Status == status }, nil
	case OpNotEquals:
		return func(i *types.Issue) bool { return i.Status != status }, nil
	default:
		return nil, fmt.Errorf("status does not support %s operator", comp.Op.String())
	}
}

func (e *Evaluator) buildPriorityPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	priority, err := strconv.Atoi(comp.Value)
	if err != nil {
		return nil, fmt.Errorf("invalid priority: %s", comp.Value)
	}
	switch comp.Op {
	case OpEquals:
		return func(i *types.Issue) bool { return i.Priority == priority }, nil
	case OpNotEquals:
		return func(i *types.Issue) bool { return i.Priority != priority }, nil
	case OpLess:
		return func(i *types.Issue) bool { return i.Priority < priority }, nil
	case OpLessEq:
		return func(i *types.Issue) bool { return i.Priority <= priority }, nil
	case OpGreater:
		return func(i *types.Issue) bool { return i.Priority > priority }, nil
	case OpGreaterEq:
		return func(i *types.Issue) bool { return i.Priority >= priority }, nil
	default:
		return nil, fmt.Errorf("unexpected operator: %s", comp.Op.String())
	}
}

func (e *Evaluator) buildTypePredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	issueType := types.IssueType(strings.ToLower(comp.Value))
	switch comp.Op {
	case OpEquals:
		return func(i *types.Issue) bool { return i.IssueType == issueType }, nil
	case OpNotEquals:
		return func(i *types.Issue) bool { return i.IssueType != issueType }, nil
	default:
		return nil, fmt.Errorf("type does not support %s operator", comp.Op.String())
	}
}

func (e *Evaluator) buildAssigneePredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	value := comp.Value
	isNone := value == "" || strings.ToLower(value) == "none" || strings.ToLower(value) == "null"
	switch comp.Op {
	case OpEquals:
		if isNone {
			return func(i *types.Issue) bool { return i.Assignee == "" }, nil
		}
		return func(i *types.Issue) bool { return strings.EqualFold(i.Assignee, value) }, nil
	case OpNotEquals:
		if isNone {
			return func(i *types.Issue) bool { return i.Assignee != "" }, nil
		}
		return func(i *types.Issue) bool { return !strings.EqualFold(i.Assignee, value) }, nil
	default:
		return nil, fmt.Errorf("assignee does not support %s operator", comp.Op.String())
	}
}

func (e *Evaluator) buildOwnerPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	value := comp.Value
	switch comp.Op {
	case OpEquals:
		return func(i *types.Issue) bool { return strings.EqualFold(i.Owner, value) }, nil
	case OpNotEquals:
		return func(i *types.Issue) bool { return !strings.EqualFold(i.Owner, value) }, nil
	default:
		return nil, fmt.Errorf("owner does not support %s operator", comp.Op.String())
	}
}

func (e *Evaluator) buildLabelPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	value := comp.Value
	isNone := value == "" || strings.ToLower(value) == "none" || strings.ToLower(value) == "null"
	switch comp.Op {
	case OpEquals:
		if isNone {
			return func(i *types.Issue) bool { return len(i.Labels) == 0 }, nil
		}
		return func(i *types.Issue) bool {
			for _, l := range i.Labels {
				if strings.EqualFold(l, value) {
					return true
				}
			}
			return false
		}, nil
	case OpNotEquals:
		if isNone {
			return func(i *types.Issue) bool { return len(i.Labels) > 0 }, nil
		}
		return func(i *types.Issue) bool {
			for _, l := range i.Labels {
				if strings.EqualFold(l, value) {
					return false
				}
			}
			return true
		}, nil
	default:
		return nil, fmt.Errorf("label does not support %s operator", comp.Op.String())
	}
}

func (e *Evaluator) buildTitlePredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	value := strings.ToLower(comp.Value)
	switch comp.Op {
	case OpEquals:
		return func(i *types.Issue) bool {
			return strings.Contains(strings.ToLower(i.Title), value)
		}, nil
	case OpNotEquals:
		return func(i *types.Issue) bool {
			return !strings.Contains(strings.ToLower(i.Title), value)
		}, nil
	default:
		return nil, fmt.Errorf("title does not support %s operator", comp.Op.String())
	}
}

func (e *Evaluator) buildDescriptionPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	value := comp.Value
	isNone := value == "" || strings.ToLower(value) == "none" || strings.ToLower(value) == "null"
	switch comp.Op {
	case OpEquals:
		if isNone {
			return func(i *types.Issue) bool { return i.Description == "" }, nil
		}
		return func(i *types.Issue) bool {
			return strings.Contains(strings.ToLower(i.Description), strings.ToLower(value))
		}, nil
	case OpNotEquals:
		if isNone {
			return func(i *types.Issue) bool { return i.Description != "" }, nil
		}
		return func(i *types.Issue) bool {
			return !strings.Contains(strings.ToLower(i.Description), strings.ToLower(value))
		}, nil
	default:
		return nil, fmt.Errorf("description does not support %s operator", comp.Op.String())
	}
}

func (e *Evaluator) buildNotesPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	value := strings.ToLower(comp.Value)
	switch comp.Op {
	case OpEquals:
		return func(i *types.Issue) bool {
			return strings.Contains(strings.ToLower(i.Notes), value)
		}, nil
	case OpNotEquals:
		return func(i *types.Issue) bool {
			return !strings.Contains(strings.ToLower(i.Notes), value)
		}, nil
	default:
		return nil, fmt.Errorf("notes does not support %s operator", comp.Op.String())
	}
}

func (e *Evaluator) buildCreatedPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	t, err := e.parseTimeValue(comp)
	if err != nil {
		return nil, fmt.Errorf("invalid created time: %w", err)
	}
	return e.buildTimePredicate(comp.Op, t, func(i *types.Issue) time.Time { return i.CreatedAt })
}

func (e *Evaluator) buildUpdatedPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	t, err := e.parseTimeValue(comp)
	if err != nil {
		return nil, fmt.Errorf("invalid updated time: %w", err)
	}
	return e.buildTimePredicate(comp.Op, t, func(i *types.Issue) time.Time { return i.UpdatedAt })
}

func (e *Evaluator) buildClosedPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	t, err := e.parseTimeValue(comp)
	if err != nil {
		return nil, fmt.Errorf("invalid closed time: %w", err)
	}
	return func(i *types.Issue) bool {
		if i.ClosedAt == nil {
			return false
		}
		return e.compareTime(comp.Op, *i.ClosedAt, t)
	}, nil
}

func (e *Evaluator) buildTimePredicate(op ComparisonOp, t time.Time, getter func(*types.Issue) time.Time) (func(*types.Issue) bool, error) {
	return func(i *types.Issue) bool {
		return e.compareTime(op, getter(i), t)
	}, nil
}

func (e *Evaluator) compareTime(op ComparisonOp, actual, target time.Time) bool {
	switch op {
	case OpEquals:
		// Same day comparison
		return actual.Year() == target.Year() &&
			actual.Month() == target.Month() &&
			actual.Day() == target.Day()
	case OpNotEquals:
		return !(actual.Year() == target.Year() &&
			actual.Month() == target.Month() &&
			actual.Day() == target.Day())
	case OpLess:
		return actual.Before(target)
	case OpLessEq:
		return actual.Before(target) || actual.Equal(target)
	case OpGreater:
		return actual.After(target)
	case OpGreaterEq:
		return actual.After(target) || actual.Equal(target)
	default:
		return false
	}
}

func (e *Evaluator) buildIDPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	value := comp.Value
	hasWildcard := strings.HasSuffix(value, "*")
	if hasWildcard {
		prefix := strings.TrimSuffix(value, "*")
		switch comp.Op {
		case OpEquals:
			return func(i *types.Issue) bool { return strings.HasPrefix(i.ID, prefix) }, nil
		case OpNotEquals:
			return func(i *types.Issue) bool { return !strings.HasPrefix(i.ID, prefix) }, nil
		default:
			return nil, fmt.Errorf("id with wildcard only supports = and != operators")
		}
	}
	switch comp.Op {
	case OpEquals:
		return func(i *types.Issue) bool { return i.ID == value }, nil
	case OpNotEquals:
		return func(i *types.Issue) bool { return i.ID != value }, nil
	default:
		return nil, fmt.Errorf("id does not support %s operator", comp.Op.String())
	}
}

func (e *Evaluator) buildSpecPredicate(comp *ComparisonNode) (func(*types.Issue) bool, error) {
	value := comp.Value
	hasWildcard := strings.HasSuffix(value, "*")
	if hasWildcard {
		prefix := strings.TrimSuffix(value, "*")
		switch comp.Op {
		case OpEquals:
			return func(i *types.Issue) bool { return strings.HasPrefix(i.SpecID, prefix) }, nil
		case OpNotEquals:
			return func(i *types.Issue) bool { return !strings.HasPrefix(i.SpecID, prefix) }, nil
		default:
			return nil, fmt.Errorf("spec with wildcard only supports = and != operators")
		}
	}
	switch comp.Op {
	case OpEquals:
		return func(i *types.Issue) bool { return i.SpecID == value }, nil
	case OpNotEquals:
		return func(i *types.Issue) bool { return i.SpecID != value }, nil
	default:
		return nil, fmt.Errorf("spec does not support %s operator", comp.Op.String())
	}
}

func (e *Evaluator) buildBoolPredicate(comp *ComparisonNode, getter func(*types.Issue) bool) (func(*types.Issue) bool, error) {
	val := strings.ToLower(comp.Value)
	var boolVal bool
	switch val {
	case "true", "yes", "1":
		boolVal = true
	case "false", "no", "0":
		boolVal = false
	default:
		return nil, fmt.Errorf("invalid boolean value: %s", comp.Value)
	}

	switch comp.Op {
	case OpEquals:
		return func(i *types.Issue) bool { return getter(i) == boolVal }, nil
	case OpNotEquals:
		return func(i *types.Issue) bool { return getter(i) != boolVal }, nil
	default:
		return nil, fmt.Errorf("boolean field does not support %s operator", comp.Op.String())
	}
}

// Evaluate is a convenience function that parses and evaluates a query string.
func Evaluate(query string) (*QueryResult, error) {
	return EvaluateAt(query, time.Now())
}

// EvaluateAt parses and evaluates a query string with a specific reference time.
func EvaluateAt(query string, now time.Time) (*QueryResult, error) {
	node, err := Parse(query)
	if err != nil {
		return nil, err
	}
	eval := NewEvaluator(now)
	return eval.Evaluate(node)
}
