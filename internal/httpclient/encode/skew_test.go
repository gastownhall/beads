// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/skew_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import (
	"fmt"
	"strings"
	"testing"
)

// GATE 5: the skew reverse lookup.
//
// The bijection gate above proves every field reaches a parameter. This one
// proves every parameter reaches back to a FLAG, which is the direction D7's
// third refusal text runs.
//
// The failure it exists to prevent is quiet and specific. A parameter added to
// the wire with no row here still encodes, still sends, and still earns a 400
// from an older server — the only thing that breaks is the refusal, which
// degrades from "--include-comments is not supported by bd serve at ..." into a
// bare invalid-argument. That is a strictly worse message on exactly the day the
// user most needs a good one, and nothing else in this package would notice.

func TestEveryWireParameterMapsBackToAFlagOrSaysWhyNot(t *testing.T) {
	params := SkewParams()
	if len(params) == 0 {
		t.Fatal("the encoder table published no parameters; this gate is vacuous")
	}

	for _, pf := range params {
		if pf.HasFlag {
			if pf.Why != "" {
				t.Errorf("parameter %q has both a flag (--%s) and a no-flag reason; one of the two tables is wrong", pf.Param, pf.Flag)
			}
			if strings.HasPrefix(pf.Flag, "-") {
				t.Errorf("parameter %q maps to %q; the table holds BARE flag names and the text supplies the dashes", pf.Param, pf.Flag)
			}
			continue
		}
		// No flag is a decision, and a decision carries its reason. Without this
		// arm the cheapest way to green this gate would be to leave a new
		// parameter out of both tables.
		if pf.Why == "" {
			t.Errorf("parameter %q is on the encoder table with no flag and no reason for having none;\n"+
				"add it to paramFlag if a flag produces it, or to paramNoFlag with why it cannot", pf.Param)
		}
	}
}

func TestTheSkewTablesCarryNoRowsTheEncoderNeverSends(t *testing.T) {
	// The converse direction. A row for a parameter no table publishes is a rule
	// that matches nothing — it would survive a rename of the parameter it was
	// written for and go on looking like coverage.
	published := map[string]bool{}
	for _, pf := range SkewParams() {
		published[pf.Param] = true
	}

	for param := range paramFlag {
		if !published[param] {
			t.Errorf("paramFlag names %q, which no encoder table publishes", param)
		}
	}
	for param := range paramNoFlag {
		if !published[param] {
			t.Errorf("paramNoFlag names %q, which no encoder table publishes", param)
		}
	}
}

func TestTheReverseLookupAnswersTheParametersASkewedServerNames(t *testing.T) {
	// The mapping itself, spelled out. These are the pairs a 400 from an older
	// server actually produces, and getting one of them wrong is invisible until
	// a user reads the refusal.
	for _, tc := range []struct{ param, flag string }{
		{"include_comments", "include-comments"},
		{"include_dependents", "include-dependents"},
		{"has_metadata_key", "has-metadata-key"},
		{"metadata_field", "metadata-field"},
		{"exclude_type", "exclude-type"},
		{"label_any", "label-any"},
		{"created_before", "created-before"},
		{"include_ephemeral", "include-ephemeral"},
		{"sort", "sort"},
		{"limit", "limit"},
		{"parent", "parent"},
	} {
		got, ok := FlagForParam(tc.param)
		if !ok {
			t.Errorf("FlagForParam(%q) found nothing", tc.param)
			continue
		}
		if got != tc.flag {
			t.Errorf("FlagForParam(%q) = %q, want %q", tc.param, got, tc.flag)
		}
	}
}

func TestParametersWithNoFlagDoNotInventOne(t *testing.T) {
	// `--q` and `--cursor` do not exist. Rendering either would send the user
	// looking for a flag that was never there, which is worse than naming the
	// parameter, because it reads like the client knows something.
	for _, param := range []string{"q", "cursor", "{id}"} {
		if flag, ok := FlagForParam(param); ok {
			t.Errorf("FlagForParam(%q) invented --%s", param, flag)
		}
		entry := paramNoFlag[param]
		if entry.Why == "" {
			t.Errorf("parameter %q has no recorded reason for having no flag", param)
		}
		// The subject is what the user reads, so it must never be a flag
		// spelling and never be empty.
		if entry.Subject == "" {
			t.Errorf("parameter %q has no subject; case 3 could not name what it refused", param)
		}
		if strings.HasPrefix(ParameterSubject(param), "--") {
			t.Errorf("ParameterSubject(%q) = %q, which reads as a flag", param, ParameterSubject(param))
		}
	}
}

