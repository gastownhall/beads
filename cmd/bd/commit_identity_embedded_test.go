//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedCommitIdentityHonorsGitAuthor pins embedded-mode commit
// authorship to the resolved identity (GIT_AUTHOR_*), as server mode does,
// rather than the hardcoded `beads <beads@local>` (#7628).
func TestEmbeddedCommitIdentityHonorsGitAuthor(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	// --prefix ci names the embedded database deterministically ("ci"); with
	// no prefix the database name derives from the temp dir.
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ci")

	// The create must commit as the environment's identity, not the store's
	// hardcoded default. bdEnv inherits GIT_AUTHOR_* from the ambient
	// environment, so strip and set them explicitly: the test is hermetic
	// whether or not the host exports them.
	env := append(envWithout(envWithout(bdEnv(dir), "GIT_AUTHOR_NAME"), "GIT_AUTHOR_EMAIL"),
		"GIT_AUTHOR_NAME=Repro Author", "GIT_AUTHOR_EMAIL=repro@example.com")
	cmd := exec.Command(bd, "create", "Identity probe", "--type", "task", "--json")
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd create failed: %v\n%s", err, out)
	}

	// Read the create commit's identity in-process (no bd process holds the
	// store). dolt_log's committer/email are the session identity the commit
	// was made under, which is the plane the repro asserts.
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), "ci", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	var committer, email string
	err = db.QueryRowContext(t.Context(),
		"SELECT committer, email FROM dolt_log WHERE message LIKE 'bd: create %' ORDER BY commit_order DESC LIMIT 1").
		Scan(&committer, &email)
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	if committer != "Repro Author" || email != "repro@example.com" {
		t.Errorf("embedded create commit identity = %q <%s>, want %q <%s>: embedded commits must carry GIT_AUTHOR_* rather than the hardcoded default (#7628)",
			committer, email, "Repro Author", "repro@example.com")
	}
}
