//go:build cgo

package embeddeddolt

import (
	"net/url"
	"strings"
	"testing"
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

// TestBuildDSN_CommitIdentityFromGitAuthorEnv pins GH#7628 AC-7: the DSN's
// commit identity follows GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL when set —
// normalized like every other identity field — and keeps the anonymous
// beads <beads@local> pair otherwise. The issue's triage showed this DSN
// identity losing to the SQL client identity for DOLT_COMMIT, which is why
// the commit paths now pass --author explicitly; the DSN keeps naming the
// engine's own unauthored work (schema migrations and their like) honestly.
func TestBuildDSN_CommitIdentityFromGitAuthorEnv(t *testing.T) {
	// The query is url-encoded (spaces, @), so read it parsed, not raw.
	assertDSNIdentity := func(t *testing.T, dsn, wantName, wantEmail string) {
		t.Helper()
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse DSN %q: %v", dsn, err)
		}
		q := u.Query()
		if got := q.Get("commitname"); got != wantName {
			t.Errorf("DSN commitname = %q, want %q (dsn %q)", got, wantName, dsn)
		}
		if got := q.Get("commitemail"); got != wantEmail {
			t.Errorf("DSN commitemail = %q, want %q (dsn %q)", got, wantEmail, dsn)
		}
	}

	t.Run("env identity", func(t *testing.T) {
		t.Setenv("GIT_AUTHOR_NAME", "Alice Author")
		t.Setenv("GIT_AUTHOR_EMAIL", "alice@example.com")
		assertDSNIdentity(t, buildDSN("/tmp/test", "mydb"), "Alice Author", "alice@example.com")
	})

	t.Run("hostile identity is normalized", func(t *testing.T) {
		t.Setenv("GIT_AUTHOR_NAME", "Alice (bot)\n<x>")
		t.Setenv("GIT_AUTHOR_EMAIL", "alice@example.com")
		assertDSNIdentity(t, buildDSN("/tmp/test", "mydb"), "Alice bot x", "alice@example.com")
	})

	t.Run("anonymous default", func(t *testing.T) {
		t.Setenv("GIT_AUTHOR_NAME", "")
		t.Setenv("GIT_AUTHOR_EMAIL", "")
		assertDSNIdentity(t, buildDSN("/tmp/test", "mydb"), "beads", "beads@local")
	})
}
