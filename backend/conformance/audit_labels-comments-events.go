package conformance

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// Audit cases for the "labels-comments-events" slice. Each case pins a strange
// but real behavior of the issueops label/comment/event helpers, validated
// against the embedded-Dolt oracle. See the audit findings for provenance
// (issueops/events.go, issueops/labels.go, issueops/comments.go; formerly sqlkit/annotations.go).

// auditEventsOfType returns the events of exactly the given type, in the order
// GetEvents returned them.
func auditEventsOfType(evs []*types.Event, typ types.EventType) []*types.Event {
	var out []*types.Event
	for _, e := range evs {
		if e.EventType == typ {
			out = append(out, e)
		}
	}
	return out
}

// testAuditEventValueNullability pins the durable-NULL vs wisp-empty split baked
// into the schema defaults: events.old_value/new_value have no DEFAULT (NULL),
// while wisp_events.old_value/new_value DEFAULT empty-string. AddLabel never writes those
// columns, so the SAME operation yields a nil pointer on a durable issue but a
// non-nil pointer to "" on a wisp.
func testAuditEventValueNullability(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-dur", Title: "durable"}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-wisp", Title: "wisp", Ephemeral: true}), "a"))
	must(t, s.AddLabel(c, "test-dur", "bug", "a"))
	must(t, s.AddLabel(c, "test-wisp", "bug", "a"))

	evD, err := s.GetEvents(c, "test-dur", 0)
	must(t, err)
	la := auditEventsOfType(evD, types.EventLabelAdded)
	if len(la) != 1 {
		t.Fatalf("durable label_added events = %d, want 1", len(la))
	}
	ev := la[0]
	if ev.OldValue != nil {
		t.Errorf("durable label_added OldValue = %q, want nil (events.old_value has no DEFAULT)", *ev.OldValue)
	}
	if ev.NewValue != nil {
		t.Errorf("durable label_added NewValue = %q, want nil", *ev.NewValue)
	}
	if ev.Comment == nil || *ev.Comment != "Added label: bug" {
		t.Errorf("durable label_added Comment = %v, want \"Added label: bug\"", ev.Comment)
	}

	evW, err := s.GetEvents(c, "test-wisp", 0)
	must(t, err)
	law := auditEventsOfType(evW, types.EventLabelAdded)
	if len(law) != 1 {
		t.Fatalf("wisp label_added events = %d, want 1", len(law))
	}
	evw := law[0]
	if evw.OldValue == nil || *evw.OldValue != "" {
		t.Errorf("wisp label_added OldValue = %v, want non-nil pointer to \"\" (wisp_events.old_value DEFAULT '')", evw.OldValue)
	}
	if evw.NewValue == nil || *evw.NewValue != "" {
		t.Errorf("wisp label_added NewValue = %v, want non-nil pointer to \"\"", evw.NewValue)
	}
	if evw.Comment == nil || *evw.Comment != "Added label: bug" {
		t.Errorf("wisp label_added Comment = %v, want \"Added label: bug\"", evw.Comment)
	}
}

// testAuditLabelEventNonIdempotent pins that label mutations are idempotent at the
// row level (INSERT IGNORE / DELETE) yet NON-idempotent at the event level: the
// label_added / label_removed event is emitted unconditionally.
func testAuditLabelEventNonIdempotent(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-i", Title: "I"}), "a"))
	must(t, s.AddLabel(c, "test-i", "x", "a"))
	must(t, s.AddLabel(c, "test-i", "x", "a"))

	labels, err := s.GetLabels(c, "test-i")
	must(t, err)
	if len(labels) != 1 || labels[0] != "x" {
		t.Errorf("labels after double-add = %v, want [x] (row-level idempotent)", labels)
	}
	evs, err := s.GetEvents(c, "test-i", 0)
	must(t, err)
	la := auditEventsOfType(evs, types.EventLabelAdded)
	if len(la) != 2 {
		t.Fatalf("label_added events after double-add = %d, want 2 (event-level NON-idempotent)", len(la))
	}
	for _, e := range la {
		if e.Comment == nil || *e.Comment != "Added label: x" {
			t.Errorf("label_added Comment = %v, want \"Added label: x\"", e.Comment)
		}
	}

	// Removing a never-present label: no error, but a spurious label_removed event.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-j", Title: "J"}), "a"))
	must(t, s.RemoveLabel(c, "test-j", "never-added", "a"))
	evsJ, err := s.GetEvents(c, "test-j", 0)
	must(t, err)
	lr := auditEventsOfType(evsJ, types.EventLabelRemoved)
	if len(lr) != 1 {
		t.Fatalf("label_removed events after remove-of-absent = %d, want 1 (spurious event)", len(lr))
	}
	if lr[0].Comment == nil || *lr[0].Comment != "Removed label: never-added" {
		t.Errorf("label_removed Comment = %v, want \"Removed label: never-added\"", lr[0].Comment)
	}
}

