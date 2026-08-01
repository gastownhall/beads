//go:build cgo && linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/procid"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

const managedLocalIdentityIdleTimeout = 5 * time.Minute

func TestManagedLocalProxiedForeignListenerNotAdopted(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p, oldProxy, _ := initStoppedManagedLocal(t, bd, "mlforeign")

	listener := startManagedForeignListener(t, oldProxy.Port)
	wrongBirth, err := procid.Capture(os.Getpid())
	if err != nil {
		t.Fatalf("capture test-process birth token: %v", err)
	}
	doctored := *oldProxy
	doctored.Birth = string(wrongBirth)
	if err := pidfile.Write(p.proxyRoot, proxy.PIDFileName, doctored); err != nil {
		t.Fatalf("write doctored proxy record: %v", err)
	}

	if out, err := bdProxiedRun(t, bd, p.dir, "list", "--json"); err != nil {
		t.Fatalf("bd list did not self-heal a foreign listener: %v\n%s", err, out)
	}
	newProxy, newBackend := waitForManagedProxiedHealthy(t, p)
	if newProxy.Port == oldProxy.Port {
		t.Fatalf("replacement proxy reused foreign listener port %d", oldProxy.Port)
	}
	assertManagedPidfilesConsistent(t, p, newProxy, newBackend)
	assertManagedForeignListenerReachable(t, listener)
	assertManagedQuarantineCount(t, p.proxyRoot, proxy.PIDFileName, 1)
}

func TestManagedLocalProxiedReusedPidNeverKilled(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p, oldProxy, _ := initStoppedManagedLocal(t, bd, "mlreused")
	helper := startManagedSleepProcess(t)

	doctored := *oldProxy
	doctored.Pid = helper.cmd.Process.Pid
	// oldProxy.Birth is a valid token from the same boot, but it cannot
	// match the newly spawned helper. This models numeric PID reuse without
	// weakening the record into a syntactically invalid token.
	if err := pidfile.Write(p.proxyRoot, proxy.PIDFileName, doctored); err != nil {
		t.Fatalf("write reused-pid proxy record: %v", err)
	}

	if out, err := bdProxiedRun(t, bd, p.dir, "list", "--json"); err != nil {
		t.Fatalf("bd list did not self-heal reused PID record: %v\n%s", err, out)
	}
	newProxy, newBackend := waitForManagedProxiedHealthy(t, p)
	assertManagedPidfilesConsistent(t, p, newProxy, newBackend)
	if !processAlive(helper.cmd.Process.Pid) {
		t.Fatalf("unrelated helper pid %d was killed", helper.cmd.Process.Pid)
	}
	assertManagedQuarantineCount(t, p.proxyRoot, proxy.PIDFileName, 1)
}

