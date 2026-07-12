package flatfile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// SearchIssues loads all issues, applies the text query and filter, then returns
// the matching slice. For < 10k issues this is fast (all in memory).
func (s *FlatFileStore) SearchIssues(_ context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	return searchIn(all, query, filter)
}

// searchIn applies query, filter, sort, and offset/limit to a pre-loaded
// snapshot, so callers that also need the full issue set (e.g. for dependent
// counts) can share one load and see one consistent snapshot.
func searchIn(all []*types.Issue, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	// Pre-compile label regex once for the entire search. An invalid regex
	// or glob is an error, never a silent match-all / match-none.
	var labelRe *regexp.Regexp
	if filter.LabelRegex != "" {
		re, err := regexp.Compile(filter.LabelRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid label regex %q: %w", filter.LabelRegex, err)
		}
		labelRe = re
	}
	if filter.LabelPattern != "" {
		if _, err := filepath.Match(filter.LabelPattern, ""); err != nil {
			return nil, fmt.Errorf("invalid label pattern %q: %w", filter.LabelPattern, err)
		}
	}

	// SQL contract (sqlbuild.AppendMetadataClauses): metadata keys are
	// validated and invalid keys error rather than silently matching nothing.
	if filter.HasMetadataKey != "" {
		if err := storage.ValidateMetadataKey(filter.HasMetadataKey); err != nil {
			return nil, err
		}
	}
	for k := range filter.MetadataFields {
		if err := storage.ValidateMetadataKey(k); err != nil {
			return nil, err
		}
	}

	var result []*types.Issue
	for _, issue := range all {
		if matchesQuery(issue, query) && matchesFilter(issue, filter, labelRe) {
			result = append(result, issue)
		}
	}

	sortIssues(result, filter.SortBy, filter.SortDesc)

	if filter.Offset > 0 && filter.Offset < len(result) {
		result = result[filter.Offset:]
	} else if filter.Offset >= len(result) {
		return nil, nil
	}
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}

	return result, nil
}

// SearchIssueIDs is the narrow-projection variant of SearchIssues, returning
// only the matching issue ids (parity with the SQL and Dolt stores).
func (s *FlatFileStore) SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error) {
	issues, err := s.SearchIssues(ctx, query, filter)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	return ids, nil
}

// SearchIssuesWithCounts is like SearchIssues but attaches dependency and
// comment counts. Issues are loaded once: the same snapshot backs both the
// returned rows and the reverse-dependency index, so counts are always
// consistent with the listed issues.
func (s *FlatFileStore) SearchIssuesWithCounts(_ context.Context, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	all, err := s.loadAllIssues()
	if err != nil {
		return nil, err
	}
	issues, err := searchIn(all, query, filter)
	if err != nil {
		return nil, err
	}

	// Build reverse dep index for dependent counts from the same snapshot.
	// Counts include only 'blocks' edges, mirroring sqlbuild.SearchCountsSQL's
	// dep_count/rdep_count subqueries (WHERE type = 'blocks') and this store's
	// own GetReadyWorkWithCounts/GetDependencyCounts paths.
	depCounts := make(map[string]int)
	parentMap := make(map[string]string)
	for _, a := range all {
		for _, dep := range a.Dependencies {
			if dep.Type == types.DepParentChild {
				parentMap[a.ID] = dep.DependsOnID
			} else if dep.Type == types.DepBlocks {
				depCounts[dep.DependsOnID]++
			}
		}
	}

	var result []*types.IssueWithCounts
	for _, issue := range issues {
		depCount := 0
		for _, dep := range issue.Dependencies {
			if dep.Type == types.DepBlocks {
				depCount++
			}
		}
		// readDirSafe already maps ErrNotExist to zero entries, so any error
		// here is a real failure (permissions, I/O) and must surface, not
		// render as 0 comments.
		commentCount, err := s.countCommentFiles(issue.ID)
		if err != nil {
			return nil, fmt.Errorf("count comments for %s: %w", issue.ID, err)
		}
		wc := &types.IssueWithCounts{
			Issue:           issue,
			DependencyCount: depCount,
			DependentCount:  depCounts[issue.ID],
			CommentCount:    commentCount,
		}
		if p, ok := parentMap[issue.ID]; ok {
			wc.Parent = &p
		}
		result = append(result, wc)
	}
	return result, nil
}

