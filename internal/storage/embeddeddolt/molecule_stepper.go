//go:build cgo

package embeddeddolt

import (
	"github.com/steveyegge/beads/issueops"
)

// MoleculeStepper returns the advance-a-molecule surface for this store:
// issueops.NewMoleculeStepper composed over this store's own BatchGetter,
// Relations, EdgeReader, Claimer and Lifecycle, so the advance reads and
// claims exactly as those roles do and no second copy of the rule exists.
func (s *EmbeddedDoltStore) MoleculeStepper() (issueops.MoleculeStepper, error) {
	roles, err := issueops.MoleculeStepperRolesOf(s)
	if err != nil {
		return nil, err
	}
	return issueops.NewMoleculeStepper(roles), nil
}
