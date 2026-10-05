//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_harness_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The SERVED-SURFACE conformance harness: client → in-process bd serve →
// reference store.
//
// This is the composition design D8's lane row specifies, and it is what a
// per-role contract binds against. The binding rule is the one composition_test
// states and checkFixtureBinding enforces: SEEDING NEVER GOES THROUGH THE
// CLIENT. Every fixture below takes its seed and post-state hooks from the
// reference store the server serves, and binds only the ROLE under test to the
// http client. A fixture that seeded through the client would prove that the
// client agrees with itself.
//
// Bare RunAll is deliberately absent. Its Factory contract wants a write-ready
// store and its cases seed through raw CreateIssue and SetConfig, both of which
// this backend refuses — so it would prove nothing about a partial backend
// except that it is partial.
//
// One environment per case. Standing up an embedded engine and an HTTP listener
// per case costs about a second; sharing one across cases would make an id
// collision between two contracts a debugging session instead of a rename, and
// the contracts namespace their ids by prefix precisely so they can be run this
// way.

// servedEnv is one client-server-store triple and the hooks a fixture needs.
type servedEnv struct {
	reference *embeddeddolt.EmbeddedDoltStore
	subject   *Store
	dataDir   string
	database  string
	prefix    string
}

const servedDatabase = "httpconf"

// servedProjectID is the identity the in-process server publishes in its
// handshake. Most fixtures leave the client's ExpectProjectID empty and never
// consult it, but the wrong-server cases (served_wrong_server_test.go) pin the
// client against this value — one matching it, one not — to drive the
// project-identity gate (ga-b8ddd.11).
const servedProjectID = "proj-httpconf"

// skipUnlessEmbeddedDolt gates the tier on the engine it needs.
//
// It is a skip rather than a failure for a DEVELOPER, for the reason the
// embedded backend's own conformance wiring skips: the engine is a cgo-linked
// build-time capability, not a service the environment forgot to start.
//
// It is a FAILURE for the lane that stands the tier up. BEADS_HTTP_TEST_REQUIRED=1
// is how scripts/conformance.sh says "this tier is the required home of the
// served surface" — the same shape as BEADS_PG_TEST_REQUIRED for the live
// PostgreSQL tier and BEADS_TEST_EMBEDDED_DOLT for the oracle. Without it, a
// dropped env key in the workflow, or a rename of the variable, would restore
// exactly the state this wiring exists to end: two hundred served cases
// skipping themselves while the gate reports success.
//
// The per-test `--- PASS:` greps in the lane are the second layer, not the
// first: they catch a tier that stopped being invoked at all, which no
// in-process check can see.
func skipUnlessEmbeddedDolt(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") == "1" {
		return
	}
	if os.Getenv("BEADS_HTTP_TEST_REQUIRED") == "1" {
		t.Fatal("BEADS_HTTP_TEST_REQUIRED=1 but BEADS_TEST_EMBEDDED_DOLT is not 1; " +
			"the served-surface conformance tier is not enforced")
	}
	t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run the served-surface conformance tier")
}

// newServedEnv boots a reference store, serves its roles over an in-process
// listener, and points an http client store at the bound address. The client
// pins no expected identity, so the project-identity gate is inert — which is
// what every role contract below wants, since none of them is about identity.
//
// The optional config tweaks run against the served Config after the roles are
// bound and before Listen, so a case can slow the watch cadence or serve a
// journal-OFF workspace without a second harness. They are variadic so the many
// callers that need none stay unchanged.
func newServedEnv(t *testing.T, prefix string, opts ...func(*httpapi.Config)) *servedEnv {
	t.Helper()
	return newServedEnvExpecting(t, prefix, "", opts...)
}

