// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/problem_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// refuseWith answers every request with one canned problem document and returns
// the error the client made of it.
func refuseWith(t *testing.T, status int, body string, headers map[string]string, r Request, opts ...func(*Options)) error {
	t.Helper()
	o := Options{}
	for _, fn := range opts {
		fn(&o)
	}
	c, _ := newTestClient(t, o, nil, func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		problemJSON(w, status, body)
	})
	return c.Do(ctx(t), r, &struct{}{})
}

func listIssues() Request {
	return Request{Op: OpListIssues, Method: http.MethodGet, Path: PathIssues}
}

// TestEveryProblemCodeMapsToItsSentinel walks the whole frozen v0 vocabulary at
// tip — the eleven upstream codes plus the enterprise tree's `unauthenticated`
// — with each code on the status the server freezes it to.
func TestEveryProblemCodeMapsToItsSentinel(t *testing.T) {
	cases := []struct {
		code   string
		status int
		body   string
		want   error
	}{
		{"invalid_argument", 400, `{"status":400,"code":"invalid_argument","detail":"unknown parameter","param":"sort","reason":"unknown_parameter","request_id":"r1"}`, issueops.ErrValidation},
		{"invalid_cursor", 400, `{"status":400,"code":"invalid_cursor","detail":"restart paging","request_id":"r2"}`, ErrInvalidCursor},
		{"unauthenticated", 401, `{"status":401,"code":"unauthenticated","detail":"missing or invalid bearer token","request_id":"r3"}`, ErrUnauthenticated},
		{"not_found", 404, `{"status":404,"code":"not_found","detail":"no issue or wisp with that id","request_id":"r4"}`, issueops.ErrNotFound},
		{"already_claimed", 409, `{"status":409,"code":"already_claimed","assignee":"other","issue_status":"in_progress","request_id":"r5"}`, issueops.ErrAlreadyClaimed},
		{"not_claimable", 409, `{"status":409,"code":"not_claimable","issue_status":"closed","request_id":"r6"}`, issueops.ErrNotClaimable},
		{"not_closable (open children)", 409, `{"status":409,"code":"not_closable","open_children":3,"request_id":"r7"}`, issueops.ErrCloseOpenChildren},
		{"not_closable (blocked)", 409, `{"status":409,"code":"not_closable","request_id":"r8"}`, issueops.ErrCloseBlocked},
		{"dependency_cycle (scheduling)", 409, `{"status":409,"code":"dependency_cycle","request_id":"r9"}`, issueops.ErrDependencyCycle},
		{"dependency_exists", 409, `{"status":409,"code":"dependency_exists","existing_type":"blocks","requested_type":"related","request_id":"r10"}`, nil},
		{"busy", 503, `{"status":503,"code":"busy","detail":"the server is busy; retry shortly","request_id":"r11"}`, ErrBusy},
		{"db_unavailable", 503, `{"status":503,"code":"db_unavailable","detail":"database temporarily unavailable; retry","request_id":"r12"}`, ErrDBUnavailable},
		{"internal", 500, `{"status":500,"code":"internal","detail":"internal server error","request_id":"r13"}`, ErrServerFault},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			err := refuseWith(t, tc.status, tc.body, nil, listIssues())

			var problem *ProblemError
			if !errors.As(err, &problem) {
				t.Fatalf("err is %T (%v), want *ProblemError", err, err)
			}
			if problem.Status != tc.status {
				t.Errorf("Status = %d, want %d", problem.Status, tc.status)
			}
			if problem.RequestID == "" {
				t.Error("RequestId did not survive the decode")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err does not match %v: %v", tc.want, err)
			}
			// The dependency_exists row has no plain sentinel: it is a typed
			// error whose members ARE the refusal.
			if tc.code == "dependency_exists" {
				var conflict *issueops.DependencyTypeConflictError
				if !errors.As(err, &conflict) {
					t.Fatalf("err is %T, want *issueops.DependencyTypeConflictError", err)
				}
			}
		})
	}
}

