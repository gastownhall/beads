//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_detail_reads_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The four getIssue riders and the anchored dependency-records read, run as a
// DUAL RUN: the same off-role method asked of the reference store directly and
// of the http client, and the two answers compared (ga-b8ddd.30).
//
// These are raw methods rather than roles, so they carry no backend/conformance
// contract — the audit tier and this served parity file are the right shape. The
// comparison is what the encode gate cannot make: the parameters round-trip
// through the server's decoder there, but whether the ROWS come back the same is
// a fact only a run against the served reference can establish. Here both sides
// run it.
//
// Ordering is normalized before every comparison. None of these reads promises a
// stable order across the two code paths — the reference reads them straight off
// the table, the wire assembles them through the detail view — so the parity that
// matters is the row SET and each row's fields, not the sequence.

func sortMetadata(rows []*types.IssueWithDependencyMetadata) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Issue.ID != rows[j].Issue.ID {
			return rows[i].Issue.ID < rows[j].Issue.ID
		}
		return rows[i].DependencyType < rows[j].DependencyType
	})
}

func sortComments(rows []*types.Comment) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
}

// shallowMetadata projects a dependents list onto the identity-and-shape fields
// its callers consume, mirroring workapi.shallowDep. The include_dependents arm
// of getIssue streams dependents in exactly this shape (be-4d36f2: hub beads
// with thousands of dependents made the full-row marshal allocate gigabytes), so
// it is the shape a dependents read answers over http and the shape `bd show`
// renders — id, status, type, priority, title, plus the edge type. Comparing the
// full local rows would fail on free-form fields no dependents consumer reads;
// comparing this projection is the parity that is actually promised.
func shallowMetadata(rows []*types.IssueWithDependencyMetadata) []*types.IssueWithDependencyMetadata {
	out := make([]*types.IssueWithDependencyMetadata, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, &types.IssueWithDependencyMetadata{
			Issue: types.Issue{
				ID:        row.Issue.ID,
				Status:    row.Issue.Status,
				IssueType: row.Issue.IssueType,
				Priority:  row.Issue.Priority,
				Title:     row.Issue.Title,
			},
			DependencyType: row.DependencyType,
		})
	}
	return out
}

func sortRecords(rows []*types.Dependency) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].DependsOnID != rows[j].DependsOnID {
			return rows[i].DependsOnID < rows[j].DependsOnID
		}
		return rows[i].Type < rows[j].Type
	})
}

// seedDetailReadsFixture plants a subject issue with labels, a comment, one
// outgoing dependency and one incoming dependent, so every one of the five reads
// has a non-empty answer to compare.
func seedDetailReadsFixture(t *testing.T, ctx context.Context, e *servedEnv) {
	t.Helper()
	rows := []*types.Issue{
		{ID: "drd-root", Title: "root", Status: types.StatusOpen, IssueType: types.TypeTask, Labels: []string{"drd-alpha", "drd-beta"}},
		{ID: "drd-dep", Title: "a dependency of root", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "drd-ant", Title: "a dependent of root", Status: types.StatusOpen, IssueType: types.TypeTask},
	}
	for _, row := range rows {
		if err := e.createIssue(ctx, row, "seed"); err != nil {
			t.Fatalf("seed %s: %v", row.ID, err)
		}
	}
	// root depends on drd-dep; drd-ant depends on root.
	for _, edge := range []*types.Dependency{
		{IssueID: "drd-root", DependsOnID: "drd-dep", Type: types.DepBlocks},
		{IssueID: "drd-ant", DependsOnID: "drd-root", Type: types.DepBlocks},
	} {
		if err := e.addDependency(ctx, edge, "seed"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", edge.IssueID, edge.DependsOnID, err)
		}
	}
	// Two comments, by different authors, so the thread read is exercised for
	// ORDERING and PER-FIELD carriage rather than for a single row that could pass
	// a shallow projection by accident.
	for _, c := range []struct{ author, text string }{
		{"seed", "a comment the detail view carries"},
		{"reviewer", "a second comment, so ordering and per-field parity are exercised"},
	} {
		if err := e.addComment(ctx, "drd-root", c.author, c.text); err != nil {
			t.Fatalf("seed the comment %q: %v", c.text, err)
		}
	}
}