// testAuditCountCommentsWispParity pins that CountIssueComments wisp-routes the
// same way GetIssueComments does: a wisp's structured comments live in
// `wisp_comments`, and the count must agree with the rows (GH#5565 — the count
// used to query only `comments` and report 0, so `bd show` showed comment_count 0
// on a wisp that had comments).
func testAuditCountCommentsWispParity(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-w", Title: "wisp", Ephemeral: true}), "a"))
	ts := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := s.ImportIssueComment(c, "test-w", "a", "hi", ts); err != nil {
		t.Fatalf("ImportIssueComment: %v", err)
	}

	got, err := s.GetIssueComments(c, "test-w")
	must(t, err)
	if len(got) != 1 || got[0].Text != "hi" {
		t.Fatalf("GetIssueComments(wisp) = %+v, want 1 comment 'hi' (wisp-routed)", got)
	}
	n, err := s.CountIssueComments(c, "test-w")
	must(t, err)
	if n != int64(len(got)) {
		t.Errorf("CountIssueComments(wisp) = %d, want %d (must wisp-route like GetIssueComments)", n, len(got))
	}
}

// testAuditLabelCollationOrder pins GetLabels' bare `ORDER BY label` against the
// embedded-Dolt oracle. The finding predicted case-insensitive ordering, but the
// embedded-Dolt (gms_pure_go) build actually sorts BINARY/code-point — uppercase
// before lowercase — so mixed-case labels come back [Banana, Cherry, apple]. A
// backend with true case-insensitive collation ([apple, Banana, Cherry]) diverges;
// this pins the collation contract to whatever the oracle actually produces.
func testAuditLabelCollationOrder(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-i", Title: "I"}), "a"))
	must(t, s.AddLabel(c, "test-i", "Cherry", "a"))
	must(t, s.AddLabel(c, "test-i", "apple", "a"))
	must(t, s.AddLabel(c, "test-i", "Banana", "a"))

	labels, err := s.GetLabels(c, "test-i")
	must(t, err)
	want := []string{"Banana", "Cherry", "apple"}
	if len(labels) != len(want) {
		t.Fatalf("GetLabels = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("GetLabels = %v, want %v (uppercase-first binary ordering on the oracle; ci backends diverge)", labels, want)
		}
	}
}

// testAuditEventsSinceStrictBoundary pins the strict `created_at > ?` boundary in
// GetAllEventsSince: an event whose created_at exactly equals `since` is EXCLUDED,
// but the same event one second earlier than `since` is included.
func testAuditEventsSinceStrictBoundary(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-i", Title: "I"}), "a"))
	must(t, s.AddComment(c, "test-i", "a", "hello"))

	evs, err := s.GetEvents(c, "test-i", 0)
	must(t, err)
	commented := auditEventsOfType(evs, types.EventCommented)
	if len(commented) != 1 {
		t.Fatalf("commented events = %d, want 1", len(commented))
	}
	target := commented[0]
	tAt := target.CreatedAt

	// Strict >: an event at exactly `since` is excluded.
	at, err := s.GetAllEventsSince(c, tAt)
	must(t, err)
	if auditContainsEventID(at, target.ID) {
		t.Errorf("GetAllEventsSince(T) includes the event at exactly T; boundary must be exclusive (created_at > ?)")
	}
	// One second earlier: the same event is included.
	before, err := s.GetAllEventsSince(c, tAt.Add(-time.Second))
	must(t, err)
	if !auditContainsEventID(before, target.ID) {
		t.Errorf("GetAllEventsSince(T-1s) does not include the event at T; want included")
	}
}

func auditContainsEventID(evs []*types.Event, id string) bool {
	for _, e := range evs {
		if e.ID == id {
			return true
		}
	}
	return false
}

