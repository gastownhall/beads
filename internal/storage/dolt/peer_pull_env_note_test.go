package dolt

import (
	"context"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
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
