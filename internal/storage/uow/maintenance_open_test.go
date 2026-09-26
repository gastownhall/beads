package uow

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewExternalDoltServerUOWProvider_NoDatabaseBindOpensWithMissingDatabase
// covers the WithNoDatabaseBind maintenance open used by
// `bd dolt clean-databases` (#5087 review, ask 4): the open must succeed even
// though the configured database does not exist on the server, must not
// create it, and the resulting provider must be able to run server-wide
// statements (SHOW DATABASES) through MaintenanceProvider.RunNonTx.
func TestNewExternalDoltServerUOWProvider_NoDatabaseBindOpensWithMissingDatabase(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The configured database is deliberately absent — the broken state
	// clean-databases exists to recover from.
	provider, err := NewExternalDoltServerUOWProvider(
		ctx,
		storeRootDir,
		"beads_dropped_serverside",
		logPath,
		external,
		"root",
		"",
		0,
		0,
		false,
		"",
		WithNoDatabaseBind(),
	)
	require.NoError(t, err,
		"a no-bind maintenance open must succeed with the configured database missing")
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })

	mp, ok := provider.(MaintenanceProvider)
	require.True(t, ok, "no-bind provider must support maintenance operations, got %T", provider)

	// Server-wide statements work without a bound database.
	var names []string
	require.NoError(t, mp.RunNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, "SHOW DATABASES")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			names = append(names, name)
		}
		return rows.Err()
	}))
	assert.NotEmpty(t, names, "SHOW DATABASES must work on a no-bind connection")

	// The open must not have created (or bound) the configured database.
	admin, err := sql.Open("mysql", fmt.Sprintf("root:@tcp(127.0.0.1:%d)/?parseTime=true", portInt))
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	var name string
	scanErr := admin.QueryRowContext(ctx, "SHOW DATABASES LIKE 'beads_dropped_serverside'").Scan(&name)
	assert.ErrorIs(t, scanErr, sql.ErrNoRows,
		"a no-bind open must not create the configured database")
}
