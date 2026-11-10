package versioncontrolops

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Default Dolt commit identity, used for a field neither the caller nor the
// environment sets.
const (
	DefaultCommitterName  = "beads"
	DefaultCommitterEmail = "beads@local"
)

// The characters a field of Dolt's --author argument cannot carry. Dolt
// parses "Name <email>" with the pattern `([^)]+) \<([^)]+)` and strips '>'
// from the email (cli.ParseAuthor, behind DOLT_COMMIT and DOLT_MERGE). A name
// ending in ')' matches nothing, so every commit fails ("Author not formatted
// correctly"); any other ')' cuts the name or the email short; and an email
// holding '<' or '>' does not come back as written.
const (
	authorNameRejects  = ")"
	authorEmailRejects = ")<>"
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
// in both modes (#7628). A value Dolt's --author parser cannot carry counts
// as unset, with a warning that names it, so a GIT_AUTHOR_NAME such as
// "Jane Doe (CI)" cannot fail or rename every commit.
func ResolveCommitter(name, email string) Committer {
	return Committer{
		Name: resolveCommitterField(DefaultCommitterName, authorNameRejects,
			committerCandidate{"the configured committer name", name},
			committerCandidate{"GIT_AUTHOR_NAME", os.Getenv("GIT_AUTHOR_NAME")}),
		Email: resolveCommitterField(DefaultCommitterEmail, authorEmailRejects,
			committerCandidate{"the configured committer email", email},
			committerCandidate{"GIT_AUTHOR_EMAIL", os.Getenv("GIT_AUTHOR_EMAIL")}),
	}
}

// committerCandidate is one source of a committer field, named the way the
// warning names it.
type committerCandidate struct {
	source, value string
}

// resolveCommitterField returns the first candidate that is set and holds
// none of the rejected characters, else def. Emptiness, not blankness, is
// unset: a whitespace-only value is kept.
func resolveCommitterField(def, rejects string, candidates ...committerCandidate) string {
	resolved := def
	var skipped []committerCandidate
	for _, c := range candidates {
		if c.value == "" {
			continue
		}
		if strings.ContainsAny(c.value, rejects) {
			skipped = append(skipped, c)
			continue
		}
		resolved = c.value
		break
	}
	for _, c := range skipped {
		warnUnusableCommitter(c, rejects, resolved)
	}
	return resolved
}

// committerWarningOut is where a skipped committer value is reported; a
// variable so tests can capture it.
var committerWarningOut io.Writer = os.Stderr

// committerWarned holds the sources already reported in this process. Every
// store open resolves the identity, so without it one bd command could print
// the same warning several times.
var committerWarned sync.Map

func warnUnusableCommitter(c committerCandidate, rejects, used string) {
	if _, seen := committerWarned.LoadOrStore(c.source, true); seen {
		return
	}
	bad := c.value[strings.IndexAny(c.value, rejects)]
	fmt.Fprintf(committerWarningOut, "Warning: %s %q contains '%c', which Dolt cannot store in a commit author; Dolt commits use %q instead\n",
		c.source, c.value, bad, used)
}

// Author returns the identity in DOLT_COMMIT's --author form, "Name <email>".
func (c Committer) Author() string {
	return c.Name + " <" + c.Email + ">"
}
