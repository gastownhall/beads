//go:build cgo

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// `bd delete --if-threads` on all three routes: the direct route (a `bd init
// --server` workspace, a DoltStore against a sql-server), the embedded route,
// and the proxied route. Every case drives the real binary, so the flag rules,
// the exit code and the stderr report are the ones a caller sees, and every
// case seeds its own thread under its own tag, so one workspace per route
// serves them all.

const (
	ifThreadsMailLabel = "thread-mail"
	ifThreadsKeepLabel = "thread-keep"
)

// ifThreadsWorkspace is one workspace on one route.
type ifThreadsWorkspace struct {
	bd  string
	dir string
	env []string
	// database and sqlDB reach the workspace's sql-server database, for the
	// lock test. Both are empty on the embedded route, which has no server.
	database string
	sqlDB    *sql.DB
}

// ifThreadsRoutes returns the routes this run can reach, each with a fresh
// workspace. The embedded route needs BEADS_TEST_EMBEDDED_DOLT=1 and the two
// server routes BEADS_TEST_PROXIED_SERVER=1, as their neighbours' tests do; a
// route this run cannot reach is a skipped subtest, not a missing one.
func ifThreadsRoutes() []struct {
	name string
	open func(t *testing.T) ifThreadsWorkspace
} {
	return []struct {
		name string
		open func(t *testing.T) ifThreadsWorkspace
	}{
		{"direct", openIfThreadsDirect},
		{"embedded", openIfThreadsEmbedded},
		{"proxied", openIfThreadsProxied},
	}
}

func openIfThreadsDirect(t *testing.T) ifThreadsWorkspace {
	t.Helper()
	port := requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "it")
	db, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?parseTime=true", port, p.database))
	if err != nil {
		t.Fatalf("open %s: %v", p.database, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return ifThreadsWorkspace{bd: bd, dir: p.dir, env: p.env, database: p.database, sqlDB: db}
}

func openIfThreadsEmbedded(t *testing.T) ifThreadsWorkspace {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "it")
	return ifThreadsWorkspace{bd: bd, dir: dir, env: bdEnv(dir)}
}

func openIfThreadsProxied(t *testing.T) ifThreadsWorkspace {
	t.Helper()
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "it")
	return ifThreadsWorkspace{bd: bd, dir: p.dir, env: bdProxiedEnv(p.dir), database: p.database, sqlDB: openProxiedDB(t, p)}
}

// ifThreadsRun is one finished bd invocation.
type ifThreadsRun struct {
	stdout, stderr string
	code           int
}

func (w ifThreadsWorkspace) run(t *testing.T, args ...string) ifThreadsRun {
	t.Helper()
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = w.dir
	cmd.Env = w.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("bd %s: %v", strings.Join(args, " "), err)
		}
		code = exitErr.ExitCode()
	}
	return ifThreadsRun{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// runAsync is run for a goroutine, which may not fail the test: an error
// starting bd comes back as exit -1 with the error on stderr.
func (w ifThreadsWorkspace) runAsync(args ...string) ifThreadsRun {
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = w.dir
	cmd.Env = w.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return ifThreadsRun{stdout: stdout.String(), stderr: stderr.String() + err.Error(), code: -1}
		}
		code = exitErr.ExitCode()
	}
	return ifThreadsRun{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// ifThreadsLockWaiters counts the sessions, other than the caller's own,
// blocked in a GET_LOCK statement on w's database. It is what the race test
// waits on, and the race test first requires it to read zero while the test
// holds the lock and no command has started, so a count that included the
// observer or the holder fails there.
func ifThreadsLockWaiters(t *testing.T, ctx context.Context, w ifThreadsWorkspace) int {
	t.Helper()
	var waiting int
	if err := w.sqlDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.PROCESSLIST "+
			"WHERE DB = ? AND ID <> CONNECTION_ID() AND INFO LIKE 'SELECT GET_LOCK(%'",
		w.database).Scan(&waiting); err != nil {
		t.Fatalf("read waiters: %v", err)
	}
	return waiting
}

