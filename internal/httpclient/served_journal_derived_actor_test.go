//go:build cgo

package httpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestServedLifecycleJournalRecordsCarryTheRequestActor holds the served
// surface to "every journaled change a request causes, including derived
// is_blocked updates, carries that request's actor".
//
// It drives eight commands through THIS client's roles, the way `bd` drives
// them on an HTTP workspace — `bd dep add`, `bd dep remove`, `bd label add`,
// `bd label remove`, `bd update --assignee`, `bd update --status blocked`,
// `bd update --status open` and `bd close --force` — each naming one actor.
// Then it reads GET /v0/beads/events as RAW JSON, the way an external journal
// consumer does: this client's own journal decode drops every record's actor
// (B14, internal/httpclient/journal.go journalRow).
//
// Every record in each command's window must carry the request's actor. On
// the subject (E_A) alone, the record that used to fail is its own derived
// is_blocked update at `bd dep remove`; the subject here also has a dependent
// and a child, so the derived records blocked-state maintenance journals
// AROUND the subject are judged too — the rows the same hole leaves
// unattributed on any graph with edges.
func TestServedLifecycleJournalRecordsCarryTheRequestActor(t *testing.T) {
	env := newServedEnv(t, "hjda")
	ctx := t.Context()
	env.reference.SetEventsJournalEnabled(true)
	t.Cleanup(func() {
		// Journaling is instance-scoped, and the reference store can outlive
		// this case inside one test binary.
		env.reference.SetEventsJournalEnabled(false)
	})

	// actor is the one value the request names, and so the one every record
	// the request causes must carry.
	const actor = "user-jda-a"
	const assignee = "service-jda-b"

	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	editor, err := env.subject.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}

	h0 := servedRawJournalHead(t, env)

	create := func(title string) string {
		t.Helper()
		created, err := lifecycle.Create(ctx, issueops.CreateRequest{
			Actor: actor,
			Issue: &issueops.Issue{Title: title, Priority: 3, IssueType: types.TypeTask},
		})
		if err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		return created.Issue.ID
	}
	subject := create("journal E_A")
	blocker := create("journal E_D")
	dependent := create("waits on E_A")
	child := create("under E_A")
	if _, err := editor.AddDependencies(ctx, issueops.AddDependenciesRequest{Actor: actor, Edges: []issueops.DependencyEdge{
		{IssueID: dependent, DependsOnID: subject, Type: types.DepBlocks},
		{IssueID: child, DependsOnID: subject, Type: types.DepParentChild},
	}}); err != nil {
		t.Fatalf("seed the subject's dependent and child: %v", err)
	}

	label := "jda-" + env.prefix + "-e"
	// allowTemplate is what bd label sets and bd update does not
	// (cmd/bd/label.go), so each command reaches the server as bd sends it.
	update := func(patch issueops.IssuePatch, allowTemplate bool) func() error {
		return func() error {
			_, err := lifecycle.Update(ctx, issueops.UpdateRequest{Actor: actor, IssueID: subject, Patch: patch, AllowTemplate: allowTemplate})
			return err
		}
	}
	commands := []struct {
		name string
		op   string
		run  func() error
	}{
		{"bd dep add E_A E_D", "dep_add", func() error {
			_, err := editor.AddDependencies(ctx, issueops.AddDependenciesRequest{Actor: actor, Edges: []issueops.DependencyEdge{
				{IssueID: subject, DependsOnID: blocker, Type: types.DepBlocks},
			}})
			return err
		}},
		{"bd dep remove E_A E_D", "dep_remove", func() error {
			_, err := editor.RemoveDependency(ctx, issueops.RemoveDependencyRequest{Actor: actor, IssueID: subject, DependsOnID: blocker})
			return err
		}},
		{"bd label add", "update", update(issueops.IssuePatch{Labels: issueops.LabelPatch{Add: []string{label}}}, true)},
		{"bd label remove", "update", update(issueops.IssuePatch{Labels: issueops.LabelPatch{Remove: []string{label}}}, true)},
		{"bd update --assignee", "update", update(issueops.IssuePatch{Assignee: set(assignee)}, false)},
		{"bd update --status blocked", "update", update(issueops.IssuePatch{Status: set(types.StatusBlocked)}, false)},
		{"bd update --status open", "update", update(issueops.IssuePatch{Status: set(types.StatusOpen)}, false)},
		{"bd close --force", "close", func() error {
			_, err := lifecycle.Close(ctx, issueops.CloseRequest{Actor: actor, IssueID: subject, Reason: "journal check", Force: true})
			return err
		}},
	}

	for _, command := range commands {
		lo := servedRawJournalHead(t, env)
		if err := command.run(); err != nil {
			t.Fatalf("%s: %v", command.name, err)
		}
		window := servedRawJournalRead(t, env, lo)
		own := false
		var bad []string
		for _, record := range window {
			if record.Op == command.op && record.IssueID == subject && record.Actor == actor {
				own = true
			}
			if record.Actor != actor {
				bad = append(bad, servedRawRecordString(record, subject, dependent, child))
			}
		}
		if !own {
			t.Errorf("%s: no %s record of E_A by %q in seq (%d, ...]; it holds %v",
				command.name, command.op, actor, lo, servedRawWindowString(window, subject, dependent, child))
		}
		if len(bad) > 0 {
			t.Errorf("%s: %d record(s) the request caused do not carry its actor %q: %v "+
				"(a derived record must carry the request's actor or not be journaled as a record of its own; "+
				"an absent actor reads as system/unknown)",
				command.name, len(bad), actor, bad)
		}
	}

	// The per-issue judgement: every journal record of E_A since H0 carries
	// the request's actor.
	var subjectBad []string
	for _, record := range servedRawJournalRead(t, env, h0) {
		if record.IssueID == subject && record.Actor != actor {
			subjectBad = append(subjectBad, servedRawRecordString(record, subject, dependent, child))
		}
	}
	if len(subjectBad) > 0 {
		t.Errorf("not every record of E_A carries the request's actor %q: %v", actor, subjectBad)
	}
}

