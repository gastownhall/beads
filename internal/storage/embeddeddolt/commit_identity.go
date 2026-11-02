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
// The author is resolved on the first commit that needs it and memoized for
// the store's life, so reads and transactions that commit nothing never
// resolve it.
type commitIdentity struct {
	mu     sync.Mutex
	author string // "name <email>"; "" until resolved
}

// commitAuthor returns this store's commit author, resolving it on first use.
func (s *EmbeddedDoltStore) commitAuthor() string {
	s.identity.mu.Lock()
	defer s.identity.mu.Unlock()
	if s.identity.author == "" {
		s.identity.author = resolveCommitAuthor()
	}
	return s.identity.author
}

// resolveCommitAuthor formats envCommitIdentity as a Dolt --author value.
func resolveCommitAuthor() string {
	name, email := envCommitIdentity()
	return name + " <" + email + ">"
}

// envCommitIdentity resolves name and email independently, the way server
// mode does: GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL, else beads / beads@local.
// A value that is empty once cleaned falls through. buildDSN uses it too, so
// the DSN and the commits name the same identity.
func envCommitIdentity() (name, email string) {
	name = cleanCommitIdentityField(os.Getenv("GIT_AUTHOR_NAME"))
	email = cleanCommitIdentityField(os.Getenv("GIT_AUTHOR_EMAIL"))
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
