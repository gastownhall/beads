package uowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// createOpts selects which of DoltStore's per-entry-point create rules apply.
// They differ by entry point on DoltStore, and the adapter matches each:
//
//   - routeWispTypes: the flat single CreateIssue also routes a wisp-typed or
//     infrastructure-typed issue to the wisp plane (marking it Ephemeral), as
//     DoltStore.CreateIssue does; the batch and transaction creates route on
//     issueops.IsWisp alone.
//   - validatePrefix: the flat CreateIssues validates a caller-named ID
//     against issue_prefix / allowed_prefixes (DoltStore's
//     SkipPrefixValidation=false); every other create skips it.
//   - withDeps: both CreateIssues persist each issue's Dependencies after the
//     whole batch is inserted, after the mixed-bucket check.
type createOpts struct {
	routeWispTypes bool
	validatePrefix bool
	withDeps       bool
}

// create is the Storage create contract over one unit of work. It goes through
// the same domain create use case as the proxied `bd create`, which mints,
// upserts a caller-named ID, refuses a retired ID, reserves a child suffix and
// writes the created event, journal entry and versioned row. Around it, create
// adds what DoltStore's create does and the use case leaves to its caller:
//
//   - PrepareIssueForInsert (metadata validation, timestamp normalization,
//     closed_at, custom status/type validation, content hash);
//   - plane routing by issueops.IsWisp, so NoHistory and WispPlaneOverride are
//     honored, not only Ephemeral;
//   - a refusal when a caller-named ID already exists in the other plane
//     (issues and wisps share one ID space);
//   - issue.Labels and issue.Comments persisted with the issue, the minted
//     comment IDs written back onto the caller's comment structs;
//   - for a batch, issue.Dependencies persisted with no dependency_added
//     events-table row, as DoltStore's create-with-deps writes them.
//
// KNOWN DIVERGENCES from DoltStore's create. The first two reach any batch;
// the rest need import-shaped input (explicit IDs over existing rows,
// preset comments or dependency timestamps):
//
//   - a batch dependency whose target does not exist is refused by the
//     dependency use case, where DoltStore skips it;
//   - a batch dependency's created_by is the acting user, and its created_at
//     is the write time (dep.CreatedAt is not honored);
//   - re-creating an explicit ID over an existing live row upserts it: another
//     `created` events-table row is written (DoltStore writes one only for a
//     new row), and no imported lease is restored or orphaned lease dropped
//     (DoltStore's RestoreLeaseOnImportInTx);
//   - a create-time comment with a zero CreatedAt is stamped time.Now(), not
//     NextLiveCommentTime, so several comments in one create can share a
//     second and read back in digest order rather than list order;
//   - two identical create-time comments collapse onto one row but are each
//     journaled (DoltStore journals only inserted comments), and an explicit
//     comment ID that is already stored fails with a duplicate-key error where
//     DoltStore skips it or names the issue that owns it;
//   - on such a re-create, a batch dependency edge that already exists with a
//     different type fails with DependencyTypeConflictError, and one with the
//     same type but changed metadata or thread is rewritten, journaled and
//     versioned, where DoltStore leaves the edge untouched.
func (t *txAdapter) create(ctx context.Context, issues []*types.Issue, actor string, opts createOpts) error {
	for i, issue := range issues {
		if issue == nil {
			return fmt.Errorf("CreateIssues: issue %d must not be nil", i)
		}
	}
	if len(issues) == 0 {
		return nil
	}
	cc, err := t.uw.ConfigUseCase().LoadCreateContext(ctx)
	if err != nil {
		return err
	}
	if opts.routeWispTypes {
		for _, issue := range issues {
			if !issue.Ephemeral && !issue.NoHistory && (issue.WispType != "" || cc.InfraTypes[string(issue.IssueType)]) {
				issue.Ephemeral = true
			}
		}
	}
	if opts.withDeps {
		if err := issueops.ValidateCreateIssuesMixedBucketDependencies(issues); err != nil {
			return err
		}
	}

	claimed := make(map[string]bool, len(issues))
	for _, issue := range issues {
		if issue.ID != "" {
			claimed[issue.ID] = true
		}
	}
	prefix := strings.TrimSuffix(cc.IssuePrefix, "-")
	statuses := types.CustomStatusNames(cc.CustomStatuses)
	for _, issue := range issues {
		if err := t.createOne(ctx, issue, actor, cc, prefix, statuses, claimed, opts); err != nil {
			return err
		}
	}

	if !opts.withDeps {
		return nil
	}
	var deps []*types.Dependency
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep == nil {
				continue
			}
			if dep.IssueID == "" {
				dep.IssueID = issue.ID
			}
			deps = append(deps, dep)
		}
	}
	if len(deps) == 0 {
		return nil
	}
	_, err = t.uw.DependencyUseCase().AddDependencies(ctx, deps, actor, domain.BulkAddDepsOpts{SuppressEvents: true})
	return err
}