func (w ifThreadsWorkspace) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	r := w.run(t, args...)
	if r.code != 0 {
		t.Fatalf("bd %s exited %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r.stdout
}

// mail creates one closed wisp titled name carrying labels.
func (w ifThreadsWorkspace) mail(t *testing.T, name string, labels ...string) string {
	t.Helper()
	args := []string{"create", "--json", "--ephemeral", "-t", "task", name}
	if len(labels) > 0 {
		args = append(args, "--labels", strings.Join(labels, ","))
	}
	id := parseIssueJSON(t, []byte(w.mustRun(t, args...))).ID
	w.mustRun(t, "close", id)
	return id
}

func (w ifThreadsWorkspace) reply(t *testing.T, from, to string) {
	t.Helper()
	w.mustRun(t, "dep", "add", from, to, "--type", string(types.DepRepliesTo))
}

// ifThreadsThread is a three-message thread, m2 replying to m1 replying to
// root, every message a closed thread-mail wisp unless the seed said
// otherwise.
type ifThreadsThread struct {
	root, m1, m2 string
}

func (th ifThreadsThread) ids() []string { return []string{th.root, th.m1, th.m2} }

func (w ifThreadsWorkspace) thread(t *testing.T, tag string, m2Labels ...string) ifThreadsThread {
	t.Helper()
	if m2Labels == nil {
		m2Labels = []string{ifThreadsMailLabel}
	}
	th := ifThreadsThread{
		root: w.mail(t, tag+" root", ifThreadsMailLabel),
		m1:   w.mail(t, tag+" m1", ifThreadsMailLabel),
		m2:   w.mail(t, tag+" m2", m2Labels...),
	}
	w.reply(t, th.m1, th.root)
	w.reply(t, th.m2, th.m1)
	return th
}

// images is `bd image` of ids, keyed by id: the whole stored image line, so two
// reads compare every row of every bead.
func (w ifThreadsWorkspace) images(t *testing.T, ids ...string) map[string]string {
	t.Helper()
	out := w.mustRun(t, append([]string{"image"}, ids...)...)
	images := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var img struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &img); err != nil {
			t.Fatalf("parse bd image line %q: %v", line, err)
		}
		images[img.ID] = line
	}
	return images
}

// archive is what a caller does before deleting: image the thread with `bd
// image` and write the guard file that names each member's revision.
func (w ifThreadsWorkspace) archive(t *testing.T, cutoff time.Time, th ifThreadsThread) string {
	t.Helper()
	thread := publicops.GuardedThread{Root: th.root}
	images := w.images(t, th.ids()...)
	for _, id := range th.ids() {
		var img publicops.Image
		if err := json.Unmarshal([]byte(images[id]), &img); err != nil {
			t.Fatalf("parse image of %s: %v", id, err)
		}
		thread.Members = append(thread.Members, publicops.GuardedMember{ID: id, Revision: img.Revision})
	}
	guard := publicops.ThreadGuard{
		Cutoff:       cutoff,
		MemberLabel:  ifThreadsMailLabel,
		ExcludeLabel: ifThreadsKeepLabel,
		Edge:         string(types.DepRepliesTo),
		Threads:      []publicops.GuardedThread{thread},
	}
	data, err := json.Marshal(guard)
	if err != nil {
		t.Fatalf("marshal guard: %v", err)
	}
	path := filepath.Join(t.TempDir(), "guard.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write guard: %v", err)
	}
	return path
}

func ifThreadsIDsFile(t *testing.T, ids ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ids.txt")
	if err := os.WriteFile(path, []byte(strings.Join(ids, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write ids: %v", err)
	}
	return path
}

// guardDelete runs the guarded delete of ids, with extra flags.
func (w ifThreadsWorkspace) guardDelete(t *testing.T, guard string, ids []string, extra ...string) ifThreadsRun {
	t.Helper()
	args := append([]string{"delete", "--json", "--from-file", ifThreadsIDsFile(t, ids...), "--if-threads", guard}, extra...)
	return w.run(t, args...)
}

// ifThreadsMismatch is the JSON error line a refused guarded delete ends
// stderr with.
type ifThreadsMismatch struct {
	Error         string                         `json:"error"`
	FailedThreads []publicops.ThreadGuardFailure `json:"failed_threads"`
	GuardMismatch bool                           `json:"guard_mismatch"`
}

// requireGuardRefusal checks r is the refusal: exit 13, the last stderr line
// the JSON report naming check on root, nothing on stdout.
func requireGuardRefusal(t *testing.T, r ifThreadsRun, root, check string) {
	t.Helper()
	if r.code != ExitGuardMismatch {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, ExitGuardMismatch, r.stdout, r.stderr)
	}
	lines := strings.Split(strings.TrimSpace(r.stderr), "\n")
	var got ifThreadsMismatch
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &got); err != nil {
		t.Fatalf("last stderr line is not the JSON report: %v\nstderr:\n%s", err, r.stderr)
	}
	if !got.GuardMismatch || !strings.Contains(got.Error, "thread guard mismatch") {
		t.Errorf("report = %+v, want guard_mismatch true and the mismatch error", got)
	}
	found := false
	for _, f := range got.FailedThreads {
		if f.Root == root && f.Check == check && len(f.IDs) > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("failed_threads = %+v, want a %q failure on thread %s", got.FailedThreads, check, root)
	}
	if strings.TrimSpace(r.stdout) != "" {
		t.Errorf("a refused delete printed on stdout: %s", r.stdout)
	}
}

