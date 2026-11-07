package dolt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestPeerPullFailureNamesServerEnvironment pins bd-f68dud (GH#5953) for a
// federation pull: a server-mode pull from a cloud peer whose transfer fails,
// leaving no conflicts behind, returns one DOLT_PULL note naming the peer
// scheme's variables bd holds. A failed pull whose conflicts PullFrom returns
// as data keeps that contract: conflicts and no error.
func TestPeerPullFailureNamesServerEnvironment(t *testing.T) {
	const s3URL = "s3://bucket/beads"
	setup := func(t *testing.T) (*DoltStore, sqlmock.Sqlmock) {
		t.Helper()
		clearPredicateEnv(t)
		clearCloudAuthEnv(t)
		refusingDoltOnPath(t, "peer", s3URL, "pull")
		t.Setenv("AWS_ACCESS_KEY_ID", "sentinel-key")
		return envNoteStore(t, "peer", s3URL, envNoteOpts{cli: true, peerUser: "peer-user", readOnly: true})
	}

	t.Run("transfer fails without conflicts", func(t *testing.T) {
		store, mock := setup(t)
		mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))

		conflicts, err := store.PullFrom(context.Background(), "peer")

		if len(conflicts) != 0 {
			t.Fatalf("PullFrom conflicts = %v, want none", conflicts)
		}
		if err == nil || !strings.Contains(err.Error(), "failed to pull from peer peer: pull from peer peer failed") {
			t.Fatalf("PullFrom = %v, want the CLI pull failure in its existing context", err)
		}
		checkServerEnvNote(t, err, "DOLT_PULL", "AWS_*", s3URL)
	})

	t.Run("conflicts are returned as data", func(t *testing.T) {
		store, mock := setup(t)
		mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(
			sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("issues", 2))

		conflicts, err := store.PullFrom(context.Background(), "peer")

		if err != nil {
			t.Fatalf("PullFrom = %v, want the conflicts returned as data with no error", err)
		}
		if len(conflicts) != 1 || conflicts[0].Field != "issues" {
			t.Fatalf("PullFrom conflicts = %+v, want the issues table's", conflicts)
		}
	})
}

// TestPeerSQLPullFailureNamesServerEnvironment pins bd-f68dud (GH#5953) for a
// federation pull on the SQL route: CALL DOLT_PULL failing on a server that
// took the connection, or the GH#3144 fallback's CALL DOLT_FETCH failing in
// its place, returns one DOLT_PULL note. The fallback's local DOLT_MERGE and
// a connection that never reached the server are not transfers, so they keep
// their error with no note.
func TestPeerSQLPullFailureNamesServerEnvironment(t *testing.T) {
	const s3URL = "s3://bucket/beads"
	const noTracking = "You must specify a branch: you did not specify a branch to pull from"
	refused := "Error 1105 (HY000): " + refusedCallMessage

	for _, tc := range []struct {
		name        string
		o           envNoteOpts
		wantContext string
		vars        string
	}{
		{
			name:        "pull refused mid-transfer",
			wantContext: "failed to pull from peer peer: " + refused, vars: "AWS_*",
		},
		{
			name:        "fallback fetch refused",
			o:           envNoteOpts{replies: map[string]string{"CALL DOLT_PULL": noTracking}},
			wantContext: "failed to pull from peer peer: fetch from peer/main: " + refused, vars: "AWS_*",
		},
		{
			name:        "fallback merge fails after the fetch landed",
			o:           envNoteOpts{replies: map[string]string{"CALL DOLT_PULL": noTracking, "CALL DOLT_FETCH": ""}},
			wantContext: "failed to pull from peer peer: " + refused,
		},
		{
			name:        "server never took the connection",
			o:           envNoteOpts{unreachable: true},
			wantContext: "failed to pull from peer peer: failed to begin transaction",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearPredicateEnv(t)
			clearCloudAuthEnv(t)
			refusingDoltOnPath(t, "peer", s3URL)
			t.Setenv("AWS_ACCESS_KEY_ID", "sentinel-key")
			tc.o.readOnly = true
			store, mock := envNoteStore(t, "peer", s3URL, tc.o)
			mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))

			conflicts, err := store.PullFrom(context.Background(), "peer")

			if len(conflicts) != 0 {
				t.Fatalf("PullFrom conflicts = %v, want none", conflicts)
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantContext) {
				t.Fatalf("PullFrom = %v, want it to keep %q", err, tc.wantContext)
			}
			checkServerEnvNote(t, err, "DOLT_PULL", tc.vars, s3URL)
		})
	}
}