// newServedEnvExpecting is newServedEnv with the client's ExpectProjectID
// pinned. The wrong-server cases use it to arm the identity gate the plain
// harness deliberately leaves off (ga-b8ddd.11).
func newServedEnvExpecting(t *testing.T, prefix, expectID string, opts ...func(*httpapi.Config)) *servedEnv {
	t.Helper()
	skipUnlessEmbeddedDolt(t)

	beadsDir := t.TempDir()
	ctx := t.Context()
	reference, err := embeddeddolt.Open(ctx, beadsDir, servedDatabase, "main")
	if err != nil {
		t.Fatalf("open the reference store: %v", err)
	}
	t.Cleanup(func() { _ = reference.Close() })
	if err := reference.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		t.Fatalf("set the issue prefix: %v", err)
	}

	cfg := serveConfig(t, reference)
	for _, opt := range opts {
		opt(&cfg)
	}
	srv, err := httpapi.Listen(cfg)
	if err != nil {
		t.Fatalf("bind the in-process server: %v", err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the in-process server did not shut down")
		}
	})

	base, err := url.Parse("http://" + srv.Addr())
	if err != nil {
		t.Fatalf("parse the bound address: %v", err)
	}
	client, err := wire.New(base, nil, wire.Options{ExpectProjectID: expectID})
	if err != nil {
		t.Fatalf("build the client: %v", err)
	}

	// ExpectProjectID is the identity pin `bd connect` records and D6 checks.
	// The plain harness leaves it empty so the gate is inert for the role
	// contracts; the wrong-server cases pass a value to arm it.
	subject := New(Target{BaseURL: base}, client, nil)
	return &servedEnv{
		reference: reference,
		subject:   subject,
		// The SQL handle addresses the engine's data directory, which the
		// store creates one level under the workspace. Passing beadsDir here
		// finds no database at all.
		dataDir:  filepath.Join(beadsDir, "embeddeddolt"),
		database: servedDatabase,
		prefix:   prefix,
	}
}

// serveConfig binds every served role to the reference store.
//
// Every one of them is taken through the store's own capability accessor rather
// than a constructor: a store that stopped offering a role is the regression
// this catches, and a constructor call would hide it. The server refuses a
// partial role set outright, which is why there is no shortcut here.
func serveConfig(t *testing.T, s storage.DoltStorage) httpapi.Config {
	t.Helper()
	cfg := httpapi.Config{
		Addr:   "127.0.0.1:0",
		Stdout: io.Discard,
		Stderr: io.Discard,
		// The handshake's published identity. Role contracts never read it, but
		// the wrong-server cases compare the client's ExpectProjectID against it.
		Workspace: domain.ContextInfo{ProjectID: servedProjectID, Database: servedDatabase},
	}

	var err error
	fail := func(name string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s(): %v", name, err)
		}
	}
	cfg.Reader, err = s.IssueReader()
	fail("IssueReader")
	cfg.Claimer, err = s.IssueClaimer()
	fail("IssueClaimer")
	cfg.ReadyClaimer, err = s.ReadyClaimer()
	fail("ReadyClaimer")
	cfg.Releaser, err = s.Releaser()
	fail("Releaser")
	cfg.Lifecycle, err = s.IssueLifecycle()
	fail("IssueLifecycle")
	cfg.Settings, err = s.WorkspaceConfig()
	fail("WorkspaceConfig")
	cfg.Stats, err = s.StatsReporter()
	fail("StatsReporter")
	cfg.CycleDetector, err = s.CycleDetector()
	fail("CycleDetector")
	cfg.EdgeReader, err = s.EdgeReader()
	fail("EdgeReader")
	cfg.GraphCounter, err = s.GraphCounter()
	fail("GraphCounter")
	cfg.Relations, err = s.IssueRelations()
	fail("IssueRelations")
	cfg.Commenter, err = s.Commenter()
	fail("Commenter")
	cfg.BlockingAnnotator, err = s.BlockingAnnotator()
	fail("BlockingAnnotator")
	cfg.TreeWalker, err = s.TreeWalker()
	fail("TreeWalker")
	cfg.ReadyCounter, err = s.ReadyCounter()
	fail("ReadyCounter")
	cfg.Counter, err = s.Counter()
	fail("Counter")
	cfg.Querier, err = s.Querier()
	fail("Querier")
	cfg.Sweeper, err = s.Sweeper()
	fail("Sweeper")
	cfg.Deleter, err = s.Deleter()
	fail("Deleter")
	cfg.BatchCreator, err = s.BatchCreator()
	fail("BatchCreator")
	cfg.BatchCloser, err = s.BatchCloser()
	fail("BatchCloser")
	cfg.DependencyEditor, err = s.DependencyEditor()
	fail("DependencyEditor")
	cfg.MetadataCAS, err = s.MetadataCAS()
	fail("MetadataCAS")
	cfg.BatchApplier, err = s.BatchApplier()
	fail("BatchApplier")
	cfg.Memories, err = s.Memories()
	fail("Memories")

	// THE JOURNAL IS NOT AN ACCESSOR, alone among the roles above: it is reached
	// by TYPE ASSERTION, because it is not on storage.DoltStorage and a backend
	// is free not to implement it at all. This is verbatim the expression
	// cmd/bd's serveJournalCursor makes.
	//
	// EventsJournalEnabled is hard-set rather than resolved, and it has to be:
	// the flag is read per REQUEST off this config, the store's own
	// SetEventsJournalEnabled is what decides whether anything is RECORDED, and
	// the Journal fixture flips that after Listen has already bound. A config
	// resolved from the workspace would leave every read a 409. Serving with it
	// on costs the other contracts nothing — no rows exist until a case asks for
	// them — and Listen refuses to bind an enabled workspace with no reader, so
	// the two lines travel together.
	cursor, ok := any(s).(storage.EventsJournalCursor)
	if !ok {
		t.Fatalf("%T does not implement storage.EventsJournalCursor; the served journal has nothing to read", s)
	}
	cfg.EventsJournal = cursor
	cfg.EventsJournalEnabled = true
	return cfg
}

