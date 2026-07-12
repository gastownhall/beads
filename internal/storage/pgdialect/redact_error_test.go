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
		{"url userinfo encoded", "postgres://u:SUPER%2ASECRET@h:5432/db"},
		{"libpq keyword", "host=h user=u password=" + secret + " dbname=db"},
		{"libpq keyword quoted", "host=h user=u password='" + secret + "' dbname=db"},
		{"libpq sslpassword", "host=h user=u sslpassword=" + secret + " dbname=db"},
		{"url query param percent-encoded key (pass%77ord)", "postgres://u@h:5432/db?pass%77ord=" + secret},
		{"url query sslpassword percent-encoded key (sslpass%77ord)", "postgres://u@h:5432/db?sslpass%77ord=" + secret + "&sslmode=require"},
		{"url query param fully percent-encoded key (%70assword)", "postgres://u@h:5432/db?%70assword=" + secret},
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

// TestScrubDSNString proves the string-level primitive removes every password form
// when a DSN is echoed as plain text (a telemetry span, a log line) rather than an
// error — the same shapes TestScrubDSNError covers, but exercised directly since the
// telemetry scrubber depends on this function, not on ScrubDSNError.
func TestScrubDSNString(t *testing.T) {
	const secret = "SUPERSECRET"
	leakForms := []string{secret, "SUPER%2ASECRET", "SUPER*SECRET"}
	cases := []struct {
		name string
		dsn  string
	}{
		{"url userinfo", "postgres://u:" + secret + "@h:5432/db"},
		{"url userinfo encoded", "postgres://u:SUPER%2ASECRET@h:5432/db"},
		{"url query param", "postgres://u@h:5432/db?password=" + secret},
		{"url sslpassword", "postgres://u@h:5432/db?sslpassword=" + secret + "&sslmode=require"},
		{"libpq keyword", "host=h user=u password=" + secret + " dbname=db"},
		{"libpq sslpassword", "host=h user=u sslpassword=" + secret + " dbname=db"},
		{"url query param percent-encoded key (pass%77ord)", "postgres://u@h:5432/db?pass%77ord=" + secret},
		{"url query sslpassword percent-encoded key (sslpass%77ord)", "postgres://u@h:5432/db?sslpass%77ord=" + secret + "&sslmode=require"},
		{"url query param fully percent-encoded key (%70assword)", "postgres://u@h:5432/db?%70assword=" + secret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScrubDSNString(tc.dsn, tc.dsn)
			for _, form := range leakForms {
				if strings.Contains(got, form) {
					t.Errorf("ScrubDSNString(%q) still leaks password (%q): %q", tc.dsn, form, got)
				}
			}
			if !strings.Contains(got, "xxxxx") {
				t.Errorf("ScrubDSNString(%q) did not redact: %q", tc.dsn, got)
			}
		})
	}
	// A DSN with no password, and an empty DSN, must pass the target string through.
	if got := ScrubDSNString("postgres://u@h:5432/db", "postgres://u@h:5432/db"); got != "postgres://u@h:5432/db" {
		t.Errorf("passwordless DSN altered: %q", got)
	}
	if got := ScrubDSNString("", "no dsn here"); got != "no dsn here" {
		t.Errorf("empty dsn altered s: %q", got)
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

// TestScrubDSNStringEscapedWhitespaceValue proves an unquoted libpq keyword/value
// password containing a backslash-escaped space is redacted in full. pgx accepts
// password=SUPER\ SECRET as the single password `SUPER\ SECRET` (the backslash is
// retained literally, the escaped space is part of the value — confirmed against
// pgx.ParseConfig); the pre-fix value regex (a \S+-shaped run) stopped at the
// unescaped whitespace boundary and left the tail " SECRET" behind in plaintext.
func TestScrubDSNStringEscapedWhitespaceValue(t *testing.T) {
	dsn := `host=h user=u password=SUPER\ SECRET dbname=db`
	got := ScrubDSNString(dsn, dsn)
	if strings.Contains(got, "SECRET") {
		t.Errorf("ScrubDSNString(%q) leaked escaped-whitespace password tail: %q", dsn, got)
	}
	if strings.Contains(got, `SUPER\`) {
		t.Errorf("ScrubDSNString(%q) leaked escaped-whitespace password head: %q", dsn, got)
	}
	if !strings.Contains(got, "xxxxx") {
		t.Errorf("ScrubDSNString(%q) did not redact: %q", dsn, got)
	}
	if !strings.Contains(got, "host=h") || !strings.Contains(got, "dbname=db") {
		t.Errorf("ScrubDSNString(%q) mangled non-secret structure: %q", dsn, got)
	}
}

// TestScrubDSNStringVerticalTabSeparator proves a password separated from the
// preceding keyword/value token by a vertical tab (\v) is still found and redacted.
// pgx treats \v as inter-token whitespace in libpq keyword/value DSNs (confirmed
// against pgx.ParseConfig: "host=h\vpassword=x" parses host="h", password="x"), but
// Go's regexp \s class is [\t\n\f\r ] and does NOT include \v, so the pre-fix
// password regex — anchored on (^|\s) — never matched at a \v boundary and the
// password leaked entirely.
func TestScrubDSNStringVerticalTabSeparator(t *testing.T) {
	const secret = "SUPERSECRET"
	dsn := "host=h user=u" + "\v" + "password=" + secret + " dbname=db"
	got := ScrubDSNString(dsn, dsn)
	if strings.Contains(got, secret) {
		t.Errorf("ScrubDSNString(%q) leaked password separated by a vertical tab: %q", dsn, got)
	}
	if !strings.Contains(got, "xxxxx") {
		t.Errorf("ScrubDSNString(%q) did not redact vertical-tab-separated password: %q", dsn, got)
	}
}

// TestScrubDSNStringOverlappingSecretsPrefix proves that when one collected secret is
// a byte-for-byte prefix of another (userinfo password "foo", query password
// "fooACTUAL"), redaction does not leak the longer secret's non-overlapping tail.
// ScrubDSNString applies strings.ReplaceAll sequentially per collected secret; doing
// the short secret ("foo") first turns "fooACTUAL" into "xxxxxACTUAL" before the
// long-secret pass ever runs, so the second pass finds no "fooACTUAL" substring left
// to replace and "ACTUAL" survives in plaintext.
func TestScrubDSNStringOverlappingSecretsPrefix(t *testing.T) {
	dsn := "postgres://u:foo@h:5432/db?password=fooACTUAL"
	got := ScrubDSNString(dsn, dsn)
	if strings.Contains(got, "ACTUAL") {
		t.Errorf("ScrubDSNString(%q) leaked the tail of a secret that is a prefix of another secret: %q", dsn, got)
	}
	if strings.Contains(got, ":foo@") {
		t.Errorf("ScrubDSNString(%q) did not redact the shorter overlapping secret: %q", dsn, got)
	}
}
