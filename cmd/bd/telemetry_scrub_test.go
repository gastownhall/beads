package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestScrubArgsForTelemetry locks the invariant that no password embedded in a
// --pg-url/--mysql-url value survives into the bd.args telemetry span, across every
// DSN password form pgx/go-sql-driver accept and both the --flag=value and
// --flag value spellings. The pre-fix scrubber handled only URL userinfo, so
// query-param and libpq keyword/value passwords (the confirmed red-team vectors)
// leaked verbatim.
func TestScrubArgsForTelemetry(t *testing.T) {
	const secret = "s3cr3t-pw"
	cases := []struct {
		name string
		argv []string
		keep []string // non-secret structure that must survive redaction
	}{
		{
			name: "pg url userinfo =form",
			argv: []string{"init", "--backend=postgres", "--pg-url=postgres://bts:" + secret + "@127.0.0.1:5432/db"},
			keep: []string{"--pg-url=postgres://bts:", "127.0.0.1:5432/db", "--backend=postgres"},
		},
		{
			name: "pg url query param space form",
			argv: []string{"init", "--pg-url", "postgres://bts@127.0.0.1:5432/db?password=" + secret},
			keep: []string{"127.0.0.1:5432/db", "password="},
		},
		{
			name: "pg url sslpassword =form",
			argv: []string{"init", "--pg-url=postgres://bts@h:5432/db?sslpassword=" + secret + "&sslmode=require"},
			keep: []string{"sslmode=require"},
		},
		{
			name: "pg libpq keyword/value =form single token",
			argv: []string{"init", "--pg-url=host=127.0.0.1 user=bts password=" + secret + " dbname=db"},
			keep: []string{"host=127.0.0.1", "user=bts", "dbname=db"},
		},
		{
			name: "mysql userinfo space form",
			argv: []string{"init", "--mysql-url", "bts:" + secret + "@tcp(127.0.0.1:3306)/db"},
			keep: []string{"tcp(127.0.0.1:3306)/db", "bts:"},
		},
		{
			name: "mysql userinfo =form",
			argv: []string{"init", "--mysql-url=bts:" + secret + "@tcp(127.0.0.1:3306)/db"},
			keep: []string{"--mysql-url=bts:", "tcp(127.0.0.1:3306)/db"},
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

	argv = []string{"weird", "postgres://u:leak@h:5432/db"}
	if out := scrubArgsForTelemetry(argv, nil); strings.Contains(out, "leak") {
		t.Fatalf("userinfo password not scrubbed as defense in depth: %q", out)
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
