//go:build cgo

package embeddeddolt

import (
	"net/url"
	"strings"
	"testing"

	doltembed "github.com/dolthub/driver/v2"
)

func TestBuildDSN_SpacesInPath(t *testing.T) {
	// Regression test for #2920: paths with spaces must not be
	// percent-encoded (%20) because the Dolt driver's ParseDataSource
	// uses the path as a literal filesystem path.
	dir := "/Users/bbrenner/Documents/Scripting Projects/beads/.beads/embeddeddolt"
	dsn := buildDSN(dir, "beads")

	if !strings.HasPrefix(dsn, "file://") {
		t.Fatalf("DSN should start with file://, got %q", dsn)
	}

	if strings.Contains(dsn, "%20") {
		t.Errorf("DSN must not percent-encode spaces; got %q", dsn)
	}

	if !strings.Contains(dsn, "Scripting Projects") {
		t.Errorf("DSN must preserve literal spaces in path; got %q", dsn)
	}

	// Verify the path portion is between "file://" and "?"
	afterScheme := strings.TrimPrefix(dsn, "file://")
	qIdx := strings.Index(afterScheme, "?")
	if qIdx == -1 {
		t.Fatalf("DSN missing query parameters: %q", dsn)
	}
	pathPortion := afterScheme[:qIdx]
	if pathPortion != dir {
		t.Errorf("path portion = %q, want %q", pathPortion, dir)
	}
}

func TestBuildDSN_NoDatabase(t *testing.T) {
	dsn := buildDSN("/tmp/test", "")
	if strings.Contains(dsn, "database=") {
		t.Errorf("DSN should not contain database param when empty; got %q", dsn)
	}
}

func TestBuildDSN_WithDatabase(t *testing.T) {
	dsn := buildDSN("/tmp/test", "mydb")
	if !strings.Contains(dsn, "database=mydb") {
		t.Errorf("DSN should contain database=mydb; got %q", dsn)
	}
}

// dsnCommitIdentity returns the commit name and email buildDSN put in the DSN.
func dsnCommitIdentity(t *testing.T, dsn string) (name, email string) {
	t.Helper()
	q, err := url.ParseQuery(dsn[strings.Index(dsn, "?")+1:])
	if err != nil {
		t.Fatalf("parse DSN query %q: %v", dsn, err)
	}
	return q.Get(doltembed.CommitNameParam), q.Get(doltembed.CommitEmailParam)
}

// TestBuildDSN_CommitIdentityFromGitAuthorEnv: the DSN carries the same
// identity the store authors its commits with (GH#7628).
func TestBuildDSN_CommitIdentityFromGitAuthorEnv(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Alice Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "alice@example.com")
	name, email := dsnCommitIdentity(t, buildDSN("/tmp/test", "mydb"))
	if name != "Alice Author" || email != "alice@example.com" {
		t.Errorf("DSN commit identity = %q <%q>, want \"Alice Author\" <\"alice@example.com\">", name, email)
	}
}

func TestBuildDSN_CommitIdentityNormalizedOrDefault(t *testing.T) {
	cases := []struct {
		name, envName, envEmail string
		wantName, wantEmail     string
	}{
		{"normalized", " Alice (bot)\n<x> ", " <alice@example.com>\t", "Alice bot x", "alice@example.com"},
		{"empty", "", "", "beads", "beads@local"},
		{"empty once cleaned", " <()> ", "\n", "beads", "beads@local"},
		{"independent", "Alice Author", "", "Alice Author", "beads@local"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GIT_AUTHOR_NAME", tc.envName)
			t.Setenv("GIT_AUTHOR_EMAIL", tc.envEmail)
			name, email := dsnCommitIdentity(t, buildDSN("/tmp/test", "mydb"))
			if name != tc.wantName || email != tc.wantEmail {
				t.Errorf("DSN commit identity = %q <%q>, want %q <%q>", name, email, tc.wantName, tc.wantEmail)
			}
		})
	}
}
