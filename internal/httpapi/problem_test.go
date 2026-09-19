package httpapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
)

// dsn is the shape a driver or dial error takes in the wild: it names the
// database user, host and port. No 5xx body may echo any of it.
const dsn = "dial tcp 127.0.0.1:3306: connect: connection refused (bd_user@tcp(127.0.0.1:3306)/beads)"

func TestProblemMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   Code
		retryAfter int
	}{
		{
			name:       "not found sentinel",
			err:        storage.ErrNotFound,
			wantStatus: http.StatusNotFound,
			wantCode:   CodeNotFound,
		},
		{
			name:       "wrapped not found sentinel",
			err:        fmt.Errorf("get issue bd-1: %w", storage.ErrNotFound),
			wantStatus: http.StatusNotFound,
			wantCode:   CodeNotFound,
		},
		{
			// The real seam shape: the SQL repository returns a bare
			// sql.ErrNoRows, which the shared read path normalizes. This row
			// is the defense-in-depth half — a missing issue must never
			// surface as a 500.
			name:       "wrapped sql.ErrNoRows",
			err:        fmt.Errorf("select issue: %w", sql.ErrNoRows),
			wantStatus: http.StatusNotFound,
			wantCode:   CodeNotFound,
		},
		{
			name:       "already claimed",
			err:        storage.ErrAlreadyClaimed,
			wantStatus: http.StatusConflict,
			wantCode:   CodeAlreadyClaimed,
		},
		{
			// The CLI reconstructs the holder from this message fragment; the
			// wire must not, which is why the extension members come from a
			// same-transaction read instead.
			name:       "already claimed with the CLI message fragment",
			err:        fmt.Errorf("%w%salice", storage.ErrAlreadyClaimed, storage.ClaimedByFragment),
			wantStatus: http.StatusConflict,
			wantCode:   CodeAlreadyClaimed,
		},
		{
			name:       "not claimable",
			err:        fmt.Errorf("%w%sclosed", storage.ErrNotClaimable, storage.NotClaimableStatusFragment),
			wantStatus: http.StatusConflict,
			wantCode:   CodeNotClaimable,
		},
		{
			name:       "in-flight limiter timed out",
			err:        fmt.Errorf("acquire slot: %w", ErrBusy),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   CodeBusy,
			retryAfter: retryAfterSaturation,
		},
		{
			name:       "serialization retry budget exhausted (deadlock)",
			err:        fmt.Errorf("claim %s: %w", dsn, &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   CodeBusy,
			retryAfter: retryAfterContention,
		},
		{
			name:       "serialization retry budget exhausted (lock wait timeout)",
			err:        &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   CodeBusy,
			retryAfter: retryAfterContention,
		},
		{
			name:       "dial failure",
			err:        fmt.Errorf("new uow: %w", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   CodeDBUnavailable,
			retryAfter: retryAfterContention,
		},
		{
			name:       "bad connection",
			err:        fmt.Errorf("query %s: %w", dsn, driver.ErrBadConn),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   CodeDBUnavailable,
			retryAfter: retryAfterContention,
		},
		{
			name:       "invalid connection",
			err:        fmt.Errorf("query %s: %w", dsn, mysql.ErrInvalidConn),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   CodeDBUnavailable,
			retryAfter: retryAfterContention,
		},
		{
			// context.DeadlineExceeded satisfies net.Error. A tripped
			// per-request deadline is a plain 500, not a claim that the
			// database is unreachable.
			name:       "request deadline",
			err:        fmt.Errorf("list issues: %w", context.DeadlineExceeded),
			wantStatus: http.StatusInternalServerError,
			wantCode:   CodeInternal,
		},
		{
			name:       "client went away",
			err:        fmt.Errorf("list issues: %w", context.Canceled),
			wantStatus: http.StatusInternalServerError,
			wantCode:   CodeInternal,
		},
		{
			// The load-bearing negative: an arbitrary error is a 500, never a
			// 404. "Every error is a not-found" is the failure this mapping
			// exists to prevent.
			name:       "arbitrary error",
			err:        fmt.Errorf("scan row: %s: %w", dsn, errors.New("unexpected column count")),
			wantStatus: http.StatusInternalServerError,
			wantCode:   CodeInternal,
		},
		{
			name:       "nil error",
			err:        nil,
			wantStatus: http.StatusInternalServerError,
			wantCode:   CodeInternal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := ClassifyError(tc.err)

			if res.Problem.Status != tc.wantStatus {
				t.Errorf("status = %d, want %d", res.Problem.Status, tc.wantStatus)
			}
			if res.Problem.Code != string(tc.wantCode) {
				t.Errorf("code = %q, want %q", res.Problem.Code, tc.wantCode)
			}
			if res.RetryAfterSeconds != tc.retryAfter {
				t.Errorf("Retry-After = %d, want %d", res.RetryAfterSeconds, tc.retryAfter)
			}

			rec := httptest.NewRecorder()
			Write(rec, res)

			if rec.Code != tc.wantStatus {
				t.Errorf("written status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/problem+json; charset=utf-8" {
				t.Errorf("Content-Type = %q, want application/problem+json", got)
			}
			wantRetry := ""
			if tc.retryAfter > 0 {
				wantRetry = strconv.Itoa(tc.retryAfter)
			}
			if got := rec.Header().Get("Retry-After"); got != wantRetry {
				t.Errorf("Retry-After header = %q, want %q", got, wantRetry)
			}

			body := rec.Body.String()
			var decoded map[string]any
			if err := json.Unmarshal([]byte(body), &decoded); err != nil {
				t.Fatalf("body is not JSON: %v (%s)", err, body)
			}
			if decoded["title"] != http.StatusText(tc.wantStatus) {
				t.Errorf("title = %v, want %q", decoded["title"], http.StatusText(tc.wantStatus))
			}

			if tc.wantStatus < 500 {
				return
			}
			// 5xx detail scrubbing: the wire carries a fixed string per code
			// and the real error goes to the log only. Driver and dial errors
			// embed the DSN, and this binary can be bound beyond loopback.
			if got := decoded["detail"]; got != staticDetail[tc.wantCode] {
				t.Errorf("detail = %v, want the static %q", got, staticDetail[tc.wantCode])
			}
			for _, leak := range []string{"127.0.0.1", "3306", "bd_user", "tcp", "connection refused", "Deadlock", "unexpected column count"} {
				if strings.Contains(body, leak) {
					t.Errorf("5xx body leaks %q from the underlying error: %s", leak, body)
				}
			}
		})
	}
}

func TestInvalidArgumentCarriesParamAndReason(t *testing.T) {
	// A 400 must be machine-attributable without parsing prose: `param` names
	// the offending input and `reason` separates "this server does not know
	// that parameter" (version skew) from "your value is malformed" (a client
	// bug). Substring-matching error text is the pathology this API deletes.
	res := InvalidArgument("mol_type", ReasonUnknownParameter, `unknown query parameter "mol_type"`)

	if res.Problem.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Problem.Status)
	}
	if res.Problem.Code != string(CodeInvalidArgument) {
		t.Errorf("code = %q, want %q", res.Problem.Code, CodeInvalidArgument)
	}
	if res.Problem.Param == nil || *res.Problem.Param != "mol_type" {
		t.Errorf("param = %v, want mol_type", res.Problem.Param)
	}
	if res.Problem.Reason == nil || *res.Problem.Reason != string(ReasonUnknownParameter) {
		t.Errorf("reason = %v, want %q", res.Problem.Reason, ReasonUnknownParameter)
	}
	// 4xx details reflect the caller's own input back and stay specific.
	if res.Problem.Detail == nil || !strings.Contains(*res.Problem.Detail, "mol_type") {
		t.Errorf("detail = %v, want the offending parameter named", res.Problem.Detail)
	}

	if got := InvalidCursor(); got.Problem.Code != string(CodeInvalidCursor) || got.Problem.Status != http.StatusBadRequest {
		t.Errorf("InvalidCursor = %d/%q, want 400/%q", got.Problem.Status, got.Problem.Code, CodeInvalidCursor)
	}
}