func TestManagedLocalProxiedLegacyRecordFailClosed(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)

	t.Run("free lock quarantines without signaling", func(t *testing.T) {
		p, oldProxy, _ := initStoppedManagedLocal(t, bd, "mllegacyfree")
		helper := startManagedSleepProcess(t)
		writeManagedLegacyProxyRecord(t, p, helper.cmd.Process.Pid, oldProxy.Port)

		if out, err := bdProxiedRun(t, bd, p.dir, "list", "--json"); err != nil {
			t.Fatalf("bd list did not replace free-lock legacy record: %v\n%s", err, out)
		}
		waitForManagedProxiedHealthy(t, p)
		if !processAlive(helper.cmd.Process.Pid) {
			t.Fatalf("legacy-record helper pid %d was killed", helper.cmd.Process.Pid)
		}
		assertManagedQuarantineCount(t, p.proxyRoot, proxy.PIDFileName, 1)
	})

	t.Run("held lock refuses foreign executable", func(t *testing.T) {
		p, oldProxy, _ := initStoppedManagedLocal(t, bd, "mllegacyforeign")
		helper := startManagedSleepProcessHoldingLock(t, p)
		writeManagedLegacyProxyRecord(t, p, helper.cmd.Process.Pid, oldProxy.Port)

		listOut := bdProxiedListFail(t, bd, p)
		assertManagedActionableLegacyError(t, listOut, p)

		_, stopStderr, stopErr := bdProxiedRunBuffers(t, bd, p.dir, "dolt", "stop")
		if stopErr == nil {
			t.Fatalf("bd dolt stop unexpectedly accepted held legacy record:\n%s", stopStderr)
		}
		assertManagedActionableLegacyError(t, stopStderr, p)

		forceStdout, forceStderr, forceErr := bdProxiedRunBuffers(
			t, bd, p.dir, "dolt", "stop", "--force", "--json",
		)
		if forceErr == nil {
			t.Fatalf("bd dolt stop --force signaled a foreign executable:\n%s", forceStdout)
		}
		result := decodeManagedDoltStopResult(t, forceStdout)
		if got := result["process_left_alone"]; got != true {
			t.Errorf("process_left_alone = %v, want true; result=%v", got, result)
		}
		if got := result["record_left_alone"]; got != true {
			t.Errorf("record_left_alone = %v, want true; result=%v", got, result)
		}
		if !strings.Contains(fmt.Sprint(result["error"]), "want bd or dolt") {
			t.Errorf("force error does not explain executable refusal: %v", result)
		}
		if strings.TrimSpace(forceStderr) != "" {
			t.Errorf("--json force refusal wrote non-JSON stderr:\n%s", forceStderr)
		}
		if !processAlive(helper.cmd.Process.Pid) {
			t.Fatalf("foreign helper pid %d was killed by --force", helper.cmd.Process.Pid)
		}
		if _, err := os.Stat(pidfile.Path(p.proxyRoot, proxy.PIDFileName)); err != nil {
			t.Fatalf("legacy record was not left in place after refusal: %v", err)
		}

		stopManagedProcess(t, helper)
		if err := pidfile.Remove(p.proxyRoot, proxy.PIDFileName); err != nil {
			t.Fatalf("remove negative-case legacy record: %v", err)
		}
	})

	t.Run("held lock accepts copied bd executable", func(t *testing.T) {
		p, oldProxy, _ := initStoppedManagedLocal(t, bd, "mllegacybd")
		helper := startCopiedBDManagedHelper(t, bd, p)
		writeManagedLegacyProxyRecord(t, p, helper.cmd.Process.Pid, oldProxy.Port)

		_, stopStderr, stopErr := bdProxiedRunBuffers(t, bd, p.dir, "dolt", "stop")
		if stopErr == nil {
			t.Fatalf("bd dolt stop unexpectedly accepted held legacy record:\n%s", stopStderr)
		}
		assertManagedActionableLegacyError(t, stopStderr, p)

		forceStdout, forceStderr, forceErr := bdProxiedRunBuffers(
			t, bd, p.dir, "dolt", "stop", "--force", "--json",
		)
		if forceErr != nil {
			t.Fatalf("bd dolt stop --force rejected copied bd helper: %v\nstdout:\n%s\nstderr:\n%s",
				forceErr, forceStdout, forceStderr)
		}
		result := decodeManagedDoltStopResult(t, forceStdout)
		for field, want := range map[string]any{
			"stopped":             true,
			"force":               true,
			"executable":          "bd",
			"executable_verified": true,
			"signal_sent":         true,
		} {
			if got := result[field]; got != want {
				t.Errorf("%s = %v, want %v; result=%v", field, got, want, result)
			}
		}
		if fmt.Sprint(result["quarantined_path"]) == "" {
			t.Errorf("force result omitted quarantined_path: %v", result)
		}
		if processAlive(helper.cmd.Process.Pid) {
			t.Fatalf("copied bd helper pid %d survived successful force stop", helper.cmd.Process.Pid)
		}
		if _, err := os.Stat(pidfile.Path(p.proxyRoot, proxy.PIDFileName)); !os.IsNotExist(err) {
			t.Fatalf("legacy record remains after successful force stop: %v", err)
		}
		assertManagedQuarantineCount(t, p.proxyRoot, proxy.PIDFileName, 1)
	})
}

