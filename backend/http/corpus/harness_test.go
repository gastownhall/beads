//go:build cgo

package corpus_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

const (
	corpusProjectID = "proj-corpus-s23"
	corpusDatabase  = "corpus"
	corpusPrefix    = "e2e"
	// corpusToken is the bearer the in-process server accepts. The reference
	// deployment authenticates every request, so the suite does too.
	// #nosec G101 -- a throwaway test credential for an httptest-scoped server.
	corpusToken = "corpus-s23-test-token"
	// bdTimeout bounds one bd subprocess so a hung shape fails rather than
	// stalling the whole suite.
	bdTimeout = 90 * time.Second
)

// startCorpusServer binds the in-process server the way the reference
// deployment is bound: AllowNonLoopback with a bearer token file. The socket is
// still 127.0.0.1 (this suite never leaves the host), but every behavior the
// server keys off its bind mode — the loopback-only refusal of an unlimited
// read above all — is the non-loopback one, because that is decided by
// Config.AllowNonLoopback and not by the address.
func startCorpusServer(t *testing.T) string {
	t.Helper()
	beadsDir := t.TempDir()
	ctx := context.Background()
	store, err := embeddeddolt.Open(ctx, beadsDir, corpusDatabase, "main")
	if err != nil {
		t.Fatalf("open the served store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", corpusPrefix); err != nil {
		t.Fatalf("set the issue prefix: %v", err)
	}

	tokenFile := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(tokenFile, []byte(corpusToken+"\n"), 0o600); err != nil {
		t.Fatalf("write the token file: %v", err)
	}
	auth, err := httpapi.NewTokenFileAuth(tokenFile)
	if err != nil {
		t.Fatalf("load the token file: %v", err)
	}

	cfg := corpusServeConfig(t, store)
	cfg.AllowNonLoopback = true
	cfg.Auth = auth
	srv, err := httpapi.Listen(cfg)
	if err != nil {
		t.Fatalf("bind the in-process server: %v", err)
	}
	serveCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(serveCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("the in-process server did not shut down")
		}
	})
	return srv.Addr()
}

// startProxy serves h on an ephemeral loopback port.
func startProxy(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the reference proxy: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// corpusServeConfig takes every role off the store, as bd serve does. It
// mirrors backend/http's e2eServeConfig (a sibling test package, so it cannot
// be imported).
func corpusServeConfig(t *testing.T, s storage.DoltStorage) httpapi.Config {
	t.Helper()
	cfg := httpapi.Config{
		Addr:      "127.0.0.1:0",
		Stdout:    io.Discard,
		Stderr:    io.Discard,
		Workspace: domain.ContextInfo{ProjectID: corpusProjectID, Database: corpusDatabase},
	}
	var err error
	must := func(name string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s(): %v", name, err)
		}
	}
	cfg.Reader, err = s.IssueReader()
	must("IssueReader")
	cfg.Claimer, err = s.IssueClaimer()
	must("IssueClaimer")
	cfg.ReadyClaimer, err = s.ReadyClaimer()
	must("ReadyClaimer")
	cfg.Releaser, err = s.Releaser()
	must("Releaser")
	cfg.Lifecycle, err = s.IssueLifecycle()
	must("IssueLifecycle")
	cfg.Settings, err = s.WorkspaceConfig()
	must("WorkspaceConfig")
	cfg.Stats, err = s.StatsReporter()
	must("StatsReporter")
	cfg.CycleDetector, err = s.CycleDetector()
	must("CycleDetector")
	cfg.EdgeReader, err = s.EdgeReader()
	must("EdgeReader")
	cfg.GraphCounter, err = s.GraphCounter()
	must("GraphCounter")
	cfg.BatchGetter, err = s.BatchGetter()
	must("BatchGetter")
	cfg.Relations, err = s.IssueRelations()
	must("IssueRelations")
	cfg.Commenter, err = s.Commenter()
	must("Commenter")
	cfg.BlockingAnnotator, err = s.BlockingAnnotator()
	must("BlockingAnnotator")
	cfg.TreeWalker, err = s.TreeWalker()
	must("TreeWalker")
	cfg.ReadyCounter, err = s.ReadyCounter()
	must("ReadyCounter")
	cfg.Counter, err = s.Counter()
	must("Counter")
	cfg.Querier, err = s.Querier()
	must("Querier")
	cfg.Sweeper, err = s.Sweeper()
	must("Sweeper")
	cfg.Deleter, err = s.Deleter()
	must("Deleter")
	cfg.BatchCreator, err = s.BatchCreator()
	must("BatchCreator")
	cfg.BatchCloser, err = s.BatchCloser()
	must("BatchCloser")
	cfg.DependencyEditor, err = s.DependencyEditor()
	must("DependencyEditor")
	cfg.MetadataCAS, err = s.MetadataCAS()
	must("MetadataCAS")
	cfg.BatchApplier, err = s.BatchApplier()
	must("BatchApplier")
	cfg.Memories, err = s.Memories()
	must("Memories")
	return cfg
}