// testAuditEventsLimitAndAll pins GetEvents' limit semantics: a positive limit
// truncates, while limit <= 0 means "return all". Asserted by count only, to avoid
// relying on the missing same-second tie-break.
func testAuditEventsLimitAndAll(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-i", Title: "I"}), "a"))
	must(t, s.AddLabel(c, "test-i", "a", "a"))
	must(t, s.AddLabel(c, "test-i", "b", "a"))
	must(t, s.AddLabel(c, "test-i", "c", "a"))

	all, err := s.GetEvents(c, "test-i", 0)
	must(t, err)
	full := len(all)
	if full < 4 {
		t.Fatalf("full event count = %d, want >= 4 (created + 3 label_added)", full)
	}
	two, err := s.GetEvents(c, "test-i", 2)
	must(t, err)
	if len(two) != 2 {
		t.Errorf("GetEvents(limit=2) = %d, want 2", len(two))
	}
	neg, err := s.GetEvents(c, "test-i", -1)
	must(t, err)
	if len(neg) != full {
		t.Errorf("GetEvents(limit=-1) = %d, want %d (limit<=0 means all)", len(neg), full)
	}
}

// testAuditImportCommentSubSecond pins the sub-second contract against the
// embedded-Dolt oracle. The finding predicted truncation-toward-zero, but the
// oracle's `datetime` (precision 0) actually ROUNDS half-up: a comment imported at
// HH:MM:SS.750 reads back at HH:MM:(SS+1). A backend that truncates (would give SS)
// or retains full precision (.750) diverges from the oracle.
func testAuditImportCommentSubSecond(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-i", Title: "I"}), "a"))
	// Sub-second precision is intentionally backend-specific (Dolt datetime(0) rounds
	// half-up; SQLite keeps the fraction), so we test the
	// portable contract every backend honors: a whole-second UTC timestamp round-trips
	// exactly. Higher fidelity on some backends is accepted, not asserted.
	ts := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := s.ImportIssueComment(c, "test-i", "a", "x", ts); err != nil {
		t.Fatalf("ImportIssueComment: %v", err)
	}
	got, err := s.GetIssueComments(c, "test-i")
	must(t, err)
	if len(got) != 1 {
		t.Fatalf("GetIssueComments = %d comments, want 1", len(got))
	}
	if !got[0].CreatedAt.Equal(ts) {
		t.Errorf("read-back created_at = %v, want %v (whole-second round-trip)", got[0].CreatedAt, ts)
	}
}

// testAuditDirectWispLabelComment pins direct-to-wisp write+read routing for
// AddLabel and AddIssueComment issued against an already-existing wisp (not via
// CreateIssue's Labels field): the label, comment, and label_added event all route
// to the wisp_* tables and read back identically.
func testAuditDirectWispLabelComment(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-w", Title: "wisp", Ephemeral: true}), "a"))
	must(t, s.AddLabel(c, "test-w", "z", "a"))
	if _, err := s.AddIssueComment(c, "test-w", "a", "note"); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}

	labels, err := s.GetLabels(c, "test-w")
	must(t, err)
	if len(labels) != 1 || labels[0] != "z" {
		t.Errorf("GetLabels(wisp) = %v, want [z]", labels)
	}
	comments, err := s.GetIssueComments(c, "test-w")
	must(t, err)
	if len(comments) != 1 || comments[0].Text != "note" {
		t.Fatalf("GetIssueComments(wisp) = %+v, want 1 comment 'note'", comments)
	}
	evs, err := s.GetEvents(c, "test-w", 0)
	must(t, err)
	if len(auditEventsOfType(evs, types.EventLabelAdded)) != 1 {
		t.Errorf("wisp label_added events = %d, want 1 (routed to wisp_events)", len(auditEventsOfType(evs, types.EventLabelAdded)))
	}
}

