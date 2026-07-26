package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestScrubArgsForTelemetry locks the invariant that a positional connection
// string cannot leak an embedded password into the bd.args telemetry span. This
// defense is backend-neutral and remains useful even when bd does not implement
// the connection string's database backend.
func TestScrubArgsForTelemetry(t *testing.T) {
	const secret = "s3cr3t-pw"
	cases := []struct {
		name string
		argv []string
		keep []string // non-secret structure that must survive redaction
	}{
		{
			name: "url userinfo",
			argv: []string{"command", "postgres://bts:" + secret + "@127.0.0.1:5432/db"},
			keep: []string{"postgres://bts:", "127.0.0.1:5432/db"},
		},
		{
			name: "url query param",
			argv: []string{"command", "postgres://bts@127.0.0.1:5432/db?password=" + secret},
			keep: []string{"127.0.0.1:5432/db", "password="},
		},
		{
			name: "url sslpassword",
			argv: []string{"command", "postgres://bts@h:5432/db?sslpassword=" + secret + "&sslmode=require"},
			keep: []string{"sslmode=require"},
		},
		{
			name: "keyword value connection string",
			argv: []string{"command", "host=127.0.0.1 user=bts password=" + secret + " dbname=db"},
			keep: []string{"host=127.0.0.1", "user=bts", "dbname=db"},
		},
		{
			name: "keyword value connection string with whitespace around equals",
			argv: []string{"command", "host = 127.0.0.1 password=" + secret},
			keep: []string{"host = 127.0.0.1", "password="},
		},
		{
			name: "password-only keyword connection string",
			argv: []string{"command", "password=" + secret},
			keep: []string{"password="},
		},
		{
			name: "service keyword connection string",
			argv: []string{"command", "service=production password=" + secret},
			keep: []string{"service=production", "password="},
		},
		{
			name: "scheme-less userinfo",
			argv: []string{"command", "bts:" + secret + "@tcp(127.0.0.1:3306)/db"},
			keep: []string{"tcp(127.0.0.1:3306)/db", "bts:"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := scrubArgsForTelemetry(tc.argv, nil)
			if strings.Contains(out, secret) {
				t.Fatalf("PASSWORD LEAK: scrubArgsForTelemetry(%v) = %q still contains %q", tc.argv, out, secret)
			}
			if !strings.Contains(out, "xxxxx") {
				t.Fatalf("expected redaction marker xxxxx in %q", out)
			}
			for _, k := range tc.keep {
				if !strings.Contains(out, k) {
					t.Errorf("expected %q to survive redaction in %q", k, out)
				}
			}
		})
	}
}

// TestScrubArgsForTelemetryLeavesOrdinaryArgs proves the scrubber does not mangle
// non-DSN arguments: an ordinary token that merely contains "password=" is not a
// credential-flag value and passes through untouched (no over-redaction), while a
// bare user:pass@host userinfo anywhere is still redacted as defense in depth.
func TestScrubArgsForTelemetryLeavesOrdinaryArgs(t *testing.T) {
	argv := []string{"create", "--title", "document the password= knob"}
	if out := scrubArgsForTelemetry(argv, nil); out != "create --title document the password= knob" {
		t.Fatalf("over-redacted ordinary args: got %q", out)
	}
	argv = []string{"create", "--title", "transport=rail password=foo"}
	if out := scrubArgsForTelemetry(argv, nil); out != "create --title transport=rail password=foo" {
		t.Fatalf("over-redacted non-DSN key/value text: got %q", out)
	}

	argv = []string{"weird", "postgres://u:leak@h:5432/db"}
	if out := scrubArgsForTelemetry(argv, nil); strings.Contains(out, "leak") {
		t.Fatalf("userinfo password not scrubbed as defense in depth: %q", out)
	}

	for _, arg := range []string{
		"postgres://u@h:5432/db?password=leak",
		"host=h user=u password=leak dbname=db",
	} {
		if out := scrubArgsForTelemetry([]string{"weird", arg}, nil); strings.Contains(out, "leak") {
			t.Fatalf("positional DSN password not scrubbed as defense in depth: %q", out)
		}
	}
}