// CountIssues returns the number of issues matching query and filter.
func (s *FlatFileStore) CountIssues(ctx context.Context, query string, filter types.IssueFilter) (int64, error) {
	// Ignore Limit/Offset for count.
	filter.Limit = 0
	filter.Offset = 0
	issues, err := s.SearchIssues(ctx, query, filter)
	if err != nil {
		return 0, err
	}
	return int64(len(issues)), nil
}

// CountIssuesByGroup returns per-group counts for the given groupBy field.
// Key normalization mirrors issueops' countByColumnInTx/countByLabelInTx:
// priorities are P-prefixed (P0/P1/...), empty assignees collapse into
// "(unassigned)", and unlabeled issues bucket under "(no labels)" — a key
// that is absent (not zero) when every issue is labeled.
func (s *FlatFileStore) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	// SQL contract (issueops countGroupForTablesInTx): unknown groupBy is an
	// error, never a plausible-looking bucket — checked before any scan so an
	// empty store errors too.
	switch groupBy {
	case "status", "priority", "type", "assignee", "label":
	default:
		return nil, fmt.Errorf("unsupported groupBy: %s", groupBy)
	}

	filter.Limit = 0
	filter.Offset = 0
	issues, err := s.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, err
	}

	counts := make(map[string]int)
	for _, issue := range issues {
		var key string
		switch groupBy {
		case "status":
			key = string(issue.Status)
		case "priority":
			key = fmt.Sprintf("P%d", issue.Priority)
		case "type":
			key = string(issue.IssueType)
		case "assignee":
			key = issue.Assignee
			if key == "" {
				key = "(unassigned)"
			}
		case "label":
			if len(issue.Labels) == 0 {
				counts["(no labels)"]++
			} else {
				for _, l := range issue.Labels {
					counts[l]++
				}
			}
			continue
		}
		counts[key]++
	}
	return counts, nil
}

// countCommentFiles returns the number of comment files for an issue.
func (s *FlatFileStore) countCommentFiles(issueID string) (int, error) {
	dir, err := safePath(s.commentsDir, issueID, "")
	if err != nil {
		return 0, err
	}
	entries, err := readDirSafe(dir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			count++
		}
	}
	return count, nil
}

// matchesQuery mirrors sqlbuild.BuildIssueFilterClauses' text-query branch:
// an ID-shaped token matches id exactly, an id prefix, or a title /
// external_ref substring; anything else matches title or id substrings.
func matchesQuery(issue *types.Issue, query string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(query)
	lowerID := strings.ToLower(issue.ID)
	if sqlbuild.LooksLikeIssueID(query) {
		ref := ""
		if issue.ExternalRef != nil {
			ref = strings.ToLower(*issue.ExternalRef)
		}
		return lowerID == q || strings.HasPrefix(lowerID, q) ||
			strings.Contains(strings.ToLower(issue.Title), q) ||
			strings.Contains(ref, q)
	}
	return strings.Contains(strings.ToLower(issue.Title), q) ||
		strings.Contains(lowerID, q)
}

