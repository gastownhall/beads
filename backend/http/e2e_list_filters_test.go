//go:build cgo

package bdhttp_test

import (
	"slices"
	"strings"
	"testing"
)

// listScopeShapes are the tokened `bd list` shapes, with the token each needs
// and the titles each must answer on the scope seed. Five are corpus shapes
// (backend/http/corpus/testdata/corpus.yaml) and one pins the title fold:
//   - gc-list-on-boot: gc's default on_boot hook, run on every agent boot;
//   - gc-list-on-boot-workflow: the same recovery, workflow lane;
//   - mc-list-title: the PR-intake formula's `--title "$SUBJECT"`;
//   - gc-polecat-title-contains: the polecat pre-flight's open-bead lookup,
//     `--title-contains "$SYMPTOM_KEY"`, folded onto `title`;
//   - title-and-title-contains-equal (not a corpus shape): one value under
//     both title flags is one `title`;
//   - mc-list-id: the pr-review skill's `--id <ids>`.
var listScopeShapes = []struct {
	corpusID string
	args     []string
	token    string
	want     []string
}{
	{"gc-list-on-boot",
		[]string{"list", "--metadata-field", "gc.routed_to=pool-a", "--status=in_progress", "--no-assignee", "--json"},
		"issues.list.noAssignee", []string{"orphan-ip"}},
	{"gc-list-on-boot-workflow",
		[]string{"list", "--metadata-field", "gc.run_target=pool-a", "--metadata-field", "gc.kind=workflow", "--status=in_progress", "--no-assignee", "--json"},
		"issues.list.noAssignee", []string{"orphan-wf"}},
	// Case-insensitive substring, every status.
	{"mc-list-title",
		[]string{"list", "--all", "--include-infra", "--json", "--limit", "10", "--title", "CHILD-A"},
		"issues.list.title", []string{"child-a"}},
	// --title-contains folds onto the same `title` parameter: case-insensitive
	// substring, and the status set leaves the closed child-c out.
	{"gc-polecat-title-contains",
		[]string{"list", "--title-contains", "CHILD", "--status", "open,in_progress,blocked", "--json"},
		"issues.list.title", []string{"child-a", "child-b", "child-x"}},
	// The same value under both flags is one predicate.
	{"title-and-title-contains-equal",
		[]string{"list", "--all", "--title", "child-a", "--title-contains", "child-a", "--json"},
		"issues.list.title", []string{"child-a"}},
	// A closed id in the set is listed under --all; ids outside it are not.
	{"mc-list-id",
		[]string{"list", "--all", "--id", "{child-c},{loner}", "--json", "--limit", "0"},
		"issues.list.ids", []string{"child-c", "loner"}},
}

// seedListScope adds the on_boot recovery rows to the shared seed: one
// unassigned in-progress row per lane, and an ASSIGNED in-progress row on the
// routed lane, which --no-assignee must leave out.
func (f *remoteRolesFixture) seedListScope() {
	f.t.Helper()
	ip := f.create("orphan-ip", "-t", "task")
	f.mustRun(nil, "update", ip, "-s", "in_progress", "--set-metadata", "gc.routed_to=pool-a")
	wf := f.create("orphan-wf", "-t", "task")
	f.mustRun(nil, "update", wf, "-s", "in_progress", "--set-metadata", "gc.run_target=pool-a", "--set-metadata", "gc.kind=workflow")
	owned := f.create("owned-ip", "-t", "task", "-a", "worker")
	f.mustRun(nil, "update", owned, "-s", "in_progress", "--set-metadata", "gc.routed_to=pool-a", "--set-metadata", "gc.run_target=pool-a", "--set-metadata", "gc.kind=workflow")
}

