//go:build cgo

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// End-to-end for the two settings writes, against real Dolt through a real
// `bd serve` subprocess.
//
// The pure tests in internal/httpapi cover the wire edge on a fake role — the
// request projection, the response projection, every refusal this edge owns.
// What only this level can prove is what the STORAGE TRANSACTION did, and on
// this plane that is most of the contract:
//
//   - the write LANDS and the CLI sees it, which is the whole point of a
//     configuration write nothing else in the process is going to re-read;
//   - `status.custom` is PROJECTED into custom_statuses in the same transaction
//     as the row, so `bd` starts accepting the status the write configured — a
//     row without its table is a value that has been stored and has no effect,
//     and no fake can be asked about a second table;
//   - the ROLE's refusals are real refusals against a real workspace: writing
//     `issue_prefix` is a 400 and the prefix is unchanged, and an unparseable
//     `status.custom` writes NOTHING — not the row and not the table;
//   - a CREDENTIAL-BEARING key is WRITABLE and its value never comes back, on
//     either the write or the read that follows it;
//   - the removal is idempotent against the table, not merely against the
//     handler.

// putJSON puts body to path with the documented media type and returns the
// status and decoded body.
func (sp *serveProcess) putJSON(t *testing.T, path, body string) (int, map[string]any) {
	t.Helper()
	return sp.bodyRequest(t, http.MethodPut, path, body)
}

// deleteAt sends the bodyless removal and returns the status and decoded body.
func (sp *serveProcess) deleteAt(t *testing.T, path string) (int, map[string]any) {
	t.Helper()
	return sp.bodyRequest(t, http.MethodDelete, path, "")
}

func (sp *serveProcess) bodyRequest(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, sp.url(path), reader)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := sp.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v\nstderr:\n%s", method, path, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	var m map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode %s %s body %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode, m
}

