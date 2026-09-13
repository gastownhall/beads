package storage

// ConflictFieldValue is one column of a conflicted row, presented per side.
// A nil pointer means the side has no value for the field: either the side
// deleted (or never had) the row, or the column itself is NULL there.
type ConflictFieldValue struct {
	Name   string  `json:"name"`
	Base   *string `json:"base"`
	Ours   *string `json:"ours"`
	Theirs *string `json:"theirs"`
}

// ConflictRow is one conflicted row of one table, presented as fields rather
// than as the raw base_/our_/their_ column soup of dolt_conflicts_<table>.
type ConflictRow struct {
	Table string `json:"table"`
	// Key identifies the row: the issue ID for the issues table, and a
	// best-effort identifying value for the others.
	Key string `json:"key"`
	// OurDiffType/TheirDiffType are dolt's own classification of the
	// row on each side ("added", "modified", "removed"); empty when the
	// conflict table does not carry them.
	OurDiffType   string `json:"our_diff_type,omitempty"`
	TheirDiffType string `json:"their_diff_type,omitempty"`
	// BaseExists/OurExists/TheirExists report whether the row is present
	// on that side of the merge. A false OurExists or TheirExists is a
	// delete/modify conflict; a false BaseExists is an add/add conflict.
	BaseExists  bool                 `json:"base_exists"`
	OurExists   bool                 `json:"our_exists"`
	TheirExists bool                 `json:"their_exists"`
	Fields      []ConflictFieldValue `json:"fields"`
}

// Differs reports whether ours and theirs disagree on this field.
func (f ConflictFieldValue) Differs() bool {
	if f.Ours == nil || f.Theirs == nil {
		return f.Ours != f.Theirs
	}
	return *f.Ours != *f.Theirs
}

// ConstraintViolation is one table's outstanding post-merge constraint
// violations (dolt_constraint_violations).
type ConstraintViolation struct {
	Table string `json:"table"`
	Count int    `json:"count"`
}

// MergeBlockers is the merge state that blocks concluding a merge but never
// appears in dolt_conflicts: a schema conflict, or a constraint violation the
// auto-repair path (mergesettle.go) declined. Both can be outstanding while
// every ROW conflict is resolved, which used to make the commit fail with a
// raw dolt error and no guidance (wy-36ilm F12).
type MergeBlockers struct {
	// Merging reports dolt_merge_status.is_merging: a merge is open and
	// awaiting its commit.
	Merging bool `json:"merging"`
	// SchemaConflictTables are the tables in dolt_schema_conflicts.
	SchemaConflictTables []string `json:"schema_conflict_tables,omitempty"`
	// ConstraintViolations are the tables with outstanding violations.
	ConstraintViolations []ConstraintViolation `json:"constraint_violations,omitempty"`
}

// Blocked reports whether anything here would refuse a merge commit.
func (b MergeBlockers) Blocked() bool {
	return len(b.SchemaConflictTables) > 0 || len(b.ConstraintViolations) > 0
}
