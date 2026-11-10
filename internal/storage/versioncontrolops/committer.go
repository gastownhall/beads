package versioncontrolops

import "os"

// Default Dolt commit identity, used for a field neither the caller nor the
// environment sets.
const (
	DefaultCommitterName  = "beads"
	DefaultCommitterEmail = "beads@local"
)

// Committer is the identity a store writes Dolt commits under, both as the
// session committer and as the --author of its explicit commits.
type Committer struct {
	Name  string
	Email string
}

// ResolveCommitter resolves each field independently: the explicit value,
// else GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL, else beads / beads@local. Server
// and embedded stores share this rule so a commit is attributed the same way
// in both modes (#7628).
func ResolveCommitter(name, email string) Committer {
	if name == "" {
		name = os.Getenv("GIT_AUTHOR_NAME")
		if name == "" {
			name = DefaultCommitterName
		}
	}
	if email == "" {
		email = os.Getenv("GIT_AUTHOR_EMAIL")
		if email == "" {
			email = DefaultCommitterEmail
		}
	}
	return Committer{Name: name, Email: email}
}

// Author returns the identity in DOLT_COMMIT's --author form, "Name <email>".
func (c Committer) Author() string {
	return c.Name + " <" + c.Email + ">"
}