func TestClaimConflictsReconstructFromTheirExtensionMembers(t *testing.T) {
	// The holder and the status are read inside the transaction that lost the
	// compare-and-set; the client rebuilds the typed error from them instead of
	// parsing the refusal's prose.
	cases := []struct {
		name     string
		body     string
		sentinel error
		assignee string
		status   types.Status
	}{
		{
			name:     "already claimed",
			body:     `{"status":409,"code":"already_claimed","assignee":"agent-7","issue_status":"in_progress"}`,
			sentinel: issueops.ErrAlreadyClaimed,
			assignee: "agent-7",
			status:   types.StatusInProgress,
		},
		{
			// Empty assignee is the documented shape when the refusal was about
			// the status rather than a foreign holder.
			name:     "not claimable",
			body:     `{"status":409,"code":"not_claimable","issue_status":"closed"}`,
			sentinel: issueops.ErrNotClaimable,
			assignee: "",
			status:   types.StatusClosed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseWith(t, 409, tc.body, nil, Request{
				Op: OpClaimIssue, Method: http.MethodPost,
				Path: "/v0/beads/issues/ga-1:claim", IssueID: "ga-1",
			})

			var conflict *issueops.ClaimConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("err is %T (%v), want *issueops.ClaimConflictError", err, err)
			}
			// The id is not on the wire — the request already said it — so the
			// client supplies it and the sentinel comes out whole.
			if conflict.IssueID != "ga-1" {
				t.Errorf("IssueID = %q, want ga-1", conflict.IssueID)
			}
			if conflict.Assignee != tc.assignee {
				t.Errorf("Assignee = %q, want %q", conflict.Assignee, tc.assignee)
			}
			if conflict.Status != tc.status {
				t.Errorf("Status = %q, want %q", conflict.Status, tc.status)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("the wrapped sentinel does not match %v", tc.sentinel)
			}
		})
	}
}

func TestNotClosableUsesOpenChildrenPresenceAsTheDiscriminator(t *testing.T) {
	// One code, two refusals. Member PRESENCE tells them apart; `detail` is
	// never read.
	t.Run("present means open children", func(t *testing.T) {
		err := refuseWith(t, 409, `{"status":409,"code":"not_closable","open_children":4}`, nil, Request{
			Op: OpCloseIssue, Method: http.MethodPost,
			Path: "/v0/beads/issues/ga-1:close", IssueID: "ga-1",
		})
		var openChildren *issueops.CloseOpenChildrenError
		if !errors.As(err, &openChildren) {
			t.Fatalf("err is %T (%v), want *issueops.CloseOpenChildrenError", err, err)
		}
		if openChildren.OpenChildren != 4 || openChildren.IssueID != "ga-1" {
			t.Errorf("reconstructed %+v", openChildren)
		}
		if !errors.Is(err, issueops.ErrCloseOpenChildren) {
			t.Error("the wrapped sentinel does not match ErrCloseOpenChildren")
		}
	})

	t.Run("absent means a live blocker", func(t *testing.T) {
		err := refuseWith(t, 409, `{"status":409,"code":"not_closable","detail":"issue is blocked"}`, nil, Request{
			Op: OpCloseIssue, Method: http.MethodPost,
			Path: "/v0/beads/issues/ga-1:close", IssueID: "ga-1",
		})
		if !errors.Is(err, issueops.ErrCloseBlocked) {
			t.Fatalf("err = %v, want ErrCloseBlocked", err)
		}
		var openChildren *issueops.CloseOpenChildrenError
		if errors.As(err, &openChildren) {
			t.Error("a live-blocker refusal classified as open children")
		}
	})

	t.Run("zero is present, not absent", func(t *testing.T) {
		// The pointer is what carries presence: a count of zero must not read
		// as the other refusal.
		err := refuseWith(t, 409, `{"status":409,"code":"not_closable","open_children":0}`, nil, Request{
			Op: OpCloseIssue, Method: http.MethodPost, Path: "/v0/beads/issues/ga-1:close", IssueID: "ga-1",
		})
		if !errors.Is(err, issueops.ErrCloseOpenChildren) {
			t.Fatalf("err = %v, want ErrCloseOpenChildren", err)
		}
	})
}

