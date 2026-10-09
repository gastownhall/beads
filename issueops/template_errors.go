package issueops

import (
	"errors"
	"fmt"
)

// ErrTemplateReadOnly is returned when a mutation that guards templates names
// one. Every update does (Lifecycle.Update, a BatchApplier update item); not
// every verb does yet (bd-jkp9v3). Templates are read-only: the way to get
// work out of one is to pour it (`bd mol pour`), which creates new issues and
// leaves the template untouched. There is no force bypass — no Force flag
// waives it; only an update that sets UpdateRequest.AllowTemplate (bd label,
// bd set-state) is let through.
var ErrTemplateReadOnly = errors.New("templates are read-only")

// TemplateReadOnlyError reports the template a mutation was refused for. Its
// message is the sentence `bd` has always printed for the refusal, so a caller
// that renders err.Error() reads the same line whichever backend refused.
type TemplateReadOnlyError struct {
	// IssueID names the template that refused the mutation.
	IssueID string
}

func (e *TemplateReadOnlyError) Error() string {
	return fmt.Sprintf("cannot modify template %s: templates are read-only; use 'bd mol pour' to create a work item", e.IssueID)
}

// Unwrap makes TemplateReadOnlyError match ErrTemplateReadOnly.
func (e *TemplateReadOnlyError) Unwrap() error { return ErrTemplateReadOnly }
