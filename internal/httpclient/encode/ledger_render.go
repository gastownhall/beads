// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/ledger_render.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import (
	"fmt"
	"strings"
)

// RenderLedgerMarkdown renders divergence ledger v1 as the human-readable
// shipped artifact engdocs/design/http-divergence-ledger.md.
//
// It reads Ledger() — the same machine-checked source the encoder consults and
// the bijection gate holds honest — so the doc cannot enumerate a divergence
// the client does not, nor omit one it does. A golden test regenerates this
// output and fails CI if the committed doc drifts from the source, which is the
// second half of the anti-drift chain: the bijection gate pins the ledger to
// the wire, and the golden test pins this doc to the ledger.
//
// Every row in Ledger() lands in exactly one section here; a row whose id
// matches no known population still renders, in the trailing "Other
// divergences" section, so a new population is loud rather than dropped.
func RenderLedgerMarkdown() string {
	rows := Ledger()

	sections := []struct {
		title   string
		blurb   string
		matches func(id string) bool
	}{
		{
			"Whole-behavior divergences (D9 ledger)",
			"The L-rows the design's D9 table names: whole behaviors that differ over HTTP, each degraded knowingly or refused outright. Retired rows stay so the L-numbers every citation uses keep resolving.",
			func(id string) bool { return strings.HasPrefix(id, "L") },
		},
		{
			"Write-side refuse-not-drop (D8)",
			"Role-request fields on the served writes that the v0 wire publishes no member for. Each refuses rather than forwarding a write shorn of the field — a silently dropped precondition or flag would widen what the write touches.",
			func(id string) bool { return strings.HasPrefix(id, "W-") },
		},
		{
			"Command and flag refusals (D7)",
			"Whole commands, and the flag modes of served commands, that dispatch onto methods the wire does not carry. They refuse early — before RunE — in the user's own command/flag vocabulary.",
			func(id string) bool { return strings.HasPrefix(id, "F-") },
		},
		{
			"Ready-request fields",
			"Members of the ready vocabulary the wire cannot express.",
			func(id string) bool { return strings.HasPrefix(id, "E-ReadyRequest") },
		},
		{
			"List-request fields",
			"The listing publishes far more filters than the v0 wire does; every one the wire lacks refuses rather than dropping, because a dropped list filter widens the page invisibly. Two hydration opt-outs are dropped instead, with the argument for why that cannot be misread as a narrower or wider answer.",
			func(id string) bool { return strings.HasPrefix(id, "E-ListRequest") },
		},
		{
			"Query-request fields",
			"The boolean-query surface publishes almost its whole vocabulary; this is what is left over.",
			func(id string) bool { return strings.HasPrefix(id, "E-QueryRequest") },
		},
		{
			"Search bridge / IssueFilter (D4, D11)",
			"The off-role SearchIssues bridge serves two shapes and no others: the exact-ids fast path and the parent descendant walk. Every other populated IssueFilter member is a filter the bridge would have to drop to answer, so it refuses.",
			func(id string) bool {
				return strings.HasPrefix(id, "E-IssueFilter") || strings.HasPrefix(id, "E-bridge")
			},
		},
		{
			"Parent-walk derived defaults (D4)",
			"The descendant walk reads the same IssueFilter as the exact-ids shape and reads it differently. The wire publishes intents (all, include_templates, include_gates, include_infra) and the server re-derives the exclusions; the bridge inverts the derived defaults back to intents and refuses anything the inversion cannot account for.",
			func(id string) bool { return strings.HasPrefix(id, "P-") },
		},
		{
			"Ready bridge / WorkFilter (D8)",
			"The throwaway bridge that maps a WorkFilter back onto ready parameters until the front door moves onto issueops.Reader. It refuses every field it cannot express.",
			func(id string) bool { return strings.HasPrefix(id, "E-WorkFilter") },
		},
	}

	assigned := make([]bool, len(rows))
	var b strings.Builder

	fmt.Fprint(&b, ledgerDocPreamble)

	var refuse, degrade, retired int
	for _, r := range rows {
		switch r.Kind {
		case KindRefuse:
			refuse++
		case KindDegrade:
			degrade++
		case KindRetired:
			retired++
		}
	}
	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "%d rows: %d refuse, %d degrade, %d retired.\n\n",
		len(rows), refuse, degrade, retired)

	writeSection := func(title, blurb string, idxs []int) {
		fmt.Fprintf(&b, "## %s\n\n", title)
		if blurb != "" {
			fmt.Fprintf(&b, "%s\n\n", blurb)
		}
		fmt.Fprint(&b, "| ID | Kind | Field / flag | Divergence | Why | Spec | Pinned test |\n")
		fmt.Fprint(&b, "|---|---|---|---|---|---|---|\n")
		for _, i := range idxs {
			r := rows[i]
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n",
				cell(r.ID), cell(string(r.Kind)), fieldFlag(r),
				cell(r.What), cell(r.Why), cell(r.SpecRow), cell(r.PinnedBy))
		}
		fmt.Fprint(&b, "\n")
	}

	for _, s := range sections {
		var idxs []int
		for i, r := range rows {
			if !assigned[i] && s.matches(r.ID) {
				assigned[i] = true
				idxs = append(idxs, i)
			}
		}
		if len(idxs) > 0 {
			writeSection(s.title, s.blurb, idxs)
		}
	}

	var leftover []int
	for i := range rows {
		if !assigned[i] {
			leftover = append(leftover, i)
		}
	}
	if len(leftover) > 0 {
		writeSection("Other divergences",
			"Rows whose id matched no known population. A new one here means RenderLedgerMarkdown needs a section for it.",
			leftover)
	}

	return b.String()
}

