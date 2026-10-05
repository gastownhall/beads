// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/skew_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/issueops"
)

// The version-skew matrix.
//
// Everything here is a DOCTORED server: a hand-built ContextResponse or a canned
// problem+json describing a bd serve that is older, newer, or subtly wrong in a
// way no real binary in the fixture cache is. That is the point. The released
// binaries in the cross-version smoke lane can only be the versions that were
// actually cut; this file is where the branch matrix lives, because a doctored
// context can be a server that never shipped and a released one cannot be a
// server that has not shipped YET.
//
// The split is the design's (test-lane map, the skew rows): the deterministic,
// in-process half runs at PR cadence here, and the released-binary composition
// half runs in cross-version-smoke.yml. Neither substitutes for the other —
// this half owns the branches, that half owns the reality check.
//
// Three skew directions are covered, and they fail in different ways:
//
//	newer CLI, older server   the client sends something the server never had.
//	                          A capability it does not advertise is D7 case 2,
//	                          caught BEFORE the dial; a parameter it does not
//	                          know is D7 case 3, caught in the 400 that comes
//	                          back.
//	older CLI, newer server   the server sends something the client never had.
//	                          Every one of these must be IGNORED — an unknown
//	                          response member, an unknown capability token, an
//	                          unknown problem code — because the wire's
//	                          additivity is the whole reason a client older than
//	                          its server keeps working.
//	wrong server entirely     the identity gates: api_version and project_id.

// firstSliceTokens is what a v0 server from the first slice advertises: the four
// gated operations of the six-operation first release (`bd-serve-v0.md`, "The
// surface"). health and getContext contribute no token.
//
// It is spelled out rather than derived, because deriving it from the client's
// own baseline set would make the fixture agree with the code under test by
// construction — the exact way a skew fixture goes vacuous.
var firstSliceTokens = []string{"issues.claim", "issues.get", "issues.list", "ready.list"}

// allTokens is the tip server's advertised set, taken from this client's own
// map. Set-equality with the SERVER's list is the vocabulary gate's job
// (TestCapabilityTableMatchesTheServerRouteTable); here it is just "a server
// that has everything".
func allTokens() []string {
	out := make([]string, 0, len(opCapability))
	for _, token := range opCapability {
		if token != "" {
			out = append(out, token)
		}
	}
	slices.Sort(out)
	return out
}

func postBaselineOps() []string {
	out := make([]string, 0, len(opCapability))
	for op := range opCapability {
		if !IsBaseline(op) {
			out = append(out, op)
		}
	}
	slices.Sort(out)
	return out
}