func TestManagedLocalProxiedOrphanBackendReaped(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "mlorphan", managedLocalIdentityIdleTimeout)
	oldProxy, oldBackend := waitForManagedProxiedHealthy(t, p)

	proc, err := os.FindProcess(oldProxy.Pid)
	if err != nil {
		t.Fatalf("find proxy pid %d: %v", oldProxy.Pid, err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("SIGKILL proxy pid %d: %v", oldProxy.Pid, err)
	}
	waitForManagedProxied(t, 10*time.Second, "SIGKILLed proxy exit", func() (bool, string) {
		return !processAlive(oldProxy.Pid), fmt.Sprintf("proxy pid %d alive", oldProxy.Pid)
	})
	if !processAlive(oldBackend.Pid) {
		t.Fatalf("backend pid %d did not survive proxy SIGKILL; test did not create an orphan", oldBackend.Pid)
	}

	if out, err := bdProxiedRun(t, bd, p.dir, "list", "--json"); err != nil {
		t.Fatalf("bd list did not recover orphan backend: %v\n%s", err, out)
	}
	newProxy, newBackend := waitForManagedProxiedHealthy(t, p)
	if processAlive(oldBackend.Pid) {
		t.Fatalf("verified orphan backend pid %d survived recovery", oldBackend.Pid)
	}
	if newBackend.Pid == oldBackend.Pid {
		t.Fatalf("replacement backend reused orphan pid %d", oldBackend.Pid)
	}
	assertManagedPidfilesConsistent(t, p, newProxy, newBackend)
	assertManagedProcessCounts(t, p, 1, 1)
	assertManagedQuarantineCount(t, p.proxyRoot, server.PIDFileName, 1)
}

func TestManagedLocalProxiedNProcessColdStartConvergence(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p, _, _ := initStoppedManagedLocal(t, bd, "mlconverge")

	const workers = 5
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make([]managedCommandResult, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index] = runManagedCommand(ctx, bd, p.dir, "list", "--json")
		}(i)
	}
	close(start)
	wg.Wait()

	for i, result := range results {
		if result.err != nil {
			t.Errorf("worker %d failed: %v\nstdout:\n%s\nstderr:\n%s",
				i, result.err, result.stdout, result.stderr)
		}
	}
	if t.Failed() {
		return
	}
	proxyPF, backendPF := waitForManagedProxiedHealthy(t, p)
	assertManagedPidfilesConsistent(t, p, proxyPF, backendPF)
	waitForManagedProxied(t, 10*time.Second, "losing cold-start processes to exit", func() (bool, string) {
		proxies, backends := managedProcessPIDs(p)
		return len(proxies) == 1 && len(backends) == 1,
			fmt.Sprintf("proxy pids=%v backend pids=%v", proxies, backends)
	})
	assertManagedProcessCounts(t, p, 1, 1)
}