// TestSecretFlagTokensResolvesByCommand proves -p is treated as secret only on the
// command that binds it to --password. federation add-peer binds -p to the SQL
// password, but init/ready/count bind -p to --prefix/--priority, so the telemetry
// scrubber must resolve no secret token for them. This is the "redact by flag
// identity, not by spelling" guarantee that keeps the overloaded -p from being
// redacted on the far-more-common non-secret commands.
func TestSecretFlagTokensResolvesByCommand(t *testing.T) {
	var pw, prefix string
	addPeer := &cobra.Command{Use: "add-peer"}
	addPeer.Flags().StringVarP(&pw, "password", "p", "", "SQL password")

	if got := secretFlagTokens(addPeer); !got["--password"] || !got["-p"] {
		t.Fatalf("add-peer secret tokens = %v, want both --password and -p", got)
	}

	initLike := &cobra.Command{Use: "init"}
	initLike.Flags().StringVarP(&prefix, "prefix", "p", "", "issue prefix")
	if got := secretFlagTokens(initLike); len(got) != 0 {
		t.Fatalf("init secret tokens = %v, want none (-p is --prefix here)", got)
	}

	if got := secretFlagTokens(nil); len(got) != 0 {
		t.Fatalf("nil command secret tokens = %v, want none", got)
	}
}

// TestScrubArgsForTelemetryRedactsFederationPassword locks that a SQL password
// passed to federation add-peer never reaches bd.args, across pflag's space,
// equals, and concatenated-shorthand spellings for both --password and -p. This is
// the same sink and harm model as the DSN leak, for a flag whose value is an opaque
// credential rather than a parseable DSN.
func TestScrubArgsForTelemetryRedactsFederationPassword(t *testing.T) {
	const secret = "s3cr3t-pw"
	secretFlags := map[string]bool{"--password": true, "-p": true}
	cases := []struct {
		name string
		argv []string
		keep []string // non-secret structure that must survive redaction
	}{
		{
			name: "--password space form",
			argv: []string{"federation", "add-peer", "partner", "h:3306/db", "--user", "admin", "--password", secret},
			keep: []string{"--user admin", "--password xxxxx"},
		},
		{
			name: "--password equals form",
			argv: []string{"federation", "add-peer", "partner", "h:3306/db", "--password=" + secret},
			keep: []string{"--password=xxxxx"},
		},
		{
			name: "-p space form",
			argv: []string{"federation", "add-peer", "partner", "h:3306/db", "-p", secret},
			keep: []string{"h:3306/db", "-p xxxxx"},
		},
		{
			name: "-p equals form",
			argv: []string{"federation", "add-peer", "partner", "h:3306/db", "-p=" + secret},
			keep: []string{"-p=xxxxx"},
		},
		{
			name: "-p concatenated shorthand",
			argv: []string{"federation", "add-peer", "partner", "h:3306/db", "-p" + secret},
			keep: []string{"-pxxxxx"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := scrubArgsForTelemetry(tc.argv, secretFlags)
			if strings.Contains(out, secret) {
				t.Fatalf("PASSWORD LEAK: scrubArgsForTelemetry(%v) = %q still contains %q", tc.argv, out, secret)
			}
			if !strings.Contains(out, "xxxxx") {
				t.Fatalf("expected redaction marker xxxxx in %q", out)
			}
			for _, k := range tc.keep {
				if !strings.Contains(out, k) {
					t.Errorf("expected %q to survive redaction in %q", k, out)
				}
			}
		})
	}
}

