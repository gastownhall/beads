package types

import (
	"regexp"
	"strings"
)

// nonCompletingCloseRegexp matches close reasons that redirect or abandon work
// rather than finish a deliverable. Children closed this way must not count
// toward epic/molecule "complete" eligibility (GH#5026).
//
// Matching is word-bounded (\b) rather than raw substring: close_reason is
// free-form prose (Issue.CloseReason is a plain string; cmd/bd/close.go only
// length-validates it), and a bare substring match false-positives on
// ordinary English — "added dedup pass for event ingest" contains "dup", and
// "removed obsolete migration shim" contains "obsolete", yet both describe
// completed work. The bare "dup" keyword is dropped entirely: it was already
// redundant with "duplicate"/"dupe" and only added false positives. Likewise
// the bare adjective "obsolete" is dropped in favor of "obsoleted" — a task
// closed because it was superseded/deprecated typically reads "obsoleted by
// X", while "obsolete" alone is commonly just describing what was removed.
//
// Two separator variants are matched directly in the regex rather than via a
// global text normalization pass (GH#5138 review): "wont[- ]?fix" and
// "won['\x{2019}]t[- ]?fix" both accept a hyphen or space (or neither)
// between "wont"/"won't" and "fix" — the two spellings must compose with the
// separator independently, not just each in isolation, or a lone
// "won't-fix" close still slips through as completing. The apostrophe class
// "['\x{2019}]" accepts both the ASCII apostrophe and the U+2019 typographic
// right single quote ("won't"/"won’t"). A blanket hyphen-to-space fold on the
// whole string was deliberately avoided: it would risk turning unrelated
// hyphenated prose into a false match (e.g. collapsing "not-yet-planned"
// into something that reads like "not planned"). Scoping the hyphen
// tolerance to just this one keyword pair sidesteps that entirely.
var nonCompletingCloseRegexp = regexp.MustCompile(`(?i)\b(duplicate|dupe|wont[- ]?fix|won['\x{2019}]t[- ]?fix|superseded|obsoleted|not planned)\b`)

// IsNonCompletingClose reports whether closeReason is a redirection/abandon
// (duplicate, wontfix, superseded, …) rather than finished work. Empty reason
// is treated as completing for backward compatibility with closes that never
// recorded a reason.
//
// This lives in the leaf types package (not internal/storage/issueops, where
// GH#5026 originally added it) so that internal/workapi can also apply the
// same classifier to epic_closeable (GH#5138 review): issueops already
// imports workapi, so workapi importing issueops back would cycle.
func IsNonCompletingClose(closeReason string) bool {
	if strings.TrimSpace(closeReason) == "" {
		return false
	}
	return nonCompletingCloseRegexp.MatchString(closeReason)
}
