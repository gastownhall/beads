package main

import (
	"context"
	"strings"
	"testing"
)

func TestHashSteeringPayloadStable(t *testing.T) {
	a := hashSteeringPayload("hello")
	b := hashSteeringPayload("hello")
	c := hashSteeringPayload("hello\n")
	if a != b {
		t.Fatalf("hash not stable: %s vs %s", a, b)
	}
	if a == c {
		t.Fatal("hash should change when payload bytes change")
	}
	if len(a) != 64 {
		t.Fatalf("sha256 hex length = %d, want 64", len(a))
	}
}

func TestFormatSteeringReceiptOmitsRawPrompt(t *testing.T) {
	prompt := "rotate production secrets to abcdef"
	r := steeringReceipt{
		Provider:   "cursor",
		Event:      "beforeSubmitPrompt",
		ThreadID:   "conv-1",
		PayloadSHA: hashSteeringPayload(prompt),
	}
	text := formatSteeringReceipt(r)
	if strings.Contains(text, prompt) {
		t.Fatalf("raw prompt leaked into receipt:\n%s", text)
	}
	if strings.Contains(text, "abcdef") {
		t.Fatalf("secret fragment leaked into receipt:\n%s", text)
	}
	if !strings.Contains(text, "kind: "+steeringReceiptKind) {
		t.Fatalf("missing kind:\n%s", text)
	}
	if !commentHasPayloadHash(text, r.PayloadSHA) {
		t.Fatalf("missing payload hash:\n%s", text)
	}
	if !strings.Contains(text, "thread_id: conv-1") {
		t.Fatalf("missing thread id:\n%s", text)
	}
	if !strings.Contains(text, "turn_id: unavailable") {
		t.Fatalf("empty turn_id should be unavailable:\n%s", text)
	}
}

func TestCommentsHavePayloadHashIdempotent(t *testing.T) {
	sha := hashSteeringPayload("p")
	text := formatSteeringReceipt(steeringReceipt{Provider: "codex", Event: "UserPromptSubmit", PayloadSHA: sha})
	if !commentsHavePayloadHash([]string{"noise", text}, sha) {
		t.Fatal("expected hash present")
	}
	if commentsHavePayloadHash([]string{"noise"}, sha) {
		t.Fatal("did not expect hash")
	}
}

func TestUnreconciledSteeringReceipts(t *testing.T) {
	r1 := formatSteeringReceipt(steeringReceipt{Provider: "claude", Event: "UserPromptSubmit", PayloadSHA: hashSteeringPayload("a")})
	r2 := formatSteeringReceipt(steeringReceipt{Provider: "claude", Event: "UserPromptSubmit", PayloadSHA: hashSteeringPayload("b")})
	pending := unreconciledSteeringReceipts([]string{r1, "Steering reconciliation: r1", r2})
	if len(pending) != 1 {
		t.Fatalf("pending=%d, want 1", len(pending))
	}
	if !commentHasPayloadHash(pending[0], hashSteeringPayload("b")) {
		t.Fatalf("expected r2 pending, got %s", pending[0])
	}
	if n := unreconciledSteeringReceipts([]string{r1, "Steering reconciliation v1"}); len(n) != 0 {
		t.Fatalf("reconciled list should be empty, got %d", len(n))
	}
}

func TestUnavailableIdentity(t *testing.T) {
	if got := unavailableIdentity(""); got != "unavailable" {
		t.Fatalf("got %q", got)
	}
	if got := unavailableIdentity("  "); got != "unavailable" {
		t.Fatalf("got %q", got)
	}
	if got := unavailableIdentity("id-1"); got != "id-1" {
		t.Fatalf("got %q", got)
	}
}

func TestRecordSteeringOnOwningBeadIdempotentAndSecretFree(t *testing.T) {
	origID := steeringOwningBeadID
	origList := steeringCommentLister
	origAdd := steeringCommentAdder
	t.Cleanup(func() {
		steeringOwningBeadID = origID
		steeringCommentLister = origList
		steeringCommentAdder = origAdd
	})

	steeringOwningBeadID = func() string { return "bd-own" }
	var added []string
	steeringCommentLister = func(_ context.Context, issueID string) ([]string, error) {
		if issueID != "bd-own" {
			t.Fatalf("listed %s", issueID)
		}
		out := make([]string, len(added))
		copy(out, added)
		return out, nil
	}
	steeringCommentAdder = func(_ context.Context, issueID, text string) error {
		if issueID != "bd-own" {
			t.Fatalf("added %s", issueID)
		}
		if strings.Contains(text, "SECRET-TOKEN") {
			t.Fatalf("raw prompt leaked: %s", text)
		}
		added = append(added, text)
		return nil
	}

	prompt := "do not store SECRET-TOKEN"
	r := steeringReceipt{Provider: "cursor", Event: "beforeSubmitPrompt", ThreadID: "t1"}
	if err := recordSteeringOnOwningBead(context.Background(), r, prompt); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if err := recordSteeringOnOwningBead(context.Background(), r, prompt); err != nil {
		t.Fatalf("second record: %v", err)
	}
	if len(added) != 1 {
		t.Fatalf("added %d comments, want 1 (idempotent)", len(added))
	}
}

func TestRecordSteeringSkipsWhenNoOwningBead(t *testing.T) {
	origID := steeringOwningBeadID
	origAdd := steeringCommentAdder
	t.Cleanup(func() {
		steeringOwningBeadID = origID
		steeringCommentAdder = origAdd
	})
	steeringOwningBeadID = func() string { return "" }
	steeringCommentAdder = func(context.Context, string, string) error {
		t.Fatal("should not add without owning bead")
		return nil
	}
	if err := recordSteeringOnOwningBead(context.Background(), steeringReceipt{Provider: "cursor", Event: "beforeSubmitPrompt"}, "x"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestRecordSteeringSkipsEmptyPayload(t *testing.T) {
	origID := steeringOwningBeadID
	origAdd := steeringCommentAdder
	t.Cleanup(func() {
		steeringOwningBeadID = origID
		steeringCommentAdder = origAdd
	})
	steeringOwningBeadID = func() string { return "bd-own" }
	steeringCommentAdder = func(context.Context, string, string) error {
		t.Fatal("should not add empty payload")
		return nil
	}
	if err := recordSteeringOnOwningBead(context.Background(), steeringReceipt{Provider: "cursor", Event: "beforeSubmitPrompt"}, "  "); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestRefuseCloseForUnreconciledSteering(t *testing.T) {
	r := formatSteeringReceipt(steeringReceipt{Provider: "cursor", Event: "beforeSubmitPrompt", PayloadSHA: hashSteeringPayload("p")})
	if msg := refuseCloseForUnreconciledSteering(false, false, []string{r}); msg != "" {
		t.Fatalf("disabled gate should allow close, got %q", msg)
	}
	if msg := refuseCloseForUnreconciledSteering(true, true, []string{r}); msg != "" {
		t.Fatalf("--force should allow close, got %q", msg)
	}
	if msg := refuseCloseForUnreconciledSteering(false, true, []string{r, "Steering reconciliation ok"}); msg != "" {
		t.Fatalf("reconciled receipts should allow close, got %q", msg)
	}
	if msg := refuseCloseForUnreconciledSteering(false, true, []string{r}); msg == "" {
		t.Fatal("expected refusal for unreconciled receipt")
	}
}