func TestConflictExtensions(t *testing.T) {
	// The 409 extension members are populated from a read in the claim's own
	// transaction — never by parsing the sentinel's message fragments.
	res := ClassifyError(storage.ErrAlreadyClaimed).WithAssignee("alice").WithIssueStatus("in_progress")

	if res.Problem.Assignee == nil || *res.Problem.Assignee != "alice" {
		t.Errorf("assignee = %v, want alice", res.Problem.Assignee)
	}
	if res.Problem.IssueStatus == nil || *res.Problem.IssueStatus != "in_progress" {
		t.Errorf("issue_status = %v, want in_progress", res.Problem.IssueStatus)
	}

	rec := httptest.NewRecorder()
	Write(rec, res)
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if decoded["assignee"] != "alice" || decoded["issue_status"] != "in_progress" {
		t.Errorf("extension members missing from the wire body: %v", decoded)
	}
}

func TestCodeVocabularyIsFrozen(t *testing.T) {
	// Every code the operation table can emit must have a frozen status, and
	// nothing may sit in the vocabulary that no operation can produce: a
	// documented status+code pair is permanent wire surface.
	reachable := map[Code]bool{}
	for op, codes := range operationCodes {
		for _, c := range codes {
			if c.Status() == 0 {
				t.Errorf("%s: code %q has no frozen status", op, c)
			}
			reachable[c] = true
		}
	}
	for c := range codeStatus {
		if !reachable[c] {
			t.Errorf("code %q is in the vocabulary but no operation can emit it", c)
		}
	}
}
