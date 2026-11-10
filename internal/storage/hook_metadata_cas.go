package storage

import (
	"context"

	"github.com/steveyegge/beads/issueops"
)

// MetadataCAS returns the inner store's conditional metadata write with this
// decorator's completion hooks layered over it.
//
// It WRAPS rather than recursing unwrapped, unlike Sweeper and Deleter: a swap
// that lands is an update to an issue, which is an event the hook vocabulary
// publishes, and the row it names is still there to hand a script.
//
// It recurses instead of delegating, for the reason Commenter does: the
// metadata writes this role replaces — SlotSet through the generic update path
// — fire the update hook, so a blind delegation would hand back the inner
// store's surface and silently stop firing it.
func (h *HookFiringStore) MetadataCAS() (issueops.MetadataCAS, error) {
	inner, err := h.inner.MetadataCAS()
	if err != nil {
		return nil, err
	}
	return &hookMetadataCAS{inner: inner, hooks: h}, nil
}

type hookMetadataCAS struct {
	inner issueops.MetadataCAS
	hooks issueOperationHooks
}

// CompareAndSetKey fires the update hook for a swap that CHANGED the row, and
// fires nothing otherwise.
//
// The two cases it stays silent for are the two the role documents as writing
// nothing: a lost race, and a precondition that held over a value already equal
// to the requested one. A hook script exists to observe changes, and firing on
// either of those would report a change no reader of the row could see.
//
// SWAPPED ALONE CANNOT DECIDE IT, because Swapped answers the PRECONDITION
// rather than the write. What decides it is the request's own pair: when the
// precondition held, the stored value WAS Expected and IS now Value, so the row
// moved exactly when those two differ. Reading the request back after the call
// is safe — an issueops.MetadataCAS never mutates caller-owned request values.
//
// An unreadable value cannot happen after a swap the role accepted, since it
// validated both sides; if one ever did, the hook fires, because a missed
// notification is the worse of the two failures.
func (c *hookMetadataCAS) CompareAndSetKey(ctx context.Context, request issueops.CompareAndSetKeyRequest) (issueops.CompareAndSetKeyResult, error) {
	result, err := c.inner.CompareAndSetKey(ctx, request)
	if err != nil || !result.Swapped {
		return result, err
	}
	unchanged, equalErr := MetadataValuesEqual(request.Expected, request.Value)
	if equalErr == nil && unchanged {
		return result, nil
	}
	c.hooks.CompleteIssueOperationMetadata(ctx, request.IssueID)
	return result, nil
}
