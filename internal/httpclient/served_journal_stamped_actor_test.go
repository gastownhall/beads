//go:build cgo

package httpclient

// Every journaled change a request causes, including derived is_blocked
// updates, carries that request's actor — held against the served surface
// behind an authenticating proxy.
//
// A deployment can put bd serve behind a proxy that authenticates the caller
// and overwrites the principal-naming body member ("actor", or a comment's
// "author") with the caller's verified subject before it forwards the write.
// The actor bd serve journals for a request is then that subject, whatever the
// client named with --actor, and a consumer that holds every record of a
// user's change to that user reads a record with no actor as system/unknown.
// These cases drive a fixed command sequence through the http client a bd CLI
// uses for an http workspace, a double of such a proxy's stamping rule, and an
// in-process bd serve over the reference store, and read GET /v0/beads/events
// as raw JSON, paged the way a journal consumer pages it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// stampingProxy is a double of an authenticating proxy's body policy and
// nothing else: every bodied request whose top-level JSON object names a
// principal member ("actor", "author") is forwarded with that member set to
// subject. The http client always sends the member, so overwriting it is the
// whole rule here.
func stampingProxy(t *testing.T, upstream *url.URL, subject string) *url.URL {
	t.Helper()
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Host = upstream.Host
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "stamping proxy: read body", http.StatusBadRequest)
				return
			}
			body = stampPrincipal(body, subject)
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			r.Header.Set("Content-Length", strconv.Itoa(len(body)))
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse the stamping proxy's URL: %v", err)
	}
	return u
}

func stampPrincipal(body []byte, subject string) []byte {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil || members == nil {
		return body
	}
	stamped := false
	for _, member := range []string{"actor", "author"} {
		if _, ok := members[member]; ok {
			encoded, _ := json.Marshal(subject)
			members[member] = encoded
			stamped = true
		}
	}
	if !stamped {
		return body
	}
	out, err := json.Marshal(members)
	if err != nil {
		return body
	}
	return out
}

// stampedSide is one identity: the http client bd builds for an http
// workspace, pointed at that identity's stamping proxy.
type stampedSide struct {
	name    string
	subject string
	store   *Store
}

func newStampedSide(t *testing.T, env *servedEnv, name, subject string) stampedSide {
	t.Helper()
	proxy := stampingProxy(t, env.base, subject)
	client, err := wire.New(proxy, nil, servedWireOptions(newServedWitness(t, "stamped side "+name), wire.Options{}))
	if err != nil {
		t.Fatalf("build %s's client: %v", name, err)
	}
	return stampedSide{name: name, subject: subject, store: New(Target{BaseURL: proxy}, client, nil)}
}

// create is `bd create <title> --labels <tag> --type task --priority 3` as the
// side, with the client naming actor (the stamping proxy replaces it).
func (s stampedSide) create(t *testing.T, ctx context.Context, actor, title, tag string) string {
	t.Helper()
	lifecycle, err := s.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("%s: IssueLifecycle: %v", s.name, err)
	}
	result, err := lifecycle.Create(ctx, issueops.CreateRequest{
		Actor: actor,
		Issue: &issueops.Issue{Title: title, IssueType: types.TypeTask, Priority: 3, Status: types.StatusOpen, Labels: []string{tag}},
	})
	if err != nil {
		t.Fatalf("%s: create %q: %v", s.name, title, err)
	}
	return result.Issue.ID
}

// journalWindow is one command and the journal seq window it ran in: its own
// records have lo < seq <= hi (the journal heads read before and after it).
type journalWindow struct {
	issue, name, op string
	lo, hi          int64
}

