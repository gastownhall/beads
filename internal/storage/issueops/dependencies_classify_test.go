package issueops

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// TestClassifyDepTargetFromPresence pins the presence-first classification
// order every dependency write leg shares (GH#8299): an explicit external:
// reference is external without any target read, an active wisp wins over a
// same-prefix issue row, a same-prefix id is an issue without a probe, a
// differently-prefixed id is an issue only when a local row holds it, and an
// issue-probe failure is an error rather than silence — a failed read must
// not be read as absence, or an unreadable issues table would re-file every
// local cross-prefix edge as external.
func TestClassifyDepTargetFromPresence(t *testing.T) {
	probeErr := errors.New("issues unreadable")
	tests := []struct {
		name     string
		source   string
		target   string
		wispSet  map[string]bool
		issueSet map[string]bool
		issueErr error
		want     DepTargetKind
		wantErr  bool
		// wantIssueProbe: the issues probe must run (cross-prefix arm).
		wantIssueProbe bool
	}{
		{
			name:   "external reference never probes",
			source: "bd-1", target: "external:tracker/7",
			want: DepTargetExternal,
		},
		{
			name:   "active wisp wins regardless of prefix",
			source: "bd-1", target: "hq-2", wispSet: map[string]bool{"hq-2": true},
			want: DepTargetWisp,
		},
		{
			name:   "wisp beats a same-prefix issue row",
			source: "bd-1", target: "bd-2", wispSet: map[string]bool{"bd-2": true},
			want: DepTargetWisp,
		},
		{
			name:   "same prefix without a wisp is an issue, without probing issues",
			source: "bd-1", target: "bd-2",
			want: DepTargetIssue,
		},
		{
			name:   "cross-prefix issue row present is an issue",
			source: "bd-1", target: "hq-2", issueSet: map[string]bool{"hq-2": true},
			want:           DepTargetIssue,
			wantIssueProbe: true,
		},
		{
			name:   "cross-prefix id absent from both tables is external",
			source: "bd-1", target: "hq-2",
			want:           DepTargetExternal,
			wantIssueProbe: true,
		},
		{
			name:   "cross-prefix issue-probe error propagates",
			source: "bd-1", target: "hq-2", issueErr: probeErr,
			wantErr:        true,
			wantIssueProbe: true,
		},
		{
			name:   "no prefix on either id is same-plane",
			source: "nopl", target: "othernopl",
			want: DepTargetIssue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issueProbed := false
			presence := DepTargetPresence{
				WispActive: func(_ context.Context, id string) bool {
					return tt.wispSet[id]
				},
				IssueExists: func(_ context.Context, id string) (bool, error) {
					issueProbed = true
					if tt.issueErr != nil {
						return false, tt.issueErr
					}
					return tt.issueSet[id], nil
				},
			}
			got, err := ClassifyDepTargetFrom(context.Background(), tt.source, tt.target, presence)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ClassifyDepTargetFrom(%s -> %s) error = nil, want %v", tt.source, tt.target, probeErr)
				}
				if !errors.Is(err, probeErr) {
					t.Fatalf("ClassifyDepTargetFrom error = %v, want it to wrap %v", err, probeErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ClassifyDepTargetFrom(%s -> %s): %v", tt.source, tt.target, err)
			}
			if got != tt.want {
				t.Errorf("ClassifyDepTargetFrom(%s -> %s) = %v, want %v", tt.source, tt.target, got, tt.want)
			}
			if issueProbed != tt.wantIssueProbe {
				t.Errorf("issues probe ran = %v, want %v", issueProbed, tt.wantIssueProbe)
			}
		})
	}
}

// TestClassifyDepTargetInTx pins the transaction-backed classifier's reads:
// the wisp probe tolerates a missing wisps table (no wisps, not an error),
// and a cross-prefix target is re-read from issues before being called
// external.
func TestClassifyDepTargetInTx(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
		WithArgs("hq-2").
		WillReturnError(sqlmock.ErrCancelled) // any error: no active wisp
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\? LIMIT 1").
		WithArgs("hq-2").
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	dep := &types.Dependency{IssueID: "bd-1", DependsOnID: "hq-2", Type: types.DepBlocks}
	got, err := ClassifyDepTarget(ctx, tx, dep)
	if err != nil {
		t.Fatalf("ClassifyDepTarget: %v", err)
	}
	if got != DepTargetIssue {
		t.Errorf("ClassifyDepTarget = %v, want DepTargetIssue: the hq-2 row exists in issues", got)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestClassifyDepTargetInTxIssueProbeErrorIsFatal pins that an issues read
// failure is returned, never flattened into "absent".
func TestClassifyDepTargetInTxIssueProbeErrorIsFatal(t *testing.T) {
	ctx := context.Background()
	db, mock, tx := beginMockTx(t)
	defer db.Close()

	readErr := errors.New("issues table unreadable")
	mock.ExpectQuery("SELECT 1 FROM wisps WHERE id = \\? LIMIT 1").
		WithArgs("hq-2").
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery("SELECT 1 FROM issues WHERE id = \\? LIMIT 1").
		WithArgs("hq-2").
		WillReturnError(readErr)

	dep := &types.Dependency{IssueID: "bd-1", DependsOnID: "hq-2", Type: types.DepBlocks}
	if _, err := ClassifyDepTarget(ctx, tx, dep); err == nil {
		t.Fatal("ClassifyDepTarget error = nil, want the issues read failure")
	} else if !errors.Is(err, readErr) {
		t.Fatalf("ClassifyDepTarget error = %v, want it to wrap %v", err, readErr)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