// requireUnchanged checks that ids image exactly as before: no row of any of
// them, its labels, comments, events or edges, changed.
func (w ifThreadsWorkspace) requireUnchanged(t *testing.T, before map[string]string, ids ...string) {
	t.Helper()
	if after := w.images(t, ids...); !reflect.DeepEqual(after, before) {
		t.Errorf("a refused delete changed rows:\nbefore: %v\nafter:  %v", before, after)
	}
}

func (w ifThreadsWorkspace) requireGone(t *testing.T, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if r := w.run(t, "image", id); r.code == 0 {
			t.Errorf("%s survived the delete: %s", id, r.stdout)
		}
	}
}

func TestDeleteIfThreadsRoutes(t *testing.T) {
	for _, route := range ifThreadsRoutes() {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			w := route.open(t)
			runIfThreadsCases(t, w)
		})
	}
}

func runIfThreadsCases(t *testing.T, w ifThreadsWorkspace) {
	later := time.Now().Add(time.Hour)

	t.Run("PassesAndDeletesExactlyTheIDs", func(t *testing.T) {
		th := w.thread(t, "pass")
		bystander := w.mail(t, "pass bystander", ifThreadsMailLabel)
		guard := w.archive(t, later, th)
		r := w.guardDelete(t, guard, th.ids(), "--force")
		if r.code != 0 {
			t.Fatalf("guarded delete exited %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
		}
		var got struct {
			Deleted      []string `json:"deleted"`
			DeletedCount int      `json:"deleted_count"`
		}
		if err := json.Unmarshal([]byte(r.stdout[strings.Index(r.stdout, "{"):]), &got); err != nil {
			t.Fatalf("parse delete JSON: %v\n%s", err, r.stdout)
		}
		if !reflect.DeepEqual(got.Deleted, th.ids()) || got.DeletedCount != 3 {
			t.Errorf("deleted = %v (%d), want exactly %v", got.Deleted, got.DeletedCount, th.ids())
		}
		w.requireGone(t, th.ids()...)
		if r := w.run(t, "image", bystander); r.code != 0 {
			t.Errorf("bystander %s did not survive: %s", bystander, r.stderr)
		}
	})

	t.Run("StaleRevisionExits13", func(t *testing.T) {
		th := w.thread(t, "stale")
		guard := w.archive(t, later, th)
		w.mustRun(t, "comments", "add", th.m2, "a late edit")
		before := w.images(t, th.ids()...)
		requireGuardRefusal(t, w.guardDelete(t, guard, th.ids(), "--force"), th.root, publicops.ThreadGuardRevision)
		w.requireUnchanged(t, before, th.ids()...)
	})

	t.Run("OutsideReplyExits13", func(t *testing.T) {
		th := w.thread(t, "outside")
		guard := w.archive(t, later, th)
		outsider := w.mail(t, "outside late reply", ifThreadsMailLabel)
		w.reply(t, outsider, th.m1)
		ids := append(th.ids(), outsider)
		before := w.images(t, ids...)
		requireGuardRefusal(t, w.guardDelete(t, guard, th.ids(), "--force"), th.root, publicops.ThreadGuardMembership)
		w.requireUnchanged(t, before, ids...)
	})

	t.Run("MissingMemberLabelExits13", func(t *testing.T) {
		th := w.thread(t, "unlabelled", "other")
		guard := w.archive(t, later, th)
		before := w.images(t, th.ids()...)
		requireGuardRefusal(t, w.guardDelete(t, guard, th.ids(), "--force"), th.root, publicops.ThreadGuardEligibility)
		w.requireUnchanged(t, before, th.ids()...)
	})

	t.Run("ClosedAfterCutoffExits13", func(t *testing.T) {
		th := w.thread(t, "late")
		guard := w.archive(t, time.Now().Add(-time.Hour), th)
		before := w.images(t, th.ids()...)
		requireGuardRefusal(t, w.guardDelete(t, guard, th.ids(), "--force"), th.root, publicops.ThreadGuardEligibility)
		w.requireUnchanged(t, before, th.ids()...)
	})

	t.Run("DryRunWithABadGuardExits13", func(t *testing.T) {
		th := w.thread(t, "dryrun")
		guard := w.archive(t, later, th)
		w.mustRun(t, "comments", "add", th.root, "a late edit")
		before := w.images(t, th.ids()...)
		requireGuardRefusal(t, w.guardDelete(t, guard, th.ids(), "--force", "--dry-run"), th.root, publicops.ThreadGuardRevision)
		w.requireUnchanged(t, before, th.ids()...)
	})

	t.Run("NeedsForceAndRefusesCascade", func(t *testing.T) {
		th := w.thread(t, "flags")
		guard := w.archive(t, later, th)
		before := w.images(t, th.ids()...)
		for _, tc := range []struct {
			name  string
			extra []string
			want  string
		}{
			{"without --force", nil, "--if-threads requires --force"},
			{"with --cascade", []string{"--force", "--cascade"}, "cannot be combined with --cascade"},
		} {
			r := w.guardDelete(t, guard, th.ids(), tc.extra...)
			if r.code != 1 || !strings.Contains(r.stdout+r.stderr, tc.want) {
				t.Errorf("%s: exit %d, want 1 naming %q\nstdout:\n%s\nstderr:\n%s", tc.name, r.code, tc.want, r.stdout, r.stderr)
			}
		}
		w.requireUnchanged(t, before, th.ids()...)
	})

	// An explicitly empty path is a guarded delete with no guard, and is
	// refused before the deleter runs: a purge passing `--if-threads
	// "$guard_file"` with the variable unset must not get an unguarded forced
	// delete. Both spellings, with --force and with --cascade too.
	t.Run("EmptyPathIsRefused", func(t *testing.T) {
		th := w.thread(t, "empty")
		ids := ifThreadsIDsFile(t, th.ids()...)
		before := w.images(t, th.ids()...)
		for _, tc := range []struct {
			name string
			args []string
		}{
			{"separate empty argument", []string{"--if-threads", "", "--force"}},
			{"empty after =", []string{"--if-threads=", "--force"}},
			{"empty with --cascade", []string{"--if-threads", "", "--force", "--cascade"}},
			{"empty without --force", []string{"--if-threads="}},
		} {
			r := w.run(t, append([]string{"delete", "--json", "--from-file", ids}, tc.args...)...)
			if r.code == 0 || !strings.Contains(r.stdout+r.stderr, "--if-threads needs a guard file path") {
				t.Errorf("%s: exit %d, want a refusal naming the empty path\nstdout:\n%s\nstderr:\n%s", tc.name, r.code, r.stdout, r.stderr)
			}
		}
		w.requireUnchanged(t, before, th.ids()...)
	})
}

// TestDeleteIfThreadsSerialisesWithAComment pins that a guarded delete and a
// concurrent comment on a thread member cannot both land, with the events
// journal off (the default): both write a bead table, so both queue on the
// journal writer lock (beads#7645), and the guard runs inside the delete's
// transaction. The test holds the lock itself, queues the two commands on it
// one after the other, then lets them go. Whichever takes the lock first, the
// other must see it: the comment lands first and the delete finds the changed
// revision and exits 13, or the delete lands first and the comment finds its
// bead gone. A comment landing under a delete that reports success is the
// failure. Each route runs both queue orders.
//
// It needs the journal writer lock of beads#7645: until that lands no bd
// writer takes the lock, the test sees no waiter, and it is skipped. Remove the
// skip with #7645.
func TestDeleteIfThreadsSerialisesWithAComment(t *testing.T) {
	t.Skip("needs the journal writer lock of beads#7645")
	for _, route := range []struct {
		name string
		open func(t *testing.T) ifThreadsWorkspace
	}{
		{"direct", openIfThreadsDirect},
		{"proxied", openIfThreadsProxied},
	} {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			w := route.open(t)
			t.Run("DeleteQueuedFirst", func(t *testing.T) { raceGuardedDeleteWithComment(t, w, "racedel", false) })
			t.Run("CommentQueuedFirst", func(t *testing.T) { raceGuardedDeleteWithComment(t, w, "racecom", true) })
		})
	}
}