// The reference store's roles, seeded through the store rather than the client.

func (e *servedEnv) createIssue(ctx context.Context, issue *types.Issue, actor string) error {
	return e.reference.CreateIssue(ctx, issue, actor)
}

// createWisp seeds into the EPHEMERAL plane. The flag is set here rather than by
// the caller because the contracts hand the same issue value to both hooks and
// expect the hook to decide the plane.
func (e *servedEnv) createWisp(ctx context.Context, issue *types.Issue, actor string) error {
	// A NoHistory bead is routed into the wisps plane by the CREATE verb
	// itself, and upstream (#5191) refuses ephemeral AND no_history on one
	// row — so the flag is forced only for the plain ephemeral shape, the
	// same way the Dolt role kit's CreateWisp leaves the routing to the verb.
	if !issue.NoHistory {
		issue.Ephemeral = true
	}
	return e.reference.CreateIssue(ctx, issue, actor)
}

func (e *servedEnv) addDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return e.reference.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{EmitEvent: true})
}

// addComment seeds through the reference store's Commenter ROLE, which resolves
// the plane itself — so a case can cite a sweep candidate from a WISP's comment
// without knowing how the backend reaches wisp_comments.
//
// IT STAYS ON THE REFERENCE SIDE NOW THAT THE CLIENT HAS A COMMENTER of its own
// (client wave ga-f352s), and the reason is the harness rule rather than the old
// absence: SEEDING NEVER GOES THROUGH THE CLIENT. A fixture that seeded its
// preconditions through the subject would prove the client agrees with itself,
// which is exactly what the Commenter contract must not be able to do.
func (e *servedEnv) addComment(ctx context.Context, issueID, author, text string) error {
	commenter, err := e.reference.Commenter()
	if err != nil {
		return err
	}
	_, err = commenter.AddComment(ctx, issueops.AddCommentRequest{IssueID: issueID, Author: author, Text: text})
	return err
}

// seedCommentAt appends a comment to an existing thread at a chosen created_at.
//
// It is the IMPORT shape and it has to be: the served operation deliberately
// publishes no created_at member — a stored time the caller supplied makes the
// row disagree with the entry that records it — so the client's own Commenter
// cannot place a comment anywhere but now. That is what makes this the one hook
// here with no client-side equivalent even in principle, and it is why the
// timing clauses of the Commenter contract are observable at all: the seeded
// comment sits an hour from the clock, so the right answer and the wrong one are
// an hour apart rather than a runner's margin.
func (e *servedEnv) seedCommentAt(ctx context.Context, issueID, author, text string, at time.Time) error {
	_, err := e.reference.ImportIssueComment(ctx, issueID, author, text, at)
	return err
}

