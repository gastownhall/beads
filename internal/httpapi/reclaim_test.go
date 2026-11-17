package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These cover the transport half of POST /v0/beads/issues:reclaim: the body
// shape, the refusals the handler raises itself, how the role's own refusals
// reach the wire, and the projection of the role's result. Everything below
// the wire (staleness, scope, the replica guard, the minted revision, the hook)
// is issueops.LeaseReclaimer's, pinned by
// backend/conformance/lease_reclaimer_contract.go on all four legs.

const reclaimPath = "/v0/beads/issues:reclaim"

func newReclaimServer(t *testing.T, reclaimer *roleLeaseReclaimer) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{LeaseReclaimer: reclaimer}))
}

// TestReclaimIssuesMapsEveryMemberOntoTheRole pins the request half: every
// member reaches the role's request, the actor trimmed by the claim's rules and
// the fractional grace window converted exactly.
func TestReclaimIssuesMapsEveryMemberOntoTheRole(t *testing.T) {
	reclaimer := &roleLeaseReclaimer{result: issueops.ReclaimResult{Reclaimed: []issueops.ReclaimedLease{}}}
	ts := newReclaimServer(t, reclaimer)

	resp := ts.claim(t, reclaimPath, `{
		"actor": "  reaper  ",
		"older_than_seconds": 90.5,
		"ids": ["bd-1", "bd-2"],
		"assignees": ["w1"],
		"labels": ["lane-a"],
		"labels_any": ["x", "y"],
		"exclude_labels": ["pinned"],
		"any_replica": true
	}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	calls := reclaimer.reclaimRequests()
	if len(calls) != 1 {
		t.Fatalf("role calls = %d, want 1", len(calls))
	}
	want := issueops.ReclaimRequest{
		Actor:     "reaper",
		OlderThan: 90*time.Second + 500*time.Millisecond,
		Filter: issueops.ReclaimFilter{
			IDs:           []string{"bd-1", "bd-2"},
			Assignees:     []string{"w1"},
			Labels:        []string{"lane-a"},
			LabelsAny:     []string{"x", "y"},
			ExcludeLabels: []string{"pinned"},
			AnyReplica:    true,
		},
	}
	if !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("role request = %+v, want %+v", calls[0], want)
	}
}

// TestReclaimIssuesDefaultsToAnUnscopedZeroGraceSweep pins the absent members:
// no grace window is zero, no scope is no scope, and any_replica is off.
func TestReclaimIssuesDefaultsToAnUnscopedZeroGraceSweep(t *testing.T) {
	reclaimer := &roleLeaseReclaimer{result: issueops.ReclaimResult{Reclaimed: []issueops.ReclaimedLease{}}}
	ts := newReclaimServer(t, reclaimer)

	resp := ts.claim(t, reclaimPath, `{"actor":"reaper"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	calls := reclaimer.reclaimRequests()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], issueops.ReclaimRequest{Actor: "reaper"}) {
		t.Fatalf("role calls = %+v, want one bare request from reaper", calls)
	}
}

// TestReclaimIssuesAnswersEveryReclaimedLeaseWithItsRevision pins the response
// half: each entry carries id, previous_owner and the role's revision as the
// decimal string, past 2^53 so a float64 anywhere on the path would show.
func TestReclaimIssuesAnswersEveryReclaimedLeaseWithItsRevision(t *testing.T) {
	reclaimer := &roleLeaseReclaimer{result: issueops.ReclaimResult{Reclaimed: []issueops.ReclaimedLease{
		{ID: "bd-1", PreviousOwner: "w1", Revision: types.RevisionToken(guardToken)},
		{ID: "bd-2", PreviousOwner: "", Revision: "7"},
	}}}
	ts := newReclaimServer(t, reclaimer)

	resp := ts.claim(t, reclaimPath, `{"actor":"reaper"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw := readAll(t, resp)
	var body struct {
		Reclaimed []map[string]any `json:"reclaimed"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if len(body.Reclaimed) != 2 {
		t.Fatalf("reclaimed = %v, want 2 entries", body.Reclaimed)
	}
	first := body.Reclaimed[0]
	if first["id"] != "bd-1" || first["previous_owner"] != "w1" || first["revision"] != types.RevisionToken(guardToken) {
		t.Errorf("reclaimed[0] = %v, want {bd-1 w1 %s}", first, types.RevisionToken(guardToken))
	}
	if second := body.Reclaimed[1]; second["previous_owner"] != "" || second["revision"] != "7" {
		t.Errorf("reclaimed[1] = %v, want an empty previous_owner and revision \"7\"", second)
	}
}

// TestReclaimIssuesAnswersAnEmptyArrayNotNull pins `reclaimed: []` when the
// sweep found nothing, whatever the role handed back.
func TestReclaimIssuesAnswersAnEmptyArrayNotNull(t *testing.T) {
	for _, result := range []issueops.ReclaimResult{{}, {Reclaimed: []issueops.ReclaimedLease{}}} {
		ts := newReclaimServer(t, &roleLeaseReclaimer{result: result})
		resp := ts.claim(t, reclaimPath, `{"actor":"reaper","ids":["bd-gone"]}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
		}
		if raw := readAll(t, resp); !strings.Contains(raw, `"reclaimed":[]`) {
			t.Errorf("body = %s, want `\"reclaimed\":[]`", raw)
		}
	}
}

