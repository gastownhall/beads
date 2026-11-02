package uow

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

// TestProxyEndpointResolverReplacesOnlyAddress pins what a rediscovered
// connection keeps from the provider's open: everything in the DSN except the
// proxy address, including an external upstream's TLS config and the server
// name it verifies, which names the upstream rather than the local proxy.
func TestProxyEndpointResolverReplacesOnlyAddress(t *testing.T) {
	const tlsName = "uow-test-rediscovery-tls"
	require.NoError(t, mysql.RegisterTLSConfig(tlsName, &tls.Config{ServerName: "db.example.test", MinVersion: tls.VersionTLS12}))
	t.Cleanup(func() { mysql.DeregisterTLSConfig(tlsName) })

	bootstrap := proxy.Endpoint{Host: "127.0.0.1", Port: 4000}
	cfg, err := mysql.ParseDSN(buildDSN(bootstrap, "beads", "operator", "s3cret", tlsName))
	require.NoError(t, err)
	require.NotNil(t, cfg.TLS)

	r := newProxyEndpointResolver(func() (proxy.Endpoint, error) {
		return proxy.Endpoint{Host: "127.0.0.1", Port: 4321}, nil
	})
	r.arm()
	got := cfg.Clone()
	require.NoError(t, r.beforeConnect(context.Background(), got))

	want := cfg.Clone()
	want.Addr = "127.0.0.1:4321"
	assert.Equal(t, want.FormatDSN(), got.FormatDSN())
	assert.Equal(t, "beads", got.DBName)
	assert.Equal(t, "operator", got.User)
	assert.Equal(t, "s3cret", got.Passwd)
	assert.Equal(t, 5*time.Second, got.Timeout)
	assert.True(t, got.ClientFoundRows)
	require.NotNil(t, got.TLS)
	assert.Equal(t, "db.example.test", got.TLS.ServerName)
	assert.Equal(t, "127.0.0.1:4000", cfg.Addr, "the provider's own config must not change")
}