func TestDependencyCycleUsesIssueIDPresenceAsTheDiscriminator(t *testing.T) {
	// The hierarchy refusal and the scheduling cycle share one code because they
	// share one recovery. The typed distinction rides on the extension members.
	t.Run("absent means a plain scheduling cycle", func(t *testing.T) {
		err := refuseWith(t, 409, `{"status":409,"code":"dependency_cycle","detail":"would create a cycle"}`, nil, Request{
			Op: OpAddDependencies, Method: http.MethodPost, Path: PathDependenciesAdd,
		})
		if !errors.Is(err, issueops.ErrDependencyCycle) {
			t.Fatalf("err = %v, want ErrDependencyCycle", err)
		}
		var hierarchy *issueops.DependencyHierarchyConflictError
		if errors.As(err, &hierarchy) {
			t.Error("a scheduling cycle classified as a hierarchy conflict")
		}
	})

	// Both polarities: the boolean is emitted when false, so absence never
	// means false and the reconstruction has to prove it in both directions.
	for _, ancestor := range []bool{true, false} {
		t.Run("hierarchy conflict, blocker_is_ancestor="+boolText(ancestor), func(t *testing.T) {
			body := `{"status":409,"code":"dependency_cycle","issue_id":"ga-child","blocker_id":"ga-parent","blocker_is_ancestor":` + boolText(ancestor) + `}`
			err := refuseWith(t, 409, body, nil, Request{
				Op: OpAddDependencies, Method: http.MethodPost, Path: PathDependenciesAdd,
			})
			var hierarchy *issueops.DependencyHierarchyConflictError
			if !errors.As(err, &hierarchy) {
				t.Fatalf("err is %T (%v), want *issueops.DependencyHierarchyConflictError", err, err)
			}
			want := &issueops.DependencyHierarchyConflictError{
				IssueID: "ga-child", BlockerID: "ga-parent", BlockerIsAncestor: ancestor,
			}
			if *hierarchy != *want {
				t.Errorf("reconstructed %+v, want %+v", hierarchy, want)
			}
			// The message is what `bd dep add` prints, and the two polarities
			// say opposite things.
			if hierarchy.Error() != want.Error() {
				t.Errorf("message = %q, want %q", hierarchy.Error(), want.Error())
			}
		})
	}
}

func TestDependencyExistsCarriesBothEdgeTypes(t *testing.T) {
	err := refuseWith(t, 409, `{"status":409,"code":"dependency_exists","existing_type":"blocks","requested_type":"related"}`, nil, Request{
		Op: OpAddDependencies, Method: http.MethodPost, Path: PathDependenciesAdd,
		IssueID: "ga-1", DependsOnID: "ga-2",
	})
	var conflict *issueops.DependencyTypeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err is %T (%v), want *issueops.DependencyTypeConflictError", err, err)
	}
	want := &issueops.DependencyTypeConflictError{
		IssueID: "ga-1", DependsOnID: "ga-2", ExistingType: "blocks", RequestedType: "related",
	}
	if *conflict != *want {
		t.Errorf("reconstructed %+v, want %+v", conflict, want)
	}
}

func TestUnknownCodesFallBackToTheStatusClass(t *testing.T) {
	// Adding a code is not a breaking change, so a client that hard-failed on an
	// unknown one would break against every server newer than itself.
	cases := []struct {
		name      string
		status    int
		body      string
		want      error
		retryable bool
	}{
		{"unknown 400", 400, `{"status":400,"code":"quota_exhausted"}`, ErrBadRequest, false},
		{"unknown 409", 409, `{"status":409,"code":"lease_conflict"}`, ErrBadRequest, false},
		{"unknown 429", 429, `{"status":429,"code":"too_many_requests"}`, ErrBadRequest, false},
		{"unknown 503", 503, `{"status":503,"code":"draining"}`, ErrBusy, true},
		{"unknown 500", 500, `{"status":500,"code":"panicked"}`, ErrServerFault, false},
		{"unknown 502 with no code at all", 502, `<html>bad gateway</html>`, ErrServerFault, false},
		{"a 504 from a proxy that is not a problem document", 504, ``, ErrServerFault, false},
		// A 401 is authentication whichever layer answered it. bd serve's own
		// carries a code; these two are what an edge in FRONT of it returns when
		// it rejects a credential before the request arrives — and the 4xx
		// default used to call both of them a bad request, sending an operator
		// to look at flags for a token to rotate.
		{"401 with no code at all", 401, `<html>401 Unauthorized</html>`, ErrUnauthenticated, false},
		{"401 from an edge with no body", 401, ``, ErrUnauthenticated, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseWith(t, tc.status, tc.body, nil, listIssues())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			var problem *ProblemError
			if !errors.As(err, &problem) {
				t.Fatalf("err is %T, want *ProblemError", err)
			}
			if problem.Retryable() != tc.retryable {
				t.Errorf("Retryable() = %v, want %v", problem.Retryable(), tc.retryable)
			}
		})
	}
}

