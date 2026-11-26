package issueops

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// canonicalActor normalizes an identity string so two spellings of the same
// Gas Town identity compare equal. The same identity arrives at bd in more
// than one spelling depending on which layer produced the string it was
// handed: a dotted alias like "gastown.mayor" gets its dot replaced wherever
// a dot is unsafe for that context — "__" in a session name, "_" in a Dolt
// table/database name, "-" elsewhere — and bd only ever sees the resulting
// string, never the substitution itself (ga-wzl83). None of ".", "_", "-"
// carries meaning in an identity string: each is always a positional
// separator between a rig and a role/agent name, never part of either name.
// Collapsing a run of them to one canonical separator lets two spellings of
// the same identity compare equal without weakening comparisons between
// genuinely different identities, whose non-separator characters still
// differ (e.g. "gastown.mayor" vs "gastown.dog-3" stay distinct).
//
// Letter case carries no meaning in an identity either (GH#8628): an
// assignee is free text typed by humans and agents, so mixed case drifts in
// within a day in a multi-agent setup. Both the ASCII ("Hierophant" vs
// "hierophant") and the Unicode simple-lowercase fold are applied — the
// fold is exactly what strings.ToLower does on valid UTF-8, which is also
// what Dolt's LOWER() runs on a utf8mb4 column, so a Go-side gate and a
// SQL-side LOWER() predicate stay in agreement (the listing plane's
// assignee filter renders LOWER(assignee) = ?). Two caveats the fold
// deliberately keeps: a rune that is not a valid UTF-8 start is copied
// byte-for-byte, so "\xff" and "\xfe" stay distinct (plain strings.ToLower
// would merge both into U+FFFD), and the fold is per-rune simple
// lowercasing, so "ſ" (U+017F) is not folded onto "s" ("ſ" lowercases to
// itself) while the Kelvin sign "K" does fold onto "k" — matching
// strings.ToLower, and that agreement is what AC-level parity tests pin.
//
// A run that is the exact two-byte sequence "--" is a second, distinct axis:
// gascity's session-name encoding (session_name.go) substitutes "--" for a
// rig-qualified agent's "/" — a DIFFERENT identity from one substituting "_"
// or "__" for a dotted alias's "." — so it decodes to a literal "/" instead
// of collapsing into the generic "_" separator (ga-2vy9p2). Any other run,
// including "__" and longer or mixed runs, still collapses to "_": those
// keep meaning nothing but "here was some separator". A raw "/" already
// passes through unchanged (it hits the default case below), which is what
// makes the "--" decode land on the same canonical form: "gastown/mayor" and
// "gastown--mayor" both canonicalize to "gastown/mayor", while
// "gastown--mayor" and "gastown__mayor" no longer collapse to the same
// value — collapsing them was a real widening: "gastown--mayor" is a
// rig-qualified agent named "mayor", "gastown__mayor" is a dotted alias
// "gastown.mayor"; treating them as the same actor was wrong regardless of
// whether either happens to be held by the same principal today. The case
// fold composes with the separator fold: separators are case-neutral ASCII,
// and no rune lowercases to '.', '_' or '-', so lowercase runs cannot create
// or destroy separators.
//
// Byte-scanned rather than rune-scanned so a 2-byte lookahead can detect an
// exact "--" run: safe because '.', '_', '-' are single-byte ASCII that never
// appear as a continuation byte of a multi-byte UTF-8 rune, so slicing on
// them cannot split one. A byte at or above 0x80 is decoded as a rune so the
// case fold stays correct on multi-byte characters.
//
// Empty stays empty: an actual absence of an actor must never canonicalize
// to the same value as a non-empty one, or a caller comparing against an
// unassigned issue would start matching everyone.
//
// This is a package-local duplicate of internal/validation/issue.go's
// CanonicalActor (ga-wzl83): issueops sits below validation in the layering
// (storage depending on a higher-level validation package would invert it),
// so the two copies stay independent rather than sharing an import. Keep
// them in sync if the separator set or normalization rule ever changes
// (ga-5ksp5).
func canonicalActor(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '.' || c == '_' || c == '-' {
			j := i
			for j < len(s) && (s[j] == '.' || s[j] == '_' || s[j] == '-') {
				j++
			}
			if s[i:j] == "--" {
				b.WriteByte('/')
			} else {
				b.WriteByte('_')
			}
			i = j
			continue
		}
		if c < utf8.RuneSelf {
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// An invalid byte is not a character to case-fold; keep it
			// distinct rather than merging it with every other invalid
			// byte the way strings.ToLower's U+FFFD substitution would.
			b.WriteByte(c)
			i++
			continue
		}
		b.WriteString(string(unicode.ToLower(r)))
		i += size
	}
	return b.String()
}

// actorMatches reports whether a and b denote the same identity: either
// they're byte-identical, or they canonicalize to the same string — the
// comparison forgives separator respellings and letter case (GH#8628) and
// nothing else. The byte-identical check is not redundant — it avoids
// paying canonicalization on the overwhelmingly common exact-match path.
func actorMatches(a, b string) bool {
	return a == b || canonicalActor(a) == canonicalActor(b)
}

// ActorMatches is the exported form of actorMatches. Exported for the
// proxied-server (uow) claim path in internal/storage/domain/db (ga-v2k49),
// which builds its own claim UPDATE rather than calling ClaimIssueInTx —
// same reason lease.go's RowLockClause and FreshRowLock are exported, and
// same package-local-duplicate-over-cross-layer-import tradeoff documented on
// canonicalActor above: domain/db already depends on issueops for the
// row_lock and pool/status helpers, so reusing this one too adds no new
// layering edge, unlike importing validation would.
func ActorMatches(a, b string) bool {
	return actorMatches(a, b)
}
