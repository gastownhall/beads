package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// listFiltersParams is the vocabulary issues.list.filters publishes on
// listIssues, in the document's order. It is a list here and not a read-back of
// listFilterParams alone because the parity test below compares the two: a name
// the handler reads and this list does not hold is an unannounced filter, and a
// name here the handler never reads is a document promise the server refuses.
var listFiltersParams = []string{
	"title_contains", "desc_contains", "notes_contains", "external_contains", "external_ref",
	"spec", "label_pattern", "label_regex",
	"updated_after", "updated_before", "closed_after", "closed_before",
	"defer_after", "defer_before", "due_after", "due_before",
	"priority", "priority_min", "priority_max",
	"empty_description", "no_labels", "no_parent", "exclude_type", "exclude_status",
	"pinned", "no_pinned", "deferred", "overdue",
	"mol_type", "wisp_type",
}

// TestListFilterParametersMatchTheDocument is mechanical in both directions:
// driving listFilterParams over an empty query yields exactly the names it
// reads (query.read; it is straight-line, so an empty query exercises every
// accessor), and each of those must be a listIssues parameter whose description
// cites the token, so an older-server client can tell why it was refused.
func TestListFilterParametersMatchTheDocument(t *testing.T) {
	q := newQuery(url.Values{})
	var req issueops.ListRequest
	listFilterParams(q, &req)

	var read []string
	for name := range q.read {
		read = append(read, name)
	}
	slices.Sort(read)
	want := slices.Clone(listFiltersParams)
	slices.Sort(want)
	if !slices.Equal(read, want) {
		t.Errorf("listFilterParams reads %v, want exactly %v", read, want)
	}
	if len(want) != 30 {
		t.Errorf("issues.list.filters publishes %d parameters, want 30", len(want))
	}

	doc := loadSpec(t)
	so := specOps(t, doc)["listIssues"]
	for _, name := range listFiltersParams {
		p := specParam(t, so, name)
		desc, _ := p["description"].(string)
		if !strings.Contains(desc, "`issues.list.filters`") {
			t.Errorf("listIssues parameter %q does not cite issues.list.filters in its description", name)
		}
	}
}

// TestListFilterEnumsMatchTheTypesVocabulary keeps mol_type's and wisp_type's
// documented enums equal to the server's accepted sets, which are read off the
// types package `bd list` validates against.
func TestListFilterEnumsMatchTheTypesVocabulary(t *testing.T) {
	doc := loadSpec(t)
	so := specOps(t, doc)["listIssues"]
	for _, tc := range []struct {
		param string
		want  []string
	}{
		{"mol_type", molTypeNames()},
		{"wisp_type", wispTypeNames()},
	} {
		schema, ok := specParam(t, so, tc.param)["schema"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no schema", tc.param)
		}
		if got := toStrings(t, schema["enum"]); !slices.Equal(got, tc.want) {
			t.Errorf("%s enum = %v, the server accepts %v", tc.param, got, tc.want)
		}
	}
}

