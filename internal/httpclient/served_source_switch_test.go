package httpclient

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi"
)

// The served tier's DATABASE SOURCE is a dimension of the tier, the same way
// its protocol is (served_protocol_seam_test.go). Production `bd serve` refuses
// embedded Dolt and serves every server-mode workspace through httpapi's
// Provider arm: a per-request timedProvider over the external-dependency layer
// over a unit-of-work provider over a dolt sql-server. The tier's default
// source binds the Roles arm over an embedded workspace instead, which is the
// cheapest honest composition but not the one production runs.
//
// BEADS_HTTP_SERVED_SOURCE names the source:
//
//   - unset or "roles": the Roles arm over a private copy of the embedded
//     template workspace (servedRolesSource in served_harness_test.go);
//   - "provider": httpapi.Config{Provider: ...} built the way cmd/bd/serve.go
//     builds it for a server-mode workspace, over a local dolt sql-server
//     (served_provider_source_test.go);
//   - anything else fails every served case. A typo must never fall back to
//     roles, or the provider lane would pass while testing the other arm.
//
// Only newServedEnvExpecting follows the switch; the shared composition
// (served_test.go) stays on roles. TestServedTierServesFromTheSourceItNames
// proves on the server's own log that a case was served from the source the
// switch names, and its parser is falsified below.
//
// This file is deliberately UNTAGGED, for the reason
// served_protocol_seam_test.go gives: the gate and the witness parser need no
// engine, so their falsification tables run in the plain test lane.

// servedSourceEnv selects the served tier's database source.
const servedSourceEnv = "BEADS_HTTP_SERVED_SOURCE"

// servedSource is a value of BEADS_HTTP_SERVED_SOURCE. It is also the value
// httpapi logs as db= on its startup line, which is what the witness compares.
type servedSource string

const (
	servedSourceRoles    servedSource = "roles"
	servedSourceProvider servedSource = "provider"
)

// parseServedSource reads BEADS_HTTP_SERVED_SOURCE. Unset means roles; an
// unknown value is an error, never a fallback.
func parseServedSource(v string) (servedSource, error) {
	switch v {
	case "", string(servedSourceRoles):
		return servedSourceRoles, nil
	case string(servedSourceProvider):
		return servedSourceProvider, nil
	}
	return "", fmt.Errorf("%s=%q: want %q or %q (unset means %q)",
		servedSourceEnv, v, servedSourceRoles, servedSourceProvider, servedSourceRoles)
}

// servedGate is what a served case does before it builds anything.
type servedGate int

const (
	servedGateRun servedGate = iota
	servedGateSkip
	servedGateFail
)

func (g servedGate) String() string {
	switch g {
	case servedGateRun:
		return "run"
	case servedGateSkip:
		return "skip"
	case servedGateFail:
		return "fail"
	}
	return "servedGate(" + strconv.Itoa(int(g)) + ")"
}

// servedGateDecision is skipUnlessEmbeddedDolt's decision as a pure function of
// the environment, so its table can run without an engine.
//
// The provider source needs no embedded engine, so it runs without
// BEADS_TEST_EMBEDDED_DOLT. Under BEADS_HTTP_TEST_REQUIRED=1 it also requires
// BEADS_TEST_REQUIRE_DOLT_CONTAINER=1: without it a dolt sql-server that cannot
// start would skip every case while the lane reports success. A case that opens
// embedded Dolt itself (embeddedOnly) is not reached by the switch and keeps
// the embedded gate.
//
// The roles messages are byte-identical to the gate's historical ones: the
// skip census (tools/bazel/skip_census.txt) classifies env-gate skips by them.
func servedGateDecision(getenv func(string) string, embeddedOnly bool) (servedSource, servedGate, string) {
	source, err := parseServedSource(getenv(servedSourceEnv))
	if err != nil {
		return "", servedGateFail, err.Error()
	}
	required := getenv("BEADS_HTTP_TEST_REQUIRED") == "1"
	if source == servedSourceProvider && !embeddedOnly {
		if required && getenv("BEADS_TEST_REQUIRE_DOLT_CONTAINER") != "1" {
			return source, servedGateFail, "BEADS_HTTP_TEST_REQUIRED=1 with BEADS_HTTP_SERVED_SOURCE=provider " +
				"but BEADS_TEST_REQUIRE_DOLT_CONTAINER is not 1: a dolt sql-server that cannot start would skip every case"
		}
		return source, servedGateRun, ""
	}
	if getenv("BEADS_TEST_EMBEDDED_DOLT") == "1" {
		return source, servedGateRun, ""
	}
	if source == servedSourceProvider {
		return source, servedGateSkip, "this case opens embedded Dolt itself; BEADS_HTTP_SERVED_SOURCE=provider does not reach it"
	}
	if required {
		return source, servedGateFail, "BEADS_HTTP_TEST_REQUIRED=1 but BEADS_TEST_EMBEDDED_DOLT is not 1; " +
			"the served-surface conformance tier is not enforced"
	}
	return source, servedGateSkip, "set BEADS_TEST_EMBEDDED_DOLT=1 to run the served-surface conformance tier"
}

