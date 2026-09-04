package types

import "time"

// LabelDefinition is an entry in the opt-in curated label vocabulary registry
// (bd label define / undefine / defined). Its presence changes nothing about
// what label a caller may write by itself -- whether it is consulted at write
// time, and whether an undefined label warns or is refused, is controlled by
// the labels.vocabulary config knob (open|warn|enforce; see
// docs/core-concepts/labels.md).
//
// Only ONE case-variant spelling of a label may ever be defined at a time:
// `bd label define` refuses a case-insensitive collision against an existing
// row. This does not fold labels already stored on issues in a different
// case -- that stays visible to `bd doctor`'s case-variant-cluster check.
type LabelDefinition struct {
	Label       string    `json:"label"`
	Description *string   `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	CreatedBy   *string   `json:"created_by,omitempty"`
}
