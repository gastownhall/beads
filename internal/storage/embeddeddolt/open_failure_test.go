//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	doltembed "github.com/dolthub/driver/v2"
)

// fakeConnector stands in for the engine connector: connect decides each
// connection attempt and Close returns closeErr.
type fakeConnector struct {
	connect  func(ctx context.Context) error
	closeErr error
}

func (c *fakeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := c.connect(ctx); err != nil {
		return nil, err
	}
	return fakeConn{}, nil
}

func (c *fakeConnector) Driver() driver.Driver { return fakeDriver{} }
func (c *fakeConnector) Close() error          { return c.closeErr }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fakeDriver: use the connector")
}

type fakeConn struct{}

func (fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fakeConn: no statements")
}
func (fakeConn) Close() error              { return nil }
func (fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("fakeConn: no transactions") }

// useFakeConnector routes OpenSQL's engine connector to c for the test.
func useFakeConnector(t *testing.T, c *fakeConnector) {
	t.Helper()
	orig := newEmbeddedConnector
	newEmbeddedConnector = func(doltembed.Config) (embeddedConnector, error) { return c, nil }
	t.Cleanup(func() { newEmbeddedConnector = orig })
}

var openElapsed = regexp.MustCompile(`after [0-9][0-9.]*(ns|µs|ms|s|m|h)`)

// TestOpenSQLFailureContext pins AC-6 of GH#7989: a failed embedded open
// names the requested database location and how long the open ran, and keeps
// the engine's error, the cleanup error and the caller's cancellation, while
// claiming nothing about a lock holder that the engine did not report.
func TestOpenSQLFailureContext(t *testing.T) {
	errCleanup := errors.New("cleanup: connector close failed")

	t.Run("engine failure names the location and elapsed time", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "my beads", "embedded")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		errEngine := errors.New("engine: database open failed")
		useFakeConnector(t, &fakeConnector{
			connect:  func(context.Context) error { return errEngine },
			closeErr: errCleanup,
		})

		_, _, err := OpenSQL(context.Background(), dir, "beads", "")
		if err == nil {
			t.Fatal("OpenSQL succeeded, want the engine failure")
		}
		for _, want := range []error{errEngine, errCleanup} {
			if !errors.Is(err, want) {
				t.Errorf("errors.Is(err, %q) = false: %v", want, err)
			}
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("an uncancelled open reports a context error: %v", err)
		}
		msg := err.Error()
		for _, want := range []string{dir, "beads"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not name %q", msg, want)
			}
		}
		if !openElapsed.MatchString(msg) {
			t.Errorf("error %q does not report the elapsed open time", msg)
		}
		own := strings.ToLower(strings.ReplaceAll(msg, errEngine.Error(), ""))
		for _, claim := range []string{"lock", "holder", "pid"} {
			if strings.Contains(own, claim) {
				t.Errorf("error %q claims %q, which the engine did not report", msg, claim)
			}
		}
	})

	t.Run("caller cancellation during the open is kept", func(t *testing.T) {
		dir := t.TempDir()
		errEngine := errors.New("engine: open interrupted")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		useFakeConnector(t, &fakeConnector{
			connect: func(context.Context) error {
				cancel() // the caller gives up while the engine is still opening
				return errEngine
			},
			closeErr: errCleanup,
		})

		_, _, err := OpenSQL(ctx, dir, "beads", "")
		for _, want := range []error{context.Canceled, errEngine, errCleanup} {
			if !errors.Is(err, want) {
				t.Errorf("errors.Is(err, %q) = false: %v", want, err)
			}
		}
		if err != nil && (!strings.Contains(err.Error(), dir) || !openElapsed.MatchString(err.Error())) {
			t.Errorf("error %q lacks the location or elapsed time", err)
		}
	})

	t.Run("driver-reported holder details stay visible", func(t *testing.T) {
		errHolder := errors.New("database is locked by pid 4242 (dolt sql-server) since 12:00")
		useFakeConnector(t, &fakeConnector{connect: func(context.Context) error { return errHolder }})

		_, _, err := OpenSQL(context.Background(), t.TempDir(), "beads", "")
		if !errors.Is(err, errHolder) {
			t.Fatalf("errors.Is(err, holder error) = false: %v", err)
		}
		if msg := err.Error(); !strings.Contains(msg, errHolder.Error()) || strings.Count(msg, "pid") != 1 {
			t.Errorf("error %q should carry the driver's holder text once and add none", msg)
		}
	})

	t.Run("successful open", func(t *testing.T) {
		useFakeConnector(t, &fakeConnector{connect: func(context.Context) error { return nil }})

		db, cleanup, err := OpenSQL(context.Background(), t.TempDir(), "", "")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		if db == nil || cleanup == nil {
			t.Fatal("OpenSQL returned no database or cleanup")
		}
		if err := cleanup(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
}