// TestScrubArgsForTelemetryRedactsShorthandCluster proves a pflag boolean-shorthand
// cluster ending in the secret shorthand (-qpSECRET, -vpSECRET) is redacted, not just
// the bare -pSECRET spelling. pflag parses a leading run of registered boolean
// shorthands followed by a value-taking shorthand as that cluster: -q (--quiet, bool)
// and -v (--verbose, bool) are both root persistent flags, so `-qp<secret>` on
// federation add-peer parses as -q, then -p <secret> — the same secret value
// secretShorthandPrefix must catch when it only recognized a[:2] == "-p".
func TestScrubArgsForTelemetryRedactsShorthandCluster(t *testing.T) {
	const secret = "s3cr3t-pw"
	secretFlags := map[string]bool{"--password": true, "-p": true}
	cases := []struct {
		name string
		argv []string
	}{
		{
			name: "boolean shorthand -q then -p cluster",
			argv: []string{"federation", "add-peer", "partner", "h:3306/db", "-qp" + secret},
		},
		{
			name: "boolean shorthand -v then -p cluster",
			argv: []string{"federation", "add-peer", "partner", "h:3306/db", "-vp" + secret},
		},
		{
			name: "boolean shorthand -q then separate -p value",
			argv: []string{"federation", "add-peer", "partner", "h:3306/db", "-qp", secret},
		},
		{
			name: "boolean shorthand -v then separate -p value",
			argv: []string{"federation", "add-peer", "partner", "h:3306/db", "-vp", secret},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := scrubArgsForTelemetry(tc.argv, secretFlags)
			if strings.Contains(out, secret) {
				t.Fatalf("PASSWORD LEAK: scrubArgsForTelemetry(%v) = %q still contains %q", tc.argv, out, secret)
			}
			if !strings.Contains(out, "xxxxx") {
				t.Fatalf("expected redaction marker xxxxx in %q", out)
			}
		})
	}
}

// TestScrubArgsForTelemetryKeepsOverloadedShortFlag proves the -p value on
// non-secret commands is left intact: when no secret token is resolved (exactly
// what secretFlagTokens returns for --priority/--prefix commands), -p and its value
// pass through untouched. Without this guarantee the fix would silently redact
// priority/prefix on the most common bd invocations.
func TestScrubArgsForTelemetryKeepsOverloadedShortFlag(t *testing.T) {
	if out := scrubArgsForTelemetry([]string{"ready", "-p", "1"}, nil); out != "ready -p 1" {
		t.Fatalf("over-redacted non-secret -p priority: got %q", out)
	}
	if out := scrubArgsForTelemetry([]string{"init", "-p", "myprefix"}, map[string]bool{}); out != "init -p myprefix" {
		t.Fatalf("over-redacted non-secret -p prefix: got %q", out)
	}
	if out := scrubArgsForTelemetry([]string{"init", "-p=myprefix"}, nil); out != "init -p=myprefix" {
		t.Fatalf("over-redacted non-secret -p=prefix: got %q", out)
	}
}

// TestTelemetryRedactionURLInFlagEqualsValue covers URL values embedded in a
// --flag=value argv element. The flag prefix must not prevent URL query parsing.
func TestTelemetryRedactionURLInFlagEqualsValue(t *testing.T) {
	const secret = "query-secret"
	arg := "--remote=postgres://bts@db.example/beads?password=" + secret
	out := scrubArgsForTelemetry([]string{"init", arg}, nil)
	if strings.Contains(out, secret) {
		t.Fatalf("PASSWORD LEAK: URL query password in --flag=value survived telemetry scrub: %q", out)
	}
}

// TestTelemetryRedactionMalformedURLQuery fails closed when a URL-shaped DSN has
// an invalid percent escape. net/url rejects the whole URL in that case, but the
// query password must still never reach the bd.args telemetry span.
func TestTelemetryRedactionMalformedURLQuery(t *testing.T) {
	const secret = "query-secret"
	arg := "postgres://bts@db.example/beads%zz?password=" + secret + "&sslmode=require"
	out := scrubArgsForTelemetry([]string{"command", arg}, nil)
	if strings.Contains(out, secret) {
		t.Fatalf("PASSWORD LEAK: malformed URL query password survived telemetry scrub: %q", out)
	}
	if !strings.Contains(out, "password=xxxxx") || !strings.Contains(out, "sslmode=require") {
		t.Fatalf("malformed URL query structure was not preserved after redaction: %q", out)
	}
}