func TestManagedLocalProxiedStopStartInterleave(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p, _, _ := initStoppedManagedLocal(t, bd, "mlinterleave")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := make(chan struct{})
	var listResult, stopResult managedCommandResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		listResult = runManagedCommand(ctx, bd, p.dir, "list", "--json")
	}()
	go func() {
		defer wg.Done()
		<-start
		stopResult = runManagedCommand(ctx, bd, p.dir, "dolt", "stop")
	}()
	close(start)
	wg.Wait()

	if listResult.err != nil && stopResult.err != nil {
		t.Fatalf("both interleaved operations failed\nlist: %v\n%s\nstop: %v\n%s",
			listResult.err, listResult.stderr, stopResult.err, stopResult.stderr)
	}

	// The later successful completion defines the observable winner. When
	// stop completes last, no process it was responsible for stopping may
	// survive. When list completes last (or stop refuses), the started
	// topology must be healthy.
	wantRunning := listResult.err == nil &&
		(stopResult.err != nil || listResult.ended.After(stopResult.ended))
	if wantRunning {
		proxyPF, backendPF := waitForManagedProxiedHealthy(t, p)
		assertManagedPidfilesConsistent(t, p, proxyPF, backendPF)
		assertManagedProcessCounts(t, p, 1, 1)
		return
	}
	if stopResult.err != nil {
		t.Fatalf("stop was the logical winner but failed: %v\n%s", stopResult.err, stopResult.stderr)
	}
	waitForManagedProxied(t, 10*time.Second, "stop-winning interleave to leave no topology", func() (bool, string) {
		arts := snapshotManagedProxiedArtifacts(p)
		proxies, backends := managedProcessPIDs(p)
		done := !arts.ProxyPid && !arts.BackendPid && len(proxies) == 0 && len(backends) == 0
		return done, fmt.Sprintf("artifacts=%+v proxy pids=%v backend pids=%v", arts, proxies, backends)
	})
}

func initStoppedManagedLocal(
	t *testing.T,
	bd string,
	prefix string,
) (proxiedProject, *pidfile.PidFile, *pidfile.PidFile) {
	t.Helper()
	p := bdManagedLocalInit(t, bd, prefix, managedLocalIdentityIdleTimeout)
	proxyPF, backendPF := waitForManagedProxiedHealthy(t, p)
	if out, err := bdProxiedRun(t, bd, p.dir, "dolt", "stop"); err != nil {
		t.Fatalf("bd dolt stop: %v\n%s", err, out)
	}
	waitForManagedProxiedShutdown(t, p, proxyPF.Pid, backendPF.Pid, 60*time.Second)
	return p, proxyPF, backendPF
}

func waitForManagedProxiedHealthy(
	t *testing.T,
	p proxiedProject,
) (*pidfile.PidFile, *pidfile.PidFile) {
	t.Helper()
	var proxyPF, backendPF *pidfile.PidFile
	waitForManagedProxied(t, 60*time.Second, "healthy managed proxy and backend", func() (bool, string) {
		proxyPF = readManagedProxyPidFile(t, p)
		backendPF = readManagedBackendPidFile(t, p)
		if proxyPF != nil && backendPF != nil &&
			processAlive(proxyPF.Pid) && processAlive(backendPF.Pid) {
			return true, ""
		}
		return false, fmt.Sprintf("proxy=%+v backend=%+v", proxyPF, backendPF)
	})
	return proxyPF, backendPF
}

func assertManagedPidfilesConsistent(
	t *testing.T,
	p proxiedProject,
	proxyPF *pidfile.PidFile,
	backendPF *pidfile.PidFile,
) {
	t.Helper()
	if err := proxyPF.ValidateV2(pidfile.KindProxy); err != nil {
		t.Errorf("proxy pidfile is not valid v2: %+v: %v", proxyPF, err)
	}
	if err := backendPF.ValidateV2(pidfile.KindDoltBackend); err != nil {
		t.Errorf("backend pidfile is not valid v2: %+v: %v", backendPF, err)
	}
	if proxyPF.RootID == "" || proxyPF.RootID != backendPF.RootID {
		t.Errorf("pidfile root identities disagree: proxy=%q backend=%q",
			proxyPF.RootID, backendPF.RootID)
	}
	if proxyPF.ControlPort == 0 {
		t.Errorf("proxy pidfile has no control port: %+v", proxyPF)
	}
	assertExpectedLoopbackListener(t, proxyPF.Pid, proxyPF.Port, "managed proxy")
	assertExpectedLoopbackListener(t, backendPF.Pid, backendPF.Port, "managed backend")
	assertManagedConfigLoopback(t, p, backendPF.Port)
}