func TestAgainstAFirstSliceServerEveryPostBaselineOperationRefusesWithCaseTwoData(t *testing.T) {
	// The headline skew case: today's CLI against the server that shipped first.
	// Twenty-six operations did not exist there, and every one of them has to
	// refuse from the advertised list rather than dial and read a bare 404 as
	// "no such issue".
	c, rec := newTestClient(t, Options{}, nil, serveContext(contextBody("v0", "proj-1", firstSliceTokens...)))

	// Partition the post-baseline set by whether the first-slice server already
	// serves the operation. claimIssue is the one post-baseline op gated on a
	// first-slice token (issues.claim): forcing its handshake (ga-b8ddd.11) adds
	// the identity gate, but because that token IS advertised it preflights clean
	// against even the oldest server — no capability-compat risk. Everything else
	// post-baseline is a genuinely newer operation the first slice never served.
	var compat, refused []string
	for _, op := range postBaselineOps() {
		token, _ := CapabilityFor(op)
		if slices.Contains(firstSliceTokens, token) {
			compat = append(compat, op)
		} else {
			refused = append(refused, op)
		}
	}
	if len(compat) != 1 || compat[0] != OpClaimIssue {
		t.Fatalf("post-baseline ops on a first-slice token = %v, want exactly [claimIssue]", compat)
	}
	// The dimension is pinned so the matrix cannot shrink silently: the first
	// slice served six operations and the tip surface has forty-one, so
	// thirty-five of them are things a first-slice server has never heard of. The
	// wire wave moved this from twenty-seven to thirty — claimNext, release and
	// count — wave 2 to thirty-two, adding the dependency-edge count and the
	// related read, and the mini-sync to thirty-five: the comment write and the
	// two config writes. Each is an operation a first-slice server never served
	// and this client does not yet dial, which is exactly the shape the refusal
	// matrix is about.
	if len(refused) != 35 {
		t.Fatalf("the post-baseline set has %d operations a first-slice server never served, want 35", len(refused))
	}

	// The compat op forces the handshake yet preflights clean: issues.claim is on
	// the first-slice server's advertised list, so the gate never becomes a refusal.
	for _, op := range compat {
		if err := c.Preflight(ctx(t), op); err != nil {
			t.Errorf("Preflight(%s) against a first-slice server = %v, want nil (issues.claim is first-slice)", op, err)
		}
	}

	for _, op := range refused {
		token, _ := CapabilityFor(op)
		err := c.Preflight(ctx(t), op)

		var absent *CapabilityError
		if !errors.As(err, &absent) {
			t.Errorf("Preflight(%s) = %v (%T), want *CapabilityError", op, err, err)
			continue
		}
		// D7 case 2 names three things, and the typed error has to carry all
		// three or the render layer starts parsing prose for them.
		if absent.Capability != token {
			t.Errorf("Preflight(%s) refused on capability %q, want %q", op, absent.Capability, token)
		}
		if absent.BdVersion != "1.1.0" {
			t.Errorf("Preflight(%s) carries bd_version %q, want the server's", op, absent.BdVersion)
		}
		if absent.ServerURL == "" || !strings.HasPrefix(absent.ServerURL, "http") {
			t.Errorf("Preflight(%s) carries server URL %q", op, absent.ServerURL)
		}
		if !slices.Equal(absent.Capabilities, firstSliceTokens) {
			t.Errorf("Preflight(%s) carries %v, want the server's advertised %v", op, absent.Capabilities, firstSliceTokens)
		}
		if !errors.Is(err, ErrCapabilityAbsent) {
			t.Errorf("Preflight(%s) does not unwrap to ErrCapabilityAbsent", op)
		}
	}

	// One context fetch for the whole matrix: the refusals are answered from the
	// cache, not by re-asking a server that already said no.
	if rec.count() != 1 {
		t.Errorf("made %d context fetches, want 1", rec.count())
	}
}

func TestEachOperationRefusesOnItsOwnTokenAndNoOtherOperationsToken(t *testing.T) {
	// The holdout matrix: for every post-baseline operation, a doctored server
	// that advertises the whole vocabulary EXCEPT that operation's token.
	//
	// This is what catches a mis-wired opCapability row that a "server has
	// everything" and a "server has nothing" fixture both miss: two operations
	// sharing a token, or an operation pointed at a neighbour's, still pass both
	// extremes and fail exactly here.
	every := allTokens()

	for _, op := range postBaselineOps() {
		token, _ := CapabilityFor(op)
		held := make([]string, 0, len(every))
		for _, candidate := range every {
			if candidate != token {
				held = append(held, candidate)
			}
		}

		c, _ := newTestClient(t, Options{}, nil, serveContext(contextBody("v0", "proj-1", held...)))

		var absent *CapabilityError
		if err := c.Preflight(ctx(t), op); !errors.As(err, &absent) {
			t.Errorf("holding back %q: Preflight(%s) = %v, want a refusal", token, op, err)
		} else if absent.Capability != token {
			t.Errorf("holding back %q: Preflight(%s) refused on %q", token, op, absent.Capability)
		}

		// And nothing else moved: every other operation still serves against the
		// same doctored server.
		for _, other := range postBaselineOps() {
			if other == op {
				continue
			}
			if err := c.Preflight(ctx(t), other); err != nil {
				t.Errorf("holding back %q broke an unrelated operation: Preflight(%s) = %v", token, other, err)
			}
		}
	}
}

func TestABaselineOperationDispatchesEvenWhenTheServerAdvertisesNoTokenForIt(t *testing.T) {
	// The doctored route table, missing arm: a server whose advertised list has
	// none of the baseline tokens.
	//
	// The two-speed policy says the five baseline operations dispatch without
	// consulting the list AT ALL, and that has to survive a server that says
	// nothing about them — a trimmed deployment, a capability list built from a
	// route table someone pruned, or simply a context this client mis-decoded.
	// Gating them on the list would turn the hot work-distribution path off on
	// the strength of a field that is not the contract for it. claimIssue is not
	// in this set: it forces the handshake now (ga-b8ddd.11), so it would dial.
	c, rec := newTestClient(t, Options{}, nil, func(http.ResponseWriter, *http.Request) {
		t.Error("a baseline pre-flight dialed a server whose list omits its token")
	})
	for _, op := range []string{OpHealth, OpGetContext, OpListReadyWork, OpListIssues, OpGetIssue} {
		if err := c.Preflight(ctx(t), op); err != nil {
			t.Errorf("Preflight(%s) against a server advertising nothing: %v", op, err)
		}
	}
	if rec.count() != 0 {
		t.Errorf("made %d requests, want 0", rec.count())
	}
}