// TestListFilterParametersReachTheStorageFilter is the handler half of
// issues.list.filters: each parameter reaches the storage seam, through the
// Reader role, as the filter field the local `bd list` flag produces, and a
// listing that sends none narrows nothing.
func TestListFilterParametersReachTheStorageFilter(t *testing.T) {
	const instant = "2026-03-04T05:06:07Z"
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	isAt := func(t *testing.T, name string, got *time.Time) {
		t.Helper()
		if got == nil || !got.Equal(at) {
			t.Errorf("%s = %v, want %v", name, got, at)
		}
	}
	isInt := func(t *testing.T, name string, got *int, want int) {
		t.Helper()
		if got == nil || *got != want {
			t.Errorf("%s = %v, want %d", name, got, want)
		}
	}
	for _, tc := range []struct {
		query string
		check func(t *testing.T, f types.IssueFilter)
	}{
		{"", func(t *testing.T, f types.IssueFilter) {
			if f.TitleContains != "" || f.DescriptionContains != "" || f.NotesContains != "" ||
				f.ExternalRefContains != "" || f.ExternalRef != nil || f.SpecIDPrefix != "" ||
				f.LabelPattern != "" || f.LabelRegex != "" ||
				f.UpdatedAfter != nil || f.UpdatedBefore != nil || f.ClosedAfter != nil || f.ClosedBefore != nil ||
				f.DeferAfter != nil || f.DeferBefore != nil || f.DueAfter != nil || f.DueBefore != nil ||
				f.Priority != nil || f.PriorityMin != nil || f.PriorityMax != nil ||
				f.EmptyDescription || f.NoLabels || f.NoParent || slices.Contains(f.ExcludeTypes, "epic") ||
				!slices.Equal(f.ExcludeStatus, []types.Status{types.StatusClosed, types.StatusPinned}) ||
				f.Deferred || f.Overdue || f.MolType != nil || f.WispType != nil {
				t.Errorf("a listing with no filter parameter narrowed the filter: %+v", f)
			}
			if f.Pinned == nil || *f.Pinned {
				t.Errorf("default Pinned = %v, want the role's false", f.Pinned)
			}
		}},
		{"title_contains=Routed", func(t *testing.T, f types.IssueFilter) {
			if f.TitleContains != "Routed" || f.TitleSearch != "" {
				t.Errorf("TitleContains = %q TitleSearch = %q, want Routed and empty", f.TitleContains, f.TitleSearch)
			}
		}},
		{"desc_contains=d", func(t *testing.T, f types.IssueFilter) {
			if f.DescriptionContains != "d" {
				t.Errorf("DescriptionContains = %q", f.DescriptionContains)
			}
		}},
		{"notes_contains=n", func(t *testing.T, f types.IssueFilter) {
			if f.NotesContains != "n" {
				t.Errorf("NotesContains = %q", f.NotesContains)
			}
		}},
		{"external_contains=gh-", func(t *testing.T, f types.IssueFilter) {
			if f.ExternalRefContains != "gh-" {
				t.Errorf("ExternalRefContains = %q", f.ExternalRefContains)
			}
		}},
		{"external_ref=gh-7", func(t *testing.T, f types.IssueFilter) {
			if f.ExternalRef == nil || *f.ExternalRef != "gh-7" {
				t.Errorf("ExternalRef = %v", f.ExternalRef)
			}
		}},
		{"spec=docs/", func(t *testing.T, f types.IssueFilter) {
			if f.SpecIDPrefix != "docs/" {
				t.Errorf("SpecIDPrefix = %q", f.SpecIDPrefix)
			}
		}},
		{"label_pattern=tech-*", func(t *testing.T, f types.IssueFilter) {
			if f.LabelPattern != "tech-*" {
				t.Errorf("LabelPattern = %q", f.LabelPattern)
			}
		}},
		{"label_regex=tech-.%2B", func(t *testing.T, f types.IssueFilter) {
			if f.LabelRegex != "tech-.+" {
				t.Errorf("LabelRegex = %q", f.LabelRegex)
			}
		}},
		{"updated_after=" + instant, func(t *testing.T, f types.IssueFilter) { isAt(t, "UpdatedAfter", f.UpdatedAfter) }},
		{"updated_before=" + instant, func(t *testing.T, f types.IssueFilter) { isAt(t, "UpdatedBefore", f.UpdatedBefore) }},
		{"closed_after=" + instant, func(t *testing.T, f types.IssueFilter) { isAt(t, "ClosedAfter", f.ClosedAfter) }},
		{"closed_before=" + instant, func(t *testing.T, f types.IssueFilter) { isAt(t, "ClosedBefore", f.ClosedBefore) }},
		{"defer_after=" + instant, func(t *testing.T, f types.IssueFilter) { isAt(t, "DeferAfter", f.DeferAfter) }},
		{"defer_before=" + instant, func(t *testing.T, f types.IssueFilter) { isAt(t, "DeferBefore", f.DeferBefore) }},
		{"due_after=" + instant, func(t *testing.T, f types.IssueFilter) { isAt(t, "DueAfter", f.DueAfter) }},
		{"due_before=" + instant, func(t *testing.T, f types.IssueFilter) { isAt(t, "DueBefore", f.DueBefore) }},
		{"priority=0", func(t *testing.T, f types.IssueFilter) { isInt(t, "Priority", f.Priority, 0) }},
		{"priority_min=1", func(t *testing.T, f types.IssueFilter) { isInt(t, "PriorityMin", f.PriorityMin, 1) }},
		{"priority_max=3", func(t *testing.T, f types.IssueFilter) { isInt(t, "PriorityMax", f.PriorityMax, 3) }},
		{"empty_description=true", func(t *testing.T, f types.IssueFilter) {
			if !f.EmptyDescription {
				t.Error("EmptyDescription not set")
			}
		}},
		{"no_labels=true", func(t *testing.T, f types.IssueFilter) {
			if !f.NoLabels {
				t.Error("NoLabels not set")
			}
		}},
		{"no_parent=true", func(t *testing.T, f types.IssueFilter) {
			if !f.NoParent {
				t.Error("NoParent not set")
			}
		}},
		{"exclude_type=epic&exclude_type=chore,bug", func(t *testing.T, f types.IssueFilter) {
			// After the role's own default exclusions, which it prepends.
			want := []types.IssueType{"epic", "chore", "bug"}
			if n := len(f.ExcludeTypes); n < len(want) || !slices.Equal(f.ExcludeTypes[n-len(want):], want) {
				t.Errorf("ExcludeTypes = %v, want %v (repeated, and each value comma-split by the role)", f.ExcludeTypes, want)
			}
		}},
		{"exclude_status=blocked&exclude_status=deferred,in_progress", func(t *testing.T, f types.IssueFilter) {
			// APPENDED to the role's default exclusions (closed, pinned),
			// which it computes first; never in place of them.
			want := []types.Status{types.StatusClosed, types.StatusPinned, "blocked", "deferred", "in_progress"}
			if !slices.Equal(f.ExcludeStatus, want) {
				t.Errorf("ExcludeStatus = %v, want %v", f.ExcludeStatus, want)
			}
		}},
		{"exclude_status=closed&all=true", func(t *testing.T, f types.IssueFilter) {
			// Under `all` there are no defaults, so the caller's list is the
			// whole exclusion: the active-set read.
			if want := []types.Status{types.StatusClosed}; !slices.Equal(f.ExcludeStatus, want) {
				t.Errorf("ExcludeStatus = %v, want %v", f.ExcludeStatus, want)
			}
		}},
		{"pinned=true", func(t *testing.T, f types.IssueFilter) {
			if f.Pinned == nil || !*f.Pinned {
				t.Errorf("Pinned = %v, want true", f.Pinned)
			}
		}},
		{"no_pinned=true&all=true", func(t *testing.T, f types.IssueFilter) {
			// `all` alone drops the pinned predicate; no_pinned holds it.
			if f.Pinned == nil || *f.Pinned {
				t.Errorf("Pinned = %v, want false held under all", f.Pinned)
			}
		}},
		{"deferred=true", func(t *testing.T, f types.IssueFilter) {
			if !f.Deferred {
				t.Error("Deferred not set")
			}
		}},
		{"overdue=true", func(t *testing.T, f types.IssueFilter) {
			if !f.Overdue {
				t.Error("Overdue not set")
			}
		}},
		{"mol_type=swarm", func(t *testing.T, f types.IssueFilter) {
			if f.MolType == nil || *f.MolType != types.MolTypeSwarm {
				t.Errorf("MolType = %v, want swarm", f.MolType)
			}
		}},
		{"wisp_type=gc_report", func(t *testing.T, f types.IssueFilter) {
			if f.WispType == nil || *f.WispType != types.WispTypeGCReport {
				t.Errorf("WispType = %v, want gc_report", f.WispType)
			}
		}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			ts, rec := newReadServer(t, Config{})
			path := "/v0/beads/issues"
			if tc.query != "" {
				path += "?" + tc.query
			}
			if resp := ts.get(t, path); resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200 (body %v)", path, resp.StatusCode, decodeBody(t, resp))
			}
			filters := rec.searchFilters()
			if len(filters) != 1 {
				t.Fatalf("search calls = %d, want 1", len(filters))
			}
			tc.check(t, filters[0])
		})
	}
}

