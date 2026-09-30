package externaldeps

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestUOWForcedClosingUpdatePassesTheExternalGuard pins the unit-of-work arm's
// status-closed update to the store arm's rule: ForceClosePolicy (`bd update
// --status closed --force`, PATCH force_close_policy) bypasses the external
// close guard, and the lifecycle resolves no foreign project for it. The
// ApplyUpdate override used to call the guard unforced, so the proxied route
// and serve's provider arm refused a forced close every other route allowed.
// Unforced, it is still refused.
func TestUOWForcedClosingUpdatePassesTheExternalGuard(t *testing.T) {
	blocked := issue("be-blocked")
	issues := &fakeIssueUseCase{ready: []*types.Issue{blocked}}
	inner := &fakeUOW{issues: issues, deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
		blocked.ID: {externalDep(blocked.ID, "external:remote:missing", types.DepBlocks)},
	}}}
	opens := 0
	provider := WrapUOWProvider(&openCountingProvider{uw: inner},
		func(project ProjectName) (string, bool) { return "/projects/remote", project == "remote" },
		func(context.Context, string) (storage.DoltStorage, error) { opens++; return &fakeStore{}, nil })
	lifecycle, err := provider.(uow.IssueLifecycleSource).IssueLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	closing := publicops.IssuePatch{Status: publicops.Field[publicops.Status]{Set: true, Value: types.StatusClosed}}
	ctx := t.Context()

	if _, err := lifecycle.Update(ctx, publicops.UpdateRequest{IssueID: blocked.ID, Actor: "w", Patch: closing}); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("unforced closing update: err = %v, want the external refusal", err)
	}
	if slices.Contains(issues.closed, blocked.ID) {
		t.Fatal("the refused update reached ApplyUpdate's body")
	}
	opens = 0
	if _, err := lifecycle.Update(ctx, publicops.UpdateRequest{IssueID: blocked.ID, Actor: "w", Patch: closing, ForceClosePolicy: true}); err != nil {
		t.Fatalf("forced closing update: %v", err)
	}
	if !slices.Contains(issues.closed, blocked.ID) {
		t.Error("the forced update did not reach ApplyUpdate's body")
	}
	if opens != 0 {
		t.Errorf("a forced close opened %d foreign projects; it is not guarded, so it resolves nothing", opens)
	}
}
