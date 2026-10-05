// Written fresh for OSS beads S2 (no bd-enterprise source copied).
package wire

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// applyBatchRequestOfSize builds the smallest valid ApplyBatchRequest with n
// items -- a plain create apiece, since ApplyBatch's own cap logic never looks
// past len(body.Items).
func applyBatchRequestOfSize(n int) ApplyBatchRequest {
	items := make([]ApplyItem, n)
	for i := range items {
		items[i] = ApplyItem{Kind: string(apigen.ApplyItemKindCreate), Create: &apigen.ApplyCreateItem{Title: "t"}}
	}
	return ApplyBatchRequest{Actor: "w", Items: items}
}

// This suite is task #3's own behavior, separate from the generic two-speed
// gate writes_twospeed_test.go already proves for OpApplyBatch (whether the
// OPERATION "issues.batchApply" is routed at all). Here the operation is
// always routed; what is under test is the ITEM-COUNT ceiling ApplyBatch
// enforces for itself, off issues.batchApplyLarge, before ever dialing
// PathIssuesBatchApply.

// withBatchApply advertises issues.batchApply (so Preflight never refuses)
// plus whatever else the caller wants layered on top.
func withBatchApply(extra ...string) []string {
	return append([]string{"issues.batchApply"}, extra...)
}

func TestApplyBatchAtOrUnderTheFloorSucceedsWithoutTheCapability(t *testing.T) {
	c, rec := newTestClient(t, Options{}, nil, serveEverything(contextBody("v0", "proj-1", withBatchApply()...)))
	if _, err := c.ApplyBatch(ctx(t), applyBatchRequestOfSize(defaultApplyBatchItemCap)); err != nil {
		t.Fatalf("ApplyBatch at the floor (%d items): %v", defaultApplyBatchItemCap, err)
	}
	// One handshake (cached across Handshake-then-Preflight) plus the batch
	// apply itself.
	if rec.count() != 2 {
		t.Fatalf("made %d requests, want 2 (context, then the batch apply)", rec.count())
	}
	if got := rec.at(t, 1).path; got != PathIssuesBatchApply {
		t.Errorf("second request = %q, want %q", got, PathIssuesBatchApply)
	}
}

func TestApplyBatchOverTheFloorWithoutTheCapabilityRefusesBeforeDialing(t *testing.T) {
	c, rec := newTestClient(t, Options{}, nil, serveEverything(contextBody("v0", "proj-1", withBatchApply()...)))
	n := defaultApplyBatchItemCap + 1
	_, err := c.ApplyBatch(ctx(t), applyBatchRequestOfSize(n))

	var capErr *CapabilityError
	if !errors.As(err, &capErr) {
		t.Fatalf("err = %v (%T), want *CapabilityError", err, err)
	}
	if capErr.Capability != CapBatchApplyLarge {
		t.Errorf("Capability = %q, want %q", capErr.Capability, CapBatchApplyLarge)
	}
	// Only the handshake's own context fetch; the batch-apply endpoint must
	// never be dialed once the item count alone is enough to refuse.
	if rec.count() != 1 {
		t.Fatalf("made %d requests, want only the handshake", rec.count())
	}
	if got := rec.at(t, 0).path; got != PathContext {
		t.Errorf("only request = %q, want %q", got, PathContext)
	}
}

func TestApplyBatchAtTheRaisedCeilingSucceedsWithTheCapability(t *testing.T) {
	c, rec := newTestClient(t, Options{}, nil,
		serveEverything(contextBody("v0", "proj-1", withBatchApply(CapBatchApplyLarge)...)))
	n := issueops.MaxApplyBatchItems
	if _, err := c.ApplyBatch(ctx(t), applyBatchRequestOfSize(n)); err != nil {
		t.Fatalf("ApplyBatch at the raised ceiling (%d items) with %s advertised: %v", n, CapBatchApplyLarge, err)
	}
	if rec.count() != 2 {
		t.Fatalf("made %d requests, want 2 (context, then the batch apply)", rec.count())
	}
	if got := rec.at(t, 1).path; got != PathIssuesBatchApply {
		t.Errorf("second request = %q, want %q", got, PathIssuesBatchApply)
	}
}

func TestApplyBatchBetweenTheFloorAndTheCeilingWithTheCapabilitySucceeds(t *testing.T) {
	// A count the floor alone would refuse, admitted once the server raises
	// its own ceiling -- the capability actually changing the outcome, not
	// just being present.
	c, _ := newTestClient(t, Options{}, nil,
		serveEverything(contextBody("v0", "proj-1", withBatchApply(CapBatchApplyLarge)...)))
	n := defaultApplyBatchItemCap + 1
	if _, err := c.ApplyBatch(ctx(t), applyBatchRequestOfSize(n)); err != nil {
		t.Fatalf("ApplyBatch at %d items with %s advertised: %v", n, CapBatchApplyLarge, err)
	}
}

func TestApplyBatchOverTheAbsoluteCeilingRefusesRegardlessOfTheCapability(t *testing.T) {
	for _, tc := range []struct {
		name         string
		capabilities []string
	}{
		{"capability absent", withBatchApply()},
		{"capability present", withBatchApply(CapBatchApplyLarge)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newTestClient(t, Options{}, nil, serveEverything(contextBody("v0", "proj-1", tc.capabilities...)))
			n := issueops.MaxApplyBatchItems + 1
			_, err := c.ApplyBatch(ctx(t), applyBatchRequestOfSize(n))

			var tooLarge *BatchTooLargeError
			if !errors.As(err, &tooLarge) {
				t.Fatalf("err = %v (%T), want *BatchTooLargeError", err, err)
			}
			if tooLarge.Count != n {
				t.Errorf("Count = %d, want %d", tooLarge.Count, n)
			}
			if tooLarge.Limit != issueops.MaxApplyBatchItems {
				t.Errorf("Limit = %d, want %d (no token raises this further)", tooLarge.Limit, issueops.MaxApplyBatchItems)
			}
			if !errors.Is(err, issueops.ErrValidation) {
				t.Error("err does not satisfy issueops.ErrValidation; over the absolute ceiling is a validation refusal, not a skew one")
			}
			// No token any server advertises raises this further, so this must
			// never reach the batch-apply endpoint either.
			if rec.count() != 1 {
				t.Fatalf("made %d requests, want only the handshake", rec.count())
			}
		})
	}
}