func startManagedForeignListener(t *testing.T, port int) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("occupy former proxy port %d: %v", port, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return listener
}

func assertManagedForeignListenerReachable(t *testing.T, listener net.Listener) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("foreign listener was disturbed: %v", err)
	}
	_ = conn.Close()
}

func writeManagedLegacyProxyRecord(t *testing.T, p proxiedProject, processPID, port int) {
	t.Helper()
	if err := pidfile.Write(p.proxyRoot, proxy.PIDFileName, pidfile.PidFile{
		Pid:  processPID,
		Port: port,
	}); err != nil {
		t.Fatalf("write legacy proxy record: %v", err)
	}
}

func assertManagedActionableLegacyError(t *testing.T, output string, p proxiedProject) {
	t.Helper()
	for _, want := range []string{
		"legacy",
		"old bd binary",
		pidfile.Path(p.proxyRoot, proxy.PIDFileName),
		".stale-<unix-timestamp>",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("legacy refusal missing %q:\n%s", want, output)
		}
	}
}

func assertManagedQuarantineCount(t *testing.T, root, name string, want int) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, name+".stale-*"))
	if err != nil {
		t.Fatalf("glob quarantines for %s: %v", name, err)
	}
	if len(matches) != want {
		t.Errorf("%s quarantine count = %d, want %d: %v", name, len(matches), want, matches)
	}
}

type managedProcess struct {
	cmd   *exec.Cmd
	token procid.Token
	done  <-chan error
}

func startManagedSleepProcess(t *testing.T) managedProcess {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("find sleep helper: %v", err)
	}
	return startManagedProcess(t, exec.Command(sleep, "300"))
}

func startManagedSleepProcessHoldingLock(t *testing.T, p proxiedProject) managedProcess {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("find sleep helper: %v", err)
	}
	return startManagedProcessHoldingProxyLock(t, p, exec.Command(sleep, "300"))
}

func startManagedProcessHoldingProxyLock(
	t *testing.T,
	p proxiedProject,
	cmd *exec.Cmd,
) managedProcess {
	t.Helper()
	lock, err := util.TryLock(filepath.Join(p.proxyRoot, proxy.LockFileName))
	if err != nil {
		t.Fatalf("hold proxy lock: %v", err)
	}
	parentFDClosed := false
	t.Cleanup(func() {
		if !parentFDClosed {
			_ = lock.File().Close()
		}
	})
	cmd.ExtraFiles = append(cmd.ExtraFiles, lock.File())
	helper := startManagedProcess(t, cmd)
	// Do not call Unlock: the child inherited this open file description and
	// now owns the flock. Closing only the parent's descriptor makes the lock
	// release automatically when the helper exits.
	if err := lock.File().Close(); err != nil {
		t.Fatalf("close parent proxy-lock descriptor: %v", err)
	}
	parentFDClosed = true
	return helper
}

func startManagedProcess(t *testing.T, cmd *exec.Cmd) managedProcess {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper %s: %v", cmd.Path, err)
	}
	token, err := procid.Capture(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("capture helper pid %d: %v", cmd.Process.Pid, err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
		close(done)
	}()
	helper := managedProcess{cmd: cmd, token: token, done: done}
	t.Cleanup(func() { stopManagedProcess(t, helper) })
	return helper
}

func stopManagedProcess(t *testing.T, helper managedProcess) {
	t.Helper()
	matched, err := procid.Verify(helper.cmd.Process.Pid, helper.token)
	if err == nil && matched {
		handle, openErr := procid.Open(helper.cmd.Process.Pid, helper.token)
		if openErr == nil {
			_ = handle.Kill()
			_ = handle.Close()
		}
	}
	select {
	case <-helper.done:
	case <-time.After(5 * time.Second):
		t.Errorf("helper pid %d did not exit", helper.cmd.Process.Pid)
	}
}

