//go:build cgo

package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
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
		if os.Getenv(testutil.EnvRequireDoltContainer) == "1" {
			t.Fatalf("shared proxied-server unavailable: %v, but %s=1; this lane must not skip", sharedProxiedErr, testutil.EnvRequireDoltContainer)
		}
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

// sharedProxiedCredential is who a shared-server project's `bd init` (and so
// its proxy) connects to the shard's dolt sql-server as.
type sharedProxiedCredential int

const (
	// sharedProxiedScopedUser: the default. The database is provisioned
	// server-side and the project connects as a user that can see only it
	// (see provisionSharedProxiedDatabase).
	sharedProxiedScopedUser sharedProxiedCredential = iota
	// sharedProxiedRoot: the project connects as root and `bd init` creates
	// the database itself. Only for tests that need a server-wide privilege
	// (CALL DOLT_REMOTE, creating another database); each such init pays for
	// every database on the server.
	sharedProxiedRoot
)

func sharedProxiedInitArgs(t *testing.T, cred sharedProxiedCredential, extraInitArgs ...string) (string, []string) {
	t.Helper()
	port := requireSharedProxiedServer(t)

	database := uniqueProxiedDatabase()
	args := []string{
		"--database", database,
		"--proxied-server-external-host", "127.0.0.1",
		"--proxied-server-external-port", strconv.Itoa(port),
	}
	if cred == sharedProxiedScopedUser {
		provisionSharedProxiedDatabase(t, port, database)
		args = append(args, "--proxied-server-external-user", database)
	}
	return database, append(args, extraInitArgs...)
}

// provisionSharedProxiedDatabase provisions database on the shard's shared
// dolt sql-server the way a server that provisions databases server-side
// does: root creates the empty database and a passwordless user of the same
// name with ALL on it and nothing else, and the project connects as that user
// (--proxied-server-external-user).
//
// The scoping is what keeps a shard's inits from slowing each other down.
// Dolt answers an INFORMATION_SCHEMA query by walking every database the
// session can see, and `bd init`'s migration chain issues ~220 of them, so as
// root each init cost the server ~1.2 CPU-seconds more per database already on
// it: a shard's k-th init paid for the k-1 projects before it. A user that can
// see one database pays for one. Dropping finished projects' databases instead
// is not an option: a DROP DATABASE fails other sessions' in-flight
// INFORMATION_SCHEMA queries ("no root value found in session").
func provisionSharedProxiedDatabase(t *testing.T, port int, database string) {
	t.Helper()
	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("provisioning %s: opening the shared server: %v", database, err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// database is uniqueProxiedDatabase's bdtest_<n>: safe to splice.
	for _, stmt := range []string{
		fmt.Sprintf("CREATE DATABASE `%s`", database),
		fmt.Sprintf("CREATE USER '%s'@'%%'", database),
		fmt.Sprintf("GRANT ALL ON `%s`.* TO '%s'@'%%'", database, database),
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("provisioning %s: %s: %v", database, stmt, err)
		}
	}
}

func newSharedProxiedProject(t *testing.T, bd, prefix string, extraInitArgs ...string) proxiedProject {
	t.Helper()
	database, args := sharedProxiedInitArgs(t, sharedProxiedScopedUser, extraInitArgs...)
	p := bdProxiedInit(t, bd, prefix, args...)
	p.database = database
	return p
}

// newSharedProxiedRootProject is newSharedProxiedProject connecting as root
// (sharedProxiedRoot), for a test that needs a server-wide privilege.
func newSharedProxiedRootProject(t *testing.T, bd, prefix string, extraInitArgs ...string) proxiedProject {
	t.Helper()
	database, args := sharedProxiedInitArgs(t, sharedProxiedRoot, extraInitArgs...)
	p := bdProxiedInit(t, bd, prefix, args...)
	p.database = database
	return p
}

func newSharedProxiedProjectWithHooks(t *testing.T, bd, prefix string, hooks map[string]string, extraInitArgs ...string) proxiedProject {
	t.Helper()
	database, args := sharedProxiedInitArgs(t, sharedProxiedScopedUser, extraInitArgs...)
	p := bdProxiedInitWithHooks(t, bd, prefix, hooks, args...)
	p.database = database
	return p
}
