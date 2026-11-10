package versioncontrolops

import (
	"os"
	"testing"
)

// setGitAuthorEnv pins both GIT_AUTHOR_* variables and registers the
// restore, so a test mutates the environment without leaking it. Values set
// to "" simulate unset where the host environment could carry a real value.
func setGitAuthorEnv(t *testing.T, name, email string) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", name)
	t.Setenv("GIT_AUTHOR_EMAIL", email)
}

// TestResolveCommitter pins the per-field identity ladder — explicit >
// environment > default — that server mode's applyConfigDefaults has always
// applied and embedded mode now captures where its stores open. Each field
// resolves independently, and the environment is read per field, so one
// explicit value never suppresses the other field's environment lookup.
// Env-mutating test: no t.Parallel.
func TestResolveCommitter(t *testing.T) {
	setGitAuthorEnv(t, "", "")
	tests := []struct {
		name      string
		inName    string
		inEmail   string
		envName   string
		envEmail  string
		wantName  string
		wantEmail string
	}{
		{
			name:     "explicit wins over env and default",
			inName:   "Ada Lovelace",
			inEmail:  "ada@example.com",
			envName:  "Env Person",
			envEmail: "env@example.com",
			wantName: "Ada Lovelace", wantEmail: "ada@example.com",
		},
		{
			name:     "env fills an unset field while explicit holds the other",
			inName:   "Ada Lovelace",
			inEmail:  "",
			envName:  "Env Person",
			envEmail: "env@example.com",
			wantName: "Ada Lovelace", wantEmail: "env@example.com",
		},
		{
			name:     "defaults when nothing is set",
			inName:   "",
			inEmail:  "",
			envName:  "",
			envEmail: "",
			wantName: DefaultCommitterName, wantEmail: DefaultCommitterEmail,
		},
		{
			name:     "env name alone",
			inName:   "",
			inEmail:  "explicit@example.com",
			envName:  "Env Person",
			envEmail: "",
			wantName: "Env Person", wantEmail: "explicit@example.com",
		},
		{
			name:     "env email alone",
			inName:   "Explicit Person",
			inEmail:  "",
			envName:  "",
			envEmail: "env@example.com",
			wantName: "Explicit Person", wantEmail: "env@example.com",
		},
		{
			// The ladder tests emptiness, not blankness: a whitespace-only
			// value is a resolved value, never trimmed or skipped. This pins
			// the server rule's exact semantics against a future "helpful"
			// TrimSpace.
			name:     "whitespace env value is kept, not treated as unset",
			inName:   "",
			inEmail:  "",
			envName:  " ",
			envEmail: " ",
			wantName: " ", wantEmail: " ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setGitAuthorEnv(t, tt.envName, tt.envEmail)
			got := ResolveCommitter(tt.inName, tt.inEmail)
			if got.Name != tt.wantName || got.Email != tt.wantEmail {
				t.Errorf("ResolveCommitter(%q, %q) = (%q, %q), want (%q, %q)",
					tt.inName, tt.inEmail, got.Name, got.Email, tt.wantName, tt.wantEmail)
			}
		})
	}
}

// TestCommitterAuthor pins the --author formatting DOLT_COMMIT and
// DOLT_MERGE parse: "Name <email>", with the angle brackets added by the
// formatter and never expected from the resolved fields themselves.
func TestCommitterAuthor(t *testing.T) {
	c := Committer{Name: "Grace Hopper", Email: "grace@navy.mil"}
	if got := c.Author(); got != "Grace Hopper <grace@navy.mil>" {
		t.Errorf("Committer.Author() = %q, want %q", got, "Grace Hopper <grace@navy.mil>")
	}
}

// TestResolveCommitterDefaultsMatchEmbeddedConstants guards the default
// identity shared across modes: embedded mode's historical constants
// (beads <beads@local>) are what the ladder falls to, so a rename here is a
// deliberate, visible change to every unattributed commit.
func TestResolveCommitterDefaultsMatchEmbeddedConstants(t *testing.T) {
	setGitAuthorEnv(t, "", "")
	if os.Getenv("GIT_AUTHOR_NAME") != "" || os.Getenv("GIT_AUTHOR_EMAIL") != "" {
		t.Fatalf("precondition: ambient GIT_AUTHOR_* leaked through t.Setenv")
	}
	got := ResolveCommitter("", "")
	if got.Name != "beads" || got.Email != "beads@local" {
		t.Errorf("default identity = (%q, %q), want (beads, beads@local)", got.Name, got.Email)
	}
}