func TestCodeIsTheOnlyDispatchKey(t *testing.T) {
	// A code carries one frozen status, so reading the status instead would be a
	// second, weaker copy of the table. A code arriving on the wrong status still
	// classifies by the code.
	err := refuseWith(t, http.StatusInternalServerError, `{"status":500,"code":"not_found"}`, nil, listIssues())
	if !errors.Is(err, issueops.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if errors.Is(err, ErrServerFault) {
		t.Error("the status class overrode a code this client knows")
	}
}

func TestRequestIDIsSurfacedOn5xx(t *testing.T) {
	// The 5xx detail is a fixed string per code by design, so the correlation id
	// is the client's only handle on the log line that has the real error.
	err := refuseWith(t, 500, `{"status":500,"code":"internal","detail":"internal server error","request_id":"01JABCDEF"}`, nil, listIssues())
	var problem *ProblemError
	if !errors.As(err, &problem) {
		t.Fatalf("err is %T, want *ProblemError", err)
	}
	if problem.RequestID != "01JABCDEF" {
		t.Errorf("RequestID = %q", problem.RequestID)
	}
	if got := problem.Error(); !strings.Contains(got, "01JABCDEF") {
		t.Errorf("Error() = %q, want the request_id in it", got)
	}

	// A 4xx's detail already reflects the caller's own input, so the id is
	// carried but does not clutter the sentence.
	err = refuseWith(t, 404, `{"status":404,"code":"not_found","detail":"no issue or wisp with that id","request_id":"01JXYZ"}`, nil, listIssues())
	if !errors.As(err, &problem) {
		t.Fatalf("err is %T, want *ProblemError", err)
	}
	if problem.RequestID != "01JXYZ" {
		t.Errorf("RequestID = %q", problem.RequestID)
	}
	if strings.Contains(problem.Error(), "01JXYZ") {
		t.Errorf("Error() = %q, want the 4xx sentence without the request_id", problem.Error())
	}
}

func TestRetryAfterIsParsedAndBounded(t *testing.T) {
	cases := []struct {
		name   string
		header string
		max    time.Duration
		want   time.Duration
	}{
		{"delta seconds", "5", 30 * time.Second, 5 * time.Second},
		{"the saturation value", "1", 30 * time.Second, time.Second},
		{"absent", "", 30 * time.Second, 0},
		{"garbage", "soon", 30 * time.Second, 0},
		{"negative", "-5", 30 * time.Second, 0},
		// Server-controlled input: an unbounded honor is a server parking the
		// caller's process for as long as it likes.
		{"past the bound is clamped, not dropped", "3600", 30 * time.Second, 30 * time.Second},
		{"a date in the past", "Mon, 02 Jan 2006 15:04:05 GMT", 30 * time.Second, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			if tc.header != "" {
				headers["Retry-After"] = tc.header
			}
			err := refuseWith(t, 503, `{"status":503,"code":"busy"}`, headers, listIssues(),
				func(o *Options) { o.MaxRetryAfter = tc.max })
			var problem *ProblemError
			if !errors.As(err, &problem) {
				t.Fatalf("err is %T, want *ProblemError", err)
			}
			if problem.RetryAfter != tc.want {
				t.Errorf("RetryAfter = %v, want %v", problem.RetryAfter, tc.want)
			}
			if !problem.Retryable() {
				t.Error("a 503 did not report as retryable")
			}
		})
	}
}

func TestARetryAfterDateInTheFutureIsHonoredAndBounded(t *testing.T) {
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	err := refuseWith(t, 503, `{"status":503,"code":"db_unavailable"}`,
		map[string]string{"Retry-After": future}, listIssues())
	var problem *ProblemError
	if !errors.As(err, &problem) {
		t.Fatalf("err is %T, want *ProblemError", err)
	}
	// Second-granularity formatting plus the round trip make an exact
	// comparison meaningless; the bound and the sign are the contract.
	if problem.RetryAfter <= 0 || problem.RetryAfter > DefaultMaxRetryAfter {
		t.Errorf("RetryAfter = %v, want a positive value at or under %v", problem.RetryAfter, DefaultMaxRetryAfter)
	}
}

func TestParamAndReasonSurviveForTheSkewTaxonomy(t *testing.T) {
	// The version-skew signal: a newer CLI flag against an older server. The
	// refusal taxonomy maps `param` back to the flag that produced it, so both
	// members have to arrive typed rather than as prose.
	err := refuseWith(t, 400,
		`{"status":400,"code":"invalid_argument","param":"sort","reason":"unknown_parameter","detail":"unknown parameter \"sort\""}`,
		nil, listIssues())
	var problem *ProblemError
	if !errors.As(err, &problem) {
		t.Fatalf("err is %T, want *ProblemError", err)
	}
	if problem.Param != "sort" || problem.Reason != "unknown_parameter" {
		t.Errorf("param/reason = %q/%q", problem.Param, problem.Reason)
	}
	if !errors.Is(err, issueops.ErrValidation) {
		t.Error("invalid_argument did not map to ErrValidation")
	}
}

