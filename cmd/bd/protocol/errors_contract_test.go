// errors_contract_test.go — protocol v0 §E (errors and exit codes), the
// error-class half.
//
// E4 (error classes). Implementations MUST distinguish, in message and
// structured output (NOT by exit code), at least: not-found, already-claimed
// (naming the holder), not-claimable (naming the state), and validation. The
// Go error values (storage.ErrAlreadyClaimed & co.) are accident; what an agent
// branches on is the message and the --json shape, so that is what these tests
// pin.
//
// E5 (structured errors). With --json, an error is a JSON object carrying at
// least "error" (message), optionally "hint", and schema_version: 1.
//
// E1/E2 (empty is success, failure is nonzero) are pinned in exit_codes_test.go
// and reinforced here: every error class below exits 1, none exits 0.
package protocol

import (
	"strings"
	"testing"
)

const (
	actorAlice = "BEADS_ACTOR=alice"
	actorBob   = "BEADS_ACTOR=bob"
)

// isNotFound reports whether a message expresses the not-found class. The
// clause requires the CLASS to be recognizable, not one blessed phrase, so this
// tolerates both spellings an implementation is likely to use ("not found",
// "no issue found matching …") rather than freezing bd's current wording as
// though it were the protocol.
func isNotFound(msg string) bool {
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "not found") {
		return true
	}
	return (strings.Contains(lower, "no issue found") || strings.Contains(lower, "no issues found")) &&
		strings.Contains(lower, "matching")
}

// TestProtocol_ErrorClass_NotFound pins the not-found class: a nonzero exit,
// a message that expresses not-found, and a structured --json error (§E5).
func TestProtocol_ErrorClass_NotFound(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	out, code := w.runExpectError("show", "nonexistent-xyz")
	if code == 0 {
		t.Errorf("exit code = 0, want nonzero (§E2)")
	}
	if !isNotFound(out) {
		t.Errorf("message does not express the not-found class (§E4):\n%s", out)
	}
	if !strings.Contains(out, "nonexistent-xyz") {
		t.Errorf("not-found message does not name the missing id (§E4):\n%s", out)
	}

	jsonOut, jsonCode := w.runExpectError("show", "nonexistent-xyz", "--json")
	if jsonCode == 0 {
		t.Errorf("--json exit code = 0, want nonzero (§E2)")
	}
	obj := requireJSONError(t, jsonOut, "not-found --json")
	if !isNotFound(errorMessage(obj)) {
		t.Errorf("--json error message does not express the not-found class (§E4/§E5): %q", errorMessage(obj))
	}
}

// TestProtocol_ErrorClass_AlreadyClaimedNamesHolder pins the already-claimed
// class (§E4, §L2.4): claiming an issue another actor holds must fail with a
// message that NAMES THE HOLDER — an agent that cannot see who holds the claim
// cannot decide whether to wait, escalate, or move on.
func TestProtocol_ErrorClass_AlreadyClaimedNamesHolder(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("Contended issue")

	w.runEnv([]string{actorAlice}, "update", id, "--claim")

	out, code := w.runEnvExpectError([]string{actorBob}, "update", id, "--claim")
	if code == 0 {
		t.Errorf("exit code = 0, want nonzero (§L2.4)")
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "already claimed") {
		t.Errorf("message does not identify the already-claimed class (§E4):\n%s", out)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("message does not name the holder (§E4/§L2.4):\n%s", out)
	}
}

// TestProtocol_ErrorClass_NotClaimableNamesState pins the not-claimable class
// (§E4): claiming an issue that is in a non-claimable STATE (here: closed) is a
// different failure from already-claimed, and the message must name the state.
// Distinguishing the two is the whole point of the clause — "retry later" is
// right for a contended claim and wrong for a closed issue.
func TestProtocol_ErrorClass_NotClaimableNamesState(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("Closed issue")
	w.run("close", id)

	out, code := w.runEnvExpectError([]string{actorBob}, "update", id, "--claim")
	if code == 0 {
		t.Errorf("exit code = 0, want nonzero (§E2)")
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "not claimable") {
		t.Errorf("message does not identify the not-claimable class (§E4):\n%s", out)
	}
	if !strings.Contains(lower, "closed") {
		t.Errorf("message does not name the blocking state (§E4):\n%s", out)
	}
}