// TestSettleMergeInTxNamesServerEnvironment pins where the SQL pull's note
// attaches through the settle machinery (bd-f68dud, GH#5953): a transfer
// failure settlement surfaces as is carries one note once the pull is
// annotated, and conflicts the resolver declined stay data for the operator,
// with no note and the pull's own error still in MergeErr.
func TestSettleMergeInTxNamesServerEnvironment(t *testing.T) {
	ctx := context.Background()
	const s3URL = "s3://bucket/beads"
	pullFailed := errors.New("error 1105: pull failed mid-transfer")
	env := []string{"AWS_ACCESS_KEY_ID=sentinel-key"}

	// settleCase is a server-mode store around a fresh sqlmock, whose
	// dolt_remotes row names an s3 peer, and a begun tx to settle on.
	settleCase := func(t *testing.T) (*DoltStore, sqlmock.Sqlmock, *sql.Tx) {
		t.Helper()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.MatchExpectationsInOrder(false)
		mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes").
			WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}).AddRow("peer", s3URL, nil))
		mock.ExpectBegin()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		return &DoltStore{serverMode: true, db: db}, mock, tx
	}
	settle := func(s *DoltStore, tx *sql.Tx) error {
		return s.withServerEnvNote(ctx, "DOLT_PULL", "peer", nil, env, s.settleMergeInTx(ctx, tx, remoteTransferFailure(pullFailed)))
	}

	t.Run("plain transfer failure carries one note", func(t *testing.T) {
		store, mock, tx := settleCase(t)
		mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"})) // resolver pre-screen
		mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"})) // declined-set capture
		mock.ExpectQuery("FROM dolt_constraint_violations").WillReturnRows(sqlmock.NewRows([]string{"table", "num_violations"}))
		mock.ExpectRollback()

		err := settle(store, tx)

		if !errors.Is(err, pullFailed) {
			t.Fatalf("settle = %v, want it to still identify the pull's own error", err)
		}
		checkServerEnvNote(t, err, "DOLT_PULL", "AWS_*", s3URL)
	})

	t.Run("operator conflicts stay data with no note", func(t *testing.T) {
		store, mock, tx := settleCase(t)
		mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("roadmaps", 3)) // resolver pre-screen declines roadmaps
		mock.ExpectQuery("FROM dolt_conflicts").WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("roadmaps", 3)) // captured before the rollback
		mock.ExpectRollback()

		err := settle(store, tx)

		var mce *versioncontrolops.MergeConflictsError
		if !errors.As(err, &mce) {
			t.Fatalf("settle = %v, want MergeConflictsError", err)
		}
		if len(mce.Conflicts) != 1 || mce.Conflicts[0].Field != "roadmaps" || mce.Conflicts[0].Count != 3 {
			t.Fatalf("conflicts = %+v, want the roadmaps row", mce.Conflicts)
		}
		if !errors.Is(mce.MergeErr, pullFailed) {
			t.Fatalf("MergeErr = %v, want the pull's own error", mce.MergeErr)
		}
		checkServerEnvNote(t, err, "DOLT_PULL", "", s3URL)
	})
}

// TestFinishPeerPullFailureCarriesNoCredentialNote pins that a peer pull whose
// transfer succeeded but whose post-merge is_blocked recompute failed keeps
// the recompute's error clean (bd-f68dud, GH#5953): the note explains
// transfer credentials, not derived-state recomputes. The unroutable connStr
// makes the recompute's long-timeout connection fail at once.
func TestFinishPeerPullFailureCarriesNoCredentialNote(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := &DoltStore{serverMode: true, db: db, connStr: unroutableConnStr}

	conflicts, err := store.finishPeerPull(context.Background(), nil, nil, "")

	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}
	if err == nil || !strings.Contains(err.Error(), "pull succeeded but is_blocked recompute failed") {
		t.Fatalf("err = %v, want the recompute failure", err)
	}
	checkServerEnvNote(t, err, "DOLT_PULL", "", "")
}