// TestPreconditionFailuresCarryBothSidesOfTheGuard is the decode half of the
// compare-and-set wave: a 409 precondition_failed says WHICH guard missed in
// `param` and — where the refusing operation can report it — what the request
// asked for and what the row was found holding.
//
// Both halves have to survive the decode. `param` alone tells a caller which
// member to recompose; the expected/actual pair is what lets it recompose the
// member WITHOUT a second read, and dropping it would leave "re-read the row and
// try again" as the only recovery on a surface whose refusal already carried the
// answer.
//
// THE VERSIONS ARE DECIMAL STRINGS ON THE WIRE AND ARE CARRIED AS SUCH. Live
// tokens run past 5e17, where an IEEE-754 double's ulp is already 64, which is
// why upstream #6053 spells every revision token as a string
// (types.RevisionToken): a JSON number would hand a lossy consumer a value NEAR
// the token that is not it, and the corruption would show up as a
// precondition_failed on the NEXT request. The case drives a token in that
// range and pins that the strings come through verbatim — the ProblemError does
// not parse them back, because they are an echo and a diagnostic, not a value
// to compose the next guard from (see the field comment).
func TestPreconditionFailuresCarryBothSidesOfTheGuard(t *testing.T) {
	const expected = int64(576460752303423487) // 2^59 - 1: a float64 cannot hold it
	const actual = int64(576460752303423489)

	t.Run("version", func(t *testing.T) {
		body := fmt.Sprintf(`{"status":409,"code":"precondition_failed","param":"expected_version",`+
			`"expected_version":%q,"actual_version":%q}`, types.RevisionToken(expected), types.RevisionToken(actual))
		err := refuseWith(t, 409, body, nil, listIssues())

		var p *ProblemError
		if !errors.As(err, &p) {
			t.Fatalf("err is %T, want *ProblemError", err)
		}
		if !errors.Is(err, issueops.ErrVersionMismatch) {
			t.Errorf("param expected_version did not map to ErrVersionMismatch: %v", err)
		}
		if p.ExpectedVersion == nil || *p.ExpectedVersion != types.RevisionToken(expected) {
			t.Errorf("ExpectedVersion = %s, want %q verbatim", renderVersion(p.ExpectedVersion), types.RevisionToken(expected))
		}
		if p.ActualVersion == nil || *p.ActualVersion != types.RevisionToken(actual) {
			t.Errorf("ActualVersion = %s, want %q verbatim", renderVersion(p.ActualVersion), types.RevisionToken(actual))
		}
	})

	t.Run("status and assignee", func(t *testing.T) {
		body := `{"status":409,"code":"precondition_failed","param":"expected_assignee",` +
			`"expected_assignee":"","actual_assignee":"holder",` +
			`"expected_status":"open","actual_status":"in_progress"}`
		err := refuseWith(t, 409, body, nil, listIssues())

		var p *ProblemError
		if !errors.As(err, &p) {
			t.Fatalf("err is %T, want *ProblemError", err)
		}
		if !errors.Is(err, issueops.ErrAssigneeMismatch) {
			t.Errorf("param expected_assignee did not map to ErrAssigneeMismatch: %v", err)
		}
		// The EMPTY expected assignee is the guard that says "only if nobody
		// holds it", so its presence is load-bearing: a decode that collapsed it
		// into absence would report a refusal the request never made.
		if p.ExpectedAssignee == nil {
			t.Error("ExpectedAssignee is absent; the empty string is a real guard and the server echoed it")
		} else if *p.ExpectedAssignee != "" {
			t.Errorf("ExpectedAssignee = %q, want the empty guard", *p.ExpectedAssignee)
		}
		for member, got := range map[string]*string{
			"actual_assignee": p.ActualAssignee,
			"expected_status": p.ExpectedStatus,
			"actual_status":   p.ActualStatus,
		} {
			if got == nil {
				t.Errorf("%s was dropped by the decode", member)
			}
		}
	})

	t.Run("an operation that cannot report what it found says so by absence", func(t *testing.T) {
		// `actual_version` is present only where the refusing operation can
		// report it: an all-or-nothing operation rolls its transaction back, so
		// a value read afterwards would describe a row the refusal never saw.
		// Absence means "this server cannot tell you", never "it found zero".
		body := `{"status":409,"code":"precondition_failed","param":"expected_version","expected_version":"0"}`
		err := refuseWith(t, 409, body, nil, listIssues())

		var p *ProblemError
		if !errors.As(err, &p) {
			t.Fatalf("err is %T, want *ProblemError", err)
		}
		if p.ActualVersion != nil {
			t.Errorf("ActualVersion = %q for a refusal that reported none; absent is not zero", *p.ActualVersion)
		}
		if p.ExpectedVersion == nil {
			t.Fatal("ExpectedVersion is absent; \"0\" is a legal token and the server echoed it")
		}
		if *p.ExpectedVersion != "0" {
			t.Errorf("ExpectedVersion = %q, want the echoed \"0\"", *p.ExpectedVersion)
		}
	})

	t.Run("the strings are stripped like every other server-controlled field", func(t *testing.T) {
		body := `{"status":409,"code":"precondition_failed","param":"expected_status",` +
			`"expected_status":"open\u009b31m","actual_status":"done\u001b]0;x\u0007",` +
			`"expected_assignee":"a\u0000","actual_assignee":"b\u2028"}`
		err := refuseWith(t, 409, body, nil, listIssues())

		var p *ProblemError
		if !errors.As(err, &p) {
			t.Fatalf("err is %T, want *ProblemError", err)
		}
		for name, got := range map[string]*string{
			"ExpectedStatus": p.ExpectedStatus, "ActualStatus": p.ActualStatus,
			"ExpectedAssignee": p.ExpectedAssignee, "ActualAssignee": p.ActualAssignee,
		} {
			if got == nil {
				t.Errorf("%s was dropped by the decode", name)
				continue
			}
			if strings.ContainsFunc(*got, isControlRune) {
				t.Errorf("%s still carries a control rune: %q", name, *got)
			}
		}
	})
}

