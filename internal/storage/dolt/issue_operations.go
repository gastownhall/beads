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
	err := o.verifiedUpdate(ctx, snapshot, func(ctx context.Context) error {
		// The message names the issue because that is the one `bd dolt log`
		// affordance callers actually grep, and it is what the CLI's own
		// per-command commit wrote before updates moved onto this path.
		return o.store.runIssueOperationTx(ctx, storageissueops.HistoryEntry(snapshot.Provenance, updateCommitMessage(snapshot.IssueID)), func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
			var err error
			var tables storageissueops.ChangedTables
			result, tables, err = storageissueops.ExecuteUpdate(ctx, tx, snapshot)
			return tables, err
		})
	})
	return result, err
}

// verifiedUpdate verifies a coordination-only claim or a qualifying guarded
// update after a write (bd-zccb9), and preserves the write outcome otherwise.
// A re-read of assignee and status cannot prove an ordinary patch or its event
// landed, even when the claim state already matches. The facade reaches the
// same writes as ClaimIssue and UpdateIssueWithOptions, so under a degraded
// server its exit status is no more trustworthy than theirs.
//
// A verifying update runs write and its post-write re-read under withCircuitWrite
// so terminal circuit success is recorded once at the boundary, only after
// verifiedClaimWrite returns nil — matching issueClaimer.Claim and the store's
// ClaimIssue. write is invoked with the circuit-managed context so its nested
// runIssueOperationTx defers success to that boundary instead of recording it
// eagerly when the SQL commit lands but before the re-read can fail. A
// non-verifying update keeps the original context and its eager accounting,
// leaving the ordinary patch path unchanged.
func (o *issueOperations) verifiedUpdate(ctx context.Context, request issueops.UpdateRequest, write func(context.Context) error) error {
	post, verify := updateClaimPostcondition(request)
	if !verify {
		return write(ctx)
	}
	return o.store.withCircuitWrite(ctx, func(ctx context.Context) error {
		return o.store.verifiedClaimWrite(ctx, request.IssueID, post, func() error {
			return write(ctx)
		})
	})
}

// updateClaimPostcondition derives the row state a facade update must leave
// behind to count as applied, and whether that state is enough to prove the
// whole update. A claim is verifiable only when its patch changes no state
// beyond assignee and status. An ordinary update is verifiable only when a
// compare-and-set guard authorizes a coordination-only write. The postcondition
// honors a coordination-only claim patch that overrides the claim's own
// assignee or status.
func updateClaimPostcondition(request issueops.UpdateRequest) (claimPostcondition, bool) {
	if hasNonCoordinationPatch(request.Patch) {
		return claimPostcondition{}, false
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
	updates := map[string]interface{}{}
	if request.Patch.Assignee.Set {
		updates["assignee"] = request.Patch.Assignee.Value
	}
	if request.Patch.Status.Set {
		updates["status"] = string(request.Patch.Status.Value)
	}
	opts := storage.UpdateIssueOptions{ExpectedAssignee: request.ExpectedAssignee}
	if request.ExpectedStatus != nil {
		expected := string(*request.ExpectedStatus)
		opts.ExpectedStatus = &expected
	}
	return guardedUpdatePostcondition(opts, updates)
}

// hasNonCoordinationPatch reports whether an update also changes state that an
// assignee/status re-read cannot prove.
func hasNonCoordinationPatch(patch issueops.IssuePatch) bool {
	for _, changed := range nonCoordinationPatchSignals(patch) {
		if changed {
			return true
		}
	}
	return false
}

// nonCoordinationPatchSignals enumerates a "changed" bool for every settable
// IssuePatch field except the coordination pair (Assignee, Status) — the only
// two an assignee/status re-read can prove. Keep this explicit rather than
// reflective so each field is visibly classified; add a signal here whenever
// IssuePatch, LabelPatch, or MetadataPatch grows a field. TestNonCoordination*
// fails until you do.
func nonCoordinationPatchSignals(patch issueops.IssuePatch) []bool {
	return []bool{
		patch.Title.Set,
		patch.Description.Set,
		patch.Design.Set,
		patch.AcceptanceCriteria.Set,
		patch.Notes.Set,
		patch.AppendNotes.Set,
		patch.SpecID.Set,
		patch.AwaitID.Set,
		patch.Priority.Set,
		patch.IssueType.Set,
		patch.Owner.Set,
		patch.ClosedBySession.Set,
		patch.EstimatedMinutes.Set,
		patch.ExternalRef.Set,
		patch.DueAt.Set,
		patch.DeferUntil.Set,
		patch.Persistence.Set,
		patch.ParentID.Set,
		len(patch.Labels.Add) != 0,
		len(patch.Labels.Remove) != 0,
		patch.Labels.Replace.Set,
		len(patch.Metadata.Set) != 0,
		len(patch.Metadata.Unset) != 0,
		patch.Metadata.Replace.Set,
		patch.Metadata.Merge.Set,
	}
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
	err := o.store.runIssueOperationTx(ctx, storageissueops.HistoryEntry(snapshot.Provenance, "bd: reopen issue"), func(tx *sql.Tx) (storageissueops.ChangedTables, error) {
		var err error
		var tables storageissueops.ChangedTables
		result, tables, err = storageissueops.ExecuteReopen(ctx, tx, snapshot)
		return tables, err
	})
	return result, err
}

var _ issueops.Lifecycle = (*issueOperations)(nil)
