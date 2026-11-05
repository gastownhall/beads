package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// mergeSlotRaceStore holds one merge slot row and runs each transaction's
// callback against a copy of it, keeping the callback's writes only when the
// transaction commits. Its first transaction loses a write conflict to a
// competitor whose acquire commits first.
type mergeSlotRaceStore struct {
	Storage
	slot         types.Issue
	competitor   string
	transactions int
}

func (s *mergeSlotRaceStore) GetConfig(context.Context, string) (string, error) { return "test", nil }

func (s *mergeSlotRaceStore) RunInTransaction(_ context.Context, _ string, fn func(tx Transaction) error) error {
	s.transactions++
	tx := &mergeSlotRaceTx{slot: s.slot}
	if err := fn(tx); err != nil {
		return err
	}
	if s.transactions == 1 {
		s.slot.Status = types.StatusInProgress
		s.slot.Metadata = json.RawMessage(`{"holder":"` + s.competitor + `"}`)
		return MarkTransactionConflict(errors.New("sql commit (regular): serialization failure"))
	}
	s.slot = tx.slot
	return nil
}

type mergeSlotRaceTx struct {
	Transaction
	slot types.Issue
}

func (tx *mergeSlotRaceTx) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if id != tx.slot.ID {
		return nil, ErrNotFound
	}
	slot := tx.slot
	return &slot, nil
}

func (tx *mergeSlotRaceTx) UpdateIssue(_ context.Context, _ string, updates map[string]interface{}, _ string) error {
	if status, ok := updates["status"].(types.Status); ok {
		tx.slot.Status = status
	}
	if meta, ok := updates["metadata"].(string); ok {
		tx.slot.Metadata = json.RawMessage(meta)
	}
	return nil
}

// TestMergeSlotAcquireReplayReportsTheReplaysOutcome: two agents acquiring at
// once is the race the slot arbitrates. The loser's transaction is replayed,
// finds the slot held, and must report exactly that, not the acquisition its
// rolled-back attempt made.
func TestMergeSlotAcquireReplayReportsTheReplaysOutcome(t *testing.T) {
	useInstantReplayTimer(t)
	st := &mergeSlotRaceStore{slot: types.Issue{ID: "test-merge-slot", Status: types.StatusOpen}, competitor: "other"}
	res, err := MergeSlotAcquireImpl(context.Background(), st, "me", "me", false)
	if err != nil {
		t.Fatalf("acquire was not replayed to an answer: %v", err)
	}
	if st.transactions != 2 {
		t.Fatalf("transactions = %d, want 2 (one lost conflict, one replay)", st.transactions)
	}
	if res.Acquired || res.Holder != "other" || res.SlotID != "test-merge-slot" {
		t.Fatalf("result = %+v, want the slot reported held by other: the rolled-back attempt's acquisition leaked into the replay's result", res)
	}
	if st.slot.Status != types.StatusInProgress || string(st.slot.Metadata) != `{"holder":"other"}` {
		t.Fatalf("slot = %s %s, want the competitor's acquisition intact", st.slot.Status, st.slot.Metadata)
	}
}