// journalCmds runs eight changes as the side on issue (blocker dep, assignee
// who), with the client naming actor, each through the role the bd CLI uses
// for it over http.
func (s stampedSide) journalCmds(t *testing.T, ctx context.Context, env *servedEnv, issue, dep, who, actor, tag string) []journalWindow {
	t.Helper()
	lifecycle, err := s.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("%s: IssueLifecycle: %v", s.name, err)
	}
	editor, err := s.store.DependencyEditor()
	if err != nil {
		t.Fatalf("%s: DependencyEditor: %v", s.name, err)
	}
	closer, err := s.store.BatchCloser()
	if err != nil {
		t.Fatalf("%s: BatchCloser: %v", s.name, err)
	}
	update := func(patch issueops.IssuePatch) error {
		_, err := lifecycle.Update(ctx, issueops.UpdateRequest{Actor: actor, IssueID: issue, Patch: patch, AllowTemplate: true})
		return err
	}
	steps := []struct {
		name, op string
		run      func() error
	}{
		{"dep add", "dep_add", func() error {
			_, err := editor.AddDependencies(ctx, issueops.AddDependenciesRequest{Actor: actor,
				Edges: []issueops.DependencyEdge{{IssueID: issue, DependsOnID: dep, Type: types.DepBlocks}}})
			return err
		}},
		{"dep remove", "dep_remove", func() error {
			_, err := editor.RemoveDependency(ctx, issueops.RemoveDependencyRequest{Actor: actor, IssueID: issue, DependsOnID: dep})
			return err
		}},
		{"label add", "update", func() error {
			return update(issueops.IssuePatch{Labels: issueops.LabelPatch{Add: []string{tag + "-e"}}})
		}},
		{"label remove", "update", func() error {
			return update(issueops.IssuePatch{Labels: issueops.LabelPatch{Remove: []string{tag + "-e"}}})
		}},
		{"update --assignee", "update", func() error {
			return update(issueops.IssuePatch{Assignee: set(who)})
		}},
		{"update --status blocked", "update", func() error {
			return update(issueops.IssuePatch{Status: set(issueops.StatusBlocked)})
		}},
		{"update --status open", "update", func() error {
			return update(issueops.IssuePatch{Status: set(issueops.StatusOpen)})
		}},
		{"close --force", "close", func() error {
			result, err := closer.CloseBatch(ctx, issueops.CloseBatchRequest{Actor: actor, Force: true,
				Items: []issueops.BatchCloseItem{{IssueID: issue, Reason: "journal check"}}})
			if err != nil {
				return err
			}
			for _, outcome := range result.Outcomes {
				if outcome.Err != nil {
					return outcome.Err
				}
			}
			return nil
		}},
	}
	lo := servedRawJournalHead(t, env)
	windows := make([]journalWindow, 0, len(steps))
	for _, step := range steps {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %s %s (actor %q): %v", s.name, step.name, issue, actor, err)
		}
		hi := servedRawJournalHead(t, env)
		windows = append(windows, journalWindow{issue: issue, name: s.name + " " + step.name, op: step.op, lo: lo, hi: hi})
		lo = hi
	}
	return windows
}

// judgeIssueJournal judges one issue's journal: each command on the issue has
// a record of its op, on the issue, carrying subject, inside its own seq
// window; and EVERY record of the issue carries subject (an empty actor is not
// subject). It returns one line per violation.
func judgeIssueJournal(records []eventsjournal.Record, windows []journalWindow, issue, subject string) []string {
	var mine []eventsjournal.Record
	for _, r := range records {
		if r.IssueID == issue {
			mine = append(mine, r)
		}
	}
	var bad []string
	for _, w := range windows {
		if w.issue != issue {
			continue
		}
		var inside []string
		found := false
		for _, r := range mine {
			if r.Seq > w.lo && r.Seq <= w.hi {
				inside = append(inside, fmt.Sprintf("seq %d %s by %q", r.Seq, r.Op, r.Actor))
				if r.Op == w.op && r.Actor == subject {
					found = true
				}
			}
		}
		if !found {
			bad = append(bad, fmt.Sprintf("%s: no %s record by %s in seq (%d, %d]; it holds [%s]",
				w.name, w.op, subject, w.lo, w.hi, strings.Join(inside, ", ")))
		}
	}
	for _, r := range mine {
		if r.Actor != subject {
			cause := "outside every command window"
			for _, w := range windows {
				if w.issue == issue && r.Seq > w.lo && r.Seq <= w.hi {
					cause = "in the window of `" + w.name + "`"
				}
			}
			bad = append(bad, fmt.Sprintf("seq %d op %s on %s carries actor %q, want %q (%s)",
				r.Seq, r.Op, issue, r.Actor, subject, cause))
		}
	}
	return bad
}

