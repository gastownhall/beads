//go:build cgo

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// End-to-end lifecycle for `bd serve`, driven as a subprocess exactly as an
// operator runs it: bound address parsed off stdout, requests over real TCP,
// and a signal to shut it down.

// serveProcess is a running `bd serve` subprocess plus the two streams the
// contract makes promises about.
type serveProcess struct {
	cmd    *exec.Cmd
	addr   string
	stderr *syncBuffer
	client *http.Client

	waitOnce sync.Once
	waitErr  error
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startServe launches `bd serve` in dir and waits for the bound address it
// prints. Under the ephemeral default that line is the ONLY way to find the
// port, which is why it is stdout's first line and why this parses it rather
// than guessing.
//
// It takes the environment rather than a proxied project because the same
// lifecycle is the contract in every mode serve supports: the server-mode
// integration test drives this exact harness with a server-mode env.
func startServe(t *testing.T, bd, dir string, env []string, args ...string) *serveProcess {
	t.Helper()

	cmd := exec.Command(bd, append([]string{"serve", "--addr", "127.0.0.1:0"}, args...)...)
	cmd.Dir = dir
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start bd serve: %v", err)
	}

	sp := &serveProcess{
		cmd:    cmd,
		stderr: stderr,
		client: &http.Client{Timeout: 30 * time.Second},
	}
	t.Cleanup(func() {
		if sp.cmd.ProcessState == nil {
			_ = sp.cmd.Process.Kill()
			sp.wait()
		}
	})

	lines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			default:
			}
		}
		close(lines)
	}()

	select {
	case line, ok := <-lines:
		if !ok {
			t.Fatalf("bd serve exited before printing the bound address\nstderr:\n%s", stderr.String())
		}
		addr, found := strings.CutPrefix(line, "bd serve: listening on http://")
		if !found {
			t.Fatalf("first stdout line %q does not announce the bound address\nstderr:\n%s", line, stderr.String())
		}
		sp.addr = strings.TrimSpace(addr)
	case <-time.After(90 * time.Second):
		t.Fatalf("bd serve did not print a bound address\nstderr:\n%s", stderr.String())
	}

	return sp
}

func (sp *serveProcess) wait() error {
	sp.waitOnce.Do(func() { sp.waitErr = sp.cmd.Wait() })
	return sp.waitErr
}

func (sp *serveProcess) url(path string) string { return "http://" + sp.addr + path }

func (sp *serveProcess) get(t *testing.T, path string) (int, map[string]any, http.Header) {
	t.Helper()
	resp, err := sp.client.Get(sp.url(path))
	if err != nil {
		t.Fatalf("GET %s: %v\nstderr:\n%s", path, err, sp.stderr.String())
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("decode %s body %q: %v", path, body, err)
		}
	}
	return resp.StatusCode, m, resp.Header
}

// shutdown sends the signal an operator or a supervisor sends, and requires a
// clean drain and exit 0.
func (sp *serveProcess) shutdown(t *testing.T) {
	t.Helper()
	if err := sp.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- sp.wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("bd serve exited %v after SIGTERM, want a clean exit\nstderr:\n%s", err, sp.stderr.String())
		}
	case <-time.After(60 * time.Second):
		_ = sp.cmd.Process.Kill()
		t.Fatalf("bd serve did not exit after SIGTERM\nstderr:\n%s", sp.stderr.String())
	}
}