// auditLabelClockAged is the updated_at every label-clock fixture is imported
// with: whole-second and decades before any clock this suite runs under, so a
// write that stamps the wall clock can never tie it.
var auditLabelClockAged = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// auditSeedAgedIssue imports one issue (or, with wisp, one wisp) whose
// updated_at is auditLabelClockAged and which carries labels, and returns its
// RowVersion as stored.
func auditSeedAgedIssue(t *testing.T, s storage.DoltStorage, id string, wisp bool, labels ...string) int64 {
	t.Helper()
	c := ctx()
	must(t, s.CreateIssuesWithFullOptions(c, []*types.Issue{withDefaults(&types.Issue{
		ID: id, Title: id, Priority: 2, Ephemeral: wisp,
		CreatedAt: auditLabelClockAged, UpdatedAt: auditLabelClockAged,
		Labels: labels,
	})}, "seed", storage.BatchCreateOptions{SkipPrefixValidation: true}))
	got, err := s.GetIssue(c, id)
	must(t, err)
	if !got.UpdatedAt.UTC().Equal(auditLabelClockAged) {
		t.Fatalf("seed %s: updated_at = %v, want the imported %v", id, got.UpdatedAt.UTC(), auditLabelClockAged)
	}
	return got.RowVersion
}

// auditAssertLabelClock asserts id's updated_at advanced past the aged seed
// (advanced) or still equals it, and that its RowVersion is still rowVersion:
// a label edit is a content change for recency but not an ownership change,
// so it must never remint the row_lock CAS token.
func auditAssertLabelClock(t *testing.T, s storage.DoltStorage, op, id string, rowVersion int64, advanced bool) {
	t.Helper()
	got, err := s.GetIssue(ctx(), id)
	must(t, err)
	at := got.UpdatedAt.UTC()
	switch {
	case advanced && !at.After(auditLabelClockAged):
		t.Errorf("%s on %s: updated_at = %v, want it advanced past the aged %v", op, id, at, auditLabelClockAged)
	case !advanced && !at.Equal(auditLabelClockAged):
		t.Errorf("%s on %s: updated_at = %v, want the aged %v kept (no label row changed)", op, id, at, auditLabelClockAged)
	}
	if got.RowVersion != rowVersion {
		t.Errorf("%s on %s: RowVersion = %d, want %d unchanged", op, id, got.RowVersion, rowVersion)
	}
}

// testAuditLabelEditsAdvanceUpdatedAt pins GH#5442: a label edit that really
// changes an issue's label set advances that issue's updated_at, on both
// planes and through every write door (the direct store methods, a
// transaction, and a guarded labels-only update), while an idempotent re-add
// or a removal of an absent label leaves it where it was. --updated-after,
// stale-upsert rejection and merge recency all read that column. Neither kind
// of edit remints RowVersion.
func testAuditLabelEditsAdvanceUpdatedAt(t *testing.T, f Factory) {
	for _, plane := range []struct {
		name string
		wisp bool
	}{{"durable", false}, {"wisp", true}} {
		t.Run(plane.name, func(t *testing.T) {
			s := f(t)
			c := ctx()
			lifecycle, err := s.IssueLifecycle()
			must(t, err)
			patch := func(id string, labels publicops.LabelPatch) error {
				_, err := lifecycle.Update(c, publicops.UpdateRequest{
					Actor: "a", IssueID: id, Patch: publicops.IssuePatch{Labels: labels},
				})
				return err
			}
			inTx := func(fn func(tx storage.Transaction) error) error {
				return s.RunInTransaction(c, "bd: label clock", fn)
			}
			cases := []struct {
				op       string
				labels   []string
				advanced bool
				run      func(id string) error
			}{
				{"AddLabel", nil, true, func(id string) error { return s.AddLabel(c, id, "x", "a") }},
				{"RemoveLabel", []string{"x"}, true, func(id string) error { return s.RemoveLabel(c, id, "x", "a") }},
				{"AddLabel re-add", []string{"x"}, false, func(id string) error { return s.AddLabel(c, id, "x", "a") }},
				{"RemoveLabel absent", nil, false, func(id string) error { return s.RemoveLabel(c, id, "x", "a") }},
				{"tx AddLabel", nil, true, func(id string) error {
					return inTx(func(tx storage.Transaction) error { return tx.AddLabel(c, id, "x", "a") })
				}},
				{"tx RemoveLabel", []string{"x"}, true, func(id string) error {
					return inTx(func(tx storage.Transaction) error { return tx.RemoveLabel(c, id, "x", "a") })
				}},
				{"tx AddLabel re-add", []string{"x"}, false, func(id string) error {
					return inTx(func(tx storage.Transaction) error { return tx.AddLabel(c, id, "x", "a") })
				}},
				{"tx RemoveLabel absent", nil, false, func(id string) error {
					return inTx(func(tx storage.Transaction) error { return tx.RemoveLabel(c, id, "x", "a") })
				}},
				{"labels-only update", []string{"x"}, true, func(id string) error {
					return patch(id, publicops.LabelPatch{Add: []string{"y"}, Remove: []string{"x"}})
				}},
				{"labels-only update no-op", []string{"x"}, false, func(id string) error {
					return patch(id, publicops.LabelPatch{Add: []string{"x"}, Remove: []string{"absent"}})
				}},
			}
			for i, tc := range cases {
				id := "test-" + plane.name + "-" + string(rune('a'+i))
				rowVersion := auditSeedAgedIssue(t, s, id, plane.wisp, tc.labels...)
				if err := tc.run(id); err != nil {
					t.Fatalf("%s on %s: %v", tc.op, id, err)
				}
				auditAssertLabelClock(t, s, tc.op, id, rowVersion, tc.advanced)
			}
		})
	}
}

