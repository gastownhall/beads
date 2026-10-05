//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_watch_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/journalops"
)

// The Watcher contract against the served surface: client → in-process bd serve
// → embedded-Dolt reference.
//
// It is the PUSH sibling of TestServedJournalContract, and it satisfies the
// journalops role census — journalops.Watcher.WatchEventsJournal is reached from
// a conformance entrypoint here rather than waived. Where the paged contract has
// three in-tree bodies bottoming out in one reading, the watch has exactly one
// production body — the SSE client — so this leg is the only place its parser,
// its idle deadline and its reconnect bookkeeping meet a real server.
//
// THE POLL IS SLOWED ON PURPOSE. The mid-stream truncation case needs the
// server's cursor to lag the head while a prune lands, and a production-speed
// one-second poll would deliver the records instead of racing the prune. The
// slow cadence is set through #94's test-only knob (httpapi.TestOnlyWatchCadence),
// the sole way another package reaches the stream's cadence; it costs the other
// cases nothing, because they seed BEFORE the watch opens and the connect read
// delivers the backlog at once without waiting for a poll.
//
// THE HEARTBEAT IS EXPLICIT AND POSITIVE. The knob refuses a zero heartbeat, and
// leaving it at the production default is not an option a consumer package can
// express — so this names 20s outright: long enough that it never fires during a
// case, short enough to stay inside the client's 60s idle deadline while the slow
// poll keeps the stream otherwise quiet.
func TestServedWatchContract(t *testing.T) {
	env := newServedEnv(t, "hwch", func(cfg *httpapi.Config) {
		cfg.WatchCadence = httpapi.TestOnlyWatchCadence(t, 3*time.Second, 20*time.Second)
	})
	ctx := t.Context()
	fixture := newServedWatchFixture(t, env)
	t.Cleanup(func() {
		// Journaling is INSTANCE-scoped and the reference store outlives this
		// fixture in the binary; leaving it on would journal a later case's writes.
		env.reference.SetEventsJournalEnabled(false)
	})

	t.Run("DeliversCommittedMutationsInSeqOrder", func(t *testing.T) {
		conformance.RunWatchDeliversCommittedMutationsInSeqOrder(t, ctx, fixture)
	})
	t.Run("IsSinceExclusiveAndResumable", func(t *testing.T) {
		conformance.RunWatchIsSinceExclusiveAndResumable(t, ctx, fixture)
	})
	t.Run("ConnectTruncationMatchesThePagedRead", func(t *testing.T) {
		conformance.RunWatchConnectTruncationMatchesThePagedRead(t, ctx, fixture)
	})
	t.Run("MidStreamTruncationIsTypedAndStops", func(t *testing.T) {
		conformance.RunWatchMidStreamTruncationIsTypedAndStops(t, ctx, fixture)
	})
}

// newServedWatchFixture binds BOTH read roles to the client through the TYPE
// ASSERTION a front door makes: the watch under test and the paged read it must
// agree with are the same *Store, reached the way `bd serve` reaches the journal.
func newServedWatchFixture(t *testing.T, env *servedEnv) conformance.WatchFixture {
	t.Helper()
	watcher, ok := any(env.subject).(storage.EventsJournalWatcher)
	if !ok {
		t.Fatalf("%T does not implement storage.EventsJournalWatcher", env.subject)
	}
	cursor, ok := any(env.subject).(storage.EventsJournalCursor)
	if !ok {
		t.Fatalf("%T does not implement storage.EventsJournalCursor", env.subject)
	}
	reference := env.reference
	return conformance.WatchFixture{
		IssuePrefix:       env.prefix,
		Watcher:           watcher,
		Journal:           cursor,
		SetJournalEnabled: reference.SetEventsJournalEnabled,
		Prune:             reference.PruneEventsJournal,
		Mutations:         servedJournalMutations(reference),
	}
}

// TestServedWatchRefusesAWrongProjectStamp is the identity gate on the stream:
// events:watch is project-stamp-ENFORCED (projectExempt is false), so a client
// pinned to the wrong workspace is refused before a single stream byte.
//
// It mirrors served_wrong_server_test.go: newServedEnvExpecting arms the client's
// ExpectProjectID with an id the server does not serve, and the refusal surfaces
// as the wrong-server sentinel at the forced handshake the watch's Preflight runs.
func TestServedWatchRefusesAWrongProjectStamp(t *testing.T) {
	env := newServedEnvExpecting(t, "hwcx", "not-"+servedProjectID)
	env.reference.SetEventsJournalEnabled(true)
	t.Cleanup(func() { env.reference.SetEventsJournalEnabled(false) })

	watcher, ok := any(env.subject).(storage.EventsJournalWatcher)
	if !ok {
		t.Fatalf("%T does not implement storage.EventsJournalWatcher", env.subject)
	}
	err := watcher.WatchEventsJournal(t.Context(), 0, failingDeliver(t))
	if !errors.Is(err, wire.ErrProjectMismatch) {
		t.Fatalf("a watch against the wrong workspace = %v, want ErrProjectMismatch before any stream byte", err)
	}
}

// TestServedWatchRefusesADisabledWorkspace is the activation gate: a workspace
// whose journal is OFF refuses the watch with the 409 sentinel, exactly as the
// paged read refuses GET /v0/beads/events. A stream over a disabled journal would
// be a connection held open forever against a workspace that will never emit.
func TestServedWatchRefusesADisabledWorkspace(t *testing.T) {
	env := newServedEnv(t, "hwcd", func(cfg *httpapi.Config) {
		cfg.EventsJournalEnabled = false
	})

	watcher, ok := any(env.subject).(storage.EventsJournalWatcher)
	if !ok {
		t.Fatalf("%T does not implement storage.EventsJournalWatcher", env.subject)
	}
	err := watcher.WatchEventsJournal(t.Context(), 0, failingDeliver(t))
	if !errors.Is(err, wire.ErrEventsJournalDisabled) {
		t.Fatalf("a watch against a journal-off workspace = %v, want ErrEventsJournalDisabled", err)
	}
}

// failingDeliver is the deliver hook for the refusal cases, where a delivered
// record would mean the connect did not refuse as it must.
func failingDeliver(t *testing.T) func(journalops.Row) error {
	return func(row journalops.Row) error {
		t.Errorf("the watch delivered seq %d, but the connect was supposed to refuse before any byte", row.Seq)
		return nil
	}
}