func TestAServerAdvertisingOperationsThisClientDoesNotKnowIsToleratedWhole(t *testing.T) {
	// The doctored route table, extra arm: a NEWER server, advertising tokens for
	// post-baseline operations this client has never heard of.
	//
	// Older CLI against newer server is the additive direction, and the rule is
	// that an unknown token is inert. It must not fail the handshake, must not
	// disturb the operations this client does know, and must still appear in the
	// advertised list a refusal prints — the user is entitled to see that the
	// server offers things their binary cannot reach.
	future := append(allTokens(), "issues.teleport", "beads.prophesy", "memories.forget.v2")
	slices.Sort(future)

	c, _ := newTestClient(t, Options{}, nil, serveContext(contextBody("v0", "proj-1", future...)))

	snap, err := c.Handshake(ctx(t))
	if err != nil {
		t.Fatalf("Handshake against a newer server: %v", err)
	}
	if !slices.Equal(snap.Capabilities(), future) {
		t.Errorf("Capabilities() = %v, want the server's own %v", snap.Capabilities(), future)
	}
	for _, op := range postBaselineOps() {
		if err := c.Preflight(ctx(t), op); err != nil {
			t.Errorf("Preflight(%s) against a newer server: %v", op, err)
		}
	}
	// An operation the client does not know still refuses at the pre-flight,
	// even though the SERVER advertises it: this client cannot build a request
	// for an operation it has no path or encoder for, and pretending otherwise
	// would turn an unknown operation into an unrouted dial.
	if err := c.Preflight(ctx(t), "teleportIssue"); err == nil {
		t.Error("an operation this client does not know pre-flighted clean because the server advertised one")
	}
}

func TestAFuturisticContextDecodesAndGatesOnApiVersionAlone(t *testing.T) {
	// Additive content on a v0 context: members this client's struct does not
	// have, a bd_version from the future, a schema_version well past anything
	// shipped. None of the three is a feature contract — the capability list is
	// — so all three have to be inert.
	body := `{"api_version":"v0","backend":"dolt","bd_version":"9.9.9",` +
		`"beads_dir":"/srv/repo/.beads","capabilities":["issues.list","issues.get"],` +
		`"database":"beads","dolt_mode":"embedded","project_id":"proj-1",` +
		`"repo_root":"/srv/repo","schema_version":9001,` +
		`"replication_lag_ms":12,"tenant":{"id":"acme","tier":"gold"},"features":["wisps","convoys"]}`

	c, _ := newTestClient(t, Options{ExpectProjectID: "proj-1"}, nil, serveContext(body))
	snap, err := c.Handshake(ctx(t))
	if err != nil {
		t.Fatalf("Handshake against a futuristic v0 server: %v", err)
	}
	if snap.Context.BdVersion != "9.9.9" || snap.Context.SchemaVersion != 9001 {
		t.Errorf("diagnostic members did not decode: %+v", snap.Context)
	}
	if !snap.Has("issues.list") || snap.Has("issues.update") {
		t.Errorf("capabilities decoded wrong alongside unknown members: %v", snap.Capabilities())
	}
	// And the list is still the contract: a token the futuristic server did NOT
	// advertise refuses, however new the server claims to be.
	if err := c.Preflight(ctx(t), OpUpdateIssue); !errors.Is(err, ErrCapabilityAbsent) {
		t.Errorf("Preflight(updateIssue) = %v, want a capability refusal", err)
	}
}

func TestTheApiVersionGateRefusesEveryNonV0Spelling(t *testing.T) {
	// The wire is versioned by PATH, so the gate is equality and not a range
	// check: a "v0.1" is not a v0 this client can address, and a server that
	// answers an empty api_version is not answering the question.
	for _, version := range []string{"v1", "v2", "v0.1", "V0", "0", "", "v0 "} {
		c, _ := newTestClient(t, Options{}, nil, serveContext(contextBody(version, "proj-1", "issues.list")))
		_, err := c.Handshake(ctx(t))
		if !errors.Is(err, ErrAPIVersion) {
			t.Errorf("api_version %q: err = %v, want ErrAPIVersion", version, err)
			continue
		}
		var mismatch *APIVersionError
		if !errors.As(err, &mismatch) {
			t.Errorf("api_version %q: err is %T, want *APIVersionError", version, err)
			continue
		}
		if mismatch.Got != version || mismatch.Want != APIVersion {
			t.Errorf("api_version %q: error carries got=%q want=%q", version, mismatch.Got, mismatch.Want)
		}
	}
}

