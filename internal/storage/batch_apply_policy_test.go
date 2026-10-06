package storage

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

type policyBatchApplierStore struct {
	DoltStorage
	applier issueops.BatchApplier
	policy  BatchClosePolicy
	called  bool
}

func (s *policyBatchApplierStore) BatchApplierWithPolicy(policy BatchClosePolicy) (issueops.BatchApplier, error) {
	s.called = true
	s.policy = policy
	return s.applier, nil
}

type plainBatchApplierStore struct {
	DoltStorage
	applier issueops.BatchApplier
}

func (s *plainBatchApplierStore) BatchApplier() (issueops.BatchApplier, error) {
	return s.applier, nil
}

type batchApplierSentinel struct{ issueops.BatchApplier }

func TestBatchApplierWithPolicyPassesThroughEmptyPolicy(t *testing.T) {
	sentinel := &batchApplierSentinel{}
	store := &plainBatchApplierStore{applier: sentinel}
	applier, err := BatchApplierWithPolicy(store, BatchClosePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if applier != sentinel {
		t.Fatalf("empty policy applier = %T, want the store's own applier", applier)
	}
}

func TestBatchApplierWithPolicyForwardsNonEmptyPolicy(t *testing.T) {
	sentinel := &batchApplierSentinel{}
	store := &policyBatchApplierStore{applier: sentinel}
	policy := NewBatchClosePolicy(map[string][]string{"blocked": {"external:p:c"}})
	applier, err := BatchApplierWithPolicy(store, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !store.called || applier != sentinel {
		t.Fatalf("policy not forwarded: called=%v applier=%T", store.called, applier)
	}
	if !errors.Is(store.policy.CheckClose("blocked", false), ErrCloseBlocked) {
		t.Fatal("forwarded policy lost its external blocker")
	}
}

func TestBatchApplierWithPolicyRefusesUnsupportedBackend(t *testing.T) {
	store := &plainBatchApplierStore{applier: &batchApplierSentinel{}}
	policy := NewBatchClosePolicy(map[string][]string{"blocked": {"external:p:c"}})
	_, err := BatchApplierWithPolicy(store, policy)
	var unsupported *ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("BatchApplierWithPolicy on unsupported backend = %v, want ErrUnsupported", err)
	}
}
