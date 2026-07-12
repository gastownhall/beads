package mysql

import (
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
)

// placeholderPW is an obvious non-secret stand-in used wherever a password appears in
// these tests, so nothing here can be mistaken for a leaked real credential.
const placeholderPW = "s3cr3t-PLACEHOLDER"

// TestRedactPassword locks the security invariant for mysql.RedactPassword across every
// secret-bearing DSN shape the go-sql-driver grammar accepts. For each case it asserts
// BOTH:
//
//	(a) the persisted string, re-parsed by go-sql-driver — the SAME parser that opens
//	    the connection — carries NO password (prove the secret is gone); and
//	(b) every non-secret component (user, host:port, network, database, params, and the
//	    parseTime flag) survives redaction.
//
// A no-password DSN must additionally round-trip byte-for-byte unchanged.
func TestRedactPassword(t *testing.T) {
	cases := []struct {
		name          string
		dsn           string
		wantUser      string
		wantAddr      string
		wantNet       string
		wantDB        string
		wantParseTime bool
		wantParams    map[string]string
		roundTripSame bool
	}{
		{
			name:     "password present",
			dsn:      "bts:" + placeholderPW + "@tcp(127.0.0.1:3306)/beadsdb",
			wantUser: "bts", wantAddr: "127.0.0.1:3306", wantNet: "tcp", wantDB: "beadsdb",
		},
		{
			// go-sql-driver splits the userinfo password on the LAST '@', so a password
			// full of ':' '/' '#' '@' must still be stripped cleanly.
			name:     "password with special chars",
			dsn:      "bts:p@ss:w/rd#-" + placeholderPW + "@tcp(127.0.0.1:3306)/beadsdb",
			wantUser: "bts", wantAddr: "127.0.0.1:3306", wantNet: "tcp", wantDB: "beadsdb",
		},
		{
			name:     "password present, params + parseTime preserved",
			dsn:      "bts:" + placeholderPW + "@tcp(db.corp:3307)/beadsdb?parseTime=true&myapp=beads",
			wantUser: "bts", wantAddr: "db.corp:3307", wantNet: "tcp", wantDB: "beadsdb",
			wantParseTime: true, wantParams: map[string]string{"myapp": "beads"},
		},
		{
			// The go-sql-driver grammar cannot carry a password without a username;
			// FormatDSN drops the whole user:pass@ block, so redaction still removes the
			// secret. (The loud refusal for this shape lives in withDatabase/WithPassword,
			// not RedactPassword — here the invariant is only that the password is gone.)
			name:     "password but no username",
			dsn:      ":" + placeholderPW + "@tcp(127.0.0.1:3306)/beadsdb",
			wantUser: "", wantAddr: "127.0.0.1:3306", wantNet: "tcp", wantDB: "beadsdb",
		},
		{
			// A canonical, param-free DSN must survive redaction byte-for-byte
			// (go-sql-driver reorders query params on reformat, so the round-trip
			// invariant is asserted only against a DSN already in canonical form).
			name:     "no password round-trips unchanged",
			dsn:      "bts@tcp(127.0.0.1:3306)/beadsdb",
			wantUser: "bts", wantAddr: "127.0.0.1:3306", wantNet: "tcp", wantDB: "beadsdb",
			roundTripSame: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RedactPassword(tc.dsn)
			if err != nil {
				t.Fatalf("RedactPassword(%q) errored: %v", tc.dsn, err)
			}

			// (a) The authoritative security check: the same parser go-sql-driver uses to
			// open the connection must find no password in the persisted string.
			if pw := password(t, got); pw != "" {
				t.Fatalf("PASSWORD LEAK: RedactPassword(%q) = %q still carries password %q", tc.dsn, got, pw)
			}

			// (b) Non-secret components must survive.
			cfg, err := gomysql.ParseDSN(got)
			if err != nil {
				t.Fatalf("redacted DSN no longer parses: %q: %v", got, err)
			}
			if cfg.User != tc.wantUser {
				t.Errorf("user = %q, want %q", cfg.User, tc.wantUser)
			}
			if cfg.Addr != tc.wantAddr {
				t.Errorf("addr = %q, want %q", cfg.Addr, tc.wantAddr)
			}
			if cfg.Net != tc.wantNet {
				t.Errorf("net = %q, want %q", cfg.Net, tc.wantNet)
			}
			if cfg.DBName != tc.wantDB {
				t.Errorf("dbname = %q, want %q", cfg.DBName, tc.wantDB)
			}
			if cfg.ParseTime != tc.wantParseTime {
				t.Errorf("parseTime = %v, want %v (must be preserved)", cfg.ParseTime, tc.wantParseTime)
			}
			for k, want := range tc.wantParams {
				if got := cfg.Params[k]; got != want {
					t.Errorf("param %q = %q, want %q (must be preserved)", k, got, want)
				}
			}

			if tc.roundTripSame && got != tc.dsn {
				t.Errorf("no-password DSN must round-trip unchanged:\n in  = %q\n out = %q", tc.dsn, got)
			}
		})
	}
}
