//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The CLI/HTTP parity oracle for the three read operations, against real Dolt
// through a real `bd serve` subprocess.
//
// IT COMPARES FULL ITEM JSON, not id sets, and that is the whole point. An
// id-set oracle passes while `ready` ships a bare Issue against the CLI's
// IssueWithCounts — the item sets agree perfectly and every field the caller
// actually reads is missing. Field-level drift is the failure mode this epic
// exists to prevent, so it is the thing the oracle has to be able to see.
//
// The comparison is on DECODED JSON, so key order and whitespace do not enter
// into it; what is compared is every key and every value of every item.

// readsParityAllowlist is the complete set of documented differences between
// the two surfaces' bodies. It is EMPTY, deliberately: both surfaces marshal
// the same canonical Go structs (the wire schemas are x-go-type-pinned to
// them), so there is nothing left for an item to differ by. An entry here is a
// permanent, reviewed divergence — not a place to record a surprise.
//
// The three differences that DO exist are structural rather than per-field, so
// every comparison below states its terms explicitly instead of waving them
// through here:
//
//   - `bd show --json` emits an array of one; GET /v0/beads/issues/{id} emits
//     the object. The CLI envelope is byte-pinned by the protocol corpus.
//   - `bd list`'s default order is priority-first; the list endpoint's is
//     (created_at DESC, id ASC), because the cursor is a keyset position in
//     the created order. The ITEM SET and every item's JSON still match.
//   - THE DEFAULT LIMIT. The endpoint always defaults to
//     workapi.DefaultListLimit; `bd list` resolves a five-way CLI policy
//     first (an explicit --limit, --all, a configured list.limit, piped
//     stdout -> unlimited per GH#4094, agent mode -> 20), so the shared
//     request-level default is unreachable from the CLI. A client swapping
//     `bd list --json | ...` for the HTTP call therefore loses rows past 50.
//     TestListLimitPolicyIsResolvedBeforeTheRequest pins that policy branch by
//     branch; every comparison here passes an EXPLICIT limit on both sides so
//     it is comparing the operation and not that policy.
var readsParityAllowlist = map[string]string{}