// TestListFilterTimeBoundsAreNormalizedToUTC: a bound carrying an offset
// reaches storage as the SAME instant in UTC. A zone-less timestamp column reads
// only a literal's wall clock, so a `+02:00` bound spelled as-is would compare
// two hours off; sqlbuild binds every range bound in UTC for that reason, and
// this decode is the route's own guard, so the value it hands storage is
// already the UTC instant. Every bound on the operation goes through the one
// decoder, the created bounds included.
func TestListFilterTimeBoundsAreNormalizedToUTC(t *testing.T) {
	want := time.Date(2026, 3, 4, 3, 6, 7, 0, time.UTC)
	for _, param := range []string{
		"created_after", "created_before", "updated_after", "updated_before", "closed_after", "closed_before",
		"defer_after", "defer_before", "due_after", "due_before",
	} {
		t.Run(param, func(t *testing.T) {
			ts, rec := newReadServer(t, Config{})
			path := "/v0/beads/issues?" + param + "=" + url.QueryEscape("2026-03-04T05:06:07+02:00")
			if resp := ts.get(t, path); resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: status = %d", path, resp.StatusCode)
			}
			f := rec.searchFilters()[0]
			got := map[string]*time.Time{
				"created_after": f.CreatedAfter, "created_before": f.CreatedBefore,
				"updated_after": f.UpdatedAfter, "updated_before": f.UpdatedBefore,
				"closed_after": f.ClosedAfter, "closed_before": f.ClosedBefore,
				"defer_after": f.DeferAfter, "defer_before": f.DeferBefore,
				"due_after": f.DueAfter, "due_before": f.DueBefore,
			}[param]
			if got == nil || !got.Equal(want) {
				t.Fatalf("%s = %v, want the instant %v", param, got, want)
			}
			if got.Location() != time.UTC {
				t.Errorf("%s reached storage in zone %v, want UTC", param, got.Location())
			}
			// The sub-second part survives the decode; truncation to the
			// second is the storage layer's, on every route alike.
			if got.Format(time.RFC3339) != "2026-03-04T03:06:07Z" {
				t.Errorf("%s formats as %s, want 2026-03-04T03:06:07Z", param, got.Format(time.RFC3339))
			}
		})
	}
}

