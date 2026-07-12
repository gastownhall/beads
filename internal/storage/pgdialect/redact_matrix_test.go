package pgdialect

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// placeholderPW is an obvious non-secret stand-in used wherever a password appears in
// these tests, so nothing here can be mistaken for a leaked real credential.
const placeholderPW = "s3cr3t-PLACEHOLDER"

// tlsExpect encodes what the redacted DSN's TLS state must be: pgx folds sslmode into
// TLSConfig (sslmode=disable -> nil, sslmode=require -> non-nil), so this is how we
// prove sslmode survived redaction.
type tlsExpect int

const (
	tlsSkip    tlsExpect = iota // don't assert (case sets no sslmode)
	tlsMustNil                  // sslmode=disable -> TLSConfig must be nil
	tlsMustSet                  // sslmode=require -> TLSConfig must be non-nil
)

// TestRedactPasswordMatrix locks the security invariant across every secret-bearing DSN
// shape pgx accepts. For each case it asserts BOTH:
//
//	(a) the persisted string, re-parsed by pgx — the SAME parser that opens the
//	    connection — carries NO password (prove the secret is gone, not just absent
//	    from the raw string); and
//	(b) every non-secret component (host, port, database, user, sslmode, other query
//	    params) survives redaction.
//
// A no-password DSN must additionally round-trip byte-for-byte unchanged.
func TestRedactPasswordMatrix(t *testing.T) {
	cases := []struct {
		name          string
		dsn           string
		wantHost      string
		wantPort      uint16
		wantDB        string
		wantUser      string
		wantTLS       tlsExpect
		wantParams    map[string]string // RuntimeParams that must be preserved
		roundTripSame bool              // no-password input must come back unchanged
		// skipUser: when the input has no user, pgx defaults cfg.User to the OS user, so
		// the parsed user is environment-dependent and not something redaction controls.
		skipUser bool
	}{
		{
			name:     "url userinfo password",
			dsn:      "postgres://bts:" + placeholderPW + "@127.0.0.1:5432/db",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", wantUser: "bts",
		},
		{
			// The special characters are percent-encoded in userinfo per RFC 3986:
			// '@'=%40, ':'=%3A, '/'=%2F. The redactor must still strip it cleanly.
			name:     "url userinfo password with special chars (url-encoded)",
			dsn:      "postgres://bts:p%40ss%3Aw%2Frd-" + placeholderPW + "@127.0.0.1:5432/db",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", wantUser: "bts",
		},
		{
			name:     "url query param password + sslmode preserved",
			dsn:      "postgres://bts@127.0.0.1:5432/db?password=" + placeholderPW + "&sslmode=disable",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", wantUser: "bts",
			wantTLS: tlsMustNil,
		},
		{
			name:     "url sslpassword query param + sslmode preserved",
			dsn:      "postgres://bts@127.0.0.1:5432/db?sslpassword=" + placeholderPW + "&sslmode=require",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", wantUser: "bts",
			wantTLS: tlsMustSet,
		},
		{
			name:     "url userinfo password with sslmode and application_name preserved",
			dsn:      "postgres://bts:" + placeholderPW + "@127.0.0.1:5432/db?sslmode=require&application_name=beads",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", wantUser: "bts",
			wantTLS: tlsMustSet, wantParams: map[string]string{"application_name": "beads"},
		},
		{
			name:     "url password but no user",
			dsn:      "postgres://:" + placeholderPW + "@127.0.0.1:5432/db",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", skipUser: true,
		},
		{
			name:     "keyword/value password",
			dsn:      "host=127.0.0.1 port=5432 user=bts password=" + placeholderPW + " dbname=db",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", wantUser: "bts",
		},
		{
			name:     "keyword/value single-quoted password containing spaces",
			dsn:      "host=127.0.0.1 port=5432 user=bts password='se cret " + placeholderPW + "' dbname=db",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", wantUser: "bts",
		},
		{
			name:     "keyword/value sslpassword + sslmode preserved",
			dsn:      "host=127.0.0.1 port=5432 user=bts sslpassword=" + placeholderPW + " sslmode=require dbname=db",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", wantUser: "bts",
			wantTLS: tlsMustSet,
		},
		{
			name:     "keyword/value password but no user",
			dsn:      "host=127.0.0.1 port=5432 password=" + placeholderPW + " dbname=db",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", skipUser: true,
		},
		{
			// Params are in sorted key order because stripPasswordBestEffort re-encodes
			// the query via url.Values.Encode(), which sorts keys; the byte-for-byte
			// round-trip invariant is asserted against a DSN already in that canonical
			// order (application_name < sslmode).
			name:     "no password (url) round-trips unchanged",
			dsn:      "postgres://bts@127.0.0.1:5432/db?application_name=beads&sslmode=require",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", wantUser: "bts",
			wantTLS: tlsMustSet, wantParams: map[string]string{"application_name": "beads"},
			roundTripSame: true,
		},
		{
			name:     "no password (keyword/value) round-trips unchanged",
			dsn:      "host=127.0.0.1 port=5432 user=bts dbname=db sslmode=require",
			wantHost: "127.0.0.1", wantPort: 5432, wantDB: "db", wantUser: "bts",
			wantTLS: tlsMustSet, roundTripSame: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RedactPassword(tc.dsn)
			if err != nil {
				t.Fatalf("RedactPassword(%q) errored: %v", tc.dsn, err)
			}

			// (a) The authoritative security check: the same parser pgx uses to open the
			// connection must find no password in the persisted string.
			cfg, perr := pgx.ParseConfig(got)
			if perr != nil {
				t.Fatalf("redacted DSN no longer parses: %q: %v", got, perr)
			}
			if cfg.Password != "" {
				t.Fatalf("PASSWORD LEAK: RedactPassword(%q) = %q still carries password %q", tc.dsn, got, cfg.Password)
			}
			// Belt and suspenders: the embedded-only detector must also agree.
			if HasPassword(got) {
				t.Fatalf("PASSWORD LEAK: HasPassword still true for redacted %q", got)
			}

			// (b) Non-secret components must survive.
			if cfg.Host != tc.wantHost {
				t.Errorf("host = %q, want %q", cfg.Host, tc.wantHost)
			}
			if cfg.Port != tc.wantPort {
				t.Errorf("port = %d, want %d", cfg.Port, tc.wantPort)
			}
			if cfg.Database != tc.wantDB {
				t.Errorf("database = %q, want %q", cfg.Database, tc.wantDB)
			}
			if !tc.skipUser && cfg.User != tc.wantUser {
				t.Errorf("user = %q, want %q", cfg.User, tc.wantUser)
			}
			switch tc.wantTLS {
			case tlsMustNil:
				if cfg.TLSConfig != nil {
					t.Errorf("sslmode not preserved: TLSConfig = %v, want nil (sslmode=disable)", cfg.TLSConfig)
				}
			case tlsMustSet:
				if cfg.TLSConfig == nil {
					t.Errorf("sslmode not preserved: TLSConfig = nil, want non-nil (sslmode=require)")
				}
			}
			for k, want := range tc.wantParams {
				if got := cfg.RuntimeParams[k]; got != want {
					t.Errorf("param %q = %q, want %q (must be preserved)", k, got, want)
				}
			}

			if tc.roundTripSame && got != tc.dsn {
				t.Errorf("no-password DSN must round-trip unchanged:\n in  = %q\n out = %q", tc.dsn, got)
			}
		})
	}
}