// renderVersion prints a row-version guard THROUGH the pointer, so a failure
// message shows the token rather than the address of one — which is the
// difference between reading a corrupted value and reading a heap pointer.
func renderVersion(v *string) string {
	if v == nil {
		return "absent"
	}
	return fmt.Sprintf("%q", *v)
}

// TestProblemDecodeStripsControlCharactersFromEveryField is the source-of-truth
// layer of the two-layer terminal-injection defense. A *ProblemError must leave
// the wire safe in EVERY server-controlled field — the direct ones AND the
// extension members the typed conflicts are reconstructed from — because many
// stderr sinks render it via %v with no display-layer strip (bd close/show/reopen/
// update write each per-id failure straight to os.Stderr). The earlier per-field
// strip covered only detail/title/param/reason; these cases pin code, request_id
// and the reconstructed conflict's issue ids too.
func TestProblemDecodeStripsControlCharactersFromEveryField(t *testing.T) {
	t.Run("direct fields including code and request_id", func(t *testing.T) {
		body := `{"status":500,"code":"internal\u009b31m","title":"t\u009b","detail":"boom\u001b]0;x\u0007","param":"sort\u0000","reason":"r\u2028x","request_id":"req-\u009b1"}`
		err := refuseWith(t, 500, body, nil, listIssues())

		var p *ProblemError
		if !errors.As(err, &p) {
			t.Fatalf("err is %T, want *ProblemError", err)
		}
		for name, got := range map[string]string{
			"Code": p.Code, "Title": p.Title, "Detail": p.Detail,
			"Param": p.Param, "Reason": p.Reason, "RequestID": p.RequestID,
		} {
			if strings.ContainsFunc(got, isControlRune) {
				t.Errorf("%s still carries a control rune: %q", name, got)
			}
		}
		// The whole rendered error is what a %v sink prints; it must be clean.
		if strings.ContainsFunc(err.Error(), isControlRune) {
			t.Errorf("ProblemError.Error() carries a control rune: %q", err.Error())
		}
	})

	t.Run("reconstructed dependency-cycle issue ids", func(t *testing.T) {
		body := `{"status":409,"code":"dependency_cycle","issue_id":"bd-1\u009b31m","blocker_id":"bd-2\u001b]0;x\u0007","blocker_is_ancestor":true}`
		err := refuseWith(t, 409, body, nil, listIssues())

		var hc *issueops.DependencyHierarchyConflictError
		if !errors.As(err, &hc) {
			t.Fatalf("err is %T, want *DependencyHierarchyConflictError", err)
		}
		if strings.ContainsFunc(hc.IssueID, isControlRune) || strings.ContainsFunc(hc.BlockerID, isControlRune) {
			t.Errorf("reconstructed conflict carries a control rune: issue=%q blocker=%q", hc.IssueID, hc.BlockerID)
		}
		if strings.ContainsFunc(hc.Error(), isControlRune) {
			t.Errorf("conflict Error() carries a control rune: %q", hc.Error())
		}
	})
}