func TestProxiedServerServeLifecycle(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "srvl")

	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))

	// Startup lines: what the operator needs before the first request, and the
	// only record of which workspace this process is serving.
	startup := sp.awaitLogLine(t, "event=startup")
	for _, want := range []string{"addr=" + sp.addr, "mode=", "workspace=", "beads_dir="} {
		if !strings.Contains(startup, want) {
			t.Errorf("startup line is missing %q:\n%s", want, startup)
		}
	}
	limits := sp.awaitLogLine(t, "event=limits")
	for _, want := range []string{"max_inflight=", "max_conns=", "sem_wait=", "deadline=", "pool_max_open="} {
		if !strings.Contains(limits, want) {
			t.Errorf("limits line is missing %q:\n%s", want, limits)
		}
	}

	t.Run("healthz", func(t *testing.T) {
		status, body, header := sp.get(t, "/healthz")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if body["status"] != "ok" {
			t.Errorf("body = %v", body)
		}
		if got := header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
	})

	t.Run("context", func(t *testing.T) {
		status, body, _ := sp.get(t, "/v0/beads/context")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if body["api_version"] != "v0" {
			t.Errorf("api_version = %v, want v0", body["api_version"])
		}
		if body["dolt_mode"] != "proxied-server" {
			t.Errorf("dolt_mode = %v, want proxied-server", body["dolt_mode"])
		}
		if body["database"] != p.database {
			t.Errorf("database = %v, want %q", body["database"], p.database)
		}
		if body["bd_version"] == "" || body["bd_version"] == nil {
			t.Error("bd_version is empty")
		}
		// The handshake advertises exactly what this build implements. A client
		// that gates on capabilities is then correct without knowing which
		// release it hit. With the read endpoints landed that is the whole v0
		// vocabulary; the assertion is on the derived list, not on a count, so
		// the next operation to arrive stubbed still fails here.
		caps, ok := body["capabilities"].([]any)
		if !ok {
			t.Fatalf("capabilities = %#v, want an array", body["capabilities"])
		}
		want := []any{"issues.claim", "issues.get", "issues.list", "ready.list"}
		if !reflect.DeepEqual(caps, want) {
			t.Errorf("capabilities = %v, want %v", caps, want)
		}
		// The allowlist is enforced in internal/httpapi; this is the end-to-end
		// half — a real workspace's real config, over a real socket.
		for _, forbidden := range []string{"sync_remote", "server_host", "server_port", "data_dir", "proxied_dir", "role"} {
			if _, present := body[forbidden]; present {
				t.Errorf("context body carries %q: %v", forbidden, body)
			}
		}
	})

	t.Run("a read operation answers from the database", func(t *testing.T) {
		status, body, header := sp.get(t, "/v0/beads/ready")
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %v", status, body)
		}
		if got := header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Errorf("Content-Type = %q, want json", got)
		}
		if _, ok := body["items"].([]any); !ok {
			t.Errorf("items = %#v, want an array (never null)", body["items"])
		}
		if _, ok := body["has_more"].(bool); !ok {
			t.Errorf("has_more = %#v, want a boolean", body["has_more"])
		}
	})

	t.Run("foreign Host is refused", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, sp.url("/healthz"), nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Host = "evil.example"
		resp, err := sp.client.Do(req)
		if err != nil {
			t.Fatalf("GET with foreign Host: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (DNS-rebinding defense)", resp.StatusCode)
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["code"] != "invalid_argument" || body["param"] != "Host" {
			t.Errorf("body = %v, want invalid_argument on param Host", body)
		}
		// The rebinding defense has to leave a trace naming what it refused,
		// or an actual probe is invisible server-side.
		sp.awaitLogLine(t, "refused=evil.example")
	})

	t.Run("unrouted paths keep the error shape", func(t *testing.T) {
		status, body, header := sp.get(t, "/v0/beads/nope")
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}
		if got := header.Get("Content-Type"); !strings.HasPrefix(got, "application/problem+json") {
			t.Errorf("Content-Type = %q, want problem+json", got)
		}
		if body["code"] != "not_found" {
			t.Errorf("code = %v", body["code"])
		}
	})

	// One line per request, with the fields that separate "the database is
	// slow" from "the server is saturated" from "the handler is broken".
	requests := []struct{ path, op, status string }{
		{"/healthz", "health", "200"},
		{"/v0/beads/context", "getContext", "200"},
		{"/v0/beads/ready", "listReadyWork", "200"},
	}
	for _, want := range requests {
		line := sp.awaitLogLine(t, "path="+want.path+" ")
		for _, field := range []string{
			"request_id=", "op=" + want.op, "method=GET", "status=" + want.status,
			"duration_ms=", "sem_wait_ms=", "uow_ms=",
			// The connection gauge and the caller. The cap is enforced by the
			// listener, so this line is where an operator watches it climb;
			// on loopback the remote port names the calling process.
			"conns=", "remote_addr=127.0.0.1:",
		} {
			if !strings.Contains(line, field) {
				t.Errorf("request log line for %s is missing %q:\n%s", want.path, field, line)
			}
		}
	}

	sp.shutdown(t)

	// Wait has returned, so the stream copiers are done: read directly.
	stderr := sp.stderr.String()
	for _, want := range []string{"event=shutdown_start", "event=shutdown_complete"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("shutdown log is missing %q:\n%s", want, stderr)
		}
	}
	// The listener is released, which is what makes "the TCP bind is the mutual
	// exclusion" true rather than aspirational.
	if _, err := http.Get(sp.url("/healthz")); err == nil {
		t.Error("server still answering after shutdown")
	}
}