// testAuditLabelRenameAdvancesCarriers pins the rename half of GH#5442: every
// issue and wisp that carried the renamed label, including one that already
// carried the new name (the merge), advances its updated_at; an issue that
// never carried it keeps its own. RowVersion stays put throughout.
func testAuditLabelRenameAdvancesCarriers(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()
	type fixture struct {
		id       string
		wisp     bool
		labels   []string
		advanced bool
	}
	fixtures := []fixture{
		{"test-carrier", false, []string{"old"}, true},
		{"test-merge", false, []string{"old", "new"}, true},
		{"test-wisp-carrier", true, []string{"old"}, true},
		{"test-bystander", false, []string{"other"}, false},
		{"test-wisp-bystander", true, nil, false},
	}
	rowVersions := make(map[string]int64, len(fixtures))
	for _, fx := range fixtures {
		rowVersions[fx.id] = auditSeedAgedIssue(t, s, fx.id, fx.wisp, fx.labels...)
	}
	renamed, merged, _, err := s.RenameLabel(c, "old", "new", "a")
	must(t, err)
	if renamed != 3 || merged != 1 {
		t.Fatalf("RenameLabel = renamed %d merged %d, want 3 and 1", renamed, merged)
	}
	for _, fx := range fixtures {
		auditAssertLabelClock(t, s, "RenameLabel", fx.id, rowVersions[fx.id], fx.advanced)
	}

	// A rename nothing carries touches no owning row.
	ghostVersion := auditSeedAgedIssue(t, s, "test-ghost", false, "keep")
	renamed, _, _, err = s.RenameLabel(c, "never-carried", "phantom", "a")
	must(t, err)
	if renamed != 0 {
		t.Fatalf("RenameLabel of an uncarried label renamed %d, want 0", renamed)
	}
	auditAssertLabelClock(t, s, "RenameLabel (no carriers)", "test-ghost", ghostVersion, false)
}

// RunAudit_labels_comments_events runs the labels-comments-events audit cases.
func RunAudit_labels_comments_events(t *testing.T, f Factory) {
	t.Helper()
	t.Run("EventValueNullability", func(t *testing.T) { testAuditEventValueNullability(t, f) })
	t.Run("LabelEventNonIdempotent", func(t *testing.T) { testAuditLabelEventNonIdempotent(t, f) })
	t.Run("CountCommentsWispParity", func(t *testing.T) { testAuditCountCommentsWispParity(t, f) })
	t.Run("LabelCollationOrder", func(t *testing.T) { testAuditLabelCollationOrder(t, f) })
	t.Run("EventsSinceStrictBoundary", func(t *testing.T) { testAuditEventsSinceStrictBoundary(t, f) })
	t.Run("EventsLimitAndAll", func(t *testing.T) { testAuditEventsLimitAndAll(t, f) })
	t.Run("ImportCommentSubSecond", func(t *testing.T) { testAuditImportCommentSubSecond(t, f) })
	t.Run("DirectWispLabelComment", func(t *testing.T) { testAuditDirectWispLabelComment(t, f) })
	t.Run("LabelEditsAdvanceUpdatedAt", func(t *testing.T) { testAuditLabelEditsAdvanceUpdatedAt(t, f) })
	t.Run("LabelRenameAdvancesCarriers", func(t *testing.T) { testAuditLabelRenameAdvancesCarriers(t, f) })
}