func (e *servedEnv) setConfig(ctx context.Context, key, value string) error {
	return e.reference.SetConfig(ctx, key, value)
}

// listEvents answers ONE issue's whole event journal, which is how the update
// cases end the clause a row read cannot: RowVersion says the row did not move,
// and nothing on the row says an event was not appended beside it.
//
// The limit is 0 — GetEvents reads that as "no limit" — because every case takes
// a DELTA around the operation under test, and a truncated journal would make
// "the refusal wrote nothing" unfalsifiable.
//
// It reads the REFERENCE store like every other out-of-band hook here, and the
// reason is now the FIRST of the two this comment used to give rather than the
// second. The v0 journal is keyed on `since` and `limit` rather than on an
// issue, so asking the subject would mean paging a whole feed and projecting one
// id out of it — which is a different question from the one these cases ask.
// The second reason is gone: client wave ga-jpywb wired journalops.Journal
// (journal.go), so the client CAN read a journal, and served_journal_test.go is
// where that is proved.
func (e *servedEnv) listEvents(ctx context.Context, issueID string) ([]*types.Event, error) {
	return e.reference.GetEvents(ctx, issueID, 0)
}

// listDependencies answers one issue's outgoing edges as records, for the
// reparent cases: the clause under test is that a set ParentID "atomically
// replaces ALL parents with exactly that target", which needs the whole SET
// rather than a lookup of the new parent.
//
// It reads through the reference store's dependency-with-metadata surface,
// which resolves each target to the issue behind it, so an edge onto an id no
// plane holds is dropped. The cases assert a parent set whose every member is a
// row they seeded, so that resolution is invisible to them.
func (e *servedEnv) listDependencies(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	records, err := e.reference.GetDependenciesWithMetadata(ctx, issueID)
	if err != nil {
		return nil, err
	}
	edges := make([]*types.Dependency, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		edges = append(edges, &types.Dependency{
			IssueID:     issueID,
			DependsOnID: record.ID,
			Type:        record.DependencyType,
		})
	}
	return edges, nil
}

func (e *servedEnv) getIssue(ctx context.Context, id string) (*types.Issue, error) {
	return e.reference.GetIssue(ctx, id)
}

// servedJournalMutations is the one-hook-per-op kit both the paged Journal and
// the pushed Watcher contracts drive, in ONE place so the two cannot disagree
// about how a mutation is made.
//
// Every hook drives the REFERENCE store, which is the harness rule and not a
// shortcut around a refusing client: two of the seven have no client-side
// spelling at all (a raw delete and a raw dependency remove are on the
// unsupported allowlist), and the question both contracts ask is whether the
// client can READ — page or stream — the journal a server wrote.
func servedJournalMutations(reference *embeddeddolt.EmbeddedDoltStore) conformance.JournalMutations {
	return conformance.JournalMutations{
		Create: func(ctx context.Context, id string) error {
			return reference.CreateIssue(ctx, &types.Issue{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
			}, "actor")
		},
		Update: func(ctx context.Context, id string) error {
			return reference.UpdateIssue(ctx, id, map[string]any{"title": "renamed " + id}, "actor")
		},
		Close: func(ctx context.Context, id string) error {
			return reference.CloseIssue(ctx, id, "done", "actor", "")
		},
		Delete: func(ctx context.Context, id string) error {
			return reference.DeleteIssue(ctx, id)
		},
		AddDependency: func(ctx context.Context, from, to string) error {
			return reference.AddDependency(ctx, &types.Dependency{
				IssueID: from, DependsOnID: to, Type: types.DepBlocks,
			}, "actor")
		},
		RemoveDependency: func(ctx context.Context, from, to string) error {
			return reference.RemoveDependency(ctx, from, to, "actor")
		},
		Comment: func(ctx context.Context, id, text string) error {
			return reference.AddComment(ctx, id, "actor", text)
		},
	}
}

// wispExists probes the EPHEMERAL plane alone, which is the one question a
// both-plane read cannot answer: getIssue resolves the durable row first, so a
// stray wisp written under an occupied durable id is invisible to it.
//
// The create contracts need it and nothing else does yet. It is a RAW read of
// the reference store's own table, like every other out-of-band hook here — the
// client publishes no ephemeral-plane listing at all (ListWisps is on the
// unsupported allowlist), so asking through the subject would be asking the
// thing under test.
func (e *servedEnv) wispExists(ctx context.Context, id string) (bool, error) {
	var rows int
	if err := e.queryScalar(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", []any{id}, &rows); err != nil {
		return false, err
	}
	return rows > 0, nil
}

