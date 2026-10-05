//go:build cgo

package main

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// if_revision_proxied_integration_test.go is the server-path (uow/proxied)
// twin of #7203's if_revision_embedded_test.go (37acca96d): that file only
// exercises --if-revision through buildEmbeddedBD's direct, non-proxied
// route. These tests exercise the same CLI guard through *_proxied_server.go's
// uow-backed preflight/apply path, against a real shared proxied dolt-server,
// to pin that gascity's code:"precondition_failed" + numeric
// expected_revision/current_revision body is identical across both legs, not
// just the embedded one -- including on `bd reopen`, which #7203 did not add
// --if-revision to at all (see reopen.go).
//
// Like every other *_proxied_integration_test.go in this package, this
// requires BEADS_TEST_PROXIED_SERVER=1 and a reachable Dolt container; it
// skips otherwise (see requireProxiedServerEnv).

// proxiedPreconditionBody mirrors #7203's own ifRevisionFailureBody
// (if_revision.go): numeric expected_revision/current_revision only, no
// decimal-string twins -- TestGCConditionalMatcherDecode already confirmed
// gc's matcher decodes this shape exactly (encoding/json decodes a JSON
// number straight into an int64 struct field with no float64 intermediate,
// so there is no precision loss to work around here).
type proxiedPreconditionBody struct {
	Code             string `json:"code"`
	ExpectedRevision *int64 `json:"expected_revision"`
	CurrentRevision  *int64 `json:"current_revision"`
}

func assertProxiedPreconditionBody(t *testing.T, stderr string, wantExpected int64) {
	t.Helper()
	obj := lastJSONObjectLine(stderr)
	if obj == "" {
		t.Fatalf("no JSON object line on stderr:\n%s", stderr)
	}
	var body proxiedPreconditionBody
	if err := json.Unmarshal([]byte(obj), &body); err != nil {
		t.Fatalf("parse error body: %v\nraw: %s", err, obj)
	}
	if body.Code != ifRevisionCodePreconditionFailed {
		t.Errorf("code = %q, want %q\nraw: %s", body.Code, ifRevisionCodePreconditionFailed, obj)
	}
	if body.ExpectedRevision == nil || *body.ExpectedRevision != wantExpected {
		t.Errorf("expected_revision = %v, want %d (JSON number, not string)\nraw: %s", body.ExpectedRevision, wantExpected, obj)
	}
	if body.CurrentRevision == nil {
		t.Errorf("current_revision is absent, want a value\nraw: %s", obj)
	}
}

func currentRevisionOfProxied(t *testing.T, bd, dir, id string) int64 {
	t.Helper()
	details := bdProxiedShowDetailsFirst(t, bd, dir, id)
	rev, ok := details["revision"].(string)
	if !ok || rev == "" {
		t.Fatalf("bd show %s --json has no non-empty revision field: %+v", id, details)
	}
	v, err := strconv.ParseInt(rev, 10, 64)
	if err != nil {
		t.Fatalf("bd show %s --json returned an unparseable revision %q: %v", id, rev, err)
	}
	return v
}

func staleRevisionFor(rev int64) int64 {
	stale := int64(999999999)
	if stale == rev {
		stale = 999999998
	}
	return stale
}

// TestIfRevisionGuardProxiedUpdate pins bd update --if-revision on the
// proxied-server leg: a stale token refuses with exit 13 and a
// precondition_failed JSON body naming numeric revisions, writing nothing;
// the matching token applies.
func TestIfRevisionGuardProxiedUpdate(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	proj := newSharedProxiedProject(t, bd, "piu")
	env := crossModeEnv{mode: "proxied", bd: bd, dir: proj.dir, env: bdProxiedEnv(proj.dir)}

	issue := bdProxiedCreate(t, bd, proj.dir, "Guarded proxied update", "--type", "task")
	rev := currentRevisionOfProxied(t, bd, proj.dir, issue.ID)

	t.Run("mismatch_refuses_and_writes_nothing", func(t *testing.T) {
		stale := staleRevisionFor(rev)
		stdout, stderr, code := env.run(t, "update", issue.ID, "--if-revision", strconv.FormatInt(stale, 10),
			"--priority", "3", "--json")
		if code != ExitGuardMismatch {
			t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
		}
		assertProxiedPreconditionBody(t, stderr, stale)
		got := bdProxiedShow(t, bd, proj.dir, issue.ID)
		if got.Priority == 3 {
			t.Errorf("stale --if-revision wrote the row: priority = %d", got.Priority)
		}
	})

	t.Run("match_applies", func(t *testing.T) {
		env.run(t, "update", issue.ID, "--if-revision", strconv.FormatInt(rev, 10), "--priority", "3")
		got := bdProxiedShow(t, bd, proj.dir, issue.ID)
		if got.Priority != 3 {
			t.Fatalf("matching --if-revision did not apply: priority = %d", got.Priority)
		}
	})
}

