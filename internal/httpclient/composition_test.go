// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/composition_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
)

// The served-surface composition fixture shape.
//
// Behavior parity for this backend is proved by running the per-role conformance
// contracts through client → in-process `bd serve` → reference store, never by
// bare RunAll: RunAll's Factory contract wants a write-ready store and its cases
// seed through raw CreateIssue/SetConfig, both of which this backend refuses.
//
// The binding rule that makes the composition honest is that SEEDING NEVER GOES
// THROUGH THE CLIENT. Each fixture's seed handles bind to the reference store the
// server serves; only the subject role binds to the http client. A fixture that
// seeded through the client would prove the client agrees with itself.
//
// This bead bootstraps the shape and its invariants. Standing up the in-process
// server and wiring the per-role contracts onto it is ga-soq7e's work.

// servedFixture is what a per-role contract binds against.
type servedFixture struct {
	// Reference is the store the server serves — the seed handle. Contracts
	// write their preconditions here.
	Reference storage.DoltStorage
	// Subject is the http client store under test. Contracts read and exercise
	// the role through this and only this.
	Subject *Store
	// BaseURL is the address the in-process server bound.
	BaseURL string
}

// errSeedsThroughClient reports the one binding that silently invalidates every
// contract run through the fixture.
var errSeedsThroughClient = errors.New(
	"served fixture seeds through the client: the seed handle must be the server's reference " +
		"store, or the contract proves only that the client agrees with itself")

// checkFixtureBinding validates a binding before it is used. It is separate from
// newServedFixture so the invariant itself is testable without a live harness.
func checkFixtureBinding(reference storage.DoltStorage, subject *Store) error {
	if reference == nil {
		return errors.New("served fixture needs a reference store to seed through")
	}
	if subject == nil {
		return errors.New("served fixture needs a client store as its subject")
	}
	if reference == storage.DoltStorage(subject) {
		return errSeedsThroughClient
	}
	return nil
}

// newServedFixture binds a reference store and a client store into the shape
// above.
func newServedFixture(t *testing.T, reference storage.DoltStorage, subject *Store, baseURL string) servedFixture {
	t.Helper()
	if err := checkFixtureBinding(reference, subject); err != nil {
		t.Fatal(err)
	}
	return servedFixture{Reference: reference, Subject: subject, BaseURL: baseURL}
}

// bindRole is how a role contract asks the fixture for its subject. It returns
// the accessor's error unchanged, so a contract wired before its role bead lands
// FAILS rather than skips — refusals are owned by the unsupported contract, not
// by skipped conformance cases.
func (f servedFixture) bindRole(bind func(*Store) error) error {
	return bind(f.Subject)
}

// stubReference is a placeholder seed handle: it exists so the fixture shape can
// be exercised before the in-process server harness lands. ga-soq7e replaces it
// with the real embedded-Dolt store the server serves.
type stubReference struct{ *Store }

// TestFixtureBindingRejectsSeedingThroughTheClient pins the invariant. It is the
// one thing about the composition that cannot be checked by reading the fixture
// wiring later, once there are seventeen of them.
func TestFixtureBindingRejectsSeedingThroughTheClient(t *testing.T) {
	subject := New(testTarget(t), &fakeWire{res: &apigen.ContextResponse{}}, nil)

	if err := checkFixtureBinding(subject, subject); !errors.Is(err, errSeedsThroughClient) {
		t.Errorf("binding the client as its own seed handle = %v, want a refusal", err)
	}
	if err := checkFixtureBinding(nil, subject); err == nil {
		t.Error("binding with no reference store was accepted")
	}
	if err := checkFixtureBinding(stubReference{subject}, nil); err == nil {
		t.Error("binding with no subject was accepted")
	}
}

// TestServedFixtureBindsSubjectAndReferenceApart is the positive arm.
func TestServedFixtureBindsSubjectAndReferenceApart(t *testing.T) {
	subject := New(testTarget(t), &fakeWire{res: &apigen.ContextResponse{}}, nil)
	reference := stubReference{New(testTarget(t), nil, nil)}

	f := newServedFixture(t, reference, subject, "http://127.0.0.1:7777")
	if f.Subject != subject {
		t.Error("fixture bound the wrong subject")
	}
	if f.Reference == storage.DoltStorage(f.Subject) {
		t.Error("fixture bound one store as both seed handle and subject")
	}
}

// TestServedFixtureRoleBindingFailsRatherThanSkips: until a role bead lands, its
// accessor refuses, and a contract wired against it must surface that refusal.
func TestServedFixtureRoleBindingFailsRatherThanSkips(t *testing.T) {
	subject := New(testTarget(t), &fakeWire{res: &apigen.ContextResponse{}}, nil)
	reference := stubReference{New(testTarget(t), nil, nil)}
	f := newServedFixture(t, reference, subject, "http://127.0.0.1:7777")

	// VersionReconciler is the sample: this case needs an ACCESSOR — that is what
	// bindRole takes — and it asserts only the typed sentinel and its Op, both
	// of which the generated shell's refusal carries. So unlike the two refusal
	// tests in store_test.go it does not need a store-bound refusal, and it must
	// use one of the accessors that refuses PERMANENTLY rather than one that
	// eventually flips.
	//
	// It used to be Commenter, chosen when that was one of seven permanent
	// refusals. It stopped being one: #5594 published addComment and client wave
	// ga-f352s wired the accessor, so this case failed the day the role started
	// answering — which is the flip rule finding a second, unplanned home. The
	// sample is now a role no wire operation can ever answer for a client:
	// version markers are clone-local, so there is nothing for a server to serve.
	err := f.bindRole(func(s *Store) error {
		_, err := s.VersionReconciler()
		return err
	})
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) || unsup.Op != "VersionReconciler" {
		t.Fatalf("role binding returned %v, want the typed refusal naming VersionReconciler", err)
	}
}
