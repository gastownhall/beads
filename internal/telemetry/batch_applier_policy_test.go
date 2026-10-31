package telemetry

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

type policyBatchApplierStore struct {
	storage.DoltStorage
	applier issueops.BatchApplier
	policy  storage.BatchClosePolicy
}

func (s *policyBatchApplierStore) BatchApplierWithPolicy(policy storage.BatchClosePolicy) (issueops.BatchApplier, error) {
	s.policy = policy
	return s.applier, nil
}

type batchApplierSentinel struct{ issueops.BatchApplier }

func TestInstrumentedStorageForwardsBatchApplyPolicy(t *testing.T) {
	sentinel := &batchApplierSentinel{}
	raw := &policyBatchApplierStore{applier: sentinel}
	store := &InstrumentedStorage{inner: raw}
	applier, err := storage.BatchApplierWithPolicy(store, storage.NewBatchClosePolicy(map[string][]string{"blocked": {"external:p:c"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(raw.policy.CheckClose("blocked", false), storage.ErrCloseBlocked) {
		t.Fatal("telemetry wrapper dropped external policy")
	}
	instrumented, ok := applier.(*instrumentedBatchApplier)
	if !ok || instrumented.inner != sentinel || instrumented.storage != store {
		t.Fatalf("policy accessor lost telemetry layer: %T", applier)
	}
}
