package httpapi

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/issueops"
)

// The pins for POST /v0/beads/issues:sweep. What is asserted here is the WIRE
// EDGE — that the handler decodes the document's six members into the role's
// request faithfully, refuses what the document refuses, and does not
// re-implement anything the role owns.

const sweepPath = "/v0/beads/issues:sweep"

func (ts *testServer) sweep(t *testing.T, body string) *http.Response {
	t.Helper()
	return ts.postBody(t, sweepPath, "application/json", body)
}

// TestSweepPathReachesItsHandler: the sweep path is a LITERAL segment
// registered beside the claim's wildcard `/v0/beads/issues/{idop}`, and ServeMux
// precedence is by specificity rather than by registration order. A 404 or a
// claim-shaped refusal here would mean the sweep is being parsed as a claim of
// an issue named ":sweep".
func TestSweepPathReachesItsHandler(t *testing.T) {
	sweeper := &roleSweeper{result: issueops.SweepResult{Swept: 3}}
	ts := newTestServer(t, rolesConfig(Config{Sweeper: sweeper}))

	resp := ts.sweep(t, `{"tier":"ephemeral"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if len(sweeper.requests()) != 1 {
		t.Fatalf("the sweep role was called %d times, want 1 — the path reached another handler",
			len(sweeper.requests()))
	}
}

// TestSweepForwardsEveryDocumentedMember is the operation's central pin: each
// of the six body members reaches the role's request unchanged.
//
// It is asserted on the REQUEST the role received rather than on the response:
// a body carrying the right numbers says nothing about which set was swept, and
// a handler dropping a narrowing member widens what is erased.
func TestSweepForwardsEveryDocumentedMember(t *testing.T) {
	sweeper := &roleSweeper{}
	ts := newTestServer(t, rolesConfig(Config{Sweeper: sweeper}))

	resp := ts.sweep(t, `{
		"tier": "durable",
		"actor": "  alice  ",
		"closed_before": "2026-03-01T12:00:00Z",
		"pattern": "bd-old-*",
		"protect_referenced": true,
		"dry_run": true
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	reqs := sweeper.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d sweeps, want 1", len(reqs))
	}
	got := reqs[0]
	want := issueops.SweepRequest{
		Tier: issueops.SweepDurable,
		// TRIMMED, by the same rule and the same function the claim's actor
		// goes through: it reaches the same commit-message interpolation.
		Actor:             "alice",
		IDPattern:         "bd-old-*",
		ProtectReferenced: true,
		DryRun:            true,
	}
	cutoff := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	want.ClosedBefore = &cutoff
	if got.ClosedBefore == nil || !got.ClosedBefore.Equal(cutoff) {
		t.Errorf("ClosedBefore = %v, want %v", got.ClosedBefore, cutoff)
	}
	got.ClosedBefore, want.ClosedBefore = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request = %+v, want %+v", got, want)
	}
}

// TestSweepDefaultsTheOptionalMembers: a body carrying only `tier` reaches the
// role as an unfiltered request of that tier, and NOT as a dry run.
//
// The dry-run default is the one worth pinning. `dry_run` absent must mean
// false — that is what the document says — and a handler that defaulted it the
// safe-looking way would make the endpoint silently incapable of deleting
// anything.
func TestSweepDefaultsTheOptionalMembers(t *testing.T) {
	sweeper := &roleSweeper{}
	ts := newTestServer(t, rolesConfig(Config{Sweeper: sweeper}))

	if resp := ts.sweep(t, `{"tier":"ephemeral"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := sweeper.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d sweeps, want 1", len(reqs))
	}
	// protect_referenced ABSENT means TRUE on this surface, which is the one
	// default here that deliberately differs from the role's zero value. The
	// endpoint is destructive and its optional bearer is shared and
	// surface-wide, so an omitted member must not buy weaker protection than
	// `bd prune` gives by default.
	want := issueops.SweepRequest{Tier: issueops.SweepEphemeral, ProtectReferenced: true}
	if reqs[0] != want {
		t.Errorf("request = %+v, want %+v", reqs[0], want)
	}
}

// TestSweepHonorsAnExplicitProtectReferencedFalse is the other half of the
// default: opting OUT still works. Without this the defaults test alone could be
// satisfied by a handler that hard-coded protection on.
func TestSweepHonorsAnExplicitProtectReferencedFalse(t *testing.T) {
	sweeper := &roleSweeper{}
	ts := newTestServer(t, rolesConfig(Config{Sweeper: sweeper}))

	if resp := ts.sweep(t, `{"tier":"ephemeral","protect_referenced":false}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := sweeper.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d sweeps, want 1", len(reqs))
	}
	if reqs[0].ProtectReferenced {
		t.Errorf("explicit protect_referenced:false did not reach the role: %+v", reqs[0])
	}
}