// showAgreesWithJournal checks that `bd show --json` (the client's GetIssue)
// agrees with the issue snapshot of the issue's last journal record on the
// fields the eight commands moved: status, assignee and labels.
func showAgreesWithJournal(t *testing.T, ctx context.Context, side stampedSide, records []eventsjournal.Record, issue string) []string {
	t.Helper()
	var last *eventsjournal.Record
	for i := range records {
		if records[i].IssueID == issue {
			last = &records[i]
		}
	}
	if last == nil {
		return []string{"no journal record at all"}
	}
	var journaled struct {
		Status   string   `json:"status"`
		Assignee string   `json:"assignee"`
		Labels   []string `json:"labels"`
	}
	if err := json.Unmarshal(last.Issue, &journaled); err != nil {
		return []string{fmt.Sprintf("decode seq %d's issue: %v", last.Seq, err)}
	}
	shown, err := side.store.GetIssue(ctx, issue)
	if err != nil {
		t.Fatalf("%s: show %s: %v", side.name, issue, err)
	}
	var bad []string
	if string(shown.Status) != journaled.Status {
		bad = append(bad, fmt.Sprintf("status: show %q, journal seq %d %q", shown.Status, last.Seq, journaled.Status))
	}
	if shown.Assignee != journaled.Assignee {
		bad = append(bad, fmt.Sprintf("assignee: show %q, journal seq %d %q", shown.Assignee, last.Seq, journaled.Assignee))
	}
	shownLabels := slices.Sorted(slices.Values(shown.Labels))
	journaledLabels := slices.Sorted(slices.Values(journaled.Labels))
	if !slices.Equal(shownLabels, journaledLabels) {
		bad = append(bad, fmt.Sprintf("labels: show %v, journal seq %d %v", shownLabels, last.Seq, journaledLabels))
	}
	return bad
}