// servedRawJournalHead is the journal's head, read the way an external
// consumer reads it: a since above every seq, one record at most, the page's
// head.
func servedRawJournalHead(t *testing.T, env *servedEnv) int64 {
	t.Helper()
	page := servedRawEventsPage(t, env, math.MaxInt64, 1)
	if len(page.Records) != 0 {
		t.Fatalf("a since above every seq answered %d records", len(page.Records))
	}
	return page.Head
}

// servedRawJournalRead is every record after since, paged at the handler's
// bound until the last seq reaches the head.
func servedRawJournalRead(t *testing.T, env *servedEnv, since int64) []eventsjournal.Record {
	t.Helper()
	var out []eventsjournal.Record
	for {
		page := servedRawEventsPage(t, env, since, eventsWireLimit)
		out = append(out, page.Records...)
		if len(page.Records) == 0 {
			return out
		}
		last := page.Records[len(page.Records)-1].Seq
		if last >= page.Head {
			return out
		}
		if last <= since {
			t.Fatalf("GET /v0/beads/events?since=%d did not advance (last seq %d, head %d)", since, last, page.Head)
		}
		since = last
	}
}

// servedRawEventsTimeout bounds one raw GET /v0/beads/events.
const servedRawEventsTimeout = wire.DefaultTimeout

// servedRawEventsBody is the EventsPage envelope as the wire carries it.
type servedRawEventsBody struct {
	Records []eventsjournal.Record `json:"records"`
	Head    int64                  `json:"head"`
}

// servedRawEventsPage is one GET /v0/beads/events, decoded as the published
// envelope (eventsjournal.Record is the operation's EventRecord schema) with no
// help from this package's client.
func servedRawEventsPage(t *testing.T, env *servedEnv, since int64, limit int) servedRawEventsBody {
	t.Helper()
	target := *env.base
	target.Path = strings.TrimSuffix(target.Path, "/") + "/v0/beads/events"
	target.RawQuery = "since=" + strconv.FormatInt(since, 10) + "&limit=" + strconv.Itoa(limit)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target.String(), nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", target.String(), err)
	}
	// A bounded client, like every other served dial: a wedged server fails
	// the case instead of hanging it until the test binary's timeout.
	resp, err := (&http.Client{Timeout: servedRawEventsTimeout}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", target.String(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", target.String(), err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d: %s", target.String(), resp.StatusCode, body)
	}
	var page servedRawEventsBody
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode GET %s: %v: %s", target.String(), err, body)
	}
	return page
}

func servedRawRecordString(record eventsjournal.Record, subject, dependent, child string) string {
	who := record.IssueID
	switch record.IssueID {
	case subject:
		who = "E_A"
	case dependent:
		who = "dependent"
	case child:
		who = "child"
	}
	return fmt.Sprintf("seq %d %s(%s) actor=%q", record.Seq, record.Op, who, record.Actor)
}

func servedRawWindowString(window []eventsjournal.Record, subject, dependent, child string) []string {
	out := make([]string, 0, len(window))
	for _, record := range window {
		out = append(out, servedRawRecordString(record, subject, dependent, child))
	}
	return out
}