func startCopiedBDManagedHelper(t *testing.T, bd string, p proxiedProject) managedProcess {
	t.Helper()
	copyPath := filepath.Join(t.TempDir(), "bd")
	copyExecutable(t, bd, copyPath)

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for copied-bd upstream: %v", err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	go func() {
		for {
			conn, err := upstream.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	upstreamPort := upstream.Addr().(*net.TCPAddr).Port
	helperRoot := t.TempDir()
	// The helper isolates its records under helperRoot, but force-stop only
	// signals a process whose command line references the target workspace (a
	// real pre-upgrade proxy carries the workspace in its --root argument);
	// logging under p.proxyRoot models that scope without record collisions.
	cmd := exec.Command(
		copyPath,
		"db-proxy-child",
		"--root", helperRoot,
		"--port", "0",
		"--idle-timeout", "-1ns",
		"--backend", string(proxy.BackendExternal),
		"--logpath", filepath.Join(p.proxyRoot, "copied-bd-helper.log"),
		"--external-host", "127.0.0.1",
		"--external-port", strconv.Itoa(upstreamPort),
	)
	helper := startManagedProcessHoldingProxyLock(t, p, cmd)
	waitForManagedProxied(t, 10*time.Second, "copied bd helper to publish identity", func() (bool, string) {
		pf, readErr := pidfile.Read(helperRoot, proxy.PIDFileName)
		if readErr != nil {
			return false, readErr.Error()
		}
		return pf != nil && pf.Pid == helper.cmd.Process.Pid && processAlive(pf.Pid),
			fmt.Sprintf("pidfile=%+v helper alive=%v", pf, processAlive(helper.cmd.Process.Pid))
	})
	return helper
}

func copyExecutable(t *testing.T, source, target string) {
	t.Helper()
	in, err := os.Open(source) //nolint:gosec // source is the test-selected bd binary
	if err != nil {
		t.Fatalf("open helper source %s: %v", source, err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatalf("create helper copy %s: %v", target, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("copy helper binary: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close helper copy: %v", err)
	}
}

func decodeManagedDoltStopResult(t *testing.T, output string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &result); err != nil {
		t.Fatalf("decode bd dolt stop JSON: %v\n%s", err, output)
	}
	return result
}

type managedCommandResult struct {
	stdout string
	stderr string
	err    error
	ended  time.Time
}

func runManagedCommand(ctx context.Context, bd, dir string, args ...string) managedCommandResult {
	cmd := exec.CommandContext(ctx, bd, args...)
	cmd.Dir = dir
	cmd.Env = bdProxiedEnv(dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return managedCommandResult{
		stdout: stdout.String(),
		stderr: stderr.String(),
		err:    err,
		ended:  time.Now(),
	}
}

func managedProcessPIDs(p proxiedProject) (proxyPIDs, backendPIDs []int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, nil
	}
	configPath := proxiedServerConfigPath(p.beadsDir)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		cmdline := procCmdline(pid)
		switch {
		case strings.Contains(cmdline, "db-proxy-child") &&
			strings.Contains(cmdline, p.proxyRoot):
			proxyPIDs = append(proxyPIDs, pid)
		case strings.Contains(cmdline, "sql-server") &&
			strings.Contains(cmdline, configPath):
			backendPIDs = append(backendPIDs, pid)
		}
	}
	return proxyPIDs, backendPIDs
}

func assertManagedProcessCounts(t *testing.T, p proxiedProject, wantProxy, wantBackend int) {
	t.Helper()
	proxyPIDs, backendPIDs := managedProcessPIDs(p)
	if len(proxyPIDs) != wantProxy || len(backendPIDs) != wantBackend {
		t.Errorf("managed process counts: proxy=%d (%v), backend=%d (%v); want %d/%d",
			len(proxyPIDs), proxyPIDs, len(backendPIDs), backendPIDs, wantProxy, wantBackend)
	}
}