func (t *txAdapter) createOne(ctx context.Context, issue *types.Issue, actor string, cc domain.CreateContext, prefix string, statuses []string, claimed map[string]bool, opts createOpts) error {
	if issue.Status == "" {
		issue.Status = types.StatusOpen
	}
	explicit := issue.ID != ""
	if explicit && opts.validatePrefix {
		if err := issueops.ValidateIssueIDPrefix(issue.ID, prefix, cc.AllowedPrefixes); err != nil {
			return err
		}
	}
	if err := issueops.PrepareIssueForInsert(issue, statuses, cc.CustomTypes); err != nil {
		return err
	}
	useWisp := issueops.IsWisp(issue)
	if explicit {
		if err := t.refuseCrossPlane(ctx, issue.ID, useWisp); err != nil {
			return err
		}
	}

	// The use case replaces issue.Comments with the rows it inserted. Hand the
	// caller its own slice back, with the minted IDs on its own structs, the
	// way DoltStore's PersistComments fills them in place.
	callerComments := issue.Comments
	defer func() { issue.Comments = callerComments }()

	params := domain.CreateIssueParams{
		Issue:      issue,
		Labels:     issue.Labels,
		Comments:   callerComments,
		ClaimedIDs: claimed,
	}
	uc := t.uw.IssueUseCase()
	var res domain.CreateIssueResult
	var err error
	if useWisp {
		res, err = uc.CreateWisp(ctx, params, actor)
	} else {
		res, err = uc.CreateIssue(ctx, params, actor)
	}
	if err != nil {
		return err
	}
	created := issue
	if res.Issue != nil {
		created = res.Issue
		// The use case mints into the struct it was handed; write the ID back
		// anyway in case a future use case returns a copy, because callers
		// read issue.ID after CreateIssue on every other backend.
		if created != issue && issue.ID == "" {
			issue.ID = created.ID
		}
	}
	for i, inserted := range created.Comments {
		if i < len(callerComments) && callerComments[i] != nil && inserted != nil && callerComments[i].ID == "" {
			callerComments[i].ID = inserted.ID
		}
	}
	t.planes[issue.ID] = useWisp
	claimed[issue.ID] = true
	return nil
}

// refuseCrossPlane refuses a caller-named ID the other plane already holds.
// A same-plane row is the create use case's to upsert, as on DoltStore.
func (t *txAdapter) refuseCrossPlane(ctx context.Context, id string, useWisp bool) error {
	var err error
	other := "wisps"
	if useWisp {
		other = "issues"
		_, err = t.uw.IssueUseCase().GetIssue(ctx, id)
	} else {
		_, err = t.uw.IssueUseCase().GetWisp(ctx, id)
	}
	switch {
	case err == nil:
		return fmt.Errorf("cannot create %q: ID already exists in the %s table (issues and wisps share one ID space)", id, other)
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		return nil
	default:
		return fmt.Errorf("cannot create %q: check the %s table: %w", id, other, err)
	}
}