// TestServedDetailReadsAgreeWithTheReferenceStore is the parity pin for all five
// flips: each off-role read answers the same value over http that it answers
// locally.
func TestServedDetailReadsAgreeWithTheReferenceStore(t *testing.T) {
	e := newServedEnv(t, "drd")
	ctx := t.Context()
	seedDetailReadsFixture(t, ctx, e)

	t.Run("GetLabels", func(t *testing.T) {
		want, err := e.reference.GetLabels(ctx, "drd-root")
		if err != nil {
			t.Fatalf("reference GetLabels: %v", err)
		}
		got, err := e.subject.GetLabels(ctx, "drd-root")
		if err != nil {
			t.Fatalf("http GetLabels: %v", err)
		}
		sort.Strings(want)
		sort.Strings(got)
		assertSameJSON(t, "GetLabels", want, got)
	})

	t.Run("GetDependenciesWithMetadata", func(t *testing.T) {
		want, err := e.reference.GetDependenciesWithMetadata(ctx, "drd-root")
		if err != nil {
			t.Fatalf("reference GetDependenciesWithMetadata: %v", err)
		}
		got, err := e.subject.GetDependenciesWithMetadata(ctx, "drd-root")
		if err != nil {
			t.Fatalf("http GetDependenciesWithMetadata: %v", err)
		}
		if len(want) != 1 || want[0].Issue.ID != "drd-dep" {
			t.Fatalf("the REFERENCE answered %+v, the fixture expects one edge onto drd-dep", want)
		}
		sortMetadata(want)
		sortMetadata(got)
		assertSameJSON(t, "GetDependenciesWithMetadata", want, got)
	})

	t.Run("GetDependentsWithMetadata is carried only when asked", func(t *testing.T) {
		want, err := e.reference.GetDependentsWithMetadata(ctx, "drd-root")
		if err != nil {
			t.Fatalf("reference GetDependentsWithMetadata: %v", err)
		}
		got, err := e.subject.GetDependentsWithMetadata(ctx, "drd-root")
		if err != nil {
			t.Fatalf("http GetDependentsWithMetadata: %v", err)
		}
		if len(want) != 1 || want[0].Issue.ID != "drd-ant" {
			t.Fatalf("the REFERENCE answered %+v, the fixture expects one dependent drd-ant", want)
		}
		// The dependents arm answers the shallow include_dependents shape, so the
		// parity is over the fields that shape carries and `bd show` renders.
		wantShallow := shallowMetadata(want)
		gotShallow := shallowMetadata(got)
		sortMetadata(wantShallow)
		sortMetadata(gotShallow)
		assertSameJSON(t, "GetDependentsWithMetadata", wantShallow, gotShallow)
	})

	t.Run("GetIssueComments carry the WHOLE comment, not a shallow projection", func(t *testing.T) {
		want, err := e.reference.GetIssueComments(ctx, "drd-root")
		if err != nil {
			t.Fatalf("reference GetIssueComments: %v", err)
		}
		got, err := e.subject.GetIssueComments(ctx, "drd-root")
		if err != nil {
			t.Fatalf("http GetIssueComments: %v", err)
		}
		if len(want) != 2 {
			t.Fatalf("the REFERENCE answered %d comments, the fixture seeded two", len(want))
		}
		sortComments(want)
		sortComments(got)
		// The comment thread rides include_comments as the FULL types.Comment —
		// unlike the shallow dependents arm — so the http answer must carry every
		// field a reply renders. A zeroed CreatedAt would sort a reply ahead of the
		// root and blank its byline, and an empty Author/Text would gut the render.
		// Assert the carriage BY FIELD before the whole-value parity, so a future
		// shallow projection fails here by name rather than only through the opaque
		// assertSameJSON diff. This is the pin the too-shallow prior test lacked.
		if len(got) != len(want) {
			t.Fatalf("http answered %d comments, want %d", len(got), len(want))
		}
		for i, c := range got {
			if c.CreatedAt.IsZero() {
				t.Errorf("http comment %d has a zero CreatedAt: the wire dropped it, which would break thread ordering", i)
			}
			if c.Author == "" || c.Text == "" {
				t.Errorf("http comment %d dropped Author/Text: %+v", i, c)
			}
		}
		assertSameJSON(t, "GetIssueComments", want, got)
	})

	t.Run("GetDependentsWithMetadata is SHALLOW over http (why bd show --thread/--refs/--children refuse)", func(t *testing.T) {
		ref, err := e.reference.GetDependentsWithMetadata(ctx, "drd-root")
		if err != nil {
			t.Fatalf("reference GetDependentsWithMetadata: %v", err)
		}
		got, err := e.subject.GetDependentsWithMetadata(ctx, "drd-root")
		if err != nil {
			t.Fatalf("http GetDependentsWithMetadata: %v", err)
		}
		if len(ref) != 1 || len(got) != 1 {
			t.Fatalf("fixture expects one dependent on each side, got ref=%d http=%d", len(ref), len(got))
		}
		// The reference reads the full row off the table; the wire answers the
		// collectDependents shallow projection (be-4d36f2), which drops the
		// free-form fields. CreatedAt is the reliable discriminator — the store
		// stamps it on create, so the full row carries it and the shallow one
		// zeroes it. This divergence is deliberate and is EXACTLY why the three
		// reverse-view show modes refuse rather than render off it; pin it so a
		// change that makes the wire look full also has to revisit those refusals.
		if ref[0].Issue.CreatedAt.IsZero() {
			t.Fatalf("the reference dependent has a zero CreatedAt, so this test cannot tell shallow from full")
		}
		if !got[0].Issue.CreatedAt.IsZero() {
			t.Errorf("http dependent CreatedAt = %v, want zero: the wire is supposed to answer the shallow projection", got[0].Issue.CreatedAt)
		}
		// The shallow fields the show-text renderer consumes DO ride the wire.
		if got[0].Issue.ID != "drd-ant" || got[0].Issue.Title != ref[0].Issue.Title || got[0].Issue.Status != ref[0].Issue.Status {
			t.Errorf("the shallow fields diverged: ref=%+v http=%+v", ref[0].Issue, got[0].Issue)
		}
	})

	t.Run("GetDependencyRecords", func(t *testing.T) {
		want, err := e.reference.GetDependencyRecords(ctx, "drd-root")
		if err != nil {
			t.Fatalf("reference GetDependencyRecords: %v", err)
		}
		got, err := e.subject.GetDependencyRecords(ctx, "drd-root")
		if err != nil {
			t.Fatalf("http GetDependencyRecords: %v", err)
		}
		if len(want) != 1 || want[0].DependsOnID != "drd-dep" {
			t.Fatalf("the REFERENCE answered %+v, the fixture expects one record onto drd-dep", want)
		}
		sortRecords(want)
		sortRecords(got)
		assertSameJSON(t, "GetDependencyRecords", want, got)
	})
}

