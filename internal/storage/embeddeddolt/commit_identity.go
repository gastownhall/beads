//go:build cgo

package embeddeddolt

import (
	"os"
	"strings"
	"sync"
)

// commitIdentity is the author of the Dolt commits ONE store creates
// (GH#7628). Every commit site passes it as an explicit --author: the
// embedded driver's SQL client is root@%, and Dolt prefers that client over
// the DSN's commit name/email, so an unauthored DOLT_COMMIT records root, and
// the DSN identity alone never reaches the commit.
//
// The author is resolved on the first commit that needs it and memoized until
// SetCommitIdentityFallback replaces the fallback, so reads and transactions
// that commit nothing never resolve it.
type commitIdentity struct {
	mu       sync.Mutex
	fallback func() (name, email string) // storage.CommitIdentityConfigurer
	author   string                      // "name <email>"; "" until resolved
}

// SetCommitIdentityFallback implements storage.CommitIdentityConfigurer. It
// clears the memoized author, so the next commit resolves it again.
func (s *EmbeddedDoltStore) SetCommitIdentityFallback(fallback func() (name, email string)) {
	s.identity.mu.Lock()
	defer s.identity.mu.Unlock()
	s.identity.fallback = fallback
	s.identity.author = ""
}

// commitAuthor returns this store's commit author, resolving it on first use.
func (s *EmbeddedDoltStore) commitAuthor() string {
	s.identity.mu.Lock()
	defer s.identity.mu.Unlock()
	if s.identity.author == "" {
		name, email := resolveCommitIdentity(s.identity.fallback)
		s.identity.author = name + " <" + email + ">"
	}
	return s.identity.author
}

// resolveCommitIdentity resolves name and email independently, the way server
// mode does: GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL, else what fallback (when
// non-nil) returns for that field, else beads / beads@local. Every candidate
// is cleaned first, and one that is empty once cleaned falls through.
// fallback is called only when the environment leaves a field empty. buildDSN
// uses it too (without a fallback), so the DSN and the commits name the same
// identity whenever the environment does.
func resolveCommitIdentity(fallback func() (name, email string)) (name, email string) {
	name = cleanCommitIdentityField(os.Getenv("GIT_AUTHOR_NAME"))
	email = cleanCommitIdentityField(os.Getenv("GIT_AUTHOR_EMAIL"))
	if (name == "" || email == "") && fallback != nil {
		fallbackName, fallbackEmail := fallback()
		if name == "" {
			name = cleanCommitIdentityField(fallbackName)
		}
		if email == "" {
			email = cleanCommitIdentityField(fallbackEmail)
		}
	}
	if name == "" {
		name = commitName
	}
	if email == "" {
		email = commitEmail
	}
	return name, email
}

// cleanCommitIdentityField makes one identity field safe for Dolt's
// "name <email>" parser, whose unanchored pattern rejects or silently
// truncates a value holding angle brackets, parentheses or line breaks — and
// it parses only after the SQL commit has landed. It drops those characters
// and collapses whitespace runs to one space.
func cleanCommitIdentityField(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', '(', ')':
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
