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
// Separator tolerance is written into each multi-word keyword rather than
// applied by a global text normalization pass (GH#5138 review):
// "wont[- ]?fix", "won['\x{2019}]t[- ]?fix" and "not[- ]?planned" each accept
// a hyphen or space (or neither) between their two words. The "wont" and
// "won't" spellings must compose with the separator independently, not just
// each in isolation, or a lone "won't-fix" close still slips through as
// completing; "not-planned" is an equally natural hand-typed spelling and is
// given the same tolerance rather than being the one keyword that demands a
// space. The apostrophe class "['\x{2019}]" accepts both the ASCII apostrophe
// and the U+2019 typographic right single quote ("won't"/"won’t").
//
// A blanket hyphen-to-space fold over the whole reason was deliberately
// avoided: it would rewrite prose these keywords never cover, so the text
// that matched would no longer be the text the operator typed. Per-keyword
// tolerance keeps the reach exact — "not-yet-planned" does not match
// "not[- ]?planned" under any spelling, because the intervening "yet" blocks
// it either way, and that negative stays pinned in the test.
var nonCompletingCloseRegexp = regexp.MustCompile(`(?i)\b(duplicate|dupe|wont[- ]?fix|won['\x{2019}]t[- ]?fix|superseded|obsoleted|not[- ]?planned)\b`)

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
