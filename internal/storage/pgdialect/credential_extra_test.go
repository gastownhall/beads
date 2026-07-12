package pgdialect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestMain neutralizes ambient PGPASSWORD/~/.pgpass so redaction and HasPassword tests
// are hermetic (pgx.ParseConfig folds them into a connection). The ambient-immunity
// tests re-set PGPASSWORD locally via t.Setenv.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "beads-pgdialect-test")
	if err != nil {
		panic(err)
	}
	os.Unsetenv("PGPASSWORD")
	os.Setenv("PGPASSFILE", filepath.Join(dir, "no-pgpass"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func mustUser(t *testing.T, out string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(out)
	if err != nil {
		t.Fatalf("pgx.ParseConfig(%q): %v", out, err)
	}
	return cfg.User
}

func TestWithCredentialURLForm(t *testing.T) {
	out, err := WithCredential("postgres://old@h:5432/db", "vu", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if u := mustUser(t, out); u != "vu" {
		t.Fatalf("user = %q, want vu", u)
	}
	if pw := mustPassword(t, out); pw != "pw" {
		t.Fatalf("password = %q, want pw", pw)
	}
}

func TestWithCredentialURLEmptyUsernameKeepsUser(t *testing.T) {
	out, err := WithCredential("postgres://old@h:5432/db", "", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if u := mustUser(t, out); u != "old" {
		t.Fatalf("user = %q, want old (preserved)", u)
	}
}

func TestWithCredentialKeywordForm(t *testing.T) {
	out, err := WithCredential("host=h user=old dbname=db", "vu", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if u := mustUser(t, out); u != "vu" {
		t.Fatalf("user = %q, want vu (old stripped)", u)
	}
	if pw := mustPassword(t, out); pw != "pw" {
		t.Fatalf("password = %q, want pw", pw)
	}
}

// WithCredential's own round-trip verify guards escaping; a user and password full of
// quotes/backslashes must survive the keyword grammar.
func TestWithCredentialKeywordEscaping(t *testing.T) {
	wantU, wantP := `v'u\x`, `p'w\y`
	out, err := WithCredential("host=h dbname=db", wantU, wantP)
	if err != nil {
		t.Fatal(err)
	}
	if u := mustUser(t, out); u != wantU {
		t.Fatalf("user = %q, want %q", u, wantU)
	}
	if pw := mustPassword(t, out); pw != wantP {
		t.Fatalf("password = %q, want %q", pw, wantP)
	}
}

func TestWithCredentialURLSpecialCharsPassword(t *testing.T) {
	tok := "p@ss:w/rd&x=1"
	out, err := WithCredential("postgres://h:5432/db", "dynuser", tok)
	if err != nil {
		t.Fatal(err)
	}
	if u := mustUser(t, out); u != "dynuser" {
		t.Fatalf("user = %q, want dynuser", u)
	}
	if pw := mustPassword(t, out); pw != tok {
		t.Fatalf("password = %q, want %q", pw, tok)
	}
}

func TestHostPort(t *testing.T) {
	cases := []struct {
		dsn  string
		host string
		port int
		ok   bool
	}{
		{"postgres://u@127.0.0.1:6543/db", "127.0.0.1", 6543, true},
		{"postgres://u@db.corp/db", "db.corp", 5432, true}, // pgx defaults the port to 5432
		{"host=db.corp user=u", "db.corp", 5432, true},     // keyword form
		{"host=/var/run/postgresql user=u", "", 0, false},  // unix-socket dir -> not ok
	}
	for _, c := range cases {
		h, p, ok := HostPort(c.dsn)
		if ok != c.ok || (ok && (h != c.host || p != c.port)) {
			t.Errorf("HostPort(%q) = (%q,%d,%v), want (%q,%d,%v)", c.dsn, h, p, ok, c.host, c.port, c.ok)
		}
	}
}

// HasPassword must reflect only an embedded password, not one pgx would resolve from
// ambient PGPASSWORD/~/.pgpass — otherwise a stale PGPASSWORD would short-circuit the
// whole ladder.
func TestHasPasswordIgnoresAmbientPGPASSWORD(t *testing.T) {
	t.Setenv("PGPASSWORD", "from-env")
	if HasPassword("postgres://bts@127.0.0.1:5432/db") {
		t.Fatal("HasPassword must not report a password from ambient PGPASSWORD")
	}
	if !HasPassword("postgres://bts:embedded@127.0.0.1:5432/db") {
		t.Fatal("HasPassword must detect an embedded URL password")
	}
	if !HasPassword("postgres://bts@127.0.0.1:5432/db?password=q") {
		t.Fatal("HasPassword must detect a ?password= query param")
	}
}

// RedactPassword must not be fooled by an ambient PGPASSWORD (which pgx would fold in
// but which is never persisted) into refusing a password-less --pg-url at init.
func TestRedactPasswordIgnoresAmbientPGPASSWORD(t *testing.T) {
	t.Setenv("PGPASSWORD", "from-env")
	got, err := RedactPassword("postgres://bts@127.0.0.1:5432/db")
	if err != nil {
		t.Fatalf("RedactPassword must not be fooled by ambient PGPASSWORD: %v", err)
	}
	if HasPassword(got) {
		t.Fatalf("redacted DSN still carries an embedded password: %q", got)
	}
}
