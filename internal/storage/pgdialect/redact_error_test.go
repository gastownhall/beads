package pgdialect

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// TestScrubDSNError proves an error carrying a DSN can never echo the cleartext
// password back. pgx already redacts URL userinfo and libpq keyword passwords, so
// the URL query-param vectors are the ones that actually leak (see
// TestOpenRedactsPasswordInError); this drives every shape against a worst-case
// error that echoes the raw DSN verbatim.
func TestScrubDSNError(t *testing.T) {
	const secret = "SUPERSECRET"
	leakForms := []string{secret, "SUPER%2ASECRET", "SUPER*SECRET"}
	cases := []struct {
		name string
		dsn  string
	}{
		{"url query param", "postgres://u@h:5432/db?password=" + secret},
		{"url query sslpassword", "postgres://u@h:5432/db?sslpassword=" + secret + "&sslmode=require"},
		{"url query param encoded", "postgres://u@h:5432/db?password=SUPER%2ASECRET"},
		{"url userinfo", "postgres://u:" + secret + "@h:5432/db"},
		{"libpq keyword", "host=h user=u password=" + secret + " dbname=db"},
		{"libpq keyword quoted", "host=h user=u password='" + secret + "' dbname=db"},
		{"libpq sslpassword", "host=h user=u sslpassword=" + secret + " dbname=db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := errors.New("cannot parse `" + tc.dsn + "`: boom")
			got := ScrubDSNError(tc.dsn, raw).Error()
			for _, form := range leakForms {
				if strings.Contains(got, form) {
					t.Errorf("scrubbed error still leaks password (%q): %q", form, got)
				}
			}
			// The non-secret failure reason must survive so the error stays useful.
			if !strings.Contains(got, "boom") {
				t.Errorf("scrubbed error dropped the failure reason: %q", got)
			}
		})
	}
	if ScrubDSNError("postgres://u@h/db?password=x", nil) != nil {
		t.Error("ScrubDSNError(dsn, nil) must return nil")
	}
}

// TestOpenRedactsPasswordInError drives the real pgx parser: a DSN that fails to
// parse while carrying a ?password=/?sslpassword= query param must not leak the
// secret through Open or OpenRaw. connect_timeout=x is what makes ParseConfig fail;
// the query-param password is exactly the vector pgx's own redaction misses.
func TestOpenRedactsPasswordInError(t *testing.T) {
	const secret = "SUPERSECRET"
	dsns := []string{
		"postgres://u@h:5432/db?password=" + secret + "&connect_timeout=x",
		"postgres://u@h:5432/db?sslpassword=" + secret + "&connect_timeout=x",
	}
	opens := []struct {
		name string
		open func(string, string) (*sql.DB, error)
	}{
		{"Open", Open},
		{"OpenRaw", OpenRaw},
	}
	for _, dsn := range dsns {
		for _, o := range opens {
			t.Run(o.name+" "+dsn, func(t *testing.T) {
				db, err := o.open(dsn, "public")
				if err == nil {
					_ = db.Close()
					t.Fatal("expected a parse error, got nil")
				}
				if strings.Contains(err.Error(), secret) {
					t.Errorf("%s leaked password in error: %q", o.name, err.Error())
				}
			})
		}
	}
}
