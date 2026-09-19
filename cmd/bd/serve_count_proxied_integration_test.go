//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// End-to-end for the issue count, against real Dolt through a real `bd serve`
// subprocess.
//
// The pure tests in internal/httpapi cover the wire edge on a fake role — the
// parameter projection, the method selection, the response shape. What only
// this level can prove is what the numbers MEAN, and on this operation that is
// most of the contract:
//
//   - a bare count includes CLOSED rows where the listing beside it hides them,
//     which is the difference that makes Counter a role rather than a counted
//     Reader;
//   - `include_infra` really merges the wisps tier, and its absence really does
//     not, which is the plane question a fake cannot be asked;
//   - label buckets really OVERLAP, so the role's `total` really is not the sum
//     of `groups` — the one number a client is most likely to derive wrongly.
//
// Every case narrows to a label this test seeds, so the numbers are exact
// whatever else the workspace holds.

// countSliceLabel scopes every case below to rows this test created.
const countSliceLabel = "srvcnt-slice"

// countIssues asks the server for a count and returns the status and the
// decoded body. `groups` is a POINTER, because its ABSENCE is the answer to "you
// did not ask for buckets" and an empty object is the answer to "nothing
// matched".
func (sp *serveProcess) countIssues(t *testing.T, query string) (int, int64, *map[string]int) {
	t.Helper()
	resp, err := sp.client.Get(sp.url("/v0/beads/issues:count" + query))
	if err != nil {
		t.Fatalf("GET issues:count%s: %v\nstderr:\n%s", query, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read count body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, 0, nil
	}
	var body struct {
		Total  int64           `json:"total"`
		Groups *map[string]int `json:"groups"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode count body %q: %v", raw, err)
	}
	return resp.StatusCode, body.Total, body.Groups
}

// bdProxiedCountTotal runs `bd count --json` and returns the scalar it printed.
// It is the PARITY ORACLE: the CLI and this server reach one role through one
// accessor, so a number that differs between them is a construction difference
// and not a rounding one.
func bdProxiedCountTotal(t *testing.T, bd, dir string, args ...string) int64 {
	t.Helper()
	out, err := bdProxiedRun(t, bd, dir, append([]string{"count", "--json"}, args...)...)
	if err != nil {
		t.Fatalf("bd count --json %v failed: %v\n%s", args, err, out)
	}
	var body struct {
		Count int64 `json:"count"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("decode bd count --json %q: %v", out, err)
	}
	return body.Count
}

func TestProxiedServerServeCount(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvcnt")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	scoped := "?label=" + countSliceLabel

	// THE DIFFERENCE FROM A LISTING, which is the whole reason this is its own
	// role: a default count hides nothing. Three durable rows, one of them
	// closed, and the bare count answers three.
	//
	// The closed row is the load-bearing one. `GET /v0/beads/issues` would hide
	// it, so a handler that reused the listing's defaults would answer two here
	// and look perfectly reasonable doing it.
	t.Run("a bare count includes the closed rows a listing hides", func(t *testing.T) {
		open1 := bdProxiedCreate(t, bd, p.dir, "count open one", "-p", "2", "-l", countSliceLabel)
		bdProxiedCreate(t, bd, p.dir, "count open two", "-p", "2", "-l", countSliceLabel)
		closed := bdProxiedCreate(t, bd, p.dir, "count closed", "-p", "2", "-l", countSliceLabel)

		if out, err := bdProxiedRun(t, bd, p.dir, "close", closed.ID, "--reason", "done"); err != nil {
			t.Fatalf("bd close: %v\n%s", err, out)
		}

		status, total, groups := sp.countIssues(t, scoped)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if total != 3 {
			t.Fatalf("total = %d, want 3 — a count applies none of the listing's exclusions, so the closed row is in it", total)
		}
		if groups != nil {
			t.Errorf("groups = %v; a request that asked for no buckets must not carry the member", *groups)
		}

		// The parity oracle: `bd count` answers the same number through the
		// same role.
		if cli := bdProxiedCountTotal(t, bd, p.dir, "--label", countSliceLabel); cli != total {
			t.Errorf("bd count = %d, the server said %d; the two front doors are on one role and must agree", cli, total)
		}

		// And the filters narrow it the way the role says: one status, not a
		// set, and an unrecognized name matches nothing rather than failing.
		if _, closedOnly, _ := sp.countIssues(t, scoped+"&status=closed"); closedOnly != 1 {
			t.Errorf("status=closed counted %d, want 1", closedOnly)
		}
		if _, none, _ := sp.countIssues(t, scoped+"&status=no-such-status"); none != 0 {
			t.Errorf("an unrecognized status counted %d, want 0 — the role matches nothing rather than refusing", none)
		}
		if _, byAssignee, _ := sp.countIssues(t, scoped+"&no_assignee=true"); byAssignee != 3 {
			t.Errorf("no_assignee counted %d, want 3", byAssignee)
		}
		if _, byID, _ := sp.countIssues(t, scoped+"&id="+open1.ID); byID != 1 {
			t.Errorf("id=%s counted %d, want 1", open1.ID, byID)
		}
	})

	// THE BUCKETS, and the number a client must not derive. `total` is the
	// cardinality of the whole matching set; the label buckets OVERLAP, so their
	// sum is larger.
	t.Run("grouped counts carry the role's total and not the sum of the buckets", func(t *testing.T) {
		label := countSliceLabel + "-grouped"
		scopedGroup := "?label=" + label

		bdProxiedCreate(t, bd, p.dir, "grouped one", "-p", "1", "-l", label+",alpha")
		bdProxiedCreate(t, bd, p.dir, "grouped two", "-p", "1", "-l", label+",alpha,beta")
		third := bdProxiedCreate(t, bd, p.dir, "grouped three", "-p", "3", "-l", label)
		if out, err := bdProxiedRun(t, bd, p.dir, "close", third.ID, "--reason", "done"); err != nil {
			t.Fatalf("bd close: %v\n%s", err, out)
		}

		status, total, groups := sp.countIssues(t, scopedGroup+"&group_by=status")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if total != 3 {
			t.Fatalf("total = %d, want 3", total)
		}
		if groups == nil {
			t.Fatal("groups is absent on a grouped request")
		}
		if (*groups)["open"] != 2 || (*groups)["closed"] != 1 {
			t.Errorf("status buckets = %v, want two open and one closed", *groups)
		}

		// PRIORITY KEYS ARE NORMALIZED, and the document publishes the
		// normalization because both front doors print these keys unmodified.
		_, _, byPriority := sp.countIssues(t, scopedGroup+"&group_by=priority")
		if byPriority == nil {
			t.Fatal("groups is absent for group_by=priority")
		}
		if (*byPriority)["P1"] != 2 || (*byPriority)["P3"] != 1 {
			t.Errorf("priority buckets = %v, want P1:2 and P3:1 — the key is `P` followed by the number", *byPriority)
		}

		// THE OVERLAP. Three rows; `alpha` covers two of them and `beta` one,
		// and every row carries the scoping label as well, so the buckets sum to
		// more than the set holds.
		_, labelTotal, byLabel := sp.countIssues(t, scopedGroup+"&group_by=label")
		if byLabel == nil {
			t.Fatal("groups is absent for group_by=label")
		}
		if (*byLabel)[label] != 3 || (*byLabel)["alpha"] != 2 || (*byLabel)["beta"] != 1 {
			t.Errorf("label buckets = %v, want the scoping label on 3, alpha on 2 and beta on 1", *byLabel)
		}
		sum := 0
		for _, n := range *byLabel {
			sum += n
		}
		if int64(sum) <= labelTotal {
			t.Fatalf("the label buckets sum to %d and the total is %d; this case cannot show the overlap it is named for", sum, labelTotal)
		}
		if labelTotal != 3 {
			t.Errorf("total = %d under group_by=label, want the scalar 3 — never the bucket sum %d", labelTotal, sum)
		}

		// The assignee dimension's key for unassigned rows is spelled, never
		// empty: an empty key would be indistinguishable from a stored empty
		// assignee.
		_, _, byAssignee := sp.countIssues(t, scopedGroup+"&group_by=assignee")
		if byAssignee == nil {
			t.Fatal("groups is absent for group_by=assignee")
		}
		if (*byAssignee)["(unassigned)"] != 3 {
			t.Errorf("assignee buckets = %v, want three under `(unassigned)`", *byAssignee)
		}

		// A dimension that matches nothing is an EMPTY object, not an absent
		// member and not null: "you asked and nothing matched" has to stay
		// distinguishable from "you did not ask".
		_, emptyTotal, emptyGroups := sp.countIssues(t, "?label=no-such-label-anywhere&group_by=status")
		if emptyTotal != 0 {
			t.Errorf("total = %d for a predicate matching nothing, want 0", emptyTotal)
		}
		if emptyGroups == nil {
			t.Fatal("groups is absent for a grouped request that matched nothing; an empty object is the answer")
		}
		if len(*emptyGroups) != 0 {
			t.Errorf("groups = %v, want empty", *emptyGroups)
		}
	})

	// THE PLANE QUESTION, answered against a real wisp. This is the case the
	// pure tests cannot write: a fake can report the flag it was handed, and
	// only a store can show that the flag moves the SET.
	t.Run("the wisps tier is counted only under include_infra", func(t *testing.T) {
		label := countSliceLabel + "-planes"
		scopedPlane := "?label=" + label

		bdProxiedCreate(t, bd, p.dir, "durable row", "-p", "2", "-l", label)
		wisp := bdProxiedCreate(t, bd, p.dir, "ephemeral row", "-p", "2", "-l", label,
			"--ephemeral", "--wisp-type", "heartbeat")
		if wisp.ID == "" {
			t.Fatal("the wisp was not created")
		}

		_, durableOnly, _ := sp.countIssues(t, scopedPlane)
		if durableOnly != 1 {
			t.Fatalf("a default count answered %d, want 1 — the wisps tier is not counted without include_infra", durableOnly)
		}

		_, withInfra, _ := sp.countIssues(t, scopedPlane+"&include_infra=true")
		if withInfra != 2 {
			t.Fatalf("include_infra counted %d, want 2 — the ephemeral tier is merged in", withInfra)
		}

		// Both answers match `bd count`'s, which is what makes the flag a
		// property of the ROLE rather than of either front door.
		if cli := bdProxiedCountTotal(t, bd, p.dir, "--label", label); cli != durableOnly {
			t.Errorf("bd count = %d, the server said %d", cli, durableOnly)
		}
		if cli := bdProxiedCountTotal(t, bd, p.dir, "--label", label, "--include-infra"); cli != withInfra {
			t.Errorf("bd count --include-infra = %d, the server said %d", cli, withInfra)
		}
	})

	// The refusals, over the wire: a closed vocabulary and a typed parameter
	// set, both refused before any query runs.
	t.Run("the document's refusals are refused", func(t *testing.T) {
		for _, query := range []string{
			"?group_by=nosuch",
			"?group_by=Status",
			"?priority=high",
			"?include_infra=maybe",
			"?created_after=yesterday",
			"?limit=10",
			"?sort=priority",
		} {
			status, _, _ := sp.countIssues(t, query)
			if status != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", query, status)
			}
		}
	})

	sp.shutdown(t)
}