func TestProxiedServerServeSettingsWrites(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvcfgw")
	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// THE ROW LANDS, and the CLI is the read-back rather than this surface's own
	// GET: a write only this API can see would be a plane of its own rather than
	// the settings plane `bd config` reads.
	t.Run("the value lands and the CLI reads it", func(t *testing.T) {
		status, body := sp.putJSON(t, "/v0/beads/config/notes.wire", `{"value":"written over http"}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		if body["key"] != "notes.wire" || body["value"] != "written over http" {
			t.Errorf("body = %v, want the stored setting", body)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "config", "get", "notes.wire")
		if err != nil {
			t.Fatalf("bd config get: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "written over http") {
			t.Errorf("bd config get printed %q, want the value the HTTP write stored", out)
		}
	})

	// THE PROJECTION, which is the reason SetSetting is not a thin REPLACE INTO
	// and the one property no fake can be asked about: `status.custom` is
	// rewritten into custom_statuses IN THE SAME TRANSACTION as the row, and the
	// visible consequence is that `bd` starts accepting the status it configured.
	t.Run("status.custom is projected into the table reads consult", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "an issue to park", "-p", "2")

		// Before the write the status is not in the workspace's vocabulary.
		if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID, "--status", "awaiting_review"); err == nil {
			t.Fatalf("the workspace already accepts awaiting_review; this test proves nothing:\n%s", out)
		}

		status, body := sp.putJSON(t, "/v0/beads/config/status.custom", `{"value":"awaiting_review:active"}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}

		if out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID, "--status", "awaiting_review"); err != nil {
			t.Fatalf("the row landed without its table: %v\n%s", err, out)
		}
	})

	// THE PROTECTED KEY, refused by the ROLE against a real workspace, with the
	// prefix left exactly as it was. A wire that let this through would leave the
	// beads created before the write and the beads created after it disagreeing
	// about their own namespace.
	t.Run("the issue prefix is refused in either spelling and nothing changes", func(t *testing.T) {
		before := bdProxiedCreate(t, bd, p.dir, "a bead minted before", "-p", "2")

		for _, key := range []string{"issue_prefix", "issue-prefix"} {
			status, body := sp.putJSON(t, "/v0/beads/config/"+key, `{"value":"zz"}`)
			if status != http.StatusBadRequest {
				t.Fatalf("writing %s: status = %d, want 400: %v", key, status, body)
			}
			if body["code"] != "invalid_argument" {
				t.Errorf("writing %s: code = %v, want invalid_argument", key, body["code"])
			}
			if detail, _ := body["detail"].(string); !strings.Contains(detail, "bd init --prefix") {
				t.Errorf("writing %s: detail = %q, want the role's own sentence", key, detail)
			}
		}

		after := bdProxiedCreate(t, bd, p.dir, "a bead minted after", "-p", "2")
		beforePrefix := before.ID[:strings.LastIndex(before.ID, "-")]
		afterPrefix := after.ID[:strings.LastIndex(after.ID, "-")]
		if beforePrefix != afterPrefix {
			t.Errorf("the workspace's prefix moved from %q to %q; the refusal did not hold", beforePrefix, afterPrefix)
		}
	})

	// AN UNPARSEABLE PROJECTION WRITES NOTHING — not the row and not the table.
	// The parse is in the role rather than at a front door precisely so a value
	// that cannot be projected cannot become a row that one door accepted and
	// another refused.
	t.Run("a status.custom that does not parse leaves the row alone", func(t *testing.T) {
		const good = "triage_hold:active"
		if status, body := sp.putJSON(t, "/v0/beads/config/status.custom", `{"value":"`+good+`"}`); status != http.StatusOK {
			t.Fatalf("seeding a parseable value: status = %d: %v", status, body)
		}

		status, body := sp.putJSON(t, "/v0/beads/config/status.custom", `{"value":"::not a status::"}`)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %v", status, body)
		}

		readStatus, read, _ := sp.get(t, "/v0/beads/config/status.custom")
		if readStatus != http.StatusOK {
			t.Fatalf("read back: status = %d", readStatus)
		}
		if read["value"] != good {
			t.Errorf("value = %v, want the previous value %q — the refused write must change nothing", read["value"], good)
		}
	})

	// THE DOCTRINE, against a real workspace: a credential-bearing key is
	// WRITABLE, and neither the write nor the read hands the value back. Refusing
	// the write would protect nothing a writer does not already hold; withholding
	// it from every reader is what redaction actually is.
	t.Run("a credential key is writable and is never handed back", func(t *testing.T) {
		const secret = "not-a-real-token-9f13"
		status, body := sp.putJSON(t, "/v0/beads/config/notion.token", `{"value":"`+secret+`"}`)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200 — a credential-bearing key is writable: %v", status, body)
		}
		if body["redacted"] != true {
			t.Errorf("redacted = %v, want true", body["redacted"])
		}
		if _, present := body["value"]; present {
			t.Errorf("the write handed the credential back: %v", body)
		}

		// The read agrees, and the enumeration agrees: one rule, decided on the
		// key, for every operation that could carry the value.
		readStatus, read, _ := sp.get(t, "/v0/beads/config/notion.token")
		if readStatus != http.StatusOK {
			t.Fatalf("read back: status = %d", readStatus)
		}
		if read["redacted"] != true || read["value"] != nil {
			t.Errorf("the read published the credential: %v", read)
		}
		listStatus, list, _ := sp.get(t, "/v0/beads/config")
		if listStatus != http.StatusOK {
			t.Fatalf("list: status = %d", listStatus)
		}
		if raw, err := json.Marshal(list); err != nil {
			t.Fatalf("marshal the settings page: %v", err)
		} else if strings.Contains(string(raw), secret) {
			t.Errorf("the settings enumeration carries the stored credential")
		}

		// And the CLI, which holds the database, prints it: the withholding is
		// this SURFACE's and not a claim that the value was not stored.
		out, err := bdProxiedRun(t, bd, p.dir, "config", "get", "notion.token")
		if err != nil {
			t.Fatalf("bd config get: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), secret) {
			t.Errorf("bd config get printed %q; the write did not store the value it accepted", out)
		}
	})

	// THE VALUE BOUND, against the real column, in both directions. This is the
	// hazard settingWriteKey already guards on the key half: config.value is a
	// TEXT column, and before the bound a 100 KiB PUT was a generic 500 from the
	// column — a request the caller could have fixed, answered with the one code
	// that says nothing about how to fix it.
	//
	// The accepted case is the half that keeps the refusal honest: a value at
	// the ceiling must reach the column and land, or the bound is stricter than
	// the storage it claims to describe.
	t.Run("an oversized value is refused and a ceiling value lands", func(t *testing.T) {
		oversized, err := json.Marshal(map[string]string{"value": strings.Repeat("v", 100*1024)})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		status, body := sp.putJSON(t, "/v0/beads/config/notes.big", string(oversized))
		if status != http.StatusBadRequest {
			t.Fatalf("a 100 KiB value: status = %d, want 400 — refused at the edge, not by the column: %v", status, body)
		}
		if body["code"] != "invalid_argument" || body["param"] != "value" {
			t.Errorf("problem = %v, want invalid_argument on param value", body)
		}

		// Nothing landed, which a 400 asserts and a read-back proves.
		readStatus, read, _ := sp.get(t, "/v0/beads/config/notes.big")
		if readStatus != http.StatusOK {
			t.Fatalf("read back: status = %d", readStatus)
		}
		if _, present := read["value"]; present {
			t.Errorf("the refused write landed: %v", read)
		}

		// And exactly the column's ceiling goes all the way through to Dolt.
		ceiling, err := json.Marshal(map[string]string{"value": strings.Repeat("c", 65535)})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		status, body = sp.putJSON(t, "/v0/beads/config/notes.ceiling", string(ceiling))
		if status != http.StatusOK {
			t.Fatalf("a 65535-byte value: status = %d, want 200 — this value fits the column: %v", status, body)
		}
		readStatus, read, _ = sp.get(t, "/v0/beads/config/notes.ceiling")
		if readStatus != http.StatusOK {
			t.Fatalf("read back: status = %d", readStatus)
		}
		if got, _ := read["value"].(string); len(got) != 65535 {
			t.Errorf("stored %d bytes of a 65535-byte value; the column truncated it", len(got))
		}
	})

	// THE REMOVAL, against the table: it removes, and removing again succeeds
	// rather than reporting a miss this role cannot see.
	t.Run("the removal is idempotent and reports no miss", func(t *testing.T) {
		if status, body := sp.putJSON(t, "/v0/beads/config/notes.temp", `{"value":"transient"}`); status != http.StatusOK {
			t.Fatalf("seed: status = %d: %v", status, body)
		}

		for i := range 2 {
			status, body := sp.deleteAt(t, "/v0/beads/config/notes.temp")
			if status != http.StatusOK {
				t.Fatalf("removal %d: status = %d, want 200 — this operation has no miss to report: %v", i+1, status, body)
			}
			if body["key"] != "notes.temp" {
				t.Errorf("removal %d: key = %v, want the key the path named", i+1, body["key"])
			}
			if _, present := body["removed"]; present {
				t.Errorf("removal %d publishes a `removed` flag: %v", i+1, body)
			}
		}

		// Gone from the CLI's view too, which is the read-back that matters.
		out, err := bdProxiedRun(t, bd, p.dir, "config", "get", "notes.temp")
		if err == nil && strings.Contains(string(out), "transient") {
			t.Errorf("bd config get still prints the removed value: %s", out)
		}
	})
}
