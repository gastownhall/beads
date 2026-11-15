//go:build cgo && unix

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestEmbeddedArrivalSeqOnEveryJSONRead pins arrival_seq at the CLI's JSON
// surface, the one a consumer that shells out to bd reads. With the journal on,
// a wisp-plane bead (ephemeral or no_history) is stamped with the seq of its own
// create record, and every --json read of it carries that value: the create's
// own answer, show, list, ready and query. A durable bead carries no member,
// and bd export never writes one, since the value is local to this workspace.
//
// The storage contract cases (backend/conformance, the Journal role's arrival
// cases) prove the stamp on every backend. What they cannot see is the CLI's
// own rendering, which marshals types.Issue, IssueWithCounts or IssueDetails
// per command; a command that switched to a projection of its own would drop
// the member without any storage test noticing.
func TestEmbeddedArrivalSeqOnEveryJSONRead(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "asq", "--skip-hooks", "--skip-agents")
	env := append(envWithout(bdEnv(dir), "BD_EVENTS_JOURNAL"), "BD_EVENTS_JOURNAL=1")
	run := func(args ...string) string {
		t.Helper()
		stdout, _ := runBDOut(t, bd, dir, env, args...)
		return stdout
	}

	const (
		ephemeral = "asq-eph"
		noHistory = "asq-nohist"
		durable   = "asq-durable"
	)
	var created map[string]any
	if err := json.Unmarshal([]byte(run("create", "--json", "--id", ephemeral, "--ephemeral", "an ephemeral wisp")), &created); err != nil {
		t.Fatalf("decoding bd create --json: %v", err)
	}
	run("create", "--silent", "--id", noHistory, "--no-history", "a no-history wisp")
	run("create", "--silent", "--id", durable, "a durable bead")

	// A wisp's stamp IS the seq of its own create record; the durable bead's
	// create is journaled too, but stamps nothing (want stays 0).
	want := map[string]int64{ephemeral: 0, noHistory: 0, durable: 0}
	for _, rec := range decodeEventRecords(t, run("events", "export")) {
		if (rec.IssueID == ephemeral || rec.IssueID == noHistory) && rec.Op == "create" {
			want[rec.IssueID] = rec.Seq
		}
	}
	for _, id := range []string{ephemeral, noHistory} {
		if want[id] == 0 {
			t.Fatalf("no create record for %s in bd events export", id)
		}
	}

	requireStamp := func(t *testing.T, surface, id string, row map[string]any) {
		t.Helper()
		got, present := row["arrival_seq"]
		if want[id] == 0 {
			if present {
				t.Errorf("%s: durable %s carries arrival_seq %v, want no member", surface, id, got)
			}
			return
		}
		if n, ok := got.(float64); !present || !ok || int64(n) != want[id] {
			t.Errorf("%s: %s arrival_seq = %v (present %v), want %d, the seq of its create record", surface, id, got, present, want[id])
		}
	}

	t.Run("create", func(t *testing.T) {
		requireStamp(t, "bd create --json", ephemeral, created)
	})
	for _, surface := range []struct {
		name string
		args []string
	}{
		{"show", []string{"show", ephemeral, noHistory, durable, "--json"}},
		{"list", []string{"list", "--include-ephemeral", "--json"}},
		{"ready", []string{"ready", "--include-ephemeral", "--json"}},
		{"query", []string{"query", "status=open", "--json"}},
	} {
		t.Run(surface.name, func(t *testing.T) {
			var rows []map[string]any
			out := run(surface.args...)
			if err := json.Unmarshal([]byte(out), &rows); err != nil {
				t.Fatalf("decoding bd %s: %v\n%s", strings.Join(surface.args, " "), err, out)
			}
			seen := map[string]bool{}
			for _, row := range rows {
				id, _ := row["id"].(string)
				if _, ok := want[id]; !ok {
					continue
				}
				seen[id] = true
				requireStamp(t, "bd "+strings.Join(surface.args, " "), id, row)
			}
			for id := range want {
				if !seen[id] {
					t.Errorf("bd %s did not return %s", strings.Join(surface.args, " "), id)
				}
			}
		})
	}
	t.Run("export", func(t *testing.T) {
		out := run("export")
		if !strings.Contains(out, noHistory) {
			t.Fatalf("bd export did not write the no_history wisp %s, so it cannot show the member is stripped:\n%s", noHistory, out)
		}
		if strings.Contains(out, "arrival_seq") {
			t.Errorf("bd export wrote arrival_seq, a value local to this workspace:\n%s", out)
		}
	})
}