// TestScrubArgsForTelemetryAdversarialDSNVectors ports the adversarial regression
// vectors from the deleted internal/storage/pgdialect redaction tests
// (redact_error_test.go, redact_matrix_test.go). Each vector was a real leak or a
// scrubber-bypass near miss in an earlier redactor, so they are pinned here against
// the argv scrubber — the telemetry sink is backend-neutral and keeps receiving
// these DSN shapes even though the backends that motivated them are gone.
//
// Historical failure modes locked in, per case:
//   - percent-encoded userinfo/query passwords must vanish in BOTH the raw
//     (SUPER%2ASECRET) and decoded (SUPER*SECRET) forms;
//   - percent-encoded query KEYS (pass%77ord, sslpass%77ord, %70assword) decode to
//     password keys and bypassed key matching done on the raw key only;
//   - password=SUPER\ SECRET: pgx accepts the backslash-escaped space as part of a
//     single value, but a \S+-shaped value regex stopped at the space and shipped
//     with the tail " SECRET" left in cleartext;
//   - a single-quoted libpq password containing spaces is one value, not several;
//   - pgx treats \v as inter-token whitespace, but Go's regexp \s class does not
//     include \v, so a (^|\s)-anchored password regex never matched after \v and the
//     password leaked entirely;
//   - when one collected secret is a prefix of another (userinfo "foo", query
//     "fooACTUAL"), replacing the short secret first turned "fooACTUAL" into
//     "xxxxxACTUAL" and the tail survived; replacement must be length-descending.
func TestScrubArgsForTelemetryAdversarialDSNVectors(t *testing.T) {
	const secret = "SUPERSECRET"
	cases := []struct {
		name  string
		dsn   string
		leaks []string // every cleartext form that must NOT survive redaction
		keep  []string // non-secret structure that must survive redaction
	}{
		{
			name:  "url userinfo percent-encoded password",
			dsn:   "postgres://u:SUPER%2ASECRET@h:5432/db",
			leaks: []string{"SUPER%2ASECRET", "SUPER*SECRET"},
			keep:  []string{"postgres://u:", "h:5432/db"},
		},
		{
			name:  "url userinfo percent-encoded special chars",
			dsn:   "postgres://bts:p%40ss%3Aw%2Frd-" + secret + "@127.0.0.1:5432/db",
			leaks: []string{"p%40ss%3Aw%2Frd-" + secret, "p@ss:w/rd-" + secret},
			keep:  []string{"postgres://bts:", "127.0.0.1:5432/db"},
		},
		{
			name:  "url query param percent-encoded value",
			dsn:   "postgres://u@h:5432/db?password=SUPER%2ASECRET",
			leaks: []string{"SUPER%2ASECRET", "SUPER*SECRET"},
			keep:  []string{"password=", "h:5432/db"},
		},
		{
			name:  "url query param percent-encoded key (pass%77ord)",
			dsn:   "postgres://u@h:5432/db?pass%77ord=" + secret,
			leaks: []string{secret},
			keep:  []string{"pass%77ord=", "h:5432/db"},
		},
		{
			name:  "url query sslpassword percent-encoded key (sslpass%77ord)",
			dsn:   "postgres://u@h:5432/db?sslpass%77ord=" + secret + "&sslmode=require",
			leaks: []string{secret},
			keep:  []string{"sslpass%77ord=", "sslmode=require"},
		},
		{
			name:  "url query param fully percent-encoded key (%70assword)",
			dsn:   "postgres://u@h:5432/db?%70assword=" + secret,
			leaks: []string{secret},
			keep:  []string{"%70assword=", "h:5432/db"},
		},
		{
			name:  "url query password with sslmode preserved",
			dsn:   "postgres://bts@127.0.0.1:5432/db?password=" + secret + "&sslmode=disable",
			leaks: []string{secret},
			keep:  []string{"sslmode=disable", "127.0.0.1:5432/db"},
		},
		{
			name:  "url userinfo password with sslmode and application_name preserved",
			dsn:   "postgres://bts:" + secret + "@127.0.0.1:5432/db?sslmode=require&application_name=beads",
			leaks: []string{secret},
			keep:  []string{"sslmode=require", "application_name=beads", "127.0.0.1:5432/db"},
		},
		{
			name:  "url userinfo password but no user",
			dsn:   "postgres://:" + secret + "@h:5432/db",
			leaks: []string{secret},
			keep:  []string{"h:5432/db"},
		},
		{
			name: "keyword value password with backslash-escaped whitespace",
			dsn:  `host=h user=u password=SUPER\ SECRET dbname=db`,
			// "SECRET" pins the historical shipped bug: the tail after the escaped
			// space must not survive in cleartext.
			leaks: []string{"SECRET", `SUPER\`},
			keep:  []string{"host=h", "user=u", "dbname=db"},
		},
		{
			name:  "keyword value single-quoted password containing spaces",
			dsn:   "host=h user=u password='se cret " + secret + "' dbname=db",
			leaks: []string{"se cret", secret},
			keep:  []string{"host=h", "user=u", "dbname=db"},
		},
		{
			name:  "keyword value single-quoted password",
			dsn:   "host=h user=u password='" + secret + "' dbname=db",
			leaks: []string{secret},
			keep:  []string{"host=h", "user=u", "dbname=db"},
		},
		{
			name:  "keyword value vertical-tab separator",
			dsn:   "host=h user=u\vpassword=" + secret + " dbname=db",
			leaks: []string{secret},
			keep:  []string{"host=h", "dbname=db"},
		},
		{
			name:  "keyword value sslpassword with sslmode preserved",
			dsn:   "host=h user=u sslpassword=" + secret + " sslmode=require dbname=db",
			leaks: []string{secret},
			keep:  []string{"host=h", "sslmode=require", "dbname=db"},
		},
		{
			name:  "keyword value password but no user",
			dsn:   "host=h port=5432 password=" + secret + " dbname=db",
			leaks: []string{secret},
			keep:  []string{"host=h", "port=5432", "dbname=db"},
		},
		{
			name:  "overlapping secrets where one is a prefix of another",
			dsn:   "postgres://u:foo@h:5432/db?password=fooACTUAL",
			leaks: []string{"ACTUAL", ":foo@"},
			keep:  []string{"postgres://u:", "h:5432/db"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := scrubArgsForTelemetry([]string{"command", tc.dsn}, nil)
			for _, leak := range tc.leaks {
				if strings.Contains(out, leak) {
					t.Errorf("PASSWORD LEAK: %q survived telemetry scrub of %q: %q", leak, tc.dsn, out)
				}
			}
			if !strings.Contains(out, "xxxxx") {
				t.Errorf("expected redaction marker xxxxx in %q", out)
			}
			for _, k := range tc.keep {
				if !strings.Contains(out, k) {
					t.Errorf("expected %q to survive redaction in %q", k, out)
				}
			}
		})
	}
}

// TestScrubArgsForTelemetryPasswordlessDSNUnchanged ports the deleted matrix
// round-trip invariant: a DSN carrying no password must pass through the scrubber
// byte-for-byte unchanged, in both URL and libpq keyword/value form. Over-redaction
// here would destroy the non-secret operational signal the span exists to carry.
func TestScrubArgsForTelemetryPasswordlessDSNUnchanged(t *testing.T) {
	for _, dsn := range []string{
		"postgres://bts@127.0.0.1:5432/db?application_name=beads&sslmode=require",
		"host=127.0.0.1 port=5432 user=bts dbname=db sslmode=require",
	} {
		want := "command " + dsn
		if out := scrubArgsForTelemetry([]string{"command", dsn}, nil); out != want {
			t.Errorf("passwordless DSN altered by scrubber:\n in  = %q\n out = %q", want, out)
		}
	}
}
