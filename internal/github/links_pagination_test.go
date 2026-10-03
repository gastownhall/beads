package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeRelationshipServer serves the sub-issue and blocked_by endpoints the way
// GitHub does: 30 items per page unless per_page says otherwise (capped at
// 100), with a Link rel="next" header while more pages remain. Created links
// are appended to the remote state, so a second push sees them.
type fakeRelationshipServer struct {
	mu      sync.Mutex
	targets map[string][]int // "<from>/<linkType>" -> target issue numbers
	posts   int
	perPage []string
}

func (f *fakeRelationshipServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// /repos/o/r/issues/{n}[/sub_issues | /dependencies/blocked_by]
		if len(segments) < 5 || segments[3] != "issues" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		number, err := strconv.Atoi(segments[4])
		if err != nil {
			t.Errorf("bad issue number in %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		rest := strings.Join(segments[5:], "/")
		var linkType string
		switch rest {
		case "":
			// FetchIssueByNumber: internal ID is number+1000.
			_ = json.NewEncoder(w).Encode(Issue{ID: number + 1000, Number: number})
			return
		case "sub_issues":
			linkType = githubLinkSubIssue
		case "dependencies/blocked_by":
			linkType = githubLinkBlockedBy
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		key := fmt.Sprintf("%d/%s", number, linkType)

		switch r.Method {
		case http.MethodGet:
			f.perPage = append(f.perPage, r.URL.Query().Get("per_page"))
			perPage := 30
			if v, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil && v > 0 {
				perPage = min(v, 100)
			}
			page := 1
			if v, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && v > 0 {
				page = v
			}
			all := f.targets[key]
			start := min((page-1)*perPage, len(all))
			end := min(start+perPage, len(all))
			if end < len(all) {
				w.Header().Set("Link", fmt.Sprintf("<http://%s%s?per_page=%d&page=%d>; rel=\"next\"", r.Host, r.URL.Path, perPage, page+1))
			}
			items := make([]Issue, 0, end-start)
			for _, n := range all[start:end] {
				items = append(items, Issue{ID: n + 1000, Number: n})
			}
			_ = json.NewEncoder(w).Encode(items)
		case http.MethodPost:
			f.posts++
			var body map[string]int
			_ = json.NewDecoder(r.Body).Decode(&body)
			id := body["sub_issue_id"] + body["issue_id"]
			f.targets[key] = append(f.targets[key], id-1000)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Issue{ID: id, Number: id - 1000})
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}
}

// TestPushLinksIdempotentAcrossPages pins finding 1 of the #5971 review: the
// existing-relationship read must see every page, or links beyond the first
// page look missing and are re-POSTed on every sync.
func TestPushLinksIdempotentAcrossPages(t *testing.T) {
	const existing = 150 // more than one page even at per_page=100
	fake := &fakeRelationshipServer{targets: map[string][]int{}}
	var desired []DependencyLink
	for n := 1; n <= existing+3; n++ {
		child, blocker := 1000+n, 2000+n
		if n <= existing {
			fake.targets["1/"+githubLinkSubIssue] = append(fake.targets["1/"+githubLinkSubIssue], child)
			fake.targets["2/"+githubLinkBlockedBy] = append(fake.targets["2/"+githubLinkBlockedBy], blocker)
		}
		desired = append(desired,
			DependencyLink{FromNumber: 1, ToNumber: child, LinkType: githubLinkSubIssue},
			DependencyLink{FromNumber: 2, ToNumber: blocker, LinkType: githubLinkBlockedBy},
		)
	}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()
	gt := newTestTracker(server.URL)

	first := gt.PushLinks(context.Background(), desired, PushLinkOptions{})
	if len(first.Errors) != 0 {
		t.Fatalf("first PushLinks errors = %v", first.Errors)
	}
	if first.Created != 6 || fake.posts != 6 {
		t.Fatalf("first push: Created = %d, POSTs = %d; want only the 6 missing links", first.Created, fake.posts)
	}

	fake.posts = 0
	second := gt.PushLinks(context.Background(), desired, PushLinkOptions{})
	if len(second.Errors) != 0 {
		t.Fatalf("second PushLinks errors = %v", second.Errors)
	}
	if second.Created != 0 || fake.posts != 0 {
		t.Fatalf("second push: Created = %d, POSTs = %d; want no duplicate POSTs", second.Created, fake.posts)
	}

	for _, pp := range fake.perPage {
		if pp != strconv.Itoa(MaxPerPage) {
			t.Fatalf("list request per_page = %q, want %d", pp, MaxPerPage)
		}
	}
}
