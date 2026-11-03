package uow

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// refusedResolver returns an unarmed resolver for a root with a secret whose
// endpoint refuses, and whose discovery is the real one: with zero OpenOpts it
// fails validation at once, naming OpenOpts, without starting anything.
func refusedResolver(t *testing.T) *proxy.EndpointResolver {
	t.Helper()
	root := t.TempDir()
	if _, err := identity.WriteSecret(root); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ep := proxy.Endpoint{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}
	_ = ln.Close()
	r := proxy.NewEndpointResolver(root, proxy.OpenOpts{}, ep)
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	return r
}

// rediscovered reports whether err carries the discovery failure of a
// refusedResolver, i.e. whether the dial ran discovery.
func rediscovered(err error) bool {
	return err != nil && strings.Contains(err.Error(), "OpenOpts")
}

// TestOpenWithResolver_ConstructionNeverRediscovers: a dial that fails while
// the provider is being built returns its own error to construction, which
// fails as it always has; only a provider that was built arms recovery.
func TestOpenWithResolver_ConstructionNeverRediscovers(t *testing.T) {
	ctx := context.Background()

	r := refusedResolver(t)
	var constructionErr error
	_, err := openWithResolver(ctx, r, func() (UnitOfWorkProvider, error) {
		_, constructionErr = r.DialContext(ctx, proxy.DataNetwork, "")
		return nil, constructionErr
	})
	if !errors.Is(err, syscall.ECONNREFUSED) || proxy.IsEndpointUnavailable(err) || rediscovered(err) {
		t.Fatalf("construction failed with %v, want the dial's own refusal", err)
	}
	if !errors.Is(err, constructionErr) {
		t.Fatalf("construction error %v does not carry the dial error %v", err, constructionErr)
	}

	r = refusedResolver(t)
	if _, err := openWithResolver(ctx, r, func() (UnitOfWorkProvider, error) {
		return &doltSQLProvider{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = r.DialContext(ctx, proxy.DataNetwork, "")
	if !proxy.IsEndpointUnavailable(err) || !rediscovered(err) {
		t.Fatalf("dial after construction: %v, want rediscovery", err)
	}
}
