//go:build cgo

package bdhttp_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestE2E_PromoteOverHTTP runs `bd promote <wisp> --reason ...` — the shape
// MC's PR-intake formula (mol-normalized-issue-pr-v1) issues, corpus shape
// mc-promote — through the real `bd` binary against an in-process `bd serve`
// (slice S21b).
//
// The CLI runs issueops.Promote, the entry the direct route runs too: one
// updateIssue carrying `patch.persistence: persistent` (the role's plane move,
// in one server transaction) and then the promotion comment. Against a server
// that does not advertise issues.update.persistence — the MC server's
// handshake shape — the client refuses BEFORE the dial, naming the token, and
// nothing is written.
func TestE2E_PromoteOverHTTP(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	bin := buildBD(t)
	addr := startE2EServer(t)

	t.Run("advertised", func(t *testing.T) {
		proxyAddr, counter := startCountingProxy(t, addr)
		f := newHTTPFixture(t, bin, proxyAddr)
		wisp := f.create("wisp to keep", "--ephemeral", "-l", "keep-me")
		if !showJSONBool(t, f.mustRun(nil, "show", wisp, "--json").stdout, "ephemeral") {
			t.Fatalf("created %s is not ephemeral; nothing to promote", wisp)
		}

		counter.reset()
		r := f.mustRun(nil, "promote", wisp, "--reason", "keep it")
		if !strings.Contains(r.stdout, "Promoted "+wisp+" to permanent bead") {
			t.Errorf("bd promote output = %q, want the promotion line", r.stdout)
		}
		if strings.Contains(r.stderr, "Warning") {
			t.Errorf("bd promote warned: %s", r.stderr)
		}
		patches := 0
		for _, p := range counter.snapshot() {
			if strings.HasPrefix(p, "PATCH /v0/beads/issues/") {
				patches++
			}
		}
		if patches != 1 {
			t.Errorf("bd promote sent %d updateIssue requests, want exactly one: %v", patches, counter.snapshot())
		}

		show := f.mustRun(nil, "show", wisp, "--json").stdout
		if showJSONBool(t, show, "ephemeral") {
			t.Errorf("%s is still ephemeral after bd promote: %s", wisp, show)
		}
		if !strings.Contains(show, "keep-me") {
			t.Errorf("%s lost its label across the promotion: %s", wisp, show)
		}
		comments := f.mustRun(nil, "comments", wisp, "--json").stdout
		if !strings.Contains(comments, "Promoted from Level 0: keep it") {
			t.Errorf("%s carries no promotion comment: %s", wisp, comments)
		}

		// A durable row is refused with the sentence bd promote always printed,
		// and so is a second promotion of the same row.
		again := f.run(nil, "promote", wisp)
		if again.code == 0 || !strings.Contains(again.stderr, "is not a wisp (already persistent)") {
			t.Errorf("re-promote: exit %d stderr=%s, want the not-a-wisp refusal", again.code, again.stderr)
		}
		missing := f.run(nil, "promote", "e2e-absent-wisp9")
		if missing.code == 0 {
			t.Errorf("promote of an absent id exited 0: %s", missing.stdout)
		}
	})

	t.Run("MC server mask", func(t *testing.T) {
		proxyAddr, counter := startMaskingProxy(t, addr, mcContextMask)
		f := newHTTPFixture(t, bin, proxyAddr)
		wisp := f.create("wisp the old server cannot promote", "--ephemeral")

		counter.reset()
		r := f.run(nil, "promote", wisp, "--reason", "keep it")
		if r.code == 0 {
			t.Fatalf("bd promote against a server without issues.update.persistence exited 0: %s", r.stdout)
		}
		if !strings.Contains(r.stderr, "issues.update.persistence") {
			t.Errorf("refusal does not name the missing capability: %s", r.stderr)
		}
		for _, p := range counter.snapshot() {
			if !strings.HasPrefix(p, "GET ") {
				t.Errorf("a refused promote wrote %s; the refusal must come before any write", p)
			}
		}
		if !showJSONBool(t, f.mustRun(nil, "show", wisp, "--json").stdout, "ephemeral") {
			t.Errorf("%s left the wisp plane though the promote was refused", wisp)
		}
	})
}

// showJSONBool reads a boolean member of `bd show --json`'s first row; an
// absent member (omitempty) reads false.
func showJSONBool(t *testing.T, out, field string) bool {
	t.Helper()
	var generic any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &generic); err != nil {
		t.Fatalf("parse bd show --json: %v\n%s", err, out)
	}
	if rows, ok := generic.([]any); ok && len(rows) > 0 {
		generic = rows[0]
	}
	row, _ := generic.(map[string]any)
	b, _ := row[field].(bool)
	return b
}