// ifThreadsJournalWriterLockName is the server-wide GET_LOCK name bead writers
// on database queue on: the body of issueops.JournalWriterLockName, which
// arrives with the journal writer lock in beads#7645. Until then no bd writer
// takes this lock, so TestDeleteIfThreadsSerialisesWithAComment cannot see a
// waiter; replace this with the issueops function when #7645 lands.
func ifThreadsJournalWriterLockName(database string) string {
	return "bd_events_seq:" + database
}

func raceGuardedDeleteWithComment(t *testing.T, w ifThreadsWorkspace, tag string, commentFirst bool) {
	t.Helper()
	ctx := context.Background()
	th := w.thread(t, tag)
	guard := w.archive(t, time.Now().Add(time.Hour), th)

	conn, err := w.sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("lock session: %v", err)
	}
	defer conn.Close()
	lockName := ifThreadsJournalWriterLockName(w.database)
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 5)", lockName).Scan(&got); err != nil || got.Int64 != 1 {
		t.Fatalf("hold %s: got=%v err=%v", lockName, got, err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			_, _ = conn.ExecContext(ctx, "SELECT RELEASE_LOCK(?)", lockName)
		}
	}
	defer release()

	// Every child is awaited before the test returns, on a failure too: the
	// deferred release lets any queued command through, and this wait reaps it
	// so no bd process outlives the workspace it writes to.
	var children sync.WaitGroup
	defer func() {
		release()
		children.Wait()
	}()
	start := func(args ...string) <-chan ifThreadsRun {
		done := make(chan ifThreadsRun, 1)
		children.Add(1)
		go func() {
			defer children.Done()
			done <- w.runAsync(args...)
		}()
		return done
	}

	// requireWaiters returns once exactly n sessions other than this test's
	// own are blocked in a writer-lock GET_LOCK on the server, and fails if
	// more ever are. The observer's own query is excluded twice over: by its
	// connection id and because it is not a GET_LOCK statement.
	requireWaiters := func(n int) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for {
			waiting := ifThreadsLockWaiters(t, ctx, w)
			if waiting > n {
				t.Fatalf("%d sessions waiting on a lock, want %d", waiting, n)
			}
			if waiting == n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("only %d session(s) waiting on a lock after 45s, want %d", waiting, n)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	requireWaiters(0)
	deleteArgs := []string{"delete", "--json", "--from-file", ifThreadsIDsFile(t, th.ids()...), "--if-threads", guard, "--force"}
	commentArgs := []string{"comments", "add", th.m1, "a reply racing the purge"}
	var deleted, commented <-chan ifThreadsRun
	if commentFirst {
		commented = start(commentArgs...)
		requireWaiters(1)
		deleted = start(deleteArgs...)
	} else {
		deleted = start(deleteArgs...)
		requireWaiters(1)
		commented = start(commentArgs...)
	}
	requireWaiters(2)
	release()

	del, com := <-deleted, <-commented
	t.Logf("delete exit %d, comment exit %d", del.code, com.code)
	switch {
	case del.code == 0 && com.code != 0:
		// The delete took the lock first: the comment found its bead gone.
		if !strings.Contains(strings.ToLower(com.stdout+com.stderr), "not found") {
			t.Errorf("the comment failed for another reason than its bead being gone:\nstdout:\n%s\nstderr:\n%s", com.stdout, com.stderr)
		}
		w.requireGone(t, th.ids()...)
	case del.code == ExitGuardMismatch && com.code == 0:
		// The comment took it first: the delete saw m1's new revision and refused.
		requireGuardRefusal(t, del, th.root, publicops.ThreadGuardRevision)
		if out := w.mustRun(t, "comments", th.m1, "--json"); !strings.Contains(out, "a reply racing the purge") {
			t.Errorf("the comment that landed is missing from %s: %s", th.m1, out)
		}
	default:
		t.Fatalf("delete exit %d and comment exit %d: want exactly one to land\ndelete stdout:\n%s\ndelete stderr:\n%s\ncomment stdout:\n%s\ncomment stderr:\n%s",
			del.code, com.code, del.stdout, del.stderr, com.stdout, com.stderr)
	}
}
