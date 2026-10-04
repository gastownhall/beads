//go:build cgo

package embeddeddolt

import (
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	doltembed "github.com/dolthub/driver/v2"
)

func TestOpenSQLReadOnlyExhaustionStopsConcurrentConnectorInitialization(t *testing.T) {
	// Two callers are the minimum that proves concurrent behavior.
	const callers = 2
	var connectorCalls atomic.Int32
	probe := func(string) (bool, error) { return false, nil }
	connector := func(doltembed.Config) (*doltembed.Connector, error) {
		connectorCalls.Add(1)
		return nil, errors.New("connector must not be initialized")
	}

	dir := t.TempDir()
	ctx := t.Context()
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := openSQL(ctx, dir, "beads", "main", probe, connector)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		var fullErr *FilesystemFullError
		if !errors.As(err, &fullErr) {
			t.Fatalf("openSQL error = %v, want *FilesystemFullError", err)
		}
		if fullErr.Path != dir {
			t.Fatalf("FilesystemFullError.Path = %q, want %q", fullErr.Path, dir)
		}
		if got, want := err.Error(), "embedded Dolt read-only open stopped: no filesystem space is available; free space and retry"; got != want {
			t.Fatalf("openSQL error = %q, want %q", got, want)
		}
		if !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("openSQL error = %v, want syscall.ENOSPC", err)
		}
	}
	if got := connectorCalls.Load(); got != 0 {
		t.Fatalf("connector initialization calls = %d, want 0", got)
	}
}

func TestOpenSQLReadOnlyHealthySpaceReachesConnector(t *testing.T) {
	wantErr := errors.New("connector reached")
	called := false
	_, _, err := openSQL(
		t.Context(),
		t.TempDir(),
		"beads",
		"main",
		func(string) (bool, error) { return true, nil },
		func(doltembed.Config) (*doltembed.Connector, error) {
			called = true
			return nil, wantErr
		},
	)
	if !called {
		t.Fatal("healthy read-only open did not reach connector initialization")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("openSQL error = %v, want %v", err, wantErr)
	}
}

func TestOpenSQLWritableSkipsSpacePreflight(t *testing.T) {
	wantErr := errors.New("connector reached")
	called := false
	_, _, err := openSQL(
		t.Context(),
		t.TempDir(),
		"beads",
		"main",
		nil,
		func(doltembed.Config) (*doltembed.Connector, error) {
			called = true
			return nil, wantErr
		},
	)
	if !called {
		t.Fatal("writable open did not reach connector initialization")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("openSQL error = %v, want %v", err, wantErr)
	}
}