// fieldFlag renders the Go request field and the user's flag spelling a row
// carries, as inline code; "—" when the row describes a behavior with neither.
func fieldFlag(r Row) string {
	var parts []string
	if r.Type != nil {
		parts = append(parts, "`"+r.Type.Name()+"."+r.Field+"`")
	}
	if r.Flag != "" {
		parts = append(parts, "`"+r.Flag+"`")
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

// cell makes an arbitrary ledger string safe for one Markdown table cell:
// pipes are escaped so they do not read as column breaks, and every run of
// whitespace (the Why strings are built by concatenation and can carry
// newlines) collapses to a single space.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.Join(strings.Fields(s), " ")
}

const ledgerDocPreamble = `<!-- Code generated by internal/httpclient/encode.RenderLedgerMarkdown; DO NOT EDIT. -->
<!-- Regenerate: go generate ./internal/httpclient/encode, or -->
<!-- go test ./internal/httpclient/encode -run TestDivergenceLedgerDocMatchesLedger -update-ledger-doc -->

# bd HTTP mode — the divergence ledger

This is the shipped, human-readable rendering of divergence ledger v1: every
place ` + "`bd`" + `'s HTTP client mode observably differs from local (embedded/served)
mode, each with its reason and the test that pins it.

The discipline (design decision D9) is empty-by-default and test-asserted:
**every knowingly-degraded behavior is a ledger row with a pinned test, and a
degradation not in this table is a bug.** ` + "`refuse`" + ` rows fail rather than
proceeding — a dropped filter widens a result set invisibly, the one failure
class no server-side gate can observe — while ` + "`degrade`" + ` rows proceed with a
difference argued to be unmisreadable as a narrower or wider answer than the
caller asked for.

This file is generated from ` + "`internal/httpclient/encode`" + `'s
` + "`Ledger()`" + `, the machine-readable source the client actually consults. Do not
hand-edit it: change the ledger in Go and regenerate. Two gates keep it honest —
the encoder's reflection **bijection gate** pins the ledger to the wire (a
request field added upstream lands here or fails CI), and the golden test
` + "`TestDivergenceLedgerDocMatchesLedger`" + ` pins this doc to the ledger. The prose
rationale, the decision numbers (D-rows) each entry cites, and the full test-lane
map live in the architecture spec, ` + "`http-client-backend.md`" + `, which is not
included in this repository.

A handful of rows still carry a ` + "`TODO`" + ` pin: these are the read-display and
pre-run residuals (wisp-in-list, the pretty ` + "`bd ready`" + ` parent-epic map, the
molecule/auto-import pre-run degradations). The
client core is complete; ` + "`ga-b8ddd.12`" + ` (per-request project-id enforcement)
closed the read-display escalation, so pinning the remaining fixture corpus is
deferred work, tracked on ` + "`ga-b8ddd.23`" + ` (the read-display residual)
and ` + "`ga-b8ddd.19`" + ` (the per-id D7 taxonomy). They are refusals or degradations
already in force — the ` + "`TODO`" + ` is on the test that will hold each one, not on
the behavior.

## Kind

- **refuse** — the operation fails; the wire cannot express what was asked and
  proceeding would silently widen or narrow the answer.
- **degrade** — the operation proceeds with a knowingly different behavior that
  cannot be misread as a different answer to the question asked.
- **retired** — a row a later council removed; kept so its L-number keeps
  resolving and a re-enumeration can tell "retired" from "never existed".

`
