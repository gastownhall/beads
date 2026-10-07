// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/skew.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import (
	"fmt"
	"slices"
	"strings"
)

// Version skew, read backwards.
//
// Everything else in this package runs one direction — a request FIELD becomes a
// wire parameter — because that is the direction a filter gets silently dropped
// in. This file runs the other way, and it exists for the other half of the skew
// problem: a newer CLI sending a parameter an older bd serve has never heard of.
//
// The server answers that with 400 `invalid_argument`, `reason:
// "unknown_parameter"` and the offending `param` (internal/httpapi/query.go,
// ReasonUnknownParameter at problem.go:139). The client has the parameter; the
// user has a flag. D7's third refusal text is the translation between them, and
// this table is its mechanism.
//
// It is a table rather than a mechanical kebab-case rewrite of the parameter
// name for two reasons, and neither is cosmetic. First, the rewrite would be
// wrong: `q` is `bd query`'s positional argument and `cursor` is minted by the
// server, so both would render as flags that do not exist and send the user
// looking for one. Second, a table is CHECKABLE — the gate in skew_test.go reads
// it against the encoder table in both directions, so a parameter added to the
// wire tomorrow cannot arrive here unmapped and degrade case 3 into a bare
// "invalid argument" the moment it is the parameter that skews.

// UnknownParameterReason is the `reason` member bd serve sets on the 400 that
// means "this server is older than you think" rather than "that value is
// wrong". The two refusals share a code and carry opposite client recoveries —
// upgrade the server versus fix the argument — so the reason is what dispatches,
// never the code alone.
const UnknownParameterReason = "unknown_parameter"

// UnknownParameterFormat is D7's third refusal text, minus the "Error: " prefix
// the standard exit path adds (cmd/bd/errors.go).
//
// It lives here rather than at the render site because the render site is not
// the only party that has to agree on it: the refusal-UX layer byte-pins these
// bytes, and a pin against a string that layer also owns proves only that it
// agrees with itself. Sharing the constant is what makes the pin a contract
// between the mechanism and the text.
//
// Arguments, in order: subject (already spelled the way a user would read it,
// dashes included), server URL, bd_version, parameter.
const UnknownParameterFormat = "%s is not supported by bd serve at %s (bd_version %s): parameter %q is unknown to the server"

// UnknownParameterNoVersionFormat is the same refusal before the handshake has
// run, when there is no server version to name.
//
// The two forms exist because the handshake is LAZY (D6): a case-3 refusal can
// arrive from a baseline operation that never fetched a context, and dialing for
// a version so the refusal could be one clause longer would be the wrong trade —
// a refusal is the one path that has to stay fast and side-effect free.
//
// Arguments, in order: subject, server URL, parameter.
const UnknownParameterNoVersionFormat = "%s is not supported by bd serve at %s: parameter %q is unknown to the server"

// paramFlag is the flag each wire parameter came from.
//
// The map is flat rather than per-operation because the CLI spells these
// identically on every command that has them — `--limit` is `--limit` on `bd
// list`, `bd ready` and `bd query` alike — and because the parameter is all the
// server tells us: a 400 names the parameter, not the operation's flag set. The
// spellings are pinned against the real cobra tree by the enterprise-tagged gate
// in cmd/bd, so a flag renamed upstream fails there rather than shipping as a
// refusal pointing at a flag that no longer exists.
var paramFlag = map[string]string{
	"all":                "all",
	"assignee":           "assignee",
	"brief":              "brief",
	"brief_deps":         "brief-deps",
	"closed_after":       "closed-after",
	"closed_before":      "closed-before",
	"created_after":      "created-after",
	"created_before":     "created-before",
	"desc_contains":      "desc-contains",
	"empty_description":  "empty-description",
	"exclude_label":      "exclude-label",
	"exclude_status":     "exclude-status",
	"exclude_type":       "exclude-type",
	"has_metadata_key":   "has-metadata-key",
	"id":                 "id",
	"include_comments":   "include-comments",
	"include_deferred":   "include-deferred",
	"include_dependents": "include-dependents",
	"include_ephemeral":  "include-ephemeral",
	"include_gates":      "include-gates",
	"include_infra":      "include-infra",
	"include_templates":  "include-templates",
	"label":              "label",
	"label_any":          "label-any",
	"label_pattern":      "label-pattern",
	"label_regex":        "label-regex",
	"limit":              "limit",
	"metadata_field":     "metadata-field",
	"no_assignee":        "no-assignee",
	"no_labels":          "no-labels",
	"no_parent":          "no-parent",
	"notes_contains":     "notes-contains",
	"parent":             "parent",
	"priority":           "priority",
	"priority_max":       "priority-max",
	"priority_min":       "priority-min",
	"reverse":            "reverse",
	"sort":               "sort",
	"status":             "status",
	"title":              "title",
	"title_contains":     "title-contains",
	"type":               "type",
	"unassigned":         "unassigned",
	"updated_after":      "updated-after",
	"updated_before":     "updated-before",
}

