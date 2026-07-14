package pgdialect

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestHasPassword(t *testing.T) {
	cases := []struct {
		dsn  string
		want bool
	}{
		{"postgres://bts:secret@127.0.0.1:5432/db", true},
		{"postgres://bts@127.0.0.1:5432/db", false},
		{"host=127.0.0.1 port=5432 user=bts password=secret dbname=db", true},
		{"host=127.0.0.1 port=5432 user=bts dbname=db", false},
	}
	for _, c := range cases {
		if got := HasPassword(c.dsn); got != c.want {
			t.Errorf("HasPassword(%q) = %v, want %v", c.dsn, got, c.want)
		}
	}
}

// mustPassword parses out and returns the password pgx sees.
func mustPassword(t *testing.T, out string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(out)
	if err != nil {
		t.Fatalf("pgx.ParseConfig(%q): %v", out, err)
	}
	return cfg.Password
}

func TestWithPasswordURLForm(t *testing.T) {
	out, err := WithPassword("postgres://bts@127.0.0.1:5432/db", "s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	if pw := mustPassword(t, out); pw != "s3cr3t" {
		t.Fatalf("password = %q, want s3cr3t", pw)
	}
	// The username must be preserved.
	cfg, _ := pgx.ParseConfig(out)
	if cfg.User != "bts" {
		t.Fatalf("user = %q, want bts", cfg.User)
	}
}

// A token with URL-reserved characters (as an RDS-IAM / GCP-IAM auth token carries)
// must round-trip through the URL grammar intact.
func TestWithPasswordURLSpecialChars(t *testing.T) {
	tok := "X-Amz=abc&Signature=d/e+f%20g"
	out, err := WithPassword("postgres://bts@host:5432/db", tok)
	if err != nil {
		t.Fatal(err)
	}
	if pw := mustPassword(t, out); pw != tok {
		t.Fatalf("password = %q, want %q", pw, tok)
	}
}

// The keyword/value grammar is the case the old net/url merge silently dropped.
func TestWithPasswordKeywordForm(t *testing.T) {
	out, err := WithPassword("host=127.0.0.1 port=5432 user=bts dbname=db", "s3 cr3t") // note the space
	if err != nil {
		t.Fatal(err)
	}
	if pw := mustPassword(t, out); pw != "s3 cr3t" {
		t.Fatalf("password = %q, want %q", pw, "s3 cr3t")
	}
}

// An existing password in a keyword DSN is replaced, not duplicated.
func TestWithPasswordKeywordReplacesExisting(t *testing.T) {
	out, err := WithPassword("host=h user=u password=old dbname=db", "new")
	if err != nil {
		t.Fatal(err)
	}
	if pw := mustPassword(t, out); pw != "new" {
		t.Fatalf("password = %q, want new", pw)
	}
}

// A value with a single quote and a backslash survives keyword escaping.
func TestWithPasswordKeywordEscaping(t *testing.T) {
	tok := `a'b\c`
	out, err := WithPassword("host=h user=u dbname=db", tok)
	if err != nil {
		t.Fatal(err)
	}
	if pw := mustPassword(t, out); pw != tok {
		t.Fatalf("password = %q, want %q", pw, tok)
	}
}
