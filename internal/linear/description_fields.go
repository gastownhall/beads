package linear

import (
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// SettleDescriptionFields decides, field by field, which side each of the
// description fields a Linear body folds follows when a pull brings the
// body back (bd #8506). The pulled issue arrives with the body already
// parsed: Description holds the text above the section headings, and a
// field holds its section's text when the body still carries that section.
//
// A section the body still carries wins: its text is Linear's edit of what
// the last push folded there. When the body holds exactly what a push of
// the local fields sends, the local fields already are the remote's
// decision — including a description that itself ends in a section-shaped
// run, which the parse would otherwise lift into a field and reshape. A
// section the body no longer carries is decided by its local text: blank
// stays blank, text whose lines sit contiguously in the description moved
// into the body (the field clears, the text stays), and anything else was
// deleted on the remote — the local text is kept and reported so the
// following push restores it.
func (t *Tracker) SettleDescriptionFields(existing, pulled *types.Issue) (keptLocal bool) {
	if existing == nil || pulled == nil {
		return false
	}
	if DescriptionFieldsEqual(existing, pulled) {
		pulled.Description = existing.Description
		pulled.AcceptanceCriteria = existing.AcceptanceCriteria
		pulled.Design = existing.Design
		pulled.Notes = existing.Notes
		return false
	}
	fields := []struct{ remote, local *string }{
		{&pulled.AcceptanceCriteria, &existing.AcceptanceCriteria},
		{&pulled.Design, &existing.Design},
		{&pulled.Notes, &existing.Notes},
	}
	for _, f := range fields {
		if strings.TrimSpace(*f.remote) != "" {
			continue // the body still carries the section: the remote wins
		}
		if strings.TrimSpace(*f.local) == "" {
			continue // nothing local to keep
		}
		if linearTextContains(pulled.Description, *f.local) {
			*f.remote = "" // the text lives in the description now
			continue
		}
		*f.remote = *f.local
		keptLocal = true
	}
	return keptLocal
}

// linearTextContains reports whether every non-blank line of text appears,
// in order and contiguously, among the non-blank lines of body: the text
// was moved into (or never left) the body, not deleted from it.
func linearTextContains(body, text string) bool {
	want := nonBlankTrimmedLines(text)
	if len(want) == 0 {
		return false
	}
	lines := nonBlankTrimmedLines(body)
	for start := 0; start+len(want) <= len(lines); start++ {
		match := true
		for i, w := range want {
			if lines[start+i] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