func TestTheApiVersionGateRunsBeforeTheIdentityGate(t *testing.T) {
	// Ordering matters for the message, not for the outcome. A server on another
	// major will also have a different project_id, and reporting THAT would send
	// an operator to re-run bd connect against a server no bd connect can help
	// them with.
	c, _ := newTestClient(t, Options{ExpectProjectID: "proj-1"}, nil,
		serveContext(contextBody("v1", "proj-2", "issues.list")))
	_, err := c.Handshake(ctx(t))
	if !errors.Is(err, ErrAPIVersion) {
		t.Fatalf("err = %v, want the api_version gate to fire first", err)
	}
	if errors.Is(err, ErrProjectMismatch) {
		t.Error("the identity gate fired on a server this client cannot address at all")
	}
}

func TestTheWrongServerDiagnosticNamesEveryFactD6Requires(t *testing.T) {
	// D6's wrong-server checklist, whole: both project ids, plus the server's own
	// database and repo_root. On a shared host running several bd serve
	// processes, "the ids differ" identifies nothing — the database and the repo
	// root are what say WHICH one answered.
	c, _ := newTestClient(t, Options{ExpectProjectID: "proj-mine"}, nil,
		serveContext(contextBody("v0", "proj-theirs", "issues.list")))
	_, err := c.Handshake(ctx(t))

	var mismatch *ProjectMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err is %T, want *ProjectMismatchError", err)
	}
	for field, got := range map[string]string{
		"Expected":  mismatch.Expected,
		"Got":       mismatch.Got,
		"Database":  mismatch.Database,
		"RepoRoot":  mismatch.RepoRoot,
		"ServerURL": mismatch.ServerURL,
	} {
		if got == "" {
			t.Errorf("ProjectMismatchError.%s is empty", field)
		}
	}
	msg := mismatch.Error()
	for _, want := range []string{"proj-mine", "proj-theirs", "beads", "/srv/repo", "bd connect"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}

	// A server that publishes neither diagnostic still produces a whole sentence
	// rather than a dangling parenthetical — the members are optional on the
	// wire, and a degraded diagnostic is not a broken one.
	bare := `{"api_version":"v0","bd_version":"1.1.0","capabilities":[],"project_id":"proj-theirs"}`
	c2, _ := newTestClient(t, Options{ExpectProjectID: "proj-mine"}, nil, serveContext(bare))
	_, err2 := c2.Handshake(ctx(t))
	if !errors.Is(err2, ErrProjectMismatch) {
		t.Fatalf("err = %v, want ErrProjectMismatch", err2)
	}
	if strings.Contains(err2.Error(), `database ""`) {
		t.Errorf("the degraded diagnostic printed empty members: %s", err2.Error())
	}
}

func TestAnUnknownProblemCodeTakesTheDefaultBranchOfItsStatusClass(t *testing.T) {
	// Older CLI, newer server, error path. Adding a problem code is not a
	// breaking change on this wire, so a client that hard-failed on an unknown
	// one would break against every server newer than itself.
	//
	// The unit-level probe of sentinelFor covers the table; this covers the
	// crossing — a code arriving in a real body, over a real connection, in each
	// status class that has a rule.
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"a 4xx the client has never heard of", 409, `{"status":409,"code":"tenant_quota_exceeded","detail":"quota"}`, ErrBadRequest},
		{"a 400 with a future reason", 400, `{"status":400,"code":"malformed_expression","param":"q","reason":"unparseable"}`, ErrBadRequest},
		{"a 503 the client has never heard of", 503, `{"status":503,"code":"failing_over","detail":"draining"}`, ErrBusy},
		{"a 5xx the client has never heard of", 502, `{"status":502,"code":"upstream_gone"}`, ErrServerFault},
		{"a non-problem body from something that is not a bd serve", 404, `<html><title>404</title></html>`, ErrBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
				problemJSON(w, tc.status, tc.body)
			})
			err := c.Do(ctx(t), listIssues(), &struct{}{})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			// The envelope still arrives typed. A default-branched code is not a
			// discarded response: request_id is what makes the server's log line
			// findable, and it is the only handle on a code nobody here knows.
			var problem *ProblemError
			if !errors.As(err, &problem) {
				t.Fatalf("err is %T, want *ProblemError", err)
			}
			if problem.Status != tc.status {
				t.Errorf("Status = %d, want %d", problem.Status, tc.status)
			}
		})
	}
}