// TestReclaimIssuesRefusesAMalformedBodyByName pins every refusal the handler
// raises itself: each is a 400 naming its member, and none reaches the role.
func TestReclaimIssuesRefusesAMalformedBodyByName(t *testing.T) {
	for _, tc := range []struct {
		name, body, param, reason string
	}{
		{"unknown member", `{"actor":"r","force":true}`, "force", string(ReasonUnknownParameter)},
		{"missing actor", `{"ids":["bd-1"]}`, "actor", string(ReasonInvalidValue)},
		{"null actor", `{"actor":null}`, "actor", string(ReasonInvalidValue)},
		{"blank actor", `{"actor":"   "}`, "actor", ""},
		{"negative grace", `{"actor":"r","older_than_seconds":-1}`, "older_than_seconds", string(ReasonInvalidValue)},
		{"string grace", `{"actor":"r","older_than_seconds":"10m"}`, "older_than_seconds", string(ReasonInvalidValue)},
		{"grace past a duration", `{"actor":"r","older_than_seconds":1e300}`, "older_than_seconds", string(ReasonInvalidValue)},
		{"ids not an array", `{"actor":"r","ids":"bd-1"}`, "ids", string(ReasonInvalidValue)},
		{"empty ids", `{"actor":"r","ids":[]}`, "ids", string(ReasonInvalidValue)},
		{"empty labels", `{"actor":"r","labels":[]}`, "labels", string(ReasonInvalidValue)},
		{"empty assignees", `{"actor":"r","assignees":[]}`, "assignees", string(ReasonInvalidValue)},
		{"null exclude", `{"actor":"r","exclude_labels":null}`, "exclude_labels", string(ReasonInvalidValue)},
		{"any_replica not a bool", `{"actor":"r","any_replica":"yes"}`, "any_replica", string(ReasonInvalidValue)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reclaimer := &roleLeaseReclaimer{}
			ts := newReclaimServer(t, reclaimer)

			resp := ts.claim(t, reclaimPath, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want %q", body["code"], CodeInvalidArgument)
			}
			if body["param"] != tc.param {
				t.Errorf("param = %v, want %s", body["param"], tc.param)
			}
			if tc.reason != "" && body["reason"] != tc.reason {
				t.Errorf("reason = %v, want %s", body["reason"], tc.reason)
			}
			if calls := reclaimer.reclaimRequests(); len(calls) != 0 {
				t.Errorf("the role was called %d times for a refused request", len(calls))
			}
		})
	}
}