// TestServedDetailReadsMissParity is the (nil, nil) contract each flip carries:
// a nonexistent id answers empty with no error on BOTH sides, because the front
// doors read a nil result as "not found" and an error would print the
// transport's vocabulary in its place.
func TestServedDetailReadsMissParity(t *testing.T) {
	e := newServedEnv(t, "drm")
	ctx := t.Context()
	const absent = "drm-nope"

	labels, err := e.subject.GetLabels(ctx, absent)
	if err != nil || labels != nil {
		t.Errorf("GetLabels on a miss = (%v, %v), want (nil, nil)", labels, err)
	}
	deps, err := e.subject.GetDependenciesWithMetadata(ctx, absent)
	if err != nil || deps != nil {
		t.Errorf("GetDependenciesWithMetadata on a miss = (%v, %v), want (nil, nil)", deps, err)
	}
	dependents, err := e.subject.GetDependentsWithMetadata(ctx, absent)
	if err != nil || dependents != nil {
		t.Errorf("GetDependentsWithMetadata on a miss = (%v, %v), want (nil, nil)", dependents, err)
	}
	comments, err := e.subject.GetIssueComments(ctx, absent)
	if err != nil || comments != nil {
		t.Errorf("GetIssueComments on a miss = (%v, %v), want (nil, nil)", comments, err)
	}
	records, err := e.subject.GetDependencyRecords(ctx, absent)
	if err != nil || records != nil {
		t.Errorf("GetDependencyRecords on a miss = (%v, %v), want (nil, nil)", records, err)
	}

	// The reference answers the same way, so the parity is a shared contract
	// rather than a client-side convention. ALL FIVE flipped reads are pinned on
	// the reference, not just two: the "answer a miss with empty-and-no-error,
	// never the transport's 404 vocabulary" contract has to hold for every read
	// the http client mirrors, and spot-checking two of five left the other three
	// free to drift.
	if got, err := e.reference.GetLabels(ctx, absent); err != nil || got != nil {
		t.Errorf("reference GetLabels on a miss = (%v, %v), want (nil, nil)", got, err)
	}
	if got, err := e.reference.GetDependenciesWithMetadata(ctx, absent); err != nil || got != nil {
		t.Errorf("reference GetDependenciesWithMetadata on a miss = (%v, %v), want (nil, nil)", got, err)
	}
	if got, err := e.reference.GetDependentsWithMetadata(ctx, absent); err != nil || got != nil {
		t.Errorf("reference GetDependentsWithMetadata on a miss = (%v, %v), want (nil, nil)", got, err)
	}
	if got, err := e.reference.GetIssueComments(ctx, absent); err != nil || got != nil {
		t.Errorf("reference GetIssueComments on a miss = (%v, %v), want (nil, nil)", got, err)
	}
	if got, err := e.reference.GetDependencyRecords(ctx, absent); err != nil || got != nil {
		t.Errorf("reference GetDependencyRecords on a miss = (%v, %v), want (nil, nil)", got, err)
	}
}

