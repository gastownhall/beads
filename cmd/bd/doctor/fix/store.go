package fix

import (
	"context"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// openBeadMutatingStore opens a repair handler's store WITH the workspace's
// events-journal activation applied.
//
// Most of bd doctor inspects and repairs workspace state — schema cursors,
// metadata, remotes, fingerprints, git — and none of that belongs in a journal
// whose records a consumer replays onto beads. Three repair handlers are not
// like that: StaleClosedIssues and PatrolPollution DELETE issues, and the two
// fresh-clone import paths CREATE them. Those are ordinary bead mutations, and
// a consumer whose mirror silently diverges because someone ran
// `bd doctor --fix` is exactly the failure the journal exists to prevent — the
// more so because a repair is unattended and nobody is watching the diff.
//
// The read-only and workspace-state handlers deliberately keep opening the
// store directly; they carry their exemption in the construction guard, with a
// reason that is true of them.
// The concrete *dolt.DoltStore is returned rather than storage.DoltStorage
// because these handlers reach past the interface for raw diagnostic SQL, which
// is what a repair path legitimately does.
func openBeadMutatingStore(ctx context.Context, beadsDir string) (*dolt.DoltStore, error) {
	store, err := dolt.NewFromConfig(ctx, beadsDir)
	return activated(beadsDir, store, err)
}

// openBeadMutatingStoreCreating is openBeadMutatingStore for the fresh-clone
// repair, which must be allowed to create the database it is about to import
// into — that is the whole point of the fix, so it opts out of the create
// guard rather than failing on an absent database.
func openBeadMutatingStoreCreating(ctx context.Context, beadsDir string) (*dolt.DoltStore, error) {
	store, err := dolt.NewFromConfigWithOptions(ctx, beadsDir, &dolt.Config{CreateIfMissing: true})
	return activated(beadsDir, store, err)
}

// activated applies the workspace's journal setting and closes the store if it
// cannot be honored. A failed open passes straight through, so the typed-nil
// store never reaches the activation.
func activated(beadsDir string, store *dolt.DoltStore, err error) (*dolt.DoltStore, error) {
	if _, err = eventsjournal.ActivateStore(beadsDir, store, err); err != nil {
		return nil, err
	}
	return store, nil
}
