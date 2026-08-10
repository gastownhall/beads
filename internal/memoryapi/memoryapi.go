// Package memoryapi holds the parts of the memory plane that decide what an
// answer MEANS: the key derivation, the two refusals and the search filter.
// Every implementation of memoryops.Memories goes through them, so they are the
// single definition of what `bd remember` and `bd memories` mean, and they are
// checkable in milliseconds with no database.
//
// It is its own package rather than a file in internal/workapi because nothing
// in it is issue-shaped, and it is deliberately importable by cmd/bd: the
// `bd remember` front door needs DeriveKey to spot the bare-slug desire path
// BEFORE it decides which role method to call, exactly as the twenty-odd cmd/bd
// files that import workapi need the builders. What a front door must not
// reach is a role CONSTRUCTOR, and there is none here — these are meaning
// functions, not bodies.
//
// The kv.memory. storage encoding is NOT here: that is
// internal/storage/memoryops, below the role, because a user key is what this
// package reasons about.
package memoryapi

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/memoryops"
)

// nonSlugChars matches every run of characters a derived key collapses to a
// single hyphen.
var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

// DeriveKey turns memory content into the key it is stored under when the
// caller supplies none. It is `bd remember`'s slugify, moved verbatim.
//
// ITS QUIRKS ARE LOAD-BEARING AND MUST NOT BE TIDIED. Content is lowercased,
// every run of non-alphanumerics becomes ONE hyphen, the first EIGHT
// hyphen-separated segments are kept and the rest DROPPED, and the result is
// capped at 60 BYTES with a hyphen left at the cut trimmed off. All four are
// observable in the key, and a "cleanup" here silently re-keys every memory
// anyone re-remembers: `bd remember "always run tests with -race flag"` would
// mint a new key beside the old one instead of updating it, and the old memory
// would sit there forever answering an obsolete question. memoryapi_test.go
// pins the whole shape with a golden table.
//
// The SplitN bound of TEN below is the one quirk with no observable effect —
// keeping eight of at-most-ten segments and keeping eight of all of them agree
// on every input. It is preserved verbatim anyway, because "this simplification
// cannot change the answer" is exactly the reasoning that re-keys a workspace
// when it turns out to be wrong, and the golden table is what would catch it.
//
// The result is "" for content with no alphanumerics at all. Callers that
// derive must treat that as a refusal (ValidateDerivedKey), not as a key.
func DeriveKey(content string) string {
	s := strings.ToLower(content)
	s = nonSlugChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")

	// Ten, then eight: the extra two segments are what keeps the tail of a long
	// insight attached to the eighth word instead of being discarded.
	parts := strings.SplitN(s, "-", 10)
	if len(parts) > 8 {
		parts = parts[:8]
	}
	slug := strings.Join(parts, "-")

	if len(slug) > 60 {
		slug = slug[:60]
		// A cap that landed mid-word must not leave the key ending in the
		// separator.
		slug = strings.TrimRight(slug, "-")
	}
	return slug
}

// ValidateContent checks the content a caller wants to remember.
//
// The prose is `bd remember`'s, preserved: it is the sentence agents have been
// reading since the command shipped, and the sentinel is what code classifies
// on.
func ValidateContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("%w: memory content cannot be empty", memoryops.ErrValidation)
	}
	return nil
}

// ValidateKey checks a key a caller wants to read or remove, and returns it
// UNCHANGED.
//
// Unchanged is the whole point: `bd remember --key` accepts any string, so a
// key differing from another only by surrounding space is a key someone may
// genuinely hold, and trimming it here would answer a different question from
// the one asked. The only rule is that a key has to name something.
func ValidateKey(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("%w: memory key must not be empty", memoryops.ErrValidation)
	}
	return key, nil
}

// ResolveKey answers which key a Remember lands under: the caller's if it gave
// one, otherwise the derivation of the content.
//
// It is one function rather than a derivation at each implementation because
// the ORDER matters and is invisible once it is spelled twice: an explicit key
// is used verbatim even when it is content-shaped, and content is only ever
// derived from when no key was given. The refusal for content that derives to
// nothing lives here too, so a caller cannot get a row under the empty key by
// reaching the write through a body that forgot to check.
func ResolveKey(key, content string) (string, error) {
	if err := ValidateContent(content); err != nil {
		return "", err
	}
	if key != "" {
		// THE SAME RULE READS AND WRITES USE. ValidateKey — which Recall and
		// Forget run — refuses a key that is empty after trimming, so accepting
		// one here mints a row that no memory operation can ever name again:
		// enumerable by List forever, unrecallable, unforgettable, reachable
		// only through `bd config unset` on the raw storage key. The HTTP
		// surface inherits the same split, so POST would accept what GET and
		// DELETE refuse.
		//
		// Surrounding space is still preserved on keys that survive this: the
		// rule is that a key must NAME something, not that it must be tidy.
		return ValidateKey(key)
	}
	derived := DeriveKey(content)
	if derived == "" {
		return "", fmt.Errorf("%w: could not generate key from content; use --key to specify one", memoryops.ErrValidation)
	}
	return derived, nil
}

// FilterMemories narrows already-prefix-stripped memories by a raw search term.
//
// THE FOLDING IS HERE, on both sides. The shipped `bd memories` lowercased the
// term at the front door and the filter lowercased the stored values, which
// worked only for as long as every caller remembered the first half; a second
// door that passed the term through raw would have matched nothing and reported
// an empty plane. Now the caller passes what the user typed.
//
// The answer is a fresh map, never nil and never the argument: a caller holding
// the full plane must still hold it after asking a narrower question.
func FilterMemories(all map[string]string, search string) map[string]string {
	if search == "" {
		out := make(map[string]string, len(all))
		for k, v := range all {
			out[k] = v
		}
		return out
	}
	needle := strings.ToLower(search)
	out := make(map[string]string)
	for k, v := range all {
		if strings.Contains(strings.ToLower(k), needle) || strings.Contains(strings.ToLower(v), needle) {
			out[k] = v
		}
	}
	return out
}
