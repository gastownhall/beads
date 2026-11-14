package externaldeps

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// TestPolicyLayerIsAdvertisedOnTheHandshake closes the loop between this
// package and the server's handshake, the composition `bd serve` performs
// (cmd/bd/serve.go): the policy layer found by AppliedToProvider becomes
// httpapi.Config.ExternalDependencyPolicy, and the handshake must then publish
// httpapi.CapExternalDependencies. A client reads that token to stand its own
// policy down (remote_roles.go), so a server composed through the policy that
// never says so makes every client apply the policy a second time, with the
// CLIENT machine's project config; one that says so without the layer makes
// every client skip it.
func TestPolicyLayerIsAdvertisedOnTheHandshake(t *testing.T) {
	plain := &fakeUOWProvider{}
	for _, tc := range []struct {
		name      string
		provider  uow.UnitOfWorkProvider
		advertise bool
	}{
		{"composed through the policy", WrapUOWProvider(plain, nil, nil), true},
		{"raw provider", plain, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := handshakeCapabilities(t, httpapi.Config{
				Provider:                 tc.provider,
				ExternalDependencyPolicy: AppliedToProvider(tc.provider),
			})
			if got := slices.Contains(caps, httpapi.CapExternalDependencies); got != tc.advertise {
				t.Fatalf("handshake advertises %s = %v, want %v: %v", httpapi.CapExternalDependencies, got, tc.advertise, caps)
			}
		})
	}
}

func handshakeCapabilities(t *testing.T, cfg httpapi.Config) []string {
	t.Helper()
	cfg.Addr = "127.0.0.1:0"
	cfg.Stdout, cfg.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	srv, err := httpapi.Listen(cfg)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("Serve did not return after shutdown")
		}
	})
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get("http://" + srv.Addr() + "/v0/beads/context")
	if err != nil {
		t.Fatalf("GET /v0/beads/context: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v0/beads/context: status %d", resp.StatusCode)
	}
	var body struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode the handshake: %v", err)
	}
	return body.Capabilities
}
