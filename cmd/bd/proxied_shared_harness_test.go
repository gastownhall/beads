//go:build cgo

package main

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

var (
	sharedProxiedOnce  sync.Once
	sharedProxiedPort  int
	sharedProxiedErr   error
	sharedProxiedDBSeq atomic.Int64
)

func sharedProxiedServerPort(t *testing.T) int {
	t.Helper()
	sharedProxiedOnce.Do(func() {
		if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
			sharedProxiedErr = err
			return
		}
		port := testutil.DoltContainerPortInt()
		if port == 0 {
			sharedProxiedErr = fmt.Errorf("shared dolt container reported no port")
			return
		}
		sharedProxiedPort = port
	})
	if sharedProxiedErr != nil {
		t.Skipf("shared proxied-server unavailable: %v", sharedProxiedErr)
	}
	return sharedProxiedPort
}

func requireSharedProxiedServer(t *testing.T) int {
	t.Helper()
	if os.Getenv("BEADS_TEST_PROXIED_SERVER") != "1" {
		t.Skip("set BEADS_TEST_PROXIED_SERVER=1 to run proxied-server integration tests")
	}
	return sharedProxiedServerPort(t)
}

func uniqueProxiedDatabase() string {
	return fmt.Sprintf("bdtest_%d", sharedProxiedDBSeq.Add(1))
}

func sharedProxiedInitArgs(t *testing.T, extraInitArgs ...string) (string, []string) {
	t.Helper()
	port := requireSharedProxiedServer(t)

	database := uniqueProxiedDatabase()
	args := append([]string{
		"--database", database,
		"--proxied-server-external-host", "127.0.0.1",
		"--proxied-server-external-port", strconv.Itoa(port),
	}, extraInitArgs...)
	return database, args
}

func newSharedProxiedProject(t *testing.T, bd, prefix string, extraInitArgs ...string) proxiedProject {
	t.Helper()
	database, args := sharedProxiedInitArgs(t, extraInitArgs...)
	p := bdProxiedInit(t, bd, prefix, args...)
	p.database = database
	return p
}

func newSharedProxiedProjectWithHooks(t *testing.T, bd, prefix string, hooks map[string]string, extraInitArgs ...string) proxiedProject {
	t.Helper()
	database, args := sharedProxiedInitArgs(t, extraInitArgs...)
	p := bdProxiedInitWithHooks(t, bd, prefix, hooks, args...)
	p.database = database
	return p
}
