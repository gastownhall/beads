package mysqldialect

import (
	"testing"

	"github.com/go-sql-driver/mysql"
)

func mustUser(t *testing.T, out string) string {
	t.Helper()
	cfg, err := mysql.ParseDSN(out)
	if err != nil {
		t.Fatalf("mysql.ParseDSN(%q): %v", out, err)
	}
	return cfg.User
}

func TestWithCredentialOverridesUser(t *testing.T) {
	out, err := WithCredential("old@tcp(h:3306)/db", "vu", "pw")
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

func TestWithCredentialEmptyUsernameKeepsUser(t *testing.T) {
	out, err := WithCredential("old@tcp(h:3306)/db", "", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if u := mustUser(t, out); u != "old" {
		t.Fatalf("user = %q, want old (preserved)", u)
	}
}

// A provided username satisfies the grammar's user requirement, making a userless base
// DSN plus a dynamic user/password pair valid.
func TestWithCredentialUsernameSatisfiesUserlessDSN(t *testing.T) {
	out, err := WithCredential("tcp(h:3306)/db", "vu", "pw")
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

// Password-only against a userless DSN still fails (the guard is intact).
func TestWithCredentialUserlessStillFails(t *testing.T) {
	if _, err := WithCredential("tcp(h:3306)/db", "", "pw"); err == nil {
		t.Fatal("expected error: a userless DSN cannot carry a password without a username")
	}
}

func TestHostPort(t *testing.T) {
	cases := []struct {
		dsn  string
		host string
		port int
		ok   bool
	}{
		{"u@tcp(10.0.0.5:55441)/", "10.0.0.5", 55441, true},
		{"u@/", "127.0.0.1", 3306, true},           // tcp Addr defaults to 127.0.0.1:3306
		{"u@tcp(dbhost)/", "dbhost", 3306, true},   // ensureHavePort appends :3306
		{"u@unix(/tmp/mysql.sock)/", "", 0, false}, // non-tcp -> not ok
	}
	for _, c := range cases {
		h, p, ok := HostPort(c.dsn)
		if ok != c.ok || (ok && (h != c.host || p != c.port)) {
			t.Errorf("HostPort(%q) = (%q,%d,%v), want (%q,%d,%v)", c.dsn, h, p, ok, c.host, c.port, c.ok)
		}
	}
}
