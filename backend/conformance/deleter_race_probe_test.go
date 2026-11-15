package conformance

import (
	"context"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestDeleterSingleWinnerCasesCatchABrokenDeleter is the RED evidence for
// RunDeleterExpectedVersionSingleWinnerUnderConcurrency and
// RunDeleterExpectedVersionCascadeSingleWinnerUnderConcurrency.
//
// Both cases run, in a child process (runProbeChild), against memDeleter: an
// in-memory Deleter whose mode is one defect at a time. Against "fenced" (a
// correct compare-and-delete) both must PASS, history clause included, so a
// case that failed everything cannot pass as red evidence. Against each
// broken mode both must FAIL, naming the clause that caught it:
//
//   - "unfenced" is the defect the cases exist for (mc-zndi7.73): every racer
//     reads the row before any of them erases it, and every one reports
//     Deleted with a nil error and records its own history entry.
//   - "silent" refuses no loser: a racer that finds the row gone answers
//     Deleted = 0 with a nil error. Two nil errors are two claimed successes.
//   - "double-entry" has one winner that records two history entries.
//
// The real backends' own falsification (dropping withDeleteFence from either
// body) needs a mutated production tree and a Dolt server; it is recorded in
// the PR that added these cases, not here.
func TestDeleterSingleWinnerCasesCatchABrokenDeleter(t *testing.T) {
	if mode, ok := strings.CutPrefix(os.Getenv(roleBundleProbeEnv), "deleter-race-"); ok {
		fixture := newMemDeleterFixture(mode)
		t.Run("Plain", func(t *testing.T) {
			RunDeleterExpectedVersionSingleWinnerUnderConcurrency(t, context.Background(), fixture)
		})
		t.Run("Cascade", func(t *testing.T) {
			RunDeleterExpectedVersionCascadeSingleWinnerUnderConcurrency(t, context.Background(), fixture)
		})
		return
	}

	for _, test := range []struct {
		mode string
		// want is a substring every failing case's output must carry.
		want string
	}{
		{"fenced", ""},
		{"unfenced", fmt.Sprintf("got %d winners, want exactly 1", deleterRacers)},
		{"silent", fmt.Sprintf("got %d winners, want exactly 1", deleterRacers)},
		{"double-entry", "want exactly one more entry"},
	} {
		// The child re-runs THIS test by name, so it is started from here and
		// not from inside the subtest, whose name the child would filter on.
		out, err := runProbeChild(t, "deleter-race-"+test.mode)
		parent := t.Name()
		t.Run(test.mode, func(t *testing.T) {
			for _, c := range []string{"Plain", "Cascade"} {
				name := parent + "/" + c
				if test.want == "" {
					if !strings.Contains(out, "--- PASS: "+name+" ") || !strings.Contains(out, "--- PASS: "+name+"/HistoryGainsExactlyOneEntry ") {
						t.Errorf("%s did not pass, history clause included, against a correct deleter:\n%s", c, out)
					}
					continue
				}
				if !strings.Contains(out, "--- FAIL: "+name+" ") {
					t.Errorf("%s did not FAIL against the %q deleter:\n%s", c, test.mode, out)
				}
			}
			switch {
			case test.want == "" && err != nil:
				t.Errorf("the cases failed against a correct deleter: %v\n%s", err, out)
			case test.want != "" && err == nil:
				t.Errorf("the cases passed against the %q deleter", test.mode)
			case test.want != "" && !strings.Contains(out, test.want):
				t.Errorf("the %q deleter failed for another reason than %q:\n%s", test.mode, test.want, out)
			}
		})
	}
}

// memDeleter is the probe's Deleter: one durable plane, parent-child edges,
// and a history counter, all under one lock except where its mode says not.
type memDeleter struct {
	mode string

	mu      sync.Mutex
	rows    map[string]int64  // id -> row_lock
	parent  map[string]string // child -> parent
	history int
	minted  int64
	// read gates "unfenced": per raced id, closed once every racer has read
	// its snapshot, so all of them read before any of them erases.
	read  map[string]chan struct{}
	reads map[string]int
}

func newMemDeleterFixture(mode string) DeleterFixture {
	d := &memDeleter{
		mode: mode, rows: map[string]int64{}, parent: map[string]string{},
		read: map[string]chan struct{}{}, reads: map[string]int{},
	}
	create := func(_ context.Context, issue *types.Issue, _ string) error {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.minted++
		d.rows[issue.ID] = 7919 * d.minted
		return nil
	}
	return DeleterFixture{
		IssuePrefix: "mem",
		Deleter:     d,
		CreateIssue: create,
		CreateWisp:  create,
		AddDependency: func(_ context.Context, dep *types.Dependency, _ string) error {
			d.mu.Lock()
			defer d.mu.Unlock()
			if dep.Type == types.DepParentChild {
				d.parent[dep.IssueID] = dep.DependsOnID
			}
			return nil
		},
		QueryScalar:   d.queryScalar,
		CountHistory:  func(context.Context) (int, error) { d.mu.Lock(); defer d.mu.Unlock(); return d.history, nil },
		CommitPending: func(context.Context) error { return nil },
	}
}

// queryScalar answers the two raw reads the race cases make, and refuses any
// other, so a case that grew a third read fails here loudly.
func (d *memDeleter) queryScalar(_ context.Context, query string, args []any, dest ...any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case query == "SELECT COALESCE(row_lock, 0) FROM issues WHERE id = ?":
		*dest[0].(*int64) = d.rows[args[0].(string)]
	case strings.HasPrefix(query, "SELECT COUNT(*) FROM issues WHERE id IN ("):
		n := 0
		for _, arg := range args {
			if _, ok := d.rows[arg.(string)]; ok {
				n++
			}
		}
		*dest[0].(*int) = n
	default:
		return fmt.Errorf("memDeleter answers no query %q", query)
	}
	return nil
}

