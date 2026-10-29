package httpapi

import (
	"errors"
	"net/http"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
)

// Config.RequestContext lets an embedder attach per-request metadata asserted
// by a trusted fronting proxy (or refuse a request) after authorization — for
// example a request-scoped trace span, or a caller label the proxy put in a
// header. bd serve sets no hook; the field exists for programs that embed this
// server.
//
// The contract:
//
//   - The hook runs once for EVERY routed request: after the Host check and,
//     on a route that requires one, the bearer check; and before the project
//     stamp, the wire-revision check, the concurrency slot and the handler. A
//     request refused earlier, or one that matches no route, never reaches it.
//   - It also runs for the routes exempt from the bearer check (the liveness
//     probe) and, on a server configured without bearer authentication, for
//     every route — in both cases with no credential checked at all. A hook
//     must not read "it reached me" as "it was authorized", and should pass a
//     request it has nothing to say about through unchanged.
//   - The context it returns MUST be derived from r.Context(): that context
//     carries the request deadline and the per-request record the log line is
//     written from. A context that is not derived from it is an embedder bug,
//     and the request is answered 500 rather than run without them.
//   - The returned context is the one handlers, roles and stores receive.
//     Work a handler detaches with context.WithoutCancel keeps its values.
//   - A non-nil error refuses the request in the standard problem envelope; see
//     RequestRefusal. The error's own text never reaches the wire — a plain
//     error is answered with a fixed body and its text goes to the server log.

// codeRequestRefused is the code a RequestContext refusal carries when the
// hook did not choose one. It is not part of the v0 vocabulary bd serve
// documents: only an embedder that sets the hook can produce it, and that
// embedder owns documenting it to its clients.
const codeRequestRefused = "request_refused"

// RequestRefusal is an error a RequestContext hook returns to choose the
// refusal status and the code/message rendered in the standard error envelope.
// It is matched with errors.As, so it may be wrapped.
//
// Status must be a 4xx or 5xx; any other value is answered as 403. An empty
// Code is answered as "request_refused". Message becomes the envelope's
// `detail` and is sent to the client verbatim, so it must not carry anything
// the caller may not see; an empty Message omits `detail`.
type RequestRefusal struct {
	Status  int
	Code    string
	Message string
}

// Error describes the refusal for logs.
func (e *RequestRefusal) Error() string {
	code := e.Code
	if code == "" {
		code = codeRequestRefused
	}
	if e.Message == "" {
		return "request refused: " + code
	}
	return "request refused: " + code + ": " + e.Message
}

// applyRequestContext runs Config.RequestContext for one request. It returns
// the request carrying the hook's context and true when the handler may run,
// or writes the refusal itself and returns false.
func (s *Server) applyRequestContext(w http.ResponseWriter, r *http.Request, rec *reqInfo) (*http.Request, bool) {
	ctx, err := s.cfg.RequestContext(r)
	if err != nil {
		s.event("request_refused", "request_id", rec.id, "op", rec.op, "error", err.Error())
		s.fail(w, r, requestRefusalResult(err))
		return r, false
	}
	if ctx == nil || requestInfo(ctx) != rec {
		s.failErr(w, r, errors.New("RequestContext returned a context not derived from the request's"))
		return r, false
	}
	return r.WithContext(ctx), true
}

// requestRefusalResult builds the problem response for a hook's error.
func requestRefusalResult(err error) Result {
	status, code, detail := http.StatusForbidden, codeRequestRefused, ""
	var refusal *RequestRefusal
	if errors.As(err, &refusal) {
		if refusal.Status >= 400 && refusal.Status <= 599 {
			status = refusal.Status
		}
		if refusal.Code != "" {
			code = refusal.Code
		}
		detail = refusal.Message
	}
	p := apigen.Problem{
		Status: status,
		Title:  http.StatusText(status),
		Code:   code,
	}
	if detail != "" {
		p.Detail = &detail
	}
	return Result{Problem: p}
}