// TestListFilterMalformedValuesAreRefusedByName: a value the server cannot
// read is a 400 invalid_value naming the parameter — never ignored, which would
// widen the listing, and never unknown_parameter, which a client reads as skew.
func TestListFilterMalformedValuesAreRefusedByName(t *testing.T) {
	for _, tc := range []struct{ param, value string }{
		{"updated_after", "yesterday"},
		{"closed_before", "2026-03-04"},
		{"due_after", "2026-03-04 05:06:07"},
		{"priority", "high"},
		{"priority_min", "1.5"},
		{"no_parent", "maybe"},
		{"overdue", "yes please"},
		{"mol_type", "squad"},
		{"wisp_type", "Heartbeat"},
		// Cross-vocabulary: each enum accepts its OWN list only, so a value
		// valid for the other one is refused, not matched against nothing.
		{"mol_type", "heartbeat"},
		{"wisp_type", "swarm"},
		// Present-but-empty is not absent: the role's empty pointer is a real
		// `= ''` predicate, so reading it as absent would widen the listing.
		{"wisp_type", ""},
	} {
		t.Run(tc.param+"="+tc.value, func(t *testing.T) {
			ts, rec := newReadServer(t, Config{})
			resp := ts.get(t, "/v0/beads/issues?"+tc.param+"="+url.QueryEscape(tc.value))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) || body["param"] != tc.param || body["reason"] != string(ReasonInvalidValue) {
				t.Errorf("body = %v, want invalid_argument on %s with reason invalid_value", body, tc.param)
			}
			if n := len(rec.searchFilters()); n != 0 {
				t.Errorf("a refused request reached storage %d time(s)", n)
			}
		})
	}
}