func TestAnUnknownResponseMemberDoesNotBreakADecode(t *testing.T) {
	// The same additivity on the success path. A server that grew a member is a
	// server this client keeps reading; refusing the decode would make every
	// forward-compatible response a hard failure.
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok","uptime_s":41,"leader":"node-3"}`)
	})
	if err := c.Health(ctx(t)); err != nil {
		t.Fatalf("Health against a server with extra members: %v", err)
	}
}

func TestTheUnknownParameterSignalCrossesTheWireAndRendersCaseThree(t *testing.T) {
	// The whole case-3 mechanism, end to end: a newer CLI's parameter, an older
	// server's 400, the typed members that survive it, and the reverse lookup
	// through the encoder table that turns the parameter back into the flag the
	// user typed.
	//
	// The encode import is test-only and deliberate. Production code in this
	// package must not depend on the encoder — the transport does not know what
	// a flag is — but the two halves of case 3 live in different packages, and
	// something has to prove they meet.
	for _, tc := range []struct {
		param, want string
	}{
		{"include_comments", `--include-comments is not supported by bd serve at %s (bd_version 1.1.0): parameter "include_comments" is unknown to the server`},
		{"include_dependents", `--include-dependents is not supported by bd serve at %s (bd_version 1.1.0): parameter "include_dependents" is unknown to the server`},
		{"exclude_type", `--exclude-type is not supported by bd serve at %s (bd_version 1.1.0): parameter "exclude_type" is unknown to the server`},
		{"has_metadata_key", `--has-metadata-key is not supported by bd serve at %s (bd_version 1.1.0): parameter "has_metadata_key" is unknown to the server`},
	} {
		t.Run(tc.param, func(t *testing.T) {
			c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == PathContext {
					_, _ = io.WriteString(w, contextBody("v0", "proj-1", allTokens()...))
					return
				}
				problemJSON(w, 400, `{"status":400,"code":"invalid_argument","param":"`+tc.param+
					`","reason":"unknown_parameter","detail":"unknown parameter \"`+tc.param+`\""}`)
			})

			err := c.Do(ctx(t), listIssues(), &struct{}{})
			var problem *ProblemError
			if !errors.As(err, &problem) {
				t.Fatalf("err is %T, want *ProblemError", err)
			}
			// The reason is what separates skew from a bad value: both ride
			// invalid_argument, and their recoveries are opposite.
			if problem.Reason != encode.UnknownParameterReason {
				t.Fatalf("reason = %q, want %q", problem.Reason, encode.UnknownParameterReason)
			}
			if !errors.Is(err, issueops.ErrValidation) {
				t.Error("the skew signal did not classify as a validation refusal")
			}

			snap, err := c.Handshake(ctx(t))
			if err != nil {
				t.Fatalf("Handshake: %v", err)
			}
			got := encode.UnknownParameterRefusal(problem.Param, problem.ServerURL, snap.Context.BdVersion)
			want := strings.Replace(tc.want, "%s", problem.ServerURL, 1)
			if got != want {
				t.Errorf("case-3 refusal =\n%s\nwant\n%s", got, want)
			}
			// D7's standing rule for every refusal text: the user's vocabulary,
			// never the client's internals.
			if strings.Contains(got, "listIssues") || strings.Contains(got, "ListRequest") {
				t.Errorf("the refusal leaked client vocabulary: %s", got)
			}
		})
	}
}

func TestAnInvalidValueIsNotReadAsVersionSkew(t *testing.T) {
	// The converse of the case above, and the reason the reason member exists.
	// `invalid_value` on a parameter the server DOES know means the argument is
	// wrong; rendering it as case 3 would tell a user to upgrade a server that is
	// working correctly.
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		problemJSON(w, 400, `{"status":400,"code":"invalid_argument","param":"sort","reason":"invalid_value","detail":"unknown sort key"}`)
	})
	err := c.Do(ctx(t), listIssues(), &struct{}{})
	var problem *ProblemError
	if !errors.As(err, &problem) {
		t.Fatalf("err is %T, want *ProblemError", err)
	}
	if problem.Reason == encode.UnknownParameterReason {
		t.Fatalf("reason = %q; a bad value must not classify as skew", problem.Reason)
	}
}