// TestHandshakeErrorsStripControlCharacters is the handshake-family half of the
// decode-layer strip. CapabilityError and ProjectMismatchError are built in
// handshake.go, NOT through mapProblem, so they carry their own source-layer strip:
// a hostile server's bd_version or repo_root must not reach a per-id write sink
// (bd close/reopen/update) with a live escape sequence in it.
func TestHandshakeErrorsStripControlCharacters(t *testing.T) {
	t.Run("CapabilityError bd_version", func(t *testing.T) {
		// Advertises only issues.list; bd close needs issues.close, and the
		// bd_version carries a CSI. Pre-flighting closeIssue forces the refusal.
		body := `{"api_version":"v0","backend":"dolt","bd_version":"9.9\u009b31m","beads_dir":"/srv/.beads","capabilities":["issues.list"],"database":"beads","dolt_mode":"embedded","project_id":"p","repo_root":"/srv","schema_version":7}`
		c, _ := newTestClient(t, Options{}, nil, serveContext(body))
		err := c.Preflight(ctx(t), OpCloseIssue)

		var capErr *CapabilityError
		if !errors.As(err, &capErr) {
			t.Fatalf("err is %T (%v), want *CapabilityError", err, err)
		}
		if strings.ContainsFunc(capErr.BdVersion, isControlRune) {
			t.Errorf("BdVersion carries a control rune: %q", capErr.BdVersion)
		}
		if strings.ContainsFunc(err.Error(), isControlRune) {
			t.Errorf("CapabilityError.Error() carries a control rune: %q", err.Error())
		}
	})

	t.Run("ProjectMismatchError repo_root, database and got", func(t *testing.T) {
		// The pinned id differs from the server's, so the handshake refuses with a
		// wrong-server diagnostic whose repo_root/database/project_id carry escapes.
		body := `{"api_version":"v0","backend":"dolt","bd_version":"1.1.0","beads_dir":"/srv/.beads","capabilities":["issues.list"],"database":"db\u009bX","dolt_mode":"embedded","project_id":"other\u001b]0;x\u0007","repo_root":"/srv\u009b/repo","schema_version":7}`
		c, _ := newTestClient(t, Options{ExpectProjectID: "mine"}, nil, serveContext(body))
		_, err := c.Handshake(ctx(t))

		var pm *ProjectMismatchError
		if !errors.As(err, &pm) {
			t.Fatalf("err is %T (%v), want *ProjectMismatchError", err, err)
		}
		for name, got := range map[string]string{"Got": pm.Got, "Database": pm.Database, "RepoRoot": pm.RepoRoot} {
			if strings.ContainsFunc(got, isControlRune) {
				t.Errorf("%s carries a control rune: %q", name, got)
			}
		}
		if strings.ContainsFunc(err.Error(), isControlRune) {
			t.Errorf("ProjectMismatchError.Error() carries a control rune: %q", err.Error())
		}
	})
}

func TestARefusalNeverEchoesTheCredential(t *testing.T) {
	const token = "s3cr3t-bearer-token"
	creds := &staticToken{tokens: []string{token}}
	c, _ := newTestClient(t, Options{}, creds, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		problemJSON(w, 401, `{"status":401,"code":"unauthenticated","detail":"missing or invalid bearer token","request_id":"r"}`)
	})
	err := c.Do(ctx(t), listIssues(), &struct{}{})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("the token survived into the error text: %v", err)
	}
}