// TestSweepPublishesTheWholeResult pins the projection. SweepResult is
// deliberately not x-go-type-pinned, so a field added to the role and forgotten
// here would vanish from the wire in silence.
func TestSweepPublishesTheWholeResult(t *testing.T) {
	sweeper := &roleSweeper{result: issueops.SweepResult{
		DryRun:       true,
		Swept:        7,
		Dependencies: 3,
		Labels:       2,
		Events:       11,
		Skipped: issueops.SweepSkips{
			Pinned: 1, Referenced: 2, NotClosed: 3,
			UnknownClosedAt: 4, ClosedAtOrAfterCutoff: 5, Unreadable: 6,
		},
		ReferencedIDs: []string{"bd-1", "bd-2"},
	}}
	ts := newTestServer(t, rolesConfig(Config{Sweeper: sweeper}))

	resp := ts.sweep(t, `{"tier":"durable","pattern":"*","dry_run":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)

	for _, want := range []struct {
		key string
		val any
	}{
		{"dry_run", true},
		{"swept", float64(7)},
		{"dependencies", float64(3)},
		{"labels", float64(2)},
		{"events", float64(11)},
	} {
		if body[want.key] != want.val {
			t.Errorf("%s = %v, want %v", want.key, body[want.key], want.val)
		}
	}

	skipped, ok := body["skipped"].(map[string]any)
	if !ok {
		t.Fatalf("skipped = %v, want an object", body["skipped"])
	}
	for key, want := range map[string]float64{
		"pinned": 1, "referenced": 2, "not_closed": 3,
		"unknown_closed_at": 4, "closed_at_or_after_cutoff": 5, "unreadable": 6,
	} {
		if skipped[key] != want {
			t.Errorf("skipped.%s = %v, want %v", key, skipped[key], want)
		}
	}

	ids, ok := body["referenced_ids"].([]any)
	if !ok || len(ids) != 2 || ids[0] != "bd-1" {
		t.Errorf("referenced_ids = %v, want the role's sample", body["referenced_ids"])
	}
}

// TestSweepOmitsAnEmptyReferencedSample: the member is documented ABSENT when
// nothing was protected, so a client can tell "nothing cited" from "the
// protection was not asked for" without a second field.
func TestSweepOmitsAnEmptyReferencedSample(t *testing.T) {
	ts := newTestServer(t, rolesConfig(Config{Sweeper: &roleSweeper{}}))

	resp := ts.sweep(t, `{"tier":"ephemeral"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if _, present := decodeBody(t, resp)["referenced_ids"]; present {
		t.Error("referenced_ids is present with nothing protected; the document says absent")
	}
}

// TestSweepRefusesTheDocumentedBodies walks the 400 table. Every case names the
// member it is about in `param`, which is why the handler decodes member by
// member instead of unmarshaling the generated struct.
func TestSweepRefusesTheDocumentedBodies(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		param string
	}{
		{"no tier", `{"pattern":"*"}`, "tier"},
		{"tier is not a string", `{"tier":3}`, "tier"},
		{"tier is null", `{"tier":null}`, "tier"},
		{"tier outside the enum", `{"tier":"wisps"}`, "tier"},
		{"unknown member", `{"tier":"durable","scope":"all"}`, "scope"},
		{"actor is not a string", `{"tier":"ephemeral","actor":7}`, "actor"},
		{"actor is blank", `{"tier":"ephemeral","actor":"  "}`, "actor"},
		{"closed_before is not a timestamp", `{"tier":"ephemeral","closed_before":"yesterday"}`, "closed_before"},
		{"pattern is not a string", `{"tier":"ephemeral","pattern":true}`, "pattern"},
		{"dry_run is not a boolean", `{"tier":"ephemeral","dry_run":"yes"}`, "dry_run"},
		{"protect_referenced is not a boolean", `{"tier":"ephemeral","protect_referenced":1}`, "protect_referenced"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sweeper := &roleSweeper{}
			ts := newTestServer(t, rolesConfig(Config{Sweeper: sweeper}))

			resp := ts.sweep(t, test.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
			}
			if body["param"] != test.param {
				t.Errorf("param = %v, want %q", body["param"], test.param)
			}
			// A refused body must never reach the role: the sweep is
			// destructive, and "refused after acting" is the one failure shape
			// worth ruling out at every edge.
			if got := len(sweeper.requests()); got != 0 {
				t.Errorf("the role was called %d times for a refused body, want 0", got)
			}
		})
	}
}