// TestNoFeatureBranchesOnBdVersionOrSchemaVersion is D6's other rule, and the
// only one in this package that no behavioral test can reach.
//
// "Clients never branch on bd_version for features (the capability list is the
// contract; bd_version appears only in error text) and never on schema_version
// (documented diagnostic-only)". A violation would not fail any test here — it
// would work perfectly against every server in the fixture cache and then
// mis-gate the first server whose version string is shaped differently, which is
// exactly the class of bug version strings are famous for.
//
// So the gate is structural, and it turns on a distinction the rule itself
// makes: the members may be DISPLAYED — copied into an error, formatted into a
// message, guarded by "do I have one to show" — but never used to DECIDE.
// Displaying is not branching, which is what keeps the refusal-UX layer's
// `bd_version` text legal while a version comparison stays fatal.
//
// SCOPE. Three groups, because the members travel: the wire package that decodes
// them, the store package that copies them into refusals, and the cmd/bd files
// that consume a handshake. Sweeping only this package is what an earlier
// revision did, and it left the store package — which reads snap.BdVersion in
// production — completely unguarded while the role wave was about to land more
// snapshot-consuming code exactly there.
func TestNoFeatureBranchesOnBdVersionOrSchemaVersion(t *testing.T) {
	fset := token.NewFileSet()
	total := 0

	for _, scope := range diagnosticSweepScopes() {
		files, sawAnyFile := parseSweepScope(t, fset, scope)
		if len(files) == 0 {
			if scope.only != nil && sawAnyFile {
				// The directory parsed real, non-test .go files, but none of
				// them import the http client yet. That is the honest state
				// of cmd/bd during S2: the store/dial wiring that makes
				// cmd/bd a consumer is deferred to S3 (see the httpclient
				// lift commit's message). It is not the "path moved" failure
				// this gate otherwise guards — that failure mode is an empty
				// directory read, handled below — so there is nothing to
				// sweep here yet and that is fine.
				//
				// TODO(S3): once cmd/bd wires the store/dial seam to this
				// client, this branch's cmd/bd scope stops being vacuous —
				// drop this `continue` (or tighten it) so the sweep actually
				// covers cmd/bd's own bd_version/schema_version reads instead
				// of silently passing on an empty intersection.
				continue
			}
			// A relative path that stopped resolving is the failure mode this
			// gate cannot survive quietly: zero files walk clean.
			t.Errorf("%s (%s): swept no files; the scope's path has moved and this gate went vacuous", scope.label, scope.dir)
			continue
		}
		for _, file := range files {
			total += checkDiagnosticUse(t, fset, file)
		}
	}

	// And the sweep has to have SEEN the members somewhere, or a rename would
	// leave every scope green by matching nothing at all.
	if total == 0 {
		t.Fatal("the sweep found no reference to BdVersion or SchemaVersion anywhere; " +
			"either the members were renamed or the scopes no longer contain the client")
	}
}

type sweepScope struct {
	label string
	dir   string
	// only, when set, keeps just the files this scope cares about.
	only func(*ast.File) bool
}

// diagnosticSweepScopes names every place a handshake member can be read.
//
// The cmd/bd entry is selected by IMPORT rather than by filename. The rule is
// about code that consumes a handshake, and "imports the http client" is exactly
// that set — mechanically determined, so a file the refusal-UX layer adds
// tomorrow is swept the day it reaches for the store. Selecting by filename
// prefix would miss it, and sweeping all of cmd/bd would drag in bd's own
// version tracking, which branches on bd's version because that is its entire
// job.
func diagnosticSweepScopes() []sweepScope {
	return []sweepScope{
		{label: "the wire transport", dir: "."},
		{label: "the http store", dir: ".."},
		{
			label: "cmd/bd's http-client consumers",
			dir:   filepath.Join("..", "..", "..", "cmd", "bd"),
			only:  importsHTTPClient,
		},
	}
}

func importsHTTPClient(file *ast.File) bool {
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		if strings.Contains(path, "internal/httpclient") {
			return true
		}
	}
	return false
}