// paramNoFlag names the wire members this client puts on a request that no flag
// produced: what the refusal calls each one, and why it has no flag.
//
// They are enumerated rather than left to fall through so that "no flag" is a
// decision with a reason attached instead of the shape a forgotten entry takes.
// Subject is what the user READS and Why is what a reader of this table (and its
// gate) needs; they are different jobs, and collapsing them would either put
// rationale in a refusal or leave the rationale nowhere.
var paramNoFlag = map[string]noFlagParam{
	"q": {
		Subject: "the bd query expression",
		Why:     "`bd query`'s expression is its positional argument, not a flag; a refusal naming `--q` would send the user looking for one that does not exist",
	},
	"cursor": {
		Subject: "paging",
		Why: "the opaque keyset token is minted by the SERVER and echoed by the pager. No flag can produce it, and a client that minted its own would be " +
			"inventing a position in an order it cannot observe. A user who sees this text is looking at a paging bug rather than at something they typed",
	},
	"group_by": {
		Subject: "the count grouping",
		Why: "`bd count` spells the dimension as five mutually exclusive booleans — --by-status, --by-priority, --by-type, --by-assignee, --by-label — and the parameter carries whichever one was set. " +
			"There is no --group-by to name, and naming any single --by-* flag would name the wrong one four times out of five",
	},
	"{id}": {
		Subject: "the issue id",
		Why: "the issue id is a path segment (and, at the front door, a positional argument), so a skew refusal about it would not be a skew refusal at all — " +
			"an older server that does not route the path answers 404, which is D6's capability pre-flight case, not this one",
	},
}

type noFlagParam struct {
	Subject string
	Why     string
}

// UnknownParameterSubjectFormat is what case 3 calls a parameter this client
// does not send at all.
//
// The enterprise server's Host-allowlist refusal is the live case: it answers
// 400 with `param: "Host"` for a DNS-named URL (internal/httpapi/server.go),
// which is a request header rather than a query parameter. Rendering that as
// `--host` would be a lie, and dropping it would lose the one refusal on this
// surface whose recovery is a serve-side flag.
const UnknownParameterSubjectFormat = "the %q filter"

// FlagForParam reports the flag that produced a wire parameter.
//
// ok is false both for a parameter with no flag behind it and for one this
// client does not send at all; the caller renders the parameter itself in either
// case. Distinguishing them here would buy nothing — the user's recovery is the
// same, and the reason lives in the table for a reader rather than for a branch.
func FlagForParam(param string) (flag string, ok bool) {
	f, ok := paramFlag[param]
	return f, ok
}

// ParameterSubject is what case 3 calls the thing the server rejected.
//
// A flag is named as the user typed it, dashes and all. A wire member with no
// flag behind it is named by its recorded subject. Anything else — a parameter
// this client never sent — falls back to naming the parameter, because a
// refusal that could not name its subject would be a skew signal swallowed.
func ParameterSubject(param string) string {
	if flag, ok := paramFlag[param]; ok {
		return "--" + flag
	}
	if entry, ok := paramNoFlag[param]; ok {
		return entry.Subject
	}
	return fmt.Sprintf(UnknownParameterSubjectFormat, param)
}

// UnknownParameterRefusal renders D7 case 3 for one skew signal.
//
// bdVersion is empty before the lazy handshake has run, and that picks the
// shorter form rather than printing an empty parenthetical.
//
// It always renders. A skew refusal that could come back empty would be the
// silent drop this whole package exists to prevent, wearing a different hat: the
// user typed something, the server refused it, and every path out of here has to
// say so.
func UnknownParameterRefusal(param, serverURL, bdVersion string) string {
	subject := ParameterSubject(param)
	if bdVersion == "" {
		return fmt.Sprintf(UnknownParameterNoVersionFormat, subject, serverURL, param)
	}
	return fmt.Sprintf(UnknownParameterFormat, subject, serverURL, bdVersion, param)
}

// SkewParams lists every wire member the encoder table puts on a request, sorted,
// paired with the flag behind it where there is one.
//
// It is what the gate walks, and it is also the honest answer to "which of my
// flags can a server be too old for" — a question the refusal text can only
// answer one parameter at a time.
func SkewParams() []ParamFlag {
	entryFor := func(param string) ParamFlag {
		flag, ok := paramFlag[param]
		return ParamFlag{
			Param:   param,
			Flag:    flag,
			HasFlag: ok,
			Subject: ParameterSubject(param),
			Why:     paramNoFlag[param].Why,
		}
	}

	seen := map[string]ParamFlag{}
	for _, table := range cachedTables() {
		for _, entry := range table.Fields {
			switch entry.Disposition {
			case DispParam, DispPath:
				seen[entry.Param] = entryFor(entry.Param)
			}
		}
		for _, reserved := range table.Reserved {
			seen[reserved.Name] = entryFor(reserved.Name)
		}
	}
	out := make([]ParamFlag, 0, len(seen))
	for _, pf := range seen {
		out = append(out, pf)
	}
	slices.SortFunc(out, func(a, b ParamFlag) int { return strings.Compare(a.Param, b.Param) })
	return out
}

// ParamFlag is one wire member and the flag behind it.
type ParamFlag struct {
	Param   string
	Flag    string
	HasFlag bool
	// Subject is what a case-3 refusal calls this member: the flag with its
	// dashes, or the recorded spelling for a member no flag produces. Never
	// empty.
	Subject string
	// Why is the reason a member has no flag, empty when it has one.
	Why string
}