func (d *memDeleter) Delete(_ context.Context, req publicops.DeleteRequest) (publicops.DeleteResult, error) {
	if d.mode == "unfenced" {
		id := strings.TrimSpace(req.IDs[0])
		d.mu.Lock()
		rows, parent := maps.Clone(d.rows), maps.Clone(d.parent)
		if d.read[id] == nil {
			d.read[id] = make(chan struct{})
		}
		allRead := d.read[id]
		if d.reads[id]++; d.reads[id] == deleterRacers {
			close(allRead)
		}
		d.mu.Unlock()
		// The window a fence closes, held open until every racer has read the
		// same snapshot. The timeout only keeps a case that stopped racing
		// from hanging here; it then sees one winner and the probe says so.
		select {
		case <-allRead:
		case <-time.After(500 * time.Millisecond):
		}
		closure, err := resolveMemDelete(rows, parent, req)
		if err != nil {
			return publicops.DeleteResult{}, err
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, id := range closure {
			delete(d.rows, id)
		}
		d.history++
		return publicops.DeleteResult{Deleted: len(closure), DeletedIDs: closure}, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	closure, err := resolveMemDelete(d.rows, d.parent, req)
	if err != nil {
		if d.mode == "silent" {
			return publicops.DeleteResult{}, nil
		}
		return publicops.DeleteResult{}, err
	}
	for _, id := range closure {
		delete(d.rows, id)
	}
	d.history++
	if d.mode == "double-entry" {
		d.history++
	}
	return publicops.DeleteResult{Deleted: len(closure), DeletedIDs: closure}, nil
}

// resolveMemDelete runs the existence probe and the version precondition
// against rows, in that order, and returns the ids the request erases: the
// named row, and under Cascade every live row whose parent chain reaches it.
func resolveMemDelete(rows map[string]int64, parent map[string]string, req publicops.DeleteRequest) ([]string, error) {
	id := strings.TrimSpace(req.IDs[0])
	current, ok := rows[id]
	if !ok {
		return nil, &publicops.NotFoundError{IDs: []string{id}}
	}
	if req.ExpectedVersion != nil && *req.ExpectedVersion != current {
		return nil, &publicops.VersionMismatchError{Expected: *req.ExpectedVersion, Current: current}
	}
	closure := []string{id}
	if req.Cascade {
		for child, up := range parent {
			for p := up; p != ""; p = parent[p] {
				if p == id {
					if _, live := rows[child]; live {
						closure = append(closure, child)
					}
					break
				}
			}
		}
	}
	sort.Strings(closure)
	return closure, nil
}