// TestE2E_ListScopeFiltersOverHTTP is S15b. `bd list --no-assignee`, `--title`
// and `--id` refused over http (the list encoder had no parameter for them);
// they now ride listIssues' `no_assignee`, `title` and `id`, each behind its
// own behavior token, and the server applies them through the same Reader
// role a local `bd list` reads through. Each corpus shape must answer the
// rows an embedded workspace answers, and exactly the rows it names.
//
// Against a server that lacks a token (the MC production server's handshake
// shape, which predates all three) each shape refuses BEFORE dialing
// listIssues, naming the token, rather than answering a wider listing.
func TestE2E_ListScopeFiltersOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t)
	remote := newHTTPFixture(t, bin, addr)
	local := newEmbeddedFixture(t, bin)
	for _, f := range []*remoteRolesFixture{remote, local} {
		f.seed()
		f.seedListScope()
	}

	for _, shape := range listScopeShapes {
		t.Run(shape.corpusID, func(t *testing.T) {
			r := remote.run(nil, remote.expand(shape.args)...)
			if r.code != 0 {
				t.Fatalf("http: bd %s failed (exit %d): stderr=%s", strings.Join(shape.args, " "), r.code, r.stderr)
			}
			l := local.mustRun(nil, local.expand(shape.args)...)
			got, want := remote.titlesOf(r.stdout), local.titlesOf(l.stdout)
			if !slices.Equal(got, want) {
				t.Errorf("http rows %v, embedded rows %v", got, want)
			}
			if !slices.Equal(got, shape.want) {
				t.Errorf("http rows %v, want exactly %v", got, shape.want)
			}
		})
	}

	t.Run("MC server mask refuses before dialing", func(t *testing.T) {
		proxyAddr, counter := startMaskingProxy(t, addr, mcContextMask)
		f := newHTTPFixture(t, bin, proxyAddr)
		f.ids = remote.ids // same workspace behind the proxy
		for _, shape := range listScopeShapes {
			counter.reset()
			r := f.run(nil, f.expand(shape.args)...)
			if r.code == 0 {
				t.Errorf("%s: bd %s exited 0 against a server lacking %s: %s", shape.corpusID, strings.Join(shape.args, " "), shape.token, r.stdout)
				continue
			}
			if !strings.Contains(r.stderr, shape.token) {
				t.Errorf("%s: the refusal does not name %s: stderr=%s", shape.corpusID, shape.token, r.stderr)
			}
			for _, p := range counter.snapshot() {
				if p == "GET /v0/beads/issues" {
					t.Errorf("%s: dialed listIssues against a server lacking %s: %v", shape.corpusID, shape.token, counter.snapshot())
					break
				}
			}
		}
	})
}

// listFilterShapes are the `bd list` shapes that ride the listing filters
// behind issues.list.filters, each with the titles it must answer on the seed
// plus seedListFilters. Five are corpus shapes and one pins the title pair the
// fold leaves:
//   - wf-session-cleanup: the session-cleanup sweep's closed-and-stale scan;
//   - gc-polecat-closed-after: the polecat formula's "has the fix landed"
//     check (title substring, closed after a cutoff);
//   - list-filter-priority-range, list-filter-exclude-type-no-parent and
//     list-filter-updated-after: one representative per remaining family;
//   - title-and-title-contains-differ (not a corpus shape): a `--title` and a
//     `--title-contains` that differ ride `title` and `title_contains`
//     together, and the server applies both.
var listFilterShapes = []struct {
	corpusID string
	args     []string
	want     []string
}{
	{"wf-session-cleanup",
		[]string{"list", "--json", "--all", "--include-infra", "--include-gates", "--status", "closed", "--label", "lane:a",
			"--closed-before", "2100-01-01T00:00:00Z", "--updated-before", "2100-01-01T00:00:00Z", "--limit", "0"},
		[]string{"lane task closed"}},
	{"gc-polecat-closed-after",
		[]string{"list", "--title-contains", "task", "--status", "closed", "--closed-after", "2000-01-01", "--json"},
		[]string{"lane task closed", "other lane task closed"}},
	{"list-filter-priority-range",
		[]string{"list", "--json", "--priority-min", "1", "--priority-max", "3", "--limit", "0"},
		[]string{"child-a", "child-b", "loner", "open task p2", "parent", "tmpl"}},
	{"list-filter-exclude-type-no-parent",
		[]string{"list", "--json", "--exclude-type", "epic", "--no-parent", "--limit", "0"},
		[]string{"loner", "open task p2", "p4 row", "tmpl"}},
	{"list-filter-updated-after",
		[]string{"list", "--json", "--updated-after", "2000-01-01", "--limit", "0"},
		[]string{"child-a", "child-b", "child-x", "loner", "open task p2", "p4 row", "parent", "tmpl"}},
	// Either substring alone answers more: "child" every child, "a" the
	// lane tasks, parent and open task p2 too.
	{"title-and-title-contains-differ",
		[]string{"list", "--all", "--title", "child", "--title-contains", "a", "--json"},
		[]string{"child-a"}},
}