func TestTheCaseThreeRefusalNamesTheFlagTheServerTheVersionAndTheParameter(t *testing.T) {
	// D7 case 3, whole. Every one of the four facts is load-bearing: the flag is
	// what the user typed, the URL is which server refused (an agent rig has
	// several), the version is what makes "upgrade the server" actionable, and
	// the parameter is what makes the message diagnosable when the flag mapping
	// is the thing that is wrong.
	got := UnknownParameterRefusal("include_comments", "http://serve.internal:9099", "1.0.4")
	want := `--include-comments is not supported by bd serve at http://serve.internal:9099 (bd_version 1.0.4): parameter "include_comments" is unknown to the server`
	if got != want {
		t.Errorf("refusal =\n%s\nwant\n%s", got, want)
	}
	// The shared constant and the renderer are the same string. cmd/bd's
	// refusal layer formats these same constants and byte-pins the result; a pin
	// against a format that layer also owned would prove only that it agrees
	// with itself.
	if fmt.Sprintf(UnknownParameterFormat, "--include-comments", "http://serve.internal:9099", "1.0.4", "include_comments") != want {
		t.Error("UnknownParameterFormat and UnknownParameterRefusal have drifted apart")
	}
}

func TestBeforeTheHandshakeTheRefusalNamesNoVersionRatherThanAnEmptyOne(t *testing.T) {
	// The handshake is lazy (D6), so a case-3 refusal can arrive from a baseline
	// operation that never fetched a context. Dialing for a version so the
	// refusal could be one clause longer would be the wrong trade — a refusal is
	// the one path that has to stay fast and side-effect free.
	got := UnknownParameterRefusal("label_regex", "http://serve.internal:9099", "")
	want := `--label-regex is not supported by bd serve at http://serve.internal:9099: parameter "label_regex" is unknown to the server`
	if got != want {
		t.Errorf("refusal =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, "bd_version") {
		t.Errorf("the pre-handshake refusal printed an empty version parenthetical: %s", got)
	}
	if fmt.Sprintf(UnknownParameterNoVersionFormat, "--label-regex", "http://serve.internal:9099", "label_regex") != want {
		t.Error("UnknownParameterNoVersionFormat and UnknownParameterRefusal have drifted apart")
	}
}

func TestASkewSignalAlwaysRendersEvenForAParameterThisClientNeverSent(t *testing.T) {
	// The enterprise server's Host-allowlist refusal is the live case: 400 with
	// `param: "Host"` for a DNS-named URL (internal/httpapi/server.go:1196). It
	// is a request header, not a query parameter, so no flag produced it — and a
	// refusal that came back empty here would swallow the one 4xx on this
	// surface whose recovery is a serve-side flag.
	got := UnknownParameterRefusal("Host", "http://bd.example.invalid:9099", "1.0.4")
	want := `the "Host" filter is not supported by bd serve at http://bd.example.invalid:9099 (bd_version 1.0.4): parameter "Host" is unknown to the server`
	if got != want {
		t.Errorf("refusal =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, "--") {
		t.Errorf("refusal invented a flag for a parameter with none: %s", got)
	}
	// And the degenerate input, because a server is untrusted text: an empty
	// `param` must still produce a sentence rather than a fragment.
	if empty := UnknownParameterRefusal("", "http://x", "1.0.4"); empty == "" {
		t.Error("an empty param rendered an empty refusal")
	}
}

func TestTheSkewReasonIsTheServersOwnSpelling(t *testing.T) {
	// Shared with internal/httpapi's ReasonUnknownParameter (problem.go:139).
	// The two refusals that ride `invalid_argument` — unknown_parameter and
	// invalid_value — carry opposite recoveries, so the reason is what
	// dispatches. A typo here would classify every skew signal as a bad value.
	if UnknownParameterReason != "unknown_parameter" {
		t.Errorf("UnknownParameterReason = %q", UnknownParameterReason)
	}
}