// TestSweepPublishesTheRolesRefusalAsA400 is the reason failSweepErr exists.
// The require-a-filter gate, the tier vocabulary and the glob are all refused
// BELOW the wire, by issueops.Sweeper — that is what makes this endpoint
// incapable of an unguarded mass delete. A role refusal reaching the client as
// a 500 would hide the one sentence that says what to send instead.
func TestSweepPublishesTheRolesRefusalAsA400(t *testing.T) {
	// The sentinel has to MATCH, not merely be mentioned in the text.
	sweeper := &roleSweeper{err: wrapValidation("a durable sweep requires a closed-before cutoff or an id pattern")}
	ts := newTestServer(t, rolesConfig(Config{Sweeper: sweeper}))

	// A body the HANDLER accepts and the ROLE refuses: the durable tier with no
	// narrowing member is exactly that request, which is the point.
	resp := ts.sweep(t, `{"tier":"durable"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) {
		t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "closed-before cutoff") {
		t.Errorf("detail = %q, want the role's own sentence: it is what says what to send instead", detail)
	}
	// No `param`: the refusal is about two absent members at once, and the
	// document declares `param` absent on exactly that case.
	if _, present := body["param"]; present {
		t.Errorf("param = %v, want it absent for a whole-request refusal", body["param"])
	}
}

// TestSweepRefusesAForeignMediaType: a JSON content type is not CORS-"simple",
// so a cross-origin sweep always triggers a preflight this server never
// approves. Accepting text/plain would let an attacker's page drive a mass
// delete from any browser on the host.
func TestSweepRefusesAForeignMediaType(t *testing.T) {
	sweeper := &roleSweeper{}
	ts := newTestServer(t, rolesConfig(Config{Sweeper: sweeper}))

	resp := ts.postBody(t, sweepPath, "text/plain", `{"tier":"ephemeral"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "Content-Type" {
		t.Errorf("param = %v, want Content-Type", body["param"])
	}
	if got := len(sweeper.requests()); got != 0 {
		t.Errorf("the role was called %d times for a refused media type, want 0", got)
	}
}

// TestSweepPublishesNoQueryParameters: this operation's whole vocabulary is its
// body, so a query key is version skew rather than a bad value.
func TestSweepPublishesNoQueryParameters(t *testing.T) {
	ts := newTestServer(t, rolesConfig(Config{Sweeper: &roleSweeper{}}))

	resp := ts.postBody(t, sweepPath+"?tier=durable", "application/json", `{"tier":"durable","pattern":"*"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["param"] != "tier" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("body = %v, want unknown_parameter on param tier", body)
	}
}

// wrapValidation builds an error that MATCHES issueops.ErrValidation while
// reading like the role's own refusal, which is what failSweepErr keys on.
func wrapValidation(detail string) error {
	return &validationError{detail: detail}
}

type validationError struct{ detail string }

func (e *validationError) Error() string { return issueops.ErrValidation.Error() + ": " + e.detail }
func (e *validationError) Unwrap() error { return issueops.ErrValidation }