// TestProxiedServerServeModeGate covers the refusals an operator can actually
// hit from the command line.
//
// The dolt-server-mode staged refusal is NOT driven end to end here: this
// harness provisions proxied and embedded workspaces, not a `bd init --server`
// one, and building that topology for a message assertion would trade a lot of
// setup for no extra coverage. The typed error and its wording — including that
// it does not read as permanent — are pinned in TestServeRefusalsPromiseNothing
// (cmd/bd/serve_test.go), which is pure and runs in the PR workflow's
// unconditional Go test job.
func TestProxiedServerServeModeGate(t *testing.T) {
	requireProxiedServerEnv(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("embedded workspace is refused permanently", func(t *testing.T) {
		t.Parallel()
		dir, _, _ := bdInit(t, bd)

		cmd := exec.Command(bd, "serve")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err == nil {
			t.Fatalf("bd serve succeeded in an embedded workspace\nstdout:\n%s", stdout.String())
		}
		assertServeExitCode(t, err, 1)

		out := stderr.String()
		if !strings.Contains(out, "not supported by the embedded-dolt backend") {
			t.Errorf("refusal is not the typed ErrUnsupported message:\n%s", out)
		}
		if !strings.Contains(out, "embedded Dolt") {
			t.Errorf("refusal does not name the workspace's backend:\n%s", out)
		}
	})

	t.Run("non-loopback bind needs the opt-in", func(t *testing.T) {
		t.Parallel()
		dir, _, _ := bdInit(t, bd)

		cmd := exec.Command(bd, "serve", "--addr", "10.0.0.5:8080")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		_, stderr, err := runCommandBuffers(t, cmd)
		if err == nil {
			t.Fatal("bd serve accepted a non-loopback address without --allow-non-loopback")
		}
		assertServeExitCode(t, err, 1)
		// Checked before anything about the workspace, so the refusal is the
		// same in every mode — including this embedded one, which would
		// otherwise be refused first for a different reason.
		if !strings.Contains(stderr.String(), "--allow-non-loopback") {
			t.Errorf("refusal does not name the opt-in flag:\n%s", stderr.String())
		}
	})

	t.Run("a hostname is not an address", func(t *testing.T) {
		t.Parallel()
		dir, _, _ := bdInit(t, bd)

		cmd := exec.Command(bd, "serve", "--addr", "localhost:0")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		_, stderr, err := runCommandBuffers(t, cmd)
		if err == nil {
			t.Fatal("bd serve accepted a hostname as --addr")
		}
		assertServeExitCode(t, err, 1)
		if !strings.Contains(stderr.String(), "numeric IP literal") {
			t.Errorf("refusal does not explain the numeric-IP rule:\n%s", stderr.String())
		}
	})
}

// assertServeExitCode reads a SUBPROCESS exit status. The package's
// assertExitCode unwraps the in-process *exitError instead, which a forked bd
// never produces.
func assertServeExitCode(t *testing.T, err error, want int) {
	t.Helper()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("error %v is not a subprocess exit status", err)
	}
	if got := exit.ExitCode(); got != want {
		t.Errorf("exit code = %d, want %d", got, want)
	}
}

// awaitLogLine returns the first stderr line containing needle, waiting for it
// to arrive. os/exec copies the subprocess's stderr on its own goroutine, so a
// line the server has already written is not necessarily one this process has
// already read — polling is the difference between a test and a flaky test.
func (sp *serveProcess) awaitLogLine(t *testing.T, needle string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		log := sp.stderr.String()
		for _, line := range strings.Split(log, "\n") {
			if strings.Contains(line, needle) {
				return line
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no log line containing %q in:\n%s", needle, log)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
