package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// readTimeoutListStore is the slice of a store runListCore reads on the direct
// route before the page: list config, routing config and the reader, whose
// List fails with listErr. Every other method is the nil embedded interface
// and panics if the command reaches it.
type readTimeoutListStore struct {
	storage.DoltStorage
	listErr error
}

func (s readTimeoutListStore) GetCustomStatusesDetailed(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}
func (s readTimeoutListStore) GetCustomTypes(context.Context) ([]string, error) { return nil, nil }
func (s readTimeoutListStore) GetInfraTypes(context.Context) map[string]bool   { return nil }
func (s readTimeoutListStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (s readTimeoutListStore) IssueReader() (issueops.Reader, error) {
	return readTimeoutListReader{err: s.listErr}, nil
}

type readTimeoutListReader struct {
	issueops.Reader
	err error
}

func (r readTimeoutListReader) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, r.err
}

// captureListStdio runs fn with os.Stdout and os.Stderr redirected, returning
// both and fn's error.
func captureListStdio(t *testing.T, fn func() error) (stdout, stderr string, err error) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	outDone, errDone := make(chan string, 1), make(chan string, 1)
	drain := func(r *os.File, done chan<- string) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}
	go drain(outR, outDone)
	go drain(errR, errDone)
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	err = fn()

	_ = outW.Close()
	_ = errW.Close()
	stdout, stderr = <-outDone, <-errDone
	_ = outR.Close()
	_ = errR.Close()
	return stdout, stderr, err
}

// TestListReadTimeoutStderrContract pins what `bd list` does when its page
// read stops at the pool read deadline (GH#5258): with or without --json it
// exits 1, prints nothing on stdout (so no caller can mistake the failure for
// an empty list), and prints one Error line that names the operation, the
// deadline, that the timeout is only possible, and the settings to change.
func TestListReadTimeoutStderrContract(t *testing.T) {
	if usesProxiedServer() {
		t.Fatal("test process is in proxied-server mode; the direct list route is not reachable")
	}
	readErr := &storage.ReadTimeoutError{
		Timeout: 10 * time.Second,
		Err:     fmt.Errorf("search count issues: %w", mysql.ErrInvalidConn),
	}

	for _, mode := range []struct {
		name string
		json bool
	}{{"text", false}, {"json", true}} {
		t.Run(mode.name, func(t *testing.T) {
			oldStore, oldCtx, oldJSON := store, rootCtx, jsonOutput
			store, rootCtx, jsonOutput = readTimeoutListStore{listErr: readErr}, t.Context(), mode.json
			t.Cleanup(func() { store, rootCtx, jsonOutput = oldStore, oldCtx, oldJSON })

			cmd := newListFlagsCommand(t)
			stdout, stderr, err := captureListStdio(t, func() error { return runListCore(cmd, nil) })

			var exit *exitError
			if !errors.As(err, &exit) || exit.Code != 1 {
				t.Fatalf("runListCore error = %v, want exit code 1", err)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty: a failed read must not look like a (possibly empty) list", stdout)
			}
			var errorLines []string
			for _, line := range strings.Split(strings.TrimRight(stderr, "\n"), "\n") {
				if strings.HasPrefix(line, "Error: ") {
					errorLines = append(errorLines, line)
				}
			}
			if len(errorLines) != 1 {
				t.Fatalf("stderr has %d Error lines, want 1:\n%s", len(errorLines), stderr)
			}
			for _, want := range []string{
				"search count issues: invalid connection",
				"possible pool read timeout",
				"(configured 10s)",
				"read not retried",
				"BEADS_DOLT_POOL_READ_TIMEOUT",
				"dolt.pool-read-timeout",
			} {
				if !strings.Contains(errorLines[0], want) {
					t.Errorf("Error line %q lacks %q", errorLines[0], want)
				}
			}
		})
	}
}