// skipUnlessEmbeddedDolt is the served tier's gate, as in backend/http.
func skipUnlessEmbeddedDolt(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") == "1" {
		return
	}
	if os.Getenv("BEADS_HTTP_TEST_REQUIRED") == "1" {
		t.Fatal("BEADS_HTTP_TEST_REQUIRED=1 but BEADS_TEST_EMBEDDED_DOLT is not 1; the http corpus tier is not enforced")
	}
	t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run the http command-corpus tier")
}

type bdResult struct {
	stdout, stderr string
	code           int
}

func (r bdResult) String() string {
	return fmt.Sprintf("exit=%d\nstdout:\n%s\nstderr:\n%s", r.code, clip(r.stdout, 4000), clip(r.stderr, 4000))
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n... (%d more bytes)", len(s)-n)
}

// runBD runs the built bd binary in dir with a hermetic environment.
func runBD(t *testing.T, bin, dir string, env []string, stdin string, args ...string) bdResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), bdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = minimalBDEnv(dir, env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var o, e strings.Builder
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		switch {
		case ctx.Err() != nil:
			code = -1
			e.WriteString(fmt.Sprintf("\n[corpus harness] bd timed out after %s", bdTimeout))
		case errors.As(err, &ee):
			code = ee.ExitCode()
		default:
			t.Fatalf("running bd %s: %v", strings.Join(args, " "), err)
		}
	}
	return bdResult{stdout: o.String(), stderr: e.String(), code: code}
}

// minimalBDEnv is backend/http's hermetic bd environment: PATH and the few
// variables a Go binary needs, HOME repointed at dir, nothing BEADS_* carried
// over from the invoking shell. extra is applied last.
func minimalBDEnv(dir string, extra ...string) []string {
	env := []string{
		"HOME=" + dir,
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
	}
	for _, key := range []string{"PATH", "TMPDIR", "SYSTEMROOT", "WINDIR"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return append(env, extra...)
}

var (
	buildBDOnce   sync.Once
	bdBinPath     string
	bdBuildErr    error
	bdBinOwnBuild bool
)

// buildBD builds bd from this tree once per test process (or takes Bazel's
// prebuilt binary). TestMain deletes the build.
func buildBD(t *testing.T) string {
	t.Helper()
	buildBDOnce.Do(func() {
		if bazeltest.IsBazel() {
			bdBinPath, bdBuildErr = bazeltest.PrebuiltBD()
			return
		}
		if prebuilt, err := bazeltest.PrebuiltBD(); err != nil || prebuilt != "" {
			bdBinPath, bdBuildErr = prebuilt, err
			return
		}
		dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "bd-corpus")
		if err != nil {
			bdBuildErr = fmt.Errorf("mkdir temp for bd binary: %w", err)
			return
		}
		bin := filepath.Join(dir, "bd-corpus")
		cmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", bin, "./cmd/bd")
		cmd.Dir = repoRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			bdBuildErr = fmt.Errorf("build bd: %w\n%s", err, out)
			_ = os.RemoveAll(dir)
			return
		}
		bdBinPath = bin
		bdBinOwnBuild = true
	})
	if bdBuildErr != nil {
		t.Fatal(bdBuildErr)
	}
	if bdBinPath == "" {
		t.Fatal("no bd binary: Bazel did not provide one")
	}
	return bdBinPath
}

func TestMain(m *testing.M) {
	code := m.Run()
	if bdBinOwnBuild && bdBinPath != "" {
		_ = os.RemoveAll(filepath.Dir(bdBinPath))
	}
	os.Exit(code)
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0) // <repo>/backend/http/corpus/harness_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
}