// seedListFilters adds the rows the filter shapes split: two closed tasks on
// different lanes (the label keeps one out of the session-cleanup scan, the
// title substring keeps child-c out of the polecat check), a P4 row the
// priority range leaves out, and an open P2 task beside it.
func (f *remoteRolesFixture) seedListFilters() {
	f.t.Helper()
	a := f.create("lane task closed", "-t", "task", "-l", "lane:a")
	b := f.create("other lane task closed", "-t", "task", "-l", "lane:b")
	f.mustRun(nil, "close", a, "--reason", "done")
	f.mustRun(nil, "close", b, "--reason", "done")
	f.create("p4 row", "-t", "task", "-p", "4")
	f.create("open task p2", "-t", "task", "-p", "2")
}

// withoutListFilters rewrites the handshake into a server that is current in
// every respect but one: it does not advertise issues.list.filters.
func withoutListFilters(body map[string]any) {
	caps, _ := body["capabilities"].([]any)
	kept := make([]any, 0, len(caps))
	for _, c := range caps {
		if c != "issues.list.filters" {
			kept = append(kept, c)
		}
	}
	body["capabilities"] = kept
}

// TestE2E_ListFilterParityOverHTTP: `bd list`'s time bounds and every other
// filter the listing refused over http now ride listIssues behind the one
// behavior token issues.list.filters, applied by the same Reader role a local
// `bd list` reads through. Each shape must answer the rows an embedded
// workspace answers, and exactly the rows it names.
//
// Against a server that does not advertise the token each shape refuses
// BEFORE dialing listIssues, naming the token, rather than answering a wider
// listing.
func TestE2E_ListFilterParityOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t)
	remote := newHTTPFixture(t, bin, addr)
	local := newEmbeddedFixture(t, bin)
	for _, f := range []*remoteRolesFixture{remote, local} {
		f.seed()
		f.seedListFilters()
	}

	for _, shape := range listFilterShapes {
		t.Run(shape.corpusID, func(t *testing.T) {
			r := remote.run(nil, shape.args...)
			if r.code != 0 {
				t.Fatalf("http: bd %s failed (exit %d): stderr=%s", strings.Join(shape.args, " "), r.code, r.stderr)
			}
			l := local.mustRun(nil, shape.args...)
			got, want := remote.titlesOf(r.stdout), local.titlesOf(l.stdout)
			if !slices.Equal(got, want) {
				t.Errorf("http rows %v, embedded rows %v", got, want)
			}
			if !slices.Equal(got, shape.want) {
				t.Errorf("http rows %v, want exactly %v", got, shape.want)
			}
		})
	}

	t.Run("a server without issues.list.filters refuses before dialing", func(t *testing.T) {
		proxyAddr, counter := startMaskingProxy(t, addr, withoutListFilters)
		f := newHTTPFixture(t, bin, proxyAddr)
		f.ids = remote.ids // same workspace behind the proxy
		for _, shape := range listFilterShapes {
			counter.reset()
			r := f.run(nil, shape.args...)
			if r.code == 0 {
				t.Errorf("%s: bd %s exited 0 against a server lacking issues.list.filters: %s", shape.corpusID, strings.Join(shape.args, " "), r.stdout)
				continue
			}
			if !strings.Contains(r.stderr+r.stdout, "issues.list.filters") {
				t.Errorf("%s: the refusal does not name issues.list.filters: stdout=%s stderr=%s", shape.corpusID, r.stdout, r.stderr)
			}
			for _, p := range counter.snapshot() {
				if p == "GET /v0/beads/issues" {
					t.Errorf("%s: dialed listIssues against a server lacking issues.list.filters: %v", shape.corpusID, counter.snapshot())
					break
				}
			}
		}
	})
}