// getJSONArray fetches a page endpoint and returns its decoded items.
func (sp *serveProcess) items(t *testing.T, path string) []map[string]any {
	t.Helper()
	resp, err := sp.client.Get(sp.url(path))
	if err != nil {
		t.Fatalf("GET %s: %v\nstderr:\n%s", path, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status = %d, body = %s\nstderr:\n%s", path, resp.StatusCode, raw, sp.stderr.String())
	}
	var page struct {
		Items      []map[string]any `json:"items"`
		HasMore    bool             `json:"has_more"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode %s: %v\nraw: %s", path, err, raw)
	}
	if page.Items == nil {
		t.Errorf("GET %s returned a null `items`; the document promises an empty array", path)
	}
	return page.Items
}

// object fetches a single-object endpoint.
func (sp *serveProcess) object(t *testing.T, path string) (int, map[string]any) {
	t.Helper()
	resp, err := sp.client.Get(sp.url(path))
	if err != nil {
		t.Fatalf("GET %s: %v\nstderr:\n%s", path, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var body map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode %s: %v\nraw: %s", path, err, raw)
		}
	}
	return resp.StatusCode, body
}

// cliItems runs a `bd ... --json` array-emitting command and decodes it.
func cliItems(t *testing.T, bd, dir string, args ...string) []map[string]any {
	t.Helper()
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, args...)
	if err != nil {
		t.Fatalf("bd %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
	}
	start := strings.Index(stdout, "[")
	if start < 0 {
		t.Fatalf("no JSON array in `bd %s` output:\n%s", strings.Join(args, " "), stdout)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout[start:]), &items); err != nil {
		t.Fatalf("decode `bd %s` output: %v\nraw: %s", strings.Join(args, " "), err, stdout[start:])
	}
	return items
}

func itemIDs(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		id, _ := item["id"].(string)
		out = append(out, id)
	}
	return out
}

func byID(t *testing.T, label string, items []map[string]any) map[string]map[string]any {
	t.Helper()
	out := make(map[string]map[string]any, len(items))
	for _, item := range items {
		id, _ := item["id"].(string)
		if id == "" {
			t.Fatalf("%s: item with no id: %v", label, item)
		}
		out[id] = item
	}
	return out
}

// assertItemsMatch is the oracle proper: same ids, and for each id, the same
// JSON object field by field.
func assertItemsMatch(t *testing.T, what string, cli, http []map[string]any) {
	t.Helper()

	cliByID := byID(t, "cli", cli)
	httpByID := byID(t, "http", http)

	cliIDs := sortedIDs(cliByID)
	httpIDs := sortedIDs(httpByID)
	if !reflect.DeepEqual(cliIDs, httpIDs) {
		t.Fatalf("%s: item sets differ\n cli: %v\nhttp: %v", what, cliIDs, httpIDs)
	}
	if len(cliIDs) == 0 {
		t.Fatalf("%s: both surfaces returned nothing, so this oracle proved nothing", what)
	}

	for _, id := range cliIDs {
		a, b := cliByID[id], httpByID[id]
		for _, key := range unionKeys(a, b) {
			if why, allowed := readsParityAllowlist[key]; allowed {
				t.Logf("%s: %s: field %q excused: %s", what, id, key, why)
				continue
			}
			av, ok := a[key]
			if !ok {
				t.Errorf("%s: %s: field %q present over HTTP (%v) and absent from `bd --json`", what, id, key, b[key])
				continue
			}
			bv, ok := b[key]
			if !ok {
				t.Errorf("%s: %s: field %q present in `bd --json` (%v) and absent over HTTP", what, id, key, av)
				continue
			}
			if !reflect.DeepEqual(av, bv) {
				t.Errorf("%s: %s: field %q differs\n cli: %#v\nhttp: %#v", what, id, key, av, bv)
			}
		}
	}
}

func sortedIDs(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func unionKeys(a, b map[string]any) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestProxiedServerServeReadParity is the property this epic claims, made
// checkable: over the same fixture data, `bd ready --json`, `bd list --json`
// and `bd show --json` and their three endpoints answer with the same items and
// the same JSON for each item — because both front doors go through
// issueops.Reader and neither builds a filter of its own.
func TestProxiedServerServeReadParity(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvrp")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// A fixture with enough shape that a filter mistake shows: several
	// priorities, two types, labels, an assignee, and a real blocker so the
	// ready set is a strict subset of the list set.
	top := bdProxiedCreate(t, bd, p.dir, "parity top", "-p", "0", "-t", "bug", "-l", "alpha")
	mid := bdProxiedCreate(t, bd, p.dir, "parity mid", "-p", "2", "-t", "task", "-l", "alpha,beta")
	low := bdProxiedCreate(t, bd, p.dir, "parity low", "-p", "3", "-t", "task")
	blocked := bdProxiedCreate(t, bd, p.dir, "parity blocked", "-p", "1", "-t", "task")
	if out, err := bdProxiedRun(t, bd, p.dir, "dep", "add", blocked.ID, top.ID); err != nil {
		t.Fatalf("bd dep add: %v\n%s", err, out)
	}

	t.Run("ready", func(t *testing.T) {
		// --limit 0 on both surfaces: unlimited means the same thing on each,
		// and an unbounded read is the one that cannot hide a row behind a
		// default that differs.
		cli := cliItems(t, bd, p.dir, "ready", "--json", "--limit", "0")
		got := sp.items(t, "/v0/beads/ready?limit=0")
		assertItemsMatch(t, "ready", cli, got)

		// ORDER, not just membership. bd ready's default sort is priority and
		// the endpoint's documented default is the same, so the sequences must
		// agree — this is the assertion that catches a handler forwarding an
		// absent `sort` as "", which the storage layer reads as hybrid and
		// which reorders (and, once a limit truncates, re-SELECTS) the page.
		if cliIDs, gotIDs := itemIDs(cli), itemIDs(got); !reflect.DeepEqual(cliIDs, gotIDs) {
			t.Errorf("ready order differs; the endpoint's default sort is not `bd ready`'s\n cli: %v\nhttp: %v", cliIDs, gotIDs)
		}
		// And the same again with the policy stated explicitly on both sides:
		// if these two HTTP answers ever differ, the default is not priority
		// whatever the document says.
		explicit := sp.items(t, "/v0/beads/ready?limit=0&sort=priority")
		if !reflect.DeepEqual(itemIDs(got), itemIDs(explicit)) {
			t.Errorf("ready with no `sort` differs from `sort=priority`\n default: %v\npriority: %v",
				itemIDs(got), itemIDs(explicit))
		}
		if ids := itemIDs(got); contains(strings.Join(ids, ","), blocked.ID) {
			t.Errorf("ready returned the blocked issue %s: %v", blocked.ID, ids)
		}
	})

	t.Run("list", func(t *testing.T) {
		cli := cliItems(t, bd, p.dir, "list", "--json", "--limit", "0")
		got := sp.items(t, "/v0/beads/issues?limit=0")
		assertItemsMatch(t, "list", cli, got)

		for _, want := range []string{top.ID, mid.ID, low.ID, blocked.ID} {
			if !contains(strings.Join(itemIDs(got), ","), want) {
				t.Errorf("list is missing %s: %v", want, itemIDs(got))
			}
		}
	})

	t.Run("list with a filter both surfaces have to build the same way", func(t *testing.T) {
		cli := cliItems(t, bd, p.dir, "list", "--json", "--limit", "0", "--label", "alpha")
		got := sp.items(t, "/v0/beads/issues?limit=0&label=alpha")
		assertItemsMatch(t, "list --label alpha", cli, got)
	})

	t.Run("a limit truncates on both surfaces, and to the same rows", func(t *testing.T) {
		// The whole oracle above runs at limit 0, so until this subtest no
		// comparison exercised truncation at all — and truncation is where the
		// read epilogue (fetch, sort, trim, has_more) can differ without
		// changing the unlimited answer.
		//
		// `--sort created` is what makes the two surfaces comparable under a
		// limit: the endpoint's order is welded to the cursor contract and is
		// always (created_at DESC, id ASC), so asking the CLI for the same
		// order is the only way a truncated page can be compared row for row
		// rather than by count.
		cli := cliItems(t, bd, p.dir, "list", "--json", "--limit", "2", "--sort", "created")
		got := sp.items(t, "/v0/beads/issues?limit=2")
		if len(cli) != 2 {
			t.Fatalf("`bd list --limit 2` returned %d items, want 2", len(cli))
		}
		if len(got) != 2 {
			t.Fatalf("GET issues?limit=2 returned %d items, want 2", len(got))
		}
		assertItemsMatch(t, "list --limit 2", cli, got)
	})

	t.Run("a limit the database cannot push down still truncates", func(t *testing.T) {
		// `--sort id` needs natural-numeric comparison (bd-9 < bd-10) that SQL
		// cannot express, so workapi.SQLLimit zeroes the query's row limit and
		// the whole result set comes back. The client-side trim is then the
		// ONLY thing bounding the page — and the proxied route had no trim, so
		// this command answered with every row while the direct route answered
		// with two.
		//
		// There is no HTTP half to compare against: the list endpoint takes no
		// `sort` parameter. What is being pinned is that the CLI's two routes
		// agree, which is the same property by a different pair.
		cli := cliItems(t, bd, p.dir, "list", "--json", "--limit", "2", "--sort", "id")
		if len(cli) != 2 {
			t.Errorf("`bd list --sort id --limit 2` returned %d items (%v), want 2 — a sort SQL cannot express still has to respect the limit",
				len(cli), itemIDs(cli))
		}
	})

	t.Run("show", func(t *testing.T) {
		for _, id := range []string{top.ID, mid.ID, blocked.ID} {
			// `bd show --json` emits an array of one; the endpoint emits the
			// object. That envelope difference is the CLI's, byte-pinned by the
			// protocol corpus, and is the only structural divergence here.
			cli := cliItems(t, bd, p.dir, "show", id, "--json")
			if len(cli) != 1 {
				t.Fatalf("bd show %s --json returned %d items, want the array of one", id, len(cli))
			}
			status, got := sp.object(t, "/v0/beads/issues/"+id)
			if status != http.StatusOK {
				t.Fatalf("GET issue %s: status = %d, body = %v", id, status, got)
			}
			assertItemsMatch(t, "show "+id, cli, []map[string]any{got})
		}
	})

	t.Run("a page limit truncates identically and pages back to the same set", func(t *testing.T) {
		all := sp.items(t, "/v0/beads/issues?limit=0")
		if len(all) < 3 {
			t.Skipf("fixture too small to page: %d rows", len(all))
		}

		var walked []map[string]any
		path := "/v0/beads/issues?limit=2"
		for range 10 {
			resp, err := sp.client.Get(sp.url(path))
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			var page struct {
				Items      []map[string]any `json:"items"`
				HasMore    bool             `json:"has_more"`
				NextCursor *string          `json:"next_cursor"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				t.Fatalf("decode page: %v\nraw: %s", err, raw)
			}
			walked = append(walked, page.Items...)
			if !page.HasMore {
				if page.NextCursor != nil {
					t.Error("has_more is false but next_cursor is present; the document makes those a biconditional")
				}
				break
			}
			if page.NextCursor == nil {
				t.Fatal("has_more is true but next_cursor is absent; paging cannot continue")
			}
			path = fmt.Sprintf("/v0/beads/issues?limit=2&cursor=%s", *page.NextCursor)
		}

		if got, want := itemIDs(walked), itemIDs(all); !reflect.DeepEqual(got, want) {
			t.Errorf("a cursored walk did not reproduce the unlimited page\nwalked: %v\n   all: %v", got, want)
		}
	})

	t.Run("a foreign cursor is refused, not silently ignored", func(t *testing.T) {
		status, body := sp.object(t, "/v0/beads/issues?cursor=not-a-cursor")
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %v", status, body)
		}
		if body["code"] != "invalid_cursor" {
			t.Errorf("code = %v, want invalid_cursor", body["code"])
		}
	})

	t.Run("an unknown filter parameter is refused by name", func(t *testing.T) {
		// Silently ignoring it would WIDEN the result set, which is the one
		// failure a client cannot detect.
		status, body := sp.object(t, "/v0/beads/ready?nosuchparam=1")
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %v", status, body)
		}
		if body["reason"] != "unknown_parameter" || body["param"] != "nosuchparam" {
			t.Errorf("body = %v, want reason=unknown_parameter param=nosuchparam", body)
		}
	})

	t.Run("an unknown id is 404, on the same shape as every other failure", func(t *testing.T) {
		status, body := sp.object(t, "/v0/beads/issues/bd-no-such-issue")
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %v", status, body)
		}
		if body["code"] != "not_found" {
			t.Errorf("code = %v, want not_found", body["code"])
		}
	})

	sp.shutdown(t)
}
