package main

// Create-policy wiring tests for the proxied-server open path
// (gastownhall/beads#5079 / #2189): bd init is the only command whose open
// may create the configured database; ordinary command dispatch opens with
// create disabled; `bd dolt clean-databases` opens server-wide with no
// database bind at all. The storage-layer behavior of each option is tested
// in internal/storage/uow (Docker); these tests pin the cmd/bd wiring — which
// option each call site actually passes — without Docker or a server, via the
// openProxiedServerUOWProviderFn seam. That keeps the policy from silently
// regressing when uow_factory.go constructor signatures are reshaped (e.g.
// by #4303).

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/uow"
)

type capturedProxiedOpen struct {
	databaseOverride string
	posture          identityPosture
	opts             []uow.ProviderOption
}

var errProxiedPolicySentinel = errors.New("proxied create-policy test: open intercepted")

// interceptProxiedOpens swaps the openProxiedServerUOWProviderFn seam for a
// fake that records every proxied open (posture + options) and fails with a
// sentinel instead of touching any server.
func interceptProxiedOpens(t *testing.T) *[]capturedProxiedOpen {
	t.Helper()
	var captured []capturedProxiedOpen
	orig := openProxiedServerUOWProviderFn
	openProxiedServerUOWProviderFn = func(ctx context.Context, beadsDir, databaseOverride string, posture identityPosture, opts ...uow.ProviderOption) (uow.UnitOfWorkProvider, error) {
		captured = append(captured, capturedProxiedOpen{
			databaseOverride: databaseOverride,
			posture:          posture,
			opts:             opts,
		})
		return nil, errProxiedPolicySentinel
	}
	t.Cleanup(func() { openProxiedServerUOWProviderFn = orig })
	// Root dispatch in a proxied workspace sets the proxiedServerMode global
	// (and saveAndRestoreGlobals does not cover it); a leaked true diverts
	// every later test's PersistentPostRunE away from the maintenance block.
	origProxied := proxiedServerMode
	t.Cleanup(func() { proxiedServerMode = origProxied })
	return &captured
}

// chdirTemp moves the test into a fresh temp dir (restored on cleanup) and
// isolates HOME so init/config discovery cannot touch the real user config.
func chdirTemp(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	return dir
}

// runInterceptedProxiedInit drives the real `bd init --proxied-server` code
// path in the current directory up to (and into) its provider open, which the
// seam intercepts. The .beads scaffolding (metadata.json in proxied-server
// mode, config.yaml, client info) is fully written before the open, so the
// directory is a valid proxied workspace afterwards even though init exits on
// the sentinel.
func runInterceptedProxiedInit(t *testing.T) error {
	t.Helper()
	return runInitProxiedServer(&cobra.Command{}, context.Background(), initProxiedServerInput{
		prefix:         "pcp",
		quiet:          true,
		skipHooks:      true,
		skipAgents:     true,
		nonInteractive: true,
	})
}

func TestProxiedCreatePolicy_InitOpensWithCreateEnabled(t *testing.T) {
	captured := interceptProxiedOpens(t)
	chdirTemp(t)

	err := runInterceptedProxiedInit(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), errProxiedPolicySentinel.Error(),
		"init must fail on the intercepted open, not before reaching it")

	require.Len(t, *captured, 1, "init performs exactly one proxied open")
	got := (*captured)[0]
	assert.True(t, uow.CreateIfMissingForTest(got.opts...),
		"bd init is the only command whose open may create the database (#2189)")
	assert.False(t, uow.NoDatabaseBindForTest(got.opts...),
		"init must bind and migrate the database it creates")
	assert.Equal(t, adoptWorkspaceIdentity, got.posture,
		"init adopts the identity the shared database carries")
}

func TestProxiedCreatePolicy_OrdinaryCommandOpensWithCreateDisabled(t *testing.T) {
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)
	captured := interceptProxiedOpens(t)
	chdirTemp(t)

	// Scaffold a real proxied workspace via the intercepted init, then clear
	// the capture so only the ordinary command's open is asserted below.
	require.Error(t, runInterceptedProxiedInit(t))
	require.Len(t, *captured, 1, "workspace scaffolding init performs one open")
	*captured = nil

	rootCmd.SetArgs([]string{"list"})
	execErr := rootCmd.Execute()
	rootCmd.SetArgs(nil)
	require.Error(t, execErr,
		"the root dispatch must fail on the intercepted open, proving it went through the seam")

	require.Len(t, *captured, 1, "root dispatch performs exactly one proxied open")
	got := (*captured)[0]
	assert.False(t, uow.CreateIfMissingForTest(got.opts...),
		"ordinary commands must never create a missing database; they fail with not-found (#2189)")
	assert.False(t, uow.NoDatabaseBindForTest(got.opts...),
		"ordinary commands bind to the configured database")
	assert.Equal(t, assertWorkspaceIdentity, got.posture,
		"ordinary commands assert the workspace's project identity")
	assert.Empty(t, got.databaseOverride,
		"no --db/--database flag was passed, so no override reaches the open")
}

func TestProxiedCreatePolicy_CleanDatabasesOpensServerWideWithoutCreate(t *testing.T) {
	captured := interceptProxiedOpens(t)

	err := runDoltCleanDatabasesProxied(context.Background(), t.TempDir(), cleanDatabasesOptions{dryRun: true})
	require.Error(t, err,
		"clean-databases must fail on the intercepted open, proving it went through the seam")

	require.Len(t, *captured, 1, "clean-databases performs exactly one proxied open")
	got := (*captured)[0]
	assert.True(t, uow.NoDatabaseBindForTest(got.opts...),
		"clean-databases opens server-wide (no USE/bind), so it works even when the configured database was dropped")
	assert.False(t, uow.CreateIfMissingForTest(got.opts...),
		"clean-databases must never (re)create the configured database")
	assert.Equal(t, adoptWorkspaceIdentity, got.posture,
		"server-wide maintenance has no single project identity to assert")
	assert.Empty(t, got.databaseOverride,
		"maintenance is server-scoped; it never targets an override database")
}