func TestTheProblemBodyIsReadWhateverTheContentType(t *testing.T) {
	// A proxy that rewrites Content-Type must not cost the client its typed
	// classification; `code` is what dispatches, not the media type.
	c, _ := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"status":404,"code":"not_found"}`)
	})
	if err := c.Do(ctx(t), listIssues(), &struct{}{}); !errors.Is(err, issueops.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestBatchItemMembersSurviveTheDecode is the batch-apply half of the guard
// pair above, and it exists because those members are the ONLY place the
// offender exists on an all-or-nothing operation: the request either applied
// every item or none, so there is no per-item result array a client could find
// it in.
//
// PRESENCE IS LOAD-BEARING ON ALL FIVE, in two different directions.
// `item_index` 0 is the first item and a real answer, so a decode that modelled
// it as a bare int could not tell "the first item refused" from "no item was
// named". `item_key` and `item_issue_id` are absent for real states rather than
// gaps — not every item has a key, and a create whose id was never minted has
// no id to report. And `declared_later` is emitted in BOTH polarities, so its
// absence says the refusal was not about a key at all rather than saying false.
func TestBatchItemMembersSurviveTheDecode(t *testing.T) {
	t.Run("every member present, including a zero index", func(t *testing.T) {
		body := `{"status":409,"code":"precondition_failed","param":"items[0].update.expected_version",` +
			`"item_index":0,"item_kind":"update","item_key":"root","item_issue_id":"bd-7"}`
		err := refuseWith(t, 409, body, nil, listIssues())

		var p *ProblemError
		if !errors.As(err, &p) {
			t.Fatalf("err is %T, want *ProblemError", err)
		}
		if p.ItemIndex == nil {
			t.Fatal("ItemIndex is absent; 0 is the first item and a real answer")
		}
		if *p.ItemIndex != 0 {
			t.Errorf("ItemIndex = %d, want 0", *p.ItemIndex)
		}
		for name, got := range map[string]struct {
			have *string
			want string
		}{
			"ItemKind":    {p.ItemKind, "update"},
			"ItemKey":     {p.ItemKey, "root"},
			"ItemIssueID": {p.ItemIssueID, "bd-7"},
		} {
			if got.have == nil {
				t.Errorf("%s was dropped by the decode", name)
				continue
			}
			if *got.have != got.want {
				t.Errorf("%s = %q, want %q", name, *got.have, got.want)
			}
		}
	})

	t.Run("an unnamed item leaves the two optional members absent", func(t *testing.T) {
		body := `{"status":400,"code":"invalid_argument","param":"items[2]","item_index":2,"item_kind":"dep_add"}`
		err := refuseWith(t, 400, body, nil, listIssues())

		var p *ProblemError
		if !errors.As(err, &p) {
			t.Fatalf("err is %T, want *ProblemError", err)
		}
		if p.ItemKey != nil {
			t.Errorf("ItemKey = %q for an item that named nothing symbolically; absent is a real state here", *p.ItemKey)
		}
		if p.ItemIssueID != nil {
			t.Errorf("ItemIssueID = %q for a refusal that resolved no id", *p.ItemIssueID)
		}
	})

	t.Run("declared_later is read in both polarities", func(t *testing.T) {
		for _, tc := range []struct {
			body string
			want *bool
		}{
			{`{"status":400,"code":"invalid_argument","item_index":1,"item_key":"late","declared_later":true}`, problemBoolPtr(true)},
			{`{"status":400,"code":"invalid_argument","item_index":1,"item_key":"typo","declared_later":false}`, problemBoolPtr(false)},
			{`{"status":400,"code":"invalid_argument","param":"limit"}`, nil},
		} {
			err := refuseWith(t, 400, tc.body, nil, listIssues())
			var p *ProblemError
			if !errors.As(err, &p) {
				t.Fatalf("err is %T, want *ProblemError", err)
			}
			switch {
			case tc.want == nil && p.DeclaredLater != nil:
				t.Errorf("DeclaredLater = %v on a refusal that was not about a key; absence is the third state",
					*p.DeclaredLater)
			case tc.want != nil && p.DeclaredLater == nil:
				t.Error("DeclaredLater was dropped; it is emitted in both polarities and never omitted to mean false")
			case tc.want != nil && *p.DeclaredLater != *tc.want:
				t.Errorf("DeclaredLater = %v, want %v", *p.DeclaredLater, *tc.want)
			}
		}
	})

	t.Run("the strings are stripped like every other server-controlled field", func(t *testing.T) {
		body := `{"status":400,"code":"invalid_argument","item_index":0,` +
			`"item_kind":"create\u009b31m","item_key":"k\u0000","item_issue_id":"bd-1\u2028"}`
		err := refuseWith(t, 400, body, nil, listIssues())

		var p *ProblemError
		if !errors.As(err, &p) {
			t.Fatalf("err is %T, want *ProblemError", err)
		}
		for name, got := range map[string]*string{
			"ItemKind": p.ItemKind, "ItemKey": p.ItemKey, "ItemIssueID": p.ItemIssueID,
		} {
			if got == nil {
				t.Errorf("%s was dropped by the decode", name)
				continue
			}
			if strings.ContainsFunc(*got, isControlRune) {
				t.Errorf("%s still carries a control rune: %q", name, *got)
			}
		}
	})
}

func problemBoolPtr(b bool) *bool { return &b }
