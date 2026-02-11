//go:build cgo && integration

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInitCancel_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow E2E test in short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("skipping SIGINT E2E test on Windows")
	}

	tmpDir := createTempDirWithCleanup(t)
	runGitCmd(t, tmpDir, "init", "-b", "main")

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdin pipe: %v", err)
	}
	defer func() { _ = stdinW.Close() }()

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	defer func() { _ = stdoutR.Close() }()

	cmd := exec.Command(testBD, "init", "--prefix", "test", "--contributor")
	cmd.Dir = tmpDir
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stdoutW
	cmd.Env = append(filteredEnv("BEADS_DB", "BEADS_DIR", "BEADS_NO_DAEMON", "HOME", "XDG_CONFIG_HOME"),
		"BEADS_DB=",
		"BEADS_NO_DAEMON=1",
		"HOME="+tmpDir,
		"XDG_CONFIG_HOME="+filepath.Join(tmpDir, "xdg-config"),
	)

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start bd init: %v", err)
	}

	_ = stdinR.Close()
	_ = stdoutW.Close()

	prompts := [][]byte{
		[]byte("Continue with contributor setup? [y/N]: "),
		[]byte("Continue anyway? [y/N]: "),
	}
	promptSeen := make(chan struct{})
	readerDone := make(chan struct{})

	var output bytes.Buffer
	var outputMu sync.Mutex
	var promptOnce sync.Once

	go func() {
		defer close(readerDone)
		buf := make([]byte, 1024)
		for {
			n, err := stdoutR.Read(buf)
			if n > 0 {
				outputMu.Lock()
				output.Write(buf[:n])
				for _, prompt := range prompts {
					if bytes.Contains(output.Bytes(), prompt) {
						promptOnce.Do(func() { close(promptSeen) })
						break
					}
				}
				outputMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	getOutput := func() string {
		outputMu.Lock()
		defer outputMu.Unlock()
		return output.String()
	}

	select {
	case <-promptSeen:
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("failed to send SIGINT: %v", err)
		}
	case err := <-waitCh:
		t.Fatalf("bd init exited before prompt: %v\nOutput: %s", err, getOutput())
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		err := <-waitCh
		t.Fatalf("timeout waiting for prompt (exit=%v)\nOutput: %s", err, getOutput())
	}

	err = <-waitCh

	select {
	case <-readerDone:
	case <-time.After(2 * time.Second):
		t.Log("timeout waiting for output drain")
	}

	if err == nil {
		t.Fatalf("expected non-zero exit, got success\nOutput: %s", getOutput())
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("unexpected error type: %v\nOutput: %s", err, getOutput())
	}
	if exitErr.ExitCode() != exitCodeCanceled {
		t.Fatalf("expected exit code %d, got %d\nOutput: %s", exitCodeCanceled, exitErr.ExitCode(), getOutput())
	}
	if !strings.Contains(getOutput(), "Setup canceled.") {
		t.Fatalf("expected cancel message, got:\n%s", getOutput())
	}
}

func filteredEnv(keys ...string) []string {
	strip := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		strip[key+"="] = struct{}{}
	}

	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		trim := false
		for prefix := range strip {
			if strings.HasPrefix(entry, prefix) {
				trim = true
				break
			}
		}
		if !trim {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
