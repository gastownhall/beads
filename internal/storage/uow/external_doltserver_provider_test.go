package uow

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewExternalDoltServerUOWProvider_ValidationErrors(t *testing.T) {
	validExt := configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 3306}

	cases := []struct {
		name     string
		database string
		rootUser string
		external configfile.ExternalDoltConfig
		want     string
	}{
		{"empty database", "", "root", validExt, "database name must not be empty"},
		{"empty rootUser", "beads", "", validExt, "rootUser must not be empty"},
		{"empty external config", "beads", "root", configfile.ExternalDoltConfig{}, "external"},
		{"external host without port", "beads", "root", configfile.ExternalDoltConfig{Host: "db"}, "Host requires Port"},
		{"external tls cert without key", "beads", "root", configfile.ExternalDoltConfig{
			Host: "db", Port: 3306, TLSCert: "/etc/beads/client.pem",
		}, "TLSCert set without TLSKey"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewExternalDoltServerUOWProvider(
				context.Background(),
				t.TempDir(),
				tc.database,
				"",
				tc.external,
				tc.rootUser,
				"",
				0,
				0,
			)
			assert.Nil(t, p)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestNewExternalDoltServerUOWProvider_EndToEnd(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	portInt, err := strconv.Atoi(port)
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	logPath := filepath.Join(t.TempDir(), "server.log")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	provider, err := NewExternalDoltServerUOWProvider(
		ctx,
		storeRootDir,
		"beads_test",
		logPath,
		configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: portInt},
		"root",
		"",
		0,
		0,
	)
	require.NoError(t, err)
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })

	sqlProv, ok := provider.(*doltSQLProvider)
	require.True(t, ok, "expected *doltSQLProvider, got %T", provider)

	var one int
	require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT 1").Scan(&one))
	assert.Equal(t, 1, one)

	var dbName string
	require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&dbName))
	assert.Equal(t, "beads_test", dbName)

	_, err = sqlProv.db.ExecContext(ctx, "CREATE TABLE t_external_e2e (id INT PRIMARY KEY, v VARCHAR(64))")
	require.NoError(t, err)
	_, err = sqlProv.db.ExecContext(ctx, "INSERT INTO t_external_e2e (id, v) VALUES (1, 'alpha'), (2, 'beta')")
	require.NoError(t, err)

	var count int
	require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM t_external_e2e").Scan(&count))
	assert.Equal(t, 2, count)

	var v string
	require.NoError(t, sqlProv.db.QueryRowContext(ctx, "SELECT v FROM t_external_e2e WHERE id = 2").Scan(&v))
	assert.Equal(t, "beta", v)
}

func TestNewExternalDoltServerUOWProvider_ConcurrentInstantiation(t *testing.T) {
	port := testutil.StartIsolatedDoltContainer(t)
	portInt, err := strconv.Atoi(port)
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	logPath := filepath.Join(t.TempDir(), "server.log")
	external := configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: portInt}

	const concurrency = 10
	type result struct {
		provider UnitOfWorkProvider
		err      error
	}
	results := make([]result, concurrency)

	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		i := i
		go func() {
			defer wg.Done()
			p, err := NewExternalDoltServerUOWProvider(
				context.Background(),
				storeRootDir,
				"beads_test",
				logPath,
				external,
				"root",
				"",
				0,
				0,
			)
			results[i] = result{provider: p, err: err}
		}()
	}
	wg.Wait()

	t.Cleanup(func() {
		for _, r := range results {
			if r.provider != nil {
				_ = r.provider.Close(context.Background())
			}
		}
	})

	for i, r := range results {
		assert.NoErrorf(t, r.err, "provider %d", i)
		assert.NotNilf(t, r.provider, "provider %d", i)
	}
}