// TestServedSourceGateDecisions pins the gate. Its failing and skipping rows are
// the firing checks: mapping an unknown source to roles fails the "Provider"
// row, and dropping the REQUIRE_DOLT_CONTAINER clause fails the
// provider+REQUIRED row.
func TestServedSourceGateDecisions(t *testing.T) {
	const (
		embedded  = "BEADS_TEST_EMBEDDED_DOLT"
		required  = "BEADS_HTTP_TEST_REQUIRED"
		container = "BEADS_TEST_REQUIRE_DOLT_CONTAINER"
	)
	rows := []struct {
		name         string
		env          map[string]string
		embeddedOnly bool
		wantSource   servedSource
		wantGate     servedGate
		wantMsg      string
	}{
		{"developer default", map[string]string{}, false, servedSourceRoles, servedGateSkip, "set BEADS_TEST_EMBEDDED_DOLT=1"},
		{"embedded lane", map[string]string{embedded: "1"}, false, servedSourceRoles, servedGateRun, ""},
		{"required without the engine", map[string]string{required: "1"}, false, servedSourceRoles, servedGateFail, "BEADS_TEST_EMBEDDED_DOLT is not 1"},
		{"roles spelled out", map[string]string{servedSourceEnv: "roles", embedded: "1"}, false, servedSourceRoles, servedGateRun, ""},
		{"a typo never falls back to roles", map[string]string{servedSourceEnv: "Provider", embedded: "1"}, false, "", servedGateFail, `want "roles" or "provider"`},
		{"provider needs no embedded engine", map[string]string{servedSourceEnv: "provider"}, false, servedSourceProvider, servedGateRun, ""},
		{"required provider needs the server required", map[string]string{servedSourceEnv: "provider", required: "1"}, false, servedSourceProvider, servedGateFail, "BEADS_TEST_REQUIRE_DOLT_CONTAINER"},
		{"provider lane", map[string]string{servedSourceEnv: "provider", required: "1", container: "1"}, false, servedSourceProvider, servedGateRun, ""},
		{"provider lane, embedded-only case", map[string]string{servedSourceEnv: "provider", required: "1", container: "1"}, true, servedSourceProvider, servedGateSkip, "opens embedded Dolt itself"},
		{"provider with the engine, embedded-only case", map[string]string{servedSourceEnv: "provider", embedded: "1"}, true, servedSourceProvider, servedGateRun, ""},
		{"a typo fails embedded-only cases too", map[string]string{servedSourceEnv: "bogus", embedded: "1"}, true, "", servedGateFail, `want "roles" or "provider"`},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			source, gate, msg := servedGateDecision(func(k string) string { return row.env[k] }, row.embeddedOnly)
			if source != row.wantSource || gate != row.wantGate {
				t.Fatalf("servedGateDecision(%v, embeddedOnly=%v) = (%q, %v, %q), want (%q, %v, ...)",
					row.env, row.embeddedOnly, source, gate, msg, row.wantSource, row.wantGate)
			}
			if gate == servedGateRun && msg != "" {
				t.Fatalf("a run decision carries a message: %q", msg)
			}
			if !strings.Contains(msg, row.wantMsg) {
				t.Fatalf("message %q does not contain %q", msg, row.wantMsg)
			}
		})
	}
}

var (
	servedStartupDB           = regexp.MustCompile(`(?m)\bevent=startup\b.*\bdb=(\S+)`)
	servedStartupCapabilities = regexp.MustCompile(`(?m)\bevent=startup\b.*\bcapabilities=(\S+)`)
	servedCreateRequestUOW    = regexp.MustCompile(`(?m)\bevent=request\b.*\bop=` +
		regexp.QuoteMeta(httpapi.OpCreateIssue) + `\b.*\buow_ms=([0-9.]+)`)
)

