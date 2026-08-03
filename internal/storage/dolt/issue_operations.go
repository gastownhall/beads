package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// IssueLifecycle returns the guarded issue-lifecycle surface for this store.
func (s *DoltStore) IssueLifecycle() (issueops.Lifecycle, error) {
	return NewIssueOperations(s)
}

// NewIssueOperations returns guarded issue operations backed by store.
func NewIssueOperations(store *DoltStore) (issueops.Lifecycle, error) {
	if store == nil {
		return nil, &storage.ErrUnsupported{Op: "NewIssueOperations", Backend: "nil"}
	}
	return &issueOperations{store: store}, nil
}

type issueOperations struct{ store *DoltStore }

// updateCommitMessage names the updated issue in the Dolt commit message.
func updateCommitMessage(issueID string) string {
	if issueID == "" {
		return "bd: update issue"
	}
	return "bd: update " + issueID
}

func (o *issueOperations) Create(ctx context.Context, request issueops.CreateRequest) (issueops.CreateResult, error) {
	snapshot := storageissueops.CloneCreateRequest(request)
	var result issueops.CreateResult
	err := o.store.runIssueOperationTx(ctx, "bd: create issue", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		var err error
		var tables storageissueops.ChangedTables
		result, tables, err = storageissueops.ExecuteCreate(ctx, tx, snapshot)
		return tables, err
	})
	return result, err
}

func (o *issueOperations) Update(ctx context.Context, request issueops.UpdateRequest) (issueops.UpdateResult, error) {
	snapshot := storageissueops.CloneUpdateRequest(request)
	var result issueops.UpdateResult
	err := o.verifiedUpdate(ctx, snapshot, func() error {
		// The message names the issue because that is the one `bd dolt log`
		// affordance callers actually grep, and it is what the CLI's own
		// per-command commit wrote before updates moved onto this path.
		return o.store.runIssueOperationTx(ctx, updateCommitMessage(snapshot.IssueID), func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
			var err error
			var tables storageissueops.ChangedTables
			result, tables, err = storageissueops.ExecuteUpdate(ctx, tx, snapshot)
			return tables, err
		})
	})
	return result, err
}

// verifiedUpdate runs a facade update under claim-family verify-after-write
// (bd-zccb9) when the request writes coordination state, and unwrapped
// otherwise. The facade reaches the same writes as ClaimIssue and
// UpdateIssueWithOptions, so under a degraded server its exit status is no more
// trustworthy than theirs. Replay is safe: the claim CAS and the compare-and-set
// guards are re-checked inside the replayed transaction, so a racing writer
// makes the replay refuse rather than clobber.
func (o *issueOperations) verifiedUpdate(ctx context.Context, request issueops.UpdateRequest, write func() error) error {
	post, verify := updateClaimPostcondition(request)
	if !verify {
		return write()
	}
	return o.store.verifiedClaimWrite(ctx, request.IssueID, post, write)
}

// updateClaimPostcondition derives the row state a facade update must leave
// behind to count as applied, and whether the update is claim-family at all. A
// claim always is; an ordinary update is only when a compare-and-set guard
// authorizes a write to assignee or status. The postcondition describes the
// state the request intends, so a patch that overrides the claim's own
// assignee or status is honored rather than read as a lost write.
//
// A request that also moves the issue across the persistence boundary is
// exempt: the row can legitimately leave the issues table mid-write, which the
// re-read would report as an unverifiable claim.
func updateClaimPostcondition(request issueops.UpdateRequest) (claimPostcondition, bool) {
	if request.Patch.Persistence.Set {
		return claimPostcondition{}, false
	}
	updates := map[string]interface{}{}
	if request.Patch.Assignee.Set {
		updates["assignee"] = request.Patch.Assignee.Value
	}
	if request.Patch.Status.Set {
		updates["status"] = string(request.Patch.Status.Value)
	}
	if request.Claim {
		assignee, status := request.Actor, types.StatusInProgress
		if request.Patch.Assignee.Set {
			assignee = request.Patch.Assignee.Value
		}
		if request.Patch.Status.Set {
			status = request.Patch.Status.Value
		}
		return claimedAs(assignee, status), true
	}
	opts := storage.UpdateIssueOptions{ExpectedAssignee: request.ExpectedAssignee}
	if request.ExpectedStatus != nil {
		expected := string(*request.ExpectedStatus)
		opts.ExpectedStatus = &expected
	}
	return guardedUpdatePostcondition(opts, updates)
}

func (o *issueOperations) Close(ctx context.Context, request issueops.CloseRequest) (issueops.CloseResult, error) {
	snapshot := storageissueops.CloneCloseRequest(request)
	var result issueops.CloseResult
	err := o.store.runIssueOperationTx(ctx, "bd: close issue", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		var err error
		var tables storageissueops.ChangedTables
		result, tables, err = storageissueops.ExecuteClose(ctx, tx, snapshot)
		return tables, err
	})
	return result, err
}

func (o *issueOperations) Reopen(ctx context.Context, request issueops.ReopenRequest) (issueops.ReopenResult, error) {
	snapshot := storageissueops.CloneReopenRequest(request)
	var result issueops.ReopenResult
	err := o.store.runIssueOperationTx(ctx, "bd: reopen issue", func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		var err error
		var tables storageissueops.ChangedTables
		result, tables, err = storageissueops.ExecuteReopen(ctx, tx, snapshot)
		return tables, err
	})
	return result, err
}

var _ issueops.Lifecycle = (*issueOperations)(nil)
