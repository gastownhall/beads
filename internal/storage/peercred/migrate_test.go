package peercred

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
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

// sealedFor matches a v2 ciphertext that opens under key for binding.
type sealedFor struct {
	key      []byte
	binding  Binding
	password string
}

func (m sealedFor) Match(v driver.Value) bool {
	blob, ok := v.([]byte)
	if !ok {
		return false
	}
	got, err := Open(m.key, m.binding, blob)
	return err == nil && got == m.password
}

// Adapted from TestMigrateCredentialKeysSkipsUntrustedLegacyBlobs in the
// contributor's PR #7891 (Wenjix): the dbPath-derived key vintage goes
// through the same provenance check as the current key. A blob duplicated
// across two rows and a blob whose history shows another binding stay
// unchanged; only the unambiguous row is resealed under the current key.
func TestMigrateSkipsUntrustedLegacyBlobsUnderEitherKey(t *testing.T) {
	newKey := testKey(1)
	derivedKey := testKey(90)
	nonce := func(b byte) []byte { return bytes.Repeat([]byte{b}, 12) }
	shared := legacySeal(t, derivedKey, nonce(1), "shared-password")
	moved := legacySeal(t, derivedKey, nonce(2), "moved-password")
	unique := legacySeal(t, derivedKey, nonce(3), "unique-password")
	own := legacySeal(t, newKey, nonce(4), "own-password")

	dupA := Binding{Name: "duplicate-a", URL: "https://duplicate-a.example/beads", Username: "user-a"}
	dupB := Binding{Name: "duplicate-b", URL: "https://duplicate-b.example/beads", Username: "user-b"}
	movedNow := Binding{Name: "moved", URL: "https://attacker.example/beads", Username: "user-m"}
	movedThen := Binding{Name: "moved", URL: "https://good.example/beads", Username: "user-m"}
	uniqueC := Binding{Name: "unique-c", URL: "https://unique-c.example/beads", Username: "user-c"}
	ownD := Binding{Name: "own-d", URL: "https://own-d.example/beads", Username: "user-d"}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	current := sqlmock.NewRows([]string{"name", "remote_url", "username", "password_encrypted", "sovereignty"})
	history := sqlmock.NewRows([]string{"name", "remote_url", "username", "password_encrypted"})
	for _, r := range []struct {
		b    Binding
		blob []byte
	}{{dupA, shared}, {dupB, shared}, {movedNow, moved}, {uniqueC, unique}, {ownD, own}} {
		current.AddRow(r.b.Name, r.b.URL, r.b.Username, r.blob, "T2")
		history.AddRow(r.b.Name, r.b.URL, r.b.Username, r.blob)
	}
	history.AddRow(movedThen.Name, movedThen.URL, movedThen.Username, moved)
	mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers")).WillReturnRows(current)
	mock.ExpectQuery(regexp.QuoteMeta("FROM dolt_history_federation_peers")).WillReturnRows(history)
	for _, w := range []struct {
		b        Binding
		blob     []byte
		password string
	}{{uniqueC, unique, "unique-password"}, {ownD, own, "own-password"}} {
		mock.ExpectExec(regexp.QuoteMeta("UPDATE federation_peers SET password_encrypted = ?")).
			WithArgs(sealedFor{key: newKey, binding: w.b, password: w.password}, w.b.Name, w.b.URL, w.b.Username, w.blob).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	res, err := Migrate(t.Context(), db, newKey, [][]byte{newKey, derivedKey})
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if strings.Join(res.Migrated, ",") != "unique-c,own-d" {
		t.Errorf("Migrated = %v, want [unique-c own-d]", res.Migrated)
	}
	var skipped []string
	for _, r := range res.Skipped {
		skipped = append(skipped, r.Binding.Name)
	}
	if strings.Join(skipped, ",") != "duplicate-a,duplicate-b,moved" {
		t.Errorf("Skipped = %v, want [duplicate-a duplicate-b moved]", skipped)
	}
	// sqlmock fails any UPDATE it was not told to expect, so the skipped
	// rows were not rewritten.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// Rows no local key opens (another machine's key, corruption) are neither
// rebound nor reported, and need no history read: they keep failing with the
// key-mismatch classification.
func TestMigrateLeavesBlobsNoLocalKeyOpens(t *testing.T) {
	key := testKey(1)
	foreign := legacySeal(t, testKey(50), bytes.Repeat([]byte{5}, 12), "elsewhere")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(regexp.QuoteMeta("FROM federation_peers")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "remote_url", "username", "password_encrypted", "sovereignty"}).
			AddRow("fromelsewhere", "https://else.example/db", "", foreign, "T2"))

	res, err := Migrate(t.Context(), db, key, [][]byte{key})
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(res.Migrated) != 0 || len(res.Skipped) != 0 {
		t.Errorf("result = %+v, want nothing migrated or skipped", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// The same binding seen at many commits (an untouched row's whole history)
// is not ambiguity; two current rows sharing one blob are, and neither is
// rebound whichever is scanned first.
func TestPlanRepeatedBindingIsUnambiguousButCurrentDuplicatesAreNot(t *testing.T) {
	victim := Binding{Name: "victim", URL: "https://victim.example/db", Username: "victim-user"}
	attacker := Binding{Name: "attacker", URL: "https://attacker.example/db", Username: "victim-user"}

	repeated := Candidate{Row: Row{Binding: victim, Blob: []byte("blob-v")}, Plaintext: "victim-secret"}
	eligible, skipped := Plan([]Candidate{repeated}, []Occurrence{
		{Binding: victim, Blob: []byte("blob-v")},
		{Binding: victim, Blob: []byte("blob-v")},
		{Binding: Binding{Name: "other"}, Blob: nil},
	})
	if len(eligible) != 1 || len(skipped) != 0 {
		t.Errorf("repeated binding: eligible = %+v, skipped = %+v; want it eligible", eligible, skipped)
	}

	for _, order := range [][]Binding{{victim, attacker}, {attacker, victim}} {
		var cands []Candidate
		var occ []Occurrence
		for _, b := range order {
			cands = append(cands, Candidate{Row: Row{Binding: b, Blob: []byte("blob-v")}, Plaintext: "victim-secret"})
			occ = append(occ, Occurrence{Binding: b, Blob: []byte("blob-v")})
		}
		eligible, skipped := Plan(cands, occ)
		if len(eligible) != 0 || len(skipped) != 2 {
			t.Errorf("current duplicates %v: eligible = %+v, skipped = %d; want both skipped", order[0].Name, eligible, len(skipped))
		}
	}
}