// servedSourceWitness reads an in-process server's stderr log and reports every
// way the server's composition disagrees with the source the switch NAMES:
//
//   - its startup line's db= value (httpapi's dbSource) must be that source;
//   - the provider source must advertise policy.external_dependencies, the
//     capability production's external-dependency layer adds;
//   - its createIssue request line's uow_ms must be positive on the provider
//     source (the per-request timedProvider opened a unit of work) and zero on
//     the roles source (which opens none).
//
// The op is fixed to createIssue: the witness test creates one row through the
// client and judges that request.
func servedSourceWitness(named servedSource, log string) []string {
	var out []string
	if m := servedStartupDB.FindStringSubmatch(log); m == nil {
		out = append(out, "the server logged no event=startup line naming its database source")
	} else if m[1] != string(named) {
		out = append(out, fmt.Sprintf("the server says db=%s, but %s selects the %s source", m[1], servedSourceEnv, named))
	}
	if named == servedSourceProvider {
		c := servedStartupCapabilities.FindStringSubmatch(log)
		if c == nil || !slices.Contains(strings.Split(c[1], ","), "policy.external_dependencies") {
			out = append(out, "the provider source does not advertise policy.external_dependencies; "+
				"production's provider wears the external-dependency layer")
		}
	}
	r := servedCreateRequestUOW.FindStringSubmatch(log)
	if r == nil {
		return append(out, "the server logged no event=request line for op="+httpapi.OpCreateIssue)
	}
	ms, err := strconv.ParseFloat(r[1], 64)
	switch {
	case err != nil:
		out = append(out, fmt.Sprintf("op=%s logged uow_ms=%q, which does not parse: %v", httpapi.OpCreateIssue, r[1], err))
	case named == servedSourceProvider && ms <= 0:
		out = append(out, fmt.Sprintf("op=%s logged uow_ms=%s on the provider source: "+
			"the per-request timedProvider opened no unit of work", httpapi.OpCreateIssue, r[1]))
	case named == servedSourceRoles && ms != 0:
		out = append(out, fmt.Sprintf("op=%s logged uow_ms=%s on the roles source, which opens no unit of work",
			httpapi.OpCreateIssue, r[1]))
	}
	return out
}

// TestTheSourceWitnessFiresOnTheWrongComposition is the witness's falsification
// table, the TestUntokened*RuleFires pattern: every wrong composition must be
// reported, and the two right ones must stay clean.
func TestTheSourceWitnessFiresOnTheWrongComposition(t *testing.T) {
	const caps = "issues.read,policy.external_dependencies"
	provider := "bd serve: 2026/11/09 12:00:00 event=startup addr=127.0.0.1:1 db=provider capabilities=" + caps + "\n" +
		"bd serve: 2026/11/09 12:00:01 event=request request_id=1 op=createIssue status=200 uow_ms=0.412\n"
	roles := "bd serve: 2026/11/09 12:00:00 event=startup addr=127.0.0.1:1 db=roles capabilities=issues.read\n" +
		"bd serve: 2026/11/09 12:00:01 event=request request_id=1 op=createIssue status=200 uow_ms=0.000\n"
	rows := []struct {
		name  string
		named servedSource
		log   string
		want  string // "" is a control: no violation at all
	}{
		{"provider lane served by the roles arm", servedSourceProvider, roles,
			"the server says db=roles, but BEADS_HTTP_SERVED_SOURCE selects the provider source"},
		{"provider arm that opened no unit of work", servedSourceProvider,
			strings.Replace(provider, "uow_ms=0.412", "uow_ms=0.000", 1), "opened no unit of work"},
		{"provider without the external-dependency layer", servedSourceProvider,
			strings.Replace(provider, caps, "issues.read", 1), "does not advertise policy.external_dependencies"},
		{"roles lane served by the provider arm", servedSourceRoles, provider, "db=provider"},
		{"roles source that opened a unit of work", servedSourceRoles,
			strings.Replace(roles, "uow_ms=0.000", "uow_ms=1.500", 1), "on the roles source"},
		{"no startup line", servedSourceProvider, "", "no event=startup line"},
		{"no createIssue request line", servedSourceRoles,
			"bd serve: event=startup db=roles\nbd serve: event=request op=getIssue uow_ms=0.000\n", "no event=request line for op=createIssue"},
		{"control: provider", servedSourceProvider, provider, ""},
		{"control: roles", servedSourceRoles, roles, ""},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got := servedSourceWitness(row.named, row.log)
			if row.want == "" {
				if len(got) != 0 {
					t.Fatalf("the witness fired on a correct composition: %q", got)
				}
				return
			}
			if !slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, row.want) }) {
				t.Fatalf("want a violation containing %q, got %q", row.want, got)
			}
		})
	}
}
