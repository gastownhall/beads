//go:build cgo

package httpclient

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
	"github.com/steveyegge/beads/internal/versionedhistory"
)

// The PROVIDER source of the served tier (BEADS_HTTP_SERVED_SOURCE=provider;
// served_source_switch_test.go): what production `bd serve` runs for every
// Dolt server-mode workspace (cmd/bd/serve.go). The server gets
// httpapi.Config{Provider: p}, and every request builds its roles through
// httpapi's per-request timedProvider over
//
//	externaldeps.WrapUOWProvider          (cmd/bd/storage_chain.go)
//	  versionedhistory.ActivateProvider   (activateVersionedHistoryProvider)
//	    uow.NewExternalDoltServerUOWProvider
//	      bd db-proxy-child --backend external
//	        the local dolt sql-server (internal/testutil)
//
// The External constructor, not the managed NewDoltServerUOWProvider, because
// serve describes a server-mode topology as external without exception
// (cmd/bd/uow_factory.go), and because it lets every case share testutil's one
// server (BEADS_TEST_DOLT_SERVER=local).
//
// SEEDING never goes through the client (served_harness_test.go). Here the
// seeding side is a server-mode dolt.DoltStore on the same database, and the
// raw hooks get a plain MySQL handle on it.
//
// One database per case, named by servedProviderDatabase, so count-delta cases
// are isolated from each other, and DROPPED when the case ends. The drop is
// load-bearing, not tidiness: the migration chain that bootstraps each case's
// database queries INFORMATION_SCHEMA, which Dolt answers by walking every
// database the server holds, so with no drop each case's bootstrap grows by
// about 3s per earlier case (measured: 8s for a shard's first case, 180s for
// its 43rd), and a shard's run grows with the square of its case count. The
// drops are serial and one per case, not the rapid parallel CREATE/DROP storm
// internal/storage/dolt/dolt_test.go avoids.

// servedProviderProxyIdle is the db-proxy-child's idle timeout. Production runs
// proxy.IdleTimeoutNever (cmd/bd/uow_factory.go), but the child is
// setsid-detached, so a test run that is killed before TestMain's shutdown
// would leave a never-idle proxy reparented to init indefinitely. Two minutes
// is far longer than any gap between requests within a run, and lets an
// orphan retire on its own.
const servedProviderProxyIdle = 2 * time.Minute

// providerReference is the provider source's seeding store. It forwards the
// two per-instance switches a case flips on the reference to the provider as
// well: on the roles source the reference IS the store the server serves, so
// flipping it reaches the server; here the server answers from the provider's
// own switches.
type providerReference struct {
	*dolt.DoltStore
	server uow.UnitOfWorkProvider
}

var _ servedReference = providerReference{}

func (r providerReference) SetEventsJournalEnabled(enabled bool) {
	r.DoltStore.SetEventsJournalEnabled(enabled)
	if c, ok := r.server.(storage.EventsJournalConfigurer); ok {
		c.SetEventsJournalEnabled(enabled)
	}
}

func (r providerReference) SetVersionedHistoryEnabled(enabled bool) {
	r.DoltStore.SetVersionedHistoryEnabled(enabled)
	if c, ok := r.server.(storage.VersionedHistoryConfigurer); ok {
		c.SetVersionedHistoryEnabled(enabled)
	}
}

// The provider source's per-process state: one bd binary for the proxy child
// and one proxy root every case's provider adopts. The External-backend proxy
// serves any database, so one proxy serves the whole run.
var (
	servedProviderOnce  sync.Once
	servedProviderRoot  string
	servedProviderBDDir string
	servedProviderErr   error
)

// servedProviderProcess prepares the per-process state once. The proxy child is
// `bd db-proxy-child`, so the test binary cannot spawn itself: under Bazel the
// bd comes from BEADS_TEST_BD_BINARY (//cmd/bd:bd_for_tests), and a plain
// `go test` builds one.
//
// proxy.ResolveExecutable is set process-wide and never restored: every
// provider in this process must spawn that bd, and nothing else in the package
// spawns a proxy.
func servedProviderProcess() (string, error) {
	servedProviderOnce.Do(func() {
		bd, err := bazeltest.PrebuiltBD()
		if err != nil {
			servedProviderErr = err
			return
		}
		if bd == "" {
			dir, err := os.MkdirTemp("", "httpclient-served-bd-")
			if err != nil {
				servedProviderErr = err
				return
			}
			servedProviderBDDir = dir
			bd = filepath.Join(dir, "bd")
			build := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", bd, "github.com/steveyegge/beads/cmd/bd")
			if out, err := build.CombinedOutput(); err != nil {
				servedProviderErr = fmt.Errorf("go build bd for the proxy child: %w\n%s", err, out)
				return
			}
		}
		proxy.ResolveExecutable = func() (string, error) { return bd, nil }
		root, err := os.MkdirTemp("", "httpclient-served-provider-")
		if err != nil {
			servedProviderErr = err
			return
		}
		servedProviderRoot = root
	})
	return servedProviderRoot, servedProviderErr
}

// stopServedProviderSource is TestMain's teardown of the provider source: the
// shared proxy, its root, a go-built bd, and the local dolt sql-server. Each is
// a no-op when the run never used the provider source.
func stopServedProviderSource() {
	if servedProviderRoot != "" {
		if err := proxy.Shutdown(servedProviderRoot); err != nil {
			fmt.Fprintf(os.Stderr, "httpclient: stop the served provider source's db-proxy-child "+
				"(it retires itself after %v idle): %v\n", servedProviderProxyIdle, err)
		}
		_ = os.RemoveAll(servedProviderRoot)
	}
	if servedProviderBDDir != "" {
		_ = os.RemoveAll(servedProviderBDDir)
	}
	testutil.TerminateDoltContainer()
}

