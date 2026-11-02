//go:build cgo

package embeddeddolt

import (
	"os"
)

// Embedded commit authorship (GH#7628): the store resolves a commit identity
// per instance — GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL first, matching server
// mode (internal/storage/dolt/store.go applyConfigDefaults), then a fallback
// the CLI installs through storage.CommitIdentityConfigurer, then the
// anonymous beads <beads@local> pair that was previously the only identity.
//
// The identity is resolved LAZILY and memoized: ordinary reads and clean
// transactions never pay for it, and one store keeps one identity until
// reconfigured. Dolt's DOLT_COMMIT / DOLT_MERGE '--author' must be told the
// identity explicitly — the embedded driver's SQL client identity (root@%)
// outranks the DSN's commit name/email, so the DSN alone cannot attribute.

// SetCommitIdentityFallback installs the fallback identity resolver for this
// store instance only (storage.CommitIdentityConfigurer). The callback is
// consulted at most once per resolution, and only for a field the author
// environment did not supply.
func (s *EmbeddedDoltStore) SetCommitIdentityFallback(fallback func() (name, email string)) {
	s.commitIdentityMu.Lock()
	defer s.commitIdentityMu.Unlock()
	s.commitIdentityFallback = fallback
	s.commitAuthorMemo = ""
}

// commitAuthorString resolves this store's commit author, memoized per
// configuration: the first attempt that reaches an authored commit resolves
// the pair, and every later commit of the same configuration reuses it, so a
// store reports one consistent identity without re-reading the environment or
// re-running the fallback per write.
func (s *EmbeddedDoltStore) commitAuthorString() string {
	s.commitIdentityMu.Lock()
	defer s.commitIdentityMu.Unlock()
	if s.commitAuthorMemo == "" {
		s.commitAuthorMemo = s.resolveCommitAuthorLocked()
	}
	return s.commitAuthorMemo
}

// resolveCommitAuthorLocked composes the author string. Name and email are
// resolved independently: a writer with only a name (the actor chains end in
// a name) still gets attribution, with the email falling back separately.
// Caller holds commitIdentityMu.
func (s *EmbeddedDoltStore) resolveCommitAuthorLocked() string {
	name := os.Getenv("GIT_AUTHOR_NAME")
	email := os.Getenv("GIT_AUTHOR_EMAIL")
	// The fallback runs at most once per resolution, and only when an
	// environment field is missing — a complete environment never consults
	// the CLI at all.
	if (name == "" || email == "") && s.commitIdentityFallback != nil {
		fallbackName, fallbackEmail := s.commitIdentityFallback()
		if name == "" {
			name = fallbackName
		}
		if email == "" {
			email = fallbackEmail
		}
	}
	if name == "" {
		name = commitName
	}
	if email == "" {
		email = commitEmail
	}
	return name + " <" + email + ">"
}