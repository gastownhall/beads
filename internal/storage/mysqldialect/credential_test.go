package mysqldialect

import (
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestHasPassword(t *testing.T) {
	cases := []struct {
		dsn  string
		want bool
	}{
		{"bts:secret@tcp(127.0.0.1:3306)/db", true},
		{"bts@tcp(127.0.0.1:3306)/db", false},
		{"bts@/", false},                      // no-tcp form parses, no password
		{"", false},                           // empty parses to a default config
		{"bts:pw@tcp(127.0.0.1:3306)", false}, // missing trailing slash -> parse error
	}
	for _, c := range cases {
		if got := HasPassword(c.dsn); got != c.want {
			t.Errorf("HasPassword(%q) = %v, want %v", c.dsn, got, c.want)
		}
	}
}

// mustPassword parses out and returns the password go-sql-driver sees.
func mustPassword(t *testing.T, out string) string {
	t.Helper()
	cfg, err := mysql.ParseDSN(out)
	if err != nil {
		t.Fatalf("mysql.ParseDSN(%q): %v", out, err)
	}
	return cfg.Passwd
}

func TestWithPasswordBasic(t *testing.T) {
	out, err := WithPassword("bts@tcp(127.0.0.1:55441)/", "s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	if pw := mustPassword(t, out); pw != "s3cr3t" {
		t.Fatalf("password = %q, want s3cr3t", pw)
	}
	cfg, _ := mysql.ParseDSN(out)
	if cfg.User != "bts" {
		t.Fatalf("user = %q, want bts (must be preserved)", cfg.User)
	}
}

// go-sql-driver writes the password verbatim; a token full of DSN-significant
// characters (as an RDS-IAM token carries) must round-trip byte-for-byte.
func TestWithPasswordSpecialChars(t *testing.T) {
	for _, tok := range []string{"p@ss:w/rd?x=1&y=2", "X-Amz=abc&Sig=d/e+f%20", `a'b\c d`} {
		out, err := WithPassword("bts@tcp(h:3306)/db", tok)
		if err != nil {
			t.Fatalf("token %q: %v", tok, err)
		}
		if pw := mustPassword(t, out); pw != tok {
			t.Fatalf("password = %q, want %q", pw, tok)
		}
	}
}

func TestWithPasswordReplacesExisting(t *testing.T) {
	out, err := WithPassword("bts:old@tcp(h:3306)/db", "new")
	if err != nil {
		t.Fatal(err)
	}
	if pw := mustPassword(t, out); pw != "new" {
		t.Fatalf("password = %q, want new", pw)
	}
}

// The MySQL sharp edge: a userless DSN cannot carry a password (FormatDSN drops it),
// so WithPassword must refuse rather than silently connect without one.
func TestWithPasswordUserlessDSNFails(t *testing.T) {
	_, err := WithPassword("tcp(127.0.0.1:55441)/", "orphan")
	if err == nil {
		t.Fatal("expected an error for a userless DSN (password would be silently dropped)")
	}
}
