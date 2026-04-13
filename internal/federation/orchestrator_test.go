package federation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// mockPusher records PushTo/PullFrom calls and returns configured errors.
type mockPusher struct {
	pushCalls []string // remote names pushed to
	pullCalls []string // remote names pulled from
	pushErrs  map[string]error
	pullErrs  map[string]error
	pushDelay time.Duration
}

func newMockPusher() *mockPusher {
	return &mockPusher{
		pushErrs: make(map[string]error),
		pullErrs: make(map[string]error),
	}
}

func (m *mockPusher) PushTo(_ context.Context, peer string) error {
	m.pushCalls = append(m.pushCalls, peer)
	if m.pushDelay > 0 {
		time.Sleep(m.pushDelay)
	}
	return m.pushErrs[peer]
}

func (m *mockPusher) PullFrom(_ context.Context, peer string) ([]storage.Conflict, error) {
	m.pullCalls = append(m.pullCalls, peer)
	return nil, m.pullErrs[peer]
}

var testRemotes = []config.RemoteConfig{
	{Name: "primary", URL: "dolthub://org/beads", Role: config.RemoteRolePrimary},
	{Name: "backup", URL: "az://account.blob.core.windows.net/c/b", Role: config.RemoteRoleBackup},
}

func newTestOrch(t *testing.T, mock RemotePusher, remotes []config.RemoteConfig, opts ...Option) *SyncOrchestrator {
	t.Helper()
	beadsDir := t.TempDir()
	defaults := []Option{
		WithRetryInterval(1 * time.Millisecond), // fast retries in tests
	}
	return New(mock, remotes, beadsDir, append(defaults, opts...)...)
}

func TestPushAll_AllSuccess(t *testing.T) {
	mock := newMockPusher()
	orch := newTestOrch(t, mock, testRemotes)

	result, err := orch.PushAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.PrimaryOK() {
		t.Error("PrimaryOK() = false, want true")
	}
	if !result.AllOK() {
		t.Error("AllOK() = false, want true")
	}
	if len(result.Results) != 2 {
		t.Fatalf("len(Results) = %d, want 2", len(result.Results))
	}
	// Primary pushed first
	if mock.pushCalls[0] != "primary" {
		t.Errorf("first push = %q, want %q", mock.pushCalls[0], "primary")
	}
	if mock.pushCalls[1] != "backup" {
		t.Errorf("second push = %q, want %q", mock.pushCalls[1], "backup")
	}
}

