//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

// This file is a faithful, line-for-line port of the DECODE half of gc's
// (gascity origin/main) internal/beads/bdstore_conditional.go — specifically
// bdConditionalErrorBody, hasDiscriminator, parseBdConditionalErrorBody, and
// decodeBdConditionalBodies. It exists because bd and gc are separate repos
// with no shared Go package: gc's matcher parses bd's CLI JSON error output
// by convention, not by a compiled contract, so the only way to pin that
// contract from bd's side is to copy gc's own decode logic and run it against
// bd's real output. This is the "faithful copy of its decode" alternative the
// 2026-10 Opus review of f8c7ecf01 asked for, in lieu of vendoring the gc
// module. If gc's decode logic changes, this port and
// TestGCConditionalMatcherDecode below must be updated to match — the
// contract is the string/shape, not this file.
//
// Keep this in sync with gc's internal/beads/bdstore_conditional.go.

type gcBdConditionalErrorBody struct {
	Error            string `json:"error"`
	Code             string `json:"code"`
	ExpectedRevision *int64 `json:"expected_revision"`
	CurrentRevision  *int64 `json:"current_revision"`
}

func (b gcBdConditionalErrorBody) hasDiscriminator() bool {
	return b.Code != "" || b.ExpectedRevision != nil || b.CurrentRevision != nil
}

func gcParseBdConditionalErrorBody(out []byte, errText string) (gcBdConditionalErrorBody, bool) {
	sources := [][]byte{out}
	if errText != "" {
		sources = append(sources, []byte(errText))
	}
	var (
		fallback gcBdConditionalErrorBody
		haveAny  bool
	)
	for _, src := range sources {
		for _, body := range gcDecodeBdConditionalBodies(src) {
			if body.hasDiscriminator() {
				return body, true
			}
			if !haveAny {
				fallback, haveAny = body, true
			}
		}
	}
	return fallback, haveAny
}

func gcDecodeBdConditionalBodies(src []byte) []gcBdConditionalErrorBody {
	var bodies []gcBdConditionalErrorBody
	for i := 0; i < len(src); {
		brace := bytes.IndexByte(src[i:], '{')
		if brace < 0 {
			break
		}
		i += brace
		dec := json.NewDecoder(bytes.NewReader(src[i:]))
		var env struct {
			Data *gcBdConditionalErrorBody `json:"data"`
			gcBdConditionalErrorBody
		}
		if dec.Decode(&env) != nil {
			i++
			continue
		}
		if env.Data != nil {
			bodies = append(bodies, *env.Data)
		} else {
			bodies = append(bodies, env.gcBdConditionalErrorBody)
		}
		i += int(dec.InputOffset())
	}
	return bodies
}

// gcClassifyAsPrecondition replicates classifyConditionalWriteResult's
// authoritative-code branch: a recognized machine body code dominates
// everything else, including the message heuristics gc falls back to when bd
// omits a code. This is the path the BLOCKER fix activates — the fallback
// phrase-matching ("precondition failed" / "revision mismatch") is
// deliberately NOT exercised here, since the fix's whole point is that gc no
// longer needs it.
func gcClassifyAsPrecondition(out []byte, errText string) (ok bool, expected, current int64, haveExpected, haveCurrent bool) {
	body, bodyOK := gcParseBdConditionalErrorBody(out, errText)
	if !bodyOK || body.Code != "precondition_failed" {
		return false, 0, 0, false, false
	}
	if body.ExpectedRevision != nil {
		expected, haveExpected = *body.ExpectedRevision, true
	}
	if body.CurrentRevision != nil {
		current, haveCurrent = *body.CurrentRevision, true
	}
	return true, expected, current, haveExpected, haveCurrent
}

// TestGCConditionalMatcherDecode runs the ported gc decode (above) against a
// real --if-revision mismatch from the built bd, for every verb gc's
// BdStore.{Update,Close,Delete}IfMatch (and bd's own assign/reopen guards)
// cover, pinning the 2026-10 Opus-review BLOCKER fix end-to-end: gc's own
// classifier logic, not just bd's JSON shape in isolation, must recognize the
// refusal as a precondition with the caller's expected revision and bd's
// current one.
func TestGCConditionalMatcherDecode(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "igc")
	env := crossModeEnv{mode: "embedded", bd: bd, dir: dir, env: bdEnv(dir)}

	cases := []struct {
		name       string
		closeFirst bool // reopen's guard only fires on an already-closed issue
		args       func(id string) []string
	}{
		{"update", false, func(id string) []string {
			return []string{"update", id, "--if-revision", "", "--priority", "3", "--json"}
		}},
		{"close", false, func(id string) []string {
			return []string{"close", id, "--if-revision", "", "--json"}
		}},
		{"reopen", true, func(id string) []string {
			return []string{"reopen", id, "--if-revision", "", "--json"}
		}},
		{"delete", false, func(id string) []string {
			return []string{"delete", id, "--if-revision", "", "--force", "--json"}
		}},
		{"assign", false, func(id string) []string {
			return []string{"assign", id, "bob", "--if-revision", "", "--json"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each case gets its own issue so the cases can run (and be
			// re-run individually with `go test -run`) independent of each
			// other's order — a guard mismatch never writes, but reopen's
			// guard specifically only fires on a closed issue.
			issue := bdCreate(t, bd, dir, "GC matcher target: "+tc.name, "--type", "task")
			if tc.closeFirst {
				bdClose(t, bd, dir, issue.ID)
			}
			rev := bdShowRevision(t, bd, dir, issue.ID)
			expected := rev + 1_000_000 // any value distinct from rev works as the "stale" argument
			args := tc.args(issue.ID)
			for i, a := range args {
				if a == "" {
					args[i] = strconv.FormatInt(expected, 10)
				}
			}
			stdout, stderr, code := env.run(t, args...)
			if code != ExitGuardMismatch {
				t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitGuardMismatch, stdout, stderr)
			}
			// gc's runner captures bd's combined output as the subprocess
			// error text; bd's own io split (stdout vs stderr) is not
			// something gc's matcher relies on, so feeding it stderr (where
			// bd actually writes the JSON error) as both "out" and the error
			// string is a faithful stand-in for that runner contract.
			ok, gotExpected, gotCurrent, haveExpected, haveCurrent := gcClassifyAsPrecondition([]byte(stderr), stderr)
			if !ok {
				t.Fatalf("gc's matcher did not classify this as precondition_failed\nstderr:\n%s", stderr)
			}
			if !haveExpected || gotExpected != expected {
				t.Errorf("gc decoded expected_revision = %v, want %d", gotExpected, expected)
			}
			if !haveCurrent {
				t.Errorf("gc decoded no current_revision")
			} else {
				t.Logf("gc decoded current_revision = %d", gotCurrent)
			}
		})
	}
}