// TestServedLifecycleJournalStampedRecordsCarryTheVerifiedSubject runs the
// command sequence on the served tier as two identities behind a stamping
// proxy: first with no --actor, then with a made-up --actor and with B naming
// A's subject. Every record of each issue must carry the verified subject of
// the identity that changed it, and the made-up string must reach no record.
// Before the fix the serve journaled the issue's own derived is_blocked
// recompute at `bd dep remove` as a separate `update` record with NO actor
// (internal/storage/issueops/journal.go recordBlockedJournalChanges), so that
// command left an actor-less record on the issue whatever principal made the
// change.
func TestServedLifecycleJournalStampedRecordsCarryTheVerifiedSubject(t *testing.T) {
	env := newServedEnv(t, "jsv")
	ctx := t.Context()
	env.reference.SetEventsJournalEnabled(true)
	t.Cleanup(func() { env.reference.SetEventsJournalEnabled(false) })

	// Two verified subjects: a signed-in user and a service account.
	const subjA, subjB = "user-jsv-a", "service-jsv-b"
	nonce := strconv.FormatInt(time.Now().UnixNano()%1_000_000_000, 36)
	tag := "jsv-" + nonce
	forged := "jsv-forged-" + nonce
	a := newStampedSide(t, env, "A", subjA)
	b := newStampedSide(t, env, "B", subjB)

	h0 := servedRawJournalHead(t, env)

	// Each identity runs as its own subject, no --actor.
	eA := a.create(t, ctx, subjA, "jsv "+nonce+" journal E_A", tag)
	eB := b.create(t, ctx, subjB, "jsv "+nonce+" journal E_B", tag)
	eD := a.create(t, ctx, subjA, "jsv "+nonce+" journal E_D", tag)
	var windows []journalWindow
	windows = append(windows, a.journalCmds(t, ctx, env, eA, eD, subjB, subjA, tag)...)
	windows = append(windows, b.journalCmds(t, ctx, env, eB, eD, subjA, subjB, tag)...)

	// The same changes with a made-up --actor, and with B naming A's subject;
	// the stamping proxy replaces both with the caller's subject.
	fA := a.create(t, ctx, forged, "jsv "+nonce+" journal F_A", tag)
	fB := b.create(t, ctx, forged, "jsv "+nonce+" journal F_B", tag)
	fB2 := b.create(t, ctx, subjA, "jsv "+nonce+" journal F_B naming A", tag)
	fD := a.create(t, ctx, forged, "jsv "+nonce+" journal F_D", tag)
	windows = append(windows, a.journalCmds(t, ctx, env, fA, fD, subjB, forged, tag)...)
	windows = append(windows, b.journalCmds(t, ctx, env, fB, fD, subjA, forged, tag)...)
	windows = append(windows, b.journalCmds(t, ctx, env, fB2, fD, subjA, subjA, tag)...)

	records := servedRawJournalRead(t, env, h0)
	if len(records) == 0 {
		t.Fatalf("GET /v0/beads/events?since=%d returned no records: the journal is not recording", h0)
	}

	type judged struct {
		row, issue, subject string
		side                stampedSide
	}
	for _, j := range []judged{
		{"plain: every record of E_A carries A's verified subject", eA, subjA, a},
		{"plain: every record of E_B carries B's verified subject", eB, subjB, b},
		{"forged: every record of F_A carries A's verified subject", fA, subjA, a},
		{"forged: every record of F_B carries B's verified subject", fB, subjB, b},
		{"forged: every record of F_B2 (B naming A) carries B's verified subject", fB2, subjB, b},
	} {
		if bad := judgeIssueJournal(records, windows, j.issue, j.subject); len(bad) > 0 {
			t.Errorf("%s (%s):\n  %s", j.row, j.issue, strings.Join(bad, "\n  "))
		}
		if bad := showAgreesWithJournal(t, ctx, j.side, records, j.issue); len(bad) > 0 {
			t.Errorf("%s: bd show disagrees with the journal's final state of %s: %s", j.row, j.issue, strings.Join(bad, "; "))
		}
	}
	// No cross-identity actor: every record in a window carries the subject of
	// the side that ran that window's command, on whatever issue it lands.
	for _, w := range windows {
		want := subjA
		if strings.HasPrefix(w.name, "B ") {
			want = subjB
		}
		for _, r := range records {
			if r.Seq > w.lo && r.Seq <= w.hi && r.Actor != want {
				t.Errorf("seq %d %s on %s in the window of `%s` carries actor %q, want %q", r.Seq, r.Op, r.IssueID, w.name, r.Actor, want)
			}
		}
	}
	// The made-up string is in no actor, created_by, assignee or
	// author — anywhere in any record the run journaled.
	for _, r := range records {
		encoded, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("encode seq %d: %v", r.Seq, err)
		}
		if bytes.Contains(encoded, []byte(forged)) {
			t.Errorf("the made-up actor %q appears in seq %d (%s on %s): %s", forged, r.Seq, r.Op, r.IssueID, encoded)
		}
	}
	if t.Failed() {
		t.Logf("every journal record since %d (H0):\n%s", h0, journalRecordTable(records, windows))
	}
}

