//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestEmbeddedUpdateJSONRevision(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ur")
	exerciseUpdateJSONRevision(t, crossModeEnv{mode: "embedded", bd: bd, dir: dir, env: bdEnv(dir)})
}

func TestProxiedServerUpdateJSONRevision(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "upr")
	exerciseUpdateJSONRevision(t, crossModeEnv{mode: "proxied", bd: bd, dir: p.dir, env: bdProxiedEnv(p.dir)})
}

// exerciseUpdateJSONRevision pins that every row `bd update --json` returns
// carries the committed post-write revision: the token an immediately
// following `bd show --json` reports, and the one a follow-up --if-revision
// write must echo.
func exerciseUpdateJSONRevision(t *testing.T, env crossModeEnv) {
	id := env.create(t, "Revision echo")
	created := showJSONRevision(t, env, id)

	var afterFirst, afterSecond string
	t.Run("create_then_update", func(t *testing.T) {
		afterFirst = updateJSONRevision(t, env, "update", id, "--set-metadata", "k=v1", "--json")
		if afterFirst == created {
			t.Fatalf("[%s] update --json revision %q did not move off the create revision", env.mode, afterFirst)
		}
		if shown := showJSONRevision(t, env, id); afterFirst != shown {
			t.Fatalf("[%s] update --json revision %q, bd show --json revision %q", env.mode, afterFirst, shown)
		}
	})

	t.Run("update_then_update", func(t *testing.T) {
		afterSecond = updateJSONRevision(t, env, "update", id, "--set-metadata", "k=v2", "--json")
		if afterSecond == afterFirst {
			t.Fatalf("[%s] second update --json revision %q did not move", env.mode, afterSecond)
		}
		if shown := showJSONRevision(t, env, id); afterSecond != shown {
			t.Fatalf("[%s] update --json revision %q, bd show --json revision %q", env.mode, afterSecond, shown)
		}
	})

	t.Run("if_revision_takes_the_update_token", func(t *testing.T) {
		stdout, stderr, code := env.run(t, "update", id, "--if-revision", afterFirst, "--set-metadata", "k=stale", "--json")
		if code != ExitGuardMismatch {
			t.Fatalf("[%s] pre-update token: exit %d, want %d\nstdout:\n%s\nstderr:\n%s",
				env.mode, code, ExitGuardMismatch, stdout, stderr)
		}
		if shown := showJSONRevision(t, env, id); shown != afterSecond {
			t.Fatalf("[%s] refused guard moved the revision: %q, want %q", env.mode, shown, afterSecond)
		}

		guarded := updateJSONRevision(t, env, "update", id, "--if-revision", afterSecond, "--set-metadata", "k=v3", "--json")
		if shown := showJSONRevision(t, env, id); guarded != shown {
			t.Fatalf("[%s] guarded update --json revision %q, bd show --json revision %q", env.mode, guarded, shown)
		}
		if got := env.show(t, id).Metadata; !strings.Contains(string(got), `"v3"`) {
			t.Fatalf("[%s] guarded update did not apply: metadata %s", env.mode, got)
		}
	})

	t.Run("multi_id_rows_each_carry_their_own_revision", func(t *testing.T) {
		other := env.create(t, "Revision echo sibling")
		rows := updateJSONRows(t, env, "update", id, other, "--set-metadata", "k=multi", "--json")
		if len(rows) != 2 {
			t.Fatalf("[%s] update --json returned %d rows, want 2", env.mode, len(rows))
		}
		for _, row := range rows {
			rowID, _ := row["id"].(string)
			if got, shown := rowRevision(t, env.mode, row), showJSONRevision(t, env, rowID); got != shown {
				t.Errorf("[%s] %s: update --json revision %q, bd show --json revision %q", env.mode, rowID, got, shown)
			}
		}
	})

	// A --claim row answers the claim surface, whose v0 response
	// (ClaimResponse.issue) has no revision member, so it stays the bare row.
	t.Run("claim_rows_keep_the_claim_shape", func(t *testing.T) {
		claimed := env.create(t, "Revision echo claim")
		rows := updateJSONRows(t, env, "update", claimed, "--claim", "--json")
		if len(rows) != 1 {
			t.Fatalf("[%s] update --claim --json returned %d rows, want 1", env.mode, len(rows))
		}
		if raw, present := rows[0]["revision"]; present {
			t.Fatalf("[%s] update --claim --json row carries revision %#v; the claim shape has none", env.mode, raw)
		}
	})

	t.Run("envelope", func(t *testing.T) {
		enveloped := env
		enveloped.env = append(append([]string(nil), env.env...), "BD_JSON_ENVELOPE=1")
		out := enveloped.mustRun(t, "update", id, "--set-metadata", "k=enveloped", "--json")
		var body struct {
			SchemaVersion int              `json:"schema_version"`
			Data          []map[string]any `json:"data"`
		}
		start := strings.Index(out, "{")
		if start < 0 {
			t.Fatalf("[%s] enveloped update --json printed no JSON object:\n%s", env.mode, out)
		}
		if err := json.Unmarshal([]byte(out[start:]), &body); err != nil {
			t.Fatalf("[%s] parse enveloped update --json: %v\n%s", env.mode, err, out)
		}
		if body.SchemaVersion != JSONSchemaVersion || len(body.Data) != 1 {
			t.Fatalf("[%s] envelope = schema_version %d with %d rows, want %d with 1:\n%s",
				env.mode, body.SchemaVersion, len(body.Data), JSONSchemaVersion, out)
		}
		if got, shown := rowRevision(t, env.mode, body.Data[0]), showJSONRevision(t, env, id); got != shown {
			t.Fatalf("[%s] enveloped update --json revision %q, bd show --json revision %q", env.mode, got, shown)
		}
	})
}

func updateJSONRevision(t *testing.T, env crossModeEnv, args ...string) string {
	t.Helper()
	rows := updateJSONRows(t, env, args...)
	if len(rows) != 1 {
		t.Fatalf("[%s] bd %s returned %d rows, want 1", env.mode, strings.Join(args, " "), len(rows))
	}
	return rowRevision(t, env.mode, rows[0])
}

func updateJSONRows(t *testing.T, env crossModeEnv, args ...string) []map[string]any {
	t.Helper()
	out := env.mustRun(t, args...)
	start := strings.Index(out, "[")
	if start < 0 {
		t.Fatalf("[%s] bd %s printed no JSON array:\n%s", env.mode, strings.Join(args, " "), out)
	}
	var rows []map[string]any
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&rows); err != nil {
		t.Fatalf("[%s] parse bd %s: %v\n%s", env.mode, strings.Join(args, " "), err, out)
	}
	return rows
}

func showJSONRevision(t *testing.T, env crossModeEnv, id string) string {
	t.Helper()
	return updateJSONRevision(t, env, "show", id, "--json")
}

// rowRevision requires the decimal-string token encoding, so a numeric or
// absent "revision" fails here instead of comparing equal by accident.
func rowRevision(t *testing.T, mode string, row map[string]any) string {
	t.Helper()
	raw, present := row["revision"]
	if !present {
		t.Fatalf("[%s] row %v has no revision key", mode, row["id"])
	}
	rev, ok := raw.(string)
	if !ok {
		t.Fatalf("[%s] row %v revision = %#v, want a decimal string", mode, row["id"], raw)
	}
	if _, err := types.ParseRevisionToken(rev); err != nil {
		t.Fatalf("[%s] row %v revision %q: %v", mode, row["id"], rev, err)
	}
	return rev
}