// TestReclaimIssuesRefusesAQueryParameter pins the document-level
// unknown-parameter rule on this operation, which declares none.
func TestReclaimIssuesRefusesAQueryParameter(t *testing.T) {
	reclaimer := &roleLeaseReclaimer{}
	ts := newReclaimServer(t, reclaimer)

	resp := ts.claim(t, reclaimPath+"?older_than=1h", `{"actor":"reaper"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["param"] != "older_than" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("param/reason = %v/%v, want older_than/unknown_parameter", body["param"], body["reason"])
	}
	if calls := reclaimer.reclaimRequests(); len(calls) != 0 {
		t.Errorf("the role was called %d times for a refused request", len(calls))
	}
}

// TestReclaimIssuesLeavesTheIDsCapToTheRole pins that the handler neither
// counts nor trims `ids`: the whole oversized request reaches the role, and
// the role's *TooManyReclaimIDsError comes back as a 400 naming `ids`.
func TestReclaimIssuesLeavesTheIDsCapToTheRole(t *testing.T) {
	const over = issueops.MaxReclaimIDs + 1
	reclaimer := &roleLeaseReclaimer{err: &issueops.TooManyReclaimIDsError{Requested: over, Cap: issueops.MaxReclaimIDs}}
	ts := newReclaimServer(t, reclaimer)

	ids := make([]string, over)
	for i := range ids {
		ids[i] = fmt.Sprintf(`"bd-%d"`, i)
	}
	resp := ts.claim(t, reclaimPath, `{"actor":"reaper","ids":[`+strings.Join(ids, ",")+`]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) || body["param"] != "ids" || body["reason"] != string(ReasonInvalidValue) {
		t.Errorf("problem = %v, want invalid_argument/ids/invalid_value", body)
	}
	calls := reclaimer.reclaimRequests()
	if len(calls) != 1 || len(calls[0].Filter.IDs) != over {
		t.Fatalf("role calls = %d, want one carrying all %d ids", len(calls), over)
	}
}

// TestReclaimIssuesAnswersABlankIDThroughTheRole pins the other role-side
// refusal: a blank entry in `ids` reaches the role (it is not pre-checked
// here), and its ErrValidation is a 400 naming `ids`.
func TestReclaimIssuesAnswersABlankIDThroughTheRole(t *testing.T) {
	reclaimer := &roleLeaseReclaimer{err: &issueops.ReclaimFieldError{
		Field: issueops.ReclaimFieldIDs, Detail: "validation failed: reclaim scope id at position 1 is empty",
	}}
	ts := newReclaimServer(t, reclaimer)

	resp := ts.claim(t, reclaimPath, `{"actor":"reaper","ids":["bd-1",""]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "ids" {
		t.Errorf("param = %v, want ids", body["param"])
	}
	if calls := reclaimer.reclaimRequests(); len(calls) != 1 {
		t.Fatalf("role calls = %d, want 1", len(calls))
	}
}

// TestReclaimIssuesNamesTheFieldTheRoleRefused pins that a role refusal is
// attributed to the member the role's *ReclaimFieldError names, not to `ids`
// by default, and that the handler leaves blank scope entries to the role:
// the request reaches it.
func TestReclaimIssuesNamesTheFieldTheRoleRefused(t *testing.T) {
	for field, member := range reclaimFieldMembers {
		t.Run(field, func(t *testing.T) {
			reclaimer := &roleLeaseReclaimer{err: &issueops.ReclaimFieldError{Field: field, Detail: "validation failed: refused"}}
			ts := newReclaimServer(t, reclaimer)

			resp := ts.claim(t, reclaimPath, `{"actor":"reaper","labels_any":["a"," "]}`)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			if body := decodeBody(t, resp); body["param"] != member {
				t.Errorf("param = %v, want %s", body["param"], member)
			}
			calls := reclaimer.reclaimRequests()
			if len(calls) != 1 || !reflect.DeepEqual(calls[0].Filter.LabelsAny, []string{"a", " "}) {
				t.Fatalf("role calls = %+v, want one carrying the blank entry unfiltered", calls)
			}
		})
	}
}

// TestReclaimFieldMembersCoverEveryRoleField pins the attribution table
// against the role's whole field vocabulary, so a field added to the role
// cannot reach the wire with no member named.
func TestReclaimFieldMembersCoverEveryRoleField(t *testing.T) {
	for _, field := range []string{
		issueops.ReclaimFieldActor, issueops.ReclaimFieldOlderThan, issueops.ReclaimFieldIDs,
		issueops.ReclaimFieldAssignees, issueops.ReclaimFieldLabels, issueops.ReclaimFieldLabelsAny,
		issueops.ReclaimFieldExcludeLabels,
	} {
		if reclaimFieldMembers[field] == "" {
			t.Errorf("role field %q has no wire member", field)
		}
	}
}

// TestReclaimIssuesRefusesTheFirstUnrepresentableGraceWindow pins the
// boundary: float64(MaxInt64)/1e9 converts back to 2^63 nanoseconds, which
// wraps negative, so that exact value is refused, not passed on.
func TestReclaimIssuesRefusesTheFirstUnrepresentableGraceWindow(t *testing.T) {
	reclaimer := &roleLeaseReclaimer{}
	ts := newReclaimServer(t, reclaimer)

	value := strconv.FormatFloat(maxReclaimOlderThanSeconds, 'g', -1, 64)
	resp := ts.claim(t, reclaimPath, `{"actor":"reaper","older_than_seconds":`+value+`}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("older_than_seconds=%s: status = %d, want 400: %s", value, resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "older_than_seconds" {
		t.Errorf("param = %v, want older_than_seconds", body["param"])
	}
	if calls := reclaimer.reclaimRequests(); len(calls) != 0 {
		t.Fatalf("the role received %+v; an out-of-range window must not reach it", calls)
	}
}

// TestReclaimIssuesDoesNotDisguiseAStorageFailure pins that a failure that is
// not a validation is not answered as the caller's fault.
func TestReclaimIssuesDoesNotDisguiseAStorageFailure(t *testing.T) {
	ts := newReclaimServer(t, &roleLeaseReclaimer{err: errors.New("disk on fire")})

	resp := ts.claim(t, reclaimPath, `{"actor":"reaper"}`)
	if resp.StatusCode < 500 {
		t.Fatalf("status = %d, want a 5xx: %s", resp.StatusCode, readAll(t, resp))
	}
}
