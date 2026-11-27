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
// section the body no longer carries keeps the local text and reports it
// kept, so the following push sends it; the absent case is refined in
// later commits (blank locally, or text that moved into the body).
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
		*f.remote = *f.local
		keptLocal = true
	}
	return keptLocal
}
