package bench

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Row is the subset of SearchCountsSQL's output columns a shape comparison
// cares about: the id plus the six "extra" mega-query columns. The rest of
// the hydration payload is shape-invariant.
type Row struct {
	IssueID      string
	LabelsJSON   string
	DepCount     int64
	RDepCount    int64
	CommentCount int64
	ParentID     string
	DepsJSON     string
}

// DiffKind classifies one CompareRowSets finding.
type DiffKind string

const (
	DiffMissing  DiffKind = "missing"  // present in want, absent from got
	DiffExtra    DiffKind = "extra"    // present in got, absent from want
	DiffMismatch DiffKind = "mismatch" // present in both, fields disagree
)

// RowSetDiff is one disagreement between two row sets for the same IssueID.
type RowSetDiff struct {
	Kind    DiffKind
	IssueID string
	Detail  string
}

// CompareRowSets reports every disagreement between got and want, keyed by
// IssueID rather than position — result-set row order is not part of
// SearchCountsSQL's contract. labels_json and deps_json are compared as JSON
// (element order and, for deps_json's objects, key order are both
// insensitive), since SQL aggregation gives no ordering guarantee there. The
// returned slice is sorted by IssueID then Kind for deterministic output;
// an empty/nil result means the two row sets are equivalent.
func CompareRowSets(got, want []Row) []RowSetDiff {
	gotByID := make(map[string]Row, len(got))
	for _, r := range got {
		gotByID[r.IssueID] = r
	}
	wantByID := make(map[string]Row, len(want))
	for _, r := range want {
		wantByID[r.IssueID] = r
	}

	var diffs []RowSetDiff
	for id := range wantByID {
		if _, ok := gotByID[id]; !ok {
			diffs = append(diffs, RowSetDiff{Kind: DiffMissing, IssueID: id, Detail: "present in want, absent from got"})
		}
	}
	for id := range gotByID {
		if _, ok := wantByID[id]; !ok {
			diffs = append(diffs, RowSetDiff{Kind: DiffExtra, IssueID: id, Detail: "present in got, absent from want"})
		}
	}
	for id, g := range gotByID {
		w, ok := wantByID[id]
		if !ok {
			continue
		}
		if detail := rowMismatch(g, w); detail != "" {
			diffs = append(diffs, RowSetDiff{Kind: DiffMismatch, IssueID: id, Detail: detail})
		}
	}

	sort.Slice(diffs, func(i, j int) bool {
		if diffs[i].IssueID != diffs[j].IssueID {
			return diffs[i].IssueID < diffs[j].IssueID
		}
		return diffs[i].Kind < diffs[j].Kind
	})
	return diffs
}

// rowMismatch returns a combined human-readable description of every field
// where g and w disagree, or "" if they agree on all fields.
func rowMismatch(g, w Row) string {
	var detail string
	add := func(field string, gv, wv any) {
		if detail != "" {
			detail += "; "
		}
		detail += fmt.Sprintf("%s: got %v, want %v", field, gv, wv)
	}

	if g.DepCount != w.DepCount {
		add("dep_count", g.DepCount, w.DepCount)
	}
	if g.RDepCount != w.RDepCount {
		add("rdep_count", g.RDepCount, w.RDepCount)
	}
	if g.CommentCount != w.CommentCount {
		add("comment_count", g.CommentCount, w.CommentCount)
	}
	if g.ParentID != w.ParentID {
		add("parent_id", g.ParentID, w.ParentID)
	}
	if !jsonArrayEqual(g.LabelsJSON, w.LabelsJSON) {
		add("labels_json", g.LabelsJSON, w.LabelsJSON)
	}
	if !jsonArrayEqual(g.DepsJSON, w.DepsJSON) {
		add("deps_json", g.DepsJSON, w.DepsJSON)
	}
	return detail
}

// jsonArrayEqual reports whether two JSON-array strings contain the same
// elements, ignoring element order and (for object elements) key order.
// Two empty/blank strings are treated as equal.
func jsonArrayEqual(a, b string) bool {
	if a == b {
		return true
	}
	na, aOK := normalizeJSONArray(a)
	nb, bOK := normalizeJSONArray(b)
	if !aOK || !bOK {
		return false
	}
	if len(na) != len(nb) {
		return false
	}
	for i := range na {
		if na[i] != nb[i] {
			return false
		}
	}
	return true
}

// normalizeJSONArray parses s as a JSON array, canonicalizes each element
// (re-marshaling sorts object keys), and returns the element strings sorted
// for order-insensitive comparison. Blank input normalizes to an empty,
// successful result so "" and "[]" compare equal.
func normalizeJSONArray(s string) ([]string, bool) {
	if s == "" {
		return nil, true
	}
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, elem := range raw {
		var v any
		if err := json.Unmarshal(elem, &v); err != nil {
			return nil, false
		}
		canon, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		out = append(out, string(canon))
	}
	sort.Strings(out)
	return out, true
}
