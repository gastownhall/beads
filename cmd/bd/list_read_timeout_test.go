package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The list CLI contract for a pooled read timeout (GH#5258): the storage
// layer's typed ReadTimeoutError reaches the terminal as ONE application
// "Error: " line — exit code 1, no partial listing on stdout, and the
// operation context, the deadline, the "possible" qualifier and both override
// knobs all visible — in text AND --json mode. The typed error is terminal
// and not retried (see internal/storage/dolt/store.go), so what this pins is
// the last mile: nothing between issueops.Reader.List and the terminal may
// swallow the message, wrap it into a second line, or answer an empty
// listing as if the query had succeeded.
//
// The store stub carries exactly the surface runListCore's direct route
// touches before the reader: the list-config reads (custom statuses, custom
// types, infra types), the auto-routing config read (GetAllConfig, whose
// empty answer leaves routing off), and the reader accessor itself. Any
// other method panics on the nil embedded interface, which is the point —
// a new store call on this route should fail this test, not silently
// exercise the real workspace.
type listReadTimeoutStore struct {
	storage.DoltStorage
}

func (s *listReadTimeoutStore) GetCustomStatusesDetailed(context.Context) ([]types.CustomStatus, error) {
	return nil, nil
}

func (s *listReadTimeoutStore) GetCustomTypes(context.Context) ([]string, error) {
	return nil, nil
}

func (s *listReadTimeoutStore) GetInfraTypes(context.Context) map[string]bool {
	return nil
}

func (s *listReadTimeoutStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (s *listReadTimeoutStore) IssueReader() (issueops.Reader, error) {
	return &listReadTimeoutReader{}, nil
}

// listReadTimeoutReader fails List with the error the storage layer produces
// for a pooled read that died at the per-I/O read deadline: the operation
// context, then the driver's ErrInvalidConn cause, wrapped in the typed
// ReadTimeoutError. Ready and Get answer neutrally; the list route never
// calls them.
type listReadTimeoutReader struct{}

func (r *listReadTimeoutReader) Ready(context.Context, issueops.ReadyRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, nil
}

func (r *listReadTimeoutReader) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, fmt.Errorf(
		"read transaction: search issues with counts: wisp probe: %w",
		&storage.ReadTimeoutError{Timeout: 10 * time.Second, Err: mysql.ErrInvalidConn},
	)
}

func (r *listReadTimeoutReader) Get(context.Context, issueops.GetRequest) (*issueops.IssueDetails, error) {
	return nil, nil
}

// captureListStreams runs fn with os.Stdout and os.Stderr captured, under the
// package's stdio mutex like the other stream captures, and returns both
// streams plus fn's error.
func captureListStreams(t *testing.T, fn func() error) (stdout, stderr string, runErr error) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	outF, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create temp stdout file: %v", err)
	}
	defer func() { _ = outF.Close() }()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}

	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outF, errW
	runErr = fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = errW.Close()
	errBytes, _ := io.ReadAll(errR)
	_ = errR.Close()

	outBytes, readErr := os.ReadFile(outF.Name())
	if readErr != nil {
		t.Fatalf("read captured stdout: %v", readErr)
	}
	return string(outBytes), string(errBytes), runErr
}

func TestListReadTimeoutStderrContract(t *testing.T) {
	isolateBeadsDirForTest(t)

	for _, mode := range []struct {
		name string
		json bool
	}{
		{name: "text", json: false},
		{name: "json", json: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			oldStore, oldCtx, oldJSON := store, rootCtx, jsonOutput
			store, rootCtx, jsonOutput = &listReadTimeoutStore{}, context.Background(), mode.json
			t.Cleanup(func() { store, rootCtx, jsonOutput = oldStore, oldCtx, oldJSON })

			cmd := newListFlagsCommand(t)
			stdout, stderr, runErr := captureListStreams(t, func() error { return runListCore(cmd, nil) })

			var exit *exitError
			if !errors.As(runErr, &exit) || exit.Code != 1 {
				t.Fatalf("runListCore() error = %v, want *exitError with code 1", runErr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty: a failed listing is a failure, not a partial or empty result page", stdout)
			}
			lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
			if len(lines) != 1 || !strings.HasPrefix(lines[0], "Error: ") {
				t.Fatalf("stderr = %q, want exactly one application \"Error: \" line", stderr)
			}
			// The one line must convey the operation context it inherited,
			// the configured deadline, the inferred (not confirmed)
			// qualifier, the no-retry statement, and both override knobs.
			for _, want := range []string{
				"read transaction",
				"search issues with counts",
				"invalid connection",
				"possible pool read timeout",
				"(configured 10s)",
				"read not retried",
				"BEADS_DOLT_POOL_READ_TIMEOUT",
				"dolt.pool-read-timeout",
			} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr line missing %q:\n%s", want, stderr)
				}
			}
		})
	}
}