// TestProtocol_ErrorClass_ClaimFailures_StructuredJSON is the §E5 half of the
// claim error classes — and it is SKIPPED because bd-go does not satisfy it
// today (wy-kxgf4).
//
// bd routes not-found through the JSON-respecting error helper, so
// `bd show <missing> --json` emits {error, schema_version:1} (pinned above).
// The claim path does not: `bd update <id> --claim --json` prints
//
//	Error claiming t04…-qqr: issue already claimed by alice
//
// as plain text, so an agent driving bd with --json gets an unparseable stderr
// line for the single most common contended-write failure it must branch on.
// The message contract (§E4) holds; the structured contract (§E5) does not.
//
// Per proposal §14, where bd's behavior deviates from a clause the clause wins
// and the deviation is a bug — so this test asserts the clause and stays
// skipped until wy-kxgf4 lands, at which point it becomes the guardrail.
func TestProtocol_ErrorClass_ClaimFailures_StructuredJSON(t *testing.T) {
	t.Skip("bd emits claim failures as plain text even with --json; violates §E5 (wy-kxgf4)")

	t.Parallel()
	w := newWorkspace(t)

	claimed := w.create("Held by alice")
	w.runEnv([]string{actorAlice}, "update", claimed, "--claim")

	closed := w.create("Closed")
	w.run("close", closed)

	// already-claimed: structured, and still naming the holder.
	out, _ := w.runEnvExpectError([]string{actorBob}, "update", claimed, "--claim", "--json")
	obj := requireJSONError(t, out, "already-claimed --json")
	msg := errorMessage(obj)
	if !strings.Contains(strings.ToLower(msg), "already claimed") || !strings.Contains(msg, "alice") {
		t.Errorf("--json error does not name class + holder (§E4/§E5): %q", msg)
	}

	// not-claimable: structured, and still naming the state.
	out, _ = w.runEnvExpectError([]string{actorBob}, "update", closed, "--claim", "--json")
	obj = requireJSONError(t, out, "not-claimable --json")
	msg = strings.ToLower(errorMessage(obj))
	if !strings.Contains(msg, "not claimable") || !strings.Contains(msg, "closed") {
		t.Errorf("--json error does not name class + state (§E4/§E5): %q", errorMessage(obj))
	}
}

// TestProtocol_ErrorClass_Validation pins the validation class (§E4): a
// malformed argument fails with a message about the argument, and is NOT
// reported as a not-found.
func TestProtocol_ErrorClass_Validation(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := w.create("Valid issue")

	out, code := w.runExpectError("update", id, "--status", "not_a_status")
	if code == 0 {
		t.Errorf("exit code = 0, want nonzero (§E2)")
	}
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "status") {
		t.Errorf("validation message does not name the offending input (§E4):\n%s", out)
	}
	if strings.Contains(lower, "not found") {
		t.Errorf("validation failure misreported as not-found (§E4):\n%s", out)
	}
}

// TestProtocol_ErrorClasses_AreDistinguishable is the clause itself (§E4): the
// three claim-adjacent failures — not-found, already-claimed, not-claimable —
// must be told apart from the message alone. Exit codes deliberately do NOT
// distinguish them in v0 (§E2), so if the messages collapse, an agent has
// nothing left to branch on.
func TestProtocol_ErrorClasses_AreDistinguishable(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)

	claimed := w.create("Held by alice")
	w.runEnv([]string{actorAlice}, "update", claimed, "--claim")

	closed := w.create("Closed")
	w.run("close", closed)

	notFoundOut, notFoundCode := w.runExpectError("update", "nonexistent-xyz", "--claim")
	claimedOut, claimedCode := w.runEnvExpectError([]string{actorBob}, "update", claimed, "--claim")
	closedOut, closedCode := w.runEnvExpectError([]string{actorBob}, "update", closed, "--claim")

	// §E2: all three fail nonzero. §E4: none of them may be told apart by the
	// exit code — the message carries the class.
	for name, code := range map[string]int{
		"not-found":       notFoundCode,
		"already-claimed": claimedCode,
		"not-claimable":   closedCode,
	} {
		if code == 0 {
			t.Errorf("%s: exit code = 0, want nonzero (§E2)", name)
		}
	}

	// Each class is recognized by its own predicate, and must be recognized by
	// EXACTLY one: a message that reads as two classes at once (or as none) is
	// as useless to an agent as a shared exit code.
	recognizers := map[string]func(string) bool{
		"not-found":       isNotFound,
		"already-claimed": func(s string) bool { return strings.Contains(strings.ToLower(s), "already claimed") },
		"not-claimable":   func(s string) bool { return strings.Contains(strings.ToLower(s), "not claimable") },
	}
	outputs := map[string]string{
		"not-found":       notFoundOut,
		"already-claimed": claimedOut,
		"not-claimable":   closedOut,
	}
	for class, out := range outputs {
		var matched []string
		for name, recognize := range recognizers {
			if recognize(out) {
				matched = append(matched, name)
			}
		}
		if len(matched) != 1 || matched[0] != class {
			t.Errorf("%s failure is recognized as %v, want exactly [%s] — the classes are not distinguishable from the message (§E4):\n%s",
				class, matched, class, out)
		}
	}
}
