package domain

import (
	"context"
	"errors"
	"fmt"
	"testing"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/types"
)

// deleteMissingTable builds the error a Dolt read raises when `table` is
// absent, in the go-mysql-server wording dberrors.MissingTableName parses.
func deleteMissingTable(table string) error {
	return &mysql.MySQLError{Number: 1146, Message: "table not found: " + table}
}

// stubDeleteIssueRepo answers only the reads the delete-preview path makes.
// The embedded interface is nil on purpose: a call this test did not intend
// panics rather than passing quietly.
type stubDeleteIssueRepo struct {
	IssueSQLRepository
	durable    []*types.Issue
	wisps      []*types.Issue
	wispErr    error
	dependents []string
}

func (r *stubDeleteIssueRepo) GetByIDs(_ context.Context, _ []string, opts IssueTableOpts) ([]*types.Issue, error) {
	if opts.UseWispsTable {
		if r.wispErr != nil {
			// The real reader wraps, and the callers classify through the wrap.
			return nil, fmt.Errorf("db: GetByIDs: %w", r.wispErr)
		}
		return r.wisps, nil
	}
	return r.durable, nil
}

func (r *stubDeleteIssueRepo) FindAllDependents(_ context.Context, _ []string) ([]string, error) {
	return r.dependents, nil
}

// stubDeleteDepRepo returns the same edge set for the durable plane and
// nothing for the wisp plane.
type stubDeleteDepRepo struct {
	DependencySQLRepository
	durable DepBulkResult
}

func (r *stubDeleteDepRepo) ListByIssueIDs(_ context.Context, _ []string, opts DepListOpts) (DepBulkResult, error) {
	if opts.UseWispsTable {
		return DepBulkResult{}, nil
	}
	return r.durable, nil
}

func deleteUseCase(issueRepo IssueSQLRepository, depRepo DependencySQLRepository) *issueUseCaseImpl {
	return &issueUseCaseImpl{issueRepo: issueRepo, depRepo: depRepo}
}

// TestPreviewDeleteBrokenWispPlaneIsAnError pins wy-sm01o2's half of the
// wy-237yfi lesson on the delete path: previewDelete's wisp read is
// `FROM wisps LEFT JOIN leases`, so a blanket table-not-exist tolerance
// reported a rig missing `leases` as a rig with no wisp plane and listed live
// wisps under NotFound.
func TestPreviewDeleteBrokenWispPlaneIsAnError(t *testing.T) {
	for _, missing := range []string{"leases", "wisp_labels"} {
		t.Run(missing, func(t *testing.T) {
			gone := deleteMissingTable(missing)
			u := deleteUseCase(&stubDeleteIssueRepo{wispErr: gone}, &stubDeleteDepRepo{})

			_, err := u.previewDelete(t.Context(), []string{"bd-1"})
			if !errors.Is(err, gone) {
				t.Fatalf("previewDelete hid a broken wisp plane: %v", err)
			}
		})
	}
}

// TestPreviewDeleteMissingWispsTableIsTolerated is the control: a
// pre-migration rig really has no wisps table, and delete still previews.
func TestPreviewDeleteMissingWispsTableIsTolerated(t *testing.T) {
	u := deleteUseCase(
		&stubDeleteIssueRepo{wispErr: deleteMissingTable("wisps")},
		&stubDeleteDepRepo{},
	)

	preview, err := u.previewDelete(t.Context(), []string{"bd-1"})
	if err != nil {
		t.Fatalf("previewDelete errored on a rig with no wisps table: %v", err)
	}
	if len(preview.NotFound) != 1 || preview.NotFound[0] != "bd-1" {
		t.Fatalf("NotFound = %v, want [bd-1]", preview.NotFound)
	}
}

// connectedOverBrokenWispPlane runs collectConnectedIssues with one durable
// edge, so the neighbour hydration below actually runs, and fails its wisp
// leg with wispErr.
func connectedOverBrokenWispPlane(t *testing.T, wispErr error) (map[string]*types.Issue, error) {
	t.Helper()
	deps := &stubDeleteDepRepo{durable: DepBulkResult{
		Outgoing: map[string][]*types.Dependency{
			"bd-1": {{IssueID: "bd-1", DependsOnID: "bd-2", Type: types.DepBlocks}},
		},
	}}
	u := deleteUseCase(&stubDeleteIssueRepo{wispErr: wispErr}, deps)
	out, _, err := u.collectConnectedIssues(t.Context(), []string{"bd-1"}, map[string]bool{"bd-1": true})
	return out, err
}

// TestCollectConnectedBrokenWispPlaneIsAnError is the second GetByIDs(wisps)
// site: swallowing anything but a missing `wisps` table left the neighbours
// unhydrated behind a nil error.
func TestCollectConnectedBrokenWispPlaneIsAnError(t *testing.T) {
	for _, missing := range []string{"leases", "wisp_labels"} {
		t.Run(missing, func(t *testing.T) {
			gone := deleteMissingTable(missing)
			if _, err := connectedOverBrokenWispPlane(t, gone); !errors.Is(err, gone) {
				t.Fatalf("hydrate neighbors hid a broken wisp plane: %v", err)
			}
		})
	}
}

// TestCollectConnectedMissingWispsTableIsTolerated is that site's control.
func TestCollectConnectedMissingWispsTableIsTolerated(t *testing.T) {
	out, err := connectedOverBrokenWispPlane(t, deleteMissingTable("wisps"))
	if err != nil {
		t.Fatalf("hydrate neighbors errored on a rig with no wisps table: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("got %d hydrated neighbours, want 0", len(out))
	}
}

// TestDeleteWispReadUnrelatedErrorPropagates is the fail-open control both
// sites share: a tolerance that cannot parse a table name out of an error
// must not treat it as an absent wisp plane.
func TestDeleteWispReadUnrelatedErrorPropagates(t *testing.T) {
	boom := errors.New("connection refused")

	u := deleteUseCase(&stubDeleteIssueRepo{wispErr: boom}, &stubDeleteDepRepo{})
	if _, err := u.previewDelete(t.Context(), []string{"bd-1"}); !errors.Is(err, boom) {
		t.Fatalf("previewDelete did not propagate a failed wisp read: %v", err)
	}
	if _, err := connectedOverBrokenWispPlane(t, boom); !errors.Is(err, boom) {
		t.Fatalf("hydrate neighbors did not propagate a failed wisp read: %v", err)
	}
}
