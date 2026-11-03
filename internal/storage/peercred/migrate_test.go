package peercred

import (
	"bytes"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPlanRejectsBlobSeenUnderAnotherBinding(t *testing.T) {
	victim := Binding{Name: "victim", URL: "https://victim.example/db", Username: "victim-user"}
	attacker := Binding{Name: "attacker", URL: "https://attacker.example/db", Username: "victim-user"}
	moved := Candidate{Row: Row{Binding: attacker, Blob: []byte("blob-v")}, Plaintext: "victim-secret"}
	own := Candidate{Row: Row{Binding: team, Blob: []byte("blob-t")}, Plaintext: "peerpass"}

	eligible, skipped := Plan([]Candidate{moved, own}, []Occurrence{
		{Binding: attacker, Blob: []byte("blob-v")},
		{Binding: victim, Blob: []byte("blob-v")}, // the original, now deleted
		{Binding: team, Blob: []byte("blob-t")},
		{Binding: team, Blob: []byte("blob-t")}, // same binding in many commits
	})
	if len(eligible) != 1 || eligible[0].Row.Binding != team {
		t.Errorf("eligible = %+v, want only the team row", eligible)
	}
	if len(skipped) != 1 || skipped[0].Row.Binding != attacker {
		t.Errorf("skipped = %+v, want only the attacker row", skipped)
	}
}

func TestSkipWarningNeverPrintsSecretsOrURL(t *testing.T) {
	out := SkipWarning("linux", Row{
		Binding:     Binding{Name: "team", URL: "https://user:pw@peer.example/db", Username: "peeruser"},
		Sovereignty: "T2",
		Blob:        []byte("ciphertext-bytes"),
	})
	if !strings.Contains(out, "bd federation add-peer team <url> --user peeruser --sovereignty T2") {
		t.Errorf("warning %q lacks the re-add command", out)
	}
	for _, leak := range []string{"peer.example", "user:pw", "ciphertext-bytes"} {
		if strings.Contains(out, leak) {
			t.Errorf("warning %q leaks %q", out, leak)
		}
	}
	if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Errorf("warning %q is not one line", out)
	}

	hostile := SkipWarning("linux", Row{Binding: Binding{Name: "x; rm -rf ~\n", Username: "$(id)"}})
	if strings.Count(hostile, "\n") != 1 {
		t.Errorf("hostile name broke the line: %q", hostile)
	}
	if strings.Contains(hostile, "add-peer x; rm") || strings.Contains(hostile, "--user $(id)") {
		t.Errorf("hostile values pasted into the command: %q", hostile)
	}
}

// legacyRowsMock serves one current federation_peers row holding a legacy
// blob for team under key.
func legacyRowsMock(t *testing.T, key []byte) (*sql.DB, sqlmock.Sqlmock, []byte) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	blob := legacySeal(t, key, bytes.Repeat([]byte{7}, 12), "peerpass")
	mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "remote_url", "username", "password_encrypted", "sovereignty"}).
			AddRow(team.Name, team.URL, team.Username, blob, "T2"))
	return db, mock, blob
}

// Provenance the migration cannot read in full is not evidence that a blob
// is unambiguous: Migrate fails and rewrites nothing.
func TestMigrateFailsClosedOnIncompleteHistory(t *testing.T) {
	key := testKey(1)
	db, mock, _ := legacyRowsMock(t, key)
	mock.ExpectQuery(regexp.QuoteMeta("FROM dolt_history_federation_peers")).
		WillReturnError(errors.New("history unavailable"))

	res, err := Migrate(t.Context(), db, key, [][]byte{key})
	if err == nil {
		t.Fatal("Migrate succeeded without the credential history")
	}
	if len(res.Migrated) != 0 {
		t.Errorf("Migrated = %v, want none", res.Migrated)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// A row changed between the scan and the rewrite is not overwritten: the
// compare-and-swap misses and Migrate fails without reporting the row.
func TestMigrateCompareAndSwapMissFails(t *testing.T) {
	key := testKey(1)
	db, mock, blob := legacyRowsMock(t, key)
	mock.ExpectQuery(regexp.QuoteMeta("FROM dolt_history_federation_peers")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "remote_url", "username", "password_encrypted"}).
			AddRow(team.Name, team.URL, team.Username, blob))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE federation_peers SET password_encrypted = ?")).
		WithArgs(sqlmock.AnyArg(), team.Name, team.URL, team.Username, blob).
		WillReturnResult(sqlmock.NewResult(0, 0))

	res, err := Migrate(t.Context(), db, key, [][]byte{key})
	if err == nil || !strings.Contains(err.Error(), "the row changed during migration") {
		t.Fatalf("Migrate error = %v, want the compare-and-swap miss", err)
	}
	if len(res.Migrated) != 0 {
		t.Errorf("Migrated = %v, want none", res.Migrated)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