// servedProviderDatabase names a case's database: hprov_<prefix>_<12 hex>. The
// prefix is lowercased, anything outside [a-z0-9] becomes '_', and it is capped
// at 24 characters, which keeps the name far under MySQL's 64 and outside the
// dolt test-database firewall's prefixes.
func servedProviderDatabase(prefix string) string {
	tag := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return '_'
	}, prefix)
	if len(tag) > 24 {
		tag = tag[:24]
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	return "hprov_" + tag + "_" + hex.EncodeToString(suffix)
}

// servedProviderSource is one case's provider-source composition.
type servedProviderSource struct {
	reference providerReference
	provider  uow.UnitOfWorkProvider
	sqlDB     *sql.DB
	database  string
}

// newServedProviderSource builds one case's provider, seeding store and raw
// handle on a fresh database.
func newServedProviderSource(t *testing.T, prefix string) *servedProviderSource {
	t.Helper()
	testutil.RequireDoltContainer(t)
	port := testutil.DoltContainerPortInt()
	root, err := servedProviderProcess()
	if err != nil {
		t.Fatalf("prepare the provider source: %v", err)
	}
	ctx := t.Context()
	database := servedProviderDatabase(prefix)
	// Registered first, so it runs last: after the server has drained and the
	// provider, the seeding store and the raw handle are closed.
	t.Cleanup(func() { dropServedProviderDatabase(t, port, database) })

	raw, err := uow.NewExternalDoltServerUOWProvider(ctx, root, database, filepath.Join(root, "server.log"),
		configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: port}, "root", "", 0, servedProviderProxyIdle, false, "")
	if err != nil {
		t.Fatalf("open the unit-of-work provider on %s: %v", database, err)
	}
	// Registered before the server's cleanup, so it runs after the server has
	// drained its requests.
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = raw.Close(closeCtx)
	})
	// Production parity with cmd/bd's activateVersionedHistoryProvider.
	// Events-journal activation is not applied: on a fresh workspace it is
	// equivalent to the default off, and the server turns the journal on through
	// EventsJournalEnabled below.
	raw, err = versionedhistory.ActivateProvider(ctx, raw, nil)
	if err != nil {
		t.Fatalf("activate versioned history on %s: %v", database, err)
	}
	provider := externaldeps.WrapUOWProvider(raw,
		func(externaldeps.ProjectName) (string, bool) { return "", false },
		func(context.Context, string) (storage.DoltStorage, error) {
			return nil, errors.New("the provider source configures no external projects")
		})

	store, err := dolt.New(ctx, &dolt.Config{
		Path:         t.TempDir(),
		ServerHost:   "127.0.0.1",
		ServerPort:   port,
		Database:     database,
		MaxOpenConns: 4,
	})
	if err != nil {
		t.Fatalf("open the seeding store on %s: %v", database, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	reference := providerReference{DoltStore: store, server: provider}
	if err := reference.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		t.Fatalf("set the issue prefix: %v", err)
	}
	// Commit the setup write, as a workspace `bd init` committed would be.
	// Server-mode config writes stay uncommitted (dolt/store.go, GH#2455), and
	// the provider's '-Am' commits (uow/doltserver_tx.go) would otherwise sweep
	// that pending config into a case's first write: a ready claim that wins an
	// ephemeral row would record a version-control entry the roles source does
	// not, and TestServedReadyClaimerClaimsAnEphemeralRowTheFilterAdmits fails
	// "history entries went N -> N+1".
	if err := reference.CommitWithConfig(ctx, "httpclient: settle the provider source's workspace"); err != nil {
		t.Fatalf("commit the provider source's workspace setup: %v", err)
	}

	db, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/%s?parseTime=true", port, database))
	if err != nil {
		t.Fatalf("open the raw handle on %s: %v", database, err)
	}
	// No idle connections: every hook call gets a fresh session, as the roles
	// source's per-call embedded handle does, so session state an exec script
	// leaves behind cannot leak into the next hook.
	db.SetMaxIdleConns(0)
	t.Cleanup(func() { _ = db.Close() })

	return &servedProviderSource{reference: reference, provider: provider, sqlDB: db, database: database}
}

// dropServedProviderDatabase drops a case's database once nothing uses it
// (see the file comment for why every case's database is dropped). A failed
// drop is only logged: the drop keeps later cases' bootstrap flat, it is no
// part of any contract, and the gated lanes do not retry, so a transient Dolt
// error at teardown must not fail a case that passed. A server that stops
// dropping still shows: the later cases slow down until -test.timeout fires.
func dropServedProviderDatabase(t *testing.T, port int, database string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/", port))
	if err != nil {
		t.Logf("open an admin handle to drop %s: %v", database, err)
		return
	}
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+database+"`"); err != nil {
		t.Logf("drop the provider source's database %s: %v", database, err)
	}
}

// config is the provider source's served Config, as cmd/bd/serve.go builds it
// for a server-mode workspace.
func (p *servedProviderSource) config() httpapi.Config {
	return httpapi.Config{
		Addr:   "127.0.0.1:0",
		Stdout: io.Discard,
		Stderr: io.Discard,
		// The handshake's published identity, as serveConfig publishes it.
		Workspace: domain.ContextInfo{ProjectID: servedProjectID, Database: p.database},
		Provider:  p.provider,
		// On for the reason serveConfig turns it on: the journal cases read
		// what the server wrote.
		EventsJournalEnabled:     true,
		ExternalDependencyPolicy: externaldeps.AppliedToProvider(p.provider),
	}
}