func TestPushAll_PrimaryFailsPermanent(t *testing.T) {
	mock := newMockPusher()
	mock.pushErrs["primary"] = errors.New("bucket not found")
	orch := newTestOrch(t, mock, testRemotes, WithIsRetryable(func(error) bool { return false }))

	result, err := orch.PushAll(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if result.PrimaryOK() {
		t.Error("PrimaryOK() = true after primary failure")
	}
	// Backup should be skipped, not attempted
	for _, r := range result.Results {
		if r.Remote.Name == "backup" && r.Status != PushStatusSkipped {
			t.Errorf("backup status = %q, want %q", r.Status, PushStatusSkipped)
		}
	}
	// Only primary was attempted
	if len(mock.pushCalls) != 1 {
		t.Errorf("pushCalls = %d, want 1 (only primary)", len(mock.pushCalls))
	}
}

func TestPushAll_PrimarySucceedsBackupFails(t *testing.T) {
	mock := newMockPusher()
	mock.pushErrs["backup"] = errors.New("access denied")
	orch := newTestOrch(t, mock, testRemotes, WithIsRetryable(func(error) bool { return false }))

	result, err := orch.PushAll(context.Background())
	if !errors.Is(err, ErrDegradedSync) {
		t.Fatalf("expected ErrDegradedSync, got: %v", err)
	}
	if !result.PrimaryOK() {
		t.Error("PrimaryOK() = false, want true")
	}
	if result.AllOK() {
		t.Error("AllOK() = true, want false")
	}
	failed := result.FailedRemotes()
	if len(failed) != 1 || failed[0] != "backup" {
		t.Errorf("FailedRemotes() = %v, want [backup]", failed)
	}
}

func TestPushAll_PrimaryTransientThenSuccess(t *testing.T) {
	var callCount atomic.Int32
	mock := &countingPusher{
		callCount: &callCount,
		failUntil: 2, // fail first 2 attempts, succeed on 3rd
		failErr:   errors.New("connection reset by peer"),
		failPeer:  "primary",
	}
	orch := newTestOrch(t, mock, testRemotes[:1], // just primary
		WithMaxRetries(3),
		WithIsRetryable(func(err error) bool { return err.Error() == "connection reset by peer" }),
	)

	result, err := orch.PushAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.PrimaryOK() {
		t.Error("PrimaryOK() = false after retry success")
	}
	if result.Results[0].Retries != 2 {
		t.Errorf("Retries = %d, want 2", result.Results[0].Retries)
	}
}

func TestPushAll_AllRetriesExhausted(t *testing.T) {
	mock := newMockPusher()
	mock.pushErrs["primary"] = errors.New("connection timeout")
	orch := newTestOrch(t, mock, testRemotes[:1],
		WithMaxRetries(2),
		WithIsRetryable(func(error) bool { return true }),
	)

	result, err := orch.PushAll(context.Background())
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if result.Results[0].Retries != 2 {
		t.Errorf("Retries = %d, want 2", result.Results[0].Retries)
	}
	if result.Results[0].Status != PushStatusFailed {
		t.Errorf("Status = %q, want %q", result.Results[0].Status, PushStatusFailed)
	}
}

func TestPushAll_NoPrimary(t *testing.T) {
	mock := newMockPusher()
	backupOnly := []config.RemoteConfig{
		{Name: "backup", URL: "az://x", Role: config.RemoteRoleBackup},
	}
	orch := newTestOrch(t, mock, backupOnly)

	_, err := orch.PushAll(context.Background())
	if !errors.Is(err, ErrNoPrimary) {
		t.Fatalf("expected ErrNoPrimary, got: %v", err)
	}
}

func TestPushAll_ContextCancelled(t *testing.T) {
	mock := newMockPusher()
	mock.pushDelay = 50 * time.Millisecond

	remotes := []config.RemoteConfig{
		{Name: "primary", URL: "dolthub://a/b", Role: config.RemoteRolePrimary},
		{Name: "backup1", URL: "az://x", Role: config.RemoteRoleBackup},
		{Name: "backup2", URL: "gs://y", Role: config.RemoteRoleBackup},
	}
	orch := newTestOrch(t, mock, remotes)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after primary succeeds but before backups
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	result, _ := orch.PushAll(ctx)
	if !result.PrimaryOK() {
		t.Error("PrimaryOK() should be true")
	}
	// At least one backup should be skipped due to cancellation
	skipped := 0
	for _, r := range result.Results {
		if r.Status == PushStatusSkipped {
			skipped++
		}
	}
	if skipped == 0 {
		t.Error("expected at least one skipped backup due to context cancellation")
	}
}

func TestPull_RoutesToPrimary(t *testing.T) {
	mock := newMockPusher()
	orch := newTestOrch(t, mock, testRemotes)

	_, err := orch.Pull(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.pullCalls) != 1 || mock.pullCalls[0] != "primary" {
		t.Errorf("pullCalls = %v, want [primary]", mock.pullCalls)
	}
}

func TestPull_NoPrimary(t *testing.T) {
	mock := newMockPusher()
	backupOnly := []config.RemoteConfig{
		{Name: "backup", URL: "az://x", Role: config.RemoteRoleBackup},
	}
	orch := newTestOrch(t, mock, backupOnly)

	_, err := orch.Pull(context.Background())
	if !errors.Is(err, ErrNoPrimary) {
		t.Fatalf("expected ErrNoPrimary, got: %v", err)
	}
}

func TestPushAll_LockPreventsConccurrent(t *testing.T) {
	mock := newMockPusher()
	mock.pushDelay = 100 * time.Millisecond
	beadsDir := t.TempDir()

	orch1 := New(mock, testRemotes, beadsDir, WithRetryInterval(1*time.Millisecond))
	orch2 := New(mock, testRemotes, beadsDir, WithRetryInterval(1*time.Millisecond))

	// Start first push
	done := make(chan error, 1)
	go func() {
		_, err := orch1.PushAll(context.Background())
		done <- err
	}()

	// Give orch1 time to acquire lock
	time.Sleep(20 * time.Millisecond)

	// Second push should fail with lock error
	_, err := orch2.PushAll(context.Background())
	if !errors.Is(err, ErrLockHeld) {
		t.Errorf("expected ErrLockHeld, got: %v", err)
	}

	// First push should complete
	if err := <-done; err != nil {
		t.Errorf("first push failed: %v", err)
	}
}

func TestPushAll_BackupOrderIsDeterministic(t *testing.T) {
	mock := newMockPusher()
	remotes := []config.RemoteConfig{
		{Name: "zeta-backup", URL: "gs://z", Role: config.RemoteRoleBackup},
		{Name: "primary", URL: "dolthub://a/b", Role: config.RemoteRolePrimary},
		{Name: "alpha-backup", URL: "az://a", Role: config.RemoteRoleBackup},
	}
	orch := newTestOrch(t, mock, remotes)

	_, err := orch.PushAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"primary", "alpha-backup", "zeta-backup"}
	if len(mock.pushCalls) != len(expected) {
		t.Fatalf("pushCalls = %v, want %v", mock.pushCalls, expected)
	}
	for i, name := range expected {
		if mock.pushCalls[i] != name {
			t.Errorf("pushCalls[%d] = %q, want %q", i, mock.pushCalls[i], name)
		}
	}
}

func TestDefaultIsRetryable(t *testing.T) {
	tests := []struct {
		err       string
		retryable bool
	}{
		{"connection reset by peer", true},
		{"i/o timeout", true},
		{"connection refused", true},
		{"broken pipe", true},
		{"lost connection to server", true},
		{"bucket not found", false},
		{"access denied", false},
		{"permission denied", false},
		{"syntax error", false},
	}
	for _, tc := range tests {
		got := defaultIsRetryable(errors.New(tc.err))
		if got != tc.retryable {
			t.Errorf("defaultIsRetryable(%q) = %v, want %v", tc.err, got, tc.retryable)
		}
	}
}

// countingPusher fails the first N attempts for a specific peer, then succeeds.
type countingPusher struct {
	callCount *atomic.Int32
	failUntil int32 // fail attempts 0..failUntil-1
	failErr   error
	failPeer  string
}

func (p *countingPusher) PushTo(_ context.Context, peer string) error {
	if peer == p.failPeer {
		n := p.callCount.Add(1)
		if n <= p.failUntil {
			return p.failErr
		}
	}
	return nil
}

func (p *countingPusher) PullFrom(_ context.Context, _ string) ([]storage.Conflict, error) {
	return nil, nil
}