// TestServedLifecycleJournalDependentsCarryTheCausingSubject is the dependents
// half of the same rule, outside the eight commands: a change to a BLOCKER
// (close, reopen) flips its dependents' derived is_blocked, and the serve
// journals each flip as an `update` of the DEPENDENT. Each of those records is
// caused by the blocker's change and carries that request's verified subject —
// B's when B closes, A's when A reopens, never the other identity's and never
// none.
func TestServedLifecycleJournalDependentsCarryTheCausingSubject(t *testing.T) {
	env := newServedEnv(t, "jsd")
	ctx := t.Context()
	env.reference.SetEventsJournalEnabled(true)
	t.Cleanup(func() { env.reference.SetEventsJournalEnabled(false) })

	const subjA, subjB = "user-jsd-a", "service-jsd-b"
	tag := "jsd-" + strconv.FormatInt(time.Now().UnixNano()%1_000_000_000, 36)
	a := newStampedSide(t, env, "A", subjA)
	b := newStampedSide(t, env, "B", subjB)

	blocker := a.create(t, ctx, subjA, "blocker", tag)
	dependent := a.create(t, ctx, subjA, "dependent", tag)
	editor, err := a.store.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor: %v", err)
	}
	if _, err := editor.AddDependencies(ctx, issueops.AddDependenciesRequest{Actor: subjA,
		Edges: []issueops.DependencyEdge{{IssueID: dependent, DependsOnID: blocker, Type: types.DepBlocks}}}); err != nil {
		t.Fatalf("dep add %s %s: %v", dependent, blocker, err)
	}

	type change struct {
		side   stampedSide
		name   string
		run    func(stampedSide) error
		lo, hi int64
	}
	changes := []*change{
		{side: b, name: "B closes the blocker", run: func(s stampedSide) error {
			closer, err := s.store.BatchCloser()
			if err != nil {
				return err
			}
			result, err := closer.CloseBatch(ctx, issueops.CloseBatchRequest{Actor: s.subject,
				Items: []issueops.BatchCloseItem{{IssueID: blocker, Reason: "done"}}})
			if err == nil && len(result.Outcomes) == 1 {
				err = result.Outcomes[0].Err
			}
			return err
		}},
		{side: a, name: "A reopens the blocker", run: func(s stampedSide) error {
			lifecycle, err := s.store.IssueLifecycle()
			if err != nil {
				return err
			}
			_, err = lifecycle.Reopen(ctx, issueops.ReopenRequest{Actor: s.subject, IssueID: blocker})
			return err
		}},
	}
	for _, c := range changes {
		c.lo = servedRawJournalHead(t, env)
		if err := c.run(c.side); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		c.hi = servedRawJournalHead(t, env)
	}

	records := servedRawJournalRead(t, env, changes[0].lo)
	for _, c := range changes {
		dependentUpdated := false
		for _, r := range records {
			if r.Seq <= c.lo || r.Seq > c.hi {
				continue
			}
			if r.Actor != c.side.subject {
				t.Errorf("%s: seq %d op %s on %s carries actor %q, want %q (the change's verified subject)",
					c.name, r.Seq, r.Op, r.IssueID, r.Actor, c.side.subject)
			}
			if r.IssueID == dependent && r.Op == "update" {
				dependentUpdated = true
			}
		}
		if !dependentUpdated {
			t.Errorf("%s flips the dependent %s's readiness but journaled no update of it in seq (%d, %d]: "+
				"a derived record carries the request's actor; it is not dropped", c.name, dependent, c.lo, c.hi)
		}
	}
	if t.Failed() {
		t.Logf("every journal record since %d:\n%s", changes[0].lo, journalRecordTable(records, nil))
	}
}

func journalRecordTable(records []eventsjournal.Record, windows []journalWindow) string {
	sorted := append([]eventsjournal.Record(nil), records...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	var b strings.Builder
	for _, r := range sorted {
		window := ""
		for _, w := range windows {
			if r.Seq > w.lo && r.Seq <= w.hi {
				window = w.name + " (" + w.issue + ")"
			}
		}
		actor := r.Actor
		if actor == "" {
			actor = "<none>"
		}
		fmt.Fprintf(&b, "  seq %-4d %-10s %-14s actor %-22s %s\n", r.Seq, r.Op, r.IssueID, actor, window)
	}
	return b.String()
}