// TestListFilterSingleValuedParametersRefuseARepeat: only exclude_type repeats;
// a second value of any other is refused rather than silently resolved to one.
func TestListFilterSingleValuedParametersRefuseARepeat(t *testing.T) {
	ts, _ := newReadServer(t, Config{})
	resp := ts.get(t, "/v0/beads/issues?closed_after=2026-03-04T05:06:07Z&closed_after=2026-03-05T05:06:07Z")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if body := decodeBody(t, resp); body["param"] != "closed_after" {
		t.Errorf("body = %v, want param closed_after", body)
	}
}

// TestListFilterPinnedPairIsRefused: `pinned` with `no_pinned` is the role's
// ErrValidation (workapi.BuildListFilter), the refusal `bd list --pinned
// --no-pinned` gives locally, surfaced as a 400 rather than PinnedFlag
// silently winning.
func TestListFilterPinnedPairIsRefused(t *testing.T) {
	ts, rec := newReadServer(t, Config{})
	resp := ts.get(t, "/v0/beads/issues?pinned=true&no_pinned=true")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %v)", resp.StatusCode, decodeBody(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "no_pinned" || body["reason"] != string(ReasonInvalidValue) {
		t.Errorf("body = %v, want invalid_value on no_pinned", body)
	}
	if n := len(rec.searchFilters()); n != 0 {
		t.Errorf("the contradictory pair reached storage %d time(s)", n)
	}
}

// TestListFilterParentPairIsRefused: `parent` with `no_parent` is refused the
// same way, in the sentence `bd list --parent X --no-parent` and the count
// surface refuse it with, rather than ANDed into the parent's edge-less
// dotted-id children.
func TestListFilterParentPairIsRefused(t *testing.T) {
	ts, rec := newReadServer(t, Config{})
	resp := ts.get(t, "/v0/beads/issues?parent=bd-1&no_parent=true")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %v)", resp.StatusCode, decodeBody(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "no_parent" || body["reason"] != string(ReasonInvalidValue) {
		t.Errorf("body = %v, want invalid_value on no_parent", body)
	}
	if n := len(rec.searchFilters()); n != 0 {
		t.Errorf("the contradictory pair reached storage %d time(s)", n)
	}
}

// TestAnInvalidLabelRegexIsABadRequestNotAServerFault: the database compiles
// label_regex in its own dialect and refuses a pattern it cannot compile with
// "the given regular expression is invalid". On the operations that carry the
// parameter that refusal is the caller's typo, a 400 on label_regex, not a 500
// that pages as a server error. The same text on a request that sent NO regex
// stays a server fault: the attribution is to the parameter, not the words.
func TestAnInvalidLabelRegexIsABadRequestNotAServerFault(t *testing.T) {
	engineErr := errors.New("search count issues: Error 1105 (HY000): the given regular expression is invalid")
	for _, tc := range []struct {
		name, path string
		status     int
	}{
		{"listing", "/v0/beads/issues?label_regex=" + url.QueryEscape("tech-("), http.StatusBadRequest},
		{"ready", "/v0/beads/ready?label_regex=" + url.QueryEscape("tech-("), http.StatusBadRequest},
		{"listing without a regex", "/v0/beads/issues", http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, rec := newReadServer(t, Config{})
			rec.err = engineErr
			resp := ts.get(t, tc.path)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
			if tc.status != http.StatusBadRequest {
				return
			}
			if body := decodeBody(t, resp); body["code"] != string(CodeInvalidArgument) || body["param"] != "label_regex" || body["reason"] != string(ReasonInvalidValue) {
				t.Errorf("body = %v, want invalid_argument on label_regex with reason invalid_value", body)
			}
		})
	}
}
