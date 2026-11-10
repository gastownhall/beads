package httpclient

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
)

func testPolicyCache(t *testing.T, path, projectID string, now time.Time) *policyCache {
	t.Helper()
	return &policyCache{
		path:      path,
		url:       "https://example.com",
		projectID: projectID,
		ttl:       time.Minute,
		now:       func() time.Time { return now },
	}
}

func enforcingContext() *apigen.ContextResponse {
	return &apigen.ContextResponse{Capabilities: []string{wire.CapExternalDependencies}}
}

// TestPolicyProbeIsAnsweredFromTheCacheAcrossStores pins the point of the
// cache: the second store (a later bd process) learns the server's
// external-dependency policy without a handshake, until the entry ages out.
func TestPolicyProbeIsAnsweredFromTheCacheAcrossStores(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "policy.json")
	now := time.Now()
	target := Target{BaseURL: mustParseURL(t, "https://example.com"), ExpectProjectID: "prj_a"}

	first := &fakeWire{res: enforcingContext()}
	s := New(target, first, nil)
	s.policy = testPolicyCache(t, path, "prj_a", now)
	if enforced, err := s.ServerEnforcesExternalDependencyPolicy(ctx); err != nil || !enforced {
		t.Fatalf("first probe = %v, %v; want true from the handshake", enforced, err)
	}
	if first.hits != 1 {
		t.Fatalf("first store dialed %d handshakes, want 1", first.hits)
	}

	second := &fakeWire{err: errors.New("must not dial")}
	s = New(target, second, nil)
	s.policy = testPolicyCache(t, path, "prj_a", now.Add(30*time.Second))
	if enforced, err := s.ServerEnforcesExternalDependencyPolicy(ctx); err != nil || !enforced {
		t.Fatalf("cached probe = %v, %v; want true without a handshake", enforced, err)
	}
	if second.hits != 0 {
		t.Fatalf("second store dialed %d handshakes, want 0", second.hits)
	}

	expired := &fakeWire{res: &apigen.ContextResponse{}}
	s = New(target, expired, nil)
	s.policy = testPolicyCache(t, path, "prj_a", now.Add(time.Minute))
	if enforced, err := s.ServerEnforcesExternalDependencyPolicy(ctx); err != nil || enforced {
		t.Fatalf("probe past the TTL = %v, %v; want the handshake's false", enforced, err)
	}
	if expired.hits != 1 {
		t.Fatalf("store past the TTL dialed %d handshakes, want 1", expired.hits)
	}
	if enforced, ok := testPolicyCache(t, path, "prj_a", now.Add(time.Minute)).load(); !ok || enforced {
		t.Fatalf("cache after the refresh = %v, %v; want the fresh false", enforced, ok)
	}
}

// TestPolicyCacheRefusesEntriesItCannotTrust covers the entries load must not
// answer from: another project's, one from the future, and a corrupt file.
func TestPolicyCacheRefusesEntriesItCannotTrust(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	path := filepath.Join(dir, "a.json")
	testPolicyCache(t, path, "prj_a", now).store(true)
	if _, ok := testPolicyCache(t, path, "prj_b", now).load(); ok {
		t.Error("an entry written for another project was trusted")
	}
	if _, ok := testPolicyCache(t, path, "prj_a", now.Add(-time.Second)).load(); ok {
		t.Error("an entry written in the future was trusted")
	}
	otherURL := testPolicyCache(t, path, "prj_a", now)
	otherURL.url = "https://other.example"
	if _, ok := otherURL.load(); ok {
		t.Error("an entry written for another server URL was trusted")
	}
	if _, ok := testPolicyCache(t, filepath.Join(dir, "missing.json"), "prj_a", now).load(); ok {
		t.Error("a missing entry was trusted")
	}
	var disabled *policyCache
	disabled.store(true)
	if _, ok := disabled.load(); ok {
		t.Error("a disabled cache answered")
	}
}

// TestHeldHandshakeWinsOverThePolicyCache pins that a process already holding
// the server's context answers from it, and corrects a stale entry.
func TestHeldHandshakeWinsOverThePolicyCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	now := time.Now()
	testPolicyCache(t, path, "prj_a", now).store(true)

	target := Target{BaseURL: mustParseURL(t, "https://example.com"), ExpectProjectID: "prj_a"}
	s := New(target, &fakeWire{err: errors.New("must not dial")}, &apigen.ContextResponse{})
	s.policy = testPolicyCache(t, path, "prj_a", now)
	if enforced, err := s.ServerEnforcesExternalDependencyPolicy(t.Context()); err != nil || enforced {
		t.Fatalf("probe = %v, %v; want the held handshake's false", enforced, err)
	}
	if enforced, ok := testPolicyCache(t, path, "prj_a", now).load(); !ok || enforced {
		t.Fatalf("cache = %v, %v; want it corrected to false", enforced, ok)
	}
}

func TestNewPolicyCacheNeedsAProjectPinAndATTL(t *testing.T) {
	url := mustParseURL(t, "https://example.com")
	if newPolicyCache(Target{BaseURL: url}) != nil {
		t.Error("a workspace with no project pin got a policy cache")
	}
	for _, off := range []string{"0", "off", "OFF", "soon", "-5s"} {
		t.Setenv(PolicyCacheTTLEnv, off)
		if newPolicyCache(Target{BaseURL: url, ExpectProjectID: "prj_a"}) != nil {
			t.Errorf("%s=%q left the policy cache on", PolicyCacheTTLEnv, off)
		}
	}
	for raw, want := range map[string]time.Duration{"": DefaultPolicyCacheTTL, "90": 90 * time.Second, "2m": 2 * time.Minute} {
		t.Setenv(PolicyCacheTTLEnv, raw)
		if got := policyCacheTTL(); got != want {
			t.Errorf("%s=%q: TTL %v, want %v", PolicyCacheTTLEnv, raw, got, want)
		}
	}
}

// TestALiveProbeSkipsThePolicyCache pins that a claim or close
// (storage.WithLivePolicyProbe) asks the server even with a fresh entry, and
// refreshes the entry with what the server said.
func TestALiveProbeSkipsThePolicyCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	now := time.Now()
	testPolicyCache(t, path, "prj_a", now).store(true)

	target := Target{BaseURL: mustParseURL(t, "https://example.com"), ExpectProjectID: "prj_a"}
	w := &fakeWire{res: &apigen.ContextResponse{}}
	s := New(target, w, nil)
	s.policy = testPolicyCache(t, path, "prj_a", now)
	if enforced, err := s.ServerEnforcesExternalDependencyPolicy(storage.WithLivePolicyProbe(t.Context())); err != nil || enforced {
		t.Fatalf("live probe = %v, %v; want the server's false despite the cached true", enforced, err)
	}
	if w.hits != 1 {
		t.Fatalf("live probe dialed %d handshakes, want 1", w.hits)
	}
	if enforced, ok := testPolicyCache(t, path, "prj_a", now).load(); !ok || enforced {
		t.Fatalf("cache = %v, %v; want it corrected to false", enforced, ok)
	}
}