// queryScalar is the contracts' raw-row hook. It opens its own SQL handle per
// call, the way the embedded backend's own frozen fixture kit does, because a
// held handle and a live server on the same database is a lock nobody needs.
//
// "Nobody needs" is measured, not assumed. An earlier revision of the graph-role
// wiring held one handle open for the life of the environment: the first seeding
// write after it opened never returned, the run died on Go's default ten-minute
// test timeout at 600.080s, and the panic's goroutine dump put the test inside
// embeddeddolt.(*EmbeddedDoltStore).CreateIssue blocked in the store's own
// connection acquisition, with the server goroutine parked beside it for four
// minutes. It HANGS rather than fails, which is the worst shape a gate can take
// — so the per-call open is load-bearing, and a future refactor that hoists it
// out for tidiness would reintroduce a deadlock, not a slowdown.
func (e *servedEnv) queryScalar(ctx context.Context, query string, args []any, dest ...any) error {
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, e.dataDir, e.database, "main")
	if err != nil {
		return err
	}
	defer func() { _ = cleanup() }()
	return db.QueryRowContext(ctx, query, args...).Scan(dest...)
}

// countHistoryMatching counts the reference branch's history entries whose
// message matches pattern, where "" means every entry and anything else is a
// SQL LIKE pattern (backend/conformance/history_matching.go states the
// convention). The wisp-naming case needs it: "no entry NAMES this wisp" is a
// claim about what an entry READS, which a bare count cannot express, and a nil
// hook leaves that clause unpinned on this tier rather than checked.
func (e *servedEnv) countHistoryMatching(ctx context.Context, pattern string) (int, error) {
	query := "SELECT COUNT(*) FROM dolt_log"
	var args []any
	if pattern != "" {
		query += " WHERE message LIKE ?"
		args = append(args, pattern)
	}
	var entries int
	err := e.queryScalar(ctx, query, args, &entries)
	return entries, err
}

// countHistory is DEFINED as the empty-pattern count, the same way the embedded
// backend's own fixture kit defines it, so the two hooks cannot disagree about
// the length of one log.
func (e *servedEnv) countHistory(ctx context.Context) (int, error) {
	return e.countHistoryMatching(ctx, "")
}

// commitPending settles everything seeded so far into the version history, so a
// later countHistory delta measures the call under test and not the seeds that
// led up to it.
//
// It runs against the REFERENCE store rather than the subject, like every other
// out-of-band hook here: the subject is an http client, which publishes no
// commit at all (CommitPending is on its unsupported allowlist), and the history
// these cases measure belongs to the store the server is serving from.
func (e *servedEnv) commitPending(ctx context.Context) error {
	return e.commitMessage(ctx, "conformance: settle seeds before a history delta")
}

// commitMessage is the same act under the caller's own message, which the
// staging fixture's Commit hook takes: those cases read `AS OF 'HEAD'` and the
// entry they settle the seeds into is named after what it settled.
func (e *servedEnv) commitMessage(ctx context.Context, message string) error {
	return e.reference.Commit(ctx, message)
}

// execRaw is the single-statement spelling of exec, which is the shape the
// staging fixture's own Exec hook takes. It is a spelling rather than a second
// implementation so both go through the per-call handle the comment on
// queryScalar explains.
func (e *servedEnv) execRaw(ctx context.Context, query string, args ...any) error {
	return e.exec(ctx, []conformance.SQLStatement{{Query: query, Args: args}})
}

// exec runs a seeding script as ONE session, for the close/reopen cases that
// need a state no supported verb can produce.
func (e *servedEnv) exec(ctx context.Context, statements []conformance.SQLStatement) error {
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, e.dataDir, e.database, "main")
	if err != nil {
		return err
	}
	defer func() { _ = cleanup() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement.Query, statement.Args...); err != nil {
			return err
		}
	}
	return nil
}
