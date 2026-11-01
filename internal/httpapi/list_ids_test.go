package httpapi

import (
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// listIssues' repeatable `id` (IDFilter, behind issues.list.ids). The member
// was already on issueops.ListRequest; what these pin is the TRANSPORT — the
// repeated parameter reaching the role as the comma set it splits, and the
// bounds the document states being enforced before a request reaches storage.
// The contract and these cases are bd-enterprise's (E9-S2), adopted unchanged
// (its copy of this file also covers include_comments, which OSS does not
// publish).

// TestListIDsReachStorageAsTheExactSet pins the decode: repeated `id` values
// arrive on the storage filter as exactly those ids, and nothing else about the
// request moves. The default exclusions stay in place (the parameter is AND-ed
// with them), which is why the import pre-read sends the five include flags.
func TestListIDsReachStorageAsTheExactSet(t *testing.T) {
	filter := listSortOf(t, "?id=bd-2&id=bd-1&id=bd-wisp-9")
	if want := []string{"bd-2", "bd-1", "bd-wisp-9"}; !slices.Equal(filter.IDs, want) {
		t.Errorf("filter.IDs = %v, want %v", filter.IDs, want)
	}
	if len(filter.ExcludeStatus) == 0 {
		t.Error("an id scope dropped the default status exclusions; the document says `id` is AND-ed with them")
	}

	// The control: no `id` is no id scope at all, not an empty one.
	if got := listSortOf(t, "").IDs; len(got) != 0 {
		t.Errorf("a request with no `id` reached storage with IDs = %v, want none", got)
	}

	// Every exclusion off: the shape the client's bulk reads send. The filter
	// keeps the id set and NOTHING ELSE that could narrow it — not merely the
	// suppressions known today, which is why the check is every field of the
	// filter rather than a list of them: a suppression added to the listing
	// tomorrow that none of the five flags lifts has to fail here, or the
	// import pre-read misses that row kind without a sound.
	all := listSortOf(t, "?id=bd-1&all=true&include_templates=true&include_gates=true&include_infra=true&include_ephemeral=true")
	if !slices.Equal(all.IDs, []string{"bd-1"}) {
		t.Errorf("filter.IDs = %v, want [bd-1]", all.IDs)
	}
	if extra := setFilterFields(all, "IDs", "Limit", "SortBy"); len(extra) != 0 {
		t.Errorf("the every-row-kind request still narrows the id set: %s set on the storage filter %+v", strings.Join(extra, ", "), all)
	}
}

// setFilterFields names every field of a storage filter that is set — a
// non-empty slice or map, a non-nil pointer, a true bool, a non-empty string, a
// non-zero number — other than the ones skipped.
func setFilterFields(f types.IssueFilter, skip ...string) []string {
	v := reflect.ValueOf(f)
	var set []string
	for i := range v.NumField() {
		name := v.Type().Field(i).Name
		if slices.Contains(skip, name) {
			continue
		}
		field := v.Field(i)
		switch field.Kind() {
		case reflect.Slice, reflect.Map:
			if field.Len() > 0 {
				set = append(set, name)
			}
		default:
			if !field.IsZero() {
				set = append(set, name)
			}
		}
	}
	return set
}

// TestListIDsAtTheBoundIsServed is the inclusive edge of maxItems: exactly 200
// ids is a request the document admits.
func TestListIDsAtTheBoundIsServed(t *testing.T) {
	filter := listSortOf(t, "?"+idQuery(maxListIDs))
	if len(filter.IDs) != maxListIDs {
		t.Errorf("len(filter.IDs) = %d, want %d", len(filter.IDs), maxListIDs)
	}
}

func idQuery(n int) string {
	v := url.Values{}
	for i := range n {
		v.Add("id", "bd-"+strconv.Itoa(i+1))
	}
	return v.Encode()
}

// TestListIDsRefusals pins every refusal the member
// carries, each beside a control that differs in exactly the refused value, so a
// 400 cannot be read as some other parameter's.
func TestListIDsRefusals(t *testing.T) {
	long := strings.Repeat("x", types.MaxFieldLen+1)
	for _, tc := range []struct {
		name    string
		path    string
		control string
		param   string
	}{
		{
			name:    "more ids than maxItems",
			path:    "/v0/beads/issues?" + idQuery(maxListIDs+1),
			control: "/v0/beads/issues?" + idQuery(maxListIDs),
			param:   "id",
		},
		{
			// The role splits the joined set on commas, so a value carrying
			// one would silently address ids the caller never named.
			name:    "an id carrying a comma",
			path:    "/v0/beads/issues?id=" + url.QueryEscape("bd-1,bd-2"),
			control: "/v0/beads/issues?id=bd-1&id=bd-2",
			param:   "id",
		},
		{
			name:    "an id carrying a control character",
			path:    "/v0/beads/issues?id=" + url.QueryEscape("bd-1\n"),
			control: "/v0/beads/issues?id=bd-1",
			param:   "id",
		},
		{
			name:    "an empty id",
			path:    "/v0/beads/issues?id=bd-1&id=",
			control: "/v0/beads/issues?id=bd-1",
			param:   "id",
		},
		{
			// The role trims every member of the set, so a padded value
			// would silently address the trimmed id — a row the caller never
			// named, while the padded one (which storage can hold) is missed.
			name:    "an id with leading whitespace",
			path:    "/v0/beads/issues?id=" + url.QueryEscape(" bd-1"),
			control: "/v0/beads/issues?id=bd-1",
			param:   "id",
		},
		{
			// Unicode whitespace too: the rule is strings.TrimSpace's, the
			// role's own trim, not an ASCII-space check.
			name:    "an id with trailing non-breaking space",
			path:    "/v0/beads/issues?id=" + url.QueryEscape("bd-1\u00a0"),
			control: "/v0/beads/issues?id=bd-1",
			param:   "id",
		},
		{
			name:    "an id longer than storage holds",
			path:    "/v0/beads/issues?id=" + long,
			control: "/v0/beads/issues?id=" + long[:types.MaxFieldLen],
			param:   "id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, rec := newReadServer(t, Config{})

			resp := ts.get(t, tc.path)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("GET %s: status = %d, want 400", tc.path, resp.StatusCode)
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) || body["param"] != tc.param || body["reason"] != string(ReasonInvalidValue) {
				t.Errorf("body = %v, want invalid_argument/invalid_value on param %s", body, tc.param)
			}
			if n := len(rec.searchFilters()); n != 0 {
				t.Errorf("%d list queries ran; a refused request must not reach storage", n)
			}
			if got := ts.get(t, tc.control); got.StatusCode != http.StatusOK {
				t.Fatalf("control GET %s: status = %d, want 200; body=%v", tc.control, got.StatusCode, decodeBody(t, got))
			}
		})
	}
}

// TestListIDsBoundMatchesTheSpec holds the document's number to the constant
// the handler enforces, so the two cannot drift: a spec that said 300 would be
// refused by the server it documents at a number no reader predicted.
func TestListIDsBoundMatchesTheSpec(t *testing.T) {
	doc := loadSpec(t)
	so, ok := specOps(t, doc)[OpListIssues]
	if !ok {
		t.Fatalf("operation %q missing from the spec", OpListIssues)
	}
	schema := mapAt(t, specParam(t, so, "id"), "schema")
	if got, ok := schema["maxItems"].(int); !ok || got != maxListIDs {
		t.Errorf("listIssues id maxItems = %v, want %d", schema["maxItems"], maxListIDs)
	}
	if !slices.Contains(Capabilities(), CapListIDs) {
		t.Errorf("Capabilities() does not advertise %s", CapListIDs)
	}
}