// TestServedDependencyRecordsSurfaceUnresolvedRowsVerbatim is L17's dual run: the
// question the flip of GetDependencyRecords waited on.
//
// `bd dep remove`'s GH#5005 guard (exactDependencyTarget) reads the raw
// dependency rows and matches DependsOnID against the bare target the user typed,
// so a wrong-edge deletion is prevented only if an UNRESOLVED depends_on_id —
// one the database cannot resolve to any issue — is surfaced verbatim rather than
// dropped or normalized. This seeds exactly such a row (an external reference the
// workspace holds no issue for) beside a normal edge and compares the client's
// GetDependencyRecords against the reference's. If the unresolved row survives
// the wire whole, there is no divergence to ledger — which is the outcome this
// asserts and which retired L17.
func TestServedDependencyRecordsSurfaceUnresolvedRowsVerbatim(t *testing.T) {
	e := newServedEnv(t, "drx")
	ctx := t.Context()

	for _, row := range []*types.Issue{
		{ID: "drx-src", Title: "source", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "drx-tgt", Title: "a real target", Status: types.StatusOpen, IssueType: types.TypeTask},
	} {
		if err := e.createIssue(ctx, row, "seed"); err != nil {
			t.Fatalf("seed %s: %v", row.ID, err)
		}
	}
	// A normal, resolvable edge, and an UNRESOLVED one whose target names no
	// issue this workspace holds — the shape the guard exists to see. The
	// external reference routes into depends_on_external and skips the target
	// existence check, so it is stored verbatim exactly as the pre-GH#5005 bare
	// slug was.
	for _, edge := range []*types.Dependency{
		{IssueID: "drx-src", DependsOnID: "drx-tgt", Type: types.DepBlocks},
		{IssueID: "drx-src", DependsOnID: "external:ghost-slug", Type: types.DepRelated},
	} {
		if err := e.addDependency(ctx, edge, "seed"); err != nil {
			t.Fatalf("seed edge %s -> %s: %v", edge.IssueID, edge.DependsOnID, err)
		}
	}

	want, err := e.reference.GetDependencyRecords(ctx, "drx-src")
	if err != nil {
		t.Fatalf("reference GetDependencyRecords: %v", err)
	}
	got, err := e.subject.GetDependencyRecords(ctx, "drx-src")
	if err != nil {
		t.Fatalf("http GetDependencyRecords: %v", err)
	}

	// The reference is the oracle: assert the fixture really produced the
	// unresolved row before comparing, so a fixture that stopped seeding it
	// cannot make the parity pass vacuously.
	var haveUnresolved bool
	for _, r := range want {
		if r.DependsOnID == "external:ghost-slug" {
			haveUnresolved = true
		}
	}
	if len(want) != 2 || !haveUnresolved {
		t.Fatalf("the REFERENCE answered %+v, want two records including the unresolved external:ghost-slug", want)
	}

	sortRecords(want)
	sortRecords(got)
	assertSameJSON(t, "GetDependencyRecords over an unresolved target", want, got)
}