// matchesFilter returns true if the issue matches all filter criteria.
// labelRe is the pre-compiled LabelRegex (nil if not set).
func matchesFilter(issue *types.Issue, f types.IssueFilter, labelRe *regexp.Regexp) bool {
	// Status filters
	if f.Status != nil && issue.Status != *f.Status {
		return false
	}
	if len(f.Statuses) > 0 {
		found := false
		for _, s := range f.Statuses {
			if issue.Status == s {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, es := range f.ExcludeStatus {
		if issue.Status == es {
			return false
		}
	}

	// Priority
	if f.Priority != nil && issue.Priority != *f.Priority {
		return false
	}
	if f.PriorityMin != nil && issue.Priority < *f.PriorityMin {
		return false
	}
	if f.PriorityMax != nil && issue.Priority > *f.PriorityMax {
		return false
	}

	// Type
	if f.IssueType != nil && issue.IssueType != *f.IssueType {
		return false
	}
	for _, et := range f.ExcludeTypes {
		if issue.IssueType == et {
			return false
		}
	}

	// Assignee
	if f.Assignee != nil && issue.Assignee != *f.Assignee {
		return false
	}
	if f.NoAssignee && issue.Assignee != "" {
		return false
	}

	// Labels — build set once for all label checks.
	needLabelSet := len(f.Labels) > 0 || len(f.LabelsAny) > 0 || len(f.ExcludeLabels) > 0
	var labelSet map[string]bool
	if needLabelSet {
		labelSet = makeStringSet(issue.Labels)
	}
	// Labels (AND semantics)
	if len(f.Labels) > 0 {
		for _, l := range f.Labels {
			if !labelSet[l] {
				return false
			}
		}
	}
	// Labels (OR semantics)
	if len(f.LabelsAny) > 0 {
		found := false
		for _, l := range f.LabelsAny {
			if labelSet[l] {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	// Label exclusion
	if len(f.ExcludeLabels) > 0 {
		for _, l := range f.ExcludeLabels {
			if labelSet[l] {
				return false
			}
		}
	}
	// Label pattern (glob) — pre-validated in SearchIssues, so Match cannot
	// error here (filepath.Match fails only on malformed patterns).
	if f.LabelPattern != "" {
		found := false
		for _, l := range issue.Labels {
			if matched, _ := filepath.Match(f.LabelPattern, l); matched {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	// Label regex (pre-compiled)
	if labelRe != nil {
		found := false
		for _, l := range issue.Labels {
			if labelRe.MatchString(l) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if f.NoLabels && len(issue.Labels) > 0 {
		return false
	}

	// ID filters
	if len(f.IDs) > 0 {
		idSet := makeStringSet(f.IDs)
		if !idSet[issue.ID] {
			return false
		}
	}
	if f.IDPrefix != "" && !strings.HasPrefix(issue.ID, f.IDPrefix) {
		return false
	}
	if f.SpecIDPrefix != "" && !strings.HasPrefix(issue.SpecID, f.SpecIDPrefix) {
		return false
	}

	// Text contains
	if f.TitleContains != "" && !containsCI(issue.Title, f.TitleContains) {
		return false
	}
	if f.TitleSearch != "" && !containsCI(issue.Title, f.TitleSearch) {
		return false
	}
	if f.DescriptionContains != "" && !containsCI(issue.Description, f.DescriptionContains) {
		return false
	}
	if f.NotesContains != "" && !containsCI(issue.Notes, f.NotesContains) {
		return false
	}
	if f.ExternalRefContains != "" {
		ref := ""
		if issue.ExternalRef != nil {
			ref = *issue.ExternalRef
		}
		if !containsCI(ref, f.ExternalRefContains) {
			return false
		}
	}

	// Empty checks
	if f.EmptyDescription && issue.Description != "" {
		return false
	}

	// Date ranges
	if f.CreatedAfter != nil && !issue.CreatedAt.After(*f.CreatedAfter) {
		return false
	}
	if f.CreatedBefore != nil && !issue.CreatedAt.Before(*f.CreatedBefore) {
		return false
	}
	if f.UpdatedAfter != nil && !issue.UpdatedAt.After(*f.UpdatedAfter) {
		return false
	}
	if f.UpdatedBefore != nil && !issue.UpdatedAt.Before(*f.UpdatedBefore) {
		return false
	}
	if f.ClosedAfter != nil {
		if issue.ClosedAt == nil || !issue.ClosedAt.After(*f.ClosedAfter) {
			return false
		}
	}
	if f.ClosedBefore != nil {
		if issue.ClosedAt == nil || !issue.ClosedAt.Before(*f.ClosedBefore) {
			return false
		}
	}
	if f.StartedAfter != nil {
		if issue.StartedAt == nil || !issue.StartedAt.After(*f.StartedAfter) {
			return false
		}
	}
	if f.StartedBefore != nil {
		if issue.StartedAt == nil || !issue.StartedAt.Before(*f.StartedBefore) {
			return false
		}
	}

	// Source repo
	if f.SourceRepo != nil && issue.SourceRepo != *f.SourceRepo {
		return false
	}

	// Wisp routing: SkipWisps drops the wisps tier entirely.
	if f.SkipWisps && issueops.IsWisp(issue) {
		return false
	}

	// Ephemeral
	if f.Ephemeral != nil {
		if *f.Ephemeral && !issue.Ephemeral {
			return false
		}
		if !*f.Ephemeral && issue.Ephemeral {
			return false
		}
	}

	// Pinned
	if f.Pinned != nil {
		if *f.Pinned && !issue.Pinned {
			return false
		}
		if !*f.Pinned && issue.Pinned {
			return false
		}
	}

	// Template
	if f.IsTemplate != nil {
		if *f.IsTemplate && !issue.IsTemplate {
			return false
		}
		if !*f.IsTemplate && issue.IsTemplate {
			return false
		}
	}

	// MolType
	if f.MolType != nil && issue.MolType != *f.MolType {
		return false
	}

	// WispType
	if f.WispType != nil && issue.WispType != *f.WispType {
		return false
	}

	// Deferred
	if f.Deferred {
		isDeferred := issue.DeferUntil != nil || issue.Status == types.StatusDeferred
		if !isDeferred {
			return false
		}
	}
	if f.DeferAfter != nil {
		if issue.DeferUntil == nil || !issue.DeferUntil.After(*f.DeferAfter) {
			return false
		}
	}
	if f.DeferBefore != nil {
		if issue.DeferUntil == nil || !issue.DeferUntil.Before(*f.DeferBefore) {
			return false
		}
	}
	if f.DueAfter != nil {
		if issue.DueAt == nil || !issue.DueAt.After(*f.DueAfter) {
			return false
		}
	}
	if f.DueBefore != nil {
		if issue.DueAt == nil || !issue.DueAt.Before(*f.DueBefore) {
			return false
		}
	}
	if f.Overdue {
		now := time.Now()
		if issue.DueAt == nil || !issue.DueAt.Before(now) || issue.Status == types.StatusClosed {
			return false
		}
	}

	// Metadata
	if !matchesMetadataFilter(issue, f.MetadataFields, f.HasMetadataKey) {
		return false
	}

	// Parent filtering — requires scanning deps for parent-child type.
	// ParentID and NoParent are handled via the dependencies array.
	// SQL contract (sqlbuild/filter.go ParentID clause): match an explicit
	// parent-child dep on ParentID, OR an implicit dotted-ID child — id has
	// prefix "parent." and NO parent-child dep at all.
	if f.ParentID != nil {
		found := false
		hasParentDep := false
		for _, dep := range issue.Dependencies {
			if dep.Type == types.DepParentChild {
				hasParentDep = true
				if dep.DependsOnID == *f.ParentID {
					found = true
					break
				}
			}
		}
		if !found && !hasParentDep && strings.HasPrefix(issue.ID, *f.ParentID+".") {
			found = true
		}
		if !found {
			return false
		}
	}
	if f.NoParent {
		for _, dep := range issue.Dependencies {
			if dep.Type == types.DepParentChild {
				return false
			}
		}
	}

	return true
}

// sortIssues applies the canonical sort contract via sqlbuild.Less, the
// Go-side mirror of the SQL ORDER BY (priority ASC, created_at DESC, id ASC
// by default; NULLs-last-on-DESC for nullable columns like closed_at).
func sortIssues(issues []*types.Issue, sortBy string, desc bool) {
	sort.Slice(issues, func(i, j int) bool {
		return sqlbuild.Less(issues[i], issues[j], sortBy, desc)
	})
}

// matchesMetadataFilter applies the metadata predicates shared by the search
// and ready paths, mirroring sqlbuild.AppendMetadataClauses: every
// MetadataFields entry must equal the issue's top-level JSON value under
// JSON_UNQUOTE semantics (see metadataUnquote), and HasMetadataKey must be
// present. An issue with no (or unparseable) metadata matches only when no
// predicate is set (JSON_EXTRACT on NULL yields NULL).
func matchesMetadataFilter(issue *types.Issue, fields map[string]string, hasKey string) bool {
	if len(fields) == 0 && hasKey == "" {
		return true
	}
	if len(issue.Metadata) == 0 {
		return false
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(issue.Metadata, &meta) != nil {
		return false
	}
	for k, want := range fields {
		raw, ok := meta[k]
		if !ok || metadataUnquote(raw) != want {
			return false
		}
	}
	if hasKey != "" {
		if _, ok := meta[hasKey]; !ok {
			return false
		}
	}
	return true
}

// metadataUnquote mirrors SQL JSON_UNQUOTE(JSON_EXTRACT(...)): a JSON string
// compares by its unquoted contents, any other scalar by its compact JSON
// source text (so {"count": 1.0} compares as "1.0", not fmt's "1"), and
// objects/arrays by MySQL's spaced serialization (see writeMySQLJSON).
func metadataUnquote(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 {
		switch trimmed[0] {
		case '"':
			var s string
			if json.Unmarshal(trimmed, &s) == nil {
				return s
			}
		case '{', '[':
			var v any
			if json.Unmarshal(trimmed, &v) == nil {
				var buf bytes.Buffer
				writeMySQLJSON(&buf, v)
				return buf.String()
			}
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, trimmed) != nil {
		return string(trimmed)
	}
	return buf.String()
}

// writeMySQLJSON renders a decoded JSON value the way MySQL/Dolt serialize
// JSON to text (go-mysql-server sql/types/json_encode.go writeMarshalledValue
// over an encoding/json interface{} tree): ", " and ": " separators, object
// keys ordered by length then bytewise, and integral floats printed without a
// fraction. It handles exactly the types encoding/json decodes into any.
func writeMySQLJSON(buf *bytes.Buffer, v any) {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) < len(keys[j])
			}
			return keys[i] < keys[j]
		})
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteString(", ")
			}
			writeMySQLJSONString(buf, k)
			buf.WriteString(": ")
			writeMySQLJSON(buf, v[k])
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range v {
			if i > 0 {
				buf.WriteString(", ")
			}
			writeMySQLJSON(buf, e)
		}
		buf.WriteByte(']')
	case string:
		writeMySQLJSONString(buf, v)
	case float64:
		if v == float64(int64(v)) {
			buf.WriteString(strconv.FormatInt(int64(v), 10))
		} else {
			buf.WriteString(strconv.FormatFloat(v, 'f', -1, 64))
		}
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	default: // nil is the only other type encoding/json decodes into any
		buf.WriteString("null")
	}
}

// writeMySQLJSONString writes s as a JSON string with MySQL's escaping
// (go-mysql-server escapeSeq): named escapes for \b \t \n \f \r \" \\,
// \u00XX for other control bytes, and — unlike encoding/json — no HTML
// escaping of < > &.
func writeMySQLJSONString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\t':
			buf.WriteString(`\t`)
		case '\n':
			buf.WriteString(`\n`)
		case '\f':
			buf.WriteString(`\f`)
		case '\r':
			buf.WriteString(`\r`)
		default:
			if c < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, c)
			} else {
				buf.WriteByte(c)
			}
		}
	}
	buf.WriteByte('"')
}

func containsCI(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func makeStringSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
