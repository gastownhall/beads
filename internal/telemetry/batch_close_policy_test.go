package telemetry

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

type policyBatchCloserStore struct {
	storage.DoltStorage
	closer issueops.BatchCloser
	policy storage.BatchClosePolicy
}

func (s *policyBatchCloserStore) BatchCloserWithPolicy(policy storage.BatchClosePolicy) (issueops.BatchCloser, error) {
	s.policy = policy
	return s.closer, nil
}

func TestInstrumentedStorageForwardsBatchClosePolicy(t *testing.T) {
	sentinel := &roleAccessorSentinel{}
	raw := &policyBatchCloserStore{closer: sentinel}
	store := &InstrumentedStorage{inner: raw}
	closer, err := storage.BatchCloserWithPolicy(store, storage.NewBatchClosePolicy(map[string][]string{"blocked": {"external:p:c"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(raw.policy.CheckClose("blocked", false), storage.ErrCloseBlocked) {
		t.Fatal("telemetry wrapper dropped external policy")
	}
	instrumented, ok := closer.(*instrumentedBatchCloser)
	if !ok || instrumented.inner != sentinel || instrumented.storage != store {
		t.Fatalf("policy accessor lost telemetry layer: %T", closer)
	}
}