// TestIfRevisionGuardProxiedClose mirrors #7203's embedded close coverage
// against the proxied-server leg.
func TestIfRevisionGuardProxiedClose(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	proj := newSharedProxiedProject(t, bd, "pic")
	env := crossModeEnv{mode: "proxied", bd: bd, dir: proj.dir, env: bdProxiedEnv(proj.dir)}

	issue := bdProxiedCreate(t, bd, proj.dir, "Guarded proxied close", "--type", "task")
	rev := currentRevisionOfProxied(t, bd, proj.dir, issue.ID)

	t.Run("mismatch_refuses_and_leaves_open", func(t *testing.T) {
		stale := staleRevisionFor(rev)
		stdout, stderr, code := env.run(t, "close", issue.ID, "--if-revision", strconv.FormatInt(stale, 10), "--json")
		if code != ExitGuardMismatch {
			t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
		}
		assertProxiedPreconditionBody(t, stderr, stale)
		got := bdProxiedShow(t, bd, proj.dir, issue.ID)
		if got.Status != types.StatusOpen {
			t.Errorf("stale --if-revision close changed status: %s", got.Status)
		}
	})

	t.Run("match_closes", func(t *testing.T) {
		env.run(t, "close", issue.ID, "--if-revision", strconv.FormatInt(rev, 10))
		got := bdProxiedShow(t, bd, proj.dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Fatalf("matching --if-revision did not close: status = %s", got.Status)
		}
	})
}

// TestIfRevisionGuardProxiedReopen covers `bd reopen --if-revision` on the
// proxied-server leg: #7203 did not add --if-revision to reopen at all, so
// unlike the other four verbs there is no embedded-leg sibling to mirror
// here -- this is reopen's only --if-revision coverage on either route
// beyond TestGCConditionalMatcherDecode's embedded smoke case.
func TestIfRevisionGuardProxiedReopen(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	proj := newSharedProxiedProject(t, bd, "pir")
	env := crossModeEnv{mode: "proxied", bd: bd, dir: proj.dir, env: bdProxiedEnv(proj.dir)}

	issue := bdProxiedCreate(t, bd, proj.dir, "Guarded proxied reopen", "--type", "task")
	bdProxiedClose(t, bd, proj.dir, issue.ID)
	rev := currentRevisionOfProxied(t, bd, proj.dir, issue.ID)

	t.Run("mismatch_refuses_and_leaves_closed", func(t *testing.T) {
		stale := staleRevisionFor(rev)
		stdout, stderr, code := env.run(t, "reopen", issue.ID, "--if-revision", strconv.FormatInt(stale, 10), "--json")
		if code != ExitGuardMismatch {
			t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
		}
		assertProxiedPreconditionBody(t, stderr, stale)
		got := bdProxiedShow(t, bd, proj.dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("stale --if-revision reopen changed status: %s", got.Status)
		}
	})

	t.Run("match_reopens", func(t *testing.T) {
		env.run(t, "reopen", issue.ID, "--if-revision", strconv.FormatInt(rev, 10))
		got := bdProxiedShow(t, bd, proj.dir, issue.ID)
		if got.Status != types.StatusOpen {
			t.Fatalf("matching --if-revision did not reopen: status = %s", got.Status)
		}
	})
}

// TestIfRevisionGuardProxiedDelete mirrors #7203's embedded delete coverage
// against the proxied-server leg.
func TestIfRevisionGuardProxiedDelete(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	proj := newSharedProxiedProject(t, bd, "pid")
	env := crossModeEnv{mode: "proxied", bd: bd, dir: proj.dir, env: bdProxiedEnv(proj.dir)}

	issue := bdProxiedCreate(t, bd, proj.dir, "Guarded proxied delete", "--type", "task")
	rev := currentRevisionOfProxied(t, bd, proj.dir, issue.ID)

	t.Run("mismatch_refuses_and_leaves_issue", func(t *testing.T) {
		stale := staleRevisionFor(rev)
		stdout, stderr, code := env.run(t, "delete", issue.ID, "--if-revision", strconv.FormatInt(stale, 10), "--force", "--json")
		if code != ExitGuardMismatch {
			t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
		}
		assertProxiedPreconditionBody(t, stderr, stale)
		bdProxiedShow(t, bd, proj.dir, issue.ID)
	})

	t.Run("match_deletes", func(t *testing.T) {
		stdout, stderr, code := env.run(t, "delete", issue.ID, "--if-revision", strconv.FormatInt(rev, 10), "--force")
		if code != 0 {
			t.Fatalf("matching --if-revision delete failed: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if _, _, code := env.run(t, "show", issue.ID); code == 0 {
			t.Errorf("issue %s still resolves after matching --if-revision delete", issue.ID)
		}
	})
}

// TestIfRevisionGuardProxiedAssign mirrors #7203's embedded assign coverage
// against the proxied-server leg.
func TestIfRevisionGuardProxiedAssign(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	proj := newSharedProxiedProject(t, bd, "pia")
	env := crossModeEnv{mode: "proxied", bd: bd, dir: proj.dir, env: bdProxiedEnv(proj.dir)}

	issue := bdProxiedCreate(t, bd, proj.dir, "Guarded proxied assign", "--type", "task")
	rev := currentRevisionOfProxied(t, bd, proj.dir, issue.ID)

	t.Run("mismatch_refuses_and_leaves_unassigned", func(t *testing.T) {
		stale := staleRevisionFor(rev)
		stdout, stderr, code := env.run(t, "assign", issue.ID, "alice", "--if-revision", strconv.FormatInt(stale, 10), "--json")
		if code != ExitGuardMismatch {
			t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
		}
		assertProxiedPreconditionBody(t, stderr, stale)
		got := bdProxiedShow(t, bd, proj.dir, issue.ID)
		if got.Assignee != "" {
			t.Errorf("stale --if-revision assign wrote the row: assignee = %q", got.Assignee)
		}
	})

	t.Run("match_assigns", func(t *testing.T) {
		env.run(t, "assign", issue.ID, "alice", "--if-revision", strconv.FormatInt(rev, 10))
		got := bdProxiedShow(t, bd, proj.dir, issue.ID)
		if got.Assignee != "alice" {
			t.Fatalf("matching --if-revision did not assign: assignee = %q", got.Assignee)
		}
	})
}
