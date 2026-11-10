package versioncontrolops

import "os"

// Committer is a resolved commit identity: the name and email that author a
// Dolt version commit. It is captured once, where a store opens, so every
// commit channel the store owns (explicit --author sites and session
// commits alike) attributes to one identity for the store's lifetime.
type Committer struct {
	Name  string
	Email string
}

// DefaultCommitterName and DefaultCommitterEmail are the identity commits
// carry when nothing more specific resolves. They are the embedded-mode
// equivalents of server mode's CommitterName/CommitterEmail defaults.
const (
	DefaultCommitterName  = "beads"
	DefaultCommitterEmail = "beads@local"
)

// Author returns the identity in Dolt's --author form, "Name <email>", the
// shape DOLT_COMMIT and DOLT_MERGE parse. A Committer with both fields
// resolved always formats; Author is only called on a ResolveCommitter
// result or an equivalently complete value.
func (c Committer) Author() string {
	return c.Name + " <" + c.Email + ">"
}

// ResolveCommitter resolves a commit identity per field: an explicit value
// wins, then the matching GIT_AUTHOR_* environment variable, then the
// default. Each field resolves independently, so a caller that names only an
// email still honors the environment (or default) for the name. Empty and
// unset are the same condition, matching the server-mode rule this extracts
// (internal/storage/dolt applyConfigDefaults). No actor or git-subprocess
// lookup: the caller's explicit fields are where an actor identity enters,
// never this package.
func ResolveCommitter(name, email string) Committer {
	if name == "" {
		if name = os.Getenv("GIT_AUTHOR_NAME"); name == "" {
			name = DefaultCommitterName
		}
	}
	if email == "" {
		if email = os.Getenv("GIT_AUTHOR_EMAIL"); email == "" {
			email = DefaultCommitterEmail
		}
	}
	return Committer{Name: name, Email: email}
}
