package types

import (
	"sort"
	"strings"
)

// RequiresLabelPrefix is the reserved label namespace that states an
// EXECUTION-CONTEXT REQUIREMENT on an issue: a label `requires:<token>` says
// "only a reader whose declared context satisfies <token> should pick me up".
//
// The token is one of two shapes:
//
//	requires:<key>          a bare capability (e.g. requires:gpu, requires:prod-creds)
//	requires:<key>=<value>  a keyed one      (e.g. requires:host=studio, requires:os=darwin)
//
// A reader declares its context the same way, as tokens, through
// `bd ready --ctx <token>` / `--ctx-file`. A bare requirement `requires:k` is
// satisfied by a declared `k` OR by any declared `k=<anything>`; a keyed
// requirement `requires:k=v` is satisfied only by that exact `k=v`. An issue
// carrying several requirements needs ALL of them satisfied (AND).
//
// This is a LABEL CONVENTION with a read-side predicate, not a field: the
// storage, JSONL, sync, export/import and wire surfaces are untouched, and a
// client that does not know the convention simply sees the rows (the behavior
// every reader has today). The predicate is the one thing the existing label
// filters cannot express: "hide any issue carrying a label under this prefix
// whose token is NOT in my declared set" needs the full value universe for
// --exclude-label and a negative match --label-regex (RE2) cannot write.
const RequiresLabelPrefix = "requires:"

// NormalizeCtx trims, drops empty tokens and dedupes a declared context,
// PRESERVING the nil/non-nil distinction the filters read:
//
//	nil      -> nil        (no context declared: the requires predicate is off)
//	[]string{} / {""} -> []string{} (an EMPTY context: every requirement is unmet)
//
// utils.NormalizeLabels always returns a non-nil slice, which would turn "not
// declared" into "declared empty" and silently hide every requiring issue from
// a caller that never asked; that is why this is its own function.
func NormalizeCtx(ctx []string) []string {
	if ctx == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(ctx))
	out := make([]string, 0, len(ctx))
	for _, t := range ctx {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// RequiresSatisfiedLabels is the exact set of `requires:` labels a declared
// context satisfies, sorted: for every token `t` the label `requires:t`, and
// for every keyed token `k=v` also the bare `requires:k`. It is what the SQL
// predicate compares against, so the satisfaction rule lives in ONE place and
// the query stays a plain `label NOT IN (...)` under the prefix. A nil or empty
// context satisfies nothing and returns nil.
func RequiresSatisfiedLabels(ctx []string) []string {
	if len(ctx) == 0 {
		return nil
	}
	set := make(map[string]struct{}, 2*len(ctx))
	for _, t := range NormalizeCtx(ctx) {
		set[RequiresLabelPrefix+t] = struct{}{}
		if k, _, keyed := strings.Cut(t, "="); keyed && k != "" {
			set[RequiresLabelPrefix+k] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// RequiresLabelSatisfied reports whether one issue label is a requirement the
// declared context meets. Labels outside the namespace are not requirements
// and always satisfy. It is the in-memory twin of the SQL predicate, for
// callers that already hold the hydrated labels.
func RequiresLabelSatisfied(label string, ctx []string) bool {
	if !strings.HasPrefix(label, RequiresLabelPrefix) {
		return true
	}
	for _, s := range RequiresSatisfiedLabels(ctx) {
		if s == label {
			return true
		}
	}
	return false
}
