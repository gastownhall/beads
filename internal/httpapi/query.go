package httpapi

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// query decodes one request's query string against the operation's parameter
// table, and is the single place the document's unknown-parameter rule is
// enforced for an operation that HAS parameters.
//
// The rule is strict on purpose. Silently ignoring an unrecognized FILTER
// parameter WIDENS the result set, so a client one version ahead of the server
// would receive — and act on — rows it believed it had filtered out. It is
// also a client's only per-parameter capability probe, since `capabilities` is
// operation-level.
//
// Every accessor records the name it read, so the allowlist is the parameter
// table itself rather than a second copy of it that can drift: a parameter the
// handler never asks for is, by construction, one this server does not know.
type query struct {
	values url.Values
	read   map[string]bool
	// res holds the FIRST refusal. First rather than last so the answer does
	// not depend on the order the handler happens to read its parameters in;
	// `param` is what a client dispatches on.
	res *Result
}

func newQuery(values url.Values) *query {
	return &query{values: values, read: map[string]bool{}}
}

// refuse records a refusal unless one is already recorded.
func (q *query) refuse(res Result) {
	if q.res == nil {
		q.res = &res
	}
}

func (q *query) invalid(param, detail string) {
	q.refuse(InvalidArgument(param, ReasonInvalidValue, detail))
}

// has reports whether the parameter was supplied, and marks it known.
func (q *query) has(name string) bool {
	q.read[name] = true
	return len(q.values[name]) > 0
}

// str reads a single-valued string parameter. A repeated parameter is refused
// rather than silently resolved to one of its values.
func (q *query) str(name string) string {
	if !q.has(name) {
		return ""
	}
	vals := q.values[name]
	if len(vals) > 1 {
		q.invalid(name, "this parameter takes a single value")
		return ""
	}
	return vals[0]
}

// list reads a repeatable parameter, returning the values as supplied. Comma
// splitting, where the document allows it, happens in the filter builder that
// owns the vocabulary — not here.
func (q *query) list(name string) []string {
	q.read[name] = true
	return q.values[name]
}

// csv reads a repeatable parameter whose values the builder parses as one
// comma-separated selector.
func (q *query) csv(name string) string {
	vals := q.list(name)
	if len(vals) == 0 {
		return ""
	}
	return strings.Join(vals, ",")
}

// boolean reads an optional boolean, defaulting to false as the document
// documents for every flag on this surface.
func (q *query) boolean(name string) bool {
	raw := q.str(name)
	if raw == "" {
		return false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		q.invalid(name, fmt.Sprintf("%q is not a boolean", raw))
		return false
	}
	return v
}

// integer reads an optional integer, returning nil when absent so a caller can
// tell "unset" from an explicit value.
func (q *query) integer(name string) *int {
	raw := q.str(name)
	if raw == "" {
		return nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		q.invalid(name, fmt.Sprintf("%q is not an integer", raw))
		return nil
	}
	return &v
}

// integer64 reads an optional 64-bit integer, returning nil when absent.
//
// It is separate from integer above because `int` is 32 bits on a 32-bit build,
// and the one parameter that needs this — a journal sequence number — is
// declared int64 in the document and int64 in storage. Parsing it through Atoi
// would make a checkpoint past 2^31 unreadable on some builds and readable on
// others, which is the kind of divergence a consumer discovers years later.
func (q *query) integer64(name string) *int64 {
	raw := q.str(name)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		q.invalid(name, fmt.Sprintf("%q is not a 64-bit integer", raw))
		return nil
	}
	return &v
}

// limit reads the page limit: absent stays nil (the shared default), and a
// negative value is refused. 0 is legal and means unlimited, so it is not
// bounded here — the loopback-only refusal of an unlimited read is a bind-mode
// decision and lives with the handler that knows the bind mode.
func (q *query) limit() *int {
	v := q.integer("limit")
	if v != nil && *v < 0 {
		q.invalid("limit", "limit must be zero or greater; 0 means unlimited")
		return nil
	}
	return v
}

// boundedLimit reads a page limit that has a hard ceiling and no unlimited
// spelling, substituting fallback when it is absent.
//
// It is a SECOND limit decoder rather than a mode of limit() above, because the
// two operations mean different things by the same parameter name and a shared
// one would have to hide that behind a flag. On the issue listings `0` means
// unlimited and the only refusal is a bind-mode one; on a journal read there is
// no unlimited — a caller with an old checkpoint would ask one process to
// buffer the entire retained window, which under the shipped floors is a
// hundred thousand records — so the ceiling is unconditional and `0` is refused
// by name instead of being reinterpreted as "the default". A parameter that
// means unlimited on one operation and something else on another is exactly the
// silent divergence this decoder exists to prevent.
func (q *query) boundedLimit(fallback, ceiling int) int {
	v := q.integer("limit")
	if v == nil {
		return fallback
	}
	if *v < 1 || *v > ceiling {
		q.invalid("limit", fmt.Sprintf("limit must be between 1 and %d (default %d); this operation has no unlimited read", ceiling, fallback))
		return fallback
	}
	return *v
}

// timestamp reads an optional RFC 3339 instant.
func (q *query) timestamp(name string) *time.Time {
	raw := q.str(name)
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		q.invalid(name, fmt.Sprintf("%q is not an RFC 3339 timestamp", raw))
		return nil
	}
	return &t
}

// metadataFields reads the repeatable `key=value` equality filter, split on the
// FIRST `=` so a value may contain one.
func (q *query) metadataFields(name string) map[string]string {
	raw := q.list(name)
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for _, entry := range raw {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || k == "" {
			q.invalid(name, fmt.Sprintf("%q is not key=value", entry))
			return nil
		}
		out[k] = v
	}
	return out
}

// oneOf reads a parameter constrained to a closed vocabulary, substituting
// fallback when it is absent. An unrecognized value is refused: the vocabulary
// is in the document, so a client that sends something else is asking for
// behavior this server does not have.
func (q *query) oneOf(name, fallback string, allowed ...string) string {
	raw := q.str(name)
	if raw == "" {
		return fallback
	}
	if !slices.Contains(allowed, raw) {
		q.invalid(name, fmt.Sprintf("%q is not one of %s", raw, strings.Join(allowed, ", ")))
	}
	return raw
}

// result reports the refusal this request earned, if any. The
// unknown-parameter check runs LAST so that a malformed value on a parameter
// the server does know is reported as invalid_value rather than as version
// skew — the two carry opposite client recoveries.
func (q *query) result() *Result {
	if q.res != nil {
		return q.res
	}
	var unknown []string
	for name := range q.values {
		if !q.read[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	// One offender, chosen deterministically: `param` is what the client
	// dispatches on, so it must not depend on map order.
	offender := slices.Min(unknown)
	res := InvalidArgument(offender, ReasonUnknownParameter,
		"this server does not know that parameter for this operation")
	if offender == "" {
		// The degenerate `?=1` spelling parses to a parameter whose name is the
		// empty string. InvalidArgument reads "" as "this input has no nameable
		// part" and omits `param`; the document promises `param` on every 400
		// but an unparseable body. Name it.
		res.Problem.Param = &offender
	}
	return &res
}

// offender names the value a refusal turned down, for the request line.
func (r Result) offender() string {
	if r.Problem.Param != nil {
		return *r.Problem.Param
	}
	return ""
}
