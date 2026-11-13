// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/project_stamp_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// The client half of ga-b8ddd.12: the per-request Bd-Project-Id stamp, its
// validation at open, and the wrong-server refusal it earns. These are pure
// httptest cases like the rest of the wire suite, so the whole branch matrix —
// stamped/unstamped, both retry attempts, valid/invalid id, the reason arm and
// its control-rune strip — is owned at PR cadence.

const stampExpectID = "proj-workspace-alpha"

// TestEveryRequestCarriesTheProjectStamp is the headline of the stamping half:
// the id rides on the shared roundTrip, so it must appear on a baseline read, the
// identity handshake and a write alike — the point of ga-b8ddd.12 is that even the
// five unpinned baseline reads now carry it.
func TestEveryRequestCarriesTheProjectStamp(t *testing.T) {
	c, rec := newTestClient(t, Options{ExpectProjectID: stampExpectID}, nil, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	for _, r := range []Request{
		{Op: OpListIssues, Method: http.MethodGet, Path: PathIssues},
		{Op: OpGetContext, Method: http.MethodGet, Path: PathContext},
		{Op: OpClaimIssue, Method: http.MethodPost, Path: "/v0/beads/issues/x:claim", Body: map[string]string{"actor": "a"}, IssueID: "x"},
	} {
		if err := c.Do(ctx(t), r, &struct{}{}); err != nil {
			t.Fatalf("%s: %v", r.Op, err)
		}
	}
	if rec.count() != 3 {
		t.Fatalf("made %d requests, want 3", rec.count())
	}
	for i := 0; i < rec.count(); i++ {
		if got := rec.at(t, i).header.Get(ProjectIDHeader); got != stampExpectID {
			t.Errorf("request %d (%s) stamped %q, want %q", i, rec.at(t, i).method, got, stampExpectID)
		}
	}
}

// TestNoExpectedProjectIDSendsNoStamp is the backward-compatibility contract: a
// workspace that pinned no id (the tip OSS server's loopback-trust posture) sends
// no header at all, so an older server is addressed exactly as before.
func TestNoExpectedProjectIDSendsNoStamp(t *testing.T) {
	c, rec := newTestClient(t, Options{}, nil, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.Do(ctx(t), Request{Op: OpListIssues, Method: http.MethodGet, Path: PathIssues}, &struct{}{}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if _, present := rec.at(t, 0).header[http.CanonicalHeaderKey(ProjectIDHeader)]; present {
		t.Errorf("an unpinned client sent %s: %q", ProjectIDHeader, rec.at(t, 0).header.Get(ProjectIDHeader))
	}
}

// TestBothAttemptsOfThe401RetryCarryTheStamp pins the stamp onto the replay, not
// just the first attempt: the retry rebuilds the request after a credential
// refresh, and the stamp is a property of the request.
func TestBothAttemptsOfThe401RetryCarryTheStamp(t *testing.T) {
	creds := &staticToken{tokens: []string{"stale", "fresh"}, retry: true}
	var n int
	c, rec := newTestClient(t, Options{ExpectProjectID: stampExpectID}, creds, func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n == 1 {
			problemJSON(w, http.StatusUnauthorized, `{"status":401,"code":"unauthenticated"}`)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.Do(ctx(t), Request{Op: OpListIssues, Method: http.MethodGet, Path: PathIssues}, &struct{}{}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if rec.count() != 2 {
		t.Fatalf("made %d requests, want 2 (the 401 and its retry)", rec.count())
	}
	for i := 0; i < 2; i++ {
		if got := rec.at(t, i).header.Get(ProjectIDHeader); got != stampExpectID {
			t.Errorf("attempt %d stamped %q, want %q", i, got, stampExpectID)
		}
	}
}

// TestNewRefusesAnUnstampableProjectID is the validate-at-open contract: a pinned
// id that cannot be a header value is refused LOUDLY at New, never stamped-and-
// hoped or silently skipped. The ids are built with string(rune(...)) so the
// control runes are unambiguous in source. The C1 (U+009B) and line-separator
// (U+2028) cases are the ones the byte-level httpguts check misses and the
// rune-level isControlRune check catches, which is why both layers are required.
func TestNewRefusesAnUnstampableProjectID(t *testing.T) {
	base := mustParseURL(t, "http://127.0.0.1:1/")
	for _, tc := range []struct {
		name string
		bad  rune
	}{
		{"NUL", 0x00},
		{"escape introducer", 0x1b},
		{"newline", 0x0a},
		{"C1 CSI the byte check misses", 0x9b},
		{"line separator", 0x2028},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "proj" + string(tc.bad) + "id"
			c, err := New(base, nil, Options{ExpectProjectID: id})
			if err == nil {
				t.Fatalf("New accepted an unstampable id %q (client %v)", id, c.BaseURL())
			}
			if !errors.Is(err, ErrInvalidProjectID) {
				t.Fatalf("err = %v, want ErrInvalidProjectID", err)
			}
			var invalid *InvalidProjectIDError
			if !errors.As(err, &invalid) {
				t.Fatalf("err is %T, want *InvalidProjectIDError", err)
			}
			// The bad bytes are the reason it is invalid; they must not reach a
			// terminal live. %q rendering escapes them.
			if strings.ContainsFunc(invalid.Error(), isControlRune) {
				t.Errorf("the refusal leaked a live control rune: %q", invalid.Error())
			}
		})
	}
	// A clean id opens fine — the check is a gate, not a wall.
	if _, err := New(base, nil, Options{ExpectProjectID: "proj-ok-123"}); err != nil {
		t.Errorf("New refused a valid id: %v", err)
	}
}

// mismatchBody builds the wire body for a project_mismatch refusal, JSON-encoding
// serverProjectID so any control runes in it are properly escaped on the wire —
// exactly as the server's own encoder would emit them.
func mismatchBody(t *testing.T, serverProjectID string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"status":            400,
		"code":              "invalid_argument",
		"param":             ProjectIDHeader,
		"reason":            ReasonProjectMismatch,
		"server_project_id": serverProjectID,
		"detail":            "the stamp names a project this server does not serve",
	})
	if err != nil {
		t.Fatalf("marshal the mismatch body: %v", err)
	}
	return string(raw)
}

// TestAProjectMismatchMapsToTheTypedWrongServerError is the reason-discriminated
// arm: a 400 whose reason is project_mismatch becomes *ProjectMismatchError with
// both ids, not the generic ErrValidation the invalid_argument code would map to.
func TestAProjectMismatchMapsToTheTypedWrongServerError(t *testing.T) {
	const expected = "proj-workspace-beta"
	const serverOwns = "proj-the-other-one"
	err := refuseWith(t, 400, mismatchBody(t, serverOwns), nil, listIssues(), func(o *Options) { o.ExpectProjectID = expected })

	if !errors.Is(err, ErrProjectMismatch) {
		t.Fatalf("err = %v, want ErrProjectMismatch", err)
	}
	var mismatch *ProjectMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err is %T, want *ProjectMismatchError", err)
	}
	if mismatch.Expected != expected {
		t.Errorf("Expected = %q, want the workspace's %q", mismatch.Expected, expected)
	}
	if mismatch.Got != serverOwns {
		t.Errorf("Got = %q, want the server's %q", mismatch.Got, serverOwns)
	}
	// Database/RepoRoot are deliberately empty on the per-request path: this
	// refusal discloses only the server's id, not its location.
	if mismatch.Database != "" || mismatch.RepoRoot != "" {
		t.Errorf("the per-request mismatch disclosed a location: db=%q root=%q", mismatch.Database, mismatch.RepoRoot)
	}
	// The reason arm ran ahead of the code table, so this did NOT fall through to
	// the invalid_argument sentinel.
	if errors.Is(err, issueops.ErrValidation) {
		t.Errorf("the wrong-server refusal classified as a plain validation error")
	}
}

// TestTheServerProjectIDIsStrippedAtTheWireBoundary: server_project_id is
// server-controlled, so its control runes are removed where every other problem
// field's are — before the typed error can reach a %v stderr sink.
func TestTheServerProjectIDIsStrippedAtTheWireBoundary(t *testing.T) {
	dirty := "evil" + string(rune(0x9b)) + "31m" + string(rune(0x1b)) + "]0;x" + string(rune(0x07))
	err := refuseWith(t, 400, mismatchBody(t, dirty), nil, listIssues(), func(o *Options) { o.ExpectProjectID = "proj-clean" })

	var mismatch *ProjectMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err is %T, want *ProjectMismatchError", err)
	}
	if strings.ContainsFunc(mismatch.Got, isControlRune) {
		t.Errorf("Got still carries a control rune: %q", mismatch.Got)
	}
	if strings.ContainsFunc(mismatch.Error(), isControlRune) {
		t.Errorf("the rendered wrong-server error carries a control rune: %q", mismatch.Error())
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}