// parseSweepScope parses every non-test .go file in scope.dir and applies
// scope.only. It also reports sawAnyFile: whether the directory yielded any
// parseable file at all BEFORE the only filter ran, so the caller can tell
// "the path is broken" (sawAnyFile false) apart from "the path is fine but
// nothing here is a consumer yet" (sawAnyFile true, out empty).
func parseSweepScope(t *testing.T, fset *token.FileSet, scope sweepScope) (out map[string]*ast.File, sawAnyFile bool) {
	t.Helper()
	// Build constraints are deliberately not honored: this test is untagged, and
	// the enterprise-only files are precisely the ones that consume a handshake.
	// Parsing is text, so they are swept either way.
	pkgs, err := parser.ParseDir(fset, scope.dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("%s (%s): parse: %v", scope.label, scope.dir, err)
	}
	out = map[string]*ast.File{}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			sawAnyFile = true
			if scope.only != nil && !scope.only(file) {
				continue
			}
			out[name] = file
		}
	}
	return out, sawAnyFile
}

// isDiagnosticRef matches a read of either member.
//
// Selector and bare identifier alike, because the value gets passed around: the
// connect report takes it as a plain `bdVersion string` parameter, and a
// comparison there is the same mistake as a comparison on the field. Exact,
// case-insensitive name equality keeps JSONSchemaVersion, LastBdVersion and
// trackBdVersion out of it.
func isDiagnosticRef(n ast.Node) bool {
	name := ""
	switch ref := n.(type) {
	case *ast.SelectorExpr:
		name = ref.Sel.Name
	case *ast.Ident:
		name = ref.Name
	default:
		return false
	}
	switch strings.ToLower(name) {
	case "bdversion", "schemaversion":
		return true
	}
	return false
}

func diagnosticRefs(n ast.Node) []ast.Node {
	var found []ast.Node
	ast.Inspect(n, func(inner ast.Node) bool {
		if inner == nil {
			return false
		}
		if isDiagnosticRef(inner) {
			found = append(found, inner)
			// Do not descend: the selector's own X is not a second reference.
			return false
		}
		return true
	})
	return found
}

// isPresenceGuard reports a comparison that asks "is there one to show" rather
// than "which one is it".
//
// `e.BdVersion != ""` is how a renderer omits a version it does not have, and
// D6's rule is about deciding on the VALUE. Allowing the zero comparison is what
// lets the refusal-UX layer print the version without this gate calling it a
// feature branch; anything compared against an actual version is still fatal.
func isPresenceGuard(expr *ast.BinaryExpr) bool {
	if expr.Op != token.EQL && expr.Op != token.NEQ {
		return false
	}
	other := func(side ast.Expr) bool {
		lit, ok := side.(*ast.BasicLit)
		if !ok {
			return false
		}
		return lit.Value == `""` || lit.Value == "0"
	}
	if len(diagnosticRefs(expr.X)) > 0 && other(expr.Y) {
		return true
	}
	return len(diagnosticRefs(expr.Y)) > 0 && other(expr.X)
}

func checkDiagnosticUse(t *testing.T, fset *token.FileSet, file *ast.File) int {
	t.Helper()

	// Every reference that sits inside an allowed presence guard, so the
	// condition check below can tell one from a real branch.
	allowed := map[ast.Node]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if expr, ok := n.(*ast.BinaryExpr); ok && isPresenceGuard(expr) {
			for _, ref := range diagnosticRefs(expr) {
				allowed[ref] = true
			}
		}
		return true
	})

	report := func(pos token.Pos, what string) {
		t.Errorf("%s: %s (D6: the capability list is the feature contract; these members are diagnostic-only)",
			fset.Position(pos), what)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.BinaryExpr:
			switch stmt.Op {
			case token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ:
				if len(diagnosticRefs(stmt)) > 0 && !isPresenceGuard(stmt) {
					report(stmt.Pos(), "a comparison decides on a diagnostic-only member; a version string is not a capability check")
				}
			}
		case *ast.IfStmt:
			for _, ref := range diagnosticRefs(stmt.Cond) {
				if !allowed[ref] {
					report(stmt.Pos(), "an `if` branches on a diagnostic-only member")
					break
				}
			}
		case *ast.SwitchStmt:
			if stmt.Tag != nil && len(diagnosticRefs(stmt.Tag)) > 0 {
				report(stmt.Pos(), "a `switch` dispatches on a diagnostic-only member")
			}
		}
		return true
	})

	return len(diagnosticRefs(file))
}
